package expect_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"github.com/gatework/expect"
	"github.com/gatework/expect/record"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// peer is the device side of a net.Pipe.
type peer struct {
	t    testing.TB
	conn net.Conn
}

func pipe(tb testing.TB, opts ...expect.SessionOption) (*expect.Session, *peer) {
	tb.Helper()
	local, remote := net.Pipe()
	s, err := expect.NewSession(local, append([]expect.SessionOption{expect.WithoutRecording()}, opts...)...)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close(); _ = remote.Close() })
	return s, &peer{t: tb, conn: remote}
}

// say writes text from the device in the background.
func (p *peer) say(text string) {
	go func() { _, _ = p.conn.Write([]byte(text)) }()
}

// hear reads exactly len(want) bytes and checks them.
func (p *peer) hear(want string) {
	p.t.Helper()
	b := make([]byte, len(want))
	if _, err := io.ReadFull(p.conn, b); err != nil {
		p.t.Fatalf("peer read: %v", err)
	}
	if string(b) != want {
		p.t.Fatalf("peer heard %q, want %q", b, want)
	}
}

func lit(name, text string) expect.Case {
	return expect.Case{Name: name, Pattern: expect.Literal(text)}
}

func TestSession_ExpectReply(t *testing.T) {
	t.Parallel()
	s, p := pipe(t, expect.WithLineEnding("\r"))
	p.say("Username: ")
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.hear("admin\r")
		p.say("Password: ")
		p.hear("secret\r")
		p.say("\r\nrouter#")
	}()
	r, err := s.Expect(t.Context(), []expect.Case{
		{Name: "user", Pattern: expect.Literal("Username:"), Handler: expect.Then(expect.ReplyLine("admin"))},
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine("secret"))},
		{Name: "prompt", Pattern: expect.MustCompile(`(?m)^(?P<host>[\w-]+)#$`)},
	})
	<-done
	if err != nil {
		t.Fatal(err)
	}
	host, ok := r.Capture("host")
	if r.Name != "prompt" || r.CaseIndex != 2 || !ok || host.Text != "router" {
		t.Fatalf("result %+v", r)
	}
	if a, b, ok := r.Indices(1); !ok || r.Buffer()[a:b] != "router" {
		t.Fatalf("indices %d %d %v", a, b, ok)
	}
}

func TestSession_Priority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		opts  []expect.MatchOption
		rules []expect.Case
		want  string
	}{
		{"earliest wins", nil, nil, "a"},
		{"declaration order", []expect.MatchOption{expect.WithMatchOrder(expect.Declaration)}, nil, "b"},
		{"before rule wins", nil, []expect.Case{lit("rule", "C")}, "rule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, p := pipe(t, expect.WithBefore(tt.rules...))
			p.say("xxAyyBzzC")
			r, err := s.Expect(t.Context(), []expect.Case{lit("b", "B"), lit("a", "A"), lit("c", "C")}, append(tt.opts, expect.WithTimeout(time.Second))...)
			if err != nil {
				t.Fatal(err)
			}
			if r.Name != tt.want {
				t.Fatalf("matched %q, want %q", r.Name, tt.want)
			}
		})
	}
}

func TestSession_TimeoutKeepsInput(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		p.say("partial ")
		synctest.Wait()
		r, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")}, expect.WithTimeout(time.Second))
		if !errors.Is(err, expect.ErrTimeout) || r.Before != "partial " || r.CaseIndex != -1 {
			t.Fatalf("got %+v, %v", r, err)
		}
		p.say("router#")
		r, err = s.Expect(t.Context(), []expect.Case{lit("prompt", "#")})
		if err != nil || r.Before != "partial router" {
			t.Fatalf("second wait: %+v, %v", r, err)
		}
	})
}

func TestSession_TimeoutCase(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t, expect.WithLineEnding("\r"))
		go func() {
			p.hear("\r")
			p.say("router#")
		}()
		r, err := s.Expect(t.Context(), []expect.Case{
			lit("prompt", "#"),
			{Name: "nudge", Pattern: expect.Timeout(), Handler: func(ctx context.Context, s *expect.Session, _ expect.Result) (expect.Action, error) {
				_, err := s.SendLine(ctx, "")
				return expect.Continue, err
			}},
		}, expect.WithTimeout(5*time.Second))
		if err != nil || r.Name != "prompt" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
}

func TestSession_IdleTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go func() {
			for range 3 {
				_, _ = p.conn.Write([]byte("line\n"))
				time.Sleep(time.Second)
			}
		}()
		start := time.Now()
		_, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")}, expect.WithTimeout(time.Minute), expect.WithIdleTimeout(2*time.Second))
		if !errors.Is(err, expect.ErrIdleTimeout) || time.Since(start) < 4*time.Second {
			t.Fatalf("err %v after %v", err, time.Since(start))
		}
	})
}

func TestSession_EOF(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	go func() {
		_, _ = p.conn.Write([]byte("bye"))
		_ = p.conn.Close()
	}()
	r, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")})
	if !errors.Is(err, io.EOF) || r.Before != "bye" {
		t.Fatalf("no EOF case: %+v, %v", r, err)
	}
	r, err = s.Expect(t.Context(), []expect.Case{{Name: "end", Pattern: expect.EOF()}})
	if err != nil || r.Name != "end" || r.Before != "bye" {
		t.Fatalf("EOF case: %+v, %v", r, err)
	}
}

func TestSession_BufferLimit(t *testing.T) {
	t.Parallel()
	s, p := pipe(t, expect.WithBufferLimit(8))
	p.say("0123456789")
	_, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")}, expect.WithTimeout(time.Second))
	if !errors.Is(err, expect.ErrBufferLimit) {
		t.Fatalf("err = %v", err)
	}
	r, err := s.Expect(t.Context(), []expect.Case{{Name: "full", Pattern: expect.FullBuffer()}})
	if err != nil || r.Before != "0123456789" {
		t.Fatalf("full buffer: %+v, %v", r, err)
	}
}

func TestSession_StreamingFallback(t *testing.T) {
	t.Parallel()
	s, p := pipe(t, expect.WithBufferLimit(1024))
	body := strings.Repeat("interface x\n", 400)
	p.say(body + "router#")
	var got strings.Builder
	r, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "router#")}, expect.WithTimeout(5*time.Second),
		expect.WithStreamingFallback(func(_ context.Context, _ *expect.Session, r expect.Result) (expect.Action, error) {
			got.WriteString(r.Match)
			return expect.Continue, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if got.String()+r.Before != body {
		t.Fatalf("streamed %d + %d bytes, want %d", got.Len(), len(r.Before), len(body))
	}
}

func TestSession_Busy(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	p.say("x")
	inside, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = s.Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("x"), Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
			close(inside)
			<-release
			return expect.Return, nil
		}}})
	})
	<-inside
	if _, err := s.Poll(t.Context(), []expect.Case{lit("x", "x")}); !errors.Is(err, expect.ErrBusy) {
		t.Fatalf("second receiver: %v", err)
	}
	close(release)
	wg.Wait()
}

func TestSession_CaseLimit(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	p.say("--More----More--")
	_, err := s.Expect(t.Context(), []expect.Case{
		{Name: "more", Pattern: expect.Literal("--More--"), Limit: 1, Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
			return expect.Continue, nil
		}},
	}, expect.WithTimeout(time.Second))
	var le *expect.CaseLimitError
	if !errors.As(err, &le) || le.Name != "more" {
		t.Fatalf("err = %v", err)
	}
}

func TestSession_NoConsume(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	p.say("ab")
	seen := 0
	r, err := s.Expect(t.Context(), []expect.Case{
		{Name: "peek", Pattern: expect.Literal("a"), NoConsume: true, Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
			seen++
			return expect.Continue, nil
		}},
		lit("b", "b"),
	}, expect.WithTimeout(time.Second))
	if err != nil || r.Name != "b" || r.Before != "a" || seen != 1 {
		t.Fatalf("got %+v %v, peeked %d", r, err, seen)
	}
}

func TestSession_SetRules(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	if err := s.SetRules([]expect.Case{{Name: "pager", Pattern: expect.Literal("--More--"), Limit: expect.Unlimited, Handler: expect.ReplyThenContinue(" ")}}, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = p.conn.Write([]byte("a\n--More--"))
		p.hear(" ")
		_, _ = p.conn.Write([]byte("b\nrouter#"))
	}()
	r, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "router#")}, expect.WithTimeout(time.Second))
	if err != nil || r.Before != "b\n" {
		t.Fatalf("got %+v, %v", r, err)
	}
	before, _ := s.Rules()
	if len(before) != 1 || before[0].Name != "pager" {
		t.Fatalf("rules %v", before)
	}
}

func TestSession_CloseInterruptsExpect(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	errc := make(chan error, 1)
	go func() {
		_, err := s.Expect(context.Background(), []expect.Case{lit("prompt", "#")}, expect.WithTimeout(0))
		errc <- err
	}()
	time.Sleep(10 * time.Millisecond)
	first := s.Close()
	if err := <-errc; !errors.Is(err, expect.ErrClosed) {
		t.Fatalf("Expect after Close: %v", err)
	}
	if second := s.Close(); !errors.Is(second, first) {
		t.Fatalf("Close not idempotent: %v then %v", first, second)
	}
	if _, err := s.Send(t.Context(), "x"); !errors.Is(err, expect.ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
}

func TestSession_WriteErrorRetires(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, _ := pipe(t, expect.WithWriteTimeout(time.Second))
		_, err := s.Send(t.Context(), "nobody reads this")
		var we *expect.WriteError
		if !errors.As(err, &we) || we.Written != 0 {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.Send(t.Context(), "x"); !errors.Is(err, expect.ErrClosed) {
			t.Fatalf("session not retired: %v", err)
		}
	})
}

func TestSession_SendVariants(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		go p.hear("abcd")
		start := time.Now()
		if n, err := s.SendSlow(t.Context(), "abcd", 2, time.Second); err != nil || n != 4 {
			t.Fatalf("SendSlow %d %v", n, err)
		}
		if time.Since(start) != time.Second {
			t.Fatalf("SendSlow took %v", time.Since(start))
		}
		go p.hear("\x00\x00")
		if n, err := s.SendNull(t.Context(), 2); err != nil || n != 2 {
			t.Fatalf("SendNull %d %v", n, err)
		}
		if err := s.SendBreak(t.Context(), time.Millisecond); !errors.Is(err, expect.ErrBreakUnsupported) {
			t.Fatalf("SendBreak %v", err)
		}
	})
}

func TestNewSession_Validates(t *testing.T) {
	t.Parallel()
	if _, err := expect.NewSession(nil); err == nil {
		t.Fatal("nil transport accepted")
	}
	var nilConn *net.TCPConn
	if _, err := expect.NewSession(nilConn); err == nil {
		t.Fatal("typed nil transport accepted")
	}
	local, remote := net.Pipe()
	defer remote.Close()
	defer local.Close()
	if _, err := expect.NewSession(local, expect.WithBufferLimit(0)); err == nil {
		t.Fatal("zero buffer limit accepted")
	}
	s, _ := pipe(t)
	if _, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.Glob("[ab")}}); !errors.Is(err, expect.ErrInvalidPattern) {
		t.Fatalf("bad glob: %v", err)
	}
	if _, err := expect.Compile("a*"); !errors.Is(err, expect.ErrEmptyMatch) {
		t.Fatalf("empty regexp: %v", err)
	}
}

func TestSession_TranscriptRedacted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec, err := record.New(record.Config{Enabled: true, Dir: dir, Formats: []record.Format{record.FormatLog, record.FormatJSONL}})
	if err != nil {
		t.Fatal(err)
	}
	s, p := pipe(t, expect.WithRecorder(rec), expect.WithLabel("10.0.0.1"), expect.WithLineEnding("\r"))
	if err := s.Redact("s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := s.RedactPattern(regexp.MustCompile(`community (\S+)`)); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = p.conn.Write([]byte("Password:"))
		p.hear("s3cret\r")
		_, _ = p.conn.Write([]byte("\r\nsnmp-agent community public\r\nrouter#"))
	}()
	if _, err := s.Expect(t.Context(), []expect.Case{
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine("s3cret"))},
		lit("prompt", "router#"),
	}, expect.WithTimeout(time.Second)); err != nil {
		t.Fatal(err)
	}
	paths := s.Transcript()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, path := range paths {
		if filepath.Dir(filepath.Dir(path)) != dir {
			t.Fatalf("transcript %s outside %s", path, dir)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	text := all.String()
	for _, leak := range []string{"s3cret", "public"} {
		if strings.Contains(text, leak) {
			t.Fatalf("transcript leaks %q:\n%s", leak, text)
		}
	}
	for _, want := range []string{">> ******", "community ******", `"case":"password"`, `"op":"close"`} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
}

func TestNewSession_RecordsByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EXPECT_RECORD_DIR", "")
	t.Setenv("EXPECT_RECORD", "")
	local, remote := net.Pipe()
	defer remote.Close()
	s, err := expect.NewSession(local, expect.WithLabel("sw1"))
	if err != nil {
		t.Fatal(err)
	}
	paths := s.Transcript()
	_ = s.Close()
	if len(paths) != 2 || !strings.HasPrefix(paths[0], filepath.Join(home, ".local", "share", "gatework", "expect")) {
		t.Fatalf("default transcript %v", paths)
	}
	t.Setenv("EXPECT_RECORD", "off")
	s, err = expect.NewSession(local)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Transcript()) != 0 {
		t.Fatalf("EXPECT_RECORD=off still records %v", s.Transcript())
	}
}
