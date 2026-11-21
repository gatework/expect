package record

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Format is a transcript file format.
type Format string

// Transcript formats.
const (
	FormatLog   Format = "log"
	FormatCast  Format = "cast"
	FormatJSONL Format = "jsonl"
)

// DefaultDir is the transcript root, relative to the home directory, used
// when Config.Dir is empty.
const DefaultDir = ".local/expect"

// ErrConfig reports an invalid configuration value.
var ErrConfig = errors.New("record: invalid configuration")

// Config configures a Recorder. Zero fields that have a documented default
// take it in New. Environment variables are read only by ParseConfig and
// ConfigFromEnv; a Config built in code is used as is.
type Config struct {
	// Enabled turns recording on. EXPECT_RECORD (on/off, true/false, 1/0).
	Enabled bool `env:"EXPECT_RECORD" envDefault:"on"`
	// Dir is the transcript root; "~/" expands to the home directory.
	// Empty means ~/.local/expect. EXPECT_RECORD_DIR.
	Dir string `env:"EXPECT_RECORD_DIR"`
	// Formats lists the files kept per session; empty means log and cast.
	// EXPECT_RECORD_FORMATS (comma separated).
	Formats []Format `env:"EXPECT_RECORD_FORMATS" envDefault:"log,cast" envSeparator:","`
	// MaxSizeMB caps each transcript file; a file that reaches it ends
	// with a truncation marker. Zero means 100. EXPECT_RECORD_MAX_SIZE.
	MaxSizeMB int `env:"EXPECT_RECORD_MAX_SIZE" envDefault:"100"`
	// MaxAge removes day directories older than this when a Recorder is
	// created; zero keeps everything. EXPECT_RECORD_MAX_AGE (Go duration).
	MaxAge time.Duration `env:"EXPECT_RECORD_MAX_AGE" envDefault:"720h"`
	// Required makes a recording failure fail the session; otherwise the
	// failure is logged once and that file is dropped.
	// EXPECT_RECORD_REQUIRED.
	Required bool `env:"EXPECT_RECORD_REQUIRED" envDefault:"off"`

	// Logger receives recording failures; nil discards them.
	Logger *slog.Logger `env:"-"`
	// Now is the clock; nil uses time.Now.
	Now func() time.Time `env:"-"`
}

// DefaultConfig returns the configuration used when nothing is set:
// recording on, log and cast files under ~/.local/expect, 100 MB, 30 days.
func DefaultConfig() Config {
	return Config{Enabled: true, Formats: []Format{FormatLog, FormatCast}, MaxSizeMB: 100, MaxAge: 720 * time.Hour}
}

// ConfigFromEnv reads the EXPECT_RECORD_* variables of the process over the
// defaults.
func ConfigFromEnv() (Config, error) { return ParseConfig(nil) }

// ParseConfig reads EXPECT_RECORD_* variables from environ over the
// defaults; nil environ means the process environment.
func ParseConfig(environ map[string]string) (Config, error) {
	var c Config
	opts := env.Options{
		Environment: environ,
		FuncMap:     map[reflect.Type]env.ParserFunc{reflect.TypeFor[bool](): parseSwitch},
	}
	if err := env.ParseWithOptions(&c, opts); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.validate()
}

// String names a format.
func (f Format) String() string { return string(f) }

// Valid reports whether f is a known format.
func (f Format) Valid() bool {
	switch f {
	case FormatLog, FormatCast, FormatJSONL:
		return true
	}
	return false
}

// MarshalText encodes a known format.
func (f Format) MarshalText() ([]byte, error) {
	if !f.Valid() {
		return nil, fmt.Errorf("%w: format %q", ErrConfig, string(f))
	}
	return []byte(f), nil
}

// UnmarshalText decodes a format name, case-insensitively.
func (f *Format) UnmarshalText(b []byte) error {
	v := Format(strings.ToLower(strings.TrimSpace(string(b))))
	if !v.Valid() {
		return fmt.Errorf("%w: format %q", ErrConfig, string(b))
	}
	*f = v
	return nil
}

func (c Config) validate() error {
	if c.MaxSizeMB < 0 || c.MaxAge < 0 {
		return fmt.Errorf("%w: negative size or age", ErrConfig)
	}
	for _, f := range c.Formats {
		if !f.Valid() {
			return fmt.Errorf("%w: format %q", ErrConfig, string(f))
		}
	}
	return nil
}

// withDefaults fills the zero fields that have defaults.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if len(c.Formats) == 0 {
		c.Formats = d.Formats
	}
	c.Formats = slices.Compact(slices.Sorted(slices.Values(c.Formats)))
	if c.MaxSizeMB == 0 {
		c.MaxSizeMB = d.MaxSizeMB
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// root resolves the transcript root directory.
func (c Config) root() (string, error) {
	dir := c.Dir
	if dir != "" && dir != "~" && !strings.HasPrefix(dir, "~/") {
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("record: home directory: %w", err)
	}
	if dir == "" {
		return filepath.Join(home, DefaultDir), nil
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/")), nil
}

func parseSwitch(v string) (any, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "":
		return false, nil
	}
	return nil, fmt.Errorf("%w: switch %q", ErrConfig, v)
}
