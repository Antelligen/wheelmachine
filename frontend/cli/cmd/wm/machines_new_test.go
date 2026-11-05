package main

import (
	"encoding/json"
	"strings"
	"testing"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

// A team with one machine type, and the organisation's people: Ada and Lin in it, Mo in another team.
func (h *harness) typedTeam() (team string, kind api.MachineType) {
	h.inOrg("owner", nil)
	team = h.api.Project()
	cpus, after, away := 4, 600, 86400
	kind = api.MachineType{ID: "mty_00000000000000000000000001", Object: "machine_type", Project: team, Name: "builder", CPUs: &cpus,
		Network: api.Network{Internet: "closed", Allow: []string{}}, SleepWhenIdle: true, IdleAfterS: &after, PutAwayAfterS: &away,
		Apps: []string{"editor", "codex"}}
	h.api.Types = map[string][]api.MachineType{team: {kind}}
	h.api.People = []fakeapi.Person{
		{ID: "acc_0000000000000000000000ada1", Name: "Ada Lovelace", Email: "ada@example.com", Handle: "ada", Teams: []string{team}},
		{ID: "acc_0000000000000000000000000c", Name: "Lin Wu", Email: "lin@example.com", Handle: "lin", Teams: []string{team}},
		{ID: "acc_0000000000000000000000000d", Name: "Mo Diaz", Email: "mo@example.com", Handle: "mo", Teams: []string{"prj_elsewhere"}},
	}
	return team, kind
}

// wm machines new makes machines of a team's type for the people named - by @handle or e-mail - so many each, named as
// the pattern says, each on a line of its own; for everyone in the team; and says plainly of each that was not made why,
// ending in a failure. -q gives the ids of those made, --json the batch.
func TestMachinesNewMakesMachinesOfATypeForPeople(t *testing.T) {
	h := newHarness(t)
	h.typedTeam()
	r := h.wm("", "machines", "new", "--type", "builder", "--for", "@ada,lin@example.com", "--each", "2").must(t, 0)
	for _, want := range []string{"builder-ada-1", "builder-ada-2", "builder-lin-1", "builder-lin-2", "Ada Lovelace", "Lin Wu", "running"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("no %q in:\n%s", want, r.stdout)
		}
	}
	r = h.wm("", "machines", "new", "--type", "BUILDER", "--everyone", "--names", "<handle>-box", "-q").must(t, 0)
	if ids := strings.Fields(r.stdout); len(ids) != 2 || !api.IsMachineID(ids[0]) {
		t.Fatalf("everyone, quietly: %q", r.stdout)
	}
	r = h.wm("", "machines", "new", "--type", "builder", "--for", "@mo,@ada").must(t, 1)
	if !strings.Contains(r.stdout, "builder-mo") || !strings.Contains(r.stdout, "not made: Mo Diaz is not in this team") ||
		!strings.Contains(r.stderr, "1 of 2 machines were not made") {
		t.Fatalf("one not made:\n%s\n%s", r.stdout, r.stderr)
	}
	r = h.wm("", "machines", "new", "--type", "builder", "--for", "@ada", "--json").must(t, 0)
	var b api.MachineBatch
	if err := json.Unmarshal([]byte(r.stdout), &b); err != nil || b.Made != 1 || b.Data[0].Machine == nil || b.Data[0].Machine.Type == nil ||
		*b.Data[0].Machine.CPUs != 4 || b.Data[0].Machine.Network.Internet != "closed" {
		t.Fatalf("as JSON: %v %s", err, r.stdout)
	}
	if r := h.wm("", "machines", "new", "--type", "nope", "--for", "@ada").must(t, 1); !strings.Contains(r.stderr, `no machine type "nope"`) {
		t.Fatalf("no such type: %s", r.stderr)
	}
	for _, bad := range [][]string{{"machines", "new"}, {"machines", "new", "--type", "builder", "--for", "@ada", "--everyone"},
		{"machines", "new", "--type", "builder", "--each", "11"}, {"machines", "new", "--type", "builder", "extra"}} {
		if r := h.wm("", bad...); r.code != 2 {
			t.Errorf("%v: %d %s", bad, r.code, r.stderr)
		}
	}
}

// Machines still on their way are waited for, unless --no-wait.
func TestMachinesNewWaitsForThoseOnTheirWay(t *testing.T) {
	h := newHarness(t)
	h.typedTeam()
	h.api.Slow = true
	if r := h.wm("", "machines", "new", "--type", "builder", "--for", "@ada", "--no-wait").must(t, 0); !strings.Contains(r.stdout, "starting") {
		t.Fatalf("not waited for: %s", r.stdout)
	}
	if r := h.wm("", "machines", "new", "--type", "builder", "--for", "@lin").must(t, 0); !strings.Contains(r.stdout, "running") {
		t.Fatalf("waited for: %s", r.stdout)
	}
}

// wm machines types lists a team's types; wm machines create --type makes one of them for yourself, as it says - and
// is refused what the type says beside it, before anything is asked.
func TestMachinesTypesAndCreateFromAType(t *testing.T) {
	h := newHarness(t)
	_, kind := h.typedTeam()
	r := h.wm("", "machines", "types").must(t, 0)
	for _, want := range []string{"builder", "4 CPUs", "closed", "after 10m0s", "after 24h0m0s", "editor,codex"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("no %q in:\n%s", want, r.stdout)
		}
	}
	if r := h.wm("", "machines", "types", "-q").must(t, 0); strings.TrimSpace(r.stdout) != kind.ID {
		t.Fatalf("quietly: %q", r.stdout)
	}
	r = h.wm("", "machines", "create", "--type", "builder", "--json").must(t, 0)
	var m api.Machine
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil || m.Name != "builder" || m.Type == nil || *m.Type != kind.ID || m.PutAwayAfterS == nil {
		t.Fatalf("made of its type: %v %s", err, r.stdout)
	}
	if r := h.wm("", "machines", "create", "--type", "builder", "--cpus", "8"); r.code != 2 || !strings.Contains(r.stderr, "made as the type says") {
		t.Fatalf("a size beside its type: %d %s", r.code, r.stderr)
	}
	r = h.wm("", "machines", "create", "--put-away", "48h", "--json").must(t, 0)
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil || m.PutAwayAfterS == nil || *m.PutAwayAfterS != 172800 {
		t.Fatalf("its own put-away time: %v %s", err, r.stdout)
	}
	if r := h.wm("", "machines", "create", "--put-away", "30m"); r.code != 2 {
		t.Fatalf("too short a put-away time: %d", r.code)
	}
}
