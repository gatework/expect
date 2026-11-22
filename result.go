package expect

import "fmt"

// RuleScope tells which case group produced a result.
type RuleScope string

// Case groups, in matching priority order.
const (
	ScopeBefore RuleScope = "before" // session Before rules
	ScopeCase   RuleScope = "case"   // the wait's own cases
	ScopeCall   RuleScope = "call"   // WithRules of the wait
	ScopeAfter  RuleScope = "after"  // session After rules
	ScopeStream RuleScope = "stream" // WithStreamingFallback
)

// MatchSource tells which stream a result was matched on.
type MatchSource uint8

// Match sources.
const (
	// SourceRemote is input from the peer; it is the zero value.
	SourceRemote MatchSource = iota
	// SourceLocal is operator input during Interact.
	SourceLocal
)

// String names the source.
func (m MatchSource) String() string {
	if m == SourceLocal {
		return "local"
	}
	return "remote"
}

// Capture is one submatch. Matched tells an unmatched optional group from
// an empty one; offsets are relative to Result.Buffer and -1 when
// unmatched.
type Capture struct {
	Name       string
	Text       string
	Start, End int
	Matched    bool
}

// String omits the text.
func (c Capture) String() string { return "expect.Capture(" + c.Name + ")" }

// GoString is String.
func (c Capture) GoString() string { return c.String() }

// Result is a detached snapshot of the bytes a wait consumed: Before the
// match and the Match itself. On failure, Before holds the pending input
// and CaseIndex is -1. Fields hold raw, unredacted bytes; formatting
// prints metadata only.
type Result struct {
	Session string
	Source  MatchSource
	// Name is the matched case's name.
	Name  string
	Scope RuleScope
	// CaseIndex indexes the wait's cases, or is -1 for a rule or failure.
	CaseIndex int
	// RuleIndex indexes the rule within Scope, or is -1.
	RuleIndex int
	// Start and End delimit Match within Buffer().
	Start, End    int
	Before, Match string
	// Output is set with WithOutput (always by Exchange): every byte the
	// wait consumed, in order, across the cases that continued.
	Output   string
	Captures []Capture
	groups   []int
	// tail and split describe a result whose text is still to be taken
	// from the end of Output: its last tail bytes, Match from split.
	tail, split int
}

// Buffer returns Before followed by Match.
func (r Result) Buffer() string { return r.Before + r.Match }

// Capture returns the first matched group called name.
func (r Result) Capture(name string) (Capture, bool) {
	for _, c := range r.Captures {
		if c.Name == name && c.Matched {
			return c, true
		}
	}
	return Capture{}, false
}

// Indices returns the half-open offsets of a group within Buffer(); group
// 0 is the whole match.
func (r Result) Indices(group int) (int, int, bool) {
	if group < 0 || 2*group+1 >= len(r.groups) || r.groups[2*group] < 0 {
		return -1, -1, false
	}
	return r.groups[2*group], r.groups[2*group+1], true
}

// String prints metadata only.
func (r Result) String() string {
	return fmt.Sprintf("expect.Result{scope:%s case:%d rule:%d bytes:%d}", r.Scope, r.CaseIndex, r.RuleIndex, len(r.Before)+len(r.Match))
}

// GoString is String.
func (r Result) GoString() string { return r.String() }

// failed returns a result for a wait that ended without match.
func failed(session string, pending []byte) Result {
	return Result{Session: session, CaseIndex: -1, RuleIndex: -1, Before: string(pending)}
}
