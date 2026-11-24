package expect

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Background is a wait running on its own goroutine, for example to
// answer pagers or watch for asynchronous log messages while the caller
// does something else. Pause hands the input back to foreground receivers
// and Resume takes it again. A Background must not be copied.
type Background struct {
	s      *Session
	ctx    context.Context //nolint:containedctx // the wait's lifetime, cancelled by Stop
	ctl    chan func(*waiter)
	resume chan chan error
	cancel context.CancelFunc
	done   chan struct{}
	pmu    sync.Mutex // serializes Pause and Resume
	paused bool
	r      Result
	err    error
}

// StartBackground takes the receive slot now and runs the wait until a
// case returns, the wait fails, or Stop. Unlike Expect, the relative timer
// defaults to NoTimeout; WithTimeout sets one. A handler panic or Goexit
// ends the wait with a CallbackError.
func (s *Session) StartBackground(ctx context.Context, cases []Case, opts ...MatchOption) (*Background, error) {
	o, err := s.prepare(cases, opts)
	if err != nil {
		return nil, err
	}
	if !o.timeoutSet {
		o.timeout = NoTimeout
	}
	l, err := s.acquire(nil)
	if err != nil {
		return nil, err
	}
	ctx, stopScope := s.scoped(ctx)
	ctx, cancel := context.WithCancel(ctx)
	b := &Background{
		s: s, ctx: ctx, ctl: make(chan func(*waiter)), resume: make(chan chan error),
		cancel: func() { cancel(); stopScope() }, done: make(chan struct{}),
	}
	w := newWaiter(o, l.wake, newMember(l, cases, o))
	w.control = b.ctl
	go b.run(ctx, w)
	return b, nil
}

// Pause waits for the running handler to return, freezes the relative
// timers and releases the input. The parent context keeps running.
func (b *Background) Pause(ctx context.Context) error {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	if b.paused {
		return nil
	}
	ack := make(chan struct{})
	pause := func(w *waiter) {
		w.timer.pause()
		w.idle.pause()
		w.members[0].l.release()
		close(ack)
		b.waitResume(w)
	}
	select {
	case b.ctl <- pause:
	case <-b.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ack
	b.paused = true
	return nil
}

// Resume takes the input again. It returns ErrBusy while a foreground
// receiver holds it; the Background then stays paused.
func (b *Background) Resume(ctx context.Context) error {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	if !b.paused {
		return nil
	}
	reply := make(chan error, 1)
	select {
	case b.resume <- reply:
	case <-b.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := <-reply; err != nil {
		return err
	}
	b.paused = false
	return nil
}

// Stop cancels the wait and waits for it to end, bounded by ctx. It
// returns nil when the wait was stopped, or the wait's own error when it
// had already ended.
func (b *Background) Stop(ctx context.Context) error {
	b.cancel()
	select {
	case <-b.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if errors.Is(b.err, context.Canceled) {
		return nil
	}
	return b.err
}

// Wait waits for the wait to end, bounded by ctx, and returns its result.
func (b *Background) Wait(ctx context.Context) (Result, error) {
	select {
	case <-b.done:
		return b.r, b.err
	case <-ctx.Done():
		return failed(b.s.id, nil), ctx.Err()
	}
}

func (b *Background) run(ctx context.Context, w *waiter) {
	normal := false
	start := time.Now()
	defer func() {
		if v := recover(); v != nil {
			b.err = &CallbackError{Kind: CallbackPanicked}
			// The panic value may hold device output; the stack does not.
			b.s.log.LogAttrs(context.WithoutCancel(ctx), slog.LevelError, "expect: background handler panicked",
				slog.String("stack", string(debug.Stack())))
		} else if !normal {
			b.err = &CallbackError{Kind: CallbackExited}
		}
		w.members[0].l.release()
		b.s.emit(ctx, Event{Op: OpBackground, Case: b.r.Name, Scope: b.r.Scope, CaseIndex: b.r.CaseIndex, Duration: time.Since(start), Err: classify(b.err)})
		b.cancel()
		close(b.done)
	}()
	_, b.r, b.err = w.wait(ctx)
	normal = true
}

// waitResume blocks a paused waiter until Resume reacquires the input or
// the wait is cancelled.
func (b *Background) waitResume(w *waiter) {
	m := w.members[0]
	for {
		select {
		case reply := <-b.resume:
			if err := m.l.reacquire(); err != nil {
				reply <- err
				continue
			}
			w.timer.resume()
			w.idle.resume()
			reply <- nil
			return
		case <-b.ctx.Done():
			return
		}
	}
}
