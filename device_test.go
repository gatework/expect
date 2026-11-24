package expect_test

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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

// TestSettle_PromptInOutput cuts the output right after a prompt-like
// string: the Settle prompt must wait for the real prompt.
func TestSettle_PromptInOutput(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			_, _ = io.WriteString(p.conn, "interface GE1\r\n description to core#")
			time.Sleep(20 * time.Millisecond) // the next chunk arrives within Settle
			_, _ = io.WriteString(p.conn, "1 uplink\r\n#\r\nreturn\r\ncore#")
		}()
		prompt := expect.Case{Name: "prompt", Pattern: expect.MustCompile(`[\w.-]+#\s*$`), Settle: 100 * time.Millisecond}
		start := time.Now()
		r, err := s.Expect(t.Context(), []expect.Case{prompt})
		if err != nil {
			t.Fatal(err)
		}
		if r.Match != "core#" || r.Before != "interface GE1\r\n description to core#1 uplink\r\n#\r\nreturn\r\n" {
			t.Fatalf("before %q match %q", r.Before, r.Match)
		}
		if d := time.Since(start); d != 120*time.Millisecond {
			t.Fatalf("fired after %v, want the last chunk plus Settle", d)
		}
	})
}

// TestSettle_WithoutSettleFiresEarly documents the failure Settle fixes.
func TestSettle_WithoutSettleFiresEarly(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			_, _ = io.WriteString(p.conn, " description to core#")
			time.Sleep(20 * time.Millisecond)
			_, _ = io.WriteString(p.conn, "1 uplink\r\ncore#")
		}()
		r, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.MustCompile(`[\w.-]+#\s*$`)}})
		if err != nil || r.Match != "core#" || r.Before != " description to " {
			t.Fatalf("before %q match %q, %v", r.Before, r.Match, err)
		}
		_, _ = s.Expect(t.Context(), []expect.Case{lit("rest", "core#")})
	})
}

// TestSettle_EndOfInputConfirms checks that EOF confirms at once and that
// Poll does not wait.
func TestSettle_EndOfInputConfirms(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			_, _ = io.WriteString(p.conn, "bye\r\nr1#")
			_ = p.conn.Close()
		}()
		prompt := expect.Case{Pattern: expect.Literal("r1#"), Settle: time.Hour}
		start := time.Now()
		if _, err := s.Expect(t.Context(), []expect.Case{prompt}); err != nil || time.Since(start) != 0 {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		p.say("r1#")
		synctest.Wait()
		if _, err := s.Poll(t.Context(), []expect.Case{{Pattern: expect.Literal("r1#"), Settle: time.Hour}}); err != nil {
			t.Fatalf("Poll waited for Settle: %v", err)
		}
	})
}

func TestSettle_Rejected(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	for _, c := range []expect.Case{{Pattern: expect.EOF(), Settle: time.Second}, {Pattern: expect.Literal("x"), Settle: -1}} {
		if _, err := s.Poll(t.Context(), []expect.Case{c}); !errors.As(err, new(*expect.ConfigError)) {
			t.Errorf("accepted: %v", err)
		}
	}
}

// TestSettle_Group settles a prompt on one member while another is quiet.
func TestSettle_Group(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		a, _ := pipe(t)
		b, pb := pipe(t)
		g, err := expect.NewGroup(a, b)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = io.WriteString(pb.conn, "standby#")
			time.Sleep(10 * time.Millisecond)
			_, _ = io.WriteString(pb.conn, "1\r\nsb#")
		}()
		start := time.Now()
		m, r, err := g.Expect(t.Context(), []expect.Case{{Pattern: expect.MustCompile(`\w+#$`), Settle: 50 * time.Millisecond}})
		if err != nil || m != b || r.Match != "sb#" || time.Since(start) != 60*time.Millisecond {
			t.Fatalf("member %v match %q after %v: %v", m == b, r.Match, time.Since(start), err)
		}
	})
}

// TestEcho_StaleOutput leaves late output and a prompt of the previous
// command pending: without WithEcho the Exchange would return them as the
// answer to the new command.
func TestEcho_StaleOutput(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		prompt := []expect.Case{{Name: "prompt", Pattern: expect.Literal("r1#"), Omit: true}}
		_, _ = io.WriteString(p.conn, "late output of the previous command\r\nr1#")
		go func() {
			p.hear("show clock\n")
			_, _ = io.WriteString(p.conn, "show clock\r\n10:00:00 CST\r\nr1#")
		}()
		r, err := s.Exchange(t.Context(), "show clock", prompt, expect.WithEcho())
		if err != nil {
			t.Fatal(err)
		}
		if r.Output != "\r\n10:00:00 CST\r\n" {
			t.Fatalf("output %q", r.Output)
		}
	})
}

// TestEcho_RetryAfterTimeout retries a command whose first echo is still
// pending: the stale echo must not satisfy the retry.
func TestEcho_RetryAfterTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		prompt := []expect.Case{{Name: "prompt", Pattern: expect.Literal("r1#"), Omit: true}}
		go func() {
			p.hear("show run\n")
			_, _ = io.WriteString(p.conn, "show run\r\nBuilding configuration...")
			p.hear("show run\n")
			_, _ = io.WriteString(p.conn, "\r\nhostname r1\r\nr1#show run\r\nhostname r1\r\nr1#")
		}()
		if _, err := s.Exchange(t.Context(), "show run", prompt, expect.WithEcho(), expect.WithTimeout(time.Second)); !errors.Is(err, expect.ErrTimeout) {
			t.Fatalf("first try: %v", err)
		}
		r, err := s.Exchange(t.Context(), "show run", prompt, expect.WithEcho(), expect.WithTimeout(time.Second))
		if err != nil || r.Output != "\r\nhostname r1\r\n" {
			t.Fatalf("retry: %q, %v", r.Output, err)
		}
	})
}

func TestEcho_Missing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			p.hear("secret\n")
			_, _ = io.WriteString(p.conn, "\r\nr1#")
		}()
		_, err := s.Exchange(t.Context(), "secret", []expect.Case{lit("prompt", "r1#")}, expect.WithEcho(), expect.WithTimeout(time.Second))
		if !errors.Is(err, expect.ErrNoEcho) || !errors.Is(err, expect.ErrTimeout) {
			t.Fatalf("got %v", err)
		}
	})
}

// TestDrain discards a burst of late output once the peer is quiet, and
// times out on a peer that never is.
func TestDrain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			for range 3 {
				_, _ = io.WriteString(p.conn, "syslog line\r\n")
				time.Sleep(50 * time.Millisecond)
			}
		}()
		r, err := s.Drain(t.Context(), 200*time.Millisecond)
		if err != nil || r.Before != strings.Repeat("syslog line\r\n", 3) {
			t.Fatalf("drained %q, %v", r.Before, err)
		}
		if r, _ := s.Poll(t.Context(), nil); r.Before != "" {
			t.Fatalf("left %q", r.Before)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, err := io.WriteString(p.conn, "."); err != nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		_, err = s.Drain(t.Context(), 100*time.Millisecond, expect.WithTimeout(time.Second))
		_ = p.conn.Close()
		<-done
		if !errors.Is(err, expect.ErrTimeout) {
			t.Fatalf("chatty peer: %v", err)
		}
		if _, err := s.Drain(t.Context(), 0); !errors.As(err, new(*expect.ConfigError)) {
			t.Fatalf("zero quiet: %v", err)
		}
	})
}
