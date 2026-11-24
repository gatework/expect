package expect

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// closeStuckAfter is how long Close waits for the read pump before it
	// warns that the transport does not interrupt Read.
	closeStuckAfter = 5 * time.Second
	// keepaliveQuiet and keepaliveBound bound how long a keepalive waits
	// for the peer's answer before discarding it.
	keepaliveQuiet = 200 * time.Millisecond
	keepaliveBound = 5 * time.Second
)

// Stats is a snapshot of a session's activity, for pools that evict idle
// sessions and for troubleshooting.
type Stats struct {
	Opened       time.Time
	LastReceived time.Time // zero before any input
	LastSent     time.Time // zero before any send
	Received     int64     // bytes, after charset decoding
	Sent         int64     // bytes written to the transport
	Waits        int64     // completed waits of every receiver
	Failures     int64     // waits and sends that ended with an error
}

// Idle returns how long the session has neither received nor sent, as of
// now.
func (st Stats) Idle(now time.Time) time.Duration {
	last := st.Opened
	for _, t := range []time.Time{st.LastReceived, st.LastSent} {
		if t.After(last) {
			last = t
		}
	}
	return now.Sub(last)
}

// lifecycle is the end state and the counters of a session.
type lifecycle struct {
	opened   time.Time
	ended    chan struct{} // closed when the session can no longer receive
	endOnce  sync.Once
	cause    error // why it ended; written before ended closes
	lastRx   atomic.Int64
	lastTx   atomic.Int64
	rxBytes  atomic.Int64
	txBytes  atomic.Int64
	waits    atomic.Int64
	failures atomic.Int64
	bg       sync.WaitGroup // keepalive
	// keeping is closed when the running keepalive releases the input;
	// nil when none runs.
	keeping atomic.Pointer[chan struct{}]
}

func newLifecycle() lifecycle {
	return lifecycle{opened: time.Now(), ended: make(chan struct{})}
}

// Done is closed when the session ends: its input ended (the peer closed
// or a read failed), Close was called, or a failed write retired it,
// whichever came first. Input still pending can be matched afterwards. A
// pool can select on Done to notice a dropped session without starting a
// wait.
func (s *Session) Done() <-chan struct{} { return s.life.ended }

// Err returns nil while the session is live, then why it ended: the read
// error (io.EOF when the peer closed cleanly), ErrClosed after Close, or
// the *WriteError that retired it.
func (s *Session) Err() error {
	select {
	case <-s.life.ended:
		return s.life.cause
	default:
		return nil
	}
}

// Stats returns a snapshot of the session's activity.
func (s *Session) Stats() Stats {
	l := &s.life
	return Stats{
		Opened: l.opened, LastReceived: unixTime(l.lastRx.Load()), LastSent: unixTime(l.lastTx.Load()),
		Received: l.rxBytes.Load(), Sent: l.txBytes.Load(), Waits: l.waits.Load(), Failures: l.failures.Load(),
	}
}

// end records the first cause the session ended for.
func (s *Session) end(cause error) {
	s.life.endOnce.Do(func() {
		s.life.cause = cause
		close(s.life.ended)
	})
}

func (s *Session) noteReceived(n int) {
	if n > 0 {
		s.life.rxBytes.Add(int64(n))
		s.life.lastRx.Store(time.Now().UnixNano())
	}
}

func (s *Session) noteSent(n int) {
	if n > 0 {
		s.life.txBytes.Add(int64(n))
		s.life.lastTx.Store(time.Now().UnixNano())
	}
}

// count tallies a completed event for Stats.
func (s *Session) count(ev Event) {
	if ev.Op.wait() {
		s.life.waits.Add(1)
	}
	if ev.Err != ClassNone && (ev.Op.wait() || ev.Op == OpSend) {
		s.life.failures.Add(1)
	}
}

func unixTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// joinPump waits for the read pump after the transport was closed and
// warns once when the transport does not interrupt its Read, which would
// otherwise make Close hang without a trace.
func (s *Session) joinPump(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.pump.Wait()
		close(done)
	}()
	t := time.NewTimer(closeStuckAfter)
	defer t.Stop()
	select {
	case <-done:
		return
	case <-t.C:
		s.log.LogAttrs(ctx, slog.LevelWarn, "expect: transport read did not return after close",
			slog.Duration("waited", closeStuckAfter))
	}
	<-done
}

// logOpened and logClosed bracket a session in the log at Info, so one
// session's story can be told from two lines and its transcript.
func (s *Session) logOpened(ctx context.Context) {
	attrs := []slog.Attr{slog.Any("transcript", s.tr.Paths())}
	if s.o.charset != nil {
		attrs = append(attrs, slog.Bool("charset", true))
	}
	if s.o.terminal {
		attrs = append(attrs, slog.Bool("terminal_filter", true))
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "expect: session opened", attrs...)
}

func (s *Session) logClosed(ctx context.Context, closeErr error) {
	st := s.Stats()
	cause := s.Err()
	level := slog.LevelInfo
	if _, retired := errors.AsType[*WriteError](cause); retired {
		level = slog.LevelWarn
	}
	s.log.LogAttrs(ctx, level, "expect: session closed",
		slog.Duration("lifetime", time.Since(st.Opened)),
		slog.String("cause", string(classify(cause))),
		slog.Int64("bytes_in", st.Received), slog.Int64("bytes_out", st.Sent),
		slog.Int64("waits", st.Waits), slog.Int64("failures", st.Failures),
		slog.String("close_err", string(classify(closeErr))),
		slog.Any("transcript", s.tr.Paths()))
}

// keepalive sends the line ending whenever the session has been idle for
// interval and no receiver holds its input, so a device's exec-timeout
// does not drop a pooled session. It takes the input for the exchange and
// discards only what the peer sent after the keepalive, keeping input a
// caller has yet to read.
// ctx is the session scope, which ends at Close.
func (s *Session) keepalive(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.life.ended:
			return
		case <-t.C:
		}
		if s.Stats().Idle(time.Now()) < interval {
			continue
		}
		// Announce the keepalive before taking the input, so a receiver
		// that loses the race waits for it instead of failing.
		done := make(chan struct{})
		s.life.keeping.Store(&done)
		if !s.busy.CompareAndSwap(false, true) {
			s.life.keeping.Store(nil)
			close(done)
			continue // a receiver is active
		}
		l := &lease{s: s, wake: make(chan struct{}, 1), held: true}
		s.in.attach(l.wake)
		s.keepaliveOnce(ctx, l)
		s.life.keeping.Store(nil)
		l.release()
		close(done)
	}
}

// afterKeepalive waits for a running keepalive to release the input and
// takes it, so a receiver never fails with ErrBusy because of one. It
// reports false when the input is held by a real receiver.
func (s *Session) afterKeepalive() bool {
	for {
		ch := s.life.keeping.Load()
		if ch == nil {
			return false
		}
		select {
		case <-*ch:
		case <-s.done:
			return false
		}
		if s.busy.CompareAndSwap(false, true) {
			return true
		}
	}
}

func (s *Session) keepaliveOnce(ctx context.Context, l *lease) {
	ctx, cancel := context.WithTimeout(ctx, keepaliveBound)
	defer cancel()
	l.pull()
	keep := len(s.rx)
	if _, err := s.SendLine(ctx, ""); err != nil {
		return
	}
	o := matchOptions{timeout: keepaliveBound, idle: keepaliveQuiet}
	w := newWaiter(o, l.wake, newMember(l, nil, o))
	_, _, _ = w.run(ctx)
	dropped := len(s.rx) - keep
	if dropped > 0 {
		s.rx = s.rx[:keep]
		s.version++
	}
	s.log.LogAttrs(ctx, slog.LevelDebug, "expect: keepalive", slog.Int("discarded", max(dropped, 0)))
}
