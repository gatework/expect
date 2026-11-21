package redact

import (
	"bytes"
	"regexp"
	"testing"
)

func filter(tb testing.TB, secrets ...string) *Filter {
	tb.Helper()
	f := New("")
	if err := f.AddSecrets(secrets...); err != nil {
		tb.Fatal(err)
	}
	return f
}

func stream(f *Filter, chunks ...string) string {
	var out []byte
	for _, c := range chunks {
		out = append(out, f.Append([]byte(c))...)
	}
	return string(append(out, f.Finish()...))
}

func TestFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		secrets []string
		chunks  []string
		want    string
	}{
		{"whole", []string{"s3cret"}, []string{"pw s3cret ok"}, "pw ****** ok"},
		{"split", []string{"s3cret"}, []string{"pw s3", "cr", "et ok"}, "pw ****** ok"},
		{"overlap merged", []string{"abc", "bcd"}, []string{"xab", "cdx"}, "x******x"},
		{"straddle", []string{"abc", "bcd"}, []string{"xabc", "x"}, "x******x"},
		{"prefix at end", []string{"s3cret"}, []string{"pw s3c"}, "pw ******"},
		{"no secret", nil, []string{"plain"}, "plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stream(filter(t, tt.secrets...), tt.chunks...); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilter_Rule(t *testing.T) {
	t.Parallel()
	f := New("")
	f.AddRule(regexp.MustCompile(`password cipher (\S+)`))
	got := stream(f, " local-user admin password ci", "pher %^%#abc%^%#\n undo ok\n")
	if want := " local-user admin password cipher ******\n undo ok\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFilter_RejectsEmpty(t *testing.T) {
	t.Parallel()
	if err := New("").AddSecrets(""); err == nil {
		t.Fatal("empty secret accepted")
	}
}

// FuzzFilterChunks checks that no split of the stream lets a secret
// through and that the split never changes the output.
func FuzzFilterChunks(f *testing.F) {
	f.Add([]byte("user admin pass s3cret\n"), uint8(3))
	f.Add([]byte("abcbcdab"), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		whole := stream(filter(t, "s3cret", "abc", "bcd"), string(data))
		fl := filter(t, "s3cret", "abc", "bcd")
		var out []byte
		n := int(step)%5 + 1
		for i := 0; i < len(data); i += n {
			out = append(out, fl.Append(data[i:min(i+n, len(data))])...)
		}
		out = append(out, fl.Finish()...)
		if string(out) != whole {
			t.Fatalf("chunked %q, whole %q", out, whole)
		}
		for _, s := range []string{"s3cret", "abc", "bcd"} {
			if bytes.Contains(out, []byte(s)) {
				t.Fatalf("secret %q leaked in %q", s, out)
			}
		}
	})
}
