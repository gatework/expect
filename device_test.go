package expect_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gatework/expect"
	"github.com/gatework/expect/record"
)

// TestTerminalFilter_HuaweiPager plays a Huawei-style pager that erases
// itself with cursor-left after the answer, and a colored prompt.
func TestTerminalFilter_HuaweiPager(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := record.DefaultConfig()
	c.Dir = dir
	rec, err := record.New(c)
	if err != nil {
		t.Fatal(err)
	}
	s, p := pipe(t, expect.WithTerminalFilter(), expect.WithRecorder(rec))
	p.say("display interface brief\r\nGE0/0/1 up\r\n  ---- More ----")
	go func() {
		p.hear(" ")
		p.say("\x1b[42D                                          \x1b[42DGE0/0/2 down\r\n\x1b[1;32m<HUAWEI>\x1b[0m")
	}()
	r, err := s.Expect(t.Context(), []expect.Case{
		{Name: "more", Pattern: expect.Literal("---- More ----"), Omit: true, Handler: expect.ReplyThenContinue(" ")},
		{Name: "prompt", Pattern: expect.Line("<HUAWEI>"), Omit: true},
	}, expect.WithOutput())
	if err != nil {
		t.Fatal(err)
	}
	if want := "display interface brief\r\nGE0/0/1 up\r\n  GE0/0/2 down\r\n"; r.Output != want || r.Match != "<HUAWEI>" {
		t.Fatalf("output %q match %q, want %q", r.Output, r.Match, want)
	}
	paths := s.Transcript()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var castPath string
	for _, p := range paths {
		if strings.HasSuffix(p, ".cast") {
			castPath = p
		}
	}
	cast, err := os.ReadFile(castPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cast), `\u001b[42D`) {
		t.Fatalf("transcript lost the raw control sequences: %s", cast)
	}
}

// TestTerminalFilter_InteractRaw checks that an operator still receives
// the control sequences.
func TestTerminalFilter_InteractRaw(t *testing.T) {
	t.Parallel()
	s, p := pipe(t, expect.WithTerminalFilter())
	opIn, _ := io.Pipe()
	var out syncBuffer
	p.say("\x1b[1mr1#\x1b[0m")
	err := s.Interact(t.Context(), opIn, &out, expect.WithInteractOutputCases(lit("prompt", "#\x1b[0m")))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "\x1b[1mr1#\x1b[0m" {
		t.Fatalf("operator saw %q", got)
	}
}
