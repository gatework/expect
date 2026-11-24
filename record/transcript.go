package record

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

const fileMode = 0o600

// Direction tells received bytes from sent bytes.
type Direction uint8

// Directions of recorded bytes.
const (
	DirectionReceived Direction = iota + 1
	DirectionSent
)

// Transcript is the set of files recording one session. It is safe for
// concurrent use. The zero value records nothing.
type Transcript struct {
	mu       sync.Mutex // guards the sinks and closed
	text     *textSink
	cast     *castSink
	events   slog.Handler
	closers  []io.Closer
	paths    []string
	closed   bool
	required bool
	logger   *slog.Logger
	now      func() time.Time
}

// Resize records a window size change, a cast "r" event; the other
// formats do not record it.
func (t *Transcript) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.cast == nil {
		return nil
	}
	if err := t.cast.event(t.now(), "r", []byte(fmt.Sprintf("%dx%d", cols, rows))); err != nil {
		t.cast = nil
		return t.fail(fmt.Errorf("record: cast: %w", err))
	}
	return nil
}

// Received records bytes read from the peer.
func (t *Transcript) Received(p []byte) error { return t.write(DirectionReceived, p) }

// Sent records bytes about to be written to the peer.
func (t *Transcript) Sent(p []byte) error { return t.write(DirectionSent, p) }

// Handler returns the slog handler of the jsonl file; it discards when
// jsonl is not recorded.
func (t *Transcript) Handler() slog.Handler {
	if t.events == nil {
		return slog.DiscardHandler
	}
	return t.events
}

// Paths returns the files of this transcript.
func (t *Transcript) Paths() []string { return slices.Clone(t.paths) }

// Close flushes and closes every file. It is idempotent.
func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	var errs []error
	if t.cast != nil {
		errs = append(errs, t.cast.finish(t.now()))
	}
	for _, c := range t.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

func (t *Transcript) write(d Direction, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	at := t.now()
	if t.text != nil {
		if err := t.text.write(d, p, at); err != nil {
			t.text = nil
			if ferr := t.fail(fmt.Errorf("record: log: %w", err)); ferr != nil {
				return ferr
			}
		}
	}
	if t.cast != nil {
		if err := t.cast.write(d, p, at); err != nil {
			t.cast = nil
			if ferr := t.fail(fmt.Errorf("record: cast: %w", err)); ferr != nil {
				return ferr
			}
		}
	}
	return nil
}

// fail returns err when recording is required; otherwise it logs err and
// returns nil.
func (t *Transcript) fail(err error) error {
	if t.required {
		return err
	}
	t.logger.Warn("record: transcript file dropped", "err", err)
	return nil
}

func (t *Transcript) open(f Format, path string, m Meta, start time.Time, maxMB int) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("record: %s: %w", f, err)
	}
	limit := int64(maxMB) << 20
	bf := newBufferedFile(file)
	switch f {
	case FormatLog:
		t.text = &textSink{w: &capped{w: bf, limit: limit, marker: "\n--- recording truncated: size limit ---\n"}}
	case FormatJSONL:
		w := &capped{w: bf, limit: limit, marker: `{"msg":"record: truncated: size limit"}` + "\n"}
		t.events = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
	case FormatCast:
		c, err := newCastSink(bf, m, start, limit)
		if err != nil {
			return errors.Join(fmt.Errorf("record: cast: %w", err), bf.Close())
		}
		t.cast = c
	}
	t.closers = append(t.closers, bf)
	t.paths = append(t.paths, path)
	return nil
}

// capped stops writing at limit bytes, after writing marker once. Writes
// past the limit report success so a long session is not failed by its
// own transcript.
type capped struct {
	mu     sync.Mutex // slog handlers may write concurrently
	w      io.Writer
	limit  int64
	size   int64
	marker string
	full   bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return len(p), nil
	}
	if c.limit > 0 && c.size+int64(len(p)) > c.limit {
		c.full = true
		_, err := io.WriteString(c.w, c.marker)
		return len(p), err
	}
	n, err := c.w.Write(p)
	c.size += int64(n)
	return n, err
}
