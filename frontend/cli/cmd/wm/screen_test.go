package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A picture of a machine's desktop, written to a file of its person's alone, as asked; each of the commands that work its
// desktop sends what it says, at the scale asked for; and what cannot be said is refused before anything is sent.
func TestAMachinesDesktopFromTheCommandLine(t *testing.T) {
	h := newHarness(t)
	h.api.AddMachine("box", "running")
	dir := t.TempDir()
	file := filepath.Join(dir, "shot.png")
	r := h.wm("", "machines", "screenshot", "box", file, "--scale", "0.25", "--region", "1,2,30,40").must(t, 0)
	if !strings.Contains(r.stdout, "720 x 450 at scale 0.25") {
		t.Fatalf("%q", r.stdout)
	}
	if st, err := os.Stat(file); err != nil || st.Size() == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v %v", st, err)
	}
	if q := h.api.ScreenAsked; q.Get("scale") != "0.25" || q.Get("x") != "1" || q.Get("y") != "2" || q.Get("w") != "30" || q.Get("h") != "40" || q.Get("format") != "" {
		t.Fatalf("asked with %v", q)
	}
	// a file's name says its format; to standard output, and as JSON
	h.wm("", "machines", "screenshot", "box", filepath.Join(dir, "shot.jpg"), "-q").must(t, 0)
	if h.api.ScreenAsked.Get("format") != "jpeg" {
		t.Fatalf("asked with %v", h.api.ScreenAsked)
	}
	if r := h.wm("", "machines", "screenshot", "box", "-", "--format", "png").must(t, 0); !strings.HasPrefix(r.stdout, "\x89PNG") {
		t.Fatalf("to standard output: %q", r.stdout)
	}
	var said map[string]any
	r = h.wm("", "machines", "screenshot", "box", file, "--json").must(t, 0)
	if err := json.Unmarshal([]byte(r.stdout), &said); err != nil || said["file"] != file || said["width"] != 1440.0 || said["height"] != 900.0 || said["scale"] != 0.5 || said["format"] != "png" {
		t.Fatalf("as JSON: %q %v", r.stdout, err)
	}

	h.wm("", "machines", "click", "box", "720", "450", "--button", "right", "--count", "2", "--scale", "1").must(t, 0)
	got := h.api.ScreenActions
	if a := got.Actions[0]; a.Type != "click" || *a.X != 720 || *a.Y != 450 || a.Button != "right" || *a.Count != 2 || *got.Scale != 1 || got.Picture {
		t.Fatalf("a click: %+v", got)
	}
	h.wm("", "machines", "click", "box", "1", "2").must(t, 0)
	if a := h.api.ScreenActions; a.Scale != nil || a.Actions[0].Count != nil || a.Actions[0].Button != "" {
		t.Fatalf("a plain click: %+v", a)
	}
	if r := h.wm("", "machines", "type", "box", "héllo wörld").must(t, 0); !strings.Contains(r.stdout, "done") {
		t.Fatalf("%q", r.stdout)
	}
	if a := h.api.ScreenActions.Actions[0]; a.Type != "type" || *a.Text != "héllo wörld" {
		t.Fatalf("typed: %+v", a)
	}
	// from standard input, its last line's end left off and nothing of it said
	r = h.wm("a password\n", "machines", "type", "box").must(t, 0)
	if a := h.api.ScreenActions.Actions[0]; *a.Text != "a password" || strings.Contains(r.stdout+r.stderr, "a password") {
		t.Fatalf("typed from standard input: %+v, said %q", a, r.stdout+r.stderr)
	}
	h.wm("", "machines", "keys", "box", "ctrl+shift+t", "enter", "ctrl++", "+").must(t, 0)
	want := [][]string{{"ctrl", "shift", "t"}, {"enter"}, {"ctrl", "+"}, {"+"}}
	for i, a := range h.api.ScreenActions.Actions {
		if a.Type != "keys" || strings.Join(a.Keys, " ") != strings.Join(want[i], " ") {
			t.Fatalf("keys %d: %+v", i, a)
		}
	}
	h.wm("", "machines", "scroll", "box", "5", "6", "--dy", "-3", "--dx", "2").must(t, 0)
	if a := h.api.ScreenActions.Actions[0]; a.Type != "scroll" || *a.X != 5 || *a.Y != 6 || *a.DY != -3 || *a.DX != 2 {
		t.Fatalf("a scroll: %+v", a)
	}
	h.wm("", "machines", "create", "--name", "d", "--desktop").must(t, 0)
	if !h.api.DesktopAsked {
		t.Fatal("no desktop was asked for")
	}
	h.wm("", "machines", "create", "--name", "e").must(t, 0)
	if h.api.DesktopAsked {
		t.Fatal("a desktop was asked for")
	}
	for _, args := range [][]string{
		{"machines", "screenshot"},
		{"machines", "screenshot", "box", file, "--format", "gif"},
		{"machines", "screenshot", "box", file, "--region", "1,2,3"},
		{"machines", "screenshot", "box", file, "--region", "1,2,3,0"},
		{"machines", "click", "box", "1"},
		{"machines", "click", "box", "a", "b"},
		{"machines", "click", "box", "-1", "2"},
		{"machines", "keys", "box"},
		{"machines", "keys", "box", "ctrl+"},
		{"machines", "scroll", "box", "1", "2"},
		{"machines", "create", "--type", "t", "--desktop"},
	} {
		h.wm("", args...).must(t, 2)
	}
}
