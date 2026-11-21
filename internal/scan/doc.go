// Package scan finds text patterns in a receive buffer that grows by appends.
//
// A Matcher is immutable and safe for concurrent use. Per-wait progress lives
// in a State owned by the caller: it lets literals and line-local regular
// expressions resume where the last unsuccessful search stopped instead of
// rescanning the whole buffer. A State is valid only while the buffer it was
// used with changes by appending; any consumption must Reset it.
//
// Regular expressions use Go RE2 semantics. A trailing incomplete UTF-8
// sequence is not searched until more bytes or the end of input arrive, so a
// multi-byte rune split across reads is never seen as invalid input.
package scan
