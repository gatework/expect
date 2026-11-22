package expecttest_test

import (
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/gatework/expect"
	"github.com/gatework/expect/expecttest"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestPipe(t *testing.T) {
	t.Parallel()
	// Pipe cleanup waits for the peer and checks that the script finished.
	s, _ := expecttest.Pipe(t, []expecttest.Step{
		expecttest.Say("Password: "),
		expecttest.Hear("s3cret\r"),
		expecttest.Say("\r\nrouter#"),
	}, expect.WithLineEnding("\r"))
	r, err := s.Expect(t.Context(), []expect.Case{
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine("s3cret"))},
		{Name: "prompt", Pattern: expect.Line("router#")},
	}, expect.WithTimeout(time.Second))
	if err != nil || r.Name != "prompt" {
		t.Fatalf("got %+v %v", r, err)
	}
}

func TestNewWriter(t *testing.T) {
	t.Parallel()
	w := expecttest.NewWriter(t)
	if n, err := w.Write([]byte("a\nb")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}
