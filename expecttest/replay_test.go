package expecttest_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gatework/expect"
	"github.com/gatework/expect/expecttest"
	"github.com/gatework/expect/record"
)

// automate is the code under test: log in, then read a paged command.
func automate(ctx context.Context, s *expect.Session) (string, error) {
	if err := s.Redact("s3cret"); err != nil {
		return "", err
	}
	prompt := expect.Case{Name: "prompt", Pattern: expect.Literal("r1#"), Omit: true}
	if _, err := s.Expect(ctx, []expect.Case{
		{Name: "user", Pattern: expect.Literal("Username:"), Handler: expect.ReplyLineThenContinue("admin")},
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.ReplyLineThenContinue("s3cret")},
		prompt,
	}); err != nil {
		return "", err
	}
	r, err := s.Exchange(ctx, "show clock", []expect.Case{
		{Name: "more", Pattern: expect.Literal(" --More-- "), Omit: true, Handler: expect.ReplyThenContinue(" ")},
		prompt,
	})
	return r.Output, err
}

// TestReplay records automate against a scripted device, then runs it
// again against the recording alone.
func TestReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := record.DefaultConfig()
	c.Dir = dir
	rec, err := record.New(c)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := expecttest.Pipe(t, []expecttest.Step{
		expecttest.Say("Username: "), expecttest.Hear("admin\n"),
		expecttest.Say("Password: "), expecttest.Hear("s3cret\n"),
		expecttest.Say("\r\nr1#"), expecttest.Hear("show clock\n"),
		expecttest.Say("show clock\r\n10:00 --More-- "), expecttest.Hear(" "),
		expecttest.Say("CST\r\nr1#"),
	}, expect.WithRecorder(rec))
	want, err := automate(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	var cast string
	for _, p := range s.Transcript() {
		if strings.HasSuffix(p, ".cast") {
			cast = p
		}
	}
	_ = s.Close()
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*.cast"))
	if cast == "" || len(matches) != 1 {
		t.Fatalf("cast %q, files %v", cast, matches)
	}

	replayed, peer := expecttest.Replay(t, cast)
	got, err := automate(t.Context(), replayed)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || got != "show clock\r\n10:00CST\r\n" {
		t.Fatalf("replayed %q, recorded %q", got, want)
	}
	_ = replayed.Close()
	if !peer.Done() {
		t.Fatal("replay did not finish")
	}
}

func TestReplaySteps(t *testing.T) {
	t.Parallel()
	c := &record.Cast{Frames: []record.Frame{
		{At: 0, Kind: record.FrameOutput, Data: "Password: "},
		{At: 1e9, Kind: record.FrameInput, Data: "******"},
		{At: 1e9, Kind: record.FrameInput, Data: "\n"},
		{At: 2e9, Kind: record.FrameMarker, Data: "note"},
		{At: 3e9, Kind: record.FrameOutput, Data: "r1#"},
	}}
	got := expecttest.ReplaySteps(c, 0.5)
	want := []expecttest.Step{
		expecttest.Say("Password: "), expecttest.HearMasked("******\n"),
		expecttest.Pause(1e9), expecttest.Say("r1#"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d steps, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}
