# CLAUDE.md

@AGENTS.md

## Commands

```bash
make check                                   # full gate (GOWORK=off)
go test -race -shuffle=on -count=1 ./...     # tests
go test -run 'TestSession_Expect' -count=1 . # one test
go run ./examples/dialogue                   # runnable dialogue (records unless EXPECT_RECORD=off)
```

Fuzz targets: `FuzzFindChunks` (internal/scan), `FuzzFilterChunks` (internal/redact), `FuzzSessionChunks`, `FuzzCharsetChunks` (root), `FuzzFilterChunks` (internal/vt). Add new ones to the `fuzz` target in the Makefile.

## Map

- `session.go` lifecycle, read pump, redaction filters, transcript; `inbox.go` pump→receiver hand-off; `lease.go` the single receive slot.
- `wait.go` the matching engine (`waiter`, `member`, `entry`, `budget`); `expect.go` Expect/Poll; `background.go`, `group.go`, `interact*.go`, `relay.go` are receivers on top of it.
- `write.go` serialized sends, deadlines, `WriteError`; `event.go` events, slog, error classes.
- `record/` transcripts (log, cast, jsonl) and `EXPECT_RECORD_*` configuration; `spawn/` local PTY/ConPTY processes; `expecttest/` scripted peers.
