//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package spawn_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/gatework/expect"
	"github.com/gatework/expect/spawn"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func start(ctx context.Context, t *testing.T, script string) *spawn.Process {
	t.Helper()
	p, err := spawn.Start(ctx, "/bin/sh", []string{"-c", script},
		spawn.WithEnv([]string{"PS1=$ ", "TERM=dumb"}), spawn.WithSessionOptions(expect.WithoutRecording()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestStart_Dialogue(t *testing.T) {
	t.Parallel()
	p := start(t.Context(), t, `printf 'Username: '; read u; printf 'hello %s\n' "$u"; exit 3`)
	s := p.Session()
	if _, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("Username: ")}}, expect.WithTimeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendLine(t.Context(), "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("hello ops")}}, expect.WithTimeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.EOF()}}, expect.WithTimeout(5*time.Second)); err != nil {
		t.Fatalf("hangup is not a clean EOF: %v", err)
	}
	st, err := p.Wait()
	var ee *spawn.ExitError
	if !errors.As(err, &ee) || st.Code != 3 {
		t.Fatalf("wait %+v %v", st, err)
	}
}

func TestStart_CancelHangsUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	p := start(ctx, t, `sleep 60`)
	cancel()
	st, _ := p.Wait()
	if st.Success() || st.Signal == "" && st.Code == 0 {
		t.Fatalf("child survived cancel: %+v", st)
	}
}

func TestStart_Terminal(t *testing.T) {
	t.Parallel()
	p := start(t.Context(), t, `sleep 1; stty size; stty -a | grep -o -- '-echo ' || true`)
	if err := p.Terminal().SetSize(40, 132); err != nil {
		t.Fatal(err)
	}
	if err := p.Terminal().(spawn.EchoController).SetEcho(false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Session().Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("40 132")}}, expect.WithTimeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestConsole(t *testing.T) {
	t.Parallel()
	c, err := spawn.NewConsole(spawn.WithSessionOptions(expect.WithoutRecording()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "echo ready")
	if err := c.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Session().Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("ready")}}, expect.WithTimeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// TestSession_Resize changes the window through the Session and lets the
// child report its new size.
func TestSession_Resize(t *testing.T) {
	t.Parallel()
	p := start(t.Context(), t, `read _; stty size; read _`)
	s := p.Session()
	if err := s.Resize(40, 132); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendLine(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Expect(t.Context(), []expect.Case{{Pattern: expect.Literal("40 132")}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.SendLine(t.Context(), "")
}
