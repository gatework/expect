package expect_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gatework/expect"
)

// breakConn is a transport that records break requests.
type breakConn struct {
	net.Conn
	breaks chan time.Duration
}

func (c *breakConn) Break(_ context.Context, d time.Duration) error {
	c.breaks <- d
	return nil
}

func TestSession_SendBreak(t *testing.T) {
	t.Parallel()
	s, _ := pipe(t)
	if err := s.SendBreak(t.Context(), time.Second); !errors.Is(err, expect.ErrBreakUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("plain transport: %v", err)
	}
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	bc := &breakConn{Conn: local, breaks: make(chan time.Duration, 1)}
	bs, err := expect.NewSession(bc, expect.WithoutRecording())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bs.Close() })
	if err := bs.SendBreak(t.Context(), 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := <-bc.breaks; d != 250*time.Millisecond {
		t.Fatalf("break %v", d)
	}
	_ = bs.Close()
	if err := bs.SendBreak(t.Context(), time.Second); !errors.Is(err, expect.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestSession_SendNull(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	for _, n := range []int{0, 1025} {
		if _, err := s.SendNull(t.Context(), n); err == nil {
			t.Fatalf("count %d accepted", n)
		}
	}
	go func() { _, _ = s.SendNull(t.Context(), 3) }()
	p.hear("\x00\x00\x00")
}

func TestSession_SendSlow(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		if _, err := s.SendSlow(t.Context(), "x", 0, time.Second); err == nil {
			t.Fatal("zero chunk accepted")
		}
		if _, err := s.SendPaced(t.Context(), "x", nil); err == nil {
			t.Fatal("nil pacer accepted")
		}
		done := make(chan time.Duration, 1)
		go func() {
			start := time.Now()
			_, _ = s.SendSlow(t.Context(), "abcde", 2, 100*time.Millisecond)
			done <- time.Since(start)
		}()
		p.hear("abcde")
		if d := <-done; d != 200*time.Millisecond {
			t.Fatalf("took %v, want two 100ms pauses", d)
		}
	})
}

func TestSession_SendPacedCancel(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, p := pipe(t)
		ctx, cancel := context.WithCancel(t.Context())
		errc := make(chan error, 1)
		go func() {
			_, err := s.SendPaced(ctx, "ab", func(off int) time.Duration { return time.Duration(off) * time.Hour })
			errc <- err
		}()
		p.hear("a")
		synctest.Wait()
		cancel()
		var we *expect.WriteError
		if err := <-errc; !errors.As(err, &we) || we.Written != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		if _, err := s.Send(t.Context(), "x"); !errors.Is(err, expect.ErrClosed) {
			t.Fatalf("session still open after a partial write: %v", err)
		}
	})
}
