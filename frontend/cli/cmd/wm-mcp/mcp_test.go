package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/fakeapi"
)

// session is the server running on pipes, as an MCP client runs it.
type session struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan []byte
}

func start(t *testing.T, connect func() (*api.Client, error)) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := newServer(log.New(io.Discard, "", 0), connect)
	s := &session{t: t, in: inW, lines: make(chan []byte, 64)}
	done := make(chan error, 1)
	go func() {
		err := srv.serve(context.Background(), inR, outW)
		_ = outW.Close()
		done <- err
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				s.lines <- line
			}
			if err != nil {
				close(s.lines)
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the server did not end when its input did")
		}
	})
	return s
}

func connectTo(f *fakeapi.Server) func() (*api.Client, error) {
	return func() (*api.Client, error) {
		return api.New(api.WithBaseURL(f.URL), api.WithKey(f.Key),
			api.WithRetryPolicy(api.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
	}
}

func (s *session) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

// recv is the next message the server wrote: one line of JSON-RPC 2.0.
func (s *session) recv() map[string]any {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatal("the server closed its output")
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil || m["jsonrpc"] != "2.0" || bytes.Count(line, []byte("\n")) != 1 {
			s.t.Fatalf("not a JSON-RPC line: %q", line)
		}
		return m
	case <-time.After(10 * time.Second):
		s.t.Fatal("no answer within 10 s")
	}
	return nil
}

func (s *session) request(id any, method string, params any) map[string]any {
	s.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	s.send(string(b))
	got := s.recv()
	if fmt.Sprint(got["id"]) != fmt.Sprint(id) {
		s.t.Fatalf("answer for id %v, want %v: %v", got["id"], id, got)
	}
	return got
}

func (s *session) initialize(version string) map[string]any {
	s.t.Helper()
	res := ok(s.t, s.request("init", "initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}}))
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return res
}

func ok(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	if m["error"] != nil {
		t.Fatalf("error %v", m["error"])
	}
	res, isMap := m["result"].(map[string]any)
	if !isMap {
		t.Fatalf("no result: %v", m)
	}
	return res
}

func rpcErr(t *testing.T, m map[string]any, code int) map[string]any {
	t.Helper()
	e, isMap := m["error"].(map[string]any)
	if !isMap || e["code"] != float64(code) || m["result"] != nil {
		t.Fatalf("want error %d, got %v", code, m)
	}
	return e
}

// tool calls a tool and reads its text: compact JSON, and whether it is an error.
func (s *session) tool(id int, name string, args any) (bool, map[string]any) {
	s.t.Helper()
	res := ok(s.t, s.request(id, "tools/call", map[string]any{"name": name, "arguments": args}))
	blocks, _ := res["content"].([]any)
	if len(blocks) != 1 {
		s.t.Fatalf("content %v", res)
	}
	block := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	if block["type"] != "text" || strings.ContainsAny(text, "\n\t") && !json.Valid([]byte(text)) {
		s.t.Fatalf("block %v", block)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		s.t.Fatalf("text is not JSON: %q", text)
	}
	if compacted := compact(body); len(text) > len(compacted)+len(text)/10 {
		s.t.Fatalf("text is not compact: %q", text)
	}
	return res["isError"].(bool), body
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	f := fakeapi.New(t)
	s := start(t, connectTo(f))
	res := s.initialize("2025-06-18")
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("a version spoken is answered as asked: %v", res)
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "wheelmachine" || info["version"] != api.Version {
		t.Fatalf("serverInfo %v", info)
	}
	if _, has := res["capabilities"].(map[string]any)["tools"]; !has || res["instructions"] == "" {
		t.Fatalf("capabilities %v", res)
	}
	for asked, want := range map[string]string{"2024-11-05": "2024-11-05", "2025-03-26": "2025-03-26", "2025-11-25": "2025-11-25",
		"2099-01-01": "2025-11-25", "2026-07-28": "2025-11-25", "1.0": "2025-11-25"} {
		if got := s.initialize(asked)["protocolVersion"]; got != want {
			t.Fatalf("asked %s, answered %v, want %s", asked, got, want)
		}
	}
	e := rpcErr(t, s.request(9, "initialize", map[string]any{"capabilities": map[string]any{}}), codeInvalidParams)
	if !strings.Contains(e["message"].(string), "protocolVersion") {
		t.Fatalf("error %v", e)
	}
}

func TestNotificationsAreNotAnsweredAndPingIs(t *testing.T) {
	s := start(t, connectTo(fakeapi.New(t)))
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.send(`{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`)
	res := ok(t, s.request(2, "ping", nil))
	if len(res) != 0 {
		t.Fatalf("ping answers an empty result: %v", res)
	}
	s.initialize("2025-11-25")
	ok(t, s.request("p", "ping", map[string]any{}))
}

func TestToolsList(t *testing.T) {
	s := start(t, connectTo(fakeapi.New(t)))
	s.initialize("2025-11-25")
	res := ok(t, s.request(2, "tools/list", map[string]any{}))
	list := res["tools"].([]any)
	var names []string
	for _, raw := range list {
		tl := raw.(map[string]any)
		names = append(names, tl["name"].(string))
		schema := tl["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false || tl["description"] == "" || tl["title"] == "" {
			t.Fatalf("tool %v", tl)
		}
		ann := tl["annotations"].(map[string]any)
		for _, hint := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if _, has := ann[hint].(bool); !has {
				t.Fatalf("%s has no %s: %v", tl["name"], hint, ann)
			}
		}
		switch tl["name"] {
		case "delete_machine", "stop_machine", "write_file", "run_command", "use_screen":
			if ann["destructiveHint"] != true || ann["readOnlyHint"] != false {
				t.Fatalf("%s is destructive: %v", tl["name"], ann)
			}
		case "get_machine", "list_machines", "list_backups", "read_file", "list_gpus", "list_regions", "list_secrets", "list_apps", "list_repositories", "whoami":
			if ann["readOnlyHint"] != true {
				t.Fatalf("%s only reads: %v", tl["name"], ann)
			}
			if tl["name"] == "list_machines" {
				// every state machines are listed by, failed with them
				state := schema["properties"].(map[string]any)["state"].(map[string]any)
				if fmt.Sprint(state["enum"]) != fmt.Sprint(api.ListedStates) || !strings.Contains(state["description"].(string), "failed lists the ones that never came up") {
					t.Fatalf("list_machines' state: %v", state)
				}
			}
		case "take_screenshot":
			// it wakes a machine, charges for it and turns its desktop on, and what it shows goes to the model's provider: a client
			// that lets through by itself what only reads must still ask
			if ann["readOnlyHint"] != false || ann["destructiveHint"] != false {
				t.Fatalf("%s does more than read: %v", tl["name"], ann)
			}
		case "create_machine", "fork_machine", "backup_machine", "turn_app_on", "open_app", "open_pull_request":
			if ann["readOnlyHint"] != false || ann["destructiveHint"] != false || ann["idempotentHint"] != false {
				t.Fatalf("%s makes something new each call: %v", tl["name"], ann)
			}
			if tl["name"] == "open_pull_request" && (ann["openWorldHint"] != true || !strings.Contains(tl["description"].(string), "does not merge, approve or close anything")) {
				t.Fatalf("open_pull_request reaches another service, and leaves merging to people: %v", tl)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		switch tl["name"] {
		case "fork_machine":
			// a fork from a backup, at its size or another
			cpus, _ := props["cpus"].(map[string]any)
			if props["backup"] == nil || fmt.Sprint(cpus["enum"]) != "[2 4 8]" || !strings.Contains(tl["description"].(string), "with backup, from one of its backups") {
				t.Fatalf("fork_machine: %v", tl)
			}
		case "delete_machine":
			if !strings.Contains(tl["description"].(string), "all its files and its backups") {
				t.Fatalf("delete_machine says what goes with the machine: %v", tl["description"])
			}
		}
	}
	want := "search_docs list_machines create_machine get_machine run_command read_file write_file pause_machine start_machine stop_machine " +
		"fork_machine backup_machine list_backups delete_machine list_gpus list_regions list_secrets give_secrets set_network list_apps " +
		"turn_app_on turn_app_off show_app open_app take_screenshot use_screen list_repositories open_pull_request whoami " +
		"deploy_app list_deployments get_deployment redeploy delete_deployment deployment_logs"
	if strings.Join(names, " ") != want {
		t.Fatalf("tools %v", names)
	}
	if _, has := res["resultType"]; has {
		t.Fatal("a session begun with initialize is answered as its version is, with no resultType")
	}
	again := ok(t, s.request(3, "tools/list", nil))
	if a, b := compact(res), compact(again); a != b {
		t.Fatal("the list is the same every time")
	}
}

func TestListRepositoriesAndOpenPullRequest(t *testing.T) {
	f := fakeapi.New(t)
	web := f.AddMachine("web", api.StateRunning)
	f.MachineRepos = []api.MachineRepository{
		{Object: "machine_repository", Provider: "github", Host: "github.com", Repository: "acme/api", Access: "write", Linked: true, CanPush: true},
		{Object: "machine_repository", Provider: "gitlab", Host: "gitlab.com", Repository: "team/site", Access: "write", Linked: true, CanPush: true},
		{Object: "machine_repository", Provider: "github", Host: "github.com", Repository: "acme/docs", Access: "read", Linked: true},
	}
	f.Protected = []string{"main", "master", "release/**"}
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, list := s.tool(2, "list_repositories", map[string]any{"machine": "web"})
	data, _ := list["data"].([]any)
	if isErr || list["object"] != "list" || len(data) != 3 || fmt.Sprint(list["protected_branches"]) != "[main master release/**]" {
		t.Fatalf("the list: %v", list)
	}
	if row := data[0].(map[string]any); row["repository"] != "acme/api" || row["access"] != "write" || row["linked"] != true || row["can_push"] != true || row["push"] != nil {
		t.Fatalf("a repository: %v", row)
	}
	// on GitHub: opened, its number and address; what was asked, as it was given
	isErr, opened := s.tool(3, "open_pull_request", map[string]any{"machine": "web", "repository": "acme/api", "head": "agent/a", "base": "main", "title": "A change", "body": "Why.", "draft": true})
	if isErr || opened["opened"] != true || opened["number"] != 7.0 || opened["url"] != "https://github.com/acme/api/pull/7" || opened["draft"] != true {
		t.Fatalf("opened: %v", opened)
	}
	if asked := f.Pulls[web.ID]; len(asked) != 1 || asked[0].Repository != "acme/api" || asked[0].Head != "agent/a" || asked[0].Base != "main" || asked[0].Title != "A change" || asked[0].Body != "Why." || !asked[0].Draft {
		t.Fatalf("asked %+v", asked)
	}
	// on GitLab: nothing open yet, and the line that opens it
	isErr, lab := s.tool(4, "open_pull_request", map[string]any{"machine": web.ID, "repository": "team/site", "head": "agent/a", "base": "main", "title": "A change"})
	if isErr || lab["opened"] != false || lab["number"] != nil || !strings.HasPrefix(fmt.Sprint(lab["run"]), "git push -o merge_request.create") || lab["note"] == nil {
		t.Fatalf("on GitLab: %v", lab)
	}
	// what is missing said before anything is asked; a refusal as the API said it
	for i, args := range []map[string]any{{"repository": "acme/api", "head": "a", "base": "main", "title": "x"},
		{"machine": "web", "head": "a", "base": "main", "title": "x"}, {"machine": "web", "repository": "acme/api", "base": "main", "title": "x"},
		{"machine": "web", "repository": "acme/api", "head": "a", "title": "x"}, {"machine": "web", "repository": "acme/api", "head": "a", "base": "main"},
		{"machine": "web", "repository": "acme/api", "head": "a", "base": "main", "title": "x", "head_repo": "other/api"}} {
		isErr, body := s.tool(10+i, "open_pull_request", args)
		e, _ := body["error"].(map[string]any)
		if !isErr || e["code"] != api.CodeInvalidRequest || e["status"] != nil {
			t.Fatalf("%v: %v", args, body)
		}
	}
	if isErr, body := s.tool(20, "open_pull_request", map[string]any{"machine": "web", "repository": "acme/docs", "head": "a", "base": "main", "title": "x"}); !isErr || !strings.Contains(fmt.Sprint(body), "its team was given it to read") {
		t.Fatalf("one it reads: %v", body)
	}
}

func TestRunCommand(t *testing.T) {
	f := fakeapi.New(t)
	web := f.AddMachine("web", api.StateRunning)
	var asked api.ExecRequest
	f.Exec = func(_ string, req api.ExecRequest) api.ExecResult {
		asked = req
		return api.ExecResult{ExitCode: 2, Stdout: "hello\n", Stderr: "warn\n", DurationMS: 5}
	}
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, body := s.tool(2, "run_command", map[string]any{"machine": "web", "command": []string{"echo", "hello"}, "env": map[string]string{"A": "1"}})
	if isErr || body["exit_code"] != 2.0 || body["stdout"] != "hello\n" || body["stderr"] != "warn\n" || body["truncated"] != false {
		t.Fatalf("result %v", body)
	}
	if strings.Join(asked.Command, " ") != "echo hello" || asked.Env["A"] != "1" || asked.BinaryOutput {
		t.Fatalf("asked %+v", asked)
	}
	isErr, _ = s.tool(3, "run_command", map[string]any{"machine": web.ID, "shell": "ls | wc -l", "root": true})
	if isErr || asked.Shell != "ls | wc -l" || len(asked.Command) != 0 || asked.User != "root" {
		t.Fatalf("asked %+v", asked)
	}
	f.Exec = func(string, api.ExecRequest) api.ExecResult {
		return api.ExecResult{Stdout: strings.Repeat("a", 3000) + "END"}
	}
	_, body = s.tool(4, "run_command", map[string]any{"machine": "web", "shell": "yes a", "max_output_bytes": 1024})
	out := body["stdout"].(string)
	if body["truncated"] != true || !strings.HasSuffix(out, "END") || !strings.HasPrefix(out, "aaa") || !strings.Contains(out, "bytes left out") || len(out) > 1100 {
		t.Fatalf("long output keeps its start and end: %d bytes, %v", len(out), body["truncated"])
	}
}

func TestToolErrorsAreResultsWithTheAPIsCode(t *testing.T) {
	f := fakeapi.New(t)
	f.AddMachine("web", api.StateRunning)
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, body := s.tool(2, "get_machine", map[string]any{"machine": "m_00000000000000000000000077"})
	e, _ := body["error"].(map[string]any)
	if !isErr || e["status"] != 404.0 || e["code"] != "not_found" || e["detail"] != "no such machine" || !strings.HasPrefix(e["request_id"].(string), "req_") {
		t.Fatalf("error %v", body)
	}
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"run_command", map[string]any{"machine": "web", "command": []string{"ls"}, "shell": "ls"}, "give command or shell, not both"},
		{"run_command", map[string]any{"machine": "web"}, "say what to run"},
		{"run_command", map[string]any{"command": []string{"ls"}}, "give the machine"},
		{"get_machine", map[string]any{"machine": "web", "colour": "red"}, `no argument "colour" here`},
		{"list_machines", map[string]any{"limit": "ten"}, "limit must be a whole number"},
		{"list_machines", map[string]any{"limit": 500}, "limit is between 1 and 200"},
		{"list_backups", map[string]any{"limit": 0}, "limit is between 1 and 200"},
		{"read_file", map[string]any{"machine": "web", "path": "/etc/passwd"}, "path begins home/"},
		{"write_file", map[string]any{"machine": "web", "path": "work/a"}, "give the content"},
		{"get_machine", map[string]any{"machine": "nobody"}, `no machine has the id or name "nobody"`},
		{"whoami", map[string]any{"verbose": true}, `no argument "verbose" here`},
	} {
		isErr, body := s.tool(3, c.name, c.args)
		e, _ := body["error"].(map[string]any)
		if !isErr || e == nil || !strings.Contains(fmt.Sprint(e["detail"]), c.want) {
			t.Fatalf("%s %v: %v, want %q", c.name, c.args, body, c.want)
		}
	}
	f.Refuse = func(*http.Request) *fakeapi.Refusal {
		return &fakeapi.Refusal{Status: 402, Code: "payment_required", Detail: "the account's balance is used up; add to it to keep going"}
	}
	isErr, body = s.tool(4, "whoami", nil)
	if e := body["error"].(map[string]any); !isErr || e["code"] != "payment_required" || e["status"] != 402.0 {
		t.Fatalf("error %v", body)
	}
}

func TestAnUnknownToolIsRefusedAsARequest(t *testing.T) {
	s := start(t, connectTo(fakeapi.New(t)))
	s.initialize("2025-11-25")
	e := rpcErr(t, s.request(2, "tools/call", map[string]any{"name": "launch_rockets", "arguments": map[string]any{}}), codeInvalidParams)
	if e["message"] != "Unknown tool: launch_rockets" {
		t.Fatalf("error %v", e)
	}
	rpcErr(t, s.request(3, "tools/call", map[string]any{}), codeInvalidParams)
	rpcErr(t, s.request(4, "resources/list", nil), codeMethodNotFound)
}

func TestARequestWithNoVersionIsRefused(t *testing.T) {
	s := start(t, connectTo(fakeapi.New(t)))
	e := rpcErr(t, s.request(1, "tools/list", nil), codeInvalidParams)
	if !strings.Contains(e["message"].(string), "send initialize first") {
		t.Fatalf("error %v", e)
	}
	rpcErr(t, s.request(2, "prompts/list", nil), codeMethodNotFound)
	ok(t, s.request(3, "ping", nil))
}

func modernMeta() map[string]any {
	return map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/clientInfo": map[string]any{"name": "test", "version": "1"}}
}

func TestRequestsCarryingTheirOwnVersion(t *testing.T) {
	f := fakeapi.New(t)
	f.AddMachine("web", api.StateRunning)
	s := start(t, connectTo(f))

	res := ok(t, s.request(1, "server/discover", map[string]any{"_meta": modernMeta()}))
	if res["resultType"] != "complete" || fmt.Sprint(res["supportedVersions"]) != "[2026-07-28 2025-11-25 2025-06-18 2025-03-26 2024-11-05]" {
		t.Fatalf("discover %v", res)
	}
	if _, has := res["capabilities"].(map[string]any)["tools"]; !has || res["ttlMs"] == nil || res["cacheScope"] != "public" {
		t.Fatalf("discover %v", res)
	}
	if info := res["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"].(map[string]any); info["name"] != "wheelmachine" {
		t.Fatalf("serverInfo %v", info)
	}

	res = ok(t, s.request(2, "tools/list", map[string]any{"_meta": modernMeta()}))
	if res["resultType"] != "complete" || len(res["tools"].([]any)) != len(offered()) || res["cacheScope"] != "public" {
		t.Fatalf("tools/list %v", res)
	}
	call := map[string]any{"name": "run_command", "arguments": map[string]any{"machine": "web", "command": []string{"uname"}}, "_meta": modernMeta()}
	res = ok(t, s.request(3, "tools/call", call))
	if res["resultType"] != "complete" || res["isError"] != false {
		t.Fatalf("tools/call %v", res)
	}

	old := modernMeta()
	old["io.modelcontextprotocol/protocolVersion"] = "1999-01-01"
	e := rpcErr(t, s.request(4, "tools/list", map[string]any{"_meta": old}), codeUnsupportedVersion)
	data := e["data"].(map[string]any)
	if data["requested"] != "1999-01-01" || !strings.Contains(fmt.Sprint(data["supported"]), "2026-07-28") {
		t.Fatalf("error %v", e)
	}
	bare := modernMeta()
	delete(bare, "io.modelcontextprotocol/clientCapabilities")
	rpcErr(t, s.request(5, "tools/list", map[string]any{"_meta": bare}), codeInvalidParams)
}

func TestMessagesThatAreNotRight(t *testing.T) {
	s := start(t, connectTo(fakeapi.New(t)))
	s.send("this is not JSON")
	if m := s.recv(); m["id"] != nil || m["error"].(map[string]any)["code"] != float64(codeParse) {
		t.Fatalf("parse error %v", m)
	}
	s.send(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	if m := s.recv(); m["id"] != nil || m["error"].(map[string]any)["code"] != float64(codeInvalidRequest) {
		t.Fatalf("batch %v", m)
	}
	s.send(`{"jsonrpc":"1.0","id":5,"method":"ping"}`)
	if m := s.recv(); m["id"] != 5.0 || m["error"].(map[string]any)["code"] != float64(codeInvalidRequest) {
		t.Fatalf("version %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":null,"method":"ping"}`)
	if m := s.recv(); m["error"].(map[string]any)["code"] != float64(codeInvalidRequest) {
		t.Fatalf("null id %v", m)
	}
	s.send(``)
	s.send(`{"jsonrpc":"2.0","id":6,"result":{}}`)
	ok(t, s.request(7, "ping", nil))
}

func TestACancelledCallIsNotAnswered(t *testing.T) {
	f := fakeapi.New(t)
	f.AddMachine("web", api.StateRunning)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	f.Exec = func(string, api.ExecRequest) api.ExecResult {
		close(entered)
		<-release
		return api.ExecResult{Stdout: "late"}
	}
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	s.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"run_command","arguments":{"machine":"web","command":["sleep","60"]}}}`)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the command never ran")
	}
	ok(t, s.request(8, "ping", nil)) // a long call holds nothing else up
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"the person stopped it"}}`)
	time.Sleep(50 * time.Millisecond)
	ok(t, s.request(9, "ping", nil))
	select {
	case line := <-s.lines:
		t.Fatalf("a cancelled call was answered: %s", line)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFiles(t *testing.T) {
	f := fakeapi.New(t)
	web := f.AddMachine("web", api.StateRunning)
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, body := s.tool(2, "write_file", map[string]any{"machine": "web", "path": "work/notes/a.txt", "content": "héllo\n"})
	if got, _ := f.File(web.ID, "work/notes/a.txt"); isErr || string(got) != "héllo\n" || body["bytes"] != 7.0 {
		t.Fatalf("write %v, stored %q", body, got)
	}
	isErr, body = s.tool(3, "read_file", map[string]any{"machine": "web", "path": "work/notes/a.txt"})
	if isErr || body["content"] != "héllo\n" || body["encoding"] != "text" || body["size"] != 7.0 || body["truncated"] != false {
		t.Fatalf("read %v", body)
	}
	f.SetFile(web.ID, "home/blob", []byte{0xff, 0x00, 0x01})
	if _, body = s.tool(4, "read_file", map[string]any{"machine": "web", "path": "home/blob"}); body["encoding"] != "base64" || body["content"] != "/wAB" {
		t.Fatalf("read %v", body)
	}
	_, body = s.tool(5, "read_file", map[string]any{"machine": "web", "path": "work/notes/a.txt", "max_bytes": 2})
	if body["truncated"] != true || body["content"] != "h" || body["encoding"] != "text" {
		t.Fatalf("a character cut in two is left off: %v", body)
	}
	if isErr, body = s.tool(6, "read_file", map[string]any{"machine": "web", "path": "home/none"}); !isErr || body["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("read %v", body)
	}
}

func TestMachineTools(t *testing.T) {
	f := fakeapi.New(t)
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, m := s.tool(2, "create_machine", map[string]any{"name": "box", "labels": map[string]string{"team": "ml"}, "region": "mumbai"})
	if isErr || m["state"] != "running" || m["name"] != "box" {
		t.Fatalf("create %v", m)
	}
	id := m["id"].(string)
	for i, c := range []struct {
		name, state string
	}{{"pause_machine", "paused"}, {"start_machine", "running"}, {"stop_machine", "stopped"}, {"get_machine", "stopped"}} {
		if isErr, m := s.tool(10+i, c.name, map[string]any{"machine": "box"}); isErr || m["state"] != c.state || m["id"] != id {
			t.Fatalf("%s %v", c.name, m)
		}
	}
	if isErr, m := s.tool(20, "fork_machine", map[string]any{"machine": id, "name": "box-2"}); isErr || m["name"] != "box-2" || m["id"] == id {
		t.Fatalf("fork %v", m)
	}
	isErr, b := s.tool(21, "backup_machine", map[string]any{"machine": id, "note": "clean"})
	if isErr || b["object"] != "backup" || b["machine"] != id || b["note"] != "clean" || b["id"] == "" {
		t.Fatalf("backup %v", b)
	}
	isErr, page := s.tool(22, "list_machines", map[string]any{"limit": 1})
	if data := page["data"].([]any); isErr || len(data) != 1 || page["next"] == nil {
		t.Fatalf("list %v", page)
	}
	isErr, page = s.tool(23, "list_machines", map[string]any{"after": page["next"]})
	if data := page["data"].([]any); isErr || len(data) != 1 || page["next"] != nil {
		t.Fatalf("list after %v", page)
	}
	// its backups listed, and every one; a fork from its newest at another size, and what the API refuses of one
	isErr, list := s.tool(40, "list_backups", map[string]any{"machine": "box"})
	if data, _ := list["data"].([]any); isErr || list["object"] != "list" || len(data) != 1 || data[0].(map[string]any)["id"] != b["id"] {
		t.Fatalf("box's backups %v", list)
	}
	if isErr, list := s.tool(41, "list_backups", map[string]any{"limit": 1}); isErr || len(list["data"].([]any)) != 1 {
		t.Fatalf("every backup %v", list)
	}
	isErr, m = s.tool(42, "fork_machine", map[string]any{"machine": "box", "backup": "latest", "cpus": 8})
	if isErr || m["object"] != "machine" || m["name"] != "box-fork" || m["cpus"] != 8.0 || m["memory_gb"] != 16.0 {
		t.Fatalf("a fork from a backup at 8 CPU %v", m)
	}
	for _, c := range []struct {
		args       map[string]any
		code, want string
	}{
		{map[string]any{"machine": "box", "cpus": 8}, "invalid_request", "cpus is only for a fork from a backup: a fork of the machine as it is now keeps its size"},
		{map[string]any{"machine": "box", "backup": "20261023T120000Z-ffffffff"}, "not_found", "no such backup"},
		{map[string]any{"machine": "box-2", "backup": "latest"}, "conflict", "no backup of it yet: back it up first"},
	} {
		isErr, body := s.tool(43, "fork_machine", c.args)
		if e, _ := body["error"].(map[string]any); !isErr || e["code"] != c.code || e["detail"] != c.want {
			t.Fatalf("fork %v: %v", c.args, body)
		}
	}
	if isErr, m := s.tool(24, "delete_machine", map[string]any{"machine": id}); isErr || m["deleted"] != true || m["machine"] != id {
		t.Fatalf("delete %v", m)
	}
	if isErr, list := s.tool(28, "list_backups", map[string]any{}); isErr || list["object"] != "list" || len(list["data"].([]any)) != 0 {
		t.Fatalf("a deleted machine's backups go with it: %v", list)
	}
	// a machine kept running for work left in it, and one that waits an hour of idling before it pauses
	if isErr, m := s.tool(30, "create_machine", map[string]any{"name": "on", "sleep_when_idle": false}); isErr || m["sleep_when_idle"] != false || m["idle_after_s"] != nil {
		t.Fatalf("create %v", m)
	}
	if isErr, m := s.tool(31, "create_machine", map[string]any{"name": "long", "idle_after_s": 3600}); isErr || m["sleep_when_idle"] != true || m["idle_after_s"] != 3600.0 {
		t.Fatalf("create %v", m)
	}
	if isErr, m := s.tool(32, "create_machine", map[string]any{"name": "wide", "cpus": 8}); isErr || m["cpus"] != 8.0 || m["memory_gb"] != 16.0 {
		t.Fatalf("create %v", m)
	}
	// 16 is not offered yet: the API's own words come back to the agent
	if isErr, m := s.tool(33, "create_machine", map[string]any{"name": "huge", "cpus": 16}); !isErr || fmt.Sprint(m["error"].(map[string]any)["detail"]) != "16 CPUs are not offered yet: choose 2, 4 or 8" {
		t.Fatalf("create at 16 CPUs %v", m)
	}
	if isErr, g := s.tool(25, "list_gpus", map[string]any{}); isErr || g["for_sale"] != true || len(g["data"].([]any)) != 1 {
		t.Fatalf("gpus %v", g)
	}
	if isErr, r := s.tool(26, "list_regions", nil); isErr || len(r["data"].([]any)) != 2 || r["next"] != nil {
		t.Fatalf("regions %v", r)
	}
	if isErr, me := s.tool(27, "whoami", map[string]any{}); isErr || me["account"].(map[string]any)["id"] != "acc_test" {
		t.Fatalf("whoami %v", me)
	}
}

// In an organisation a member's fork waits for an OK: fork_machine makes no machine and answers the request, with a note
// saying what it waits for - the OK of the person who made the key first, asked with a key, then a full manager's, an owner's or an admin's.
// A full manager's own fork is made at once, and answered as the machine it is.
func TestAMembersForkWaits(t *testing.T) {
	f := fakeapi.New(t)
	f.Org = &fakeapi.Org{ID: "acc_00000000000000000000000077", Name: "Acme", Role: api.RoleMember, Teams: []string{f.Project()},
		Person: api.Person{ID: "acc_0000000000000000000000ada1", Name: "Ada Lovelace"}}
	f.AddMachine("box", "running")
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, q := s.tool(2, "fork_machine", map[string]any{"machine": "box", "name": "box-2"})
	note, _ := q["note"].(string)
	if isErr || q["object"] != "request" || q["kind"] != "fork" || q["state"] != "waiting" || !strings.HasPrefix(q["id"].(string), "rq_") ||
		!strings.HasPrefix(note, "No machine is made yet: the fork waits for the OK of the person who made the key, signed in to the dashboard") ||
		!strings.Contains(note, "then for the OK of a full manager of the machine's team, an owner or an admin") {
		t.Fatalf("a member's fork: %v", q)
	}
	if _, page := s.tool(3, "list_machines", map[string]any{}); len(page["data"].([]any)) != 1 {
		t.Fatalf("a fork made while it waits: %v", page)
	}
	f.Approve(q["id"].(string))
	isErr, again := s.tool(4, "fork_machine", map[string]any{"machine": "box"})
	if note, _ := again["note"].(string); isErr || again["id"] != q["id"] || strings.Contains(note, "dashboard") ||
		!strings.Contains(note, "the fork waits for the OK of a full manager of the machine's team, an owner or an admin") {
		t.Fatalf("asked again once the person who made the key approved it: %v", again)
	}
	// from a backup too, and the request says which backup, at the size asked
	f.AddMachine("db", "running")
	_, b := s.tool(6, "backup_machine", map[string]any{"machine": "db"})
	isErr, q = s.tool(7, "fork_machine", map[string]any{"machine": "db", "backup": "latest", "cpus": 8})
	if note, _ := q["note"].(string); isErr || q["object"] != "request" || q["backup"] != b["id"] || q["cpus"] != 8.0 ||
		!strings.HasPrefix(note, "No machine is made yet") {
		t.Fatalf("a member's fork from a backup: %v", q)
	}
	f.Org.Levels = map[string]string{f.Project(): api.LevelFull}
	if isErr, m := s.tool(5, "fork_machine", map[string]any{"machine": "box"}); isErr || m["object"] != "machine" || m["name"] != "box-fork" || m["note"] != nil {
		t.Fatalf("a full manager's own fork: %v", m)
	}
}

// The apps tools: a machine's apps listed, one turned on with its link and token, a fresh link, and one turned off;
// what is not an app's name is refused before any call, and an old machine says so in the API's words.
func TestAppTools(t *testing.T) {
	f := fakeapi.New(t)
	box := f.AddMachine("box", "running")
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, list := s.tool(2, "list_apps", map[string]any{"machine": "box"})
	if data, _ := list["data"].([]any); isErr || list["object"] != "list" || len(data) != 6 || data[0].(map[string]any)["name"] != "desktop" ||
		data[4].(map[string]any)["kind"] != "agent" {
		t.Fatalf("list %v", list)
	}
	// an agent whose session began before the machine's AI keys changed says so, as the API says it; no other app does
	codex, yes := f.Apps(box.ID)[4], true
	codex.Running, codex.KeysChanged = &yes, &yes
	f.SetApp(box.ID, codex)
	isErr, again := s.tool(7, "list_apps", map[string]any{"machine": "box"})
	if data, _ := again["data"].([]any); isErr || len(data) != 6 || data[4].(map[string]any)["keys_changed"] != true ||
		data[0].(map[string]any)["keys_changed"] != nil {
		t.Fatalf("list %v", again)
	}
	isErr, on := s.tool(3, "turn_app_on", map[string]any{"machine": "box", "app": "openclaw", "access_s": 600})
	if isErr || on["on"] != true || on["token"] != fakeapi.AppToken || !strings.HasPrefix(on["link"].(string), "https://18789-") || on["access_s"] != 600.0 {
		t.Fatalf("on %v", on)
	}
	if req, _ := f.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/openclaw/on"); string(req.Body) != `{"access_s":600}` {
		t.Fatalf("sent %s", req.Body)
	}
	isErr, opened := s.tool(4, "open_app", map[string]any{"machine": box.ID, "app": "openclaw"})
	if isErr || opened["link"] == on["link"] || opened["token"] != nil {
		t.Fatalf("open %v", opened)
	}
	if isErr, shown := s.tool(6, "show_app", map[string]any{"machine": "box", "app": "openclaw"}); isErr || shown["name"] != "openclaw" || shown["link"] != nil {
		t.Fatalf("show %v", shown)
	}
	if _, ok := f.Last(http.MethodPost, "/v1/machines/"+box.ID+"/apps/openclaw/show"); !ok {
		t.Fatal("the app was not shown")
	}
	if isErr, off := s.tool(5, "turn_app_off", map[string]any{"machine": "box", "app": "openclaw"}); isErr || off["on"] != false {
		t.Fatalf("off %v", off)
	}
	if ports := f.Ports(box.ID); len(ports) != 0 {
		t.Fatalf("ports %+v", ports)
	}
	before := len(f.Requests())
	for i, bad := range []map[string]any{{"machine": "box", "app": "Desktop"}, {"machine": "box"}, {"machine": "box", "app": "desktop", "access_s": 30},
		{"app": "desktop"}} {
		if isErr, body := s.tool(10+i, "turn_app_on", bad); !isErr || body["error"].(map[string]any)["code"] != "invalid_request" {
			t.Fatalf("%v: %v", bad, body)
		}
	}
	if isErr, body := s.tool(20, "turn_app_off", map[string]any{"machine": "box", "app": "desktop", "access_s": 600}); !isErr ||
		body["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("off with access_s: %v", body)
	}
	if len(f.Requests()) != before {
		t.Fatal("arguments that are not right were sent")
	}
	f.NoApps = true
	isErr, body := s.tool(21, "list_apps", map[string]any{"machine": "box"})
	if e, _ := body["error"].(map[string]any); !isErr || e["code"] != "no_apps" || e["status"] != 409.0 || !strings.Contains(e["detail"].(string), "has no apps") {
		t.Fatalf("an old machine: %v", body)
	}
}

func TestNoKeySaysWhereOneComesFrom(t *testing.T) {
	s := start(t, func() (*api.Client, error) { return nil, api.ErrNoKey })
	s.initialize("2025-11-25")
	isErr, body := s.tool(2, "whoami", nil)
	e := body["error"].(map[string]any)
	if !isErr || e["code"] != "unauthorized" || !strings.Contains(e["detail"].(string), "WHEELMACHINE_API_KEY") {
		t.Fatalf("error %v", body)
	}
	if isErr, body := s.tool(3, "run_command", map[string]any{"machine": "web"}); !isErr || !strings.Contains(fmt.Sprint(body), "say what to run") {
		t.Fatalf("arguments are checked before the key is needed: %v", body)
	}
}

func TestClip(t *testing.T) {
	if s, cut := clip("short", 10); s != "short" || cut {
		t.Fatal("short output is left whole")
	}
	s, cut := clip(strings.Repeat("é", 1000), 1024)
	if !cut || !strings.HasPrefix(s, "éé") || !strings.HasSuffix(s, "éé") || !strings.Contains(s, "bytes left out") {
		t.Fatalf("clip %q", s[:20])
	}
	if !json.Valid([]byte(compact(s))) || strings.ContainsRune(s, '�') {
		t.Fatal("cut on whole characters")
	}
}

func TestLongLinesAreRefusedAndReadingGoesOn(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 300)+"\n"+`{"ok":1}`+"\n"), 16)
	if _, err := readMessage(r, 100); err != errTooLarge {
		t.Fatalf("err %v", err)
	}
	line, err := readMessage(r, 100)
	if err != nil || string(line) != `{"ok":1}` {
		t.Fatalf("line %q, %v", line, err)
	}
}

func TestAKeyThatStopsWorkingIsReadAgain(t *testing.T) {
	f := fakeapi.New(t)
	connects := 0
	good := f.Key
	key := "ak_live_" + strings.Repeat("r", 40)
	s := start(t, func() (*api.Client, error) {
		connects++
		return api.New(api.WithBaseURL(f.URL), api.WithKey(key), api.WithRetryPolicy(api.RetryPolicy{MaxAttempts: 1}))
	})
	s.initialize("2025-11-25")
	if isErr, body := s.tool(2, "whoami", nil); !isErr || body["error"].(map[string]any)["code"] != "unauthorized" {
		t.Fatalf("whoami %v", body)
	}
	key = good // as if `wm login` kept a working key meanwhile
	if isErr, body := s.tool(3, "whoami", nil); isErr || connects != 2 {
		t.Fatalf("whoami %v after %d connects", body, connects)
	}
	s.tool(4, "whoami", nil)
	if connects != 2 {
		t.Fatalf("a working client is kept: %d connects", connects)
	}
}

// The secrets tools name secrets and never carry a value: none takes one, and none answers one.
func TestSecretToolsNameSecretsAndNeverCarryAValue(t *testing.T) {
	f := fakeapi.New(t)
	const value = "made-up-value-91c2"
	c, err := connectTo(f)()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(context.Background(), "API_TOKEN", value); err != nil {
		t.Fatal(err)
	}
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	list := ok(t, s.request(2, "tools/list", nil))["tools"].([]any)
	for _, raw := range list {
		tl := raw.(map[string]any)
		props := tl["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if _, takes := props["value"]; takes {
			t.Fatalf("%s takes a value", tl["name"])
		}
	}
	isErr, m := s.tool(3, "create_machine", map[string]any{"name": "box", "secrets": []string{"API_TOKEN"}})
	if isErr || fmt.Sprint(m["secrets"]) != "[API_TOKEN]" {
		t.Fatalf("create %v", m)
	}
	isErr, got := s.tool(4, "list_secrets", map[string]any{})
	data, _ := got["data"].([]any)
	if isErr || len(data) != 1 || data[0].(map[string]any)["name"] != "API_TOKEN" || fmt.Sprint(data[0].(map[string]any)["machines"]) != "["+m["id"].(string)+"]" {
		t.Fatalf("list %v", got)
	}
	if isErr, m := s.tool(5, "give_secrets", map[string]any{"machine": "box", "secrets": []string{}}); isErr || fmt.Sprint(m["secrets"]) != "[]" {
		t.Fatalf("give none %v", m)
	}
	if isErr, m := s.tool(6, "give_secrets", map[string]any{"machine": "box", "secrets": []string{"API_TOKEN"}}); isErr || fmt.Sprint(m["secrets"]) != "[API_TOKEN]" {
		t.Fatalf("give %v", m)
	}
	for i, args := range []map[string]any{
		{"machine": "box", "secrets": []string{"NOT_SET"}}, // refused by the API
		{"machine": "box"}, {"machine": "box", "secrets": []string{"lower"}}, // and before it is asked
	} {
		isErr, body := s.tool(7+i, "give_secrets", args)
		if e, _ := body["error"].(map[string]any); !isErr || e["code"] != "invalid_request" {
			t.Fatalf("%v: %v", args, body)
		}
	}
	for _, r := range f.Requests() {
		if r.Method == "PUT" && r.Path == "/v1/secrets/API_TOKEN" {
			continue // the one call that set it, by the client, before the server began
		}
		if strings.Contains(string(r.Body), value) || strings.Contains(r.Path, value) || strings.Contains(r.Query.Encode(), value) {
			t.Fatalf("a call through the tools carried the value: %s %s", r.Method, r.Path)
		}
	}
}

// list_secrets shows an AI key as the API keeps it: its preset, the hosts it is sent to and that it is on every machine;
// and a machine says the keys that reach it. Never the key.
func TestListSecretsShowsTheAIKeysAndWhereTheyGo(t *testing.T) {
	f := fakeapi.New(t)
	const value = "made-up-ai-key-0f3b"
	c, err := connectTo(f)()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(context.Background(), "ANTHROPIC_API_KEY", value); err != nil {
		t.Fatal(err)
	}
	s := start(t, connectTo(f))
	s.initialize("2025-11-25")
	isErr, got := s.tool(2, "list_secrets", map[string]any{})
	data, _ := got["data"].([]any)
	if isErr || len(data) != 1 {
		t.Fatalf("list %v", got)
	}
	key := data[0].(map[string]any)
	outside, _ := key["outside"].(map[string]any)
	if key["every_machine"] != true || outside == nil || outside["preset"] != "anthropic" || fmt.Sprint(outside["hosts"]) != "[api.anthropic.com]" {
		t.Fatalf("the AI key listed as %v", key)
	}
	isErr, m := s.tool(3, "create_machine", map[string]any{"name": "box"})
	if isErr || fmt.Sprint(m["ai_keys"]) != "[ANTHROPIC_API_KEY]" {
		t.Fatalf("the machine's AI keys: %v", m)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), value) {
		t.Fatal("the key is in what list_secrets answered")
	}
}

// An agent sees a machine's desktop as a real picture, an image block beside its words, and works it: as the hosted server's
// tools do, the API's own words for what it refuses.
func TestTheScreenTools(t *testing.T) {
	fake := fakeapi.New(t)
	fake.AddMachine("box", "running")
	s := start(t, connectTo(fake))
	s.initialize("2025-11-25")
	res := ok(t, s.request(2, "tools/call", map[string]any{"name": "take_screenshot",
		"arguments": map[string]any{"machine": "box", "scale": 0.25, "format": "jpeg", "region": map[string]any{"x": 1, "y": 2, "w": 30, "h": 40}}}))
	blocks := res["content"].([]any)
	if len(blocks) != 2 || res["isError"] != false {
		t.Fatalf("a picture: %.300v", res)
	}
	image, words := blocks[0].(map[string]any), blocks[1].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/jpeg" || image["data"] == "" || image["text"] != nil {
		t.Fatalf("its image block: %.300v", image)
	}
	if words["type"] != "text" || words["data"] != nil || words["text"] != `{"width":720,"height":450,"scale":0.25,"format":"jpeg"}` {
		t.Fatalf("its words: %v", words)
	}
	if q := fake.ScreenAsked; q.Get("scale") != "0.25" || q.Get("format") != "jpeg" || q.Get("x") != "1" || q.Get("y") != "2" || q.Get("w") != "30" || q.Get("h") != "40" {
		t.Fatalf("asked with %v", q)
	}
	res = ok(t, s.request(3, "tools/call", map[string]any{"name": "use_screen", "arguments": map[string]any{"machine": "box", "scale": 1, "then_screenshot": true,
		"settle_ms": 100, "actions": []map[string]any{{"type": "click", "x": 3, "y": 4, "button": "right", "count": 2}, {"type": "keys", "keys": []string{"ctrl", "c"}}, {"type": "type", "text": "héllo"}}}}))
	blocks = res["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["mimeType"] != "image/png" ||
		blocks[1].(map[string]any)["text"] != `{"done":3,"width":2880,"height":1800,"scale":1,"format":"png"}` {
		t.Fatalf("after working it: %.400v", res)
	}
	got := fake.ScreenActions
	if !got.Picture || *got.Scale != 1 || *got.SettleMS != 100 || len(got.Actions) != 3 || *got.Actions[0].X != 3 || got.Actions[0].Button != "right" || *got.Actions[0].Count != 2 ||
		got.Actions[1].Keys[1] != "c" || *got.Actions[2].Text != "héllo" {
		t.Fatalf("it was sent %+v", got)
	}
	// with no picture, words alone
	if isErr, body := s.tool(4, "use_screen", map[string]any{"machine": "box", "actions": []map[string]any{{"type": "wait", "ms": 1}}}); isErr || body["done"] != 1.0 {
		t.Fatalf("no picture: %v", body)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"take_screenshot", map[string]any{}, "machine"},
		{"take_screenshot", map[string]any{"machine": "box", "format": "gif"}, "format is png or jpeg"},
		{"take_screenshot", map[string]any{"machine": "box", "colour": "red"}, "no argument"},
		{"use_screen", map[string]any{"machine": "box"}, "1 to 64 actions"},
		{"use_screen", map[string]any{"machine": "box", "actions": []map[string]any{{"type": "wait", "ms": "soon"}}}, "must be"},
		{"use_screen", map[string]any{"machine": "box", "actions": []map[string]any{{"type": "wait", "colour": "red"}}}, "no argument"},
	} {
		isErr, body := s.tool(5, tc.tool, tc.args)
		if !isErr || !strings.Contains(fmt.Sprint(body), tc.want) {
			t.Errorf("%s %v: %v", tc.tool, tc.args, body)
		}
	}
}
