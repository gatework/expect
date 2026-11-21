package scan

import (
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
	}
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
			if ok != wantOK || ok && (got.Start != want.Start || got.End != want.End) {
				t.Fatalf("prefix %d: resumed %+v %v, fresh %+v %v", end, got, ok, want, wantOK)
			}
			if ok {
				return
			}
		}
	})
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
