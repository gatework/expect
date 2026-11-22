package expect

import "context"

// Response is a side effect run when a case matches, adapted to a Handler
// by Then.
type Response func(ctx context.Context, s *Session, r Result) error

// Then runs response and continues with a fresh relative timer, like
// Tcl's exp_continue; an error ends the wait. Each answered question thus
// gets the full timeout for what follows it.
func Then(response Response) Handler { return then(response, Continue) }

// ThenKeepTimer runs response and continues without restarting the
// timer, like exp_continue -continue_timer, so the timeout bounds the
// whole dialogue however many questions are answered.
func ThenKeepTimer(response Response) Handler { return then(response, ContinueKeepTimer) }

func then(response Response, act Action) Handler {
	return func(ctx context.Context, s *Session, r Result) (Action, error) {
		if err := response(ctx, s, r); err != nil {
			return Return, err
		}
		return act, nil
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

// ReplyThenContinue sends text verbatim and continues like Then.
func ReplyThenContinue(text string) Handler { return Then(Reply(text)) }

// ReplyLineThenContinue sends text with the line ending and continues
// like Then: the usual answer to a confirmation such as "[Y/N]:".
func ReplyLineThenContinue(text string) Handler { return Then(ReplyLine(text)) }
