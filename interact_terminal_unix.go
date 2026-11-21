//go:build unix

package expect

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// InteractTerminal is Interact on an operator terminal, typically
// os.Stdin: it switches the terminal to raw mode and restores it on
// return. input is borrowed: Interact reads a duplicate descriptor, which
// it closes, so input stays open.
func (s *Session) InteractTerminal(ctx context.Context, input *os.File, output io.Writer, opts ...InteractOption) (err error) {
	o, err := applyInteractOptions(opts)
	if err != nil {
		return err
	}
	if input == nil {
		return configError("interact", "nil terminal")
	}
	fd := int(input.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return &ConfigError{Option: "interact terminal", Reason: "not a terminal", Cause: err}
	}
	defer func() { _ = term.Restore(fd, state) }()
	dup, err := duplicate(fd)
	if err != nil {
		return err
	}
	if o.cooked {
		o.input = cookedCases(o.input, fd, state)
		o.output = cookedCases(o.output, fd, state)
	}
	return s.interact(ctx, dup, output, o)
}

// duplicate returns a pollable duplicate of fd so that closing it
// interrupts a blocked read without closing fd. The shared non-blocking
// flag is restored when the duplicate is closed.
func duplicate(fd int) (*restoringFile, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, &ConfigError{Option: "interact terminal", Reason: "fcntl", Cause: err}
	}
	d, err := unix.Dup(fd)
	if err != nil {
		return nil, &ConfigError{Option: "interact terminal", Reason: "dup", Cause: err}
	}
	if err := unix.SetNonblock(d, true); err != nil {
		_ = unix.Close(d)
		return nil, &ConfigError{Option: "interact terminal", Reason: "nonblock", Cause: err}
	}
	return &restoringFile{File: os.NewFile(uintptr(d), "interact"), fd: fd, flags: flags}, nil
}

// restoringFile closes a duplicate and restores the original flags.
type restoringFile struct {
	*os.File

	fd    int
	flags int
}

func (f *restoringFile) Close() error {
	err := f.File.Close()
	_, _ = unix.FcntlInt(uintptr(f.fd), unix.F_SETFL, f.flags)
	return err
}

// cookedCases wraps handlers so that they run with the terminal restored.
func cookedCases(cases []Case, fd int, state *term.State) []Case {
	out := make([]Case, len(cases))
	for i, c := range cases {
		out[i] = c
		if h := c.Handler; h != nil {
			out[i].Handler = func(ctx context.Context, s *Session, r Result) (Action, error) {
				_ = term.Restore(fd, state)
				defer func() { _, _ = term.MakeRaw(fd) }()
				return h(ctx, s, r)
			}
		}
	}
	return out
}
