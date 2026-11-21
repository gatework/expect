// Command dialogue logs in to a simulated router, answers a pager and a
// confirmation, and prints where the transcript was recorded. Run it with
// EXPECT_RECORD=off to skip recording, or EXPECT_RECORD_DIR=/tmp/x to
// choose the directory.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gatework/expect"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dialogue:", err)
		os.Exit(1)
	}
}

func run() error {
	local, remote := net.Pipe()
	go router(remote)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	s, err := expect.NewSession(local, expect.WithLineEnding("\r"), expect.WithLabel("lab-router"), expect.WithLogger(logger))
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Redact("s3cret"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prompt := expect.Case{Name: "prompt", Pattern: expect.MustCompile(`(?m)^[\w.-]+#$`)}
	if _, err := s.Expect(ctx, []expect.Case{
		{Name: "username", Pattern: expect.Literal("Username:"), Handler: expect.Then(expect.ReplyLine("ops"))},
		{Name: "password", Pattern: expect.Literal("Password:"), Handler: expect.Then(expect.ReplyLine("s3cret"))},
		prompt,
	}); err != nil {
		return err
	}
	var out strings.Builder
	if _, err := s.SendLine(ctx, "write memory"); err != nil {
		return err
	}
	r, err := s.Expect(ctx, []expect.Case{
		{Name: "confirm", Pattern: expect.Literal("[confirm]"), Handler: func(ctx context.Context, s *expect.Session, r expect.Result) (expect.Action, error) {
			out.WriteString(r.Before)
			_, err := s.SendLine(ctx, "")
			return expect.ContinueKeepTimer, err
		}},
		prompt,
	})
	if err != nil {
		return err
	}
	out.WriteString(r.Before)
	fmt.Printf("device said: %q\n", out.String())
	paths := s.Transcript()
	if err := s.Close(); err != nil {
		return err
	}
	for _, p := range paths {
		fmt.Println("transcript:", p)
	}
	return nil
}

// router plays a device: login, then "write memory" with a confirmation.
func router(conn net.Conn) {
	defer conn.Close()
	read := func(n int) { _, _ = io.ReadFull(conn, make([]byte, n)) }
	_, _ = io.WriteString(conn, "\r\nUser Access Verification\r\n\r\nUsername: ")
	read(len("ops\r"))
	_, _ = io.WriteString(conn, "Password: ")
	read(len("s3cret\r"))
	_, _ = io.WriteString(conn, "\r\nlab-router#")
	read(len("write memory\r"))
	_, _ = io.WriteString(conn, "write memory\r\nDestination filename [startup-config]? [confirm]")
	read(1)
	_, _ = io.WriteString(conn, "\r\nBuilding configuration...\r\n[OK]\r\nlab-router#")
}
