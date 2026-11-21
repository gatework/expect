// Package expecttest provides test fixtures for code built on expect: a
// scripted peer that plays a device byte for byte, and a writer that logs
// to the test. Sessions it creates do not record transcripts.
package expecttest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gatework/expect"
)

type stepKind uint8

const (
	stepSay stepKind = iota + 1
	stepHear
	stepPause
	stepHangup
)

// Step is one move of a scripted peer.
type Step struct {
	kind stepKind
	text string
	d    time.Duration
}

// Say makes the peer send text.
func Say(text string) Step { return Step{kind: stepSay, text: text} }

// Hear makes the peer read exactly text next; anything else fails the
// test. Use it to prove what was, and was not, sent.
func Hear(text string) Step { return Step{kind: stepHear, text: text} }

// Pause makes the peer wait d before its next step.
func Pause(d time.Duration) Step { return Step{kind: stepPause, d: d} }

// Hangup makes the peer close the connection.
func Hangup() Step { return Step{kind: stepHangup} }

// Peer is a running script.
type Peer struct {
	tb    testing.TB
	conn  net.Conn
	steps []Step
	wg    sync.WaitGroup
	mu    sync.Mutex // guards err and at
	err   error
	at    int
	extra bytes.Buffer
}

// Pipe returns a Session connected to a peer that plays steps. At cleanup
// the session is closed, and the test fails if the script did not finish,
// heard something unexpected, or the session sent bytes after the last
// step. Options apply after expect.WithoutRecording.
func Pipe(tb testing.TB, steps []Step, opts ...expect.SessionOption) (*expect.Session, *Peer) {
	tb.Helper()
	local, remote := net.Pipe()
	s, err := expect.NewSession(local, append([]expect.SessionOption{expect.WithoutRecording()}, opts...)...)
	if err != nil {
		tb.Fatal(err)
	}
	p := &Peer{tb: tb, conn: remote, steps: steps}
	p.wg.Go(p.play)
	tb.Cleanup(func() {
		tb.Helper()
		_ = s.Close()
		_ = remote.Close()
		p.wg.Wait()
		if err := p.Err(); err != nil {
			tb.Errorf("expecttest: %v", err)
		}
	})
	return s, p
}

// Err reports the first script failure, or that the script is unfinished.
func (p *Peer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.at < len(p.steps) {
		return fmt.Errorf("script stopped at step %d of %d", p.at+1, len(p.steps))
	}
	if p.extra.Len() > 0 {
		return fmt.Errorf("session sent %q after the script ended", p.extra.String())
	}
	return nil
}

// Done reports whether every step was played.
func (p *Peer) Done() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.at == len(p.steps)
}

func (p *Peer) play() {
	for i, st := range p.steps {
		if err := p.step(st); err != nil {
			p.fail(fmt.Errorf("step %d: %w", i+1, err))
			return
		}
		p.mu.Lock()
		p.at = i + 1
		p.mu.Unlock()
		if st.kind == stepHangup {
			return
		}
	}
	p.drain()
}

func (p *Peer) step(st Step) error {
	switch st.kind {
	case stepSay:
		_, err := io.WriteString(p.conn, st.text)
		return err
	case stepHear:
		got := make([]byte, len(st.text))
		n, err := io.ReadFull(p.conn, got)
		if string(got[:n]) != st.text {
			return fmt.Errorf("heard %q, want %q", got[:n], st.text)
		}
		return err
	case stepPause:
		time.Sleep(st.d)
		return nil
	case stepHangup:
		return p.conn.Close()
	}
	return errors.New("unknown step")
}

// drain records anything the session sends after the script ended.
func (p *Peer) drain() {
	buf := make([]byte, 512)
	for {
		n, err := p.conn.Read(buf)
		p.mu.Lock()
		p.extra.Write(buf[:n])
		p.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (p *Peer) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

// NewWriter returns a writer that logs each line to tb, for example as an
// Interact output or a slog handler destination.
func NewWriter(tb testing.TB) io.Writer {
	tb.Helper()
	return &logWriter{tb: tb}
}

type logWriter struct {
	tb   testing.TB
	mu   sync.Mutex // guards line
	line strings.Builder
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range string(p) {
		if c == '\n' {
			w.tb.Log(w.line.String())
			w.line.Reset()
			continue
		}
		w.line.WriteRune(c)
	}
	return len(p), nil
}
