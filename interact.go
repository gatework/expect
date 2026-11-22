package expect

import (
	"bytes"
	"context"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/gatework/expect/internal/redact"
	"github.com/gatework/expect/internal/scan"
)

const (
	// maxHeldInput bounds operator input held back for matching.
	maxHeldInput = 4 << 10
	// outputWindow bounds peer output kept for output cases.
	outputWindow = 64 << 10
)

// Interact hands the session to an operator: peer output is copied to
// output (redacted) and operator input is sent to the peer, until the
// escape byte, an input or output case that returns, the end of either
// stream, or ctx ends. It is the receiver for its duration. Interact owns
// input once admitted and closes it on return; input.Close must interrupt
// a blocked Read. output is borrowed. A clean end of either stream returns
// nil.
func (s *Session) Interact(ctx context.Context, input io.ReadCloser, output io.Writer, opts ...InteractOption) error {
	o, err := applyInteractOptions(opts)
	if err != nil {
		return err
	}
	if o.cooked {
		return configError("interact option", "cooked callbacks need InteractTerminal")
	}
	return s.interact(ctx, input, output, o)
}

func (s *Session) interact(ctx context.Context, input io.ReadCloser, output io.Writer, o interactOptions) error {
	if isNil(input) || isNil(output) {
		return configError("interact", "nil input or output")
	}
	mo, err := s.matchOptions(append([]MatchOption{WithTimeout(NoTimeout)}, o.match...))
	if err != nil {
		return err
	}
	l, err := s.acquire(nil)
	if err != nil {
		return err
	}
	defer l.release()
	ctx, cancel := s.scoped(ctx)
	defer cancel()
	in := startReader(input)
	defer in.stop()
	s.diagMu.Lock()
	filter := s.rxFilter.Clone()
	s.diagMu.Unlock()
	it := &interaction{
		s: s, l: l, o: o, out: output, filter: filter, in: in,
		remote: newMember(l, o.output, mo), local: newEntries(o.input, ScopeCase, s),
		timer: newBudget(mo.timeout), idle: newBudget(o.inputTimeout), forwarded: len(s.rx),
	}
	defer it.timer.stop()
	defer it.idle.stop()
	start := time.Now()
	err = it.run(ctx)
	s.emit(ctx, Event{Op: OpInteract, Duration: time.Since(start), Err: classify(err)})
	return err
}

// interaction is the state of one Interact, owned by its goroutine.
type interaction struct {
	s         *Session
	l         *lease
	o         interactOptions
	out       io.Writer
	filter    *redact.Filter
	in        *reader
	remote    *member  // output cases over the peer stream
	local     []*entry // input cases
	held      []byte   // operator input held for matching
	forwarded int      // rx bytes already shown to the operator
	timer     budget   // output relative timer
	idle      budget   // operator idle timer
}

func (it *interaction) run(ctx context.Context) error {
	for {
		done, err := it.remoteStep(ctx)
		if done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return it.s.closedErr(ctx.Err())
		case <-it.l.wake:
		case c := <-it.in.c:
			it.idle.reset()
			if done, err := it.localInput(ctx, c); done || err != nil {
				return err
			}
		case <-it.idle.c():
			if done, err := it.localIdle(ctx); done || err != nil {
				return err
			}
			it.idle.reset()
		case <-it.timer.c():
			if done, err := it.outputTimeout(ctx); done || err != nil {
				return err
			}
		}
	}
}

// remoteStep shows new peer output and runs output cases.
func (it *interaction) remoteStep(ctx context.Context) (bool, error) {
	s := it.s
	it.remote.refreshRules()
	if it.l.pull() {
		if err := it.show(s.rx[it.forwarded:]); err != nil {
			return true, err
		}
		it.forwarded = len(s.rx)
	}
	for {
		e, mt, ok := it.remote.scanText(matchOptions{})
		if !ok {
			break
		}
		out := it.remote.fire(ctx, e, mt)
		it.forwarded = max(0, it.forwarded-mt.End)
		if out.err != nil || out.act == Return {
			return true, out.err
		}
		if out.act == Continue {
			it.timer.reset()
		}
	}
	if win := min(outputWindow, s.o.bufferLimit); len(s.rx) > win {
		n := max(1, len(s.rx)/2)
		it.l.consume(n)
		it.remote.reset()
		it.forwarded = max(0, it.forwarded-n)
	}
	if it.l.ended() {
		return true, it.finish(s.rxErr)
	}
	return false, nil
}

// show writes peer output to the operator through the redaction filter.
func (it *interaction) show(p []byte) error {
	if !it.o.outputForward || len(p) == 0 {
		return nil
	}
	_, err := it.out.Write(it.filter.Append(p))
	return err
}

// finish flushes output at the end of the peer stream.
func (it *interaction) finish(rerr error) error {
	if it.o.outputForward {
		if _, err := it.out.Write(it.filter.Finish()); err != nil {
			return err
		}
	}
	if cleanEOF(rerr) {
		return nil
	}
	return it.s.closedErr(rerr)
}

// localInput handles one chunk of operator input.
func (it *interaction) localInput(ctx context.Context, c chunk) (bool, error) {
	data := c.data
	escaped := false
	if !it.o.noEscape {
		if i := bytes.IndexByte(data, it.o.escape); i >= 0 {
			data, escaped = data[:i], true
		}
	}
	it.held = append(it.held, data...)
	if done, err := it.matchLocal(ctx, escaped || c.err != nil); done || err != nil {
		return true, err
	}
	switch {
	case escaped:
		return true, nil
	case c.err != nil && cleanEOF(c.err):
		return it.localEvent(ctx, patternEOF)
	case c.err != nil:
		return true, c.err
	}
	return false, nil
}

// localIdle flushes held input and runs an input Timeout case.
func (it *interaction) localIdle(ctx context.Context) (bool, error) {
	if done, err := it.matchLocal(ctx, true); done || err != nil {
		return true, err
	}
	if e := it.localCase(patternTimeout, patternDefault); e == nil {
		return false, nil
	}
	return it.localEvent(ctx, patternTimeout)
}

// matchLocal runs input cases over held input and forwards what can no
// longer match; flush forwards everything left.
func (it *interaction) matchLocal(ctx context.Context, flush bool) (bool, error) {
	for {
		e, mt, ok := it.findLocal(flush)
		if !ok {
			break
		}
		if err := it.forward(ctx, it.held[:mt.Start]); err != nil {
			return true, err
		}
		if done, err := it.fireLocal(ctx, e, mt); done || err != nil {
			return true, err
		}
	}
	cut := len(it.held)
	if !flush {
		for _, e := range it.local {
			if e.c.Pattern.isText() {
				cut = min(cut, e.c.Pattern.m.Hold(it.held))
			}
		}
		cut = max(cut, len(it.held)-maxHeldInput)
	}
	err := it.forward(ctx, it.held[:cut])
	it.held = slices.Clone(it.held[cut:])
	return false, err
}

func (it *interaction) findLocal(final bool) (*entry, scan.Match, bool) {
	var best *entry
	var bm scan.Match
	for _, e := range it.local {
		if !e.c.Pattern.isText() {
			continue
		}
		var st scan.State
		if mt, ok := e.c.Pattern.m.Find(it.held, &st, scan.Limits{Final: final}); ok && (best == nil || mt.Start < bm.Start) {
			best, bm = e, mt
		}
	}
	return best, bm, best != nil
}

func (it *interaction) fireLocal(ctx context.Context, e *entry, mt scan.Match) (bool, error) {
	m := &member{s: it.s}
	if out, ok := m.admit(e); !ok {
		return true, out.err
	}
	match := it.held[mt.Start:mt.End]
	r := Result{Session: it.s.id, Source: SourceLocal, Name: e.c.Name, Scope: ScopeCase, CaseIndex: e.index, RuleIndex: -1,
		Start: 0, End: len(match), Match: string(match), groups: []int{0, len(match)}}
	if it.o.inputForward {
		if err := it.forward(ctx, match); err != nil {
			return true, err
		}
	}
	if it.o.echo {
		if _, err := it.out.Write(match); err != nil {
			return true, err
		}
	}
	it.held = slices.Clone(it.held[mt.End:])
	return it.call(ctx, e, r)
}

// localEvent runs the input EOF or Timeout case; without one, EOF ends
// the interaction and a timeout is ignored.
func (it *interaction) localEvent(ctx context.Context, kind patternKind) (bool, error) {
	e := it.localCase(kind, patternDefault)
	if e == nil {
		return kind == patternEOF, nil
	}
	m := &member{s: it.s}
	if out, ok := m.admit(e); !ok {
		return true, out.err
	}
	r := Result{Session: it.s.id, Source: SourceLocal, Name: e.c.Name, Scope: ScopeCase, CaseIndex: e.index, RuleIndex: -1}
	done, err := it.call(ctx, e, r)
	return done || kind == patternEOF, err
}

func (it *interaction) localCase(kinds ...patternKind) *entry {
	for _, e := range it.local {
		if slices.Contains(kinds, e.c.Pattern.kind) {
			return e
		}
	}
	return nil
}

func (it *interaction) call(ctx context.Context, e *entry, r Result) (bool, error) {
	it.s.emit(ctx, Event{Op: OpMatch, Case: e.c.Name, Scope: ScopeCase, CaseIndex: e.index, Bytes: len(r.Match)})
	if e.c.Handler == nil {
		return true, nil
	}
	act, err := e.c.Handler(ctx, it.s, r)
	return err != nil || act == Return, err
}

// outputTimeout runs an output Timeout case or ends with ErrTimeout.
func (it *interaction) outputTimeout(ctx context.Context) (bool, error) {
	e := it.remote.event(patternTimeout, patternDefault)
	if e == nil {
		return true, ErrTimeout
	}
	it.timer.reset()
	out := it.remote.fireEvent(ctx, e, 0)
	if out.err != nil || out.act == Return {
		return true, out.err
	}
	if out.act == Continue {
		it.timer.reset()
	}
	return false, nil
}

func (it *interaction) forward(ctx context.Context, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	_, err := it.s.Send(ctx, string(p))
	return err
}

// chunk is one read of operator input; err ends the stream.
type chunk struct {
	data []byte
	err  error
}

// reader owns operator input: one goroutine reads it until an error or
// stop, which closes the input to interrupt the read and joins it.
type reader struct {
	r    io.ReadCloser
	c    chan chunk
	quit chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

func startReader(r io.ReadCloser) *reader {
	rd := &reader{r: r, c: make(chan chunk), quit: make(chan struct{})}
	rd.wg.Go(rd.loop)
	return rd
}

func (rd *reader) loop() {
	buf := make([]byte, 4096)
	for {
		n, err := rd.r.Read(buf)
		c := chunk{data: slices.Clone(buf[:n]), err: err}
		select {
		case rd.c <- c:
		case <-rd.quit:
			return
		}
		if err != nil {
			return
		}
	}
}

func (rd *reader) stop() {
	rd.once.Do(func() {
		close(rd.quit)
		_ = rd.r.Close()
		rd.wg.Wait()
	})
}
