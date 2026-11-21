//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package spawn

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/gatework/expect"
)

// Console is a PTY with a Session on its master side, for a command the
// caller builds and owns (exec.Cmd): the caller cancels and Waits it.
// Close closes the Session and both PTY ends, not the command.
type Console struct {
	master *os.File
	tty    *os.File
	s      *expect.Session
}

// NewConsole opens a PTY and its Session; WithDir and WithEnv do not
// apply. On failure everything acquired is closed.
func NewConsole(opts ...Option) (*Console, error) {
	o, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	master, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("spawn: open pty: %w", err)
	}
	c := &Console{master: master, tty: tty}
	if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(o.rows), Cols: uint16(o.cols)}); err != nil {
		return nil, errors.Join(fmt.Errorf("spawn: size: %w", err), c.closeFiles())
	}
	if o.raw {
		if _, err := term.MakeRaw(int(tty.Fd())); err != nil {
			return nil, errors.Join(fmt.Errorf("spawn: raw mode: %w", err), c.closeFiles())
		}
	}
	if c.s, err = expect.NewSession(&terminal{File: master}, o.sessionOptions()...); err != nil {
		return nil, errors.Join(err, c.closeFiles())
	}
	return c, nil
}

// Start runs cmd as the leader of a new session with the PTY as its
// controlling terminal and standard streams. The parent's copy of the
// slave is then closed so that the Session sees EOF when the command
// exits; TTY returns nil afterwards.
func (c *Console) Start(cmd *exec.Cmd) error {
	if c.tty == nil {
		return &expect.ConfigError{Option: "console", Reason: "already started"}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.tty, c.tty, c.tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setctty, cmd.SysProcAttr.Ctty = true, true, 0
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn: start: %w", err)
	}
	err := c.tty.Close()
	c.tty = nil
	return err
}

// Session returns the conversation over the PTY.
func (c *Console) Session() *expect.Session { return c.s }

// TTY returns the slave end before Start; it is borrowed.
func (c *Console) TTY() *os.File { return c.tty }

// Terminal returns the PTY controls; it also implements EchoController.
func (c *Console) Terminal() Terminal { return &terminal{File: c.master} }

// Close closes the Session and both PTY ends.
func (c *Console) Close() error { return errors.Join(c.s.Close(), c.closeTTY()) }

// String identifies the console.
func (c *Console) String() string { return "spawn.Console" }

func (c *Console) closeTTY() error {
	if c.tty == nil {
		return nil
	}
	err := c.tty.Close()
	c.tty = nil
	return err
}

func (c *Console) closeFiles() error { return errors.Join(c.master.Close(), c.closeTTY()) }
