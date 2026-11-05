package main

import (
	"encoding/json"
	"strings"
	"testing"

	"wheelmachine/frontend/cli/api"
)

// A made-up value, nobody's.
const madeUpValue = "made-up-secret-5e1d"

// Whether any request but the one that set it carried the value, anywhere: in its path, query or headers, or in the
// body of any other call.
func carriedElsewhere(h *harness, value string) bool {
	for _, r := range h.api.Requests() {
		if strings.Contains(r.Path, value) || strings.Contains(r.Query.Encode(), value) {
			return true
		}
		for _, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, value) {
					return true
				}
			}
		}
		if !(r.Method == "PUT" && strings.HasPrefix(r.Path, "/v1/secrets/")) && strings.Contains(string(r.Body), value) {
			return true
		}
	}
	return false
}

func TestASecretIsSetFromStandardInputAndNeverShown(t *testing.T) {
	h := newHarness(t)
	r := h.wm(madeUpValue+"\n", "secrets", "set", "API_TOKEN").must(t, 0)
	if got, _ := h.api.Secret("API_TOKEN"); got != madeUpValue {
		t.Fatalf("the API was given %q (one newline at the end is left off)", got)
	}
	if !strings.Contains(r.stdout, "Set API_TOKEN. No machine gets it yet") {
		t.Fatalf("%q", r.stdout)
	}
	set, _ := h.api.Last("PUT", "/v1/secrets/API_TOKEN")
	var body map[string]string
	if err := json.Unmarshal(set.Body, &body); err != nil || body["value"] != madeUpValue || len(set.Query) != 0 {
		t.Fatalf("set as %s %v", set.Body, set.Query)
	}
	// a value of several lines keeps them, but for the last newline
	h.wm("-----BEGIN-----\nmade-up\n-----END-----\n", "secrets", "set", "PEM").must(t, 0)
	if got, _ := h.api.Secret("PEM"); got != "-----BEGIN-----\nmade-up\n-----END-----" {
		t.Fatalf("%q", got)
	}

	// never from the command line: the words are not said back, and nothing is called
	before := len(h.api.Requests())
	for _, args := range [][]string{
		{"secrets", "set", "API_TOKEN", madeUpValue},
		{"secrets", "set", "API_TOKEN", "--" + madeUpValue},
		{"secrets", "set", "--value=" + madeUpValue, "API_TOKEN"},
	} {
		r := h.wm("", args...).must(t, 2)
		if strings.Contains(r.stderr+r.stdout, madeUpValue) || !strings.Contains(r.stderr, "never taken from the command line") {
			t.Fatalf("%v: %q", args, r.stderr)
		}
	}
	for _, c := range []struct{ stdin, name, want string }{
		{"", "API_TOKEN", "no value given"},
		{"\n", "API_TOKEN", "no value given"},
		{"x", "api_token", "is not a secret's name"},
		{strings.Repeat("v", api.SecretValueMax+1), "API_TOKEN", "at most 32 KiB"},
		{"\xff\xfe", "API_TOKEN", "is text (UTF-8)"},
		{"a\x00b", "API_TOKEN", "cannot hold a NUL"},
	} {
		if r := h.wm(c.stdin, "secrets", "set", c.name).must(t, 2); !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%q: %q, want %q", c.name, r.stderr, c.want)
		}
	}
	if len(h.api.Requests()) != before {
		t.Fatal("a command that is not right called the API")
	}

	// listed by name: never a value, in any form
	r = h.wm("", "secrets", "ls").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "NAME") || !strings.HasPrefix(rows[1], "API_TOKEN") || !strings.HasPrefix(rows[2], "PEM") {
		t.Fatalf("%s", r.stdout)
	}
	out := r.stdout + h.wm("", "secrets", "ls", "--json").must(t, 0).stdout + h.wm("", "secrets", "ls", "-q").must(t, 0).stdout
	if strings.Contains(out, madeUpValue) || strings.Contains(out, "made-up\n") {
		t.Fatalf("a value is shown:\n%s", out)
	}
	if carriedElsewhere(h, madeUpValue) {
		t.Fatal("the value went somewhere but the body of the call that set it")
	}
}

func TestAMachineIsGivenSecretsByName(t *testing.T) {
	h := newHarness(t)
	h.wm(madeUpValue, "secrets", "set", "API_TOKEN").must(t, 0)
	h.wm("made-up-db", "secrets", "set", "DATABASE_URL").must(t, 0)
	r := h.wm("", "machines", "create", "--name", "web", "--secret", "API_TOKEN", "--secret", "DATABASE_URL", "-q").must(t, 0)
	id := strings.TrimSpace(r.stdout)
	made, _ := h.api.Last("POST", "/v1/machines")
	if !strings.Contains(string(made.Body), `"secrets":["API_TOKEN","DATABASE_URL"]`) {
		t.Fatalf("made with %s", made.Body)
	}
	if r := h.wm("", "machines", "get", "web").must(t, 0); !strings.Contains(r.stdout, "API_TOKEN, DATABASE_URL") {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "secrets", "ls").must(t, 0); !strings.Contains(lines(r.stdout)[1], id) {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("again", "secrets", "set", "API_TOKEN").must(t, 0); !strings.Contains(r.stdout, "The machines given it get it from their next command: "+id) {
		t.Fatalf("%q", r.stdout)
	}
	// said whole: the names given take the place of the ones it had; --none takes them all away
	h.wm("", "machines", "secrets", "web", "DATABASE_URL").must(t, 0)
	if m, _ := h.api.Machine(id); strings.Join(m.Secrets, ",") != "DATABASE_URL" {
		t.Fatalf("%v", m.Secrets)
	}
	r = h.wm("", "machines", "secrets", "web", "--none").must(t, 0)
	if m, _ := h.api.Machine(id); len(m.Secrets) != 0 || !strings.Contains(r.stdout, "secrets:") {
		t.Fatalf("%v %s", m.Secrets, r.stdout)
	}
	patch, _ := h.api.Last("PATCH", "/v1/machines/"+id)
	if string(patch.Body) != `{"secrets":[]}` {
		t.Fatalf("taken away with %s", patch.Body)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"machines", "secrets", "web"}, "say the secrets it gets, or --none"},
		{[]string{"machines", "secrets", "web", "A", "--none"}, "not both"},
		{[]string{"machines", "secrets", "web", "lower"}, "is not a secret's name"},
		{[]string{"machines", "create", "--secret", "bad-name"}, "is not a secret's name"},
	} {
		if r := h.wm("", c.args...).must(t, 2); !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%v: %q", c.args, r.stderr)
		}
	}
	if r := h.wm("", "machines", "create", "--secret", "NOT_SET").must(t, 1); !strings.Contains(r.stderr, "set it first") {
		t.Fatalf("%q", r.stderr)
	}
	// deleted after asking, and taken off the machines
	h.wm("", "machines", "secrets", "web", "API_TOKEN").must(t, 0)
	if r := h.wm("", "secrets", "rm", "API_TOKEN").must(t, 2); !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("%q", r.stderr)
	}
	h.wm("", "secrets", "rm", "--yes", "API_TOKEN").must(t, 0)
	if _, still := h.api.Secret("API_TOKEN"); still {
		t.Fatal("not deleted")
	}
	if m, _ := h.api.Machine(id); len(m.Secrets) != 0 {
		t.Fatalf("still given %v", m.Secrets)
	}
	h.wm("", "secrets", "rm", "--yes", "API_TOKEN").must(t, 1)
	if carriedElsewhere(h, madeUpValue) {
		t.Fatal("the value went somewhere but the body of the call that set it")
	}
}

func TestSecretsHelpAndTheScope(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("", "secrets", "-h").must(t, 0); !strings.Contains(r.stdout, "set <NAME>") || !strings.Contains(r.stdout, "never taken from the command line") {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "secrets", "set", "-h").must(t, 0); !strings.Contains(r.stdout, "wm secrets set [--inside] [--only-given] <NAME>") ||
		!strings.Contains(r.stdout, "An AI key") {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "-h").must(t, 0); !strings.Contains(r.stdout, "secrets    set, list and delete") {
		t.Fatalf("%s", r.stdout)
	}
	h.wm("", "keys", "create", "ci", "--scope", "secrets:write").must(t, 0)
	made, _ := h.api.Last("POST", "/v1/keys")
	if !strings.Contains(string(made.Body), `"cloud:secrets:write"`) {
		t.Fatalf("%s", made.Body)
	}
}

// An AI key is set by its name alone and kept by its preset: outside the machines and on every one of them, said so
// with the hosts it is sent to. --inside keeps it in the machines, --only-given on the machines given it; ls marks AI
// keys with their hosts, and a machine says the keys that reach it.
func TestAnAIKeyIsSetByItsNameAndKeptOutsideEveryMachine(t *testing.T) {
	h := newHarness(t)
	body := func() map[string]any {
		t.Helper()
		set, _ := h.api.Last("PUT", "/v1/secrets/ANTHROPIC_API_KEY")
		var b map[string]any
		if err := json.Unmarshal(set.Body, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	r := h.wm(madeUpValue, "secrets", "set", "ANTHROPIC_API_KEY").must(t, 0)
	if b := body(); len(b) != 1 || b["value"] != madeUpValue {
		t.Fatalf("set with %v: the API's own default is its preset", b)
	}
	if got := strings.TrimSpace(r.stdout); got != "Set ANTHROPIC_API_KEY, an AI key: on every machine. Sent only to api.anthropic.com. The key never enters a machine." {
		t.Fatalf("%q", got)
	}
	h.wm("", "machines", "create", "--name", "box").must(t, 0)
	if r := h.wm("", "machines", "get", "box").must(t, 0); !strings.Contains(r.stdout, "ai keys:") || !strings.Contains(r.stdout, "ANTHROPIC_API_KEY") {
		t.Fatalf("%s", r.stdout)
	}

	// only on the machines given it
	r = h.wm(madeUpValue, "secrets", "set", "--only-given", "ANTHROPIC_API_KEY").must(t, 0)
	if b := body(); b["every_machine"] != false || b["outside"] != nil {
		t.Fatalf("--only-given sent %v", b)
	}
	if !strings.Contains(r.stdout, "Set ANTHROPIC_API_KEY, an AI key. Sent only to api.anthropic.com; it never enters a machine. No machine gets it yet") {
		t.Fatalf("%q", r.stdout)
	}
	// kept inside, as an ordinary secret
	r = h.wm(madeUpValue, "secrets", "set", "--inside", "ANTHROPIC_API_KEY").must(t, 0)
	if b := body(); b["outside"] != false || b["every_machine"] != nil {
		t.Fatalf("--inside sent %v", b)
	}
	if !strings.Contains(r.stdout, "Set ANTHROPIC_API_KEY. No machine gets it yet") {
		t.Fatalf("%q", r.stdout)
	}

	// listed: an AI key with its hosts, on every machine; an ordinary secret sent nowhere
	h.wm(madeUpValue, "secrets", "set", "OPENAI_API_KEY").must(t, 0)
	h.wm("made-up-db", "secrets", "set", "DATABASE_URL").must(t, 0)
	rows := lines(h.wm("", "secrets", "ls").must(t, 0).stdout)
	if len(rows) != 4 || !strings.Contains(rows[0], "SENT ONLY TO") {
		t.Fatalf("%q", rows)
	}
	for _, row := range rows[1:] {
		f := strings.Fields(row)
		switch f[0] {
		case "OPENAI_API_KEY":
			if !strings.Contains(row, "every machine") || !strings.Contains(row, "api.openai.com,us.api.openai.com,eu.api.openai.com (AI key)") {
				t.Fatalf("%q", row)
			}
		case "ANTHROPIC_API_KEY", "DATABASE_URL":
			if strings.Contains(row, "every machine") || strings.Contains(row, "AI key") {
				t.Fatalf("%q", row)
			}
		}
	}
	if out := h.wm("", "secrets", "ls", "--json").must(t, 0).stdout; !strings.Contains(out, `"preset": "openai"`) || !strings.Contains(out, `"every_machine": true`) ||
		strings.Contains(out, madeUpValue) {
		t.Fatalf("%s", out)
	}
	// both flags at once: an ordinary secret, on the machines given it
	h.wm(madeUpValue, "secrets", "set", "--inside", "--only-given", "GEMINI_API_KEY").must(t, 0)
	set, _ := h.api.Last("PUT", "/v1/secrets/GEMINI_API_KEY")
	if !strings.Contains(string(set.Body), `"outside":false`) || !strings.Contains(string(set.Body), `"every_machine":false`) {
		t.Fatalf("%s", set.Body)
	}
	if carriedElsewhere(h, madeUpValue) {
		t.Fatal("the value went somewhere but the body of the call that set it")
	}
}
