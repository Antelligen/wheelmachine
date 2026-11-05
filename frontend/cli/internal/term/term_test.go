package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	for in, want := range map[string]string{
		"ak_live_x\n":      "ak_live_x",
		"ak_live_x\r\n":    "ak_live_x",
		"ak_live_x":        "ak_live_x",
		"first\nsecond\n":  "first",
		"  spaced key  \n": "  spaced key  ",
		"\n":               "",
	} {
		got, err := readLine(strings.NewReader(in))
		if err != nil || string(got) != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := readLine(strings.NewReader("")); err == nil {
		t.Error("nothing at all is no line")
	}
	if _, err := readLine(strings.NewReader(strings.Repeat("k", 9000))); err == nil {
		t.Error("a line longer than any key is refused")
	}
}

func TestAFileIsNotATerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) || IsTerminal(nil) {
		t.Fatal("a file is not a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if IsTerminal(r) {
		t.Fatal("a pipe is not a terminal")
	}
	if _, err := ReadPassword(nil); err == nil {
		t.Fatal("no terminal, no password")
	}
}
