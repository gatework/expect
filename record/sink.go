package record

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

const stampLayout = "2006-01-02T15:04:05.000Z07:00"

// textSink writes the log format: each line starts with a timestamp and a
// direction marker; CR LF becomes LF.
type textSink struct {
	w    io.Writer
	dir  Direction
	open bool   // a line of dir is in progress
	cr   bool   // a trailing CR was withheld
	buf  []byte // reused for each write
}

func (s *textSink) write(d Direction, p []byte, at time.Time) error {
	b := s.buf[:0]
	if s.open && d != s.dir {
		b = append(b, '\n')
		s.open, s.cr = false, false
	}
	s.dir = d
	var stamp [len(stampLayout) + 8]byte
	head := append(at.AppendFormat(stamp[:0], stampLayout), marker(d)...)
	if s.cr {
		s.cr = false
		if p[0] == '\n' {
			p = p[1:]
			b = append(b, '\n')
			s.open = false
		} else {
			b = append(b, '\r')
		}
	}
	for len(p) > 0 {
		if !s.open {
			b = append(b, head...)
			s.open = true
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if p[len(p)-1] == '\r' {
				p, s.cr = p[:len(p)-1], true
			}
			b = append(b, p...)
			break
		}
		b = append(b, bytes.TrimSuffix(p[:i], []byte{'\r'})...)
		b = append(b, '\n')
		s.open, p = false, p[i+1:]
	}
	s.buf = b
	if len(b) == 0 {
		return nil
	}
	_, err := s.w.Write(b)
	return err
}

func marker(d Direction) string {
	if d == DirectionSent {
		return " >> "
	}
	return " << "
}

// castSink writes asciicast v2: a header line, then one JSON array per
// event. Incomplete UTF-8 at a chunk end is held for the next chunk.
type castSink struct {
	w       io.Writer
	start   time.Time
	limit   int64
	size    int64
	stopped bool
	pending [3][]byte // per Direction
	line    []byte    // reused for each event
}

type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env"`
}

func newCastSink(w io.Writer, m Meta, start time.Time, limit int64) (*castSink, error) {
	h := castHeader{Version: 2, Width: m.Width, Height: m.Height, Timestamp: start.Unix(), Title: m.Label, Env: map[string]string{"TERM": "xterm"}}
	if h.Width <= 0 {
		h.Width = 80
	}
	if h.Height <= 0 {
		h.Height = 24
	}
	line, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	c := &castSink{w: w, start: start, limit: limit}
	return c, c.emit(append(line, '\n'))
}

func (c *castSink) write(d Direction, p []byte, at time.Time) error {
	if c.stopped {
		return nil
	}
	data := p
	if len(c.pending[d]) > 0 {
		data = slices.Concat(c.pending[d], p)
	}
	n := completeUTF8(data)
	c.pending[d] = append(c.pending[d][:0], data[n:]...)
	if n == 0 {
		return nil
	}
	code := "o"
	if d == DirectionSent {
		code = "i"
	}
	return c.event(at, code, data[:n])
}

func (c *castSink) event(at time.Time, code string, data []byte) error {
	line := c.line[:0]
	line = append(line, '[')
	line = strconv.AppendFloat(line, at.Sub(c.start).Seconds(), 'f', 6, 64)
	line = append(line, `, "`...)
	line = append(line, code...)
	line = append(line, `", `...)
	line = appendJSONString(line, data)
	line = append(line, "]\n"...)
	c.line = line
	if c.limit > 0 && c.size+int64(len(line)) > c.limit {
		c.stopped = true
		marker := []byte("[" + strconv.FormatFloat(at.Sub(c.start).Seconds(), 'f', 6, 64) + `, "m", "recording truncated: size limit"]` + "\n")
		return c.emit(marker)
	}
	return c.emit(line)
}

func (c *castSink) emit(line []byte) error {
	n, err := c.w.Write(line)
	c.size += int64(n)
	return err
}

// finish writes any held bytes as they are.
func (c *castSink) finish(at time.Time) error {
	for d, p := range c.pending {
		if len(p) > 0 && !c.stopped {
			code := "o"
			if Direction(d) == DirectionSent {
				code = "i"
			}
			if err := c.event(at, code, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// appendJSONString appends data as a JSON string, as json.Marshal of
// string(data) would decode: invalid UTF-8 becomes U+FFFD.
func appendJSONString(dst, data []byte) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(data); {
		c := data[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				dst = append(dst, '\\', c)
			case c == '\n':
				dst = append(dst, '\\', 'n')
			case c == '\r':
				dst = append(dst, '\\', 'r')
			case c == '\t':
				dst = append(dst, '\\', 't')
			case c < 0x20:
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			default:
				dst = append(dst, c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(dst, `\ufffd`...)
		case r == '\u2028' || r == '\u2029':
			dst = append(dst, `\u202`...)
			dst = append(dst, hex[r&0xf])
		default:
			dst = append(dst, data[i:i+size]...)
		}
		i += size
	}
	return append(dst, '"')
}

// completeUTF8 returns the length of b without a trailing incomplete
// UTF-8 sequence.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && len(b)-i <= utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
