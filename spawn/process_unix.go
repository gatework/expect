//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package spawn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/gatework/expect"
)

// hangupGrace is how long a cancelled child may take to exit after SIGHUP.
const hangupGrace = 2 * time.Second

// Process is a child running on a terminal. It must not be copied.
type Process struct {
	cmd     *exec.Cmd
	pty     *os.File
	s       *expect.Session
	stop    func() bool
	killMu  sync.Mutex // guards kill
	kill    *time.Timer
	once    sync.Once
	status  ExitStatus
	waitErr error
}

// Start runs program with args on a new terminal and returns its Process.
// Canceling ctx hangs up the child; it does not close the Session.
func Start(ctx context.Context, program string, args []string, opts ...Option) (*Process, error) {
	o, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(program, args...) //nolint:noctx // cancellation hangs up the process group below
	cmd.Dir, cmd.Env = o.dir, o.env
	f, err := pty.StartWithAttrs(cmd, &pty.Winsize{Rows: uint16(o.rows), Cols: uint16(o.cols)}, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return nil, fmt.Errorf("spawn: start %s: %w", program, err)
	}
	p := &Process{cmd: cmd, pty: f}
	if o.raw {
		if _, err := term.MakeRaw(int(f.Fd())); err != nil {
			return nil, errors.Join(fmt.Errorf("spawn: raw mode: %w", err), p.abort())
		}
	}
	p.s, err = expect.NewSession(&terminal{File: f}, o.sessionOptions()...)
	if err != nil {
		return nil, errors.Join(err, p.abort())
	}
	p.stop = context.AfterFunc(ctx, p.hangup)
	return p, nil
}

// Session returns the conversation with the child.
func (p *Process) Session() *expect.Session { return p.s }

// PID returns the child's process ID.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// Terminal returns the child's terminal; it also implements
// EchoController.
func (p *Process) Terminal() Terminal { return &terminal{File: p.pty} }

// Wait waits for the child to exit. A non-zero exit is an *ExitError.
// Repeated calls return the same result.
func (p *Process) Wait() (ExitStatus, error) {
	p.once.Do(func() {
		err := p.cmd.Wait()
		p.stop()
		p.killMu.Lock()
		if p.kill != nil {
			p.kill.Stop()
		}
		p.killMu.Unlock()
		p.status, p.waitErr = exitStatus(p.cmd.ProcessState), err
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			p.waitErr = &ExitError{Status: p.status}
		}
	})
	return p.status, p.waitErr
}

// Close closes the Session and the terminal, which hangs up the child,
// and waits for it to exit. It returns the close error, not the exit
// status; use Wait for that.
func (p *Process) Close() error {
	err := p.s.Close()
	p.hangup()
	_, _ = p.Wait()
	return err
}

// String identifies the process.
func (p *Process) String() string { return fmt.Sprintf("spawn.Process(%d)", p.PID()) }

// hangup sends SIGHUP to the child's process group and SIGKILL to the
// child after a grace period.
func (p *Process) hangup() {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.kill != nil {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
	p.kill = time.AfterFunc(hangupGrace, func() { _ = p.cmd.Process.Kill() })
}

func (p *Process) abort() error {
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	return p.pty.Close()
}

func exitStatus(ps *os.ProcessState) ExitStatus {
	if ps == nil {
		return ExitStatus{Code: -1}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ExitStatus{Code: -1, Signal: ws.Signal().String()}
	}
	return ExitStatus{Code: ps.ExitCode()}
}

// terminal is the PTY master: the Session's transport and the Terminal.
type terminal struct{ *os.File }

var (
	_ Terminal       = (*terminal)(nil)
	_ EchoController = (*terminal)(nil)
	_ expect.Breaker = (*terminal)(nil)
)

// Read reports the hangup of the slave side, which Linux signals as EIO,
// as io.EOF.
func (t *terminal) Read(b []byte) (int, error) {
	n, err := t.File.Read(b)
	if errors.Is(err, syscall.EIO) {
		err = io.EOF
	}
	return n, err
}

func (t *terminal) SetSize(rows, cols int) error {
	if rows <= 0 || cols <= 0 || rows > 0xffff || cols > 0xffff {
		return &expect.ConfigError{Option: "size", Reason: "out of range"}
	}
	return pty.Setsize(t.File, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (t *terminal) SetEcho(enabled bool) error { return setEcho(int(t.Fd()), enabled) }

// Break asserts a line break for d (at least the system default).
func (t *terminal) Break(ctx context.Context, d time.Duration) error {
	return sendBreak(ctx, int(t.Fd()), d)
}
