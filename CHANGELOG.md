# Changelog

All notable changes are recorded here, following Keep a Changelog.

## [Unreleased]

### Changed

- Recordings now default to `~/.local/share/gatework/expect`, the shared
  gatework data home (was `~/.local/expect`). `EXPECT_RECORD_DIR` still
  overrides it; move old recordings by hand if they are still needed.
- `Then` and `ReplyThenContinue` now continue with `Continue` and restart
  the relative timer, like Tcl's `exp_continue`; use `ThenKeepTimer` for
  the previous behavior (`exp_continue -continue_timer`).

### Added

- `Session.Exchange`: send a line and wait, taking the input first so a
  busy session sends nothing; `Result.Output` collects everything the wait
  consumed across continued cases.
- `ThenKeepTimer`, `ReplyLineThenContinue`.

### Performance

- Regular expressions, globs and case-folded literals are prefiltered by
  a literal every match must contain, searched incrementally: a glob
  prompt such as `*router#` no longer rescans the whole buffer per chunk
  (scan benchmark 2.2 s → 47 µs per 100 KB built in 2000 chunks).
- Consumed input is dropped in place instead of copying the rest; input
  chunks are appended with one growth; unfiltered input is copied with
  one `bytes.Clone`; event attributes are built only when debug logging
  is enabled.

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
