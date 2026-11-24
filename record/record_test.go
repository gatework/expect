package record_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/gatework/expect/record"
)

var clock = time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))

func TestParseConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		environ map[string]string
		want    record.Config
		wantErr bool
	}{
		{"defaults", map[string]string{}, record.DefaultConfig(), false},
		{"override", map[string]string{
			"EXPECT_RECORD": "off", "EXPECT_RECORD_DIR": "/var/log/expect", "EXPECT_RECORD_FORMATS": "jsonl, LOG",
			"EXPECT_RECORD_MAX_SIZE": "5", "EXPECT_RECORD_MAX_AGE": "24h", "EXPECT_RECORD_REQUIRED": "yes",
		}, record.Config{Dir: "/var/log/expect", Formats: []record.Format{record.FormatJSONL, record.FormatLog}, MaxSizeMB: 5, MaxAge: 24 * time.Hour, Required: true}, false},
		{"bad switch", map[string]string{"EXPECT_RECORD": "maybe"}, record.Config{}, true},
		{"bad format", map[string]string{"EXPECT_RECORD_FORMATS": "pcap"}, record.Config{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := record.ParseConfig(tt.environ)
			if tt.wantErr {
				if !errors.Is(err, record.ErrConfig) {
					t.Fatalf("err = %v, want ErrConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got, cmp.FilterPath(func(p cmp.Path) bool {
				return p.Last().String() == ".Now" || p.Last().String() == ".Logger"
			}, cmp.Ignore())); diff != "" {
				t.Fatalf("config (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNew_DefaultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	r, err := record.New(record.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "share", "gatework", "expect"); r.Dir() != want {
		t.Fatalf("Dir = %q, want %q", r.Dir(), want)
	}
}

func TestNew_Prune(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, d := range []string{"2026-10-01", "2026-11-19", "keep-me"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c := record.Config{Enabled: true, Dir: root, MaxAge: 7 * 24 * time.Hour, Now: func() time.Time { return clock }}
	if _, err := record.New(c); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if diff := cmp.Diff([]string{"2026-11-19", "keep-me"}, names); diff != "" {
		t.Fatalf("after prune (-want +got):\n%s", diff)
	}
}

func TestTranscript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := clock
	r, err := record.New(record.Config{
		Enabled: true, Dir: root, Formats: []record.Format{record.FormatLog, record.FormatCast, record.FormatJSONL},
		Now: func() time.Time { now = now.Add(time.Second); return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := r.Open(record.Meta{Session: "ab12", Label: "10.0.0.1", Width: 132})
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		d    record.Direction
		data string
	}{
		{record.DirectionReceived, "Username: "},
		{record.DirectionSent, "admin\n"},
		{record.DirectionReceived, "admin\r"},
		{record.DirectionReceived, "\nrouter# \xe4\xbd"},
		{record.DirectionReceived, "\xa0"},
	}
	for _, s := range steps {
		write := tr.Received
		if s.d == record.DirectionSent {
			write = tr.Sent
		}
		if err := write([]byte(s.data)); err != nil {
			t.Fatal(err)
		}
	}
	slog.New(tr.Handler()).Info("match", "case", "prompt")
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	paths := tr.Paths()
	if len(paths) != 3 {
		t.Fatalf("paths = %v", paths)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, filepath.Join(root, "2026-11-20", "10.0.0.1_100001_ab12.")) {
			t.Fatalf("unexpected path %s", p)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", p, fi.Mode().Perm())
		}
	}
	checkCast(t, paths[0])
	checkLog(t, paths[2])
	if b, _ := os.ReadFile(paths[1]); !strings.Contains(string(b), `"case":"prompt"`) {
		t.Errorf("jsonl = %q", b)
	}
}

func checkLog(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-11-20T10:00:02.000+08:00 << Username: \n" +
		"2026-11-20T10:00:03.000+08:00 >> admin\n" +
		"2026-11-20T10:00:04.000+08:00 << admin\n" +
		"2026-11-20T10:00:05.000+08:00 << router# \xe4\xbd\xa0"
	if diff := cmp.Diff(want, string(b)); diff != "" {
		t.Fatalf("log (-want +got):\n%s", diff)
	}
}

func checkCast(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan()
	var h struct {
		Version, Width, Height int
		Title                  string
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Version != 2 || h.Width != 132 || h.Title != "10.0.0.1" {
		t.Fatalf("header %q: %v", sc.Text(), err)
	}
	var text strings.Builder
	for sc.Scan() {
		var ev []any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || len(ev) != 3 {
			t.Fatalf("event %q: %v", sc.Text(), err)
		}
		text.WriteString(ev[1].(string) + ":" + ev[2].(string) + "|")
	}
	if want := "o:Username: |i:admin\n|o:admin\r|o:\nrouter# |o:你|"; text.String() != want {
		t.Fatalf("events %q, want %q", text.String(), want)
	}
}

func TestRecorder_Disabled(t *testing.T) {
	t.Parallel()
	tr, err := record.Disabled().Open(record.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Received([]byte("x")); err != nil || len(tr.Paths()) != 0 {
		t.Fatalf("disabled transcript: %v %v", err, tr.Paths())
	}
}

func TestRecorder_Required(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := record.New(record.Config{Enabled: true, Dir: file, Required: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open(record.Meta{Session: "x"}); err == nil {
		t.Fatal("required recording failure ignored")
	}
	lax, _ := record.New(record.Config{Enabled: true, Dir: file})
	if _, err := lax.Open(record.Meta{Session: "x"}); err != nil {
		t.Fatalf("optional recording failure: %v", err)
	}
}

func TestReadCast_Rejects(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"empty":   "",
		"version": `{"version":1}` + "\n",
		"event":   `{"version":2}` + "\n" + `[1.0, "o"]` + "\n",
		"time":    `{"version":2}` + "\n" + `["x", "o", "a"]` + "\n",
	} {
		if _, err := record.ReadCast(strings.NewReader(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c, err := record.ReadCast(strings.NewReader(`{"version":2,"width":120,"height":40,"timestamp":1,"title":"r1"}` + "\n\n" + `[0.5, "o", "a\u001b[0m"]` + "\n"))
	if err != nil || c.Width != 120 || c.Title != "r1" || len(c.Frames) != 1 || c.Frames[0].Data != "a\x1b[0m" || c.Frames[0].At != 500*time.Millisecond {
		t.Fatalf("got %+v, %v", c, err)
	}
}
