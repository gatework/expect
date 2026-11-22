package expect

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Group waits on several sessions at once, for example the two members of
// an HA pair or a fleet of console lines. Members are borrowed: the Group
// never closes them. Only one Expect runs on a Group at a time; Add and
// Remove may be called while it runs, but not from its handlers.
type Group struct {
	mu      sync.Mutex // guards members and run
	members []*Session
	run     *groupRun // the running Expect, or nil
}

// groupRun lets Add and Remove reach a running Expect.
type groupRun struct {
	ctl  chan func(*waiter)
	done chan struct{}
}

// NewGroup returns a group of distinct, non-nil sessions. It performs no
// I/O.
func NewGroup(sessions ...*Session) (*Group, error) {
	g := &Group{}
	for _, s := range sessions {
		if err := g.check(s); err != nil {
			return nil, err
		}
		g.members = append(g.members, s)
	}
	return g, nil
}

// Expect waits until a case returns on any member and reports which
// member matched. Case.Sessions binds a case to some members. Members are
// served round-robin so a chatty member cannot starve the others. A
// member's own Before and After rules apply to it. The relative timer
// defaults to the first member's default timeout.
func (g *Group) Expect(ctx context.Context, cases []Case, opts ...MatchOption) (*Session, Result, error) {
	g.mu.Lock()
	if g.run != nil {
		g.mu.Unlock()
		return nil, failed("", nil), ErrBusy
	}
	if len(g.members) == 0 {
		g.mu.Unlock()
		return nil, failed("", nil), ErrNoSessions
	}
	first := g.members[0]
	o, err := first.prepare(cases, opts)
	if err != nil {
		g.mu.Unlock()
		return nil, failed("", nil), err
	}
	wake := make(chan struct{}, 1)
	members, err := g.lease(wake, cases, o)
	if err != nil {
		g.mu.Unlock()
		return nil, failed("", nil), err
	}
	run := &groupRun{ctl: make(chan func(*waiter)), done: make(chan struct{})}
	g.run = run
	g.mu.Unlock()

	w := newWaiter(o, wake, members...)
	w.control, w.cases = run.ctl, cases
	start := time.Now()
	m, r, err := w.wait(ctx)
	g.mu.Lock()
	g.run = nil
	close(run.done)
	g.mu.Unlock()
	for _, m := range w.members {
		m.l.release()
	}
	if m == nil || m.l == nil {
		return nil, r, err
	}
	m.s.emit(ctx, Event{Op: OpExpect, Case: r.Name, Scope: r.Scope, CaseIndex: r.CaseIndex, Duration: time.Since(start), Err: classify(err)})
	return m.s, r, err
}

// Add makes s a member. During Expect, s joins the running wait once its
// input can be taken; ErrBusy means another receiver holds it.
func (g *Group) Add(ctx context.Context, s *Session) error {
	g.mu.Lock()
	err := g.check(s)
	g.mu.Unlock()
	if err != nil {
		return err
	}
	return g.change(ctx, func() error {
		if err := g.check(s); err != nil {
			return err
		}
		g.members = append(g.members, s)
		return nil
	}, func(w *waiter) error {
		l, err := s.acquire(w.wake)
		if err != nil {
			return err
		}
		w.members = append(w.members, newMember(l, w.cases, w.o))
		return nil
	})
}

// Remove drops s from the group. During Expect, its input is released with
// any unconsumed bytes kept for the next receiver.
func (g *Group) Remove(ctx context.Context, s *Session) error {
	return g.change(ctx, func() error {
		g.members = slices.DeleteFunc(g.members, func(m *Session) bool { return m == s })
		return nil
	}, func(w *waiter) error {
		w.members = slices.DeleteFunc(w.members, func(m *member) bool {
			if m.s == s {
				m.l.release()
				return true
			}
			return false
		})
		return nil
	})
}

// change applies a membership change: to the member list (g.mu held) and,
// during Expect, first to the running wait on its own goroutine.
func (g *Group) change(ctx context.Context, list func() error, live func(*waiter) error) error {
	for {
		g.mu.Lock()
		run := g.run
		if run == nil {
			defer g.mu.Unlock()
			return list()
		}
		g.mu.Unlock()
		reply := make(chan error, 1)
		select {
		case run.ctl <- func(w *waiter) { reply <- live(w) }:
			if err := <-reply; err != nil {
				return err
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			return list()
		case <-run.done:
			// The Expect ended first; apply the change to the list.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// check rejects nil and duplicate members; g.mu is held.
func (g *Group) check(s *Session) error {
	if s == nil {
		return configError("group member", "nil")
	}
	if slices.Contains(g.members, s) {
		return configError("group member", "duplicate")
	}
	return nil
}

// lease takes the input of every member, or of none.
func (g *Group) lease(wake chan struct{}, cases []Case, o matchOptions) ([]*member, error) {
	members := make([]*member, 0, len(g.members))
	for _, s := range g.members {
		l, err := s.acquire(wake)
		if err != nil {
			for _, m := range members {
				m.l.release()
			}
			return nil, err
		}
		members = append(members, newMember(l, cases, o))
	}
	return members, nil
}
