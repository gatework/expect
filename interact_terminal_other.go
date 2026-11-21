//go:build !unix

package expect

import (
	"context"
	"errors"
	"io"
	"os"
)

// InteractTerminal is Interact on an operator terminal in raw mode. It is
// supported on Unix only; elsewhere use Interact with explicit streams.
func (s *Session) InteractTerminal(context.Context, *os.File, io.Writer, ...InteractOption) error {
	return errors.ErrUnsupported
}
