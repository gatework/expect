package expect_test

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

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

// TestCharset_GB18030 plays a device that speaks GB18030: a Chinese
// confirmation split inside a character is matched and answered, Chinese
// text is sent encoded, and the transcript reads as UTF-8.
func TestCharset_GB18030(t *testing.T) {
	t.Parallel()
	gb := func(s string) []byte {
		b, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	c := record.DefaultConfig()
	c.Dir = t.TempDir()
	rec, err := record.New(c)
	if err != nil {
		t.Fatal(err)
	}
	s, p := pipe(t, expect.WithCharset(simplifiedchinese.GB18030), expect.WithRecorder(rec))
	question := gb("确认清除接口配置吗？[Y/N]:")
	go func() {
		_, _ = p.conn.Write(question[:3]) // splits the second character
		_, _ = p.conn.Write(question[3:])
		p.hear("Y\n")
		_, _ = p.conn.Write(gb("\r\n成功\r\n<H3C>"))
		p.hear(string(gb("description 上联核心\n")))
		_, _ = p.conn.Write([]byte("<H3C>"))
	}()
	r, err := s.Expect(t.Context(), []expect.Case{
		{Name: "confirm", Pattern: expect.Literal("确认清除"), Handler: expect.ReplyLineThenContinue("Y")},
		{Name: "prompt", Pattern: expect.Literal("<H3C>")},
	})
	if err != nil || r.Before != "接口配置吗？[Y/N]:\r\n成功\r\n" {
		t.Fatalf("got %q, %v", r.Before, err)
	}
	if _, err := s.Exchange(t.Context(), "description 上联核心", []expect.Case{{Pattern: expect.Literal("<H3C>")}}); err != nil {
		t.Fatal(err)
	}
	paths := s.Transcript()
	_ = s.Close()
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "清除接口配置吗") || !strings.Contains(string(b), "上联核心") {
			t.Fatalf("%s does not read as UTF-8: %q", path, b)
		}
	}
}

func TestCharset_Unencodable(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t, expect.WithCharset(simplifiedchinese.GBK))
	if _, err := s.Send(t.Context(), "emoji 😀"); !errors.Is(err, expect.ErrUnencodable) {
		t.Fatalf("got %v, want ErrUnencodable", err)
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	if _, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithCharset(nil)); !errors.As(err, new(*expect.ConfigError)) {
		t.Fatalf("nil charset: %v", err)
	}
}
