# Changelog

All notable changes are recorded here, following Keep a Changelog.

## [Unreleased]

### Changed

- Recordings now default to `~/.local/share/gatework/expect`, the shared
  gatework data home (was `~/.local/expect`). `EXPECT_RECORD_DIR` still
  overrides it; move old recordings by hand if they are still needed.

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
