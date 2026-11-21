package expect

import "slices"

// lease is the exclusive right to consume a session's input. Every
// receiver holds one while it runs; the receive state of the session
// (rx, rxErr, version) belongs to the holder.
type lease struct {
	s    *Session
	wake chan struct{}
	held bool
}

// acquire takes the receive slot. Input arrival signals wake; nil makes a
// private channel. A Group passes one channel shared by its members.
func (s *Session) acquire(wake chan struct{}) (*lease, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if !s.busy.CompareAndSwap(false, true) {
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
	for _, c := range chunks {
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

// consume drops the first n pending bytes.
func (l *lease) consume(n int) {
	if n <= 0 {
		return
	}
	s := l.s
	s.rx = slices.Clone(s.rx[n:])
	s.version++
}

// ended reports whether input ended and nothing is queued any more.
func (l *lease) ended() bool { return l.s.rxErr != nil }
