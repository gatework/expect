package record

import (
	"bufio"
	"errors"
	"os"
	"sync"
	"time"
)

const (
	// bufferSize is the write buffer of one transcript file.
	bufferSize = 64 << 10
	// flushDelay bounds how long recorded bytes stay in memory before they
	// reach the file; a crash loses at most this much of a transcript.
	flushDelay = time.Second
)

// bufferedFile buffers the writes to one transcript file so the read
// pump does not make a system call per chunk. Bytes reach the file when
// the buffer fills, flushDelay after the first unflushed write, or at
// Close. A timer, not a goroutine, schedules the delayed flush.
type bufferedFile struct {
	mu     sync.Mutex // guards the fields below; flushes run on a timer goroutine
	f      *os.File
	w      *bufio.Writer
	timer  *time.Timer
	armed  bool
	closed bool
	err    error // a delayed flush failure, reported by the next Write
	// through flushes every write, for Required transcripts whose write
	// errors must fail the session at once.
	through bool
}

func newBufferedFile(f *os.File, through bool) *bufferedFile {
	return &bufferedFile{f: f, w: bufio.NewWriterSize(f, bufferSize), through: through}
}

func (b *bufferedFile) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.w.Write(p)
	if b.through {
		return n, errors.Join(err, b.w.Flush())
	}
	if !b.armed && b.w.Buffered() > 0 {
		b.armed = true
		if b.timer == nil {
			b.timer = time.AfterFunc(flushDelay, b.flushLater)
		} else {
			b.timer.Reset(flushDelay)
		}
	}
	return n, err
}

// Close flushes what is buffered and closes the file. It is idempotent.
func (b *bufferedFile) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}
	return errors.Join(b.w.Flush(), b.f.Close())
}

func (b *bufferedFile) flushLater() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed = false
	if b.closed {
		return
	}
	if err := b.w.Flush(); err != nil && b.err == nil {
		b.err = err
	}
}
