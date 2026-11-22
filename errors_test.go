package expect_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gatework/expect"
)

// TestErrors_NoPayload checks ERR-5: errors and formatted values carry
// metadata only, never device output, patterns or causes' text.
func TestErrors_NoPayload(t *testing.T) {
	t.Parallel()
	const secret = "s3cret-payload"
	cause := errors.New(secret)
	_, compileErr := expect.Compile(`(` + secret)
	values := []any{
		&expect.WriteError{Written: 3, Cause: cause},
		&expect.RouteError{Route: 1, Target: 2, Written: 3, Cause: cause},
		&expect.CaseLimitError{Name: "confirm", CaseIndex: 0, RuleIndex: -1},
		&expect.CallbackError{Kind: expect.CallbackPanicked},
		&expect.ConfigError{Option: "secret", Reason: "empty", Cause: cause},
		compileErr,
		expect.Literal(secret), expect.Glob(secret), expect.Line(secret),
		expect.All(expect.Literal(secret), expect.Literal("x")), expect.MustCompile(secret),
		expect.Case{Name: "confirm", Pattern: expect.Literal(secret)},
		expect.Result{Before: secret, Match: secret, Output: secret},
		expect.Capture{Name: "host", Text: secret},
	}
	for _, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, v); strings.Contains(got, secret) {
				t.Errorf("%T %s reveals payload: %s", v, verb, got)
			}
		}
	}
	for _, err := range []error{&expect.WriteError{Cause: cause}, &expect.RouteError{Cause: cause}, &expect.ConfigError{Cause: cause}} {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap its cause", err)
		}
	}
	if !errors.Is(compileErr, expect.ErrInvalidPattern) {
		t.Errorf("compile error %v is not ErrInvalidPattern", compileErr)
	}
}
