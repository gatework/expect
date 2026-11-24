// Package vt reduces the output of a terminal-oriented peer to the text a
// line-oriented reader needs, so patterns written against plain text match
// what network devices print.
//
// A Filter strips control sequences: CSI (ESC [ ... final), OSC, DCS, SOS,
// PM and APC strings (terminated by BEL or ST), and other ESC sequences.
// It approximates the cursor movement devices use to erase a pager prompt
// or redraw a scrolled command line: backspace and CSI n D (cursor left)
// erase the characters before them on the current line. Carriage returns,
// line feeds and tabs pass through.
//
// Bytes arrive in chunks; a sequence split across chunks is removed as if
// it had arrived whole. Erasure reaches back into the buffer the caller
// passes, never past a line feed, so it covers text not yet consumed.
//
// A Filter is not safe for concurrent use; its owner serializes calls.
package vt

import "unicode/utf8"

const (
	esc = 0x1b
	bel = 0x07
	bs  = 0x08
)

type state uint8

const (
	ground   state = iota
	escape         // after ESC
	escInter       // ESC followed by intermediate bytes
	csi            // ESC [ parameters
	str            // OSC/DCS/SOS/PM/APC body
	strEsc         // ESC inside a string, maybe ST
)

// Filter is a streaming control-sequence filter. The zero value is ready.
type Filter struct {
	st    state
	param int  // CSI numeric parameter, saturating
	plain bool // the CSI parameters are digits only
}

// Append filters p and appends the result to buf, erasing characters of
// buf as backspaces and cursor-left sequences require. It reports whether
// bytes already in buf were erased, so incremental searches over buf must
// restart.
func (f *Filter) Append(buf, p []byte) ([]byte, bool) {
	out := sink{buf: buf, floor: len(buf)}
	for _, c := range p {
		switch f.st {
		case ground:
			f.ground(&out, c)
		case escape:
			f.escape(c)
		case escInter:
			if c < 0x20 || c > 0x2f {
				f.st = ground
			}
		case csi:
			f.csi(&out, c)
		case str, strEsc:
			f.str(c)
		}
	}
	return out.buf, out.rewrote
}

// Reset forgets a sequence in progress.
func (f *Filter) Reset() { *f = Filter{} }

func (f *Filter) ground(out *sink, c byte) {
	switch c {
	case esc:
		f.st = escape
	case bs:
		out.erase(1)
	default:
		out.buf = append(out.buf, c)
	}
}

func (f *Filter) escape(c byte) {
	switch {
	case c == '[':
		f.st, f.param, f.plain = csi, 0, true
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		f.st = str
	case c >= 0x20 && c <= 0x2f:
		f.st = escInter
	case c == esc:
		// ESC ESC: start over.
	default:
		f.st = ground // two-byte sequence, final byte dropped
	}
}

func (f *Filter) csi(out *sink, c byte) {
	switch {
	case c >= '0' && c <= '9':
		if f.param < maxCount {
			f.param = f.param*10 + int(c-'0')
		}
	case c >= 0x20 && c <= 0x3f:
		f.plain = false // ';', '?', intermediates: not a plain count
	case c >= 0x40 && c <= 0x7e:
		if c == 'D' && f.plain {
			out.erase(min(max(f.param, 1), maxCount))
		}
		f.st = ground
	default:
		f.st = ground // malformed: drop the sequence
	}
}

// str skips a string sequence up to BEL or ST (ESC \).
func (f *Filter) str(c byte) {
	switch {
	case f.st == strEsc && c == '\\':
		f.st = ground
	case c == bel:
		f.st = ground
	case c == esc:
		f.st = strEsc
	default:
		f.st = str
	}
}

// maxCount bounds a cursor-left count.
const maxCount = 1e4

// sink is the buffer one Append writes to.
type sink struct {
	buf     []byte
	floor   int // len(buf) before Append, lowered by erasure
	rewrote bool
}

// erase removes up to n runes before the cursor on the current line.
func (s *sink) erase(n int) {
	for ; n > 0 && len(s.buf) > 0 && s.buf[len(s.buf)-1] != '\n'; n-- {
		_, size := utf8.DecodeLastRune(s.buf)
		s.buf = s.buf[:len(s.buf)-size]
		if len(s.buf) < s.floor {
			s.floor, s.rewrote = len(s.buf), true
		}
	}
}
