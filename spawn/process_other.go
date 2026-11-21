//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package spawn

import (
	"context"

	"github.com/gatework/expect"
)

// Process is unavailable on this platform.
type Process struct{}

// Start returns ErrUnsupported on this platform.
func Start(context.Context, string, []string, ...Option) (*Process, error) {
	return nil, ErrUnsupported
}

// Session returns nil.
func (p *Process) Session() *expect.Session { return nil }

// PID returns -1.
func (p *Process) PID() int { return -1 }

// Terminal returns nil.
func (p *Process) Terminal() Terminal { return nil }

// Wait returns ErrUnsupported.
func (p *Process) Wait() (ExitStatus, error) { return ExitStatus{Code: -1}, ErrUnsupported }

// Close does nothing.
func (p *Process) Close() error { return nil }
