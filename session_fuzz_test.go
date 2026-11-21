package expect_test

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gatework/expect"
)

// FuzzSessionChunks checks that however the peer splits its output, a
// literal case consumes exactly the bytes before its first occurrence.
func FuzzSessionChunks(f *testing.F) {
	f.Add([]byte("show run\r\nhostname r1\r\nr1#"), uint8(3))
	f.Add([]byte("r1r1#"), uint8(1))
	f.Add([]byte("\x00r1#\x00"), uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		i := bytes.Index(data, []byte("r1#"))
		if i < 0 {
			data, i = append(data, "r1#"...), len(data)
		}
		local, remote := net.Pipe()
		s, err := expect.NewSession(local, expect.WithoutRecording())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		go func() {
			defer remote.Close()
			n := int(step)%9 + 1
			for at := 0; at < len(data); at += n {
				if _, err := remote.Write(data[at:min(at+n, len(data))]); err != nil {
					return
				}
			}
			_, _ = io.Copy(io.Discard, remote)
		}()
		r, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("r1#")}}, expect.WithTimeout(5*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if r.Before != string(data[:i]) || r.Match != "r1#" {
			t.Fatalf("before %q match %q, want %q", r.Before, r.Match, data[:i])
		}
	})
}

func BenchmarkSession_LargeOutput(b *testing.B) {
	body := []byte(strings.Repeat("interface GigabitEthernet0/1\r\n description uplink to core\r\n", 20000) + "router#")
	prompt := expect.MustCompile(`(?m)^[\w.-]+#[ \t]*$`)
	for b.Loop() {
		local, remote := net.Pipe()
		s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithBufferLimit(64<<20))
		if err != nil {
			b.Fatal(err)
		}
		go func() {
			for at := 0; at < len(body); at += 4096 {
				_, _ = remote.Write(body[at:min(at+4096, len(body))])
			}
		}()
		if _, err := s.Expect(b.Context(), []expect.Case{{Pattern: prompt}}); err != nil {
			b.Fatal(err)
		}
		_ = s.Close()
		_ = remote.Close()
	}
	b.SetBytes(int64(len(body)))
}
