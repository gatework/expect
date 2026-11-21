//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package spawn

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

// sendBreak holds the line in break state for d.
func sendBreak(ctx context.Context, fd int, d time.Duration) error {
	if err := unix.IoctlSetInt(fd, unix.TIOCSBRK, 0); err != nil {
		return err
	}
	err := sleep(ctx, d)
	return errors.Join(err, unix.IoctlSetInt(fd, unix.TIOCCBRK, 0))
}
