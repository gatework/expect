package expect

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gatework/expect/internal/redact"
)

// relayFlush is how long a route holds a possible partial match while its
// source is silent before forwarding it anyway.
const relayFlush = 50 * time.Millisecond

// errRelayDone ends every route when one returns.
var errRelayDone = errors.New("expect: relay done")

// Route forwards the output of one Source to its Targets (raw bytes,
// through their serialized Send) and to Output (redacted), for example to
// bridge a console line to another session or tee a device to a log.
// Bytes matched by Cases are withheld unless ForwardMatches is set. Each
// source belongs to one route; sessions are borrowed.
type Route struct {
	Source         *Session
	Targets        []*Session
	Output         io.Writer
	Cases          []Case
	ForwardMatches bool
}

// Relay runs routes until a case returns or a source reaches EOF, which
// ends all routes after forwarding what they hold, or until an error,
// which cancels all routes. Writes already admitted are never replayed;
// a failing target is reported as a *RouteError. The relative timer
// defaults to NoTimeout.
func Relay(ctx context.Context, routes []Route, opts ...MatchOption) error {
	if len(routes) == 0 {
		return configError("relay", "no routes")
	}
	rs := make([]*relayRoute, 0, len(routes))
	defer func() {
		for _, r := range rs {
			r.l.release()
		}
	}()
	for i, rt := range routes {
		r, err := newRelayRoute(i, rt, routes[:i], opts)
		if err != nil {
			return err
		}
		rs = append(rs, r)
	}
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range rs {
		g.Go(func() error { return r.run(gctx) })
	}
	if err := g.Wait(); err != nil && !errors.Is(err, errRelayDone) {
		return err
	}
	return nil
}

// relayRoute is one route's state, owned by its goroutine.
type relayRoute struct {
	index  int
	rt     Route
	l      *lease
	m      *member
	o      matchOptions
	filter *redact.Filter
}

func newRelayRoute(i int, rt Route, earlier []Route, opts []MatchOption) (*relayRoute, error) {
	if rt.Source == nil || slices.Contains(rt.Targets, nil) {
		return nil, configError("relay route", "nil session")
	}
	if slices.ContainsFunc(earlier, func(e Route) bool { return e.Source == rt.Source }) {
		return nil, configError("relay route", "source in two routes")
	}
	o, err := rt.Source.prepare(rt.Cases, append([]MatchOption{WithTimeout(NoTimeout)}, opts...))
	if err != nil {
		return nil, err
	}
	l, err := rt.Source.acquire(nil)
	if err != nil {
		return nil, err
	}
	s := rt.Source
	s.diagMu.Lock()
	filter := s.rxFilter.Clone()
	s.diagMu.Unlock()
	return &relayRoute{index: i, rt: rt, l: l, m: newMember(l, rt.Cases, o), o: o, filter: filter}, nil
}

func (r *relayRoute) run(ctx context.Context) error {
	w := newWaiter(r.o, r.l.wake, r.m)
	defer w.timer.stop()
	flush := time.NewTimer(relayFlush)
	defer flush.Stop()
	for {
		r.m.refreshRules()
		if r.l.pull() {
			flush.Reset(relayFlush)
		}
		if done, err := r.step(ctx, w, false); done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return r.m.s.closedErr(ctx.Err())
		case <-r.l.wake:
		case <-flush.C:
			if _, err := r.step(ctx, w, true); err != nil {
				return err
			}
		case <-w.timer.c():
			if out := w.timeout(ctx); out.err != nil || out.act == Return {
				return cmpOr(out.err, errRelayDone)
			}
		}
	}
}

// step runs cases over pending input and forwards what can no longer be
// part of a match; flush forwards everything.
func (r *relayRoute) step(ctx context.Context, w *waiter, flush bool) (bool, error) {
	s := r.m.s
	for {
		e, mt, ok := r.m.scanText(r.o)
		if !ok {
			break
		}
		end := mt.Start
		if r.rt.ForwardMatches {
			end = mt.End
		}
		if err := r.forward(ctx, s.rx[:end]); err != nil {
			return true, err
		}
		out := w.fire(ctx, r.m, e, mt)
		if out.err != nil || out.act == Return {
			return true, cmpOr(out.err, errRelayDone)
		}
		if out.act == Continue {
			w.timer.reset()
		}
	}
	ended := r.l.ended()
	cut := len(s.rx)
	if !flush && !ended {
		for _, e := range r.m.cases {
			if e.c.Pattern.isText() {
				cut = min(cut, e.c.Pattern.m.Hold(s.rx))
			}
		}
	}
	if err := r.forward(ctx, s.rx[:cut]); err != nil {
		return true, err
	}
	r.l.consume(cut)
	r.m.reset()
	if !ended {
		return false, nil
	}
	if r.rt.Output != nil {
		if _, err := r.rt.Output.Write(r.filter.Finish()); err != nil {
			return true, err
		}
	}
	if cleanEOF(s.rxErr) {
		return true, errRelayDone
	}
	return true, s.closedErr(s.rxErr)
}

func (r *relayRoute) forward(ctx context.Context, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if r.rt.Output != nil {
		if _, err := r.rt.Output.Write(r.filter.Append(p)); err != nil {
			return err
		}
	}
	for j, t := range r.rt.Targets {
		if n, err := t.Send(ctx, string(p)); err != nil {
			return &RouteError{Route: r.index, Target: j, Written: n, Cause: err}
		}
	}
	return nil
}

func cmpOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
