package main

import (
	"strings"
	"testing"
)

// wm machines create --internet and --allow set the policy, and the created machine's view shows it; wm machines
// network changes it and shows the new one.
func TestMachineInternetPolicyThroughTheCLI(t *testing.T) {
	h := newHarness(t)

	// made closed: the API is asked for a closed machine
	h.wm("", "machines", "create", "--internet", "closed").must(t, 0)
	req, ok := h.api.Last("POST", "/v1/machines")
	if !ok || !strings.Contains(string(req.Body), `"internet":"closed"`) {
		t.Fatalf("the API was not asked for a closed machine: %s", req.Body)
	}

	// made allowed with a name and a range: the allow list is sent
	h.wm("", "machines", "create", "--internet", "allowed", "--allow", "api.github.com", "--allow", "10.0.0.0/8").must(t, 0)
	req, _ = h.api.Last("POST", "/v1/machines")
	if !strings.Contains(string(req.Body), `"internet":"allowed"`) || !strings.Contains(string(req.Body), `"api.github.com"`) {
		t.Fatalf("the allow list was not sent: %s", req.Body)
	}

	// an open machine, then changed with `machines network`: the detail view shows the new policy
	id := h.api.AddMachine("job", "running").ID
	r := h.wm("", "machines", "network", id, "--internet", "allowed", "--allow", "api.openai.com").must(t, 0)
	if !strings.Contains(r.stdout, "allowed: api.openai.com") {
		t.Fatalf("the view after the change:\n%s", r.stdout)
	}
	req, ok = h.api.Last("PATCH", "/v1/machines/"+id)
	if !ok || !strings.Contains(string(req.Body), `"internet":"allowed"`) {
		t.Fatalf("the policy change was not sent: %s", req.Body)
	}
	// and get shows it kept
	if g := h.wm("", "machines", "get", id).must(t, 0); !strings.Contains(g.stdout, "internet") || !strings.Contains(g.stdout, "allowed: api.openai.com") {
		t.Fatalf("get does not show the policy:\n%s", g.stdout)
	}

	// bad words are refused before any call
	for _, args := range [][]string{
		{"machines", "create", "--internet", "maybe"},
		{"machines", "create", "--allow", "api.github.com"},
		{"machines", "create", "--internet", "closed", "--allow", "x.com"},
		{"machines", "network", id},
	} {
		if rr := h.wm("", args...).must(t, 2); rr.stderr == "" {
			t.Fatalf("%v was not refused", args)
		}
	}
}
