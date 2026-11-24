# expect

Go 1.26+ engine for automated conversations with network devices and other
interactive peers: wait for prompts and questions, answer them, never lose
input or replay a write, and keep a redacted transcript of every session.
It is the stream layer under [net-connector](https://github.com/gatework/net-connector).

```sh
go get github.com/gatework/expect
```

## Minimal use

```go
s, err := expect.NewSession(stream, expect.WithLineEnding("\r"), expect.WithLabel("10.0.0.1"))
if err != nil {
	return err
}
defer s.Close()
_ = s.Redact(password) // masked in transcripts and interactive output

r, err := s.Expect(ctx, []expect.Case{
	{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine(password))},
	{Name: "prompt", Pattern: expect.MustCompile(`(?m)^(?P<host>[\w.-]+)#$`)},
}, expect.WithTimeout(30*time.Second))
```

See [example_test.go](example_test.go) (pager, background syslog watch,
interface resets, a device session) and `go run ./examples/dialogue`.

## Real device sessions

Devices print colors and cursor commands, wipe their pagers, cut output at
any byte, leave late messages behind a prompt and, on older Chinese
platforms, speak GB18030. A session for them usually combines:

```go
s, err := expect.NewSession(stream,
	expect.WithTerminalFilter(),                  // match the text a screen shows
	expect.WithCharset(simplifiedchinese.GB18030), // only for GB18030/GBK/Big5 devices
	expect.WithKeepalive(5*time.Minute),           // outlive the device's exec-timeout
)
prompt := expect.Case{Name: "prompt", Pattern: expect.Line("router#"), Omit: true,
	Settle: 100 * time.Millisecond} // a prompt-like string cut at a read is not the prompt
r, err := s.Exchange(ctx, "show version", []expect.Case{pager, prompt},
	expect.WithEchoWait(2*time.Second), // answers stay with their command
	expect.WithOutputLimit(64<<20))     // r.Output: the command's output, pagers omitted
```

When a wait fails, the session logger gets one Info line with the cases,
the pending size, the device's silence and the transcript to open;
`WithMatchTrace(n)` adds a redacted tail of what the device sent. A
transcript recorded on a real device replays offline with
`expecttest.Replay`.

## Session transcripts

Every session is recorded by default under `~/.local/share/gatework/expect/<date>/` as
`log` (grep-friendly, `<<` received / `>>` sent) and `cast` (replay with
`asciinema play`); `jsonl` adds one event per match, send and close. Secrets
registered with `Redact` and `RedactPattern` are masked.

| Setting | Variable | Default |
| --- | --- | --- |
| on/off | `EXPECT_RECORD` | `on` |
| directory | `EXPECT_RECORD_DIR` | `~/.local/share/gatework/expect` |
| formats | `EXPECT_RECORD_FORMATS` | `log,cast` |
| file cap (MB) | `EXPECT_RECORD_MAX_SIZE` | `100` |
| retention | `EXPECT_RECORD_MAX_AGE` | `720h` |
| fail session on recording error | `EXPECT_RECORD_REQUIRED` | `off` |

Explicit options win over the environment: `expect.WithRecorder(r)` with a
`record.Config`, or `expect.WithoutRecording()`.

## Capabilities

| Task | API |
| --- | --- |
| Match | `Literal`, `Line`, `Compile`/`MustCompile`, `FromRegexp`, `Glob`, `All`, `Pattern.Fold`, `Case.Settle` |
| Command and answer | `Exchange`, `WithEcho`, `WithEchoWait`, `Drain`, `WithOutput`, `WithOutputLimit`, `Case.Omit`, `Then`/`ThenKeepTimer` |
| Device text | `WithTerminalFilter`, `WithCharset`, `WithRemoveNulls`, `WithStripParity` |
| Events | `EOF`, `Timeout`, `Default`, `FullBuffer` |
| Reuse rules | `WithBefore`, `WithAfter`, `WithRules`, `SetRules` |
| Bound a wait | `WithTimeout`, `WithIdleTimeout`, `Case.Limit`, `WithSearchWindow` |
| Large output | `WithStreamingFallback`, `WithBufferLimit` |
| Send | `Send`, `SendLine`, `SendSlow`, `SendPaced`, `SendNull`, `SendBreak` |
| Observe | `WithLogger`, `WithObserver`, `WithMatchTrace`, `Session.Transcript`, `Session.Stats` |
| Lifecycle | `Session.Done`, `Session.Err`, `WithKeepalive`, `Session.Resize` |
| Hand over | `Interact`, `InteractTerminal` |
| Several sessions | `Group`, `Relay` |
| Asynchronous watch | `StartBackground` (`Pause`, `Resume`, `Stop`, `Wait`) |
| Local programs | `spawn.Start`, `spawn.NewConsole` |
| Tests | `expecttest.Pipe` scripted peers, `expecttest.Replay` and `record.ReadCast` for recorded transcripts |

## Documentation

[design](docs/design.md) · [contracts](docs/contracts.md) ·
[ADR 0001](docs/adr/0001-expect-rewrite.md) · [glossary](GLOSSARY.md) ·
[changelog](CHANGELOG.md)

## Tests

`make check` runs the shared gates plus Windows cross-compilation, FreeBSD
vet, bounded fuzzing, a benchmark smoke run and the example. Tests use
`net.Pipe` scripted peers and local PTYs; they are not real-device evidence.
Windows ConPTY is cross-compiled only.
