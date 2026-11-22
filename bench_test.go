package expect_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/gatework/expect"
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
