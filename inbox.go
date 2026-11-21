package expect

import "sync"

// readAhead bounds the bytes the read pump queues ahead of the receiver.
const readAhead = 256 << 10

// inbox hands input from the read pump to the current receiver. The pump
// blocks while readAhead bytes are queued, which bounds memory without
// dropping data.
type inbox struct {
	mu     sync.Mutex // guards the fields below
	chunks [][]byte
	size   int
	err    error         // terminal read error, sticky
	wake   chan struct{} // receiver signal (cap 1), nil without receiver
	room   chan struct{} // pump signal (cap 1)
}

func newInbox() *inbox { return &inbox{room: make(chan struct{}, 1)} }

// put queues p, waiting for room until done closes. It reports whether p
// was queued.
func (b *inbox) put(p []byte, done <-chan struct{}) bool {
	for {
		b.mu.Lock()
		if b.size < readAhead {
			b.chunks = append(b.chunks, p)
			b.size += len(p)
			b.signal()
			b.mu.Unlock()
			return true
		}
		b.mu.Unlock()
		select {
		case <-b.room:
		case <-done:
			return false
		}
	}
}

// fail records the terminal read error.
func (b *inbox) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
	b.signal()
}

// take removes every queued chunk and returns them with the terminal
// error, which stays recorded.
func (b *inbox) take() ([][]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chunks := b.chunks
	b.chunks, b.size = nil, 0
	if len(chunks) > 0 {
		select {
		case b.room <- struct{}{}:
		default:
		}
	}
	return chunks, b.err
}

// attach directs wake-ups to w (nil detaches) and wakes w at once when
// input is already waiting.
func (b *inbox) attach(w chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wake = w
	if len(b.chunks) > 0 || b.err != nil {
		b.signal()
	}
}

// signal wakes the receiver; b.mu is held.
func (b *inbox) signal() {
	if b.wake == nil {
		return
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
