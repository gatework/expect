package expect

import (
	"slices"
	"time"
)

// NoTimeout disables a relative timer; the parent context still applies.
const NoTimeout time.Duration = 0

// streamWindow bounds the pending bytes WithStreamingFallback keeps.
const streamWindow = 64 << 10

// MatchOrder chooses between text cases of the same group that both match.
type MatchOrder uint8

// Match orders.
const (
	// Earliest selects the match that starts first; declaration order
	// breaks ties. It is the default.
	Earliest MatchOrder = iota
	// Declaration selects the first declared case that matches.
	Declaration
)

// MatchOption configures one wait.
type MatchOption func(*matchOptions) error

type matchOptions struct {
	timeout    time.Duration
	timeoutSet bool
	idle       time.Duration
	order      MatchOrder
	rules      []Case
	stream     Handler
	window     int
	output     bool
	outLimit   int
	echo       bool
	echoWait   time.Duration
}

// WithTimeout sets the relative timer of the wait, which includes handler
// time; Continue restarts it. Zero disables it.
func WithTimeout(d time.Duration) MatchOption {
	return func(o *matchOptions) error {
		if d < 0 {
			return configError("timeout", "negative")
		}
		o.timeout, o.timeoutSet = d, true
		return nil
	}
}

// WithIdleTimeout fails the wait with ErrIdleTimeout when no input arrives
// for d; zero disables it. It catches a device that stalls mid-output.
func WithIdleTimeout(d time.Duration) MatchOption {
	return func(o *matchOptions) error {
		if d < 0 {
			return configError("idle timeout", "negative")
		}
		o.idle = d
		return nil
	}
}

// WithMatchOrder selects how ties between text cases are broken.
func WithMatchOrder(order MatchOrder) MatchOption {
	return func(o *matchOptions) error {
		if order > Declaration {
			return configError("match order", "unknown")
		}
		o.order = order
		return nil
	}
}

// WithRules adds reusable cases matched after the wait's own cases and
// before the session's After rules. Their Limit defaults to 1.
func WithRules(cases ...Case) MatchOption {
	return func(o *matchOptions) error {
		o.rules = slices.Clone(cases)
		return validateRules(o.rules)
	}
}

// WithStreamingFallback hands unmatched output to handler instead of
// letting it pile up: when min(64 KiB, buffer limit) bytes are pending
// without a match, the older half is consumed and passed as the Match of
// a Result with Scope ScopeStream. Continue keeps all timers. Use it to
// collect large outputs (show running-config) without buffering them.
func WithStreamingFallback(handler Handler) MatchOption {
	return func(o *matchOptions) error {
		if handler == nil {
			return configError("streaming fallback", "nil handler")
		}
		o.stream = handler
		return nil
	}
}

// WithSearchWindow restricts every text search to the last
// n bytes of pending input; the window start counts as the start of text.
// Prompts and questions sit at the end of the output, so a window of a few
// KiB keeps prompt detection linear on multi-megabyte outputs whose
// patterns cannot resume line by line. Zero searches everything.
func WithSearchWindow(n int) MatchOption {
	return func(o *matchOptions) error {
		if n < 0 {
			return configError("search window", "negative")
		}
		o.window = n
		return nil
	}
}

// WithOutput fills Result.Output with every byte the wait consumed, in
// order: the text of cases and rules that continued (answered questions,
// pagers) followed by the returning case. Before and Match of the
// returning case then share Output's memory. Bytes handed to
// WithStreamingFallback are not collected. Exchange always collects.
func WithOutput() MatchOption {
	return func(o *matchOptions) error {
		o.output = true
		return nil
	}
}

// WithOutputLimit is WithOutput that fails the wait with ErrOutputLimit
// once more than n bytes were collected, as soon as the match that
// crosses it is consumed and before its handler runs. Zero means no
// limit.
func WithOutputLimit(n int) MatchOption {
	return func(o *matchOptions) error {
		if n < 0 {
			return configError("output limit", "negative")
		}
		o.output, o.outLimit = true, n
		return nil
	}
}

func (s *Session) matchOptions(opts []MatchOption) (matchOptions, error) {
	o := matchOptions{timeout: s.o.timeout}
	for _, opt := range opts {
		if opt == nil {
			return o, configError("match option", "nil")
		}
		if err := opt(&o); err != nil {
			return o, err
		}
	}
	return o, nil
}
