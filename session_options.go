package expect

import (
	"log/slog"
	"slices"
	"time"

	"github.com/gatework/expect/record"
)

const (
	defaultTimeout      = 30 * time.Second
	defaultWriteTimeout = 10 * time.Second
	defaultBufferLimit  = 8 << 20
)

// SessionOption configures a Session. Options are validated before the
// transport is touched; a later option overrides an earlier one.
type SessionOption func(*sessionOptions) error

type sessionOptions struct {
	timeout      time.Duration
	writeTimeout time.Duration
	bufferLimit  int
	lineEnding   string
	logger       *slog.Logger
	observers    []Observer
	recorder     *record.Recorder
	recorderSet  bool
	label        string
	cols, rows   int
	before       []Case
	after        []Case
	removeNulls  bool
	stripParity  bool
	terminal     bool
}

func defaultSessionOptions() sessionOptions {
	return sessionOptions{
		timeout: defaultTimeout, writeTimeout: defaultWriteTimeout, bufferLimit: defaultBufferLimit,
		lineEnding: "\n", logger: slog.New(slog.DiscardHandler),
	}
}

// WithDefaultTimeout sets the relative timer of a wait without
// WithTimeout; zero (NoTimeout) disables it. The default is 30 seconds.
func WithDefaultTimeout(d time.Duration) SessionOption {
	return func(o *sessionOptions) error {
		if d < 0 {
			return configError("default timeout", "negative")
		}
		o.timeout = d
		return nil
	}
}

// WithWriteTimeout bounds one write; zero disables it. The default is 10
// seconds.
func WithWriteTimeout(d time.Duration) SessionOption {
	return func(o *sessionOptions) error {
		if d < 0 {
			return configError("write timeout", "negative")
		}
		o.writeTimeout = d
		return nil
	}
}

// WithBufferLimit bounds unmatched input in bytes. The default is 8 MiB;
// up to one read and the 256 KiB read-ahead may be held beyond it.
func WithBufferLimit(n int) SessionOption {
	return func(o *sessionOptions) error {
		if n <= 0 {
			return configError("buffer limit", "not positive")
		}
		o.bufferLimit = n
		return nil
	}
}

// WithLineEnding sets what SendLine appends; the default is "\n". Many
// devices expect "\r".
func WithLineEnding(s string) SessionOption {
	return func(o *sessionOptions) error {
		o.lineEnding = s
		return nil
	}
}

// WithLogger receives session events at debug level, with "session" and
// "label" attributes. Payload is never logged. The default discards.
func WithLogger(l *slog.Logger) SessionOption {
	return func(o *sessionOptions) error {
		if l == nil {
			return configError("logger", "nil")
		}
		o.logger = l
		return nil
	}
}

// WithObserver adds observers of completed operations.
func WithObserver(observers ...Observer) SessionOption {
	return func(o *sessionOptions) error {
		if slices.ContainsFunc(observers, func(o Observer) bool { return o == nil }) {
			return configError("observer", "nil")
		}
		o.observers = append(o.observers, observers...)
		return nil
	}
}

// WithRecorder records the session transcript with r.
//
// Without WithRecorder or WithoutRecording, NewSession records with a
// Recorder built from record.ConfigFromEnv: recording is on by default,
// under ~/.local/share/gatework/expect, and EXPECT_RECORD=off turns it off.
func WithRecorder(r *record.Recorder) SessionOption {
	return func(o *sessionOptions) error {
		if r == nil {
			return configError("recorder", "nil")
		}
		o.recorder, o.recorderSet = r, true
		return nil
	}
}

// WithoutRecording disables the transcript regardless of the environment.
func WithoutRecording() SessionOption { return WithRecorder(record.Disabled()) }

// WithLabel names the peer, typically the device address; it appears in
// log attributes and transcript file names.
func WithLabel(label string) SessionOption {
	return func(o *sessionOptions) error {
		o.label = label
		return nil
	}
}

// WithWindow records the terminal size in the transcript; it does not
// change the transport.
func WithWindow(cols, rows int) SessionOption {
	return func(o *sessionOptions) error {
		if cols < 0 || rows < 0 {
			return configError("window", "negative")
		}
		o.cols, o.rows = cols, rows
		return nil
	}
}

// WithBefore installs rules matched before every wait's own cases.
func WithBefore(cases ...Case) SessionOption {
	return func(o *sessionOptions) error {
		o.before = slices.Clone(cases)
		return validateRules(o.before)
	}
}

// WithAfter installs rules matched after every wait's own cases.
func WithAfter(cases ...Case) SessionOption {
	return func(o *sessionOptions) error {
		o.after = slices.Clone(cases)
		return validateRules(o.after)
	}
}

// WithRemoveNulls drops NUL bytes from input before matching.
func WithRemoveNulls(enabled bool) SessionOption {
	return func(o *sessionOptions) error {
		o.removeNulls = enabled
		return nil
	}
}

// WithStripParity clears the high bit of every input byte, for serial
// lines with parity.
func WithStripParity(enabled bool) SessionOption {
	return func(o *sessionOptions) error {
		o.stripParity = enabled
		return nil
	}
}

// WithTerminalFilter makes waits see the text a line-oriented reader
// would: terminal control sequences (colors, cursor and erase commands,
// window titles) are removed, and backspace and cursor-left erase the
// characters before them on the current line, as devices do to wipe a
// pager prompt or redraw a long command. Patterns, Results and Output then
// hold plain text. Transcripts keep the raw bytes for faithful replay;
// Interact and Relay forward raw bytes too.
func WithTerminalFilter() SessionOption {
	return func(o *sessionOptions) error {
		o.terminal = true
		return nil
	}
}

func applySessionOptions(opts []SessionOption) (sessionOptions, error) {
	o := defaultSessionOptions()
	for _, opt := range opts {
		if opt == nil {
			return o, configError("session option", "nil")
		}
		if err := opt(&o); err != nil {
			return o, err
		}
	}
	return o, nil
}

func validateRules(cases []Case) error {
	for i, c := range cases {
		if err := c.validate(i, true); err != nil {
			return err
		}
	}
	return nil
}
