package expect

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/text/encoding"

	"github.com/gatework/expect/internal/redact"
	"github.com/gatework/expect/internal/vt"
	"github.com/gatework/expect/record"
)

const readSize = 32 << 10

// Session is a conversation with one peer over a byte stream it owns.
//
// One receiver at a time (Expect, Poll, Interact, Background, Group,
// Relay) consumes input; a second gets ErrBusy. Sends are serialized and
// may run concurrently with the receiver. Close may be called at any time
// and from any goroutine. A Session must not be copied.
type Session struct {
	id  string
	t   io.ReadWriteCloser
	o   sessionOptions
	log *slog.Logger
	tr  *record.Transcript
	in  *inbox

	// Receive state, owned by the current receiver (busy set).
	busy     atomic.Bool
	rx       []byte    // pending input
	rxErr    error     // terminal read error, once seen by a receiver
	version  uint64    // changes whenever rx changes
	vt       vt.Filter // WithTerminalFilter state across chunks
	rewrites uint64    // counts erasures of pending bytes; scans restart

	rules   atomic.Pointer[ruleSet]
	rulesMu sync.Mutex // serializes SetRules

	wmu sync.Mutex        // serializes writes and guards enc
	enc *encoding.Encoder // WithCharset encoder, or nil
	dec *streamDecoder    // WithCharset decoder, owned by the read pump

	diagMu   sync.Mutex // guards the redaction filters and the transcript writes
	rxFilter *redact.Filter
	txFilter *redact.Filter
	// redactGen changes whenever secrets or rules are added, so copies of
	// rxFilter held by Interact and Relay can catch up.
	redactGen atomic.Uint64

	// scope ends when the session closes; it bounds handler contexts. It
	// is lifecycle state, not a request context.
	scope     context.Context //nolint:containedctx // lifecycle scope, cancelled by Close
	cancel    context.CancelFunc
	closed    atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	pump      sync.WaitGroup
}

type ruleSet struct {
	before, after []Case
	version       uint64
}

// NewSession starts a conversation over t and takes ownership of t only
// when it succeeds. t.Close must interrupt a blocked Read and Write.
// Options are validated before t is used.
func NewSession(t io.ReadWriteCloser, opts ...SessionOption) (*Session, error) {
	if isNil(t) {
		return nil, configError("transport", "nil")
	}
	o, err := applySessionOptions(opts)
	if err != nil {
		return nil, err
	}
	rec, err := o.resolveRecorder()
	if err != nil {
		return nil, err
	}
	s := &Session{
		id: newID(), t: t, o: o, in: newInbox(), done: make(chan struct{}),
		rxFilter: redact.New(""), txFilter: redact.New(""),
	}
	if o.charset != nil {
		s.enc, s.dec = o.charset.NewEncoder(), newStreamDecoder(o.charset)
	}
	s.tr, err = rec.Open(record.Meta{Session: s.id, Label: o.label, Width: o.cols, Height: o.rows})
	if err != nil {
		return nil, &ConfigError{Option: "recorder", Reason: "cannot open transcript", Cause: err}
	}
	s.log = slog.New(slog.NewMultiHandler(o.logger.Handler(), s.tr.Handler())).With("session", s.id, "label", o.label)
	s.rules.Store(&ruleSet{before: o.before, after: o.after})
	s.scope, s.cancel = context.WithCancel(context.Background())
	s.pump.Go(s.readLoop)
	s.emit(s.scope, Event{Op: OpOpen})
	return s, nil
}

// ID returns the process-unique session identifier used in events and
// transcript names.
func (s *Session) ID() string { return s.id }

// Label returns the peer label set with WithLabel.
func (s *Session) Label() string { return s.o.label }

// Transcript returns the files recording this session; empty when
// recording is off.
func (s *Session) Transcript() []string { return s.tr.Paths() }

// Close ends the session: it interrupts pending reads, writes and waits,
// closes the transport, joins the read pump and flushes the transcript.
// It does not wait for handlers to return. Close is idempotent and
// returns the same error each time.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		start := time.Now()
		s.closed.Store(true)
		close(s.done)
		s.cancel()
		err := s.t.Close()
		s.pump.Wait()
		s.emit(s.scope, Event{Op: OpClose, Duration: time.Since(start), Err: classify(err)})
		s.closeErr = errors.Join(err, s.finishTranscript())
	})
	return s.closeErr
}

// Redact masks secrets in the transcript and in interactive output from
// now on; Results keep the raw bytes. Register a secret before it is sent
// or received.
func (s *Session) Redact(secrets ...string) error {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if err := s.rxFilter.AddSecrets(secrets...); err != nil {
		return &ConfigError{Option: "secret", Reason: "empty", Cause: err}
	}
	defer s.redactGen.Add(1)
	return s.txFilter.AddSecrets(secrets...)
}

// RedactPattern masks, in every recorded line re matches, its submatches
// (or the whole match without groups): for example
// `password (?:cipher|irreversible-cipher) (\S+)`.
func (s *Session) RedactPattern(re *regexp.Regexp) error {
	if re == nil {
		return configError("redact pattern", "nil")
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	s.rxFilter.AddRule(re)
	s.txFilter.AddRule(re)
	s.redactGen.Add(1)
	return nil
}

// Rules returns copies of the session's Before and After rules.
func (s *Session) Rules() (before, after []Case) {
	rs := s.rules.Load()
	return append([]Case(nil), rs.before...), append([]Case(nil), rs.after...)
}

// SetRules atomically replaces the Before and After rules. A running wait
// switches at its next scan with fresh counters for the new rules.
func (s *Session) SetRules(before, after []Case) error {
	if err := validateRules(before); err != nil {
		return err
	}
	if err := validateRules(after); err != nil {
		return err
	}
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	old := s.rules.Load()
	s.rules.Store(&ruleSet{before: append([]Case(nil), before...), after: append([]Case(nil), after...), version: old.version + 1})
	return nil
}

// String identifies the session without payload.
func (s *Session) String() string { return fmt.Sprintf("expect.Session(%s)", s.id) }

// GoString is String.
func (s *Session) GoString() string { return s.String() }

// readLoop is the only reader of the transport. It exits on a read error
// or when the session closes.
func (s *Session) readLoop() {
	buf := make([]byte, readSize)
	for {
		n, err := s.t.Read(buf)
		if n > 0 || err != nil && s.dec != nil {
			p := s.normalize(buf[:n])
			if s.dec != nil {
				p = s.dec.decode(p, err != nil)
			}
			if rerr := s.recordReceived(p); rerr != nil {
				s.in.fail(rerr)
				return
			}
			if len(p) > 0 && !s.in.put(p, s.done) {
				s.in.fail(ErrClosed)
				return
			}
		}
		if err != nil {
			if s.closed.Load() {
				err = errors.Join(ErrClosed, err)
			}
			s.in.fail(err)
			return
		}
	}
}

// normalize copies p, applying the input filters.
func (s *Session) normalize(p []byte) []byte {
	if !s.o.stripParity && !s.o.removeNulls {
		return bytes.Clone(p)
	}
	out := make([]byte, 0, len(p))
	for _, c := range p {
		if s.o.stripParity {
			c &= 0x7f
		}
		if c == 0 && s.o.removeNulls {
			continue
		}
		out = append(out, c)
	}
	return out
}

// outputFilter is a copy of the receive filter for one output stream
// (Interact or Relay output) that follows later Redact calls.
type outputFilter struct {
	s   *Session
	f   *redact.Filter
	gen uint64
}

func (s *Session) newOutputFilter() *outputFilter {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	return &outputFilter{s: s, f: s.rxFilter.Clone(), gen: s.redactGen.Load()}
}

// Append filters p with the current secrets.
func (o *outputFilter) Append(p []byte) []byte {
	o.sync()
	return o.f.Append(p)
}

// Finish releases what the filter holds.
func (o *outputFilter) Finish() []byte {
	o.sync()
	return o.f.Finish()
}

// sync adopts secrets and rules added since the last call.
func (o *outputFilter) sync() {
	if o.s.redactGen.Load() == o.gen {
		return
	}
	o.s.diagMu.Lock()
	defer o.s.diagMu.Unlock()
	o.f.Sync(o.s.rxFilter)
	o.gen = o.s.redactGen.Load()
}

func (s *Session) recordReceived(p []byte) error {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	return s.tr.Received(s.rxFilter.Append(p))
}

func (s *Session) recordSent(p []byte) error {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	return s.tr.Sent(s.txFilter.Append(p))
}

func (s *Session) finishTranscript() error {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	return errors.Join(s.tr.Received(s.rxFilter.Finish()), s.tr.Sent(s.txFilter.Finish()), s.tr.Close())
}

// resolveRecorder returns the configured recorder or the environment
// default.
func (o *sessionOptions) resolveRecorder() (*record.Recorder, error) {
	if o.recorderSet {
		return o.recorder, nil
	}
	c, err := record.ConfigFromEnv()
	if err != nil {
		return nil, &ConfigError{Option: "recorder", Reason: "environment", Cause: err}
	}
	c.Logger = o.logger
	r, err := record.New(c)
	if err != nil {
		return nil, &ConfigError{Option: "recorder", Reason: "configuration", Cause: err}
	}
	return r, nil
}

// scoped returns ctx bounded by the session lifetime.
func (s *Session) scoped(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.scope, cancel)
	return ctx, func() { stop(); cancel() }
}

// closedErr turns an interruption caused by Close into ErrClosed.
func (s *Session) closedErr(err error) error {
	if s.closed.Load() && !errors.Is(err, ErrClosed) {
		return errors.Join(ErrClosed, err)
	}
	return err
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() { //nolint:exhaustive // only nillable kinds can hide a typed nil
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
