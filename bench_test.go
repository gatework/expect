package expect_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gatework/expect"
	"github.com/gatework/expect/record"
)

// chunkConn is an in-memory transport that returns body in 4 KiB reads,
// like a socket with data waiting, so benchmarks measure the engine and
// not goroutine hand-offs. Writes are discarded.
type chunkConn struct {
	body   []byte
	at     int
	eof    bool // end the stream after body instead of waiting for Close
	closed chan struct{}
	once   sync.Once
}

func newChunkConn(body []byte) *chunkConn { return &chunkConn{body: body, closed: make(chan struct{})} }

func (c *chunkConn) Read(p []byte) (int, error) {
	if c.at == len(c.body) {
		if !c.eof {
			<-c.closed
		}
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 4096)], c.body[c.at:])
	c.at += n
	return n, nil
}
func (c *chunkConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *chunkConn) Close() error                { c.once.Do(func() { close(c.closed) }); return nil }

// benchSession runs one Expect per iteration against an in-memory peer
// holding body.
func benchSession(b *testing.B, body []byte, cases []expect.Case, opts ...expect.SessionOption) {
	b.Helper()
	benchConn(b, func() *chunkConn { return newChunkConn(body) }, len(body), cases, opts...)
}

func benchConn(b *testing.B, conn func() *chunkConn, size int, cases []expect.Case, opts ...expect.SessionOption) {
	b.Helper()
	b.SetBytes(int64(size))
	for b.Loop() {
		s, err := expect.NewSession(conn(), append([]expect.SessionOption{expect.WithoutRecording(), expect.WithBufferLimit(64 << 20)}, opts...)...)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := s.Expect(b.Context(), cases); err != nil {
			b.Fatal(err)
		}
		_ = s.Close()
	}
}

var ansiBody = []byte(strings.Repeat("\x1b[1minterface\x1b[0m GigabitEthernet0/1\r\n description uplink\x1b[K\r\n", 20000) + "\x1b[32mrouter#\x1b[0m")

// BenchmarkSession_TerminalFilter measures WithTerminalFilter on output
// full of control sequences.
func BenchmarkSession_TerminalFilter(b *testing.B) {
	benchSession(b, ansiBody, []expect.Case{{Pattern: expect.Literal("router#")}}, expect.WithTerminalFilter())
}

// BenchmarkSession_SettlePrompt measures a Settle prompt's final-line
// search on large output; the input ends, so nothing waits.
func BenchmarkSession_SettlePrompt(b *testing.B) {
	conn := func() *chunkConn { c := newChunkConn(configBody); c.eof = true; return c }
	benchConn(b, conn, len(configBody), []expect.Case{{Pattern: expect.MustCompile(`[\w.-]+#\s*$`), Settle: time.Hour}})
}

var configBody = []byte(strings.Repeat("interface GigabitEthernet0/1\r\n description uplink to core\r\n", 20000) + "router#")

func BenchmarkSession_GlobPrompt(b *testing.B) {
	benchSession(b, configBody, []expect.Case{{Pattern: expect.Glob("*router#")}})
}

func BenchmarkSession_ManyRules(b *testing.B) {
	var rules []expect.Case
	for i := range 30 {
		rules = append(rules, expect.Case{Pattern: expect.MustCompile(fmt.Sprintf(`(?i)question %d \[Y/N\]:`, i))})
	}
	benchSession(b, configBody, []expect.Case{{Pattern: expect.MustCompile(`(?m)^[\w.-]+#[ \t]*$`)}}, expect.WithBefore(rules...))
}

// BenchmarkSession_PagerContinue answers 1000 pager prompts in one wait.
func BenchmarkSession_PagerContinue(b *testing.B) {
	const pages = 1000
	page := "interface GigabitEthernet0/1\r\n description uplink\r\n --More-- "
	b.SetBytes(int64(pages * len(page)))
	for b.Loop() {
		local, remote := net.Pipe()
		s, err := expect.NewSession(local, expect.WithoutRecording())
		if err != nil {
			b.Fatal(err)
		}
		go func() {
			one := make([]byte, 1)
			for range pages {
				_, _ = io.WriteString(remote, page)
				_, _ = remote.Read(one)
			}
			_, _ = io.WriteString(remote, "\r\nrouter#")
		}()
		_, err = s.Expect(b.Context(), []expect.Case{
			{Pattern: expect.Literal(" --More-- "), Handler: func(ctx context.Context, s *expect.Session, _ expect.Result) (expect.Action, error) {
				_, err := s.Send(ctx, " ")
				return expect.Continue, err
			}},
			{Pattern: expect.Literal("router#")},
		})
		if err != nil {
			b.Fatal(err)
		}
		_ = s.Close()
		_ = remote.Close()
	}
}

// BenchmarkGroup_ChattyMember has one member stream output while three
// quiet members hold unmatched text, all under 30 rules.
func BenchmarkGroup_ChattyMember(b *testing.B) {
	var cases []expect.Case
	for i := range 29 {
		cases = append(cases, expect.Case{Pattern: expect.MustCompile(fmt.Sprintf(`(?s)(?:yes|no) %d.*\?`, i))})
	}
	cases = append(cases, expect.Case{Pattern: expect.Literal("router#")})
	quiet := strings.Repeat("syslog line without a question\n", 2000)
	b.SetBytes(int64(len(configBody)))
	for b.Loop() {
		var sessions []*expect.Session
		var remotes []net.Conn
		for range 4 {
			local, remote := net.Pipe()
			s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithBufferLimit(64<<20))
			if err != nil {
				b.Fatal(err)
			}
			sessions, remotes = append(sessions, s), append(remotes, remote)
		}
		for _, r := range remotes[1:] {
			go func() { _, _ = io.WriteString(r, quiet) }()
		}
		go func() {
			for at := 0; at < len(configBody); at += 4096 {
				_, _ = remotes[0].Write(configBody[at:min(at+4096, len(configBody))])
			}
		}()
		g, err := expect.NewGroup(sessions...)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := g.Expect(b.Context(), cases); err != nil {
			b.Fatal(err)
		}
		for i, s := range sessions {
			_ = s.Close()
			_ = remotes[i].Close()
		}
	}
}

// BenchmarkSession_LargeOutputRecorded is LargeOutput with the default
// recording formats plus jsonl written to a temporary directory.
func BenchmarkSession_LargeOutputRecorded(b *testing.B) {
	c := record.DefaultConfig()
	c.Dir = b.TempDir()
	c.Formats = []record.Format{record.FormatLog, record.FormatCast, record.FormatJSONL}
	r, err := record.New(c)
	if err != nil {
		b.Fatal(err)
	}
	prompt := expect.MustCompile(`(?m)^[\w.-]+#[ \t]*$`)
	benchSession(b, configBody, []expect.Case{{Pattern: prompt}}, expect.WithRecorder(r))
}
