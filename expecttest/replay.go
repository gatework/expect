package expecttest

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gatework/expect"
	"github.com/gatework/expect/record"
)

// ReplayOption configures Replay.
type ReplayOption func(*replayOptions)

type replayOptions struct {
	speed   float64
	session []expect.SessionOption
}

// WithReplaySpeed plays the recorded pauses scaled by speed: 1 is real
// time, 0.5 half speed, 0 (the default) no pauses at all.
func WithReplaySpeed(speed float64) ReplayOption {
	return func(o *replayOptions) { o.speed = max(speed, 0) }
}

// WithReplaySessionOptions configures the Session Replay returns.
func WithReplaySessionOptions(opts ...expect.SessionOption) ReplayOption {
	return func(o *replayOptions) { o.session = append(o.session, opts...) }
}

// Replay plays a recorded cast transcript as the peer, so a session
// captured on a real device reproduces offline: the device's output is
// said again and what the code under test sends is checked against what
// was sent then, with recorded secrets matching any text (HearMasked).
// The session and the peer behave as with Pipe.
func Replay(tb testing.TB, path string, opts ...ReplayOption) (*expect.Session, *Peer) {
	tb.Helper()
	var o replayOptions
	for _, opt := range opts {
		opt(&o)
	}
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	c, err := record.ReadCast(f)
	if err != nil {
		tb.Fatal(err)
	}
	return Pipe(tb, ReplaySteps(c, o.speed), o.session...)
}

// ReplaySteps converts a cast into script steps: output frames become
// Say, consecutive input frames one Hear (HearMasked when they hold a
// secret), and with a positive speed the time between frames a Pause.
func ReplaySteps(c *record.Cast, speed float64) []Step {
	var steps []Step
	var sent strings.Builder
	var last time.Duration
	flush := func() {
		if sent.Len() == 0 {
			return
		}
		if text := sent.String(); strings.Contains(text, expect.Mask) {
			steps = append(steps, HearMasked(text))
		} else {
			steps = append(steps, Hear(text))
		}
		sent.Reset()
	}
	for _, fr := range c.Frames {
		switch fr.Kind {
		case record.FrameInput:
			sent.WriteString(fr.Data)
			last = fr.At
		case record.FrameOutput:
			flush()
			if d := time.Duration(float64(fr.At-last) * speed); speed > 0 && d > 0 {
				steps = append(steps, Pause(d))
			}
			steps = append(steps, Say(fr.Data))
			last = fr.At
		case record.FrameResize, record.FrameMarker:
			// Annotations do not move the timeline.
		}
	}
	flush()
	return steps
}
