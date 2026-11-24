package scan

import (
	"bytes"
	"math/rand/v2"
	"regexp"
	"testing"
)

func mustLiteral(tb testing.TB, s string, fold bool) *Matcher {
	tb.Helper()
	m, err := Literal([]byte(s), fold)
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

func TestMatcher_Find(t *testing.T) {
	t.Parallel()
	line, _ := Line([]byte("router#"), false)
	all, _ := All(mustLiteral(t, "b", false), mustLiteral(t, "a", false))
	glob, err := Glob("sw*[0-9]#", false)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		m          *Matcher
		buf        string
		start, end int
		ok         bool
	}{
		{"literal", mustLiteral(t, "word:", false), "Pass word:", 5, 10, true},
		{"literal miss", mustLiteral(t, "word:", false), "Pass wor", 0, 0, false},
		{"literal fold", mustLiteral(t, "PASSWORD:", true), "password:", 0, 9, true},
		{"line final", line, "show\r\nrouter# \x00", 6, 15, true},
		{"line not final", line, "router#\r\nmore", 0, 0, false},
		{"line mid", line, "xrouter#", 0, 0, false},
		{"regexp", Regexp(regexp.MustCompile(`(?m)^(?P<host>\S+)#$`)), "a\nsw1#", 2, 6, true},
		{"all any order", all, "xaxb", 1, 4, true},
		{"glob", Regexp(glob), "hello\nsw-core9#", 6, 15, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var st State
			m, ok := tt.m.Find([]byte(tt.buf), &st, Limits{})
			if ok != tt.ok || ok && (m.Start != tt.start || m.End != tt.end) {
				t.Fatalf("Find = %+v, %v; want [%d,%d) %v", m, ok, tt.start, tt.end, tt.ok)
			}
		})
	}
}

func TestMatcher_FindWaitsForCompleteRune(t *testing.T) {
	t.Parallel()
	m := Regexp(regexp.MustCompile(`.$`))
	buf := []byte("ab\xe4\xbd")
	var st State
	got, ok := m.Find(buf, &st, Limits{})
	if !ok || got.End != 2 {
		t.Fatalf("partial rune searched: %+v %v", got, ok)
	}
	got, ok = m.Find(append(buf, 0xa0), &st, Limits{})
	if !ok || got.Start != 2 {
		t.Fatalf("complete rune: %+v %v", got, ok)
	}
}

func TestMatcher_Window(t *testing.T) {
	t.Parallel()
	m := Regexp(regexp.MustCompile(`(?s)x.*#`))
	var st State
	if _, ok := m.Find([]byte("x......#"), &st, Limits{Window: 3}); ok {
		t.Fatal("window ignored")
	}
}

func TestMatcher_Hold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    *Matcher
		buf  string
		want int
	}{
		{"literal prefix", mustLiteral(t, "~.", false), "abc~", 3},
		{"literal none", mustLiteral(t, "~.", false), "abc", 3},
		{"line-local regexp", Regexp(regexp.MustCompile(`a+b`)), "x\naa", 2},
		{"spanning regexp", Regexp(regexp.MustCompile(`(?s)a.b`)), "x\naa", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.m.Hold([]byte(tt.buf)); got != tt.want {
				t.Fatalf("Hold = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		glob, text string
		fold, ok   bool
	}{
		{"a*b", "xa\nyb", false, true},
		{"[z-a]", "m", false, true},
		{"[A-C]x", "bx", true, true},
		{`\*`, "*", false, true},
		{"a?c", "abc", false, true},
	}
	for _, tt := range tests {
		re, err := Glob(tt.glob, tt.fold)
		if err != nil {
			t.Fatalf("%q: %v", tt.glob, err)
		}
		if got := re.MatchString(tt.text); got != tt.ok {
			t.Errorf("%q on %q = %v", tt.glob, tt.text, got)
		}
	}
	for _, bad := range []string{"[ab", `a\`, "[]"} {
		if _, err := Glob(bad, false); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// FuzzFindChunks checks that a search resumed across any split of the
// input finds exactly what a fresh search of the same prefix finds.
func FuzzFindChunks(f *testing.F) {
	f.Add([]byte("show run\nrouter#"), uint8(3), uint8(0))
	f.Add([]byte("a\xe4\xbd\xa0b#"), uint8(1), uint8(1))
	f.Add([]byte("Password: "), uint8(2), uint8(2))
	matchers := []*Matcher{
		Regexp(regexp.MustCompile(`(?m)^\S+#$`)),
		Regexp(regexp.MustCompile(`.b#`)),
		mustLiteral(f, "word:", false),
		mustLiteral(f, "WORD", true),
		mustLiteral(f, "sk:", true),
		mustGlob(f, "*r#"),
		mustStarLiteral(f, "*r#", "r#"),
		mustStarLiteral(f, "**aa", "aa"),
		Regexp(regexp.MustCompile(`(?i)ques?tion \[y/n\]`)),
		Regexp(regexp.MustCompile(`(?s)Bu.*\[OK\]`)),
		Regexp(regexp.MustCompile("a\uFFFDb")),
	}
	f.Add([]byte("Are you SURE? [Y/N]"), uint8(4), uint8(7))
	f.Add([]byte("x\xe2\x84\xaak:"), uint8(1), uint8(4))
	f.Add([]byte("a\xffb"), uint8(1), uint8(8))
	f.Add([]byte("xr#yr#\xe4r#"), uint8(2), uint8(9))
	f.Add([]byte("aaaab"), uint8(1), uint8(10))
	f.Fuzz(func(t *testing.T, data []byte, step, which uint8) {
		m := matchers[int(which)%len(matchers)]
		var st State
		n := int(step)%7 + 1
		for end := 0; end < len(data); {
			end = min(end+n, len(data))
			lim := Limits{Final: end == len(data)}
			var fresh State
			want, wantOK := m.Find(data[:end], &fresh, lim)
			got, ok := m.Find(data[:end], &st, lim)
			if m.re != nil {
				// The prefilter must never hide a match the regexp finds.
				e := end
				if !lim.Final {
					e = completeUTF8(data[:end])
				}
				if ref := m.re.FindIndex(data[:e]); (ref != nil) != wantOK || wantOK && (ref[0] != want.Start || ref[1] != want.End) {
					t.Fatalf("prefix %d: matcher %+v %v, regexp %v", end, want, wantOK, ref)
				}
			}
			if ok != wantOK || ok && (got.Start != want.Start || got.End != want.End) {
				t.Fatalf("prefix %d: resumed %+v %v, fresh %+v %v", end, got, ok, want, wantOK)
			}
			if ok {
				return
			}
		}
	})
}

func mustStarLiteral(tb testing.TB, glob, lit string) *Matcher {
	tb.Helper()
	re, err := Glob(glob, false)
	if err != nil {
		tb.Fatal(err)
	}
	return StarLiteral(re, []byte(lit))
}

func mustGlob(tb testing.TB, pattern string) *Matcher {
	tb.Helper()
	re, err := Glob(pattern, false)
	if err != nil {
		tb.Fatal(err)
	}
	return Regexp(re)
}

func TestRequiredLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expr, want string
		fold       bool
	}{
		{`(?m)^\S+#$`, "#", false},
		{`(?s:.*router#)`, "router#", false},
		{`(?i)question 3 \[Y/N\]:`, "tion 3 [y/n]:", true}, // 's' folds to U+017F
		{`(?i)Kelvin`, "elvin", true},                      // 'k' folds to U+212A
		{`a|b`, "", false},
		{`(?:ab)?c`, "c", false},
		{`(x(yz)+)`, "yz", false},
	}
	for _, tt := range tests {
		got, fold := requiredLiteral(regexp.MustCompile(tt.expr))
		if string(got) != tt.want || len(got) > 0 && fold != tt.fold {
			t.Errorf("%s: got %q fold %v, want %q fold %v", tt.expr, got, fold, tt.want, tt.fold)
		}
	}
}

func BenchmarkFind_LargeOutput(b *testing.B) {
	m := Regexp(regexp.MustCompile(`(?m)^\S+#[ \t]*$`))
	chunk := []byte("interface GigabitEthernet0/1\n description uplink\n")
	var buf []byte
	for b.Loop() {
		buf = buf[:0]
		var st State
		for range 2000 {
			buf = append(buf, chunk...)
			m.Find(buf, &st, Limits{})
		}
	}
}

func benchFind(b *testing.B, m *Matcher) {
	b.Helper()
	chunk := []byte("interface GigabitEthernet0/1\n description uplink\n")
	var buf []byte
	for b.Loop() {
		buf = buf[:0]
		var st State
		for range 2000 {
			buf = append(buf, chunk...)
			m.Find(buf, &st, Limits{})
		}
	}
}

func BenchmarkFind_Glob(b *testing.B) {
	re, err := Glob("*router#", false)
	if err != nil {
		b.Fatal(err)
	}
	benchFind(b, Regexp(re))
}

func BenchmarkFind_DotAll(b *testing.B) {
	benchFind(b, Regexp(regexp.MustCompile(`(?s)Building.*\[OK\]`)))
}

// TestIndexFoldASCII compares the anchored search with a naive one.
func TestIndexFoldASCII(t *testing.T) {
	t.Parallel()
	naive := func(s, lit []byte) int {
		for i := 0; i+len(lit) <= len(s); i++ {
			if bytes.EqualFold(s[i:i+len(lit)], lit) {
				return i
			}
		}
		return -1
	}
	rng := rand.New(rand.NewPCG(3, 4))
	alphabet := []byte("aAbB 1[]:yYnN/")
	for range 20000 {
		lit := make([]byte, 1+rng.IntN(5))
		for i := range lit {
			lit[i] = lowerASCII(alphabet[rng.IntN(len(alphabet))])
		}
		s := make([]byte, rng.IntN(40))
		for i := range s {
			s[i] = alphabet[rng.IntN(len(alphabet))]
		}
		if got, want := indexFoldASCII(s, lit), naive(s, lit); got != want {
			t.Fatalf("indexFoldASCII(%q, %q) = %d, want %d", s, lit, got, want)
		}
	}
}
