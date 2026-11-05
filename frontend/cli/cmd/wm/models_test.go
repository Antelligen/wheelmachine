package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"wheelmachine/frontend/cli/api"
)

// wm models lists the models of the account's AI keys, by provider - one agent's alone with --agent - each with its
// company's list price and its score where they are known, and says under them what those are and whose, and a provider
// that did not answer; -q gives their ids and --json the API's own answer.
func TestTheModelsOfTheKeysAreListed(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "models").must(t, 0)
	got := lines(r.stdout)
	if len(got) != 4 || !regexp.MustCompile(`^MODEL +NAME +PRICE +SCORE$`).MatchString(got[0]) ||
		!regexp.MustCompile(`(?m)^deepseek/deepseek-v4-pro +DeepSeek-V4-Pro +\$1\.32 / \$3\.96 +1465$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^openai/gpt-6-luna +gpt-6-luna +\$0\.1 / \$0\.5 +-$`).MatchString(r.stdout) {
		t.Fatalf("%q", r.stdout)
	}
	notes := "Price: a million tokens in / out, each provider's own list price. Wheelmachine charges nothing for it.\n" +
		"Score: LMArena text leaderboard, 2026-10-28 (CC BY 4.0), https://lmarena.ai/leaderboard/text\n"
	if r.stderr != notes {
		t.Fatalf("what the prices and scores are: %q", r.stderr)
	}
	if r := h.wm("", "models", "--agent", "codex", "-q").must(t, 0); r.stdout != "openai/gpt-6.1-sol\nopenai/gpt-6-luna\n" {
		t.Fatalf("codex's models: %q", r.stdout)
	}
	if req, ok := h.api.Last(http.MethodGet, "/v1/models"); !ok || req.Query.Get("agent") != "codex" {
		t.Fatalf("asked %v", req.Query)
	}
	h.wm("", "models", "--refresh", "-q").must(t, 0)
	if req, _ := h.api.Last(http.MethodGet, "/v1/models"); req.Query.Get("refresh") != "true" {
		t.Fatalf("asked %v", req.Query)
	}
	var list api.ModelList
	if r := h.wm("", "models", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list.Data) != 3 || len(list.Suppliers) != 2 {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "models", "--agent", "nosuch").must(t, 1); !strings.Contains(r.stderr, "no such agent") {
		t.Fatalf("an agent that is none: %q", r.stderr)
	}
	// a provider that did not answer, said under the list
	words := "DeepSeek did not answer; this list is from 40 minutes ago."
	h.api.ChangeModels(func(l *api.ModelList) { l.Suppliers[1].State, l.Suppliers[1].Detail = "unreachable", &words })
	if r := h.wm("", "models").must(t, 0); r.stderr != words+"\n"+notes {
		t.Fatalf("a provider that did not answer: %q", r.stderr)
	}
	// with neither known, nothing is said of them
	h.api.ChangeModels(func(l *api.ModelList) {
		for i := range l.Data {
			l.Data[i].Price, l.Data[i].Intelligence = nil, nil
		}
	})
	if r := h.wm("", "models").must(t, 0); r.stderr != words+"\n" || !regexp.MustCompile(`(?m)^openai/gpt-6\.1-sol +gpt-6\.1-sol +- +-$`).MatchString(r.stdout) {
		t.Fatalf("no price and no score: %q %q", r.stdout, r.stderr)
	}
	if r := h.wm("", "models", "chosen", "extra").must(t, 2); !strings.Contains(r.stderr, "wm models chosen") {
		t.Fatalf("%q", r.stderr)
	}
	if r := h.wm("", "models", "nosuch").must(t, 2); !strings.Contains(r.stderr, `no command "nosuch" in wm models`) {
		t.Fatalf("%q", r.stderr)
	}
}

// A model chosen for an agent, and one for every agent, show in wm models chosen; taken away, each agent is on its
// default again. A team is named with --team, as the API's project.
func TestAModelIsChosenForAnAgentAndForEveryAgent(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "models", "chosen").must(t, 0)
	if !regexp.MustCompile(`(?m)^AGENT +CHOSEN +STARTS ON +STATE$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^codex +- +- +its own model$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^aider +- +openai/gpt-6\.1-sol +its default$`).MatchString(r.stdout) {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "models", "use", "codex", "openai/gpt-6-luna").must(t, 0); r.stdout != "Codex starts on openai/gpt-6-luna from its next start.\n" {
		t.Fatalf("%q", r.stdout)
	}
	if req, ok := h.api.Last(http.MethodPut, "/v1/agents/codex/model"); !ok || string(req.Body) != `{"model":"openai/gpt-6-luna"}` {
		t.Fatalf("asked %s", req.Body)
	}
	if r := h.wm("", "models", "chosen").must(t, 0); !regexp.MustCompile(`(?m)^codex +openai/gpt-6-luna +openai/gpt-6-luna +as chosen$`).MatchString(r.stdout) {
		t.Fatalf("%s", r.stdout)
	}
	if r := h.wm("", "models", "use", "--every", "deepseek/deepseek-v4-pro", "--team", "prj_team").must(t, 0); !strings.HasPrefix(r.stdout,
		"Every agent that can use it starts on deepseek/deepseek-v4-pro from its next start.") {
		t.Fatalf("%q", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodPut, "/v1/agents/model"); req.Query.Get("project") != "prj_team" {
		t.Fatalf("the team: %v", req.Query)
	}
	r = h.wm("", "models", "chosen").must(t, 0)
	if !strings.HasPrefix(r.stdout, "Every agent: deepseek/deepseek-v4-pro\n") ||
		!regexp.MustCompile(`(?m)^aider +deepseek/deepseek-v4-pro \(every agent\) +deepseek/deepseek-v4-pro +as chosen$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^codex +- +- +its own model$`).MatchString(r.stdout) {
		t.Fatalf("%s", r.stdout)
	}
	var list api.AgentList
	if r := h.wm("", "models", "chosen", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || list.EveryAgent == nil ||
		list.EveryAgent.Model != "deepseek/deepseek-v4-pro" || len(list.Data) != 2 {
		t.Fatalf("%s", r.stdout)
	}
	h.wm("", "models", "reset", "--every").must(t, 0)
	if _, ok := h.api.Last(http.MethodDelete, "/v1/agents/model"); !ok {
		t.Fatal("the one model for every agent was not taken away")
	}
	h.wm("", "models", "use", "aider", "openai/gpt-6.1-sol", "-q").must(t, 0)
	if r := h.wm("", "models", "reset", "aider").must(t, 0); !strings.HasPrefix(r.stdout, "aider starts on the model for every agent where it can") {
		t.Fatalf("%q", r.stdout)
	}
	if r := h.wm("", "models", "chosen").must(t, 0); strings.Contains(r.stdout, "as chosen") {
		t.Fatalf("all taken away: %s", r.stdout)
	}
	// what the API refuses is said as it says it; what is not a choice is said before anything is asked
	if r := h.wm("", "models", "use", "codex", "deepseek/deepseek-v4-pro").must(t, 1); !strings.Contains(r.stderr, "Codex cannot use") {
		t.Fatalf("%q", r.stderr)
	}
	for _, bad := range [][]string{{"models", "use", "codex"}, {"models", "use", "--every"}, {"models", "reset"}, {"models", "reset", "--every", "codex"}} {
		if r := h.wm("", bad...); r.code != 2 {
			t.Fatalf("%v: exit %d %s", bad, r.code, r.stderr)
		}
	}
	if r := h.wm("", "models", "use", "Codex", "openai/gpt-6.1-sol").must(t, 1); !strings.Contains(r.stderr, "an agent's name is lower-case letters") {
		t.Fatalf("%q", r.stderr)
	}
}

// wm models -h and wm help models say the commands; the main help names them.
func TestTheModelsHelp(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"models", "-h"}, {"help", "models"}} {
		if r := h.wm("", args...).must(t, 0); r.stdout != modelsHelp {
			t.Fatalf("%v: %q", args, r.stdout)
		}
	}
	if r := h.wm("", "help").must(t, 0); !strings.Contains(r.stdout, "models     the models your AI keys can use") {
		t.Fatalf("%q", r.stdout)
	}
}

// A machine made since models were chosen says what each agent started on: wm apps ls shows it in a column of its own,
// and an agent running on another model than the one chosen now says to restart it.
func TestAppsSayTheModelEachAgentStartedOn(t *testing.T) {
	h := newHarness(t)
	box := h.api.AddMachine("box", "running")
	h.wm("", "apps", "ls", "box", "-q").must(t, 0)
	codex, yes, on, by := h.api.Apps(box.ID)[4], true, "openai/gpt-6.1-sol", "chosen"
	codex.Running, codex.Suppliers, codex.Model, codex.ModelBy, codex.ModelChanged = &yes, []string{"openai", "openrouter"}, &on, &by, &yes
	h.api.SetApp(box.ID, codex)
	r := h.wm("", "apps", "ls", "box").must(t, 0)
	if !regexp.MustCompile(`(?m)^NAME +APP +KIND +STATE +MODEL +WHERE$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^codex +Codex +agent +on, restart to use the new model +openai/gpt-6\.1-sol +a session of its own in the machine$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^ollama +Ollama +service +off +- +inside the machine$`).MatchString(r.stdout) {
		t.Fatalf("%s", r.stdout)
	}
	codex.KeysChanged = &yes
	h.api.SetApp(box.ID, codex)
	if r := h.wm("", "apps", "ls", "box").must(t, 0); !strings.Contains(r.stdout, "on, restart to use new keys and model") {
		t.Fatalf("%s", r.stdout)
	}
}

// wm models allowed lists an organisation's lists of the models its people may use, and wm models allow sets one at a
// level - the organisation, a team, a role or a person, one alone - or takes it away with --any: an owner's and an
// admin's to keep, refused to a member and in a person's own account.
func TestTheListsOfAllowedModelsAreKept(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("", "models", "allowed").must(t, 1); !strings.Contains(r.stderr, "a person's own account keeps none") {
		t.Fatalf("a person's own account: %q", r.stderr)
	}
	h.inOrg("admin", nil)
	if r := h.wm("", "models", "allowed").must(t, 0); !strings.Contains(r.stdout, "No lists: everyone may use every model") {
		t.Fatalf("none kept: %q", r.stdout)
	}
	r := h.wm("", "models", "allow", "--org", "openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro").must(t, 0)
	if r.stdout != "Everyone in Acme may use openai/gpt-6.1-sol and deepseek/deepseek-v4-pro.\n" {
		t.Fatalf("the organisation's: %q", r.stdout)
	}
	if req, ok := h.api.Last(http.MethodPut, "/v1/models/allowed/organisation"); !ok || string(req.Body) != `{"models":["openai/gpt-6.1-sol","deepseek/deepseek-v4-pro"]}` {
		t.Fatalf("asked %s", req.Body)
	}
	if r := h.wm("", "models", "allow", "--role", "manager", "openai/gpt-6-luna").must(t, 0); r.stdout != "Team managers may use openai/gpt-6-luna.\n" {
		t.Fatalf("a role's: %q", r.stdout)
	}
	h.wm("", "models", "allow", "--team", "prj_00000000000000000000000001", "openai/gpt-6-luna").must(t, 0)
	if _, ok := h.api.Last(http.MethodPut, "/v1/models/allowed/teams/prj_00000000000000000000000001"); !ok {
		t.Fatal("the team's was not set")
	}
	r = h.wm("", "models", "allowed").must(t, 0)
	if !regexp.MustCompile(`(?m)^LIST +FOR +MODELS$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^organisation +Acme +openai/gpt-6\.1-sol, deepseek/deepseek-v4-pro$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^role +Team managers +openai/gpt-6-luna$`).MatchString(r.stdout) {
		t.Fatalf("the lists: %q", r.stdout)
	}
	if r := h.wm("", "models", "allow", "--role", "manager", "--any").must(t, 0); !strings.Contains(r.stdout, "List taken away") {
		t.Fatalf("taken away: %q", r.stdout)
	}
	if _, ok := h.api.Last(http.MethodDelete, "/v1/models/allowed/roles/manager"); !ok {
		t.Fatal("the role's was not taken away")
	}
	for _, args := range [][]string{
		{"models", "allow", "openai/gpt-6.1-sol"},
		{"models", "allow", "--org", "--role", "manager", "openai/gpt-6.1-sol"},
		{"models", "allow", "--org"},
		{"models", "allow", "--org", "--any", "openai/gpt-6.1-sol"},
	} {
		if r := h.wm("", args...).must(t, 2); r.stderr == "" {
			t.Fatalf("%v: said nothing", args)
		}
	}
	// a member keeps none
	h.inOrg("member", []string{"prj_00000000000000000000000001"})
	if r := h.wm("", "models", "allow", "--org", "openai/gpt-6.1-sol").must(t, 1); !strings.Contains(r.stderr, "owners and admins") {
		t.Fatalf("a member: %q", r.stderr)
	}
}

// A person limited to a list is shown its models alone, and which list it is; their agents stand by it, and a model not
// on it is refused.
func TestAPersonOnAListChoosesFromIt(t *testing.T) {
	h := newHarness(t)
	h.inOrg("member", []string{"prj_00000000000000000000000001"})
	h.api.HeldTo = &api.AllowedModels{Object: "allowed_models", Level: "team", ID: "prj_00000000000000000000000001", Name: "Research",
		Models: []string{"deepseek/deepseek-v4-pro", "openai/gpt-6-luna"}}
	r := h.wm("", "models", "--team", "prj_00000000000000000000000001").must(t, 0)
	if strings.Contains(r.stdout, "gpt-6.1-sol") || !strings.Contains(r.stdout, "deepseek/deepseek-v4-pro") ||
		!strings.Contains(r.stderr, "You may use these models alone, as the list of your team, Research says.") {
		t.Fatalf("limited to the team's: %q %q", r.stdout, r.stderr)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/models"); req.Query.Get("project") != "prj_00000000000000000000000001" {
		t.Fatalf("asked %v", req.Query)
	}
	r = h.wm("", "models", "chosen").must(t, 0)
	if !strings.Contains(r.stdout, "You may use deepseek/deepseek-v4-pro and openai/gpt-6-luna, as the list of your team, Research says.") ||
		!regexp.MustCompile(`(?m)^codex +openai/gpt-6-luna \(first allowed\) +openai/gpt-6-luna +as chosen$`).MatchString(r.stdout) ||
		!regexp.MustCompile(`(?m)^aider +deepseek/deepseek-v4-pro \(first allowed\)`).MatchString(r.stdout) {
		t.Fatalf("the agents: %q", r.stdout)
	}
	if r := h.wm("", "models", "use", "codex", "openai/gpt-6.1-sol").must(t, 1); !strings.Contains(r.stderr, "is not one of the models you may use") {
		t.Fatalf("outside the list: %q", r.stderr)
	}
	h.wm("", "models", "use", "codex", "openai/gpt-6-luna").must(t, 0)
	// a list of DeepSeek's models alone: Codex can use none of them
	h.api.HeldTo.Models = []string{"deepseek/deepseek-v4-pro"}
	if r := h.wm("", "models", "chosen").must(t, 0); !regexp.MustCompile(`(?m)^codex +- +- +no model allowed$`).MatchString(r.stdout) {
		t.Fatalf("codex with none of its providers' models allowed: %q", r.stdout)
	}
}
