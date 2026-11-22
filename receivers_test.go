package expect_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gatework/expect"
)

// syncBuffer is a concurrency-safe output sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestSession_Poll(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	if _, err := s.Poll(t.Context(), []expect.Case{lit("prompt", "#")}); !errors.Is(err, expect.ErrTimeout) {
		t.Fatalf("empty poll: %v", err)
	}
	p.say("router#")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r, err := s.Poll(t.Context(), []expect.Case{lit("prompt", "#")})
		if err == nil {
			if r.Before != "router" {
				t.Fatalf("poll %+v", r)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("poll never matched")
}

func TestBackground_PauseResume(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	var notices []string
	var mu sync.Mutex
	b, err := s.StartBackground(t.Context(), []expect.Case{
		{Name: "log", Pattern: expect.MustCompile(`%LINK-\S+`), Limit: expect.Unlimited, Handler: func(_ context.Context, _ *expect.Session, r expect.Result) (expect.Action, error) {
			mu.Lock()
			notices = append(notices, r.Match)
			mu.Unlock()
			return expect.Continue, nil
		}},
		lit("bye", "bye"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Expect(t.Context(), []expect.Case{lit("x", "x")}); !errors.Is(err, expect.ErrBusy) {
		t.Fatalf("foreground while background runs: %v", err)
	}
	p.say("%LINK-UP\n")
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(notices) == 1 })
	if err := b.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.say("router#")
	if _, err := s.Expect(t.Context(), []expect.Case{lit("prompt", "router#")}, expect.WithTimeout(time.Second)); err != nil {
		t.Fatalf("foreground while paused: %v", err)
	}
	if err := b.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.say("%LINK-DOWN bye")
	r, err := b.Wait(t.Context())
	if err != nil || r.Name != "bye" {
		t.Fatalf("wait %+v %v", r, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(notices, ",") != "%LINK-UP,%LINK-DOWN" {
		t.Fatalf("notices %v", notices)
	}
}

func TestBackground_Stop(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	b, err := s.StartBackground(t.Context(), []expect.Case{lit("never", "never")})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(t.Context()); err != nil {
		t.Fatalf("stop while paused: %v", err)
	}
	if _, err := s.Poll(t.Context(), []expect.Case{lit("x", "x")}); !errors.Is(err, expect.ErrTimeout) {
		t.Fatalf("input not released: %v", err)
	}
}

func TestBackground_Panic(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	b, err := s.StartBackground(t.Context(), []expect.Case{{Pattern: expect.Literal("boom"), Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
		panic("device said boom")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p.say("boom")
	_, err = b.Wait(t.Context())
	var ce *expect.CallbackError
	if !errors.As(err, &ce) || ce.Kind != expect.CallbackPanicked {
		t.Fatalf("err = %v", err)
	}
}

func TestGroup_Expect(t *testing.T) {
	t.Parallel()
	a, pa := pipe(t, expect.WithLabel("a"))
	b, pb := pipe(t, expect.WithLabel("b"))
	g, err := expect.NewGroup(a, b)
	if err != nil {
		t.Fatal(err)
	}
	pb.say("standby#")
	s, r, err := g.Expect(t.Context(), []expect.Case{
		{Name: "active", Pattern: expect.Literal("active#")},
		{Name: "standby", Pattern: expect.Literal("standby#"), Sessions: []*expect.Session{b}},
	}, expect.WithTimeout(time.Second))
	if err != nil || s != b || r.Name != "standby" {
		t.Fatalf("got %v %+v %v", s, r, err)
	}
	pa.say("standby#")
	if _, _, err := g.Expect(t.Context(), []expect.Case{{Name: "standby", Pattern: expect.Literal("standby#"), Sessions: []*expect.Session{b}}}, expect.WithTimeout(100*time.Millisecond)); !errors.Is(err, expect.ErrTimeout) {
		t.Fatalf("selector ignored: %v", err)
	}
}

func TestGroup_AddRemove(t *testing.T) {
	t.Parallel()
	a, _ := pipe(t)
	b, pb := pipe(t)
	g, err := expect.NewGroup(a)
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		s   *expect.Session
		err error
	}
	done := make(chan res, 1)
	go func() {
		s, _, err := g.Expect(t.Context(), []expect.Case{lit("prompt", "#")}, expect.WithTimeout(5*time.Second))
		done <- res{s, err}
	}()
	waitFor(t, func() bool { return g.Add(t.Context(), b) == nil })
	if err := g.Remove(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	pb.say("b#")
	got := <-done
	if got.err != nil || got.s != b {
		t.Fatalf("got %v %v", got.s, got.err)
	}
	if _, err := expect.NewGroup(a, a); err == nil {
		t.Fatal("duplicate member accepted")
	}
}

func TestSession_Interact(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	if err := s.Redact("s3cret"); err != nil {
		t.Fatal(err)
	}
	opIn, opWrite := io.Pipe()
	var out syncBuffer
	errc := make(chan error, 1)
	go func() {
		errc <- s.Interact(t.Context(), opIn, &out, expect.WithInteractInputCases(
			expect.Case{Name: "macro", Pattern: expect.Literal("~v"), Handler: func(ctx context.Context, s *expect.Session, _ expect.Result) (expect.Action, error) {
				_, err := s.SendLine(ctx, "show version")
				return expect.Continue, err
			}},
		))
	}()
	p.say("banner s3cret\nrouter#")
	waitFor(t, func() bool { return strings.Contains(out.String(), "router#") })
	go func() {
		_, _ = opWrite.Write([]byte("~"))
		_, _ = opWrite.Write([]byte("vterm len 0\n"))
	}()
	p.hear("show version\n")
	p.hear("term len 0\n")
	_, _ = opWrite.Write([]byte{0x1d})
	if err := <-errc; err != nil {
		t.Fatalf("Interact: %v", err)
	}
	if got := out.String(); strings.Contains(got, "s3cret") || !strings.Contains(got, "banner ******") {
		t.Fatalf("operator saw %q", got)
	}
	if _, err := opWrite.Write([]byte("x")); err == nil {
		t.Fatal("input not closed")
	}
}

func TestSession_InteractOutputCase(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	opIn, _ := io.Pipe()
	var out syncBuffer
	p.say("Connection closed by foreign host")
	err := s.Interact(t.Context(), opIn, &out, expect.WithInteractOutputCases(lit("closed", "closed by foreign host")))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRelay(t *testing.T) {
	t.Parallel()
	src, ps := pipe(t)
	dst, pd := pipe(t)
	var tee syncBuffer
	ps.say("line1\nSECRET-TOKEN\nline2\nEND")
	errc := make(chan error, 1)
	go func() {
		errc <- expect.Relay(t.Context(), []expect.Route{{
			Source: src, Targets: []*expect.Session{dst}, Output: &tee,
			Cases: []expect.Case{
				{Name: "token", Pattern: expect.Literal("SECRET-TOKEN\n"), Limit: expect.Unlimited, Handler: func(context.Context, *expect.Session, expect.Result) (expect.Action, error) {
					return expect.Continue, nil
				}},
				lit("end", "END"),
			},
		}})
	}()
	pd.hear("line1\nline2\n")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if tee.String() != "line1\nline2\n" {
		t.Fatalf("tee %q", tee.String())
	}
}

// TestRelay_HoldsSessionRulePrefix checks that a session rule split
// across chunks is held back, matched and answered rather than forwarded.
func TestRelay_HoldsSessionRulePrefix(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pager := expect.Case{Name: "more", Pattern: expect.Literal("--More--"), Limit: expect.Unlimited, Handler: expect.ReplyThenContinue(" ")}
		src, ps := pipe(t, expect.WithBefore(pager))
		var tee syncBuffer
		errc := make(chan error, 1)
		go func() {
			errc <- expect.Relay(t.Context(), []expect.Route{{Source: src, Output: &tee, Cases: []expect.Case{lit("end", "END")}}})
		}()
		_, _ = io.WriteString(ps.conn, "line1\n--Mo")
		synctest.Wait()
		_, _ = io.WriteString(ps.conn, "re--")
		ps.hear(" ")
		_, _ = io.WriteString(ps.conn, "line2\nEND")
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
		if tee.String() != "line1\nline2\n" {
			t.Fatalf("tee %q", tee.String())
		}
	})
}

func TestRelay_RejectsSharedSource(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	err := expect.Relay(t.Context(), []expect.Route{{Source: s}, {Source: s}})
	if _, ok := errors.AsType[*expect.ConfigError](err); !ok {
		t.Fatalf("err = %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
