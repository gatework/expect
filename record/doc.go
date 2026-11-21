// Package record keeps session transcripts so that an operator can review
// or replay what was exchanged with a device.
//
// A Recorder is configured once (explicit Config fields, otherwise
// EXPECT_RECORD_* environment variables, otherwise the defaults) and opens
// one Transcript per session under
//
//	<Dir>/<YYYY-MM-DD>/<label>_<HHMMSS>_<session>.<format>
//
// Formats:
//
//   - log: received (<<) and sent (>>) text, one timestamped line each.
//   - cast: asciicast v2, replayable with "asciinema play"; output is an "o"
//     event and input an "i" event.
//   - jsonl: one JSON object per session event (open, send, match,
//     timeout, close), written through log/slog.
//
// A Transcript stores what it is given. Callers redact secrets before
// handing bytes over; the expect package does so for every session.
// Each file stops with a truncation marker at MaxSizeMB; old sessions are
// removed by day directory (MaxAge). Directories are created 0700 and files 0600 because transcripts contain
// device configuration.
package record
