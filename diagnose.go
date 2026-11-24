package expect

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"
	"unicode/utf8"
)

// tailMargin is how much input before a diagnostic tail is filtered with
// it, so a secret or a line rule cut by the tail start is still masked.
const tailMargin = 256

// maxCaseLabels bounds the case list of a failed-wait log line.
const maxCaseLabels = 16

// WithMatchTrace logs every scan of a wait at Debug, as Tcl's exp_internal
// does: the pending size, the case that matched or is settling, and the
// last n bytes of pending input. Failed waits also carry that tail. The
// tail is device output: it passes the session's redaction (Redact,
// RedactPattern) and is quoted, but enable it only to troubleshoot.
// Zero, the default, logs metadata only.
func WithMatchTrace(n int) SessionOption {
	return func(o *sessionOptions) error {
		if n < 0 {
			return configError("match trace", "negative")
		}
		o.trace = n
		return nil
	}
}

// tail returns the last n bytes of p redacted and quoted for a log line.
func (s *Session) tail(p []byte, n int) string {
	s.diagMu.Lock()
	f := s.rxFilter.Clone()
	s.diagMu.Unlock()
	start := max(0, len(p)-n-max(tailMargin, f.MaxSecret()))
	w := p[start:]
	if start > 0 {
		// Begin at a line, so line rules see whole lines, or at least past
		// any secret the cut may have split.
		if i := bytes.IndexByte(w, '\n'); i >= 0 {
			w = w[i+1:]
		} else {
			w = w[min(f.MaxSecret(), len(w)):]
		}
	}
	out := append(f.Append(w), f.Finish()...)
	if len(out) > n {
		out = out[len(out)-n:]
		for len(out) > 0 && !utf8.RuneStart(out[0]) {
			out = out[1:]
		}
	}
	return strconv.Quote(string(out))
}

// trace logs one scan of a member when WithMatchTrace is on.
func (m *member) trace(ctx context.Context, hit *entry) {
	s := m.s
	if s.o.trace == 0 || !s.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{slog.Int("pending", len(s.rx)), slog.String("tail", s.tail(s.rx, s.o.trace))}
	if hit != nil {
		attrs = append(attrs, slog.String("hit", hit.label()))
	}
	if m.settling != nil {
		attrs = append(attrs, slog.String("settling", m.settling.label()))
	}
	s.log.LogAttrs(context.WithoutCancel(ctx), slog.LevelDebug, "expect: scan", attrs...)
}

// waitFailed logs a failed wait at Info with what an operator needs to
// tell why: the cases waited for, the pending input, how long the peer
// was silent and the transcript to open. A cancelled wait is the
// caller's decision and logs at Debug.
func (m *member) waitFailed(ctx context.Context, op Operation, o matchOptions, err error) {
	s := m.s
	level := slog.LevelInfo
	if errors.Is(err, context.Canceled) {
		level = slog.LevelDebug
	}
	ctx = context.WithoutCancel(ctx)
	if !s.log.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{
		slog.String("op", string(op)), slog.String("err_class", string(classify(err))),
		slog.Any("cases", m.labels()), slog.Int("pending", len(s.rx)),
		slog.Duration("idle_for", s.Stats().Idle(time.Now()).Round(time.Millisecond)),
		slog.Duration("timeout", o.timeout), slog.Any("transcript", s.tr.Paths()),
	}
	if s.o.trace > 0 {
		attrs = append(attrs, slog.String("tail", s.tail(s.rx, s.o.trace)))
	}
	s.log.LogAttrs(ctx, level, "expect: wait failed", attrs...)
}

// labels names the member's cases in priority order.
func (m *member) labels() []string {
	var out []string
	for _, g := range m.groups() {
		for _, e := range g {
			if len(out) == maxCaseLabels {
				return append(out, "...")
			}
			out = append(out, e.label())
		}
	}
	return out
}

// label names a case for logs: its Name, or its scope, index and kind.
func (e *entry) label() string {
	if e.c.Name != "" {
		return e.c.Name
	}
	return string(e.scope) + "#" + strconv.Itoa(e.index) + ":" + e.c.Pattern.kindName()
}
