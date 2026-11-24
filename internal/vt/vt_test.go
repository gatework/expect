package vt

import (
	"bytes"
	"testing"
)

func TestFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, buf, in, want string
		rewrote             bool
	}{
		{"plain", "", "show version\r\n", "show version\r\n", false},
		{"color prompt", "", "\x1b[1;32mrouter\x1b[0m#", "router#", false},
		{"erase line", "", "abc\x1b[K\r\n", "abc\r\n", false},
		{"osc title bel", "", "\x1b]0;router: ~\x07router#", "router#", false},
		{"osc title st", "", "\x1b]2;t\x1b\\ok", "ok", false},
		{"charset select", "", "\x1b(Bok", "ok", false},
		{"keypad mode", "", "\x1b=ok\x1b>", "ok", false},
		{"cisco pager erase", "line\n --More-- ", "\b\b\b\b\b\b\b\b\b\b          \b\b\b\b\b\b\b\b\b\bnext", "line\nnext", true},
		{"huawei pager erase", "  ---- More ----", "\x1b[16D                \x1b[16Dnext", "next", true},
		{"erase stops at line feed", "ab\n", "\b\b\bc", "ab\nc", false},
		{"cursor left without count", "ab", "\x1b[Dc", "ac", true},
		{"cursor position is not erase", "ab", "\x1b[1;5Hc", "abc", false},
		{"erase multibyte rune", "你好", "\b", "你", true},
		{"esc esc", "", "\x1b\x1b[0mok", "ok", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var f Filter
			got, rewrote := f.Append([]byte(tt.buf), []byte(tt.in))
			if string(got) != tt.want || rewrote != tt.rewrote {
				t.Fatalf("got %q rewrote %v, want %q %v", got, rewrote, tt.want, tt.rewrote)
			}
		})
	}
}

// FuzzFilterChunks checks that splitting the input anywhere gives the same
// text as filtering it whole, and that no control byte the filter owns
// survives.
func FuzzFilterChunks(f *testing.F) {
	f.Add([]byte("\x1b[1;32mr1\x1b[0m# --More-- \b\b\b  \b\b\x1b]0;t\x07ok\x1b[3D"), uint8(3))
	f.Add([]byte("  ---- More ----\x1b[16D                \x1b[16Dx"), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		var whole Filter
		want, _ := whole.Append(nil, data)
		var split Filter
		var got []byte
		n := int(step)%7 + 1
		for at := 0; at < len(data); at += n {
			got, _ = split.Append(got, data[at:min(at+n, len(data))])
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("split %d: %q, whole %q", n, got, want)
		}
		if bytes.ContainsAny(want, "\x1b\b") {
			t.Fatalf("control byte left in %q", want)
		}
	})
}
