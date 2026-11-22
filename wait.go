package expect

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/gatework/expect/internal/scan"
)

// entry is one case of a wait with its per-wait state.
type entry struct {
	c      Case
	scope  RuleScope
	index  int
	st     scan.State
	hits   int
	limit  int
	seen   bool   // a NoConsume match was observed ...
	seenAt uint64 // ... at this buffer version
}

// member is one session taking part in a wait: a single Expect has one,
// a Group several.
type member struct {
	l       *lease
	s       *Session
	cases   []*entry // foreground cases selecting this session
	call    []*entry // WithRules
	before  []*entry
	after   []*entry
	rulesAt uint64
	ruled   bool
	collect bool   // record consumed bytes in out (Exchange)
	out     []byte // bytes consumed by matches, in order
}

// waiter runs the matching loop over its members until a case returns or
// the wait fails. All matching state belongs to the goroutine running it.
type waiter struct {
	members []*member
	cases   []Case // for members joining a Group wait
	o       matchOptions
	wake    chan struct{}
	next    int // round-robin start for fairness across members
	timer   budget
	idle    budget
	control chan func(*waiter)
}

// outcome is what one step of the loop produced.
type outcome struct {
	m      *member
	r      Result
	act    Action
	err    error
	fired  bool
	ending bool
}

func newEntries(cases []Case, scope RuleScope, s *Session) []*entry {
	out := make([]*entry, 0, len(cases))
	for i, c := range cases {
		if len(c.Sessions) > 0 && !slices.Contains(c.Sessions, s) {
			continue
		}
		out = append(out, &entry{c: c, scope: scope, index: i, limit: c.limit(scope != ScopeCase)})
	}
	return out
}

func newMember(l *lease, cases []Case, o matchOptions) *member {
	return &member{l: l, s: l.s, cases: newEntries(cases, ScopeCase, l.s), call: newEntries(o.rules, ScopeCall, l.s)}
}

func newWaiter(o matchOptions, wake chan struct{}, members ...*member) *waiter {
	return &waiter{members: members, o: o, wake: wake, timer: newBudget(o.timeout), idle: newBudget(o.idle)}
}

// run waits until a case returns, an error occurs or ctx ends.
func (w *waiter) run(ctx context.Context) (*member, Result, error) {
	defer w.timer.stop()
	defer w.idle.stop()
	for {
		if out := w.step(ctx); out.fired || out.ending {
			if out.err != nil || out.act == Return {
				return out.m, out.r, out.err
			}
			if out.act == Continue {
				w.timer.reset()
			}
			continue
		}
		select {
		case <-ctx.Done():
			m := w.first()
			return m, m.pending(), m.s.closedErr(ctx.Err())
		case <-w.wake:
		case <-w.timer.c():
			if out := w.timeout(ctx); out.err != nil || out.act == Return {
				return out.m, out.r, out.err
			}
		case <-w.idle.c():
			m := w.first()
			return m, m.pending(), ErrIdleTimeout
		case f := <-w.control:
			f(w)
		}
	}
}

// step pulls input and evaluates every member once, starting with the
// member after the one that last matched.
func (w *waiter) step(ctx context.Context) outcome {
	n := len(w.members)
	if n == 0 {
		return outcome{err: ErrNoSessions, ending: true}
	}
	for k := range n {
		i := (w.next + k) % n
		m := w.members[i]
		if !m.l.held {
			continue // a paused Background released its input
		}
		m.refreshRules()
		if m.l.pull() {
			w.idle.reset()
		}
		if out := w.stepMember(ctx, m); out.fired || out.ending {
			w.next = (i + 1) % n
			return out
		}
	}
	return outcome{}
}

func (w *waiter) stepMember(ctx context.Context, m *member) outcome {
	if e, mt, ok := m.scanText(w.o); ok {
		return w.fire(ctx, m, e, mt)
	}
	s := m.s
	if w.o.stream != nil && len(s.rx) >= min(streamWindow, s.o.bufferLimit) {
		return w.streamOut(ctx, m)
	}
	if len(s.rx) > s.o.bufferLimit {
		if e := m.event(patternFullBuffer); e != nil {
			return w.fireEvent(ctx, m, e, len(s.rx))
		}
		return outcome{m: m, r: m.pending(), err: ErrBufferLimit, ending: true}
	}
	if m.l.ended() {
		return w.end(ctx, m)
	}
	return outcome{}
}

// end handles the end of a member's input once its buffered text could
// not match.
func (w *waiter) end(ctx context.Context, m *member) outcome {
	err := m.s.rxErr
	if cleanEOF(err) {
		if e := m.event(patternEOF, patternDefault); e != nil {
			return w.fireEvent(ctx, m, e, len(m.s.rx))
		}
	}
	return outcome{m: m, r: m.pending(), err: m.s.closedErr(err), ending: true}
}

// timeout handles expiry of the relative timer.
func (w *waiter) timeout(ctx context.Context) outcome {
	for _, m := range w.members {
		if e := m.event(patternTimeout, patternDefault); e != nil {
			w.timer.reset()
			out := w.fireEvent(ctx, m, e, 0)
			if out.err == nil && out.act == Continue {
				w.timer.reset()
			}
			return out
		}
	}
	m := w.first()
	return outcome{m: m, r: m.pending(), err: ErrTimeout, ending: true}
}

// fire consumes a text match and runs its handler.
func (w *waiter) fire(ctx context.Context, m *member, e *entry, mt scan.Match) outcome {
	if out, ok := m.admit(e); !ok {
		return out
	}
	if mt.End == mt.Start {
		return outcome{m: m, r: m.pending(), err: ErrEmptyMatch, ending: true}
	}
	r := m.result(e, mt)
	if e.c.NoConsume {
		e.seen, e.seenAt = true, m.s.version
	} else {
		m.take(mt.End)
	}
	return w.handle(ctx, m, e, r)
}

// fireEvent runs an event case, consuming n pending bytes into Before.
func (w *waiter) fireEvent(ctx context.Context, m *member, e *entry, n int) outcome {
	if out, ok := m.admit(e); !ok {
		return out
	}
	r := m.pending()
	r.Before = string(m.s.rx[:n])
	r.Name, r.Scope = e.c.Name, e.scope
	r.CaseIndex, r.RuleIndex = e.indices()
	m.take(n)
	return w.handle(ctx, m, e, r)
}

// streamOut hands the older half of the pending window to the streaming
// fallback.
func (w *waiter) streamOut(ctx context.Context, m *member) outcome {
	n := max(1, min(streamWindow, m.s.o.bufferLimit)/2)
	r := m.pending()
	r.Before, r.Match, r.Scope, r.Start, r.End = "", string(m.s.rx[:n]), ScopeStream, 0, n
	m.l.consume(n)
	m.reset()
	act, err := w.o.stream(ctx, m.s, r)
	if act == Continue {
		act = ContinueKeepTimer
	}
	return outcome{m: m, r: r, act: act, err: err, fired: true}
}

func (w *waiter) handle(ctx context.Context, m *member, e *entry, r Result) outcome {
	m.s.emit(ctx, Event{Op: OpMatch, Case: e.c.Name, Scope: e.scope, CaseIndex: r.CaseIndex, Bytes: len(r.Before) + len(r.Match)})
	if e.c.Handler == nil {
		return outcome{m: m, r: r, act: Return, fired: true}
	}
	act, err := e.c.Handler(ctx, m.s, r)
	return outcome{m: m, r: r, act: act, err: err, fired: true}
}

func (w *waiter) first() *member {
	if len(w.members) == 0 {
		return &member{s: &Session{}}
	}
	return w.members[0]
}

// admit counts a match of e and enforces its limit before anything is
// consumed.
func (m *member) admit(e *entry) (outcome, bool) {
	e.hits++
	if e.limit != Unlimited && e.hits > e.limit {
		ci, ri := e.indices()
		err := &CaseLimitError{Session: m.s.id, Name: e.c.Name, Scope: e.scope, CaseIndex: ci, RuleIndex: ri}
		return outcome{m: m, r: m.pending(), err: err, ending: true}, false
	}
	return outcome{}, true
}

// groups returns the case groups in priority order.
func (m *member) groups() [4][]*entry { return [4][]*entry{m.before, m.cases, m.call, m.after} }

// scanText finds the case to fire: the first group with a match wins;
// within it the earliest match, or the first declared with Declaration.
func (m *member) scanText(o matchOptions) (*entry, scan.Match, bool) {
	lim := scan.Limits{Final: m.l.ended(), Window: o.window}
	for _, g := range m.groups() {
		var best *entry
		var bm scan.Match
		for _, e := range g {
			if !e.c.Pattern.isText() || e.c.NoConsume && e.seen && e.seenAt == m.s.version {
				continue
			}
			mt, ok := e.c.Pattern.m.Find(m.s.rx, &e.st, lim)
			if !ok {
				continue
			}
			if best == nil || mt.Start < bm.Start {
				best, bm = e, mt
			}
			if o.order == Declaration {
				break
			}
		}
		if best != nil {
			return best, bm, true
		}
	}
	return nil, scan.Match{}, false
}

// event returns the first case, in priority order, with one of kinds.
func (m *member) event(kinds ...patternKind) *entry {
	for _, g := range m.groups() {
		for _, e := range g {
			if slices.Contains(kinds, e.c.Pattern.kind) {
				return e
			}
		}
	}
	return nil
}

// refreshRules switches to the current session rules when they changed.
func (m *member) refreshRules() {
	rs := m.s.rules.Load()
	if m.ruled && rs.version == m.rulesAt {
		return
	}
	m.before, m.after = newEntries(rs.before, ScopeBefore, m.s), newEntries(rs.after, ScopeAfter, m.s)
	m.rulesAt, m.ruled = rs.version, true
}

// take consumes the first n pending bytes of a match, collecting them
// when asked, and forgets search progress.
func (m *member) take(n int) {
	if m.collect {
		m.out = append(m.out, m.s.rx[:n]...)
	}
	m.l.consume(n)
	m.reset()
}

// reset forgets search progress after the buffer was consumed.
func (m *member) reset() {
	for _, g := range m.groups() {
		for _, e := range g {
			e.st.Reset()
			e.seen = false
		}
	}
}

func (m *member) pending() Result { return failed(m.s.id, m.s.rx) }

func (m *member) result(e *entry, mt scan.Match) Result {
	buf := m.s.rx[:mt.End]
	r := Result{
		Session: m.s.id, Name: e.c.Name, Scope: e.scope, Start: mt.Start, End: mt.End,
		Before: string(buf[:mt.Start]), Match: string(buf[mt.Start:]), groups: slices.Clone(mt.Groups),
	}
	r.CaseIndex, r.RuleIndex = e.indices()
	names := e.c.Pattern.m.Names()
	for g := 1; 2*g+1 < len(mt.Groups); g++ {
		a, b := mt.Groups[2*g], mt.Groups[2*g+1]
		c := Capture{Start: a, End: b, Matched: a >= 0}
		if g < len(names) {
			c.Name = names[g]
		}
		if c.Matched {
			c.Text = string(buf[a:b])
		}
		r.Captures = append(r.Captures, c)
	}
	return r
}

func (e *entry) indices() (caseIndex, ruleIndex int) {
	if e.scope == ScopeCase {
		return e.index, -1
	}
	return -1, e.index
}

// cleanEOF reports whether err is io.EOF, possibly wrapped, and nothing
// else.
func cleanEOF(err error) bool {
	for err != nil {
		if err == io.EOF { //nolint:errorlint // walking the chain by hand to reject joined causes
			return true
		}
		if j, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range j.Unwrap() {
				if !cleanEOF(e) {
					return false
				}
			}
			return len(j.Unwrap()) > 0
		}
		err = errors.Unwrap(err)
	}
	return false
}

// budget is a resettable, pausable relative timer; a zero duration never
// fires.
type budget struct {
	d        time.Duration
	t        *time.Timer
	deadline time.Time
	left     time.Duration
	paused   bool
}

func newBudget(d time.Duration) budget {
	b := budget{d: d}
	if d > 0 {
		b.t, b.deadline = time.NewTimer(d), time.Now().Add(d)
	}
	return b
}

func (b *budget) c() <-chan time.Time {
	if b.t == nil || b.paused {
		return nil
	}
	return b.t.C
}

func (b *budget) reset() {
	if b.t != nil && !b.paused {
		b.t.Reset(b.d)
		b.deadline = time.Now().Add(b.d)
	}
}

// pause freezes the remaining time.
func (b *budget) pause() {
	if b.t != nil && !b.paused {
		b.left, b.paused = time.Until(b.deadline), true
		b.t.Stop()
	}
}

// resume restarts the timer with the time left at pause.
func (b *budget) resume() {
	if b.t != nil && b.paused {
		b.paused = false
		b.t.Reset(max(b.left, 0))
		b.deadline = time.Now().Add(b.left)
	}
}

func (b *budget) stop() {
	if b.t != nil {
		b.t.Stop()
	}
}
