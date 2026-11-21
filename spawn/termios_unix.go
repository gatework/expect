//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package spawn

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// setEcho switches ECHO, leaving the other terminal flags alone.
func setEcho(fd int, enabled bool) error {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	if enabled {
		t.Lflag |= unix.ECHO
	} else {
		t.Lflag &^= unix.ECHO
	}
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
