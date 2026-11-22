package expect_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/gatework/expect"
	"github.com/gatework/expect/record"
)

// benchSession runs one Expect per iteration against a peer that writes
// body in 4 KiB chunks.
func benchSession(b *testing.B, body []byte, cases []expect.Case, opts ...expect.SessionOption) {
	b.Helper()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		local, remote := net.Pipe()
		s, err := expect.NewSession(local, append([]expect.SessionOption{expect.WithoutRecording(), expect.WithBufferLimit(64 << 20)}, opts...)...)
		if err != nil {
			b.Fatal(err)
		}
		go func() {
			for at := 0; at < len(body); at += 4096 {
				_, _ = remote.Write(body[at:min(at+4096, len(body))])
			}
		}()
		if _, err := s.Expect(b.Context(), cases); err != nil {
			b.Fatal(err)
		}
		_ = s.Close()
		_ = remote.Close()
	}
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
