package expect

import "time"

const defaultEscape = 0x1d // Control-]

// InteractOption configures Interact.
type InteractOption func(*interactOptions) error

type interactOptions struct {
	escape        byte
	noEscape      bool
	input         []Case
	output        []Case
	inputTimeout  time.Duration
	match         []MatchOption
	echo          bool
	inputForward  bool
	outputForward bool
	cooked        bool
}

func defaultInteractOptions() interactOptions {
	return interactOptions{escape: defaultEscape, outputForward: true}
}

// WithEscape sets the operator byte that ends the interaction; the default
// is Control-] (0x1d).
func WithEscape(key byte) InteractOption {
	return func(o *interactOptions) error {
		o.escape, o.noEscape = key, false
		return nil
	}
}

// WithoutEscape disables the escape byte; the interaction then ends only
// by a case, EOF or the context.
func WithoutEscape() InteractOption {
	return func(o *interactOptions) error {
		o.noEscape = true
		return nil
	}
}

// WithInteractInputCases matches operator input: text patterns, EOF (input
// closed) and Timeout (input idle, see WithInteractInputTimeout). Matched
// text is not forwarded unless WithInteractInputForward. Input that might
// begin a match is held back: a literal holds only its possible prefix, a
// regular expression holds the current line until it ends or the input is
// idle.
func WithInteractInputCases(cases ...Case) InteractOption {
	return func(o *interactOptions) error {
		for i, c := range cases {
			if err := c.validate(i, false); err != nil {
				return err
			}
			if c.NoConsume || len(c.Sessions) > 0 {
				return configError("interact input case", "NoConsume and Sessions are not supported")
			}
		}
		o.input = cases
		return nil
	}
}

// WithInteractOutputCases matches peer output while it is shown to the
// operator; Return ends the interaction. Matching keeps a window of
// min(64 KiB, buffer limit) bytes.
func WithInteractOutputCases(cases ...Case) InteractOption {
	return func(o *interactOptions) error {
		for i, c := range cases {
			if err := c.validate(i, false); err != nil {
				return err
			}
		}
		o.output = cases
		return nil
	}
}

// WithInteractInputTimeout sets the operator idle interval: held input is
// flushed and an input Timeout case fires. Zero disables it.
func WithInteractInputTimeout(d time.Duration) InteractOption {
	return func(o *interactOptions) error {
		if d < 0 {
			return configError("interact input timeout", "negative")
		}
		o.inputTimeout = d
		return nil
	}
}

// WithInteractMatchOptions configures output matching, for example a
// WithTimeout that ends an unattended interaction (default: no timeout).
func WithInteractMatchOptions(opts ...MatchOption) InteractOption {
	return func(o *interactOptions) error {
		o.match = append(o.match, opts...)
		return nil
	}
}

// WithInteractEcho echoes matched operator sequences to the output.
func WithInteractEcho(enabled bool) InteractOption {
	return func(o *interactOptions) error {
		o.echo = enabled
		return nil
	}
}

// WithInteractInputForward also sends matched operator sequences to the
// peer, before their handler runs.
func WithInteractInputForward(enabled bool) InteractOption {
	return func(o *interactOptions) error {
		o.inputForward = enabled
		return nil
	}
}

// WithInteractOutputForward selects whether peer output is shown to the
// operator (default true).
func WithInteractOutputForward(enabled bool) InteractOption {
	return func(o *interactOptions) error {
		o.outputForward = enabled
		return nil
	}
}

// WithInteractCookedCallbacks restores the operator terminal to its
// original mode while handlers run, so they can prompt normally. Only
// InteractTerminal accepts it.
func WithInteractCookedCallbacks() InteractOption {
	return func(o *interactOptions) error {
		o.cooked = true
		return nil
	}
}

func applyInteractOptions(opts []InteractOption) (interactOptions, error) {
	o := defaultInteractOptions()
	for _, opt := range opts {
		if opt == nil {
			return o, configError("interact option", "nil")
		}
		if err := opt(&o); err != nil {
			return o, err
		}
	}
	return o, nil
}
