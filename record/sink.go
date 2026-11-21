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
	open bool // a line of dir is in progress
	cr   bool // a trailing CR was withheld
}

func (s *textSink) write(d Direction, p []byte, at time.Time) error {
	var b bytes.Buffer
	if s.open && d != s.dir {
		b.WriteByte('\n')
		s.open, s.cr = false, false
	}
	s.dir = d
	stamp := at.Format(stampLayout)
	if s.cr {
		s.cr = false
		if p[0] == '\n' {
			p = p[1:]
			b.WriteByte('\n')
			s.open = false
		} else {
			b.WriteByte('\r')
		}
	}
	for len(p) > 0 {
		if !s.open {
			b.WriteString(stamp)
			b.WriteString(marker(d))
			s.open = true
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if bytes.HasSuffix(p, []byte{'\r'}) {
				p, s.cr = p[:len(p)-1], true
			}
			b.Write(p)
			break
		}
		b.Write(bytes.TrimSuffix(p[:i], []byte{'\r'}))
		b.WriteByte('\n')
		s.open, p = false, p[i+1:]
	}
	_, err := s.w.Write(b.Bytes())
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
	data := slices.Concat(c.pending[d], p)
	n := completeUTF8(data)
	c.pending[d] = append([]byte(nil), data[n:]...)
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
	text, err := json.Marshal(string(data))
	if err != nil {
		return err
	}
	line := make([]byte, 0, len(text)+24)
	line = append(line, '[')
	line = strconv.AppendFloat(line, at.Sub(c.start).Seconds(), 'f', 6, 64)
	line = append(line, `, "`...)
	line = append(line, code...)
	line = append(line, `", `...)
	line = append(line, text...)
	line = append(line, "]\n"...)
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
