package record

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// TestAppendJSONString checks that the hand-written encoder decodes to
// exactly what json.Marshal of the same string decodes to.
func TestAppendJSONString(t *testing.T) {
	t.Parallel()
	inputs := [][]byte{
		[]byte(`plain "quoted" \ back`), []byte("ctl \x00\x01\x1f\x7f\b\f"), []byte("crlf\r\n\ttab"),
		[]byte("utf8 你好    "), []byte("bad \xff\xfe \xe4\xbd"), {},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		b := make([]byte, rng.IntN(16))
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		inputs = append(inputs, b)
	}
	for _, in := range inputs {
		got := appendJSONString(nil, in)
		want, err := json.Marshal(string(in))
		if err != nil {
			t.Fatal(err)
		}
		var g, w string
		if err := json.Unmarshal(got, &g); err != nil {
			t.Fatalf("%q encoded to invalid JSON %s: %v", in, got, err)
		}
		if err := json.Unmarshal(want, &w); err != nil {
			t.Fatal(err)
		}
		if g != w {
			t.Fatalf("%q: decoded %q, json.Marshal decodes %q", in, g, w)
		}
	}
}

// TestBufferedFile_FlushesLater checks that buffered bytes reach the file
// after flushDelay without further writes, and at Close.
func TestBufferedFile_FlushesLater(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(dir, "a.log")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		b := newBufferedFile(f)
		if _, err := b.Write([]byte("one\n")); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); len(got) != 0 {
			t.Fatalf("flushed early: %q", got)
		}
		time.Sleep(flushDelay)
		synctest.Wait()
		if got, _ := os.ReadFile(path); string(got) != "one\n" {
			t.Fatalf("after delay: %q", got)
		}
		if _, err := b.Write([]byte("two\n")); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "one\ntwo\n" {
			t.Fatalf("after close: %q", got)
		}
		if _, err := b.Write([]byte("x")); err == nil {
			t.Fatal("write after close succeeded")
		}
	})
}
