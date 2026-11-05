package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

func TestAppsAreListedWithWhereEachIsReached(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	r := h.wm("", "apps", "ls", "box").must(t, 0)
	got := lines(r.stdout)
	if len(got) != 7 || !strings.HasPrefix(got[0], "NAME") || !strings.Contains(got[0], "WHERE") {
		t.Fatalf("%q", r.stdout)
	}
	for _, want := range []string{
		`^desktop +Desktop +web +off +https://6901-` + strings.TrimPrefix(box.ID, "m_") + `\.run\.wheelmachine\.ai$`,
		`^ollama +Ollama +service +off +inside the machine$`,
		`^codex +Codex +agent +on +a session of its own in the machine$`,
		`^gemini +Gemini CLI +terminal +- +type gemini in the machine's terminal \(wm ssh box\)$`,
	} {
		if !regexp.MustCompile(`(?m)` + want).MatchString(r.stdout) {
			t.Fatalf("no line %s in:\n%s", want, r.stdout)
		}
	}
	if r := h.wm("", "apps", "ls", "box", "-q").must(t, 0); r.stdout != "desktop\neditor\nopenclaw\nollama\ncodex\ngemini\n" {
		t.Fatalf("%q", r.stdout)
	}
	var list []api.App
	if r := h.wm("", "apps", "ls", "--json", "box").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 6 ||
		list[0].Kind != "web" || *list[0].Port != 6901 || list[4].Kind != "agent" || !*list[4].On || list[5].On != nil {
		t.Fatalf("%s", r.stdout)
	}
	if _, ok := h.api.Last(http.MethodGet, "/v1/machines/"+box.ID+"/apps"); !ok {
		t.Fatal("the machine's apps were not asked for")
	}
}

// An agent whose session began before the machine's AI keys changed says so in its state, with what puts it right;
// its JSON carries keys_changed as the API says it, and no other app's carries it where the API leaves it out. Turned
// off and on again, it is started with the keys, and says so no more.
func TestAnAgentStartedBeforeTheKeysChangedSaysToRestartIt(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	h.wm("", "apps", "ls", "box", "-q").must(t, 0)
	codex, yes := h.api.Apps(box.ID)[4], true
	codex.Running, codex.KeysChanged = &yes, &yes
	h.api.SetApp(box.ID, codex)
	r := h.wm("", "apps", "ls", "box").must(t, 0)
	if !regexp.MustCompile(`(?m)^codex +Codex +agent +on, restart to use new keys +a session of its own in the machine$`).MatchString(r.stdout) {
		t.Fatalf("%s", r.stdout)
	}
	var list []map[string]any
	if r := h.wm("", "apps", "ls", "--json", "box").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 6 ||
		list[4]["name"] != "codex" || list[4]["keys_changed"] != true {
		t.Fatalf("%s", r.stdout)
	}
	for _, a := range append(list[:4:4], list[5]) {
		if _, there := a["keys_changed"]; there {
			t.Fatalf("%s carries keys_changed: %v", a["name"], a)
		}
	}
	h.wm("", "apps", "off", "box", "codex", "-q").must(t, 0)
	h.wm("", "apps", "on", "box", "codex", "-q").must(t, 0)
	if r := h.wm("", "apps", "ls", "box").must(t, 0); !regexp.MustCompile(`(?m)^codex +Codex +agent +on, running +a session`).MatchString(r.stdout) {
		t.Fatalf("after it was started again:\n%s", r.stdout)
	}
}

func TestAWebAppTurnedOnPrintsItsLinkAndATokenOnce(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	r := h.wm("", "apps", "on", "box", "desktop").must(t, 0)
	link := regexp.MustCompile(`^https://6901-[0-9a-z]+\.run\.wheelmachine\.ai/\.wheelmachine/link/[0-9]{43}\n$`)
	if !link.MatchString(r.stdout) || !strings.Contains(r.stderr, "Desktop is on") || !strings.Contains(r.stderr, "for 30m") ||
		!strings.Contains(r.stderr, "wm apps open box desktop") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	req, _ := h.api.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/desktop/on")
	if string(req.Body) != `{"access_s":1800}` {
		t.Fatalf("sent %s", req.Body)
	}
	if ports := h.api.Ports(box.ID); len(ports) != 1 || ports[0].Port != 6901 || ports[0].Public {
		t.Fatalf("ports %+v", ports)
	}
	// its token, shown once - even with -q, since nothing keeps it
	r = h.wm("", "apps", "on", "box", "openclaw", "--for", "10m", "-q").must(t, 0)
	if got := lines(r.stdout); len(got) != 2 || !strings.HasPrefix(got[0], "https://18789-") || got[1] != "token: "+fakeapi.AppToken || r.stderr != "" {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	if req, _ := h.api.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/openclaw/on"); string(req.Body) != `{"access_s":600}` {
		t.Fatalf("sent %s", req.Body)
	}
	r = h.wm("", "apps", "on", "box", "openclaw").must(t, 0)
	if !strings.Contains(r.stderr, "the token is shown this once") {
		t.Fatalf("%q", r.stderr)
	}
	var on api.App
	if r := h.wm("", "apps", "on", "box", "openclaw", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &on) != nil || on.Token != fakeapi.AppToken ||
		on.Link == "" || on.ExpiresAt == nil {
		t.Fatalf("%s", r.stdout)
	}
	// a service: on, inside the machine, and nothing printed but that
	if r := h.wm("", "apps", "on", "box", "ollama").must(t, 0); r.stdout != "" || !strings.Contains(r.stderr, "Ollama is on, inside the machine") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestAnAppIsTurnedOffAndItsAddressClosedWithIt(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	h.wm("", "apps", "on", "box", "desktop", "-q").must(t, 0)
	r := h.wm("", "apps", "off", "box", "desktop").must(t, 0)
	if r.stdout != "desktop\n" || !strings.Contains(r.stderr, "Desktop is off") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	if req, ok := h.api.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/desktop/off"); !ok || len(req.Body) != 0 {
		t.Fatalf("sent %q", req.Body)
	}
	if ports := h.api.Ports(box.ID); len(ports) != 0 {
		t.Fatalf("ports %+v", ports)
	}
	var off api.App
	if r := h.wm("", "apps", "off", "box", "desktop", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &off) != nil || off.On == nil || *off.On {
		t.Fatalf("%s", r.stdout)
	}
}

func TestOpenPrintsAFreshLinkAndOpensItOnlyWhenAsked(t *testing.T) {
	h := newHarness(t)
	h.api.AddMachine("box", "running")
	if r := h.wm("", "apps", "open", "box", "editor").must(t, 1); !strings.Contains(r.stderr, "Code editor is off: turn it on first") {
		t.Fatalf("%q", r.stderr)
	}
	h.wm("", "apps", "on", "box", "editor", "-q").must(t, 0)
	r := h.wm("", "apps", "open", "box", "editor").must(t, 0)
	first := strings.TrimSpace(r.stdout)
	if !strings.HasPrefix(first, "https://13337-") || !strings.Contains(r.stderr, "opens once") || len(h.opened) != 0 {
		t.Fatalf("stdout %q stderr %q opened %v", r.stdout, r.stderr, h.opened)
	}
	r = h.wm("", "apps", "open", "box", "editor", "--browser", "--for", "5m").must(t, 0)
	second := strings.TrimSpace(r.stdout)
	if second == first || len(h.opened) != 1 || h.opened[0] != second || !strings.Contains(r.stderr, "opening in your browser") {
		t.Fatalf("stdout %q stderr %q opened %v", r.stdout, r.stderr, h.opened)
	}
	if r := h.wm("", "apps", "open", "box", "ollama").must(t, 1); !strings.Contains(r.stderr, "has no web address") {
		t.Fatalf("%q", r.stderr)
	}
}

func TestAppsThatAreNotRight(t *testing.T) {
	h := newHarness(t)
	h.api.AddMachine("box", "running")
	for _, args := range [][]string{{"apps", "on", "box"}, {"apps", "on", "box", "desktop", "more"}, {"apps", "on", "box", "Desktop"},
		{"apps", "off", "box", "../keys"}, {"apps", "open", "box", "desktop", "--for", "30s"}, {"apps", "on", "box", "desktop", "--for", "2h"},
		{"apps", "ls"}, {"apps", "start", "box", "desktop"}} {
		if r := h.wm("", args...); r.code != 2 {
			t.Fatalf("%v: exit %d, %q", args, r.code, r.stderr)
		}
	}
	if r := h.wm("", "apps", "on", "box", "nope").must(t, 1); !strings.Contains(r.stderr, "no such app in this machine (not_found, 404") {
		t.Fatalf("%q", r.stderr)
	}
	h.api.NoApps = true
	if r := h.wm("", "apps", "ls", "box").must(t, 1); !strings.Contains(r.stderr, "this machine has no apps; make a new machine to have them (no_apps, 409") {
		t.Fatalf("%q", r.stderr)
	}
	if r := h.wm("", "apps", "-h").must(t, 0); !strings.Contains(r.stdout, "Usage: wm apps <command>") {
		t.Fatalf("%q", r.stdout)
	}
	if r := h.wm("", "help", "apps").must(t, 0); !strings.Contains(r.stdout, "open <machine> <app>") {
		t.Fatalf("%q", r.stdout)
	}
	if r := h.wm("", "apps", "on", "-h").must(t, 0); !strings.Contains(r.stdout, "--for") {
		t.Fatalf("%q", r.stdout)
	}
}

// An app shown on the machine's screen: asked of the API with nothing in the body, said by its name, and pointed to the
// dashboard's Screen tab; one that is off is refused in the API's words.
func TestAnAppIsShownOnTheMachinesScreen(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	if r := h.wm("", "apps", "show", "box", "openclaw"); r.code == 0 || !strings.Contains(r.stderr, "is off") {
		t.Fatalf("an app that is off: %d %q", r.code, r.stderr)
	}
	h.wm("", "apps", "on", "box", "openclaw").must(t, 0)
	r := h.wm("", "apps", "show", "box", "openclaw").must(t, 0)
	if r.stdout != "openclaw\n" || !strings.Contains(r.stderr, "OpenClaw is open on the machine's screen") || !strings.Contains(r.stderr, "Screen tab") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	req, ok := h.api.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/openclaw/show")
	if !ok || len(req.Body) != 0 {
		t.Fatalf("sent %v %q", ok, req.Body)
	}
	if r := h.wm("", "apps", "show", "box", "openclaw", "--json").must(t, 0); !strings.Contains(r.stdout, `"name": "openclaw"`) && !strings.Contains(r.stdout, `"name":"openclaw"`) {
		t.Fatalf("%s", r.stdout)
	}
}
