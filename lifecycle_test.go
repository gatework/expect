package expect_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gatework/expect"
)

// logSink keeps log records for assertions.
type logSink struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *logSink) Enabled(context.Context, slog.Level) bool { return true }
func (h *logSink) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}
func (h *logSink) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logSink) WithGroup(string) slog.Handler      { return h }

// find returns the first record with msg and its attributes.
func (h *logSink) find(msg string) (slog.Level, map[string]slog.Value, bool) {
	return h.findWith(msg, "")
}

// findWith returns the first record with msg that has attribute key
// (any record with msg when key is empty).
func (h *logSink) findWith(msg, key string) (slog.Level, map[string]slog.Value, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.Message != msg {
			continue
		}
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
		if _, ok := attrs[key]; key == "" || ok {
			return r.Level, attrs, true
		}
	}
	return 0, nil, false
}

func TestSession_DoneOnPeerClose(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	if s.Err() != nil {
		t.Fatalf("live session has Err %v", s.Err())
	}
	_, _ = io.WriteString(p.conn, "bye")
	_ = p.conn.Close()
	<-s.Done() // no wait was started
	if !errors.Is(s.Err(), io.EOF) {
		t.Fatalf("Err = %v, want io.EOF", s.Err())
	}
	r, err := s.Expect(t.Context(), []expect.Case{lit("bye", "bye")})
	if err != nil || r.Match != "bye" {
		t.Fatalf("pending input lost after Done: %q, %v", r.Match, err)
	}
}

func TestSession_ErrAfterCloseAndRetire(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	_ = s.Close()
	if !errors.Is(s.Err(), expect.ErrClosed) {
		t.Fatalf("Err after Close = %v", s.Err())
	}
	synctest.Test(t, func(t *testing.T) {
		w, _ := pipe(t, expect.WithWriteTimeout(time.Second))
		if _, err := w.Send(t.Context(), "nobody reads this"); err == nil {
			t.Fatal("send succeeded")
		}
		if _, ok := errors.AsType[*expect.WriteError](w.Err()); !ok {
			t.Fatalf("Err after a failed write = %v, want *WriteError", w.Err())
		}
	})
}

func TestSession_Stats(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	p.say("r1#")
	if _, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")}); err != nil {
		t.Fatal(err)
	}
	go p.hear("x\n")
	if _, err := s.SendLine(t.Context(), "x"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Poll(t.Context(), []expect.Case{lit("none", "none")})
	st := s.Stats()
	if st.Received != 3 || st.Sent != 2 || st.Waits != 2 || st.Failures != 1 || st.LastReceived.IsZero() || st.LastSent.IsZero() {
		t.Fatalf("stats %+v", st)
	}
	if idle := st.Idle(st.LastSent); idle != 0 {
		t.Fatalf("idle %v at the last send", idle)
	}
}

// TestKeepalive checks that an idle session is kept alive, the answer is
// discarded and input the caller has not read yet is kept.
func TestKeepalive(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t, expect.WithKeepalive(time.Minute))
		_, _ = io.WriteString(p.conn, "unread answer\r\nr1#")
		go func() {
			p.hear("\n")
			_, _ = io.WriteString(p.conn, "\r\nr1#")
		}()
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		r, err := s.Poll(t.Context(), nil)
		if !errors.Is(err, expect.ErrTimeout) || r.Before != "unread answer\r\nr1#" {
			t.Fatalf("pending after keepalive %q, %v", r.Before, err)
		}
		if st := s.Stats(); st.Sent != 1 {
			t.Fatalf("keepalive sent %d bytes", st.Sent)
		}
	})
}

// stuckConn is a transport whose Close does not interrupt Read for a while.
type stuckConn struct {
	release chan struct{}
}

func (c *stuckConn) Read([]byte) (int, error)    { <-c.release; return 0, io.EOF }
func (c *stuckConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *stuckConn) Close() error {
	go func() { time.Sleep(6 * time.Second); close(c.release) }()
	return nil
}

func TestSession_CloseWarnsStuckTransport(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var logs logSink
		s, err := expect.NewSession(&stuckConn{release: make(chan struct{})}, expect.WithoutRecording(), expect.WithLogger(slog.New(&logs)))
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		level, attrs, ok := logs.find("expect: transport read did not return after close")
		if !ok || level != slog.LevelWarn || attrs["waited"].Duration() != 5*time.Second {
			t.Fatalf("no stuck-transport warning: %v %v %v", ok, level, attrs)
		}
	})
}

func TestSession_LifecycleLogs(t *testing.T) {
	t.Parallel()
	var logs logSink
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithLogger(slog.New(&logs)), expect.WithTerminalFilter())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.WriteString(remote, "r1#") }()
	if _, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "#")}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if level, attrs, ok := logs.find("expect: session opened"); !ok || level != slog.LevelInfo || !attrs["terminal_filter"].Bool() {
		t.Fatalf("opened: %v %v %v", ok, level, attrs)
	}
	level, attrs, ok := logs.find("expect: session closed")
	if !ok || level != slog.LevelInfo || attrs["cause"].String() != "closed" || attrs["bytes_in"].Int64() != 3 || attrs["waits"].Int64() != 1 {
		t.Fatalf("closed: %v %v %v", ok, level, attrs)
	}
}

func TestBackground_PanicLogsStack(t *testing.T) {
	t.Parallel()
	var logs logSink
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithLogger(slog.New(&logs)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, err := s.StartBackground(t.Context(), []expect.Case{{Pattern: expect.Literal("x"), Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
		panic("device said s3cret")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.WriteString(remote, "x") }()
	if _, err := b.Wait(t.Context()); !errors.As(err, new(*expect.CallbackError)) {
		t.Fatalf("got %v", err)
	}
	level, attrs, ok := logs.find("expect: background handler panicked")
	if !ok || level != slog.LevelError || !strings.Contains(attrs["stack"].String(), "TestBackground_PanicLogsStack") || strings.Contains(attrs["stack"].String(), "s3cret") {
		t.Fatalf("panic log: %v %v", ok, level)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if !slices.ContainsFunc(logs.recs, func(r slog.Record) bool {
		found := false
		r.Attrs(func(a slog.Attr) bool {
			found = found || a.Key == "op" && a.Value.String() == "background"
			return true
		})
		return found
	}) {
		t.Fatal("no background event")
	}
}

// TestKeepalive_ReceiverWaits starts an Expect while a keepalive holds the
// input: it waits instead of failing with ErrBusy.
func TestKeepalive_ReceiverWaits(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t, expect.WithKeepalive(time.Minute))
		heard := make(chan struct{})
		go func() {
			p.hear("\n")
			close(heard)
			time.Sleep(100 * time.Millisecond) // the answer comes slowly
			_, _ = io.WriteString(p.conn, "\r\nr1#")
			time.Sleep(time.Second)
			_, _ = io.WriteString(p.conn, "late r1#")
		}()
		time.Sleep(time.Minute)
		<-heard
		r, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "late r1#")})
		if err != nil || r.Match != "late r1#" {
			t.Fatalf("got %q, %v", r.Match, err)
		}
	})
}

// TestWaitFailed_Diagnostics checks the Info line of a timed-out wait:
// metadata by default, a redacted tail with WithMatchTrace.
func TestWaitFailed_Diagnostics(t *testing.T) {
	t.Parallel()
	for _, trace := range []int{0, 12} {
		synctest.Test(t, func(t *testing.T) {
			var logs logSink
			local, remote := net.Pipe()
			s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithLogger(slog.New(&logs)), expect.WithMatchTrace(trace))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(); _ = remote.Close() }()
			if err := s.Redact("s3cret-key"); err != nil {
				t.Fatal(err)
			}
			// The 12-byte tail starts inside the secret.
			_, _ = io.WriteString(remote, "user admin key s3cret-key\r\n% Invalid input")
			synctest.Wait()
			_, err = s.Expect(t.Context(), []expect.Case{lit("prompt", "r1#"), {Pattern: expect.Glob("*[Y/N]")}}, expect.WithTimeout(time.Second))
			if !errors.Is(err, expect.ErrTimeout) {
				t.Fatalf("got %v", err)
			}
			level, attrs, ok := logs.find("expect: wait failed")
			if !ok || level != slog.LevelInfo {
				t.Fatalf("no failed-wait line: %v %v", ok, level)
			}
			if got := attrs["cases"].Any().([]string); !slices.Equal(got, []string{"prompt", "case#1:glob"}) {
				t.Errorf("cases %v", got)
			}
			if attrs["pending"].Int64() != 42 || attrs["timeout"].Duration() != time.Second || attrs["idle_for"].Duration() != time.Second {
				t.Errorf("attrs %v", attrs)
			}
			tail, has := attrs["tail"]
			switch {
			case trace == 0 && has:
				t.Errorf("tail logged without WithMatchTrace: %v", tail)
			case trace > 0 && tail.String() != `"Invalid input"` && tail.String() != `"nvalid input"`:
				t.Errorf("tail %v", tail)
			}
			logs.mu.Lock()
			defer logs.mu.Unlock()
			for _, r := range logs.recs {
				r.Attrs(func(a slog.Attr) bool {
					if v := a.Value.String(); strings.Contains(v, "cret") || strings.Contains(v, "s3c") {
						t.Errorf("%s %s leaks the secret: %s", r.Message, a.Key, v)
					}
					return true
				})
			}
		})
	}
}

// TestMatchTrace_Scan checks the per-scan Debug line and that the tail is
// masked when a secret straddles its start.
func TestMatchTrace_Scan(t *testing.T) {
	t.Parallel()
	var logs logSink
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	s, err := expect.NewSession(local, expect.WithoutRecording(), expect.WithLogger(slog.New(&logs)), expect.WithMatchTrace(20))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Redact("topsecretvalue"); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.WriteString(remote, strings.Repeat("x", 300)+"topsecretvalue r1#") }()
	if _, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "r1#")}); err != nil {
		t.Fatal(err)
	}
	level, attrs, ok := logs.findWith("expect: scan", "hit")
	if !ok || level != slog.LevelDebug || attrs["hit"].String() != "prompt" {
		t.Fatalf("scan line: %v %v %v", ok, level, attrs)
	}
	if tail := attrs["tail"].String(); strings.Contains(tail, "value") || strings.Contains(tail, "secret") || !strings.Contains(tail, "r1#") {
		t.Fatalf("tail %s", tail)
	}
}
