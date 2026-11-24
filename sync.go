package expect

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Drain discards input until the peer has been quiet for quiet, the
// equivalent of clearing the buffer before a command in Tcl Expect. It
// returns what it discarded in Before. Session rules still apply, so a
// pager in late output is answered. The relative timer (WithTimeout, or
// the session default) bounds a peer that never goes quiet: Drain then
// fails with ErrTimeout and keeps the input. The end of input also
// completes it, with the read error (io.EOF at a clean end).
func (s *Session) Drain(ctx context.Context, quiet time.Duration, opts ...MatchOption) (Result, error) {
	if quiet <= 0 {
		return failed(s.id, nil), configError("drain", "quiet must be positive")
	}
	o, err := s.prepare(nil, opts)
	if err != nil {
		return failed(s.id, nil), err
	}
	l, err := s.acquire(nil)
	if err != nil {
		return failed(s.id, nil), err
	}
	defer l.release()
	ctx, cancel := s.scoped(ctx)
	defer cancel()
	start := time.Now()
	o.idle = quiet
	w := newWaiter(o, l.wake, newMember(l, nil, o))
	m, r, err := w.run(ctx)
	if errors.Is(err, ErrIdleTimeout) || m != nil && m.l.ended() && !errors.Is(err, ErrTimeout) {
		r = m.pending()
		m.l.consume(len(s.rx))
		if errors.Is(err, ErrIdleTimeout) {
			err = nil
		}
	}
	if err != nil && m != nil {
		m.waitFailed(ctx, OpDrain, o, err)
	}
	s.emit(ctx, Event{Op: OpDrain, Bytes: len(r.Before), Duration: time.Since(start), Err: classify(err)})
	return r, err
}

// WithEcho makes Exchange synchronize on the echo of its line before
// matching cases: input pending before the send and input that arrives
// before the echo belong to earlier commands (late output, a prompt left
// by a timed-out command, its echo) and are discarded, not matched and not
// collected. The echo itself is not collected either. The echo may contain
// CR, LF or NUL between characters, as devices wrap long lines; enable
// WithTerminalFilter for devices that redraw it with control sequences.
// A device that does not echo fails the Exchange with ErrNoEcho; do not
// use WithEcho for lines a device does not echo, such as passwords.
func WithEcho() MatchOption {
	return func(o *matchOptions) error {
		o.echo = true
		return nil
	}
}

// echoPattern matches line as a device echoes it.
func echoPattern(line string) (Pattern, error) {
	var b strings.Builder
	for i, r := range line {
		if i > 0 {
			b.WriteString(`[\r\n\x00]*`)
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return Pattern{}, fmt.Errorf("%w: echo: %w", ErrInvalidPattern, err)
	}
	return FromRegexp(re)
}

// awaitEcho discards stale input, then waits for the echo of line, which
// was sent after the discard. It returns how many stale bytes it dropped.
func (s *Session) awaitEcho(ctx context.Context, l *lease, line string, o matchOptions) (int, error) {
	p, err := echoPattern(line)
	if err != nil {
		return 0, err
	}
	eo := o
	eo.output, eo.outLimit, eo.rules, eo.stream = false, 0, nil, nil
	w := newWaiter(eo, l.wake, newMember(l, []Case{{Name: "echo", Pattern: p}}, eo))
	_, r, err := w.run(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoEcho, err)
	}
	return len(r.Before), nil
}

// discard drops the input pending on a held lease and returns its size.
func (l *lease) discard() int {
	l.pull()
	n := len(l.s.rx)
	l.consume(n)
	return n
}
