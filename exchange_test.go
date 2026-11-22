package expect_test

import (
	"errors"
	"io"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gatework/expect"
)

// TestSession_ExchangeInterfaces resets interfaces one by one; each reset
// asks for confirmation, answered by a session rule while the Exchange
// keeps waiting for the prompt.
func TestSession_ExchangeInterfaces(t *testing.T) {
	t.Parallel()
	s, p := pipe(t, expect.WithLineEnding("\r"))
	confirm := expect.Case{Name: "confirm", Pattern: expect.MustCompile(`(?i)continue\? \[Y/N\]:`), Handler: expect.ReplyLineThenContinue("Y")}
	if err := s.SetRules([]expect.Case{confirm}, nil); err != nil {
		t.Fatal(err)
	}
	ifaces := []string{"GE1/0/1", "GE1/0/2"}
	go func() {
		for _, ifc := range ifaces {
			p.hear("clear configuration interface " + ifc + "\r")
			p.say("\r\nThis will clear the interface. Continue? [Y/N]:")
			p.hear("Y\r")
			p.say("Y\r\nInfo: Succeeded.\r\n[r1]")
		}
	}()
	prompt := []expect.Case{{Name: "prompt", Pattern: expect.Literal("[r1]")}}
	for _, ifc := range ifaces {
		r, err := s.Exchange(t.Context(), "clear configuration interface "+ifc, prompt)
		if err != nil {
			t.Fatalf("%s: %v", ifc, err)
		}
		want := "\r\nThis will clear the interface. Continue? [Y/N]:Y\r\nInfo: Succeeded.\r\n[r1]"
		if r.Name != "prompt" || r.Output != want || r.Before != "Y\r\nInfo: Succeeded.\r\n" {
			t.Fatalf("%s: name %q output %q before %q", ifc, r.Name, r.Output, r.Before)
		}
	}
}

// TestSession_ExchangeBusySendsNothing proves a busy session never sends
// the command it could not receive the answer to.
func TestSession_ExchangeBusySendsNothing(t *testing.T) {
	t.Parallel()
	s, p := pipe(t)
	bg, err := s.StartBackground(t.Context(), []expect.Case{lit("never", "never")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exchange(t.Context(), "reload", []expect.Case{lit("prompt", "#")}); !errors.Is(err, expect.ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	if err := bg.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := p.conn.Read(make([]byte, 16)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("peer read %d bytes, %v", n, err)
	}
}

// TestThen_RestartsTimer checks the Tcl exp_continue semantics: each
// answer gets a fresh timeout, unless ThenKeepTimer is used.
func TestThen_RestartsTimer(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		handler expect.Handler
		want    error
	}{
		{"Then", expect.Then(expect.ReplyLine("y")), nil},
		{"ThenKeepTimer", expect.ThenKeepTimer(expect.ReplyLine("y")), expect.ErrTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				s, p := pipe(t)
				done := make(chan struct{})
				go func() {
					defer close(done)
					for range 2 {
						if _, err := io.WriteString(p.conn, "sure?"); err != nil {
							return
						}
						if _, err := io.ReadFull(p.conn, make([]byte, 2)); err != nil {
							return
						}
						time.Sleep(800 * time.Millisecond)
					}
					_, _ = io.WriteString(p.conn, "#")
				}()
				defer func() { _ = p.conn.Close(); <-done }()
				_, err := s.Expect(t.Context(), []expect.Case{
					{Name: "question", Pattern: expect.Literal("sure?"), Handler: tt.handler},
					lit("prompt", "#"),
				}, expect.WithTimeout(time.Second))
				if !errors.Is(err, tt.want) {
					t.Fatalf("got %v, want %v", err, tt.want)
				}
			})
		})
	}
}
