package expect

import "slices"

// lease is the exclusive right to consume a session's input. Every
// receiver holds one while it runs; the receive state of the session
// (rx, rxErr, version) belongs to the holder.
type lease struct {
	s    *Session
	wake chan struct{}
	held bool
	// raw makes pull skip WithTerminalFilter: Interact and Relay forward
	// the peer's bytes as they are.
	raw bool
}

// acquire takes the receive slot. Input arrival signals wake; nil makes a
// private channel. A Group passes one channel shared by its members.
func (s *Session) acquire(wake chan struct{}) (*lease, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if !s.busy.CompareAndSwap(false, true) && !s.afterKeepalive() {
		if s.closed.Load() {
			return nil, ErrClosed
		}
		return nil, ErrBusy
	}
	if wake == nil {
		wake = make(chan struct{}, 1)
	}
	s.in.attach(wake)
	return &lease{s: s, wake: wake, held: true}, nil
}

// release gives the receive slot back; pending input stays for the next
// receiver. Releasing twice is harmless.
func (l *lease) release() {
	if !l.held {
		return
	}
	l.held = false
	l.s.in.attach(nil)
	l.s.busy.Store(false)
}

// reacquire takes the receive slot again after release.
func (l *lease) reacquire() error {
	if l.held {
		return nil
	}
	n, err := l.s.acquire(l.wake)
	if err != nil {
		return err
	}
	n.raw = l.raw
	*l = *n
	return nil
}

// pull moves queued input into the pending buffer, unless it already
// exceeds the buffer limit, and reports whether the buffer grew.
func (l *lease) pull() bool {
	s := l.s
	if len(s.rx) > s.o.bufferLimit {
		return false
	}
	chunks, err := s.in.take()
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	s.rx = slices.Grow(s.rx, n)
	for _, c := range chunks {
		if s.o.terminal && !l.raw {
			var rewrote bool
			if s.rx, rewrote = s.vt.Append(s.rx, c); rewrote {
				s.rewrites++
			}
			continue
		}
		s.rx = append(s.rx, c...)
	}
	if err != nil && s.rxErr == nil {
		s.rxErr = err
	}
	if len(chunks) == 0 {
		return false
	}
	s.version++
	return true
}

// shrinkCap is the capacity above which a mostly empty buffer is
// reallocated, so one large output does not pin its memory.
const shrinkCap = 1 << 20

// consume drops the first n pending bytes in place. Nothing aliases rx:
// Results copy their text out of it.
func (l *lease) consume(n int) {
	if n <= 0 {
		return
	}
	s := l.s
	rest := len(s.rx) - n
	if cap(s.rx) > shrinkCap && rest < cap(s.rx)/4 {
		s.rx = slices.Clone(s.rx[n:])
	} else {
		s.rx = s.rx[:copy(s.rx, s.rx[n:])]
	}
	s.version++
}

// ended reports whether input ended and nothing is queued any more.
func (l *lease) ended() bool { return l.s.rxErr != nil }
