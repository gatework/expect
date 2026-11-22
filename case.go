package expect

import (
	"context"
	"fmt"
)

// Action tells a wait what to do after a handler returns.
type Action uint8

// Actions.
const (
	// Return completes the wait with the handler's Result. It is the zero
	// value and the action of a case without handler.
	Return Action = iota
	// Continue keeps waiting and restarts the relative timer, like Tcl's
	// exp_continue.
	Continue
	// ContinueKeepTimer keeps waiting without restarting the timer, like
	// exp_continue -continue_timer.
	ContinueKeepTimer
)

// Unlimited is a Case.Limit without bound.
const Unlimited = -1

// Handler runs after its case matched and its bytes were consumed, on the
// receiving goroutine and outside internal locks. It may Send on the
// session, but another receive on the same session fails with ErrBusy.
// ctx ends when the wait's context ends or the session closes.
type Handler func(ctx context.Context, s *Session, r Result) (Action, error)

// Case pairs a pattern with an optional handler (nil means Return).
type Case struct {
	// Name labels the case in results, events and transcripts. Operators
	// read it when reviewing a session ("password", "more", "confirm").
	Name    string
	Pattern Pattern
	Handler Handler
	// Limit bounds how often the case may match in one wait; exceeding it
	// fails the wait with CaseLimitError before consuming anything. Zero
	// means Unlimited for foreground text cases and 1 for rules and event
	// cases.
	Limit int
	// NoConsume previews a text match without consuming it. A Continue
	// then observes the same unchanged candidate only once.
	NoConsume bool
	// Sessions restricts a Group case to these members; empty means all.
	Sessions []*Session
}

// String describes the case without its pattern text.
func (c Case) String() string {
	if c.Name != "" {
		return "expect.Case(" + c.Name + ")"
	}
	return "expect.Case"
}

// GoString is String.
func (c Case) GoString() string { return c.String() }

func (c Case) validate(i int, rule bool) error {
	if err := c.Pattern.validate(); err != nil {
		return fmt.Errorf("case %d: %w", i, err)
	}
	if c.Limit < Unlimited {
		return configError("case limit", "negative")
	}
	if c.NoConsume && !c.Pattern.isText() {
		return configError("case", "NoConsume on an event pattern")
	}
	if rule && len(c.Sessions) > 0 {
		return configError("rule", "rules cannot select sessions")
	}
	return nil
}

func (c Case) limit(rule bool) int {
	switch {
	case c.Limit != 0:
		return c.Limit
	case rule || !c.Pattern.isText():
		return 1
	}
	return Unlimited
}
