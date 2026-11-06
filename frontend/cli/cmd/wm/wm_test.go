package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

type result struct {
	code           int
	stdout, stderr string
}

type harness struct {
	t      *testing.T
	api    *fakeapi.Server
	env    map[string]string
	opened []string // the addresses opened in a browser
}

func newHarness(t *testing.T) *harness {
	f := fakeapi.New(t)
	return &harness{t: t, api: f, env: map[string]string{
		api.EnvBaseURL:    f.URL,
		api.EnvAPIKey:     f.Key,
		"XDG_CONFIG_HOME": t.TempDir(),
		"HOME":            t.TempDir(),
	}}
}

// wm runs the command in-process, as it runs from a shell, with stdin given.
func (h *harness) wm(stdin string, args ...string) result {
	h.t.Helper()
	var out, errs bytes.Buffer
	a := &app{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errs, getenv: func(k string) string { return h.env[k] },
		now:  func() time.Time { return time.Date(2026, 10, 23, 12, 5, 0, 0, time.UTC) },
		open: func(u string) error { h.opened = append(h.opened, u); return nil },
		options: []api.Option{api.WithRetryPolicy(api.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
			MaxRetryAfter: time.Second})}}
	code := a.run(context.Background(), args)
	return result{code: code, stdout: out.String(), stderr: errs.String()}
}

func (r result) must(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	return r
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func TestVersion(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"--version"}, {"version"}, {"-v"}} {
		if r := h.wm("", args...).must(t, 0); r.stdout != "wm "+api.Version+"\n" {
			t.Fatalf("%v: %q", args, r.stdout)
		}
	}
}

func TestHelpAndUsageErrors(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("").must(t, 2); !strings.Contains(r.stderr, "Usage: wm <command>") {
		t.Fatalf("no command shows the help: %q", r.stderr)
	}
	if r := h.wm("", "-h").must(t, 0); !strings.Contains(r.stdout, "machines") || !strings.Contains(r.stdout, "WHEELMACHINE_API_KEY") {
		t.Fatalf("help %q", r.stdout)
	}
	if r := h.wm("", "machines", "ls", "-h").must(t, 0); !strings.Contains(r.stdout, "--label key=value") ||
		!strings.Contains(r.stdout, "archived, recovering, failed (failed: the ones that never came up)") {
		t.Fatalf("help %q", r.stdout)
	}
	if r := h.wm("", "help", "exec").must(t, 0); !strings.Contains(r.stdout, "wm exec <machine>") {
		t.Fatalf("help %q", r.stdout)
	}
	// a flag whose zero means "as the words say" shows no default of 0s: --idle-after left out is 5m
	if r := h.wm("", "machines", "create", "-h").must(t, 0); strings.Contains(r.stdout, "(0s)") || !strings.Contains(r.stdout, "5m when left out)\n") ||
		!strings.Contains(r.stdout, "how long to wait for it to run (10m0s)") {
		t.Fatalf("help %q", r.stdout)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"nope"}, `no command "nope"`},
		{[]string{"machines", "nope"}, `no command "nope" in wm machines`},
		{[]string{"machines", "get"}, "say which machine"},
		{[]string{"machines", "get", "a", "b"}, "too many words: b"},
		{[]string{"machines", "ls", "--bogus"}, "no flag -bogus here"},
		{[]string{"machines", "ls", "--state", "asleep"}, `no state "asleep"`},
		{[]string{"machines", "ls", "--limit"}, "the flag -limit needs a value"},
		{[]string{"exec", "web"}, "say what to run"},
		{[]string{"exec", "web", "--root", "ls"}, "put the command after --"},
		{[]string{"usage", "--month", "October"}, "--month is a month like 2026-10"},
		{[]string{"--bogus", "me"}, "no flag --bogus here"},
	} {
		r := h.wm("", c.args...).must(t, 2)
		if !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%v: stderr %q, want %q", c.args, r.stderr, c.want)
		}
	}
	if len(h.api.Requests()) != 0 {
		t.Fatal("a command that is not right calls nothing")
	}
}

func TestMachinesList(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	db := h.api.AddMachine("db", api.StatePaused)

	r := h.wm("", "machines", "ls").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "ID ") || !strings.HasPrefix(rows[1], db.ID) || !strings.HasPrefix(rows[2], web.ID) {
		t.Fatalf("table, newest first:\n%s", r.stdout)
	}
	col := strings.Index(rows[0], "STATE")
	if strings.Index(rows[1], "paused") != col || strings.Index(rows[2], "running") != col {
		t.Fatalf("columns are aligned:\n%s", r.stdout)
	}
	if !strings.Contains(rows[2], "2 CPU, 4 GB") || !strings.Contains(rows[2], "Helsinki, FI") || !strings.HasSuffix(rows[2], "5m") {
		t.Fatalf("row %q", rows[2])
	}

	var got []api.Machine
	r = h.wm("", "machines", "ls", "--json").must(t, 0)
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got) != 2 || got[0].ID != db.ID || got[1].Name != "web" {
		t.Fatalf("json %v: %s", err, r.stdout)
	}
	if r := h.wm("", "machines", "ls", "-q").must(t, 0); r.stdout != db.ID+"\n"+web.ID+"\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	if r := h.wm("", "machines", "ls", "--state", "paused", "--quiet").must(t, 0); r.stdout != db.ID+"\n" {
		t.Fatalf("state %q", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/machines"); req.Query.Get("state") != "paused" || req.Query.Get("limit") != "200" {
		t.Fatalf("query %v", req.Query)
	}
	if r := h.wm("", "machines", "ls", "--limit", "1", "-q").must(t, 0); r.stdout != db.ID+"\n" {
		t.Fatalf("limit %q", r.stdout)
	}
	if r := h.wm("", "machines", "ls", "--json", "--label", "env=prod").must(t, 0); strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("no machine has the label: %q", r.stdout)
	}
}

func TestAMachinePausedAtALimitSaysWhy(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StatePaused)
	if r := h.wm("", "machines", "get", "web").must(t, 0); strings.Contains(r.stdout, "paused:") {
		t.Fatalf("a reason with none given:\n%s", r.stdout)
	}
	why := "Your limit for this month is reached. Your machines run again on 1 December, or once you raise it."
	h.api.SetPauseReason(web.ID, &api.PauseReason{Why: why, Limit: &api.LimitHit{Scope: "account", Measure: "month_usd"}})
	if r := h.wm("", "machines", "get", "web").must(t, 0); !strings.Contains(r.stdout, "paused:") || !strings.Contains(r.stdout, why) {
		t.Fatalf("no reason:\n%s", r.stdout)
	}
}

func TestMachinesAreNamedByIDOrName(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	r := h.wm("", "machines", "get", "web").must(t, 0)
	if !strings.Contains(r.stdout, "id:") || !strings.Contains(r.stdout, web.ID) || !strings.Contains(r.stdout, "files encrypted:") {
		t.Fatalf("detail:\n%s", r.stdout)
	}
	for _, args := range [][]string{{"machines", "get", "--json", "web"}, {"--json", "machines", "get", "web"}, {"machines", "get", "web", "--json"}} {
		var m api.Machine
		if r := h.wm("", args...).must(t, 0); json.Unmarshal([]byte(r.stdout), &m) != nil || m.ID != web.ID {
			t.Fatalf("%v: %q", args, r.stdout)
		}
	}
	if r := h.wm("", "machines", "get", "nope").must(t, 1); !strings.Contains(r.stderr, `no machine has the id or name "nope"`) {
		t.Fatalf("stderr %q", r.stderr)
	}
	r = h.wm("", "machines", "get", "m_00000000000000000000000099").must(t, 1)
	if !strings.Contains(r.stderr, "wm: no such machine (not_found, 404, request req_") {
		t.Fatalf("stderr %q", r.stderr)
	}
	h.api.AddMachine("web", api.StateRunning)
	if r := h.wm("", "machines", "get", "web").must(t, 1); !strings.Contains(r.stderr, "more than one machine is named") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestExecPrintsTheOutputAsItCameAndExitsWithItsCode(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	var asked api.ExecRequest
	h.api.Exec = func(_ string, req api.ExecRequest) api.ExecResult {
		asked = req
		return api.ExecResult{ExitCode: 3, Stdout: "out\x00\xffbin", Stderr: "err\n"}
	}
	r := h.wm("", "exec", "web", "--", "sh", "-c", "echo out; exit 3").must(t, 3)
	if r.stdout != "out\x00\xffbin" || r.stderr != "err\n" {
		t.Fatalf("stdout %q stderr %q: the bytes as they came", r.stdout, r.stderr)
	}
	if strings.Join(asked.Command, "|") != "sh|-c|echo out; exit 3" || !asked.BinaryOutput || asked.User != "" {
		t.Fatalf("asked %+v", asked)
	}
	h.api.Exec = func(_ string, req api.ExecRequest) api.ExecResult {
		asked = req
		return api.ExecResult{Stdout: "uid=0(root)\n"}
	}
	r = h.wm("", "exec", "--cwd", "/srv", web.ID, "--root", "--env", "A=1", "--", "id", "-u").must(t, 0)
	if r.stdout != "uid=0(root)\n" || asked.User != "root" || asked.Cwd != "/srv" || asked.Env["A"] != "1" || strings.Join(asked.Command, " ") != "id -u" {
		t.Fatalf("stdout %q, asked %+v", r.stdout, asked)
	}
	h.wm("", "exec", "web", "uname", "-a").must(t, 0)
	if strings.Join(asked.Command, " ") != "uname -a" {
		t.Fatalf("without --, the words after the machine are the command: %+v", asked)
	}
	h.api.Exec = func(string, api.ExecRequest) api.ExecResult { return api.ExecResult{ExitCode: 300} }
	h.wm("", "exec", "web", "--", "x").must(t, 255)
}

func TestExecAsJSON(t *testing.T) {
	h := newHarness(t)
	h.api.AddMachine("web", api.StateRunning)
	h.api.Exec = func(string, api.ExecRequest) api.ExecResult {
		return api.ExecResult{ExitCode: 1, Stdout: "a\n", Stderr: "b\n", DurationMS: 7}
	}
	r := h.wm("", "exec", "web", "--json", "--", "false").must(t, 1)
	var res api.ExecResult
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.Stdout != "a\n" || res.Stderr != "b\n" || res.ExitCode != 1 {
		t.Fatalf("json %v %q", err, r.stdout)
	}
}

func TestShRunsALineOfShell(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	r := h.wm("", "sh", "web", "echo hi | wc -c").must(t, 0)
	if r.stdout != "echo hi | wc -c\n" {
		t.Fatalf("stdout %q", r.stdout)
	}
	req, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/exec")
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil || body["shell"] != "echo hi | wc -c" || body["command"] != nil {
		t.Fatalf("body %s", req.Body)
	}
}

func TestCopyToAndFromAMachine(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	dir := t.TempDir()
	local := filepath.Join(dir, "notes.txt")
	content := bytes.Repeat([]byte("line of notes\n"), 5000)
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}
	h.wm("", "cp", local, "web:work/docs/").must(t, 0)
	if got, ok := h.api.File(web.ID, "work/docs/notes.txt"); !ok || !bytes.Equal(got, content) {
		t.Fatalf("uploaded %d bytes, %v", len(got), ok)
	}
	req, _ := h.api.Last(http.MethodPut, "/v1/machines/"+web.ID+"/files")
	if req.Header.Get("Content-Length") != "70000" || req.Query.Get("path") != "work/docs/notes.txt" {
		t.Fatalf("sent %v %v", req.Header, req.Query)
	}

	out := filepath.Join(dir, "back.txt")
	h.wm("", "cp", web.ID+":work/docs/notes.txt", out).must(t, 0)
	if got, _ := os.ReadFile(out); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %d bytes", len(got))
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	h.wm("", "cp", "web:work/docs/notes.txt", sub).must(t, 0)
	if got, _ := os.ReadFile(filepath.Join(sub, "notes.txt")); !bytes.Equal(got, content) {
		t.Fatal("a folder keeps the file's name")
	}

	h.wm("piped in\n", "cp", "-", "web:home/in.txt").must(t, 0)
	if got, _ := h.api.File(web.ID, "home/in.txt"); string(got) != "piped in\n" {
		t.Fatalf("stdin gave %q", got)
	}
	if r := h.wm("", "cp", "web:home/in.txt", "-").must(t, 0); r.stdout != "piped in\n" {
		t.Fatalf("stdout %q", r.stdout)
	}

	missing := filepath.Join(dir, "missing.txt")
	if r := h.wm("", "cp", "web:home/none.txt", missing).must(t, 1); !strings.Contains(r.stderr, "no such file in the machine") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("a failed copy leaves no file behind")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Fatalf("files left: %v", entries)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"cp", local, filepath.Join(dir, "x")}, "one side is a machine"},
		{[]string{"cp", "web:work/a", "web:work/b"}, "one side is a file on this computer"},
		{[]string{"cp", local, web.ID + ":/etc/passwd"}, "a path in a machine begins home/ or work/"},
		{[]string{"cp", "web:work/", local}, "name a file in the machine, not a folder"},
		{[]string{"cp", dir, "web:work/"}, "is a folder"},
		{[]string{"cp", "-", "web:work/"}, "name the file in the machine"},
	} {
		if r := h.wm("", c.args...).must(t, 2); !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%v: %q, want %q", c.args, r.stderr, c.want)
		}
	}
}

func TestRemoteWords(t *testing.T) {
	for word, want := range map[string]string{
		"m_00000000000000000000000001:work/a": "m_00000000000000000000000001 work/a",
		"web:work/a":                          "web work/a",
		"web:home":                            "web home",
		"web:etc/a":                           "",
		"./web:work/a":                        "",
		`C:\Users\a.txt`:                      "",
		"C:work/a":                            "",
		"notes.txt":                           "",
		":work/a":                             "",
	} {
		m, p, ok := remote(word)
		got := ""
		if ok {
			got = m + " " + p
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", word, got, want)
		}
	}
}

func TestCreateWaitsForAMachineThatIsStillStarting(t *testing.T) {
	h := newHarness(t)
	h.api.Slow = true
	r := h.wm("", "machines", "create", "--name", "big", "--label", "team=ml", "--region", "helsinki").must(t, 0)
	if rows := lines(r.stdout); len(rows) != 2 || !strings.Contains(rows[1], "running") || !strings.Contains(rows[1], "big") {
		t.Fatalf("output:\n%s", r.stdout)
	}
	req, _ := h.api.Last(http.MethodPost, "/v1/machines")
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if body["wait_s"] != 600.0 || body["region"] != "helsinki" || body["labels"].(map[string]any)["team"] != "ml" {
		t.Fatalf("body %v", body)
	}
	if req.Header.Get("Idempotency-Key") == "" {
		t.Fatal("a create carries an Idempotency-Key")
	}
	waited := 0
	for _, rq := range h.api.Requests() {
		if strings.HasSuffix(rq.Path, "/wait") && rq.Query.Get("state") == "running" {
			waited++
		}
	}
	if waited != 1 {
		t.Fatalf("waited %d times", waited)
	}

	r = h.wm("", "machines", "create", "--no-wait", "--json").must(t, 0)
	var m api.Machine
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil || m.State != api.StateStarting {
		t.Fatalf("--no-wait answers at once: %v %q", err, r.stdout)
	}
	req, _ = h.api.Last(http.MethodPost, "/v1/machines")
	if !strings.Contains(string(req.Body), `"wait_s":0`) {
		t.Fatalf("body %s", req.Body)
	}
}

// A machine that never comes up is said at once, with why - never waited on to the end - and `get` shows why.
func TestCreateSaysAtOnceWhenAMachineNeverCameUp(t *testing.T) {
	h := newHarness(t)
	h.api.Slow, h.api.Fail = true, "no GPU came free in 15 minutes; nothing is charged for it"
	r := h.wm("", "machines", "create", "--name", "big", "--timeout", "1m")
	if r.code == 0 || !strings.Contains(r.stderr, "the machine failed: no GPU came free in 15 minutes; nothing is charged for it") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	if rows := lines(r.stdout); len(rows) != 2 || !strings.Contains(rows[1], "failed") {
		t.Fatalf("the machine printed, so its id is known:\n%s", r.stdout)
	}
	id := strings.Fields(lines(r.stdout)[1])[0]
	got := h.wm("", "machines", "get", id).must(t, 0)
	if !strings.Contains(got.stdout, "failed") || !strings.Contains(got.stdout, "no GPU came free in 15 minutes; nothing is charged for it") {
		t.Fatalf("get shows why:\n%s", got.stdout)
	}
	// and the machines that failed are listed by their state
	if ls := h.wm("", "machines", "ls", "--state", "failed", "-q").must(t, 0); ls.stdout != id+"\n" {
		t.Fatalf("ls --state failed %q", ls.stdout)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/machines"); req.Query.Get("state") != "failed" {
		t.Fatalf("query %v", req.Query)
	}
}

func TestAMachinePausesWhenIdleUnlessItIsMadeNotTo(t *testing.T) {
	h := newHarness(t)
	asked := func() map[string]any {
		req, _ := h.api.Last(http.MethodPost, "/v1/machines")
		var body map[string]any
		_ = json.Unmarshal(req.Body, &body)
		return body
	}
	r := h.wm("", "machines", "create", "--name", "job").must(t, 0)
	if _, said := asked()["sleep_when_idle"]; said {
		t.Fatalf("said what was not asked: %v", asked())
	}
	if r = h.wm("", "machines", "get", "job").must(t, 0); !strings.Contains(r.stdout, "after 5m 00s idle") {
		t.Fatalf("detail:\n%s", r.stdout)
	}
	h.wm("", "machines", "create", "--name", "long", "--idle-after", "1h").must(t, 0)
	if body := asked(); body["idle_after_s"] != 3600.0 || body["sleep_when_idle"] != nil {
		t.Fatalf("body %v", body)
	}
	h.wm("", "machines", "create", "--name", "on", "--no-sleep").must(t, 0)
	if body := asked(); body["sleep_when_idle"] != false || body["idle_after_s"] != nil {
		t.Fatalf("body %v", body)
	}
	if r = h.wm("", "machines", "get", "on").must(t, 0); !strings.Contains(r.stdout, "never by itself") {
		t.Fatalf("detail:\n%s", r.stdout)
	}
	for _, args := range [][]string{{"--no-sleep", "--idle-after", "10m"}, {"--idle-after", "30s"}, {"--idle-after", "25h"}, {"--idle-after", "90500ms"}} {
		if r := h.wm("", append([]string{"machines", "create"}, args...)...).must(t, 2); !strings.Contains(r.stderr, "--idle-after") {
			t.Fatalf("%v: %q", args, r.stderr)
		}
	}
}

func TestAMachineIsMadeAtTheSizeAskedFor(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "machines", "create", "--name", "big", "--cpus", "8").must(t, 0)
	req, _ := h.api.Last(http.MethodPost, "/v1/machines")
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if body["cpus"] != 8.0 {
		t.Fatalf("body %v", body)
	}
	if !strings.Contains(r.stdout, "8 CPU, 16 GB") {
		t.Fatalf("output:\n%s", r.stdout)
	}
	if r = h.wm("", "machines", "get", "big").must(t, 0); !strings.Contains(r.stdout, "8 CPU, 16 GB memory") {
		t.Fatalf("detail:\n%s", r.stdout)
	}
	h.wm("", "machines", "create", "--name", "small").must(t, 0)
	req, _ = h.api.Last(http.MethodPost, "/v1/machines")
	if strings.Contains(string(req.Body), "cpus") {
		t.Fatalf("said what was not asked: %s", req.Body)
	}
	before := len(h.api.Requests())
	for _, args := range [][]string{{"--cpus", "3"}, {"--cpus", "32"}, {"--cpus", "4", "--gpu", "gpu-h100"}} {
		if r := h.wm("", append([]string{"machines", "create"}, args...)...).must(t, 2); !strings.Contains(r.stderr, "--cpus") {
			t.Fatalf("%v: %q", args, r.stderr)
		}
	}
	// 16 is not offered yet, and is said so in the API's own words
	if r := h.wm("", "machines", "create", "--cpus", "16").must(t, 2); !strings.Contains(r.stderr, "16 CPUs are not offered yet: choose 2, 4 or 8") {
		t.Fatalf("16 CPUs: %q", r.stderr)
	}
	if len(h.api.Requests()) != before {
		t.Fatalf("a size refused was asked of the API")
	}
	if r := h.wm("", "machines", "create", "-h").must(t, 0); !strings.Contains(strings.Join(strings.Fields(r.stdout), " "), "its CPUs: 2, 4 or 8") {
		t.Fatalf("help:\n%s", r.stdout)
	}
}

func TestCreateIsTriedAgainWithTheSameIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	var refused atomic.Bool
	h.api.Refuse = func(r *http.Request) *fakeapi.Refusal {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/machines" && refused.CompareAndSwap(false, true) {
			return &fakeapi.Refusal{Status: 503, Code: "unavailable", Detail: "not available just now"}
		}
		return nil
	}
	r := h.wm("", "machines", "create", "-q").must(t, 0)
	var keys []string
	for _, rq := range h.api.Requests() {
		if rq.Method == http.MethodPost {
			keys = append(keys, rq.Header.Get("Idempotency-Key"))
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys %q", keys)
	}
	if r := h.wm("", "machines", "ls", "-q").must(t, 0); r.stdout == "" || len(lines(r.stdout)) != 1 {
		t.Fatalf("one machine made: %q", r.stdout)
	}
	if !api.IsMachineID(strings.TrimSpace(r.stdout)) {
		t.Fatalf("quiet prints the new id: %q", r.stdout)
	}
}

func TestMachineActions(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	db := h.api.AddMachine("db", api.StateRunning)
	r := h.wm("", "machines", "pause", "web", "db").must(t, 0)
	if rows := lines(r.stdout); len(rows) != 3 || !strings.Contains(rows[1], "paused") || !strings.Contains(rows[2], "paused") {
		t.Fatalf("pause:\n%s", r.stdout)
	}
	for _, c := range []struct{ action, state string }{{"stop", api.StateStopped}, {"archive", api.StateArchived}, {"start", api.StateRunning}} {
		var m api.Machine
		r := h.wm("", "machines", c.action, "web", "--json").must(t, 0)
		if err := json.Unmarshal([]byte(r.stdout), &m); err != nil || m.State != c.state {
			t.Fatalf("%s: %v %q", c.action, err, r.stdout)
		}
	}
	h.wm("", "machines", "pause", "db").must(t, 0)
	if r := h.wm("", "machines", "stop", "db").must(t, 0); !strings.Contains(r.stdout, "stopped") {
		t.Fatalf("stop %q", r.stdout)
	}
	if r := h.wm("", "machines", "pause", "db").must(t, 1); !strings.Contains(r.stderr, "(conflict, 409, request") {
		t.Fatalf("pause a stopped machine: %q", r.stderr)
	}

	r = h.wm("", "machines", "fork", "web", "--name", "web-2", "-q").must(t, 0)
	forked, ok := h.api.Machine(strings.TrimSpace(r.stdout))
	if !ok || forked.Name != "web-2" {
		t.Fatalf("fork %q", r.stdout)
	}
	// (copy, a fork's name before it was one, still forks, by the API's own fork route)
	r = h.wm("", "machines", "copy", "web", "--name", "web-3", "-q").must(t, 0)
	if forked, ok := h.api.Machine(strings.TrimSpace(r.stdout)); !ok || forked.Name != "web-3" ||
		!slices.ContainsFunc(h.api.Requests(), func(q fakeapi.Request) bool { return strings.HasSuffix(q.Path, "/fork") }) ||
		slices.ContainsFunc(h.api.Requests(), func(q fakeapi.Request) bool { return strings.HasSuffix(q.Path, "/copy") }) {
		t.Fatalf("copy %q", r.stdout)
	}
	if r := h.wm("", "machines", "-h").must(t, 0); !strings.Contains(r.stdout, "fork <machine>") || strings.Contains(r.stdout, "copy") {
		t.Fatalf("machines help:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "backup", "web", "--note", "before upgrade").must(t, 0); !strings.HasPrefix(r.stdout, "BACKUP ") ||
		!strings.Contains(r.stdout, web.ID) || !strings.Contains(r.stdout, "before upgrade") {
		t.Fatalf("backup %q", r.stdout)
	}
	r = h.wm("", "machines", "label", "web", "env=prod", "team=ml").must(t, 0)
	if !strings.Contains(r.stdout, "env=prod, team=ml") {
		t.Fatalf("label %q", r.stdout)
	}
	h.wm("", "machines", "label", "web", "team-").must(t, 0)
	if m, _ := h.api.Machine(web.ID); len(m.Labels) != 1 || m.Labels["env"] != "prod" {
		t.Fatalf("labels %v", m.Labels)
	}
	h.wm("", "machines", "rename", web.ID, "front").must(t, 0)
	if m, _ := h.api.Machine(web.ID); m.Name != "front" {
		t.Fatalf("name %q", m.Name)
	}
	if r := h.wm("", "machines", "wait", "front", "--state", "running", "-q").must(t, 0); strings.TrimSpace(r.stdout) != web.ID {
		t.Fatalf("wait %q", r.stdout)
	}
	if r := h.wm("", "machines", "wait", "front", "--state", "paused", "--timeout", "1ms").must(t, 1); !strings.Contains(r.stderr, "the machine is running, not paused") {
		t.Fatalf("wait %q", r.stderr)
	}
	_ = db
}

// A backup keeps a machine's files as they were then. Backups are listed newest first - a machine's, or every machine's
// - and a fork from one starts from its files, at the size asked or the original's; a deleted machine's go with it.
// --cpus is for a fork from a backup alone, and what the API refuses of one is said in its words.
func TestBackupsAndForksFromThem(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	db := h.api.AddMachine("db", api.StateRunning)
	h.api.SetFile(web.ID, "work/app.conf", []byte("v1"))
	first := strings.TrimSpace(h.wm("", "machines", "backup", "web", "--note", "before the upgrade", "-q").must(t, 0).stdout)
	h.api.SetFile(web.ID, "work/app.conf", []byte("v2"))
	second := strings.TrimSpace(h.wm("", "machines", "backup", "web", "-q").must(t, 0).stdout)
	h.wm("", "machines", "backup", "db").must(t, 0)

	r := h.wm("", "machines", "backups", "web").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "BACKUP ") || !strings.HasPrefix(rows[1], second+" ") || !strings.HasPrefix(rows[2], first+" ") ||
		!strings.Contains(rows[2], web.ID) || !strings.Contains(rows[2], "before the upgrade") || !strings.HasSuffix(rows[2], "2026-10-23 12:00 UTC") {
		t.Fatalf("web's backups, newest first:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "backups", "-q").must(t, 0); len(lines(r.stdout)) != 3 || lines(r.stdout)[2] != first {
		t.Fatalf("every backup: %q", r.stdout)
	}
	if sent, _ := h.api.Last(http.MethodGet, "/v1/backups"); sent.Query.Get("limit") != "200" {
		t.Fatalf("every backup asked for %v", sent.Query)
	}
	var list []api.Backup
	if r := h.wm("", "machines", "backups", "db", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 1 ||
		list[0].Machine != db.ID || list[0].Object != "backup" {
		t.Fatalf("db's backups, as JSON: %s", r.stdout)
	}

	// from the first backup at 8 CPUs: its files as they were then
	r = h.wm("", "machines", "fork", "web", "--backup", first, "--cpus", "8", "-q").must(t, 0)
	fork, ok := h.api.Machine(strings.TrimSpace(r.stdout))
	if got, _ := h.api.File(fork.ID, "work/app.conf"); !ok || fork.Name != "web-fork" || fork.CPUs == nil || *fork.CPUs != 8 || string(got) != "v1" {
		t.Fatalf("a fork from a backup at 8 CPU: %+v, its file %q", fork, got)
	}
	if sent, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/fork"); string(sent.Body) != `{"backup":"`+first+`","cpus":8,"wait_s":600}` {
		t.Fatalf("sent %s", sent.Body)
	}
	// from its newest, at the original's size
	r = h.wm("", "machines", "fork", "web", "--backup", "latest", "--name", "web-new").must(t, 0)
	if rows := lines(r.stdout); len(rows) != 2 || !strings.Contains(rows[1], "web-new") || !strings.Contains(rows[1], "2 CPU, 4 GB") {
		t.Fatalf("a fork from the newest backup:\n%s", r.stdout)
	}
	if sent, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/fork"); string(sent.Body) != `{"name":"web-new","backup":"latest","wait_s":600}` {
		t.Fatalf("sent %s", sent.Body)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"machines", "fork", "web", "--cpus", "8"}, "--cpus is only for a fork from a backup (--backup): a fork of the machine as it is now keeps its size"},
		{[]string{"machines", "fork", "web", "--backup", "latest", "--cpus", "3"}, "--cpus is 2, 4 or 8"},
		{[]string{"machines", "fork", "web", "--backup", "latest", "--cpus", "16"}, "16 CPUs are not offered yet: choose 2, 4 or 8"},
		{[]string{"machines", "fork", "web", "--backup", "latest", "--for", "meera@example.com"}, "a fork for a teammate is of the machine as it is now"},
		{[]string{"machines", "backups", "web", "db"}, "too many words: db"},
	} {
		before := len(h.api.Requests())
		if r := h.wm("", c.args...).must(t, 2); !strings.Contains(r.stderr, c.want) || len(h.api.Requests()) != before {
			t.Fatalf("%v: %q", c.args, r.stderr)
		}
	}
	if r := h.wm("", "machines", "fork", "web", "--backup", "20261023T120000Z-ffffffff").must(t, 1); !strings.Contains(r.stderr, "wm: no such backup") {
		t.Fatalf("no such backup: %q", r.stderr)
	}
	h.api.AddMachine("new", api.StateRunning)
	if r := h.wm("", "machines", "fork", "new", "--backup", "latest").must(t, 1); !strings.Contains(r.stderr, "wm: no backup of it yet: back it up first") {
		t.Fatalf("none yet: %q", r.stderr)
	}

	h.wm("", "machines", "rm", "--yes", "db").must(t, 0)
	if r := h.wm("", "machines", "backups", "-q").must(t, 0); r.stdout != second+"\n"+first+"\n" {
		t.Fatalf("a deleted machine's backups go with it: %q", r.stdout)
	}
	// (the help as it reads, its lines wrapped where they fall)
	read := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for args, want := range map[string]string{
		"machines -h":         "backups [<machine>] list backups, newest first: a machine's, or every one you reach",
		"machines backup -h":  "Make a new machine from the backup later with wm machines fork <machine> --backup <backup>, at its size or another (--cpus).",
		"machines backups -h": "Usage: wm machines backups [<machine>]",
		"machines fork -h":    "--cpus int with --backup: its CPUs, 2, 4 or 8",
		"machines rm -h":      "Delete machines, all their files and their backups. This cannot be undone.",
	} {
		if r := h.wm("", strings.Fields(args)...).must(t, 0); !strings.Contains(read(r.stdout), want) {
			t.Fatalf("wm %s:\n%s", args, r.stdout)
		}
	}
}

// A member's fork from a backup waits for an OK as every member's fork does, and the request says which backup it is to
// be made from, and at what size.
func TestAMembersForkFromABackupSaysWhichAndItsSize(t *testing.T) {
	h := newHarness(t)
	h.inOrg(api.RoleMember, []string{h.api.Project()})
	h.api.AddMachine("web", api.StateRunning)
	h.api.AddMachine("db", api.StateRunning)
	web := strings.TrimSpace(h.wm("", "machines", "backup", "web", "-q").must(t, 0).stdout)
	db := strings.TrimSpace(h.wm("", "machines", "backup", "db", "-q").must(t, 0).stdout)
	r := h.wm("", "machines", "fork", "web", "--backup", "latest", "--cpus", "8").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 5 || !strings.Contains(rows[1], "Waiting for Ada Lovelace") || rows[2] != "from the backup "+web+", at 8 CPU, 16 GB" ||
		!strings.HasPrefix(rows[3], "made with a key, it waits for your own OK") || !strings.HasPrefix(rows[4], "the fork waits for the OK of a full manager") {
		t.Fatalf("a member's fork from a backup:\n%s", r.stdout)
	}
	if q := h.api.Asks[len(h.api.Asks)-1]; q.Backup == nil || *q.Backup != web || q.CPUs == nil || *q.CPUs != 8 {
		t.Fatalf("the request: %+v", q)
	}
	var q api.Request
	if r := h.wm("", "machines", "fork", "db", "--backup", "latest", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &q) != nil ||
		q.Object != "request" || q.Backup == nil || *q.Backup != db || q.CPUs != nil {
		t.Fatalf("at the original's size, as JSON: %s", r.stdout)
	}
	if r := h.wm("", "machines", "fork", "db").must(t, 0); lines(r.stdout)[2] != "from the backup "+db+", at the original's size" {
		t.Fatalf("asked again while it waits, the one there is:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "ls", "-q").must(t, 0); len(lines(r.stdout)) != 2 {
		t.Fatalf("a fork made while it waits: %q", r.stdout)
	}
}

func TestRemoveAsksFirst(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	if r := h.wm("", "machines", "rm", "web").must(t, 2); !strings.Contains(r.stderr, "add --yes") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if _, ok := h.api.Machine(web.ID); !ok {
		t.Fatal("nothing is deleted without a yes")
	}
	if r := h.wm("", "machines", "rm", "--yes", "web").must(t, 0); r.stdout != web.ID+"\n" {
		t.Fatalf("stdout %q", r.stdout)
	}
	if _, ok := h.api.Machine(web.ID); ok {
		t.Fatal("deleted")
	}
}

func TestLoginKeepsTheKeyForThisUserOnly(t *testing.T) {
	h := newHarness(t)
	key := h.env[api.EnvAPIKey]
	delete(h.env, api.EnvAPIKey)
	if r := h.wm("", "me").must(t, 1); !strings.Contains(r.stderr, "no API key: run `wm login`") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if r := h.wm("ak_live_"+strings.Repeat("x", 40)+"\n", "login", "--key").must(t, 1); !strings.Contains(r.stderr, "this key is not known") {
		t.Fatalf("a key that does not work is not kept: %q", r.stderr)
	}
	path := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine", "config.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("nothing kept for a key that does not work")
	}
	h.wm("hello\n", "login", "--key").must(t, 2)
	h.wm(key+"\n", "login", "--key", "--scope", "machines:read").must(t, 2)

	r := h.wm(key+"\n", "login", "--key").must(t, 0)
	if !strings.Contains(r.stdout, "Signed in to account acc_test") || strings.Contains(r.stdout+r.stderr, key) {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), key) {
		t.Fatal("the key is kept")
	}
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout, "acc_test") || !strings.Contains(r.stdout, "balance:") {
		t.Fatalf("me with the kept key:\n%s", r.stdout)
	}
	if r := h.wm("", "logout").must(t, 0); !strings.Contains(r.stdout, "Signed out") {
		t.Fatalf("logout %q", r.stdout)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("logout forgets the key")
	}
	h.wm("", "me").must(t, 1)
	if r := h.wm("", "logout").must(t, 0); !strings.Contains(r.stdout, "No key is kept") {
		t.Fatalf("logout twice %q", r.stdout)
	}
}

func TestAWrongKeySaysWhatToDo(t *testing.T) {
	h := newHarness(t)
	h.env[api.EnvAPIKey] = "ak_live_" + strings.Repeat("q", 40)
	r := h.wm("", "me").must(t, 1)
	if !strings.Contains(r.stderr, "wm: this key is not known (unauthorized, 401, request req_1)") || !strings.Contains(r.stderr, "WHEELMACHINE_API_KEY") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if strings.Contains(r.stderr, h.env[api.EnvAPIKey]) {
		t.Fatal("the key is never printed")
	}
}

// A key with full access needs the code the API e-mails: off a terminal, wm has it sent and says to run again with
// --code; given right, the key is made; given wrong, it says so - and a key that does less needs none.
func TestAKeyWithFullAccessNeedsTheCodeWeEmail(t *testing.T) {
	h := newHarness(t)
	h.api.ConfirmCode = "482913"
	r := h.wm("", "keys", "create", "deploy", "--scope", "all").must(t, 2)
	if !strings.Contains(r.stderr, "a…@example.com") || !strings.Contains(r.stderr, "--code") || h.api.ConfirmsSent != 1 {
		t.Fatalf("no code asked for: %q (sent %d)", r.stderr, h.api.ConfirmsSent)
	}
	if r := h.wm("", "keys", "create", "deploy", "--scope", "all", "--code", "111111").must(t, 1); !strings.Contains(r.stderr, "not right") {
		t.Fatalf("a wrong code: %q", r.stderr)
	}
	if r := h.wm("", "keys", "create", "deploy", "--scope", "all", "--code", "482913", "-q").must(t, 0); !strings.HasPrefix(r.stdout, "ak_live_") {
		t.Fatalf("with the code: %q", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodPost, "/v1/keys"); req.Header.Get("X-Confirm-Code") != "482913" {
		t.Fatalf("the code was not carried: %v", req.Header)
	}
	h.wm("", "keys", "create", "ci", "--scope", "machines:read").must(t, 0)
	if h.api.ConfirmsSent != 1 {
		t.Fatalf("a code sent for a key that does less: %d", h.api.ConfirmsSent)
	}
}

// A key that makes keys, without full access, needs the code too: it is asked for, and carried, for its own step.
func TestAKeyThatMakesKeysNeedsTheCodeWeEmail(t *testing.T) {
	h := newHarness(t)
	h.api.ConfirmCode = "305114"
	if r := h.wm("", "keys", "create", "rotator", "--scope", "keys:write").must(t, 2); !strings.Contains(r.stderr, "--code") || h.api.ConfirmsSent != 1 {
		t.Fatalf("no code asked for a key that makes keys: %q (sent %d)", r.stderr, h.api.ConfirmsSent)
	}
	if req, _ := h.api.Last(http.MethodPost, "/v1/confirm-codes"); !strings.Contains(string(req.Body), `"key_making_key"`) {
		t.Fatalf("the code asked for: %s", req.Body)
	}
	if r := h.wm("", "keys", "create", "rotator", "--scope", "keys:write", "--code", "305114", "-q").must(t, 0); !strings.HasPrefix(r.stdout, "ak_live_") {
		t.Fatalf("with the code: %q", r.stdout)
	}
}

func TestKeys(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "keys", "create", "ci", "--scope", "machines:read,machines:exec", "--expires", "30d", "--rate", "60").must(t, 0)
	secret := ""
	for _, l := range lines(r.stdout) {
		if v, ok := strings.CutPrefix(l, "secret:"); ok {
			secret = strings.TrimSpace(v)
		}
	}
	if !strings.HasPrefix(secret, "ak_live_zzzz") || !strings.Contains(r.stderr, "store the secret now") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	// its end is to come: said as how long until it, never as a time gone by
	if !strings.Contains(r.stdout, " UTC (in 30d)\n") || strings.Contains(r.stdout, "ago") {
		t.Fatalf("the end time: %q", r.stdout)
	}
	req, _ := h.api.Last(http.MethodPost, "/v1/keys")
	var body struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
		Rate      int      `json:"rate_per_minute"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || strings.Join(body.Scopes, " ") != "cloud:machines:read cloud:machines:exec" ||
		body.ExpiresAt == "" || body.Rate != 60 || req.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("body %s", req.Body)
	}
	if r := h.wm("", "keys", "create", "deploy", "--scope", "all", "-q").must(t, 0); !strings.HasPrefix(r.stdout, "ak_live_") || len(lines(r.stdout)) != 1 {
		t.Fatalf("quiet prints the secret alone: %q", r.stdout)
	}
	h.wm("", "keys", "create", "x").must(t, 2)
	h.wm("", "keys", "create", "x", "--scope", "machines:fly").must(t, 2)
	r = h.wm("", "keys", "ls").must(t, 0)
	if rows := lines(r.stdout); len(rows) != 4 || !strings.Contains(r.stdout, "ci") || strings.Contains(r.stdout, secret) {
		t.Fatalf("ls (no secret ever):\n%s", r.stdout)
	}
	h.wm("", "keys", "revoke", "ci").must(t, 2)
	if r := h.wm("", "keys", "revoke", "ci", "--yes").must(t, 0); !strings.HasPrefix(r.stdout, "key_") {
		t.Fatalf("revoke %q", r.stdout)
	}
	if r := h.wm("", "keys", "ls", "-q").must(t, 0); len(lines(r.stdout)) != 2 {
		t.Fatalf("after revoke %q", r.stdout)
	}
}

func TestProjects(t *testing.T) {
	h := newHarness(t)
	r := h.wm("", "projects", "create", "research", "-q").must(t, 0)
	id := strings.TrimSpace(r.stdout)
	if !api.IsProjectID(id) {
		t.Fatalf("id %q", id)
	}
	h.wm("", "projects", "rename", "research", "lab").must(t, 0)
	if r := h.wm("", "projects", "ls").must(t, 0); !strings.Contains(r.stdout, "lab") || !strings.Contains(r.stdout, "Default") {
		t.Fatalf("ls:\n%s", r.stdout)
	}
	h.wm("", "machines", "create", "--project", "lab", "-q").must(t, 0)
	if r := h.wm("", "projects", "rm", "lab").must(t, 1); !strings.Contains(r.stderr, "project_not_empty") {
		t.Fatalf("stderr %q", r.stderr)
	}
	h.wm("", "projects", "rm", h.api.Project()).must(t, 0)
}

func TestMeUsageGPUsRegions(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout, "can spend:") || !strings.Contains(r.stdout, "$10.50") {
		t.Fatalf("me:\n%s", r.stdout)
	}
	h.api.Usage = api.Usage{TotalUSD: 1.25, Machines: []api.UsageLine{{Machine: "m_00000000000000000000000007", Project: "prj_1", RunningSeconds: 3725, USD: 1.25}}}
	r := h.wm("", "usage", "--month", "2026-09").must(t, 0)
	if !strings.Contains(r.stdout, "1h 02m") || !strings.Contains(r.stdout, "total 2026-09") || !strings.Contains(r.stdout, "$1.25") {
		t.Fatalf("usage:\n%s", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/usage"); req.Query.Get("month") != "2026-09" {
		t.Fatalf("query %v", req.Query)
	}
	if r := h.wm("", "gpus").must(t, 0); !strings.Contains(r.stdout, "H100 80GB") || !strings.Contains(r.stdout, "$2.49/h") {
		t.Fatalf("gpus:\n%s", r.stdout)
	}
	h.api.GPUs.ForSale = false
	if r := h.wm("", "gpus").must(t, 0); !strings.Contains(r.stderr, "not open to every account yet") {
		t.Fatalf("stderr %q", r.stderr)
	}
	var regions []api.Region
	if r := h.wm("", "regions", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &regions) != nil || len(regions) != 2 {
		t.Fatalf("regions %q", r.stdout)
	}
	if r := h.wm("", "regions").must(t, 0); !strings.Contains(r.stdout, "mumbai") || !strings.HasPrefix(r.stdout, "ID") {
		t.Fatalf("regions:\n%s", r.stdout)
	}
}

// wm credit: the credit left, whether credit can be bought, and its latest entries - a machine's running time by its
// day and its name, credit put in with its sign - then every one with --all, the balance alone with -q, and the API's
// own answer with --json. wm me says what is left too.
func TestCredit(t *testing.T) {
	h := newHarness(t)
	trainer, machine := "trainer", "m_00000000000000000000000007"
	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	h.api.Credit = api.Credit{BalanceUSD: 4.8688, Data: []api.CreditEntry{
		{ID: "ce_3", Object: "credit_entry", Kind: api.EntryMachineTime, AmountUSD: -0.1312, Reason: "Running time", Machine: &machine,
			MachineName: &trainer, At: at.Add(time.Minute), UpdatedAt: at.Add(time.Hour)},
		{ID: "ce_2", Object: "credit_entry", Kind: api.EntryStaffCredit, AmountUSD: 0.5, Reason: "Sorry for the outage", At: at, UpdatedAt: at},
		{ID: "ce_1", Object: "credit_entry", Kind: api.EntryWelcome, AmountUSD: 5, Reason: "Free credit to start with", At: at, UpdatedAt: at},
	}}
	r := h.wm("", "credit").must(t, 0)
	for _, want := range []string{"balance:     $4.87\n", "buy credit:  not open just now\n", "WHEN (UTC)", "2026-11-02        Running time",
		"trainer  -$0.1312", "Sorry for the outage", "+$0.5000", "2026-11-02 09:30  Free credit to start with", "+$5.00"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("credit lacks %q:\n%s", want, r.stdout)
		}
	}
	// two a page: the latest, and a word that there are more; then all of them
	if r := h.wm("", "credit", "--limit", "2").must(t, 0); strings.Contains(r.stdout, "Free credit") || !strings.Contains(r.stderr, "--all shows every one") {
		t.Fatalf("a page of two:\n%s\n%s", r.stdout, r.stderr)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/credit"); req.Query.Get("limit") != "2" {
		t.Fatalf("query %v", req.Query)
	}
	if r := h.wm("", "credit", "--limit", "1", "--all").must(t, 0); !strings.Contains(r.stdout, "Free credit") || r.stderr != "" {
		t.Fatalf("all of them:\n%s\n%s", r.stdout, r.stderr)
	}
	if r := h.wm("", "credit", "-q").must(t, 0); r.stdout != "4.868800\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	var w api.Credit
	if r := h.wm("", "credit", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &w) != nil || w.BalanceUSD != 4.8688 || len(w.Data) != 3 ||
		*w.Data[0].MachineName != "trainer" {
		t.Fatalf("json %s", r.stdout)
	}
	if r := h.wm("", "credit", "--limit", "0").must(t, 2); !strings.Contains(r.stderr, "--limit is 1 to 200") {
		t.Fatalf("stderr %q", r.stderr)
	}
	// used up: said, and wm me says it too
	h.api.Credit.BalanceUSD, h.api.Credit.Empty = -0.04, true
	if r := h.wm("", "credit").must(t, 0); !strings.Contains(r.stdout, "state:       used up: machines are paused") {
		t.Fatalf("used up:\n%s", r.stdout)
	}
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout, "credit:") || !strings.Contains(r.stdout, " -$0.0400\n") {
		t.Fatalf("me:\n%s", r.stdout)
	}
}

// wm credit buy: a checkout for one of the amounts started, and the link to pay it at printed - the link alone with
// -q, the API's answer with --json. Another amount is refused before the API is asked; where buying credit is not
// open, the API's words say so. wm credit says how credit is bought once it can be.
func TestCreditBuy(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("", "credit", "buy", "25").must(t, 1); !strings.Contains(r.stderr, "buying credit is not open yet") {
		t.Fatalf("not open: %q", r.stderr)
	}
	h.api.Credit.Buy = api.Buying{Open: true, AmountsUSD: []int64{10, 25, 50, 100}}
	h.api.Credit.Buy.Paddle = &struct {
		Environment string `json:"environment"`
		ClientToken string `json:"client_token"`
	}{"sandbox", "test_madeup0123"}
	if r := h.wm("", "credit").must(t, 0); !strings.Contains(r.stdout, "buy credit:  wm credit buy <10|25|50|100>, or on the dashboard's Billing page\n") {
		t.Fatalf("credit:\n%s", r.stdout)
	}
	link := "https://app.wheelmachine.ai/billing?purchase=cp_01jb2xk3n4p5q6r7s8t9v0w1x2"
	r := h.wm("", "credit", "buy", "$25").must(t, 0)
	if !strings.Contains(r.stdout, "amount:  $25.00\n") || !strings.Contains(r.stdout, "pay at:  "+link+"\n") ||
		!strings.Contains(r.stderr, "open the link in a browser signed in to the dashboard") {
		t.Fatalf("buy:\n%s\n%s", r.stdout, r.stderr)
	}
	if req, _ := h.api.Last(http.MethodPost, "/v1/credit/purchases"); string(req.Body) != `{"amount":25}` || req.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("asked %s %v", req.Body, req.Header)
	}
	// what the buyer agrees to is said before they pay, as by the dashboard's buttons - and only on stderr, so a
	// program reading the link or the answer reads nothing else
	agreed := "by buying credit, you agree to the Terms of Service (https://wheelmachine.ai/terms/) and the Refund Policy " +
		"(https://wheelmachine.ai/refunds/); Paddle.com is the seller of this order (Merchant of Record)"
	if !strings.Contains(r.stderr, agreed) || strings.Contains(r.stdout, "Merchant of Record") {
		t.Fatalf("agreement:\n%s\n%s", r.stdout, r.stderr)
	}
	if r := h.wm("", "credit", "buy", "10", "-q").must(t, 0); !strings.Contains(r.stderr, agreed) {
		t.Fatalf("quiet agreement %q", r.stderr)
	}
	if r := h.wm("", "credit", "buy", "50", "--json").must(t, 0); strings.Contains(r.stdout+r.stderr, "Merchant of Record") {
		t.Fatalf("json carries the words: %s %s", r.stdout, r.stderr)
	}
	if r := h.wm("", "credit", "buy", "10", "-q").must(t, 0); r.stdout != link+"\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	var bought api.CreditPurchase
	if r := h.wm("", "credit", "buy", "100", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &bought) != nil || bought.AmountUSD != 100 ||
		bought.Object != "credit_purchase" || bought.Checkout.TransactionID != nil || bought.Checkout.PriceID != "pri_01jb2xq4c6d8e1f3g5h7j9k100" ||
		len(bought.Checkout.Prices) != 4 || bought.Checkout.CustomData["wheelmachine_purchase"] != bought.ID || bought.Checkout.Environment != "sandbox" {
		t.Fatalf("json %s", r.stdout)
	}
	for _, words := range [][]string{{"credit", "buy"}, {"credit", "buy", "30"}, {"credit", "buy", "ten"}, {"credit", "buy", "10", "25"}} {
		if r := h.wm("", words...).must(t, 2); r.stderr == "" {
			t.Fatalf("%v: nothing said", words)
		}
	}
}

func TestPorts(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	address := func(port int) string {
		return "https://" + strconv.Itoa(port) + "-" + strings.TrimPrefix(web.ID, "m_") + ".run.wheelmachine.ai"
	}
	r := h.wm("", "ports", "open", "web", "8080").must(t, 0)
	if r.stdout != address(8080)+"\n" || !strings.Contains(r.stderr, "the address is private") || !strings.Contains(r.stderr, "wm ports link web 8080") {
		t.Fatalf("stdout %q stderr %q: the address alone, and who reaches it", r.stdout, r.stderr)
	}
	req, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/ports")
	if string(req.Body) != `{"port":8080,"public":false}` || req.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("body %s", req.Body)
	}
	var p api.Port
	r = h.wm("", "ports", "open", web.ID, "3000", "--public", "--json").must(t, 0)
	if err := json.Unmarshal([]byte(r.stdout), &p); err != nil || p.Port != 3000 || !p.Public || p.URL != address(3000) || p.Machine != web.ID {
		t.Fatalf("json %v %q", err, r.stdout)
	}
	if !strings.Contains(r.stderr, "the address is public: anyone can reach it") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if r := h.wm("", "ports", "open", "web", "8080", "--public", "-q").must(t, 0); r.stdout != address(8080)+"\n" || r.stderr != "" {
		t.Fatalf("open again, quietly: stdout %q stderr %q", r.stdout, r.stderr)
	}
	if ports := h.api.Ports(web.ID); len(ports) != 2 || !ports[1].Public {
		t.Fatalf("a port open already is made public as asked: %+v", ports)
	}
	h.wm("", "ports", "open", "web", "3000", "-q").must(t, 0)

	r = h.wm("", "ports", "ls", "web").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "PORT ") || !strings.HasPrefix(rows[1], "3000 ") || !strings.HasPrefix(rows[2], "8080 ") {
		t.Fatalf("table, lowest first:\n%s", r.stdout)
	}
	who, col := strings.Index(rows[0], "ACCESS"), strings.Index(rows[0], "ADDRESS")
	if strings.Index(rows[1], "private") != who || strings.Index(rows[2], "public") != who ||
		strings.Index(rows[1], address(3000)) != col || !strings.HasSuffix(rows[2], address(8080)) {
		t.Fatalf("columns are aligned:\n%s", r.stdout)
	}
	var list []api.Port
	if r := h.wm("", "ports", "ls", "--json", "web").must(t, 0); json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 2 ||
		list[0].Port != 3000 || list[0].Public || list[1].URL != address(8080) {
		t.Fatalf("json %q", r.stdout)
	}
	if r := h.wm("", "ports", "ls", "web", "-q").must(t, 0); r.stdout != "3000\n8080\n" {
		t.Fatalf("quiet %q", r.stdout)
	}

	if r := h.wm("", "ports", "close", "web", "3000").must(t, 0); r.stdout != "3000\n" {
		t.Fatalf("close %q", r.stdout)
	}
	if req, ok := h.api.Last(http.MethodDelete, "/v1/machines/"+web.ID+"/ports/3000"); !ok || len(req.Body) != 0 {
		t.Fatal("closed by its route")
	}
	h.wm("", "ports", "close", "web", "3000").must(t, 0) // one that is not open is closed all the same
	if r := h.wm("", "ports", "ls", "web", "-q").must(t, 0); r.stdout != "8080\n" {
		t.Fatalf("after close %q", r.stdout)
	}

	h.api.AddMachine("db", api.StateRunning)
	if r := h.wm("", "ports", "ls", "db", "--json").must(t, 0); strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("no port open: %q", r.stdout)
	}
	if r := h.wm("", "ports", "ls", "db").must(t, 0); len(lines(r.stdout)) != 1 {
		t.Fatalf("the header alone:\n%s", r.stdout)
	}
	if r := h.wm("", "ports", "open", "m_00000000000000000000000099", "8080").must(t, 1); !strings.Contains(r.stderr, "no such machine (not_found, 404") {
		t.Fatalf("a machine the key cannot see: %q", r.stderr)
	}
	h.wm("", "machines", "rm", "--yes", "web").must(t, 0)
	if ports := h.api.Ports(web.ID); len(ports) != 0 {
		t.Fatalf("a machine's ports close with it: %+v", ports)
	}
}

// linkToken is where a link's secret is; hidden, so that no test ever prints one.
var linkToken = regexp.MustCompile(`/\.wheelmachine/link/[^\s"]*`)

func hideLinks(s string) string { return linkToken.ReplaceAllString(s, "/.wheelmachine/link/[hidden]") }

func TestPortLinks(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	h.wm("", "ports", "open", "web", "8080", "-q").must(t, 0)
	// link runs wm, failing the test (with every link hidden) when it does not exit with the code
	link := func(code int, args ...string) result {
		t.Helper()
		r := h.wm("", append([]string{"ports", "link"}, args...)...)
		result{code: r.code, stdout: hideLinks(r.stdout), stderr: hideLinks(r.stderr)}.must(t, code)
		return r
	}
	sent := func() map[string]any {
		req, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/ports/8080/link")
		if req.Header.Get("Idempotency-Key") != "" {
			t.Fatal("a link is made anew by every call: no Idempotency-Key")
		}
		var body map[string]any
		_ = json.Unmarshal(req.Body, &body)
		return body
	}
	prefix := "https://8080-" + strings.TrimPrefix(web.ID, "m_") + ".run.wheelmachine.ai/.wheelmachine/link/"

	r := link(0, "web", "8080", "--for", "10m", "--path", "/app?tab=logs")
	first := strings.TrimSuffix(r.stdout, "\n")
	if strings.Contains(first, "\n") || !strings.HasPrefix(first, prefix) || len(first) == len(prefix) {
		t.Fatalf("stdout %q: the link alone", hideLinks(r.stdout))
	}
	if !strings.Contains(r.stderr, "opens once, within five minutes (until 12:05:00 UTC)") || !strings.Contains(r.stderr, "for 10m 00s") ||
		strings.Contains(r.stderr, prefix) {
		t.Fatalf("stderr %q", hideLinks(r.stderr))
	}
	if body := sent(); body["access_s"] != 600.0 || body["path"] != "/app?tab=logs" {
		t.Fatalf("body %v", body)
	}

	var l api.PortLink
	r = link(0, "web", "8080", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &l); err != nil || l.Port != 8080 || l.Machine != web.ID || l.AccessS != 1800 ||
		!l.ExpiresAt.Equal(time.Date(2026, 10, 23, 12, 5, 0, 0, time.UTC)) || !strings.HasPrefix(l.URL, prefix) || l.URL == first {
		t.Fatalf("json %v %s: a new link each time", err, hideLinks(r.stdout))
	}
	if !strings.Contains(r.stderr, "for 30m 00s") {
		t.Fatalf("stderr %q", hideLinks(r.stderr))
	}
	if body := sent(); body["access_s"] != 1800.0 || body["path"] != "/" {
		t.Fatalf("body %v", body)
	}
	if r := link(0, "web", "8080", "-q", "--for", "1h"); !strings.HasPrefix(r.stdout, prefix) || r.stderr != "" {
		t.Fatalf("quiet: stdout %q stderr %q", hideLinks(r.stdout), hideLinks(r.stderr))
	}
	if r := link(1, "web", "9000"); !strings.Contains(r.stderr, "port 9000 of this machine is not open (not_found, 404") {
		t.Fatalf("stderr %q", hideLinks(r.stderr))
	}
	for _, path := range []string{"//elsewhere.example/", `/\elsewhere.example`, "/.wheelmachine/link/x"} {
		if r := link(1, "web", "8080", "--path", path); !strings.Contains(r.stderr, "beginning with a single / (invalid_request, 400") {
			t.Fatalf("%s: a link lands on its own address only: %q", path, hideLinks(r.stderr))
		}
	}
	links := 0
	for _, rq := range h.api.Requests() {
		if strings.HasSuffix(rq.Path, "/link") {
			links++
		}
	}
	if links != 7 {
		t.Fatalf("%d links asked for, want 7", links)
	}
}

func TestPortsThatAreNotRight(t *testing.T) {
	h := newHarness(t)
	web := h.api.AddMachine("web", api.StateRunning)
	if r := h.wm("", "ports").must(t, 2); !strings.Contains(r.stderr, "Usage: wm ports <command>") {
		t.Fatalf("no command shows the group's help: %q", r.stderr)
	}
	for _, args := range [][]string{{"ports", "-h"}, {"help", "ports"}} {
		if r := h.wm("", args...).must(t, 0); !strings.Contains(r.stdout, "link <machine> <port>") || !strings.Contains(r.stdout, "run.wheelmachine.ai") {
			t.Fatalf("%v: %q", args, r.stdout)
		}
	}
	if r := h.wm("", "-h").must(t, 0); !strings.Contains(r.stdout, "ports      open a machine's ports") {
		t.Fatalf("help %q", r.stdout)
	}
	if r := h.wm("", "ports", "link", "-h").must(t, 0); !strings.Contains(r.stdout, "--for duration") || !strings.Contains(r.stdout, "--path path") {
		t.Fatalf("help %q", r.stdout)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"ports", "nope"}, `no command "nope" in wm ports`},
		{[]string{"ports", "ls"}, "say which machine"},
		{[]string{"ports", "open", "web"}, "say which machine, and which port"},
		{[]string{"ports", "open", "web", "0"}, `a port is a number from 1 to 65535, not "0"`},
		{[]string{"ports", "open", "web", "65536", "--public"}, `a port is a number from 1 to 65535, not "65536"`},
		{[]string{"ports", "close", "web", "http"}, `a port is a number from 1 to 65535, not "http"`},
		{[]string{"ports", "close", "web", "80", "443"}, "too many words: 443"},
		{[]string{"ports", "link", "web", "8080.5"}, `not "8080.5"`},
		{[]string{"ports", "link", "web", "8080", "--for", "30s"}, "--for is whole seconds from 1m to 60m"},
		{[]string{"ports", "link", "web", "8080", "--for", "61m"}, "--for is whole seconds from 1m to 60m"},
		{[]string{"ports", "link", "web", "8080", "--for", "90500ms"}, "--for is whole seconds from 1m to 60m"},
		{[]string{"ports", "link", "web", "8080", "--path", "app"}, `--path begins with /, not "app"`},
	} {
		if r := h.wm("", c.args...).must(t, 2); !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%v: stderr %q, want %q", c.args, r.stderr, c.want)
		}
	}
	if len(h.api.Requests()) != 0 {
		t.Fatal("a command that is not right calls nothing")
	}

	// the ports the machine itself uses are the API's to refuse
	for _, port := range []string{"5900", "6080", "7070", "9222", "9223", "9224"} {
		r := h.wm("", "ports", "open", "web", port).must(t, 1)
		if !strings.Contains(r.stderr, "port "+port+" is used by the machine itself; open another") || !strings.Contains(r.stderr, "(invalid_request, 400, request req_") {
			t.Fatalf("%s: stderr %q", port, r.stderr)
		}
	}
	for port := 8000; port < 8020; port++ {
		h.wm("", "ports", "open", "web", strconv.Itoa(port), "-q").must(t, 0)
	}
	if r := h.wm("", "ports", "open", "web", "8020").must(t, 1); !strings.Contains(r.stderr, "at most 20 ports open; close one first (limit_reached, 429") {
		t.Fatalf("stderr %q", r.stderr)
	}
	h.wm("", "ports", "open", "web", "8000", "--public", "-q").must(t, 0) // one open already is made public all the same
	if ports := h.api.Ports(web.ID); len(ports) != 20 || !ports[0].Public {
		t.Fatalf("%d open", len(ports))
	}

	h.api.PortsNotOpen = true
	before := len(h.api.Requests())
	for _, args := range [][]string{{"open", "web", "8020"}, {"link", "web", "8000"}} {
		if r := h.wm("", append([]string{"ports"}, args...)...).must(t, 1); !strings.Contains(r.stderr, "(not_open, 503, request req_") {
			t.Fatalf("%v: stderr %q", args, r.stderr)
		}
	}
	sent := 0
	for _, rq := range h.api.Requests()[before:] {
		if rq.Method == http.MethodPost {
			sent++
		}
	}
	if sent != 2 {
		t.Fatalf("%d calls sent for 2: what is not open yet is not asked again", sent)
	}
}

func TestMeterShowsBytesAndSpeed(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := newMeter(&out, "copying", 2048, func() time.Time { return now })
	now = now.Add(time.Second)
	m.add(1024)
	if got := m.line(); got != "copying  1.0 KiB of 2.0 KiB (50%)  1.0 KiB/s" {
		t.Fatalf("line %q", got)
	}
	m.finish()
	if !strings.HasSuffix(out.String(), "\n") || !strings.HasPrefix(out.String(), "\r") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestFormats(t *testing.T) {
	for in, want := range map[float64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 5 << 20: "5.0 MiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{45: "45s", 750: "12m 30s", 3725: "1h 02m", 200000: "2d 7h"} {
		if got := span(in); got != want {
			t.Errorf("span(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "$0.00", 0.0123: "$0.0123", 12.5: "$12.50"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
	if got := cell("a\tb\nc"); got != "a b c" {
		t.Errorf("cell %q", got)
	}
	if got := wrap("one two three four", 9); got != "one two\nthree\nfour" {
		t.Errorf("wrap %q", got)
	}
}

func TestLoginSignsInThroughTheBrowserAndKeepsANewKeyOfItsOwn(t *testing.T) {
	h := newHarness(t)
	delete(h.env, api.EnvAPIKey)
	asks := 0
	h.api.Decide = func(ask int) string {
		asks = ask
		switch ask {
		case 1:
			return "pending"
		case 2:
			// the API not answering for a moment: waited out, as no key was given then
			return "unavailable"
		}
		return "approved"
	}
	r := h.wm("", "login", "--name", "wm on 100% ci-box", "--scope", "machines:read,machines:exec").must(t, 0)
	if !strings.Contains(r.stderr, "BCDF-GHJK") || !strings.Contains(r.stderr, h.api.URL+"/device") || !strings.Contains(r.stderr, "type the code") {
		t.Fatalf("the code and where to enter it are not said: %q", r.stderr)
	}
	// the address opened carries no code: the person types it
	if len(h.opened) != 1 || h.opened[0] != h.api.URL+"/device" {
		t.Fatalf("opened %v", h.opened)
	}
	if asks != 3 || !strings.Contains(r.stdout, "Signed in") || !strings.Contains(r.stdout, `"wm on 100% ci-box"`) ||
		strings.Contains(r.stdout, "%!") || strings.Contains(r.stdout+r.stderr, h.api.Key) {
		t.Fatalf("asked %d times; stdout %q", asks, r.stdout)
	}
	start, _ := h.api.Last("POST", "/v1/login/devices")
	var asked map[string]any
	_ = json.Unmarshal(start.Body, &asked)
	if asked["name"] != "wm on 100% ci-box" || len(asked["scopes"].([]any)) != 2 || start.Header.Get("Authorization") != "" {
		t.Fatalf("the sign-in was asked for as %s with %q", start.Body, start.Header.Get("Authorization"))
	}
	if poll, _ := h.api.Last("POST", "/v1/login/devices/token"); poll.Header.Get("Authorization") != "" || !strings.Contains(string(poll.Body), "wmd_") {
		t.Fatalf("asked for the key with %q", poll.Body)
	}
	path := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine", "config.json")
	if st, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("kept as %v %v", st, err)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), h.api.Key) {
		t.Fatal("the key given is kept")
	}
	h.wm("", "me").must(t, 0)

	// a name of this computer's own when none is given; no browser opened when asked not to
	h.opened = nil
	h.wm("", "login", "--no-browser").must(t, 0)
	start, _ = h.api.Last("POST", "/v1/login/devices")
	if !strings.Contains(string(start.Body), `"name":"wm on `) || len(h.opened) != 0 {
		t.Fatalf("asked as %s, opened %v", start.Body, h.opened)
	}
	h.wm("", "login", "--scope", "everything").must(t, 2)
}

func TestLoginSaysWhenTheSignInIsRefusedRunsOutOrIsNotOpen(t *testing.T) {
	h := newHarness(t)
	delete(h.env, api.EnvAPIKey)
	path := filepath.Join(h.env["XDG_CONFIG_HOME"], "wheelmachine", "config.json")
	for said, words := range map[string]string{"denied": "nothing was given", "expired": "for a new code"} {
		h.api.Decide = func(int) string { return said }
		if r := h.wm("", "login").must(t, 1); !strings.Contains(r.stderr, words) {
			t.Fatalf("%s: %q", said, r.stderr)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: a key was kept", said)
		}
	}
	h.api.LoginsShut = true
	if r := h.wm("", "login").must(t, 1); !strings.Contains(r.stderr, "wm login --key") {
		t.Fatalf("not open: %q", r.stderr)
	}
}

func TestTheBrowserOpensOnlyAWebAddress(t *testing.T) {
	var opened []string
	a := &app{getenv: func(string) string { return "" }, open: func(u string) error { opened = append(opened, u); return nil }}
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "http://app.example/device", "ssh://host", "https://user@host/x",
		"/device", ""} {
		if err := a.openBrowser(bad); err == nil {
			t.Fatalf("%q was opened", bad)
		}
	}
	for _, good := range []string{"https://app.wheelmachine.ai/device?code=BCDF-GHJK", "http://127.0.0.1:8080/device"} {
		if err := a.openBrowser(good); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
	if len(opened) != 2 {
		t.Fatalf("opened %v", opened)
	}
	// on a computer with no screen nothing is opened: a browser in the terminal would take it over
	a.open = nil
	if runtime.GOOS == "linux" {
		if err := a.openBrowser("https://app.wheelmachine.ai/device"); err == nil {
			t.Fatal("a browser was opened with no screen")
		}
	}
}

// A time is said as far from now as it is, gone by or still to come, to the minute.
func TestTimesGoneByAndToCome(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{now.Add(-90 * time.Second), "2026-10-23 12:03 UTC (1m ago)"},
		{now.Add(-72 * time.Hour), "2026-10-20 12:05 UTC (3d ago)"},
		{now, "2026-10-23 12:05 UTC (0s ago)"},
		{now.Add(30*24*time.Hour - time.Second), "2026-11-22 12:04 UTC (in 30d)"},
		{now.Add(5*time.Hour + 20*time.Minute), "2026-10-23 17:25 UTC (in 5h)"},
		{now.Add(45 * time.Second), "2026-10-23 12:05 UTC (in 45s)"},
	} {
		if got := when(c.at, now); got != c.want {
			t.Errorf("when(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

// inOrg makes the harness's key one made in an organisation, by Ada, a member of one team with limits of her own.
func (h *harness) inOrg(role string, teams []string) *fakeapi.Org {
	machines, vcpus, month := 5, 16, 200.0
	org := &fakeapi.Org{ID: "acc_00000000000000000000000077", Name: "Acme", Role: role, Teams: teams,
		Person: api.Person{ID: "acc_0000000000000000000000ada1", Name: "Ada Lovelace"},
		Limits: api.PersonLimits{MaxMachines: &machines, MaxVCPUs: &vcpus, MonthCapUSD: &month}}
	h.api.Org = org
	return org
}

// In a person's own account wm me says what it always said; in an organisation it says the organisation, the person
// who made the key, their role and teams there, and their own limits there - plainly when they have none.
func TestMeSaysTheOrganisationThePersonTheirRoleTeamsAndLimits(t *testing.T) {
	h := newHarness(t)
	own := h.wm("", "me").must(t, 0)
	for _, line := range []string{"organisation:", "person:", "role:", "teams:", "limits:"} {
		if strings.Contains(own.stdout, line) {
			t.Fatalf("a person's own account is shown as it always was, without %q:\n%s", line, own.stdout)
		}
	}
	if !strings.HasPrefix(own.stdout, "account:") || !strings.Contains(own.stdout, "key:") {
		t.Fatalf("me:\n%s", own.stdout)
	}

	org := h.inOrg(api.RoleMember, []string{"prj_00000000000000000000000001", "prj_00000000000000000000000002"})
	r := h.wm("", "me").must(t, 0)
	for _, want := range []string{
		"account:       acc_00000000000000000000000077\n",
		"organisation:  Acme (acc_00000000000000000000000077)\n",
		"person:        Ada Lovelace (acc_0000000000000000000000ada1)\n",
		"role:          member\n",
		"teams:         prj_00000000000000000000000001, prj_00000000000000000000000002\n",
		"limits:        at most 5 machines, at most 16 CPUs at once, at most $200.00 a month; GPU machines: no\n",
		"can spend:     yes\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("me in an organisation lacks %q:\n%s", want, r.stdout)
		}
	}
	if r := h.wm("", "me", "-q").must(t, 0); r.stdout != org.ID+"\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	var me api.Me
	if r := h.wm("", "me", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &me) != nil || !me.InOrg() || me.Person.Name != "Ada Lovelace" ||
		me.Role != api.RoleMember || len(me.Teams) != 2 || *me.Limits.MaxMachines != 5 || me.Limits.GPUs {
		t.Fatalf("json %s", r.stdout)
	}

	// a member in no team yet, and an owner, whose role is not limited to teams, with no limits of their own
	org.Teams = []string{}
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout, "teams:         none yet: an owner or admin puts people in teams\n") {
		t.Fatalf("no team:\n%s", r.stdout)
	}
	org.Role, org.Teams, org.Limits = api.RoleOwner, nil, api.PersonLimits{GPUs: true}
	r = h.wm("", "me").must(t, 0)
	if !strings.Contains(r.stdout, "teams:         every project (the role is not limited to teams)\n") ||
		!strings.Contains(r.stdout, "limits:        no limit of their own; GPU machines: yes\n") {
		t.Fatalf("an owner:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "manages:") {
		t.Fatalf("one who manages no team, said to:\n%s", r.stdout)
	}
	// a member who manages two teams: each with their level there, in the order of the teams' ids
	org.Role, org.Teams = api.RoleMember, []string{"prj_00000000000000000000000001", "prj_00000000000000000000000002"}
	org.Levels = map[string]string{"prj_00000000000000000000000002": api.LevelApprover, "prj_00000000000000000000000001": api.LevelFull}
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout,
		"manages:       prj_00000000000000000000000001 (Full manager), prj_00000000000000000000000002 (Approver)\n") {
		t.Fatalf("a manager's levels:\n%s", r.stdout)
	}
	if r := h.wm("", "me", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &me) != nil || me.Levels["prj_00000000000000000000000001"] != api.LevelFull {
		t.Fatalf("json levels %s", r.stdout)
	}
}

func TestOrgsListsTheOrganisationsOfAKeysPerson(t *testing.T) {
	h := newHarness(t)
	if r := h.wm("", "orgs").must(t, 0); strings.TrimSpace(r.stdout) != "ID  NAME  ROLE  PEOPLE  PRIMARY OWNER" {
		t.Fatalf("in none: %q", r.stdout)
	}
	h.api.Orgs = []api.Org{
		{ID: "acc_00000000000000000000000078", Object: "org", Name: "Lab", Role: api.RoleBilling, People: 12, CreatedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{ID: "acc_00000000000000000000000077", Object: "org", Name: "Acme", Role: api.RoleOwner, PrimaryOwner: true, People: 3,
			CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	}
	r := h.wm("", "orgs").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "ID ") || !strings.Contains(rows[0], "PRIMARY OWNER") ||
		!regexp.MustCompile(`^acc_0+78\s+Lab\s+billing\s+12\s+no$`).MatchString(rows[1]) ||
		!regexp.MustCompile(`^acc_0+77\s+Acme\s+owner\s+3\s+yes$`).MatchString(rows[2]) {
		t.Fatalf("orgs:\n%s", r.stdout)
	}
	if r := h.wm("", "orgs", "-q").must(t, 0); r.stdout != "acc_00000000000000000000000078\nacc_00000000000000000000000077\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	var got []api.Org
	if r := h.wm("", "orgs", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &got) != nil || len(got) != 2 || !got[1].PrimaryOwner {
		t.Fatalf("json %s", r.stdout)
	}
	h.wm("", "orgs", "Acme").must(t, 2)

	// a key made in an organisation lists none: the API's words, and what lists them
	h.inOrg(api.RoleOwner, nil)
	r = h.wm("", "orgs").must(t, 1)
	if !strings.Contains(r.stderr, "an organisation's key does not list the other organisations of the person who made it (forbidden, 403") ||
		!strings.Contains(r.stderr, "wm: a key of your own account lists them") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

// events is n events of the audit, the newest first, a minute apart before the fake's own clock.
func events(n int) []api.AuditEvent {
	base := time.Date(2026, 10, 23, 11, 0, 0, 0, time.UTC)
	out := make([]api.AuditEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, api.AuditEvent{ID: strconv.Itoa(n - i), Object: "audit_event", At: base.Add(-time.Duration(i) * time.Minute),
			Action: "machine.create", Target: "m_00000000000000000000000001", Person: &api.AuditPerson{ID: "acc_0000000000000000000000ada1", Name: "Ada Lovelace"},
			By: "key_00000000000000a1", Address: "203.0.113.7", RequestID: "req_" + strconv.Itoa(i), Detail: map[string]string{}})
	}
	return out
}

func TestAuditListsWhatWasDoneNewestFirstPageAfterPage(t *testing.T) {
	h := newHarness(t)
	h.api.Audit = events(205)
	h.api.Audit[0].Action, h.api.Audit[0].Target = "machine.terminal.open", "m_00000000000000000000000009"
	h.api.Audit[1].Person, h.api.Audit[1].By, h.api.Audit[1].Address = nil, "system", ""
	r := h.wm("", "audit").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 206 || !regexp.MustCompile(`^TIME \(UTC\)\s+PERSON\s+ACTION\s+TARGET\s+FROM$`).MatchString(rows[0]) {
		t.Fatalf("every event, after a header: %d rows\n%s", len(rows), strings.Join(rows[:3], "\n"))
	}
	if !regexp.MustCompile(`^2026-10-23 11:00:00\s+Ada Lovelace\s+machine.terminal.open\s+m_0+9\s+203.0.113.7$`).MatchString(rows[1]) ||
		!regexp.MustCompile(`^2026-10-23 10:59:00\s+system\s+machine.create\s+m_0+1\s+-$`).MatchString(rows[2]) {
		t.Fatalf("rows:\n%s\n%s", rows[1], rows[2])
	}
	// two pages: the second where the first said it begins
	var asked []fakeapi.Request
	for _, req := range h.api.Requests() {
		if req.Path == "/v1/audit" {
			asked = append(asked, req)
		}
	}
	if len(asked) != 2 || asked[0].Query.Get("limit") != "200" || asked[0].Query.Get("after") != "" || asked[1].Query.Get("after") != "6" {
		t.Fatalf("pages asked: %v", asked)
	}

	if r := h.wm("", "audit", "--limit", "3", "-q").must(t, 0); r.stdout != "205\n204\n203\n" {
		t.Fatalf("the newest three: %q", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/audit"); req.Query.Get("limit") != "3" {
		t.Fatalf("a page no larger than what is shown: %v", req.Query)
	}
	var got []api.AuditEvent
	if r := h.wm("", "audit", "--json", "--limit", "2").must(t, 0); json.Unmarshal([]byte(r.stdout), &got) != nil || len(got) != 2 ||
		got[0].Action != "machine.terminal.open" || got[1].Person != nil || got[1].By != "system" {
		t.Fatalf("json %s", r.stdout)
	}
}

func TestAuditIsNarrowedByPersonActionAndTime(t *testing.T) {
	h := newHarness(t)
	h.api.Audit = events(3)
	asked := func() map[string]string {
		req, _ := h.api.Last(http.MethodGet, "/v1/audit")
		out := map[string]string{}
		for k := range req.Query {
			out[k] = req.Query.Get(k)
		}
		delete(out, "limit")
		return out
	}
	for _, c := range []struct {
		args []string
		want map[string]string
	}{
		{[]string{"--person", "acc_0000000000000000000000ada1", "--action", "machine."}, map[string]string{"person": "acc_0000000000000000000000ada1", "action": "machine."}},
		// a date is its midnight in UTC; how long ago is by wm's own clock, the one it shows ages by (12:05 here)
		{[]string{"--since", "2026-10-01", "--until", "2026-10-23T09:00:00Z"}, map[string]string{"since": "2026-10-01T00:00:00Z", "until": "2026-10-23T09:00:00Z"}},
		{[]string{"--since", "6h"}, map[string]string{"since": "2026-10-23T06:05:00Z"}},
		{[]string{"--since", "7d", "--until", "90m"}, map[string]string{"since": "2026-10-16T12:05:00Z", "until": "2026-10-23T10:35:00Z"}},
		{[]string{"--since", "2026-10-23T14:30:00+02:00"}, map[string]string{"since": "2026-10-23T12:30:00Z"}},
	} {
		h.wm("", append([]string{"audit"}, c.args...)...).must(t, 0)
		if got := asked(); !equalMaps(got, c.want) {
			t.Fatalf("%v: asked %v, want %v", c.args, got, c.want)
		}
	}
	if r := h.wm("", "audit", "--since", "2026-10-23T10:59:30Z", "-q").must(t, 0); r.stdout != "3\n" {
		t.Fatalf("since: %q", r.stdout)
	}

	before := len(h.api.Requests())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"audit", "--since", "yesterday"}, `--since is a date (2026-10-01), a time (2026-10-01T09:00:00Z) or how long ago (6h, 7d), not "yesterday"`},
		{[]string{"audit", "--until", "2026-13-01"}, `--until is a date`},
		{[]string{"audit", "--since", "-6h"}, `--since is a date`},
		{[]string{"audit", "--csv", "--json"}, "--csv or --json"},
		{[]string{"audit", "--csv", "--limit", "5"}, "--limit is for the list"},
		{[]string{"audit", "--limit", "-1"}, "--limit is 0 or more"},
		{[]string{"audit", "machine."}, "too many words"},
	} {
		if r := h.wm("", c.args...).must(t, 2); !strings.Contains(r.stderr, c.want) {
			t.Fatalf("%v: stderr %q, want %q", c.args, r.stderr, c.want)
		}
	}
	if len(h.api.Requests()) != before {
		t.Fatal("what is not right is said before any call")
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The audit as a file: CSV quoted as the API quotes it (a field a spreadsheet would run begun with '), to standard
// output or whole to a file of one's own, or JSON; and the API's own words when a file would cover more than it gives.
func TestAuditIsExportedAsAFile(t *testing.T) {
	h := newHarness(t)
	h.api.Audit = events(2)
	h.api.Audit[0].Person.Name, h.api.Audit[0].Target = "=2+3", `the "web" box`
	h.api.Audit[0].Detail = map[string]string{"user": "root", "app": "desktop"}
	r := h.wm("", "audit", "--csv").must(t, 0)
	want := `"id","at","action","target","person","person_name","by","address","request_id","detail"` + "\r\n" +
		`"2","2026-10-23T11:00:00Z","machine.create","the ""web"" box","acc_0000000000000000000000ada1","'=2+3","key_00000000000000a1","203.0.113.7","req_0","app=desktop; user=root"` + "\r\n" +
		`"1","2026-10-23T10:59:00Z","machine.create","m_00000000000000000000000001","acc_0000000000000000000000ada1","Ada Lovelace","key_00000000000000a1","203.0.113.7","req_1",""` + "\r\n"
	if r.stdout != want || r.stderr != "" {
		t.Fatalf("csv:\n%q\nwant\n%q\nstderr %q", r.stdout, want, r.stderr)
	}
	req, _ := h.api.Last(http.MethodGet, "/v1/audit/export")
	if req.Query.Get("format") != "csv" || req.Query.Get("since") != "" {
		t.Fatalf("query %v: the last year, when since is left out, is the API's to say", req.Query)
	}

	path := filepath.Join(t.TempDir(), "audit.csv")
	r = h.wm("", "audit", "--out", path, "--action", "machine.").must(t, 0)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want || r.stdout != "" || r.stderr != "wm: wrote "+strconv.Itoa(len(want))+" bytes of CSV to "+path+"\n" {
		t.Fatalf("file %q (%v); stdout %q stderr %q", got, err, r.stdout, r.stderr)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("a new file of the audit, which names people and where they were, is readable by its owner alone: %v", st.Mode().Perm())
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/audit/export"); req.Query.Get("action") != "machine." {
		t.Fatalf("query %v", req.Query)
	}

	jsonPath := filepath.Join(t.TempDir(), "audit.json")
	r = h.wm("", "audit", "--json", "--out", jsonPath, "--since", "2026-10-23T10:59:30Z").must(t, 0)
	var exported []api.AuditEvent
	if raw, _ := os.ReadFile(jsonPath); json.Unmarshal(raw, &exported) != nil || len(exported) != 1 || exported[0].ID != "2" ||
		!strings.Contains(r.stderr, "bytes of JSON to "+jsonPath) {
		t.Fatalf("json file %v; stderr %q", exported, r.stderr)
	}
	if req, _ := h.api.Last(http.MethodGet, "/v1/audit/export"); req.Query.Get("format") != "json" || req.Query.Get("since") != "2026-10-23T10:59:30Z" {
		t.Fatalf("query %v", req.Query)
	}

	// more than a file holds: the API's own words, and no file left behind
	tooLong := filepath.Join(t.TempDir(), "two-years.csv")
	r = h.wm("", "audit", "--out", tooLong, "--since", "2025-01-01").must(t, 1)
	if !strings.Contains(r.stderr, "an export covers a year at most, from since to until (invalid_request, 400") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if _, err := os.Stat(tooLong); !os.IsNotExist(err) {
		t.Fatal("a file refused is never written")
	}
}

func TestAuditIsReadByOwnersAndAdminsAlone(t *testing.T) {
	h := newHarness(t)
	h.api.Audit = events(1)
	h.inOrg(api.RoleAdmin, nil)
	h.wm("", "audit").must(t, 0)
	h.api.Org.Role = api.RoleMember
	for _, args := range [][]string{{"audit"}, {"audit", "--csv"}} {
		r := h.wm("", args...).must(t, 1)
		if !strings.Contains(r.stderr, "your role in this organisation does not allow this (forbidden, 403") ||
			!strings.Contains(r.stderr, "wm: the audit is read with a key that holds every scope, and in an organisation by its owners and admins") {
			t.Fatalf("%v: stderr %q", args, r.stderr)
		}
	}
}

// wm login --account asks for the key to be made in an organisation; the person chooses as they approve, and where the
// key was made is said.
func TestLoginAsksForAKeyInAnOrganisation(t *testing.T) {
	h := newHarness(t)
	delete(h.env, api.EnvAPIKey)
	h.inOrg(api.RoleMember, []string{"prj_00000000000000000000000001"})
	r := h.wm("", "login", "--account", "acc_00000000000000000000000077", "--no-browser").must(t, 0)
	start, _ := h.api.Last(http.MethodPost, "/v1/login/devices")
	var asked map[string]any
	if json.Unmarshal(start.Body, &asked) != nil || asked["account"] != "acc_00000000000000000000000077" {
		t.Fatalf("the sign-in was asked for as %s", start.Body)
	}
	if !strings.Contains(r.stdout, `Signed in to account acc_00000000000000000000000077, the organisation "Acme".`) {
		t.Fatalf("stdout %q", r.stdout)
	}
	// with none asked for, nothing is sent of one
	h.api.Org = nil
	h.wm("", "login", "--no-browser").must(t, 0)
	if start, _ := h.api.Last(http.MethodPost, "/v1/login/devices"); strings.Contains(string(start.Body), "account") {
		t.Fatalf("asked as %s", start.Body)
	}
	before := len(h.api.Requests())
	if r := h.wm("", "login", "--account", "Acme").must(t, 2); !strings.Contains(r.stderr, `--account is an organisation's id, acc_ and 26 letters and digits, as wm orgs lists them; not "Acme"`) {
		t.Fatalf("stderr %q", r.stderr)
	}
	if r := h.wm("ak_live_x\n", "login", "--key", "--account", "acc_00000000000000000000000077").must(t, 2); !strings.Contains(r.stderr, "--account") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if len(h.api.Requests()) != before {
		t.Fatal("an --account that is not one is refused before any call")
	}
	if r := h.wm("", "login", "-h").must(t, 0); !strings.Contains(r.stdout, "--account acc_...") || !strings.Contains(r.stdout, "wm orgs lists them") {
		t.Fatalf("help %q", r.stdout)
	}
}

// Who made a key, and whose a machine is, are said in an organisation alone: a person's own account's tables are as they
// always were.
func TestKeysAndMachinesSayWhoMadeThemAndWhoseTheyAreInAnOrganisation(t *testing.T) {
	h := newHarness(t)
	h.api.AddMachine("web", api.StateRunning)
	for _, args := range [][]string{{"keys", "ls"}, {"machines", "ls"}, {"machines", "get", "web"}, {"usage"}} {
		if r := h.wm("", args...).must(t, 0); strings.Contains(strings.ToLower(r.stdout), "made by") || strings.Contains(strings.ToLower(r.stdout), "whose") {
			t.Fatalf("%v in a person's own account:\n%s", args, r.stdout)
		}
	}

	org := h.inOrg(api.RoleMember, []string{h.api.Project()})
	h.wm("", "keys", "create", "ci", "--scope", "machines:read").must(t, 0)
	mine := h.api.AddMachine("box", api.StateRunning)
	r := h.wm("", "keys", "ls").must(t, 0)
	rows := lines(r.stdout)
	// (a column as a reader sees it: in characters, as a key's … is one)
	at := func(row, cell string) int {
		i := strings.Index(row, cell)
		if i < 0 {
			return -1
		}
		return utf8.RuneCountInString(row[:i])
	}
	col := at(rows[0], "MADE BY")
	if col < 0 || !regexp.MustCompile(`PROJECTS\s+MADE BY\s+LAST USED$`).MatchString(rows[0]) || len(rows) != 3 || at(rows[2], org.Person.ID) != col ||
		at(rows[1], "-  ") != col {
		t.Fatalf("keys, the one made before said by nobody:\n%s", r.stdout)
	}
	// a member's machines: whose each is, by name and role - a team manager's, and one whose holder has left
	manager, full, role := api.LevelManager, api.LevelFull, api.RoleMember
	theirs := h.api.AddMachine("theirs", api.StateRunning)
	h.api.SetHeldBy(theirs.ID, &api.HeldBy{ID: "acc_0000000000000000000000bee1", Name: "Meera", Role: &role, Level: &manager})
	gone := h.api.AddMachine("gone", api.StateRunning)
	h.api.SetHeldBy(gone.ID, &api.HeldBy{ID: "acc_0000000000000000000000bee2", Name: "Ravi"})
	fulls := h.api.AddMachine("fulls", api.StateRunning)
	h.api.SetHeldBy(fulls.ID, &api.HeldBy{ID: "acc_0000000000000000000000bee3", Name: "Asha", Role: &role, Level: &full})
	r = h.wm("", "machines", "ls").must(t, 0)
	rows = lines(r.stdout)
	col = at(rows[0], "WHOSE")
	if col < 0 || !regexp.MustCompile(`LOCATION\s+WHOSE\s+AGE$`).MatchString(rows[0]) || len(rows) != 6 {
		t.Fatalf("machines:\n%s", r.stdout)
	}
	for _, c := range []struct{ id, whose string }{{gone.ID, "Ravi · Left"}, {theirs.ID, "Meera · Team manager"}, {fulls.ID, "Asha · Full manager"},
		{mine.ID, "Ada Lovelace · Member"}} {
		i := slices.IndexFunc(rows, func(row string) bool { return strings.HasPrefix(row, c.id) })
		if i < 0 || at(rows[i], c.whose) != col || !strings.HasSuffix(rows[i], "5m") {
			t.Fatalf("machines, %s as %q:\n%s", c.id, c.whose, r.stdout)
		}
	}
	if r := h.wm("", "machines", "get", "box").must(t, 0); !regexp.MustCompile(`\nproject:\s+prj_\w+\nmade by:\s+` + org.Person.ID +
		`\nwhose:\s+Ada Lovelace · Member\n`).MatchString(r.stdout) {
		t.Fatalf("detail:\n%s", r.stdout)
	}
	held := &api.HeldBy{ID: org.Person.ID, Name: org.Person.Name, Role: &role}
	h.api.Usage = api.Usage{TotalUSD: 0.5, Machines: []api.UsageLine{{Machine: mine.ID, Name: "box", Project: h.api.Project(), MadeBy: &org.Person.ID,
		HeldBy: held, RunningSeconds: 60, USD: 0.5}}}
	if r := h.wm("", "usage").must(t, 0); !regexp.MustCompile(`MACHINE\s+NAME\s+PROJECT\s+WHOSE\s+RAN\s+COST`).MatchString(r.stdout) ||
		!regexp.MustCompile(mine.ID+`\s+box\s+prj_\w+\s+Ada Lovelace · Member\s+1m`).MatchString(r.stdout) {
		t.Fatalf("usage:\n%s", r.stdout)
	}
}

// A machine handed off, taken back - and one handed to the person, given back - from the command line: each, made with
// wm's key, waits for the OK of the person who made the key, said so with where to give it, and is listed as what it is.
func TestMachinesTakenBackAndGivenBack(t *testing.T) {
	h := newHarness(t)
	h.wm("", "machines", "create", "--name", "web", "-q").must(t, 0)
	var r result
	for _, c := range []struct{ command, said string }{{"take-back", "take-back"}, {"give-back", "give-back"}} {
		r = h.wm("", "machines", c.command, "web").must(t, 0)
		if !strings.Contains(r.stdout, "Waiting for You") || !strings.Contains(r.stdout, c.said) || !strings.Contains(r.stdout, "dashboard") {
			t.Fatalf("%s:\n%s", c.command, r.stdout)
		}
		if q := h.api.Asks[len(h.api.Asks)-1]; q.Way != "back" || q.Kind != api.RequestHandOff {
			t.Fatalf("%s, as it was asked: %+v", c.command, q)
		}
		if r := h.wm("", "machines", c.command).must(t, 2); !strings.Contains(r.stderr, "which machine") {
			t.Fatalf("%s of nothing: %q", c.command, r.stderr)
		}
	}
}

// A machine forked for a teammate and handed off, from the command line: made with wm's key, each waits for the person who made the key's
// own OK, said so with where to give it; once given in the dashboard, carried out where nobody else need approve it -
// the fork made, the machine theirs - else waiting, said in the dashboard's words; asks listed, taken back; a team's
// rules read, without what its machines came to for one who is not shown it. Approving is the dashboard's, and the
// help says so.
func TestForksForOthersHandOffsAndRequests(t *testing.T) {
	h := newHarness(t)
	h.wm("", "machines", "create", "--name", "web", "-q").must(t, 0)
	last := func() string { return h.api.Asks[len(h.api.Asks)-1].ID }
	r := h.wm("", "machines", "fork", "web", "--for", "meera@example.com").must(t, 0)
	if !strings.Contains(r.stdout, "Waiting for You") || !strings.Contains(r.stdout, "meera@example.com") ||
		!strings.Contains(r.stdout, "approve it signed in to the dashboard, https://app.wheelmachine.ai/requests") {
		t.Fatalf("a fork for a teammate:\n%s", r.stdout)
	}
	if !h.api.Approve(last()) {
		t.Fatal("the OK of the person who made the key, for the fork")
	}
	if r := h.wm("", "requests", "--all").must(t, 0); !strings.Contains(r.stdout, "Forked") {
		t.Fatalf("the fork, approved by the person who made the key:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "ls").must(t, 0); !strings.Contains(r.stdout, "web-fork") {
		t.Fatalf("the fork made:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "fork", "web", "--for", "meera@example.com", "--name", "x").must(t, 2); !strings.Contains(r.stderr, "named after") {
		t.Fatalf("a fork for a teammate, named: %q", r.stderr)
	}
	r = h.wm("", "machines", "hand-off", "web", "--to", "ravi", "--why", "yours now").must(t, 0)
	if !strings.Contains(r.stdout, "Waiting for You") || !strings.Contains(r.stdout, "hand-off") || !strings.Contains(r.stdout, "dashboard") {
		t.Fatalf("a hand-off:\n%s", r.stdout)
	}
	if !h.api.Approve(last()) {
		t.Fatal("the OK of the person who made the key, for the hand-off")
	}
	if r := h.wm("", "requests", "--all").must(t, 0); !strings.Contains(r.stdout, "Handed off") {
		t.Fatalf("the hand-off, approved by the person who made the key:\n%s", r.stdout)
	}
	if r := h.wm("", "machines", "hand-off", "web").must(t, 2); !strings.Contains(r.stderr, "--to") {
		t.Fatalf("a hand-off to nobody: %q", r.stderr)
	}
	h.api.GivesWait = true
	r = h.wm("", "machines", "hand-off", "web", "--to", "ravi", "--json").must(t, 0)
	var waiting api.Request
	if err := json.Unmarshal([]byte(r.stdout), &waiting); err != nil || waiting.State != "waiting" || waiting.WaitingOn == nil ||
		waiting.WaitingOn.Who != "asker" || waiting.StateWords() != "Waiting for You" {
		t.Fatalf("a hand-off waiting for the person who made the key: %v %s", err, r.stdout)
	}
	h.api.Approve(waiting.ID)
	if q := h.api.Asks[len(h.api.Asks)-1]; q.StateWords() != "Waiting for a manager" {
		t.Fatalf("a hand-off approved by the person who made the key, waiting for a manager: %+v", q)
	}
	r = h.wm("", "requests", "ask", "fork", "--of", "ravi").must(t, 0)
	if !strings.Contains(r.stdout, "Waiting for You") || !strings.Contains(r.stdout, "asks for a fork") {
		t.Fatalf("an ask:\n%s", r.stdout)
	}
	h.api.Approve(last())
	if q := h.api.Asks[len(h.api.Asks)-1]; q.StateWords() != "Waiting for ravi" {
		t.Fatalf("an ask approved by the person who made the key, waiting for its holder: %+v", q)
	}
	if r := h.wm("", "requests", "ask", "nothing", "--of", "ravi").must(t, 2); !strings.Contains(r.stderr, "fork or a hand-off") {
		t.Fatalf("an ask of nothing: %q", r.stderr)
	}
	open := h.wm("", "requests", "-q").must(t, 0)
	if ids := strings.Fields(open.stdout); len(ids) != 2 {
		t.Fatalf("the open requests: %q", open.stdout)
	}
	all := h.wm("", "requests", "--all", "-q").must(t, 0)
	if ids := strings.Fields(all.stdout); len(ids) != 4 {
		t.Fatalf("every request: %q", all.stdout)
	}
	ask := strings.Fields(open.stdout)[0]
	if r := h.wm("", "requests", "take-back", ask).must(t, 0); !strings.Contains(r.stdout, "Withdrawn") {
		t.Fatalf("taken back:\n%s", r.stdout)
	}
	if r := h.wm("", "requests", "take-back", ask).must(t, 1); !strings.Contains(r.stderr, "Decided already") {
		t.Fatalf("taken back twice: %q", r.stderr)
	}
	if r := h.wm("", "projects", "rules", "Default").must(t, 0); !strings.Contains(r.stdout, "HAND-OFFS") || !strings.Contains(r.stdout, "ask") ||
		strings.Contains(r.stdout, "SPENT") {
		t.Fatalf("a team's rules, to one not shown what it spent:\n%s", r.stdout)
	}
	var rules api.TeamRules
	if r := h.wm("", "projects", "rules", "Default", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &rules) != nil || rules.SpentUSD != nil {
		t.Fatalf("a team's rules, as JSON: %s", r.stdout)
	}
	spent, opened := 12.5, 1
	h.api.SetRules(api.TeamRules{Object: "team_rules", Project: rules.Project, HandOffs: "free", Forks: "ask", GPUs: true, OpenInternet: true,
		Models: "any", SpentUSD: &spent, OpenMachines: &opened})
	if r := h.wm("", "projects", "rules", "Default").must(t, 0); !strings.Contains(r.stdout, "SPENT") || !strings.Contains(r.stdout, "$12.50") {
		t.Fatalf("a team's rules, to its manager:\n%s", r.stdout)
	}
	if r := h.wm("", "requests", "-h").must(t, 0); !strings.Contains(r.stdout, "dashboard") {
		t.Fatalf("the requests' help:\n%s", r.stdout)
	}
}

// In an organisation a member's fork waits for the OK of a full manager of its team, an owner or an admin: wm makes
// nothing and waits for no machine, but prints the request - its id alone with -q, the request with --json - saying
// whose OK it waits for and how to follow it; asked again while it waits, it is the one there is. A full manager's own
// fork and an owner's are made at once, as a fork in a person's own account is (TestMachineActions).
func TestAMembersForkWaitsForAFullManager(t *testing.T) {
	h := newHarness(t)
	org := h.inOrg(api.RoleMember, []string{h.api.Project()})
	web := h.api.AddMachine("web", api.StateRunning)
	r := h.wm("", "machines", "fork", "web", "--name", "web-2", "--label", "stage=try").must(t, 0)
	rows := lines(r.stdout)
	if len(rows) != 4 || !regexp.MustCompile(`^rq_\w+\s+fork\s+web\s+Ada Lovelace\s+Ada Lovelace\s+Waiting for Ada Lovelace\s+5m$`).MatchString(rows[1]) ||
		rows[2] != "made with a key, it waits for your own OK: approve it signed in to the dashboard, https://app.wheelmachine.ai/requests" ||
		rows[3] != "the fork waits for the OK of a full manager of its team, an owner or an admin, and is made once they approve it: "+
			"follow it with wm requests" {
		t.Fatalf("a member's fork:\n%s", r.stdout)
	}
	q := h.api.Asks[len(h.api.Asks)-1]
	if !strings.HasPrefix(rows[1], q.ID+" ") || q.Kind != api.RequestFork || q.Holder.ID != org.Person.ID || q.Receiver.ID != org.Person.ID {
		t.Fatalf("the request: %+v", q)
	}
	// asked with the name and labels the fork is to have once it is made; nothing made now, and no machine waited for
	if sent, _ := h.api.Last(http.MethodPost, "/v1/machines/"+web.ID+"/fork"); string(sent.Body) != `{"name":"web-2","labels":{"stage":"try"},"wait_s":600}` {
		t.Fatalf("sent %s", sent.Body)
	}
	if r := h.wm("", "machines", "ls", "-q").must(t, 0); r.stdout != web.ID+"\n" {
		t.Fatalf("a fork made while it waits: %q", r.stdout)
	}
	if slices.ContainsFunc(h.api.Requests(), func(rq fakeapi.Request) bool { return strings.HasSuffix(rq.Path, "/wait") }) {
		t.Fatal("a fork that waits for an OK is never waited on as a machine")
	}
	// asked again while it waits: the same request, as JSON and as its id alone
	var again api.Request
	if r := h.wm("", "machines", "fork", "web", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &again) != nil || again.ID != q.ID ||
		again.Object != "request" || again.State != "waiting" || again.WaitingOn == nil || again.WaitingOn.Who != "asker" {
		t.Fatalf("asked again, as JSON: %s", r.stdout)
	}
	if r := h.wm("", "machines", "fork", "web", "-q").must(t, 0); r.stdout != q.ID+"\n" {
		t.Fatalf("asked again, quiet: %q", r.stdout)
	}
	// the OK of the person who made the key given in the dashboard, it waits for a full manager, said in the words the dashboard says it
	if !h.api.Approve(q.ID) {
		t.Fatal("the OK of the person who made the key, for the fork")
	}
	if r := h.wm("", "requests").must(t, 0); !strings.Contains(r.stdout, "Waiting for a full manager") {
		t.Fatalf("waiting for a full manager:\n%s", r.stdout)
	}
	r = h.wm("", "machines", "fork", "web").must(t, 0)
	if !strings.Contains(r.stdout, "Waiting for a full manager") || strings.Contains(r.stdout, "your own OK") || !strings.Contains(r.stdout, "wm requests") {
		t.Fatalf("asked again once the person who made the key approved it:\n%s", r.stdout)
	}
	// a full manager of its team, and an owner, fork at once
	org.Levels = map[string]string{h.api.Project(): api.LevelFull}
	r = h.wm("", "machines", "fork", "web", "-q").must(t, 0)
	if m, ok := h.api.Machine(strings.TrimSpace(r.stdout)); !ok || m.Name != "web-fork" || m.State != api.StateRunning {
		t.Fatalf("a full manager's own fork: %q", r.stdout)
	}
	org.Role, org.Levels = api.RoleOwner, nil
	r = h.wm("", "machines", "fork", "web", "--name", "web-3").must(t, 0)
	if rows := lines(r.stdout); len(rows) != 2 || !strings.Contains(rows[1], "web-3") || !strings.Contains(rows[1], "running") {
		t.Fatalf("an owner's own fork:\n%s", r.stdout)
	}
	// (the help as it reads, its lines wrapped where they fall)
	read := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if r := h.wm("", "machines", "fork", "-h").must(t, 0); !strings.Contains(read(r.stdout), "a member's fork waits for the OK of a full manager") {
		t.Fatalf("the fork's help:\n%s", r.stdout)
	}
	if r := h.wm("", "projects", "rules", "-h").must(t, 0); !strings.Contains(read(r.stdout), "forks Ask") || strings.Contains(read(r.stdout), "Free, Ask or Off, GPUs") {
		t.Fatalf("the rules' help:\n%s", r.stdout)
	}
}

// wm limits shows the limits for the month that apply to the account and how near each is; wm limits set month sets the
// account's own, or takes it away with none, and refuses what is not dollars.
func TestLimits(t *testing.T) {
	h := newHarness(t)
	cap, spent, pct := 200.0, 137.6, 68
	budget, tspent, tpct := 80.0, 74.2, 92
	h.api.Limits = api.UsageLimits{Account: api.AccountLimit{Name: "Lab", Kind: "org", MonthCapUSD: &cap, InForceUSD: &cap, InForce: "account",
		SpentUSD: &spent, Percent: &pct, Limited: true},
		Teams: []api.TeamLimit{{ID: "prj_d", Name: "Design", MonthBudgetUSD: &budget, SpentUSD: &tspent, Percent: &tpct}, {ID: "prj_r", Name: "Research"}}}
	r := h.wm("", "limits").must(t, 0)
	for _, want := range []string{"Lab", "$137.60 of $200.00", "68%", "Design", "$74.20 of $80.00", "92%", "Research", "none: the organisation's holds"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("limits: no %q in\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "start again on 1 November") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if r := h.wm("", "limits", "set", "month", "$250").must(t, 0); !strings.Contains(r.stdout, "$250.00 a month now") {
		t.Fatalf("set:\n%s", r.stdout)
	}
	req, _ := h.api.Last(http.MethodPut, "/v1/usage/limits/account")
	if string(bytes.TrimSpace(req.Body)) != `{"month_cap":250}` {
		t.Fatalf("body %s", req.Body)
	}
	if r := h.wm("", "limits", "set", "month", "none").must(t, 0); !strings.Contains(r.stdout, "no limit for the month of its own") {
		t.Fatalf("none:\n%s", r.stdout)
	}
	req, _ = h.api.Last(http.MethodPut, "/v1/usage/limits/account")
	if string(bytes.TrimSpace(req.Body)) != `{"month_cap":null}` {
		t.Fatalf("body %s", req.Body)
	}
	for _, args := range [][]string{{"limits", "set", "month", "lots"}, {"limits", "set", "month", "-5"}, {"limits", "set", "week", "5"},
		{"limits", "set", "month"}} {
		h.wm("", args...).must(t, 2)
	}
	if r := h.wm("", "--json", "limits").must(t, 0); !strings.Contains(r.stdout, `"object": "usage_limits"`) {
		t.Fatalf("json:\n%s", r.stdout)
	}
}

// wm folders lists the folders, and puts, lists, gets, moves and deletes their files - a folder named by its name or
// its id.
func TestFolders(t *testing.T) {
	h := newHarness(t)
	h.api.Folders = []api.SharedFolder{{Object: "shared_folder", ID: "fld_own", Kind: "own", Name: "My files", Access: "edit", Bytes: 1200},
		{Object: "shared_folder", ID: "fld_design", Kind: "team", Name: "Design", Access: "view", Bytes: 2500000000}}
	r := h.wm("", "folders").must(t, 0)
	for _, want := range []string{"My files", "fld_own", "edit", "1.2 KB", "Design", "view", "2.5 GB"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("folders: no %q in\n%s", want, r.stdout)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(src, []byte("the plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := h.wm("", "folders", "put", src, "my files:notes/plan.md").must(t, 0); !strings.Contains(r.stdout, "put notes/plan.md") {
		t.Fatalf("put:\n%s", r.stdout)
	}
	if string(h.api.FolderFiles["fld_own/notes/plan.md"]) != "the plan" {
		t.Fatalf("the file kept: %q", h.api.FolderFiles["fld_own/notes/plan.md"])
	}
	if r := h.wm("", "folders", "ls", "My files").must(t, 0); !strings.Contains(r.stdout, "notes/") {
		t.Fatalf("ls:\n%s", r.stdout)
	}
	// a folder listed a page at a time, every page asked for
	h.api.FolderPage = 1
	for _, name := range []string{"a.txt", "b.txt"} {
		h.api.FolderFiles["fld_own/notes/"+name] = []byte(name)
	}
	if r := h.wm("", "folders", "ls", "My files:notes").must(t, 0); !strings.Contains(r.stdout, "a.txt") || !strings.Contains(r.stdout, "b.txt") ||
		!strings.Contains(r.stdout, "plan.md") {
		t.Fatalf("ls, a page at a time:\n%s", r.stdout)
	}
	h.api.FolderPage = 0
	for _, name := range []string{"a.txt", "b.txt"} {
		delete(h.api.FolderFiles, "fld_own/notes/"+name)
	}
	if r := h.wm("", "folders", "get", "fld_own:notes/plan.md", "-").must(t, 0); r.stdout != "the plan" {
		t.Fatalf("get:\n%s", r.stdout)
	}
	h.wm("", "folders", "mv", "My files:notes/plan.md", "plan.md").must(t, 0)
	if r := h.wm("", "folders", "rm", "My files:plan.md").must(t, 0); !strings.Contains(r.stdout, "in the Trash") {
		t.Fatalf("rm:\n%s", r.stdout)
	}
	for _, args := range [][]string{{"folders", "put", src}, {"folders", "get", "My files"}, {"folders", "ls", "Nowhere"}, {"folders", "frob"}} {
		h.wm("", args...).must(t, 2)
	}
	// its help shows what works for everyone, and says who reaches My files from here
	help := h.wm("", "folders", "help").must(t, 0).stdout
	if !strings.Contains(help, `"Design:notes/plan.md"`) || strings.Contains(help, `put plan.md "My files`) || !strings.Contains(help, "not limited to a team") {
		t.Fatalf("folders help:\n%s", help)
	}
}

// wm code lists the code hosts connected and the repositories teams are given; wm code grant gives one, to read or to
// push too; wm code repos lists what a connection reaches.
func TestCode(t *testing.T) {
	h := newHarness(t)
	h.api.Code = api.Code{GitHub: true, Connections: []api.CodeConnection{{ID: "code_1", Provider: "github", Host: "github.com", Name: "acme"}}}
	h.api.Repos = []api.CodeRepo{{Repo: "acme/api", Host: "github.com", Connection: "code_1"}, {Repo: "acme/docs", Host: "github.com", Connection: "code_1"}}
	if r := h.wm("", "code", "repos", "code_1").must(t, 0); r.stdout != "acme/api\nacme/docs\n" {
		t.Fatalf("repos:\n%s", r.stdout)
	}
	if r := h.wm("", "code", "grant", "prj_d", "code_1", "acme/api", "write").must(t, 0); !strings.Contains(r.stdout, "github.com/acme/api to write") {
		t.Fatalf("grant:\n%s", r.stdout)
	}
	req, _ := h.api.Last(http.MethodPut, "/v1/projects/prj_d/code")
	if string(bytes.TrimSpace(req.Body)) != `{"access":"write","connection":"code_1","repo":"acme/api"}` {
		t.Fatalf("body %s", req.Body)
	}
	r := h.wm("", "code").must(t, 0)
	for _, want := range []string{"acme", "code_1", "github.com/acme/api", "read and push", "cg_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("code: no %q in\n%s", want, r.stdout)
		}
	}
	for _, args := range [][]string{{"code", "grant", "prj_d", "code_1", "acme/api", "admin"}, {"code", "connect", "bitbucket"}, {"code", "revoke", "prj_d"}} {
		h.wm("", args...).must(t, 2)
	}
}
