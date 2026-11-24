package expect

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// FuzzCharsetChunks checks that decoding GB18030 split anywhere gives the
// same UTF-8 as decoding it whole.
func FuzzCharsetChunks(f *testing.F) {
	enc := simplifiedchinese.GB18030
	seed, _ := enc.NewEncoder().Bytes([]byte("确认清除接口配置吗？[Y/N]: 😀 ok"))
	f.Add(seed, uint8(1))
	f.Add([]byte{0x81, 0x30, 0x81}, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		want := newStreamDecoder(enc).decode(data, true)
		d := newStreamDecoder(enc)
		var got []byte
		n := int(step)%5 + 1
		for at := 0; at < len(data); at += n {
			end := min(at+n, len(data))
			got = append(got, d.decode(data[at:end], false)...)
		}
		got = append(got, d.decode(nil, true)...)
		if !bytes.Equal(got, want) {
			t.Fatalf("split %d: %q, whole %q", n, got, want)
		}
	})
}
