package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// maxCastLine bounds one line of a cast file: a 32 KiB chunk can grow
// several times when JSON-escaped.
const maxCastLine = 16 << 20

// FrameKind is the code of an asciicast v2 event.
type FrameKind string

// Frame kinds written by this package.
const (
	FrameOutput FrameKind = "o" // received from the peer
	FrameInput  FrameKind = "i" // sent to the peer
	FrameResize FrameKind = "r" // window size, "COLSxROWS"
	FrameMarker FrameKind = "m" // annotation, such as truncation
)

// Frame is one event of a cast transcript.
type Frame struct {
	At   time.Duration // since the start of the recording
	Kind FrameKind
	Data string
}

// Cast is a parsed asciicast v2 transcript.
type Cast struct {
	Width, Height int
	Title         string
	Started       time.Time
	Frames        []Frame
}

// ReadCast parses an asciicast v2 transcript, such as the cast files this
// package records. Secrets appear as they were recorded: redacted.
func ReadCast(r io.Reader) (*Cast, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxCastLine)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("record: cast header: %w", err)
		}
		return nil, fmt.Errorf("record: cast header: %w", io.ErrUnexpectedEOF)
	}
	var h castHeader
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return nil, fmt.Errorf("record: cast header: %w", err)
	}
	if h.Version != 2 {
		return nil, fmt.Errorf("record: cast version %d, want 2", h.Version)
	}
	c := &Cast{Width: h.Width, Height: h.Height, Title: h.Title, Started: time.Unix(h.Timestamp, 0)}
	for line := 2; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		f, err := parseFrame(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("record: cast line %d: %w", line, err)
		}
		c.Frames = append(c.Frames, f)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("record: cast: %w", err)
	}
	return c, nil
}

func parseFrame(b []byte) (Frame, error) {
	var ev []json.RawMessage
	if err := json.Unmarshal(b, &ev); err != nil {
		return Frame{}, err
	}
	if len(ev) != 3 {
		return Frame{}, fmt.Errorf("event has %d fields, want 3", len(ev))
	}
	secs, err := strconv.ParseFloat(string(ev[0]), 64)
	if err != nil {
		return Frame{}, fmt.Errorf("time: %w", err)
	}
	var f Frame
	if err := json.Unmarshal(ev[1], &f.Kind); err != nil {
		return Frame{}, fmt.Errorf("code: %w", err)
	}
	if err := json.Unmarshal(ev[2], &f.Data); err != nil {
		return Frame{}, fmt.Errorf("data: %w", err)
	}
	f.At = time.Duration(secs * float64(time.Second))
	return f, nil
}
