package expect

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

// maxNulls bounds SendNull.
const maxNulls = 1024

// Pacer returns the delay before the byte at offset; it runs while the
// session's write lock is held.
type Pacer func(offset int) time.Duration

// Breaker is the optional transport capability behind SendBreak: an SSH
// "break" request (RFC 4335), a Telnet BREAK or a serial line break.
type Breaker interface {
	Break(ctx context.Context, d time.Duration) error
}

// Resizer is the optional transport capability behind Session.Resize: a
// local terminal (spawn), or an SSH channel sending window-change.
type Resizer interface {
	SetSize(rows, cols int) error
}

// Resize tells the peer the window is now cols wide and rows high, so a
// device wraps lines and redraws long commands for the operator's
// terminal, and records the change in the transcript. It fails with
// ErrResizeUnsupported when the transport is not a Resizer.
func (s *Session) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return configError("window size", "must be positive")
	}
	r, ok := s.t.(Resizer)
	if !ok {
		return ErrResizeUnsupported
	}
	if s.closed.Load() {
		return ErrClosed
	}
	if err := r.SetSize(rows, cols); err != nil {
		return err
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	return s.tr.Resize(cols, rows)
}

type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

// Send writes text verbatim and returns the bytes the transport accepted.
// Sends are serialized. An error before the write leaves the session
// usable; an error after it closes the session and is a *WriteError.
func (s *Session) Send(ctx context.Context, text string) (int, error) {
	return s.send(ctx, []byte(text), nil)
}

// SendLine writes text followed by the line ending (WithLineEnding).
func (s *Session) SendLine(ctx context.Context, text string) (int, error) {
	return s.send(ctx, []byte(text+s.o.lineEnding), nil)
}

// SendSlow writes text in chunks of chunk bytes, waiting interval before
// each chunk after the first, for devices that drop characters typed
// quickly (console servers, old switches).
func (s *Session) SendSlow(ctx context.Context, text string, chunk int, interval time.Duration) (int, error) {
	if chunk <= 0 || interval < 0 {
		return 0, configError("slow send", "chunk must be positive and interval not negative")
	}
	return s.SendPaced(ctx, text, func(off int) time.Duration {
		if off > 0 && off%chunk == 0 {
			return interval
		}
		return 0
	})
}

// SendPaced writes text byte by byte, waiting pacer(offset) before each.
func (s *Session) SendPaced(ctx context.Context, text string, pacer Pacer) (int, error) {
	if pacer == nil {
		return 0, configError("pacer", "nil")
	}
	return s.send(ctx, []byte(text), pacer)
}

// SendNull writes count NUL bytes (1 to 1024), a keepalive some terminal
// servers need.
func (s *Session) SendNull(ctx context.Context, count int) (int, error) {
	if count < 1 || count > maxNulls {
		return 0, configError("null count", "out of range 1..1024")
	}
	return s.send(ctx, make([]byte, count), nil)
}

// SendBreak asks the transport to send a break of duration d. It fails
// with ErrBreakUnsupported when the transport is not a Breaker.
func (s *Session) SendBreak(ctx context.Context, d time.Duration) error {
	b, ok := s.t.(Breaker)
	if !ok {
		return ErrBreakUnsupported
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed.Load() {
		return ErrClosed
	}
	err := b.Break(ctx, d)
	s.emit(ctx, Event{Op: OpBreak, Duration: d, Err: classify(err)})
	return err
}

// send writes p, paced when pacer is set, under the write lock.
func (s *Session) send(ctx context.Context, p []byte, pacer Pacer) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed.Load() {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	wire, err := s.encodeText(p)
	if err != nil {
		return 0, err
	}
	// The transcript keeps the UTF-8 text, so it reads and redacts alike
	// whatever the device charset.
	if err := s.recordSent(p); err != nil {
		return 0, err
	}
	p = wire
	start := time.Now()
	var n int
	if pacer == nil {
		n, err = s.writeAll(ctx, p)
	} else {
		n, err = s.writePaced(ctx, p, pacer)
	}
	s.noteSent(n)
	s.emit(ctx, Event{Op: OpSend, Bytes: n, Duration: time.Since(start), Err: classify(err)})
	if err != nil {
		return n, err
	}
	return n, nil
}

func (s *Session) writePaced(ctx context.Context, p []byte, pacer Pacer) (int, error) {
	for i := range p {
		if d := pacer(i); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				if i == 0 {
					return 0, ctx.Err()
				}
				return i, s.retire(i, ctx.Err())
			case <-t.C:
			}
		}
		if _, err := s.writeAll(ctx, p[i:i+1]); err != nil {
			if we, ok := errors.AsType[*WriteError](err); ok {
				we.Written += i
			}
			return i, err
		}
	}
	return len(p), nil
}

// writeAll writes p to the transport, bounded by ctx and the write
// timeout. A transport with write deadlines is interrupted through them;
// any other transport is closed to interrupt it.
func (s *Session) writeAll(ctx context.Context, p []byte) (int, error) {
	deadline := time.Time{}
	if s.o.writeTimeout > 0 {
		deadline = time.Now().Add(s.o.writeTimeout)
	}
	if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	dl, ok := s.t.(writeDeadliner)
	if ok {
		if err := dl.SetWriteDeadline(deadline); err != nil {
			if !unsupportedDeadline(err) {
				return 0, s.retire(0, err)
			}
			ok = false
		}
	}
	interrupt := func() { _ = s.t.Close() }
	if ok {
		interrupt = func() { _ = dl.SetWriteDeadline(time.Unix(1, 0)) }
	}
	stop := context.AfterFunc(ctx, interrupt)
	var timer *time.Timer
	if !ok && !deadline.IsZero() {
		timer = time.AfterFunc(time.Until(deadline), interrupt)
	}
	n, err := s.t.Write(p)
	stop()
	if timer != nil {
		timer.Stop()
	}
	if err == nil && n < len(p) {
		err = errors.New("expect: short write")
	}
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return n, s.retire(n, s.closedErr(err))
	}
	if ok {
		// Clearing the deadline of a transport the peer already closed is
		// harmless: the write itself completed.
		if err := dl.SetWriteDeadline(time.Time{}); err != nil && !isClosed(err) {
			return n, s.retire(n, err)
		}
	}
	return n, nil
}

// retire closes the session after a failed admitted write. The close
// error stays available from Close; the caller gets the write error.
func (s *Session) retire(written int, cause error) error {
	we := &WriteError{Written: written, Cause: cause}
	s.end(we)
	// A write interrupted by Close must not call Close again: Close may be
	// waiting for this goroutine (the keepalive).
	if !s.closed.Load() {
		_ = s.Close()
	}
	return we
}

func isClosed(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}

func unsupportedDeadline(err error) bool {
	return errors.Is(err, os.ErrNoDeadline) || errors.Is(err, errors.ErrUnsupported)
}
