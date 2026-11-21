// Package spawn runs a local program on a terminal and talks to it through
// an expect.Session: a Unix PTY (creack/pty) or a Windows 10 1809+
// pseudo console. Use it to drive ssh, telnet, a serial console client or
// any interactive tool that needs a terminal.
//
// A Process owns its child and terminal. Consume output before Wait, or
// Close to interrupt a child whose output nobody reads. Cancelling the
// Start context hangs up the child: Unix sends SIGHUP to its process group
// and SIGKILL to the child two seconds later; Windows terminates the job
// that holds the child and its descendants.
package spawn

import (
	"errors"
	"fmt"
	"slices"

	"github.com/gatework/expect"
)

const (
	defaultRows = 24
	defaultCols = 80
)

// ErrUnsupported reports a capability the platform does not offer.
var ErrUnsupported = fmt.Errorf("spawn: %w", errors.ErrUnsupported)

// Option configures Start.
type Option func(*options) error

type options struct {
	dir        string
	env        []string
	rows, cols int
	raw        bool
	session    []expect.SessionOption
}

// ExitStatus is how a child ended.
type ExitStatus struct {
	// Code is the exit code, or -1 when the child was killed by a signal.
	Code int
	// Signal names the signal that killed the child, when there was one.
	Signal string
}

// Success reports a zero exit code.
func (s ExitStatus) Success() bool { return s.Code == 0 }

// ExitError reports a child that did not exit successfully.
type ExitError struct{ Status ExitStatus }

// Error describes the status.
func (e *ExitError) Error() string {
	if e.Status.Signal != "" {
		return "spawn: child killed by " + e.Status.Signal
	}
	return fmt.Sprintf("spawn: child exited with code %d", e.Status.Code)
}

// Terminal controls the child's terminal.
type Terminal interface {
	// SetSize changes the window size and notifies the child.
	SetSize(rows, cols int) error
}

// EchoController is implemented by terminals whose echo can be switched.
type EchoController interface {
	SetEcho(enabled bool) error
}

// WithDir sets the child's working directory.
func WithDir(dir string) Option {
	return func(o *options) error {
		o.dir = dir
		return nil
	}
}

// WithEnv sets the child's environment ("KEY=value"); nil inherits the
// parent's.
func WithEnv(env []string) Option {
	return func(o *options) error {
		o.env = slices.Clone(env)
		return nil
	}
}

// WithSize sets the initial window size; the default is 24 rows by 80
// columns.
func WithSize(rows, cols int) Option {
	return func(o *options) error {
		if rows <= 0 || cols <= 0 || rows > 0xffff || cols > 0xffff {
			return &expect.ConfigError{Option: "size", Reason: "out of range"}
		}
		o.rows, o.cols = rows, cols
		return nil
	}
}

// WithRaw puts the terminal in raw mode: no echo, no line editing.
func WithRaw() Option {
	return func(o *options) error {
		o.raw = true
		return nil
	}
}

// WithSessionOptions configures the Session. WithWindow is set from the
// terminal size unless given here.
func WithSessionOptions(opts ...expect.SessionOption) Option {
	return func(o *options) error {
		o.session = append(o.session, opts...)
		return nil
	}
}

func applyOptions(opts []Option) (options, error) {
	o := options{rows: defaultRows, cols: defaultCols}
	for _, opt := range opts {
		if opt == nil {
			return o, &expect.ConfigError{Option: "spawn option", Reason: "nil"}
		}
		if err := opt(&o); err != nil {
			return o, err
		}
	}
	return o, nil
}

func (o options) sessionOptions() []expect.SessionOption {
	return append([]expect.SessionOption{expect.WithWindow(o.cols, o.rows)}, o.session...)
}
