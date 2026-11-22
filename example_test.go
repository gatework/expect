package expect_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/gatework/expect"
)

// device plays a router that asks for a password and pages its output.
func device(conn net.Conn) {
	defer conn.Close()
	_, _ = io.WriteString(conn, "Password: ")
	buf := make([]byte, 12)
	_, _ = io.ReadFull(conn, buf[:7]) // "s3cret\r"
	_, _ = io.WriteString(conn, "\r\nrouter#")
	_, _ = io.ReadFull(conn, buf[:12]) // "show clock \r"
	_, _ = io.WriteString(conn, "show clock\r\n10:00:00 CST\r\n --More-- ")
	_, _ = io.ReadFull(conn, buf[:1]) // " "
	_, _ = io.WriteString(conn, "\r\nFri Nov 20 2026\r\nrouter#")
}

func ExampleSession_Expect() {
	local, remote := net.Pipe()
	go device(remote)

	// A match consumes the output before it, so the pager rule keeps the
	// page it interrupts before answering it.
	var output strings.Builder
	pager := expect.Case{Name: "more", Pattern: expect.Literal(" --More-- "), Limit: expect.Unlimited,
		Handler: func(ctx context.Context, s *expect.Session, r expect.Result) (expect.Action, error) {
			output.WriteString(r.Before)
			_, err := s.Send(ctx, " ")
			return expect.ContinueKeepTimer, err
		}}
	s, err := expect.NewSession(local,
		expect.WithLineEnding("\r"),
		expect.WithLabel("router"),
		expect.WithoutRecording(), // examples keep ~/.local/share/gatework/expect clean
		expect.WithBefore(pager),
	)
	if err != nil {
		panic(err)
	}
	defer s.Close()
	if err := s.Redact("s3cret"); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prompt := expect.MustCompile(`(?m)^(?P<host>[\w.-]+)#$`)
	r, err := s.Expect(ctx, []expect.Case{
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine("s3cret"))},
		{Name: "prompt", Pattern: prompt},
	})
	if err != nil {
		panic(err)
	}
	host, _ := r.Capture("host")
	fmt.Println("logged in to", host.Text)

	if _, err := s.SendLine(ctx, "show clock "); err != nil {
		panic(err)
	}
	r, err = s.Expect(ctx, []expect.Case{{Name: "prompt", Pattern: prompt}})
	if err != nil {
		panic(err)
	}
	output.WriteString(r.Before)
	fmt.Printf("%q\n", output.String())
	// Output:
	// logged in to router
	// "show clock\r\n10:00:00 CST\r\n\r\nFri Nov 20 2026\r\n"
}

// ExampleSession_Exchange resets interfaces one at a time before
// reconfiguring them. A session rule answers the confirmation each reset
// asks for, and the Exchange keeps waiting for the prompt (exp_continue).
func ExampleSession_Exchange() {
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		buf := make([]byte, 64)
		for range 2 {
			_, _ = remote.Read(buf) // clear configuration interface ...
			_, _ = io.WriteString(remote, "Continue? [Y/N]:")
			_, _ = remote.Read(buf) // Y
			_, _ = io.WriteString(remote, "Y\r\nInfo: Succeeded.\r\n[r1]")
		}
	}()
	s, err := expect.NewSession(local, expect.WithLineEnding("\r"), expect.WithoutRecording(),
		expect.WithBefore(expect.Case{Name: "confirm", Pattern: expect.Literal("[Y/N]:"), Handler: expect.ReplyLineThenContinue("Y")}))
	if err != nil {
		panic(err)
	}
	defer s.Close()
	prompt := []expect.Case{{Name: "prompt", Pattern: expect.Literal("[r1]")}}
	for _, ifc := range []string{"GE1/0/1", "GE1/0/2"} {
		r, err := s.Exchange(context.Background(), "clear configuration interface "+ifc, prompt, expect.WithTimeout(30*time.Second))
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s: %q\n", ifc, r.Output)
	}
	// Output:
	// GE1/0/1: "Continue? [Y/N]:Y\r\nInfo: Succeeded.\r\n[r1]"
	// GE1/0/2: "Continue? [Y/N]:Y\r\nInfo: Succeeded.\r\n[r1]"
}

func ExampleSession_StartBackground() {
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		_, _ = io.WriteString(remote, "%LINK-3-UPDOWN: Interface Gi0/1, changed state to down\n")
		_, _ = io.WriteString(remote, "router#")
	}()
	s, err := expect.NewSession(local, expect.WithoutRecording())
	if err != nil {
		panic(err)
	}
	defer s.Close()
	b, err := s.StartBackground(context.Background(), []expect.Case{
		{Name: "syslog", Pattern: expect.MustCompile(`%\w+-\d-\w+: [^\n]*`), Limit: expect.Unlimited,
			Handler: func(_ context.Context, _ *expect.Session, r expect.Result) (expect.Action, error) {
				fmt.Println("notice:", r.Match)
				return expect.Continue, nil
			}},
		{Name: "prompt", Pattern: expect.Literal("router#")},
	})
	if err != nil {
		panic(err)
	}
	r, err := b.Wait(context.Background())
	fmt.Println(r.Name, err)
	// Output:
	// notice: %LINK-3-UPDOWN: Interface Gi0/1, changed state to down
	// prompt <nil>
}
