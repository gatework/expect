package expect

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"

	"github.com/gatework/expect/internal/scan"
)

type patternKind uint8

const (
	patternUnset patternKind = iota
	patternLiteral
	patternLine
	patternRegexp
	patternGlob
	patternAll
	patternEOF
	patternTimeout
	patternDefault
	patternFullBuffer
)

var kindNames = [...]string{"unset", "literal", "line", "regexp", "glob", "all", "eof", "timeout", "default", "full_buffer"}

// Pattern is an immutable text pattern (literal, line, regular expression,
// glob or conjunction) or an event (EOF, Timeout, Default, FullBuffer).
// The zero value is invalid. Formatting never reveals the pattern text.
type Pattern struct {
	kind  patternKind
	m     *scan.Matcher
	err   error
	fold  bool
	text  string // literal, line or glob source
	re    *regexp.Regexp
	parts []Pattern
}

// Literal matches a nonempty byte string, including binary data.
func Literal(text string) Pattern {
	p := Pattern{kind: patternLiteral, text: text}
	return p.build()
}

// Line matches text as the final line received: it starts the buffer or
// follows CR or LF, and only spaces, tabs or NULs follow it. It suits a
// prompt that must be the last thing the peer sent.
func Line(text string) Pattern {
	p := Pattern{kind: patternLine, text: text}
	return p.build()
}

// Compile compiles a Go regular expression. An expression that matches
// empty input is rejected with ErrEmptyMatch.
func Compile(expr string) (Pattern, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		if se, ok := errors.AsType[*syntax.Error](err); ok {
			return Pattern{}, &CompileError{Code: se.Code}
		}
		return Pattern{}, fmt.Errorf("%w: %w", ErrInvalidPattern, err)
	}
	return FromRegexp(re)
}

// MustCompile is like Compile but panics on error. It is for patterns
// known at compile time, typically package-level declarations.
func MustCompile(expr string) Pattern {
	p, err := Compile(expr)
	if err != nil {
		panic(err)
	}
	return p
}

// FromRegexp adopts a compiled expression, keeping its POSIX or Longest
// mode. re is borrowed and must not be changed afterwards. An expression
// that matches empty input is rejected with ErrEmptyMatch.
func FromRegexp(re *regexp.Regexp) (Pattern, error) {
	if re == nil {
		return Pattern{}, configError("pattern", "nil regexp")
	}
	if re.MatchString("") {
		return Pattern{}, ErrEmptyMatch
	}
	return Pattern{kind: patternRegexp, re: re, m: scan.Regexp(re)}, nil
}

// Glob matches an unanchored Tcl-style glob: '*' (any run, newlines
// included), '?', "[a-z]" sets with reversed ranges allowed, and '\'
// escapes. A malformed glob fails when the case is used.
func Glob(pattern string) Pattern {
	p := Pattern{kind: patternGlob, text: pattern}
	return p.build()
}

// All matches when every text pattern has matched, in any order. The match
// runs from the earliest to the latest part; captures are not reported.
func All(patterns ...Pattern) Pattern {
	p := Pattern{kind: patternAll, parts: slices.Clone(patterns)}
	return p.build()
}

// EOF matches a clean end of input after buffered text was matched.
func EOF() Pattern { return Pattern{kind: patternEOF} }

// Timeout matches expiry of the relative wait timer, never cancellation of
// the parent context. Its handler gets a fresh budget and may Continue.
func Timeout() Pattern { return Pattern{kind: patternTimeout} }

// Default matches a relative timeout or a clean EOF.
func Default() Pattern { return Pattern{kind: patternDefault} }

// FullBuffer matches unmatched input that exceeds the buffer limit and
// consumes all of it, instead of failing with ErrBufferLimit.
func FullBuffer() Pattern { return Pattern{kind: patternFullBuffer} }

// Fold returns a case-insensitive copy of a text pattern. A folded regular
// expression uses leftmost-first matching.
func (p Pattern) Fold() Pattern {
	if !p.isText() {
		return p
	}
	q := p
	q.fold, q.parts = true, nil
	for _, part := range p.parts {
		q.parts = append(q.parts, part.Fold())
	}
	if p.kind == patternRegexp {
		re, err := regexp.Compile("(?i)" + p.re.String())
		if err != nil {
			return Pattern{kind: patternRegexp, err: fmt.Errorf("%w: fold", ErrInvalidPattern)}
		}
		q.re = re
		q.m = scan.Regexp(re)
		return q
	}
	return q.build()
}

// IsZero reports whether p was never set.
func (p Pattern) IsZero() bool { return p.kind == patternUnset }

// ValidateText reports why p cannot be used as a text pattern.
func (p Pattern) ValidateText() error {
	if !p.isText() {
		return fmt.Errorf("%w: %s is not a text pattern", ErrInvalidPattern, p.kindName())
	}
	return p.err
}

// String describes the pattern kind and size, not its text.
func (p Pattern) String() string {
	switch p.kind {
	case patternLiteral, patternLine, patternGlob:
		return fmt.Sprintf("expect.Pattern(%s, %d bytes)", p.kindName(), len(p.text))
	case patternAll:
		return fmt.Sprintf("expect.Pattern(all, %d parts)", len(p.parts))
	case patternUnset, patternRegexp, patternEOF, patternTimeout, patternDefault, patternFullBuffer:
	}
	return "expect.Pattern(" + p.kindName() + ")"
}

// GoString is String.
func (p Pattern) GoString() string { return p.String() }

func (p Pattern) kindName() string { return kindNames[p.kind] }

func (p Pattern) isText() bool {
	switch p.kind {
	case patternLiteral, patternLine, patternRegexp, patternGlob, patternAll:
		return true
	case patternUnset, patternEOF, patternTimeout, patternDefault, patternFullBuffer:
	}
	return false
}

// validate reports why p cannot be used in a case.
func (p Pattern) validate() error {
	if p.kind == patternUnset {
		return fmt.Errorf("%w: unset", ErrInvalidPattern)
	}
	return p.err
}

// build compiles the matcher of a literal, line, glob or conjunction.
func (p Pattern) build() Pattern {
	var err error
	switch p.kind {
	case patternLiteral:
		p.m, err = scan.Literal([]byte(p.text), p.fold)
	case patternLine:
		p.m, err = scan.Line([]byte(p.text), p.fold)
	case patternGlob:
		var re *regexp.Regexp
		if re, err = scan.Glob(p.text, p.fold); err == nil {
			p.m = scan.Regexp(re)
			if lit, ok := starLiteral(p.text); ok && !p.fold {
				p.m = scan.StarLiteral(re, []byte(lit))
			}
			if re.MatchString("") {
				err = ErrEmptyMatch
			}
		}
	case patternAll:
		p.m, err = p.buildAll()
	case patternUnset, patternRegexp, patternEOF, patternTimeout, patternDefault, patternFullBuffer:
	}
	if err != nil && !errors.Is(err, ErrInvalidPattern) && !errors.Is(err, ErrEmptyMatch) {
		err = fmt.Errorf("%w: %s: %w", ErrInvalidPattern, p.kindName(), err)
	}
	p.err = err
	return p
}

// starLiteral reports whether glob is "*" followed by plain text, the
// classic prompt glob ("*#", "*router>"), and returns the text.
func starLiteral(glob string) (string, bool) {
	rest := strings.TrimLeft(glob, "*")
	if len(rest) == len(glob) || rest == "" || strings.ContainsAny(rest, `*?[\`) {
		return "", false
	}
	return rest, true
}

func (p Pattern) buildAll() (*scan.Matcher, error) {
	ms := make([]*scan.Matcher, 0, len(p.parts))
	for _, part := range p.parts {
		if err := part.ValidateText(); err != nil {
			return nil, err
		}
		ms = append(ms, part.m)
	}
	return scan.All(ms...)
}
