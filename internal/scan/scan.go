package scan

import (
	"bytes"
	"errors"
	"regexp"
	"regexp/syntax"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Errors returned by the constructors.
var (
	// ErrEmpty rejects an empty literal, line or conjunction.
	ErrEmpty = errors.New("scan: empty pattern")
	// ErrLineBreak rejects a line pattern containing CR or LF.
	ErrLineBreak = errors.New("scan: line pattern contains a line break")
)

type kind uint8

const (
	kindLiteral kind = iota + 1
	kindLine
	kindRegexp
	kindAll
)

// Matcher is an immutable compiled text pattern.
type Matcher struct {
	kind      kind
	lit       []byte         // literal or line text
	fold      bool           // case-insensitive literal or line
	re        *regexp.Regexp // regexp, or the folded form of a literal
	lineLocal bool           // re can never match across a '\n'
	req       []byte         // bytes every match of re contains; nil when unknown
	reqFold   bool           // req is lower-case ASCII compared case-insensitively
	parts     []*Matcher     // conjunction members
}

// Match locates a match in the searched buffer. Offsets are half-open and
// relative to the start of that buffer.
type Match struct {
	Start, End int
	// Groups holds submatch offset pairs, group 0 first; an unmatched
	// group is -1, -1. Literals, lines and conjunctions have group 0 only.
	Groups []int
}

// Limits bound one search.
type Limits struct {
	// Final reports that no more input will arrive, so a trailing
	// incomplete UTF-8 sequence is searched as it is.
	Final bool
	// Window, when positive, restricts the search to the last Window bytes.
	// The window start is then treated as the start of text.
	Window int
}

// State is per-wait search progress for one Matcher. The zero value starts
// at the beginning of the buffer.
type State struct {
	from    int
	reqFrom int // no occurrence of the required literal starts before it
	parts   []State
}

// Literal matches text byte for byte, or case-insensitively when fold is set.
func Literal(text []byte, fold bool) (*Matcher, error) {
	if len(text) == 0 {
		return nil, ErrEmpty
	}
	m := &Matcher{kind: kindLiteral, lit: slices.Clone(text), fold: fold}
	if fold {
		re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(string(text)))
		if err != nil {
			return nil, err
		}
		m.re, m.lineLocal = re, !bytes.ContainsRune(text, '\n')
		m.req, m.reqFold = requiredLiteral(re)
	}
	return m, nil
}

// Line matches text as the final line of the buffer: it must start the
// buffer or follow CR or LF, and only spaces, tabs or NULs may follow it.
func Line(text []byte, fold bool) (*Matcher, error) {
	if len(text) == 0 {
		return nil, ErrEmpty
	}
	if bytes.ContainsAny(text, "\r\n") {
		return nil, ErrLineBreak
	}
	return &Matcher{kind: kindLine, lit: slices.Clone(text), fold: fold}, nil
}

// Regexp searches with re, which the Matcher borrows: re must not be
// changed (for example with Longest) afterwards.
func Regexp(re *regexp.Regexp) *Matcher {
	m := &Matcher{kind: kindRegexp, re: re, lineLocal: isLineLocal(re)}
	m.req, m.reqFold = requiredLiteral(re)
	return m
}

// All matches when every part matches somewhere in the buffer, in any
// order. The match spans from the earliest part start to the latest end.
func All(parts ...*Matcher) (*Matcher, error) {
	if len(parts) == 0 {
		return nil, ErrEmpty
	}
	return &Matcher{kind: kindAll, parts: slices.Clone(parts)}, nil
}

// Names returns the submatch names, indexed like Match.Groups pairs; nil
// when the matcher has no named groups.
func (m *Matcher) Names() []string {
	if m.kind == kindRegexp {
		return m.re.SubexpNames()
	}
	return nil
}

// MatchesEmpty reports whether the pattern matches empty input.
func (m *Matcher) MatchesEmpty() bool {
	if m.kind == kindRegexp {
		return m.re.Match(nil)
	}
	return false
}

// Find returns the earliest match in buf. st records progress so a later
// Find on buf with bytes appended resumes; Reset st after consuming.
func (m *Matcher) Find(buf []byte, st *State, lim Limits) (Match, bool) {
	switch m.kind {
	case kindLiteral:
		if m.fold {
			return m.findRegexp(buf, st, lim)
		}
		return m.findLiteral(buf, st, lim)
	case kindLine:
		return m.findLine(buf)
	case kindRegexp:
		return m.findRegexp(buf, st, lim)
	case kindAll:
		return m.findAll(buf, st, lim)
	}
	return Match{}, false
}

// Hold returns the offset of the shortest suffix of buf that could still
// begin a match once more bytes arrive. Bytes before it can never be part
// of a future match. A pattern that may span lines returns 0.
func (m *Matcher) Hold(buf []byte) int {
	switch m.kind {
	case kindLiteral:
		return len(buf) - prefixSuffix(buf, m.lit, m.fold)
	case kindLine:
		return bytes.LastIndexAny(buf, "\r\n") + 1
	case kindRegexp:
		if !m.lineLocal {
			return 0
		}
		return bytes.LastIndexByte(buf, '\n') + 1
	case kindAll:
		hold := len(buf)
		for _, p := range m.parts {
			hold = min(hold, p.Hold(buf))
		}
		return hold
	}
	return 0
}

// Reset forgets all progress; call it after the buffer was consumed.
func (s *State) Reset() {
	s.from, s.reqFrom = 0, 0
	for i := range s.parts {
		s.parts[i].Reset()
	}
}

func (m *Matcher) findLiteral(buf []byte, st *State, lim Limits) (Match, bool) {
	start := windowStart(st.from, len(buf), lim.Window)
	if i := bytes.Index(buf[start:], m.lit); i >= 0 {
		s := start + i
		st.from = s
		return Match{Start: s, End: s + len(m.lit), Groups: []int{s, s + len(m.lit)}}, true
	}
	st.from = max(start, len(buf)-len(m.lit)+1)
	return Match{}, false
}

func (m *Matcher) findLine(buf []byte) (Match, bool) {
	end := len(bytes.TrimRight(buf, " \t\x00"))
	s := end - len(m.lit)
	if s < 0 {
		return Match{}, false
	}
	text := buf[s:end]
	if m.fold && !bytes.EqualFold(text, m.lit) || !m.fold && !bytes.Equal(text, m.lit) {
		return Match{}, false
	}
	if s > 0 && buf[s-1] != '\n' && buf[s-1] != '\r' {
		return Match{}, false
	}
	return Match{Start: s, End: len(buf), Groups: []int{s, len(buf)}}, true
}

func (m *Matcher) findRegexp(buf []byte, st *State, lim Limits) (Match, bool) {
	end := len(buf)
	if !lim.Final {
		end = completeUTF8(buf)
	}
	from := 0
	if m.lineLocal {
		from = min(st.from, end)
	}
	start := windowStart(from, end, lim.Window)
	if !m.required(buf[:end], st, lim.Window) {
		if m.lineLocal {
			st.from = max(start, bytes.LastIndexByte(buf[:end], '\n')+1)
		}
		return Match{}, false
	}
	loc := m.re.FindSubmatchIndex(buf[start:end])
	if loc == nil {
		if m.lineLocal {
			st.from = max(start, bytes.LastIndexByte(buf[:end], '\n')+1)
		}
		return Match{}, false
	}
	st.from = start
	for i := range loc {
		if loc[i] >= 0 {
			loc[i] += start
		}
	}
	return Match{Start: loc[0], End: loc[1], Groups: loc}, true
}

func (m *Matcher) findAll(buf []byte, st *State, lim Limits) (Match, bool) {
	if len(st.parts) != len(m.parts) {
		st.parts = make([]State, len(m.parts))
	}
	start, end := len(buf), 0
	for i, p := range m.parts {
		pm, ok := p.Find(buf, &st.parts[i], lim)
		if !ok {
			return Match{}, false
		}
		start, end = min(start, pm.Start), max(end, pm.End)
	}
	return Match{Start: start, End: end, Groups: []int{start, end}}, true
}

// required reports whether buf may hold a match: without the required
// literal no match exists, so the regexp need not run. It resumes where
// the previous search on the growing buffer stopped.
func (m *Matcher) required(buf []byte, st *State, window int) bool {
	if m.req == nil {
		return true
	}
	from := windowStart(st.reqFrom, len(buf), window)
	var i int
	if m.reqFold {
		i = indexFoldASCII(buf[from:], m.req)
	} else {
		i = bytes.Index(buf[from:], m.req)
	}
	if i < 0 {
		st.reqFrom = max(from, len(buf)-len(m.req)+1)
		return false
	}
	st.reqFrom = from + i
	return true
}

// indexFoldASCII returns the first offset where s equals the lower-case
// ASCII lit ignoring ASCII case, or -1. When lit holds a byte with no
// case (a digit, space or punctuation), candidates are found with the
// vectorized bytes.IndexByte on it.
func indexFoldASCII(s, lit []byte) int {
	k := anchor(lit)
	if k < 0 {
		first := lit[0]
		for i := 0; i+len(lit) <= len(s); i++ {
			if lowerASCII(s[i]) == first && equalFoldASCII(s[i:i+len(lit)], lit) {
				return i
			}
		}
		return -1
	}
	for from := k; from < len(s); {
		j := bytes.IndexByte(s[from:], lit[k])
		if j < 0 {
			return -1
		}
		at := from + j - k // candidate start
		if at+len(lit) > len(s) {
			return -1
		}
		if equalFoldASCII(s[at:at+len(lit)], lit) {
			return at
		}
		from += j + 1
	}
	return -1
}

// anchor returns the index of a byte of lit that has no case, preferring
// one rarer than a space, or -1.
func anchor(lit []byte) int {
	best := -1
	for i, c := range lit {
		if lowerASCII(c) != c || 'a' <= c && c <= 'z' {
			continue
		}
		if c != ' ' {
			return i
		}
		if best < 0 {
			best = i
		}
	}
	return best
}

func equalFoldASCII(a, lower []byte) bool {
	for i, c := range lower {
		if lowerASCII(a[i]) != c {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// requiredLiteral returns the longest literal that every match of re
// contains, found along the top-level concatenation. A case-folded literal
// keeps only runes whose whole fold orbit is ASCII, lower-cased, so a
// byte-wise ASCII comparison is exact. Literal U+FFFD is skipped because
// it also matches invalid UTF-8 input.
func requiredLiteral(re *regexp.Regexp) ([]byte, bool) {
	t, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return nil, false
	}
	var best []byte
	var bestFold bool
	consider := func(seg []byte, fold bool) {
		if len(seg) > len(best) {
			best, bestFold = seg, fold
		}
	}
	var walk func(r *syntax.Regexp)
	walk = func(r *syntax.Regexp) {
		switch r.Op { //nolint:exhaustive // only operators whose operand every match contains
		case syntax.OpCapture, syntax.OpPlus:
			walk(r.Sub[0])
		case syntax.OpRepeat:
			if r.Min >= 1 {
				walk(r.Sub[0])
			}
		case syntax.OpConcat:
			for _, sub := range r.Sub {
				walk(sub)
			}
		case syntax.OpLiteral:
			fold := r.Flags&syntax.FoldCase != 0
			var seg []byte
			for _, c := range r.Rune {
				if ok, lower := literalRune(c, fold); ok {
					seg = utf8.AppendRune(seg, lower)
					continue
				}
				consider(seg, fold)
				seg = nil
			}
			consider(seg, fold)
		}
	}
	walk(t)
	return best, bestFold
}

// literalRune reports whether c can take part in a required literal and
// returns the form to search for.
func literalRune(c rune, fold bool) (bool, rune) {
	if c == utf8.RuneError {
		return false, c
	}
	if !fold {
		return true, c
	}
	for f := unicode.SimpleFold(c); ; f = unicode.SimpleFold(f) {
		if f >= utf8.RuneSelf {
			return false, c
		}
		if f == c {
			break
		}
	}
	if c >= utf8.RuneSelf {
		return false, c
	}
	return true, rune(lowerASCII(byte(c)))
}

func windowStart(from, end, window int) int {
	if window > 0 {
		from = max(from, end-window)
	}
	return max(0, min(from, end))
}

// prefixSuffix returns the length of the longest proper prefix of lit that
// ends buf.
func prefixSuffix(buf, lit []byte, fold bool) int {
	for k := min(len(lit)-1, len(buf)); k > 0; k-- {
		tail, head := buf[len(buf)-k:], lit[:k]
		if fold && bytes.EqualFold(tail, head) || !fold && bytes.Equal(tail, head) {
			return k
		}
	}
	return 0
}

// completeUTF8 returns the length of buf without a trailing incomplete
// UTF-8 sequence.
func completeUTF8(buf []byte) int {
	n := len(buf)
	for i := n - 1; i >= 0 && n-i <= utf8.UTFMax; i-- {
		if !utf8.RuneStart(buf[i]) {
			continue
		}
		if utf8.FullRune(buf[i:]) {
			return n
		}
		return i
	}
	return n
}

// isLineLocal reports whether re provably never matches across a '\n' and
// never anchors at the start of text, so a search may resume at the start
// of the last line.
func isLineLocal(re *regexp.Regexp) bool {
	t, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return false
	}
	return !crossesLines(t)
}

func crossesLines(r *syntax.Regexp) bool {
	switch r.Op { //nolint:exhaustive // only operators that can consume '\n' or anchor at text start matter
	case syntax.OpBeginText, syntax.OpAnyChar:
		return true
	case syntax.OpLiteral:
		if slices.Contains(r.Rune, '\n') {
			return true
		}
	case syntax.OpCharClass:
		for i := 0; i+1 < len(r.Rune); i += 2 {
			if r.Rune[i] <= '\n' && '\n' <= r.Rune[i+1] {
				return true
			}
		}
	}
	return slices.ContainsFunc(r.Sub, crossesLines)
}
