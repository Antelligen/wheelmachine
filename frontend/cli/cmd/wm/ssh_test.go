package main

import (
	"net/http"
	"strings"
	"testing"

	"wheelmachine/frontend/cli/internal/fakeapi"
)

func TestSSHRunsACommandAndExitsWithItsCode(t *testing.T) {
	h := newHarness(t)
	m := h.api.AddMachine("box", "paused")
	// stdin that types a line, sees it come back in capitals, then exits 3
	r := h.wm("whoami\nexit 3\n", "ssh", m.ID, "--", "bash", "-l")
	r.must(t, 3)
	if !strings.Contains(r.stdout, "$ ") || !strings.Contains(r.stdout, "WHOAMI\n") {
		t.Fatalf("output did not cross: %q", r.stdout)
	}
	q := h.api.TerminalQuery()
	if got := strings.Join(q["cmd"], " "); got != "bash -l" {
		t.Fatalf("command asked for: %q", got)
	}
	// not a terminal in the test (stdin is a pipe): no window is sent, and that is fine
	if _, ok := h.api.Last("GET", "/v1/machines/"+m.ID+"/terminal"); !ok {
		t.Fatalf("the terminal route was not called: %v", h.api.Requests())
	}
}

func TestSSHOpensAsRoot(t *testing.T) {
	h := newHarness(t)
	m := h.api.AddMachine("box", "running")
	h.wm("exit 0\n", "ssh", "--root", m.ID).must(t, 0)
	if h.api.TerminalQuery().Get("user") != "root" {
		t.Fatalf("user: %v", h.api.TerminalQuery())
	}
}

func TestSSHNeedsAMachine(t *testing.T) {
	h := newHarness(t)
	h.wm("", "ssh").must(t, 2) // no machine named
}

func TestSSHSaysWhenAMachineCannotOpenOne(t *testing.T) {
	h := newHarness(t)
	m := h.api.AddMachine("box", "running")
	h.api.Refuse = func(r *http.Request) *fakeapi.Refusal {
		if strings.HasSuffix(r.URL.Path, "/terminal") {
			return &fakeapi.Refusal{Status: 501, Code: "not_open", Detail: "this machine cannot open a terminal"}
		}
		return nil
	}
	r := h.wm("", "ssh", m.ID).must(t, 1)
	if !strings.Contains(r.stderr, "cannot open a terminal") {
		t.Fatalf("stderr: %q", r.stderr)
	}
}
