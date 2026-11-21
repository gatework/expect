package record

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	dayLayout  = "2006-01-02"
	dirMode    = 0o700
	maxLabel   = 64
	timeLayout = "150405"
)

// Recorder opens transcripts. It is immutable and safe for concurrent use.
// A Recorder whose Config is not Enabled opens transcripts that discard
// everything.
type Recorder struct {
	c    Config
	root string // empty when disabled
}

// Meta describes the session a transcript records.
type Meta struct {
	// Session identifies the session; it makes file names unique.
	Session string
	// Label names the peer, typically the device address or name. It is
	// reduced to [A-Za-z0-9._-] for the file name.
	Label string
	// Width and Height are the terminal size written to a cast header;
	// zero means 80 by 24.
	Width, Height int
}

// New validates c, resolves the transcript root and removes day
// directories older than MaxAge. When the home directory cannot be
// resolved, recording is disabled with a logged warning, unless Required.
func New(c Config) (*Recorder, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	c = c.withDefaults()
	r := &Recorder{c: c}
	if !c.Enabled {
		return r, nil
	}
	root, err := c.root()
	if err != nil {
		if c.Required {
			return nil, err
		}
		c.Logger.Warn("record: recording disabled", "err", err)
		return r, nil
	}
	r.root = root
	r.prune()
	return r, nil
}

// Disabled returns a Recorder that records nothing.
func Disabled() *Recorder { return &Recorder{c: Config{}.withDefaults()} }

// Enabled reports whether transcripts are written.
func (r *Recorder) Enabled() bool { return r.root != "" }

// Dir returns the transcript root, or "" when disabled.
func (r *Recorder) Dir() string { return r.root }

// Open creates the transcript files of one session. With Required, a file
// that cannot be created is an error; otherwise it is logged and skipped.
func (r *Recorder) Open(m Meta) (*Transcript, error) {
	t := &Transcript{required: r.c.Required, logger: r.c.Logger, now: r.c.Now}
	if !r.Enabled() {
		return t, nil
	}
	start := r.c.Now()
	dir := filepath.Join(r.root, start.Format(dayLayout))
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return t, t.fail(fmt.Errorf("record: create %s: %w", dir, err))
	}
	base := filepath.Join(dir, fmt.Sprintf("%s_%s_%s", sanitize(m.Label), start.Format(timeLayout), sanitize(m.Session)))
	for _, f := range r.c.Formats {
		if err := t.open(f, base+"."+string(f), m, start, r.c.MaxSizeMB); err != nil {
			if ferr := t.fail(err); ferr != nil {
				return t, errors.Join(ferr, t.Close())
			}
		}
	}
	return t, nil
}

// prune removes day directories that ended more than MaxAge ago. Only
// directories named like a date are considered.
func (r *Recorder) prune() {
	if r.c.MaxAge <= 0 {
		return
	}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			r.c.Logger.Warn("record: prune", "dir", r.root, "err", err)
		}
		return
	}
	now := r.c.Now()
	cutoff := now.Add(-r.c.MaxAge)
	for _, e := range entries {
		day, err := time.ParseInLocation(dayLayout, e.Name(), now.Location())
		if err != nil || !e.IsDir() || !day.AddDate(0, 0, 1).Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(r.root, e.Name())); err != nil {
			r.c.Logger.Warn("record: prune", "dir", e.Name(), "err", err)
		}
	}
}

// sanitize reduces s to a safe file name component.
func sanitize(s string) string {
	if s == "" {
		return "session"
	}
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			b[i] = '_'
		}
	}
	if out := strings.Trim(string(b[:min(len(b), maxLabel)]), "."); out != "" {
		return out
	}
	return "session"
}
