# Changelog

All notable changes are recorded here, following Keep a Changelog.

## [Unreleased]

### Added

- `WithTerminalFilter`: waits match the text a line-oriented reader sees.
  Control sequences (colors, cursor and erase commands, titles) are
  removed, and backspace and cursor-left erase the pending characters
  before them, as pagers and long-command redraws do. Transcripts,
  Interact and Relay keep the raw bytes.
- `Case.Settle` turns a case into a prompt: it matches only the final line
  of pending input and fires once no input has arrived for the Settle
  duration, so a prompt-like string in output cut at a chunk boundary
  (`description core#`, Huawei `#` separators) no longer ends a command
  early. End of input confirms at once; Poll does not wait.
- `WithEcho` synchronizes an Exchange on the echo of its line: input
  pending before the send and input arriving before the echo (late output,
  the prompt or echo of a timed-out command) is discarded instead of being
  taken as the new command's answer. A missing echo fails with
  `ErrNoEcho`. The echo stage reports discarded bytes as an `OpEcho` event.
- `Session.Drain` discards input until the peer is quiet, bounded by the
  relative timer (`OpDrain` event).
- Session lifecycle: `Done` and `Err` report a dropped peer, Close or a
  retiring write without starting a wait; `Stats` (bytes, last activity,
  waits, failures, `Idle`) serves pools and troubleshooting;
  `WithKeepalive` keeps idle sessions past exec-timeout, discarding only
  the answer to the keepalive and making receivers wait rather than fail
  with `ErrBusy` while it runs.
- Logs for troubleshooting: Info lines when a session opens and closes
  (lifetime, cause, bytes, waits, failures, transcript paths), a Warn when
  the transport does not interrupt Read within 5 s of Close, an Error
  with the stack (not the value) when a Background handler panics, and
  `OpPoll`, `OpBackground` and `OpRelay` events.
- Failed waits log an Info line `expect: wait failed` with the cases waited
  for, the pending size, how long the peer was silent, the timeout and the
  transcript paths. `WithMatchTrace(n)` adds Tcl `exp_internal`-style
  Debug lines per scan and a tail of the last n pending bytes, redacted
  (a secret cut by the tail start stays masked) and quoted.
- Transcript replay: `record.ReadCast` parses cast transcripts and
  `expecttest.Replay` plays one as the peer, so a session recorded on a
  real device reproduces offline as a regression test. Output frames are
  said again, input frames checked, and recorded secrets (`expect.Mask`)
  match any text (`expecttest.HearMasked`); `WithReplaySpeed` plays the
  recorded pauses.
- `Session.Resize` and the optional `Resizer` transport capability (spawn
  terminals implement it) change the peer's window and record a cast `r`
  event; `InteractTerminal` forwards the operator's window size on entry
  and on every SIGWINCH.
- `WithCharset` for GB18030, GBK and Big5 devices: input is decoded to
  UTF-8 across chunk boundaries before recording, redaction and matching,
  so Chinese prompts match and transcripts are readable; sends are
  encoded, and text the charset cannot represent fails with
  `ErrUnencodable` before anything is written. Relay transcodes between
  sessions of different charsets.

### Fixed

- `Required` transcripts write through instead of buffering, so a
  recording failure fails the session at once again.

### Performance

- Case-insensitive prefilters find candidates with `bytes.IndexByte` on a
  byte that has no case, instead of comparing byte by byte: 30 folded
  rules over 1.2 MB, -83%. Session benchmarks now read from an in-memory
  transport, so they measure the engine rather than `net.Pipe`
  hand-offs.

## [0.2.0] - 2026-11-21

### Changed

- Recordings now default to `~/.local/share/gatework/expect`, the shared
  gatework data home (was `~/.local/expect`). `EXPECT_RECORD_DIR` still
  overrides it; move old recordings by hand if they are still needed.
- `Then` and `ReplyThenContinue` now continue with `Continue` and restart
  the relative timer, like Tcl's `exp_continue`; use `ThenKeepTimer` for
  the previous behavior (`exp_continue -continue_timer`).

### Added

- `Session.Exchange`: send a line and wait, taking the input first so a
  busy session sends nothing; it always collects `Result.Output`.
- `WithOutput`: `Result.Output` collects everything a wait consumed across
  continued cases (Expect, Poll, Background, Group); the returning case's
  `Before` and `Match` share its memory.
- `Case.Omit` keeps a match's text (pager, prompt) out of `Result.Output`;
  `WithOutputLimit(n)` collects like `WithOutput` and fails the wait with
  `ErrOutputLimit` once more than n bytes were collected.
- `ThenKeepTimer`, `ReplyLineThenContinue`.

### Fixed

- `NoConsume` cases now observe each candidate once as input grows.
- Relay retains partial matches for rules as well as route cases.
- Interact and Relay apply secrets registered during an active session to
  subsequent output.

### Performance

- Regular expressions, globs and case-folded literals are prefiltered by
  a literal every match must contain, searched incrementally: a glob
  prompt such as `*router#` no longer rescans the whole buffer per chunk
  (scan benchmark 2.2 s → 47 µs per 100 KB built in 2000 chunks).
- Consumed input is dropped in place instead of copying the rest; input
  chunks are appended with one growth; unfiltered input is copied with
  one `bytes.Clone`; event attributes are built only when debug logging
  is enabled.
- Transcript files are written through a 64 KiB buffer, flushed within
  one second, when full and at Close, instead of one system call per
  chunk; the log and cast writers reuse their buffers and encode JSON by
  hand (recorded large output: −27% time, −85% allocations). A crash now
  loses at most about one second of transcript.

## [0.1.0] - 2026-11-20

### Added

- First release: a reimplementation of go-expect's behavior on one receive
  lease and one matching engine ([ADR 0001](docs/adr/0001-expect-rewrite.md)).
- Session transcripts on by default (`record`): log, asciicast v2 and jsonl
  under `~/.local/expect`, configured by options or `EXPECT_RECORD_*`.
- Named cases, `RedactPattern`, `WithSearchWindow`, `ReplyLine`, `WithLabel`,
  `WithWindow`, `Session.Transcript`.
- `spawn` (Unix PTY, Windows ConPTY, `Console`) and `expecttest` scripted
  peers.
