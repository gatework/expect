package scan

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrGlob rejects a malformed glob: an unterminated bracket or a trailing
// escape.
var ErrGlob = errors.New("scan: malformed glob")

// Glob compiles a Tcl-style glob to an unanchored regular expression:
// '*' matches any run of characters including newlines, '?' one
// character, "[a-z]" one character of a set (reversed ranges are accepted)
// and '\' escapes the next character.
func Glob(pattern string, fold bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if fold {
		b.WriteString("(?i)")
	}
	b.WriteString("(?s:")
	for i := 0; i < len(pattern); {
		r, n := utf8.DecodeRuneInString(pattern[i:])
		i += n
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '\\':
			if i >= len(pattern) {
				return nil, ErrGlob
			}
			e, n := utf8.DecodeRuneInString(pattern[i:])
			i += n
			b.WriteString(regexp.QuoteMeta(string(e)))
		case '[':
			end, class, err := globClass(pattern[i:], fold)
			if err != nil {
				return nil, err
			}
			i += end
			b.WriteString(class)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte(')')
	return regexp.Compile(b.String())
}

// globClass translates the bracket body at the start of s (after '[') and
// returns how many bytes it used, including the closing ']'.
func globClass(s string, fold bool) (int, string, error) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < len(s); {
		lo, n := classRune(s[i:])
		if n == 0 {
			return 0, "", ErrGlob
		}
		if s[i] == ']' {
			if b.Len() == 1 {
				return 0, "", ErrGlob
			}
			b.WriteByte(']')
			return i + 1, b.String(), nil
		}
		i += n
		hi := lo
		if i+1 < len(s) && s[i] == '-' && s[i+1] != ']' {
			var m int
			if hi, m = classRune(s[i+1:]); m == 0 {
				return 0, "", ErrGlob
			}
			i += 1 + m
		}
		if fold {
			lo, hi = unicode.ToLower(lo), unicode.ToLower(hi)
		}
		lo, hi = min(lo, hi), max(lo, hi)
		b.WriteString(quoteClass(lo))
		if hi != lo {
			b.WriteByte('-')
			b.WriteString(quoteClass(hi))
		}
	}
	return 0, "", ErrGlob
}

// classRune decodes one possibly escaped rune; n is zero at a dangling
// escape or the end of s.
func classRune(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	if s[0] == '\\' {
		if len(s) < 2 {
			return 0, 0
		}
		r, n := utf8.DecodeRuneInString(s[1:])
		return r, n + 1
	}
	return utf8.DecodeRuneInString(s)
}

func quoteClass(r rune) string {
	if strings.ContainsRune(`\]^-[`, r) {
		return `\` + string(r)
	}
	return string(r)
}
