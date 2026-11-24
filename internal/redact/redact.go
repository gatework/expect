// Package redact masks secrets in a byte stream that arrives in chunks.
//
// A Filter holds back only the bytes that could still begin a secret, so a
// secret split across chunks is masked exactly as if it had arrived whole.
// Line rules mask the submatches of regular expressions (for example the
// cipher text after "password cipher") one complete line at a time.
//
// A Filter is not safe for concurrent use; its owner serializes calls.
package redact

import (
	"bytes"
	"cmp"
	"errors"
	"regexp"
	"slices"
)

// Mask replaces redacted text.
const Mask = "******"

// maxLine bounds how long a line rule waits for a line break before it
// releases the line unmatched.
const maxLine = 64 << 10

// ErrEmpty rejects an empty secret.
var ErrEmpty = errors.New("redact: empty secret")

// Filter is a streaming redaction filter. The zero value passes bytes
// through unchanged.
type Filter struct {
	secrets [][]byte
	rules   []*regexp.Regexp
	mask    []byte
	held    []byte // unreleased bytes: a possible secret prefix or an incomplete line
}

// New returns a filter that writes replacement (Mask when empty) in place
// of each secret.
func New(replacement string) *Filter {
	if replacement == "" {
		replacement = Mask
	}
	return &Filter{mask: []byte(replacement)}
}

// AddSecrets registers literal secrets for the rest of the stream.
func (f *Filter) AddSecrets(secrets ...string) error {
	if slices.Contains(secrets, "") {
		return ErrEmpty
	}
	for _, s := range secrets {
		if !slices.ContainsFunc(f.secrets, func(b []byte) bool { return string(b) == s }) {
			f.secrets = append(f.secrets, []byte(s))
		}
	}
	return nil
}

// AddRule registers a line rule: in every line re matches, each matched
// submatch is masked, or the whole match when re has no groups.
func (f *Filter) AddRule(re *regexp.Regexp) { f.rules = append(f.rules, re) }

// Clone returns a filter with the same secrets and rules for a new
// stream; held bytes are not copied.
func (f *Filter) Clone() *Filter {
	return &Filter{secrets: slices.Clone(f.secrets), rules: slices.Clone(f.rules), mask: f.mask}
}

// Sync adopts the secrets and rules of src for the rest of the stream,
// keeping the bytes f holds, which the new secrets then also cover.
func (f *Filter) Sync(src *Filter) {
	f.secrets, f.rules, f.mask = slices.Clone(src.secrets), slices.Clone(src.rules), src.mask
}

// MaxSecret returns the length of the longest secret, or zero.
func (f *Filter) MaxSecret() int {
	n := 0
	for _, s := range f.secrets {
		n = max(n, len(s))
	}
	return n
}

// Active reports whether the filter can change any byte.
func (f *Filter) Active() bool { return len(f.secrets) > 0 || len(f.rules) > 0 }

// Append filters data and returns the bytes that are now safe to release.
func (f *Filter) Append(data []byte) []byte {
	if !f.Active() {
		return data
	}
	f.held = append(f.held, data...)
	cut := f.safeCut()
	out := f.render(f.held[:cut])
	f.held = append(f.held[:0], f.held[cut:]...)
	return out
}

// Finish ends the stream: retained bytes are released, and a retained
// secret prefix is masked conservatively.
func (f *Filter) Finish() []byte {
	if len(f.held) == 0 {
		return nil
	}
	k := prefixLen(f.held, f.secrets)
	out := f.render(f.held[:len(f.held)-k])
	if k > 0 {
		out = append(out, f.mask...)
	}
	f.held = f.held[:0]
	return out
}

// safeCut returns how many held bytes no future byte can change.
func (f *Filter) safeCut() int {
	cut := len(f.held) - prefixLen(f.held, f.secrets)
	if len(f.rules) > 0 {
		line := bytes.LastIndexByte(f.held, '\n') + 1
		if len(f.held)-line < maxLine {
			cut = min(cut, line)
		}
	}
	// An occurrence must not straddle the cut, or its head would be released
	// unmasked; one ending at the cut stays held so that an adjacent future
	// occurrence merges into the same mask.
	spans := f.spans(f.held)
	for _, s := range slices.Backward(spans) {
		if s.start < cut && cut <= s.end {
			cut = s.start
		}
	}
	return cut
}

// render masks the secrets and line rules in a released segment.
func (f *Filter) render(b []byte) []byte {
	spans := f.spans(b)
	if len(spans) == 0 {
		return slices.Clone(b)
	}
	out := make([]byte, 0, len(b))
	at := 0
	for _, s := range spans {
		out = append(out, b[at:s.start]...)
		out = append(out, f.mask...)
		at = s.end
	}
	return append(out, b[at:]...)
}

type span struct{ start, end int }

// spans returns the merged union of every secret occurrence and rule
// submatch in b; overlapping or adjacent spans are masked once.
func (f *Filter) spans(b []byte) []span {
	var all []span
	for _, s := range f.secrets {
		for i := 0; ; {
			j := bytes.Index(b[i:], s)
			if j < 0 {
				break
			}
			all = append(all, span{i + j, i + j + len(s)})
			i += j + 1
		}
	}
	for _, re := range f.rules {
		all = append(all, ruleSpans(re, b)...)
	}
	if len(all) == 0 {
		return nil
	}
	slices.SortFunc(all, func(a, b span) int { return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end)) })
	merged := all[:1]
	for _, s := range all[1:] {
		last := &merged[len(merged)-1]
		if s.start <= last.end {
			last.end = max(last.end, s.end)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

func ruleSpans(re *regexp.Regexp, b []byte) []span {
	var out []span
	for _, loc := range re.FindAllSubmatchIndex(b, -1) {
		if len(loc) == 2 {
			if loc[1] > loc[0] {
				out = append(out, span{loc[0], loc[1]})
			}
			continue
		}
		for g := 2; g+1 < len(loc); g += 2 {
			if loc[g] >= 0 && loc[g+1] > loc[g] {
				out = append(out, span{loc[g], loc[g+1]})
			}
		}
	}
	return out
}

// prefixLen returns the length of the longest suffix of b that is a proper
// prefix of some secret.
func prefixLen(b []byte, secrets [][]byte) int {
	best := 0
	for _, s := range secrets {
		for k := min(len(s)-1, len(b)); k > best; k-- {
			if bytes.Equal(b[len(b)-k:], s[:k]) {
				best = k
				break
			}
		}
	}
	return best
}
