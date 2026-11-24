package expect

import (
	"errors"
	"fmt"
	"regexp/syntax"
)

// Sentinel errors classify protocol and lifecycle failures.
var (
	// ErrClosed reports an operation on, or interrupted by, a closed Session.
	ErrClosed = errors.New("expect: session closed")
	// ErrBusy reports that another receiver (Expect, Interact, Background,
	// Group or Relay) holds the session input.
	ErrBusy = errors.New("expect: another receiver is active")
	// ErrTimeout reports that the relative wait timer expired.
	ErrTimeout = errors.New("expect: timeout")
	// ErrIdleTimeout reports that no input arrived within the idle interval.
	ErrIdleTimeout = errors.New("expect: idle timeout")
	// ErrBufferLimit reports unmatched input beyond the buffer limit. The
	// input is preserved for the next receiver.
	ErrBufferLimit = errors.New("expect: buffer limit reached")
	// ErrOutputLimit reports collected output beyond WithOutputLimit. The
	// collected part is in Result.Output.
	ErrOutputLimit = errors.New("expect: output limit reached")
	// ErrUnencodable reports text the session charset (WithCharset)
	// cannot represent; nothing was sent.
	ErrUnencodable = errors.New("expect: text not representable in the session charset")
	// ErrNoEcho reports a device that did not echo an Exchange line sent
	// WithEcho; it wraps the cause, usually ErrTimeout.
	ErrNoEcho = errors.New("expect: no echo of the line sent")
	// ErrEmptyMatch reports a selected match of zero bytes.
	ErrEmptyMatch = errors.New("expect: empty match")
	// ErrInvalidPattern reports a malformed or unusable pattern.
	ErrInvalidPattern = errors.New("expect: invalid pattern")
	// ErrNoSessions reports a Group without members to receive from.
	ErrNoSessions = errors.New("expect: no sessions")
	// ErrBreakUnsupported reports a transport that cannot send a break.
	ErrBreakUnsupported error = unsupportedError("expect: transport break unsupported")
)

type unsupportedError string

func (e unsupportedError) Error() string { return string(e) }
func (unsupportedError) Unwrap() error   { return errors.ErrUnsupported }

// WriteError reports a write that failed after it was admitted to the
// transport. The session is closed: the peer may have received any prefix
// of the data, so the write must not be replayed. Error never prints the
// payload or the cause; Unwrap exposes the cause.
type WriteError struct {
	// Written is the number of bytes the transport accepted.
	Written int
	Cause   error
}

// Error describes the failure without payload.
func (e *WriteError) Error() string {
	return fmt.Sprintf("expect: write failed (written=%d class=%s)", e.Written, classify(e.Cause))
}

// Unwrap returns the cause.
func (e *WriteError) Unwrap() error { return e.Cause }

// CaseLimitError reports a case matched more often than its Limit allows.
type CaseLimitError struct {
	Session   string
	Name      string
	Scope     RuleScope
	CaseIndex int
	RuleIndex int
}

// Error identifies the case without its pattern.
func (e *CaseLimitError) Error() string {
	if e.Name != "" {
		return "expect: case " + e.Name + " exceeded its limit"
	}
	return "expect: case limit exceeded"
}

// CallbackFailure tells how an asynchronous callback ended abnormally.
type CallbackFailure string

// Abnormal callback endings.
const (
	// CallbackPanicked means a callback panicked; the value is not kept.
	CallbackPanicked CallbackFailure = "panic"
	// CallbackExited means a callback called runtime.Goexit.
	CallbackExited CallbackFailure = "goexit"
)

// CallbackError reports a callback of an asynchronous operation
// (Background, Relay, Interact input) that did not return normally.
type CallbackError struct{ Kind CallbackFailure }

// Error names the failure kind.
func (e *CallbackError) Error() string { return "expect: callback failed (" + string(e.Kind) + ")" }

// ConfigError reports a rejected option. It never holds the option value.
type ConfigError struct {
	Option string
	Reason string
	Cause  error
}

// Error names the option and the reason.
func (e *ConfigError) Error() string { return "expect: invalid " + e.Option + ": " + e.Reason }

// Unwrap returns the cause, if any.
func (e *ConfigError) Unwrap() error { return e.Cause }

// CompileError reports a regular expression that does not compile. It
// keeps the syntax category, never the expression.
type CompileError struct{ Code syntax.ErrorCode }

// Error names the syntax category.
func (e *CompileError) Error() string {
	return fmt.Sprintf("expect: invalid regular expression (%s)", e.Code)
}

// Unwrap returns ErrInvalidPattern.
func (e *CompileError) Unwrap() error { return ErrInvalidPattern }

// RouteError reports a Relay target that failed, with the bytes it
// accepted.
type RouteError struct {
	Route   int
	Target  int
	Written int
	Cause   error
}

// Error identifies the route and target without payload.
func (e *RouteError) Error() string {
	return fmt.Sprintf("expect: relay route %d target %d failed (written=%d class=%s)", e.Route, e.Target, e.Written, classify(e.Cause))
}

// Unwrap returns the cause.
func (e *RouteError) Unwrap() error { return e.Cause }

func configError(option, reason string) error { return &ConfigError{Option: option, Reason: reason} }
