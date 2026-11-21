package expect

import "context"

// Response is a side effect run when a case matches, adapted to a Handler
// by Then.
type Response func(ctx context.Context, s *Session, r Result) error

// Then runs response and continues without restarting the timer; an error
// ends the wait.
func Then(response Response) Handler {
	return func(ctx context.Context, s *Session, r Result) (Action, error) {
		if err := response(ctx, s, r); err != nil {
			return Return, err
		}
		return ContinueKeepTimer, nil
	}
}

// Reply sends text verbatim; add the line ending yourself or use
// ReplyLine.
func Reply(text string) Response {
	return func(ctx context.Context, s *Session, _ Result) error {
		_, err := s.Send(ctx, text)
		return err
	}
}

// ReplyLine sends text followed by the session line ending.
func ReplyLine(text string) Response {
	return func(ctx context.Context, s *Session, _ Result) error {
		_, err := s.SendLine(ctx, text)
		return err
	}
}

// ReplyThenContinue sends text verbatim and keeps waiting.
func ReplyThenContinue(text string) Handler { return Then(Reply(text)) }
