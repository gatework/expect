package expect

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"
)

// Operation names an observed session operation.
type Operation string

// Observed operations.
const (
	OpOpen     Operation = "open"
	OpExpect   Operation = "expect"
	OpMatch    Operation = "match"
	OpSend     Operation = "send"
	OpBreak    Operation = "break"
	OpInteract Operation = "interact"
	OpDrain    Operation = "drain"
	OpPoll     Operation = "poll"
	// OpBackground ends a StartBackground wait.
	OpBackground Operation = "background"
	OpRelay      Operation = "relay"
	// OpEcho is the echo stage of an Exchange WithEcho; Bytes counts the
	// stale input it discarded.
	OpEcho  Operation = "echo"
	OpClose Operation = "close"
)

// String returns the operation name.
func (o Operation) String() string { return string(o) }

// wait reports whether o completes a wait of some receiver.
func (o Operation) wait() bool {
	switch o { //nolint:exhaustive // only operations that end a wait
	case OpExpect, OpPoll, OpDrain, OpEcho, OpBackground, OpRelay, OpInteract:
		return true
	}
	return false
}

// ErrorClass classifies an error without its message.
type ErrorClass string

// Error classes.
const (
	ClassNone        ErrorClass = ""
	ClassTimeout     ErrorClass = "timeout"
	ClassIdle        ErrorClass = "idle_timeout"
	ClassCanceled    ErrorClass = "canceled"
	ClassDeadline    ErrorClass = "deadline"
	ClassClosed      ErrorClass = "closed"
	ClassEOF         ErrorClass = "eof"
	ClassBufferLimit ErrorClass = "buffer_limit"
	ClassBusy        ErrorClass = "busy"
	ClassPattern     ErrorClass = "pattern"
	ClassLimit       ErrorClass = "case_limit"
	ClassWrite       ErrorClass = "write"
	ClassOther       ErrorClass = "other"
)

// String returns the class name.
func (c ErrorClass) String() string { return string(c) }

// Event describes one completed operation with metadata only: names,
// sizes and durations, never payload.
type Event struct {
	Op      Operation
	Session string
	// Case is the name of the matched case, or "" when unnamed or none.
	Case      string
	Scope     RuleScope
	CaseIndex int
	Bytes     int
	Duration  time.Duration
	Err       ErrorClass
}

// Observer receives events synchronously on the goroutine that completed
// the operation. It must be fast and must not call the Session.
type Observer func(Event)

// emit logs ev on the session logger and the transcript, then notifies
// observers.
func (s *Session) emit(ctx context.Context, ev Event) {
	ev.Session = s.id
	s.count(ev)
	if s.log.Enabled(ctx, slog.LevelDebug) {
		s.logEvent(context.WithoutCancel(ctx), ev)
	}
	for _, o := range s.o.observers {
		o(ev)
	}
}

// logEvent writes ev to the session logger.
func (s *Session) logEvent(ctx context.Context, ev Event) {
	attrs := []slog.Attr{slog.String("op", string(ev.Op))}
	if ev.Case != "" {
		attrs = append(attrs, slog.String("case", ev.Case))
	}
	if ev.Scope != "" && ev.Scope != ScopeCase {
		attrs = append(attrs, slog.String("scope", string(ev.Scope)))
	}
	if ev.Bytes > 0 {
		attrs = append(attrs, slog.Int("bytes", ev.Bytes))
	}
	if ev.Duration > 0 {
		attrs = append(attrs, slog.Duration("duration", ev.Duration))
	}
	if ev.Err != ClassNone {
		attrs = append(attrs, slog.String("err_class", string(ev.Err)))
	}
	s.log.LogAttrs(ctx, slog.LevelDebug, "expect: event", attrs...)
}

// classify maps an error to its class.
func classify(err error) ErrorClass {
	_, isLimit := errors.AsType[*CaseLimitError](err)
	_, isWrite := errors.AsType[*WriteError](err)
	switch {
	case err == nil:
		return ClassNone
	case errors.Is(err, ErrTimeout):
		return ClassTimeout
	case errors.Is(err, ErrIdleTimeout):
		return ClassIdle
	case errors.Is(err, context.Canceled):
		return ClassCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return ClassDeadline
	case isWrite:
		return ClassWrite
	case errors.Is(err, ErrClosed), errors.Is(err, io.ErrClosedPipe), errors.Is(err, os.ErrClosed):
		return ClassClosed
	case errors.Is(err, io.EOF):
		return ClassEOF
	case errors.Is(err, ErrBufferLimit), errors.Is(err, ErrOutputLimit):
		return ClassBufferLimit
	case errors.Is(err, ErrBusy):
		return ClassBusy
	case errors.Is(err, ErrInvalidPattern), errors.Is(err, ErrEmptyMatch):
		return ClassPattern
	case isLimit:
		return ClassLimit
	}
	return ClassOther
}
