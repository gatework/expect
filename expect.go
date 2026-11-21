package expect

import (
	"context"
	"time"
)

// Expect waits until a case returns.
//
// Cases are matched in groups: the session's Before rules, then cases,
// then WithRules, then the session's After rules. The first group with a
// text match wins; within it the earliest match wins and declaration order
// breaks ties (see WithMatchOrder). A match consumes the input through its
// end, then its handler runs; Continue keeps waiting. A Timeout, Default,
// EOF or FullBuffer case turns the corresponding failure into a match.
//
// On failure the Result has CaseIndex -1 and Before holds the pending
// input, which stays buffered. Errors: ErrTimeout, ErrIdleTimeout,
// ErrBufferLimit, ErrClosed, ErrBusy, a CaseLimitError, the context's
// error, the read error (io.EOF at a clean end), or a handler's error.
func (s *Session) Expect(ctx context.Context, cases []Case, opts ...MatchOption) (Result, error) {
	o, err := s.prepare(cases, opts)
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
	w := newWaiter(o, l.wake, newMember(l, cases, o))
	_, r, err := w.run(ctx)
	s.emit(ctx, Event{Op: OpExpect, Case: r.Name, Scope: r.Scope, CaseIndex: r.CaseIndex, Duration: time.Since(start), Err: classify(err)})
	return r, err
}

// Poll matches the input that has already arrived without waiting for
// more. Handlers may Continue; Poll then evaluates the remaining input
// once more. Without a match it returns ErrTimeout, or the Timeout or
// Default case.
func (s *Session) Poll(ctx context.Context, cases []Case, opts ...MatchOption) (Result, error) {
	o, err := s.prepare(cases, opts)
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
	o.timeout, o.idle = 0, 0
	w := newWaiter(o, l.wake, newMember(l, cases, o))
	defer w.timer.stop()
	for {
		out := w.step(ctx)
		switch {
		case out.fired || out.ending:
			if out.err != nil || out.act == Return {
				return out.r, out.err
			}
		default:
			out = w.timeout(ctx)
			return out.r, out.err
		}
	}
}

// prepare validates cases and options before any receive.
func (s *Session) prepare(cases []Case, opts []MatchOption) (matchOptions, error) {
	o, err := s.matchOptions(opts)
	if err != nil {
		return o, err
	}
	for i, c := range cases {
		if err := c.validate(i, false); err != nil {
			return o, err
		}
	}
	return o, nil
}
