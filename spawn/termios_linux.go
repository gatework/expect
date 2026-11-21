//go:build linux

package spawn

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

// sendBreak asserts a break with TCSBRK; Linux chooses its duration
// (0.25 to 0.5 s), so d only delays the return.
func sendBreak(ctx context.Context, fd int, d time.Duration) error {
	if err := unix.IoctlSetInt(fd, unix.TCSBRK, 0); err != nil {
		return err
	}
	return sleep(ctx, d)
}
