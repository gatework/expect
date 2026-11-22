// Package expect automates conversations with network devices and other
// interactive peers over a byte stream: wait for prompts and questions,
// answer them, and keep a reviewable transcript.
//
// A Session owns one io.ReadWriteCloser (an SSH channel, a Telnet
// connection, a PTY). Expect waits for one of several Cases; each Case
// pairs a Pattern (Literal, Line, regular expression, Glob, All, or the
// EOF, Timeout, Default and FullBuffer events) with an optional Handler
// that may reply and Continue. Reusable rules (WithBefore, WithAfter,
// WithRules) answer pagers and banners on every wait.
//
// Every session is recorded by default under ~/.local/share/gatework/expect, configured
// with WithRecorder or the EXPECT_RECORD_* environment variables (see
// package record). Secrets registered with Redact and RedactPattern are
// masked in transcripts and interactive output; Results always carry the
// raw bytes. Events (open, send, match, expect, close) go to an injected
// slog.Logger and to the jsonl transcript, with metadata only.
//
// The package matches and moves bytes; it does not know vendors, prompts
// or what a command's output means. Callers decide whether a command
// succeeded.
package expect
