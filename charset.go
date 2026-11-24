package expect

import (
	"errors"
	"slices"

	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// streamDecoder decodes a device charset to UTF-8 across chunks: a
// character split between reads is held until it is complete. It belongs
// to the read pump.
type streamDecoder struct {
	t     transform.Transformer
	carry []byte
}

func newStreamDecoder(enc encoding.Encoding) *streamDecoder {
	d := &streamDecoder{t: enc.NewDecoder()}
	d.t.Reset()
	return d
}

// decode returns p as UTF-8; final flushes a held partial character,
// which becomes U+FFFD.
func (d *streamDecoder) decode(p []byte, final bool) []byte {
	src := p
	if len(d.carry) > 0 {
		src = slices.Concat(d.carry, p)
		d.carry = nil
	}
	dst := make([]byte, 2*len(src)+utf8Max)
	var out []byte
	for {
		nDst, nSrc, err := d.t.Transform(dst, src, final)
		out = append(out, dst[:nDst]...)
		src = src[nSrc:]
		switch {
		case errors.Is(err, transform.ErrShortDst):
			continue
		case errors.Is(err, transform.ErrShortSrc):
			d.carry = append([]byte(nil), src...)
		}
		return out
	}
}

// utf8Max is the longest UTF-8 encoding of one character.
const utf8Max = 4

// encodeText converts UTF-8 text to the session charset; without one it
// returns the bytes unchanged.
func (s *Session) encodeText(p []byte) ([]byte, error) {
	if s.enc == nil {
		return p, nil
	}
	out, err := s.enc.Bytes(p)
	if err != nil {
		return nil, ErrUnencodable
	}
	return out, nil
}
