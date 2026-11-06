package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"wheelmachine/frontend/cli/api"
)

// tool is a tool as tools/list shows it, and what calling it does.
type tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations annotations     `json:"annotations"`

	run func(ctx context.Context, client func() (*api.Client, error), args json.RawMessage) (any, error)
}

// annotations say how a tool behaves, as hints for the client.
type annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

var (
	reads        = annotations{ReadOnlyHint: true, IdempotentHint: true}
	makes        = annotations{}                                            // adds something new each call
	changes      = annotations{IdempotentHint: true}                        // to a state; again is the same
	destroys     = annotations{DestructiveHint: true, IdempotentHint: true} // takes something away
	runsAnything = annotations{DestructiveHint: true, OpenWorldHint: true}  // a command can do anything, reach anywhere
)

// checker is arguments that check themselves before anything is called.
type checker interface{ check() error }

// define makes a tool whose arguments decode into an A, are checked, and are then given to do with the client.
func define[A any](name, title, description string, schema json.RawMessage, ann annotations,
	do func(ctx context.Context, c *api.Client, a *A) (any, error)) tool {
	ann.Title = title
	return tool{Name: name, Title: title, Description: description, InputSchema: schema, Annotations: ann,
		run: func(ctx context.Context, client func() (*api.Client, error), raw json.RawMessage) (any, error) {
			var a A
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			if ch, ok := any(&a).(checker); ok {
				if err := ch.check(); err != nil {
					return nil, err
				}
			}
			c, err := client()
			if err != nil {
				return nil, err
			}
			return do(ctx, c, &a)
		}}
}

// object is a JSON Schema for arguments: these properties, these required, no others.
func object(required []string, props ...string) json.RawMessage {
	s := `{"type":"object","properties":{` + strings.Join(props, ",") + `}`
	if len(required) > 0 {
		s += `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return json.RawMessage(s + `,"additionalProperties":false}`)
}

const (
	pMachine = `"machine":{"type":"string","minLength":1,"description":"The machine's id (m_...) or its name."}`
	pName    = `"name":{"type":"string","maxLength":80,"description":"A name for people; ids are made by the API."}`
	pLabels  = `"labels":{"type":"object","maxProperties":32,"additionalProperties":{"type":"string","maxLength":200},"description":"Labels, from name to value."}`
	pWaitS   = `"wait_s":{"type":"number","minimum":0,"maximum":600,"default":60,"description":"How long to wait for it to run before answering with state starting, in seconds."}`
	pPath    = `"path":{"type":"string","maxLength":1024,"pattern":"^(home|work)/","description":"A path in the machine: home/... (its home) or work/... (where commands start)."}`
	pSecrets = `"secrets":{"type":"array","maxItems":200,"uniqueItems":true,"items":{"type":"string","pattern":"^[A-Z_][A-Z0-9_]{0,63}$"},"description":"Names of the account's secrets (list_secrets) that every command run in the machine gets as environment variables. Needs a key with the cloud:secrets:write scope, of the whole account."}`
	pApp     = `"app":{"type":"string","pattern":"^[a-z0-9-]{1,32}$","description":"The app's name, as list_apps gives it, such as desktop."}`
	pAccess  = `"access_s":{"type":"integer","minimum":60,"maximum":3600,"default":1800,"description":"How long the browser that opens the link may reach the app, in seconds."}`
	pNetwork = `"network":{"type":"object","additionalProperties":false,"required":["internet"],"properties":{"internet":{"type":"string","enum":["open","closed","allowed"],"description":"open (the whole internet), closed (no internet, though name lookups and some sites we host still work), or allowed (only what allow lists)."},"allow":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":253},"description":"For allowed only: domain names (exact, or a leading *. for every name under it) for HTTP and HTTPS, and IPv4/IPv6 ranges (CIDRs) for other ports."}},"description":"What it may reach on the internet."}`
)

func tools() []tool {
	return []tool{
		define("list_machines", "List machines",
			"List the account's machines, newest first, a page at a time; narrow by project, state or labels. Answers "+
				`{"object":"list","data":[machine...],"next":...}: give next as after for the page after it; next is null on the last page.`,
			object(nil,
				`"project":{"type":"string","description":"Only this project's machines: its id (prj_...) or its name."}`,
				`"state":{"type":"string","enum":["starting","running","paused","stopped","archived","recovering","failed"],"description":"Only machines in this state; failed lists the ones that never came up, each with its failure saying why."}`,
				`"labels":{"type":"object","additionalProperties":{"type":"string"},"description":"Only machines carrying every one of these labels."}`,
				`"limit":{"type":"integer","minimum":1,"maximum":200,"default":50,"description":"Machines on the page."}`,
				`"after":{"type":"string","description":"The next of the page before."}`),
			reads, listMachines),
		define("create_machine", "Make a machine",
			"Make a machine and answer with it once it runs, usually in about a second; every argument may be left "+
				"out. If that takes longer than wait_s, it is answered with "+
				"state starting: follow it with get_machine. A GPU machine can take minutes: it is answered "+
				"so within about a second; one that never comes up says state failed, and failure says why. It is charged by "+
				"the second while it runs, and pauses by itself "+
				"once it is idle - nothing has reached it for idle_after_s and its programs have been quiet as long, so work "+
				"left running in the background keeps it going; with sleep_when_idle false it never pauses by itself.",
			object(nil, pName,
				`"project":{"type":"string","description":"Its project: an id (prj_...) or a name; the account's first when left out."}`,
				`"region":{"type":"string","description":"A region id from list_regions to run near."}`,
				`"gpu":{"type":"string","description":"A GPU id from list_gpus; needs a key with the cloud:gpus scope."}`,
				`"cpus":{"type":"integer","enum":[2,4,8],"default":2,"description":"Its CPUs, with 2 GB of memory each; not with gpu, whose size comes with it."}`,
				pLabels,
				`"sleep_when_idle":{"type":"boolean","default":true,"description":"False: it never pauses by itself, and runs (charged) until it is paused or stopped."}`,
				`"idle_after_s":{"type":"integer","minimum":60,"maximum":86400,"default":300,"description":"How long it must be idle before it pauses by itself."}`,
				pSecrets, pNetwork, pWaitS),
			makes, createMachine),
		define("get_machine", "Get a machine",
			"One machine as it is now: its state, size, location, labels and running time.",
			object([]string{"machine"}, pMachine), reads, getMachine),
		define("run_command", "Run a command",
			"Run a command in a machine and answer with its exit_code, stdout and stderr. Give command (the program and its "+
				"arguments, run without a shell) or shell (a line for sh -lc, with pipes and redirections), not both. A paused "+
				"machine is woken for it. A command may run 10 minutes: start longer work in the background and read its "+
				"output from a file. A non-zero exit_code is the command's own answer, not an error. Long output is cut, and "+
				"truncated says so: write big output to a file and read the part you need.",
			object([]string{"machine"}, pMachine,
				`"command":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":4096,"description":"The program and its arguments, such as [\"python3\", \"-c\", \"print(2+2)\"]."}`,
				`"shell":{"type":"string","minLength":1,"maxLength":131072,"description":"A line of shell, such as \"ls -la | wc -l\"."}`,
				`"cwd":{"type":"string","description":"Where it starts, an absolute path; the work folder when left out."}`,
				`"env":{"type":"object","maxProperties":256,"additionalProperties":{"type":"string"},"description":"Environment variables for it."}`,
				`"root":{"type":"boolean","default":false,"description":"Run it as root, inside the machine only."}`,
				`"max_output_bytes":{"type":"integer","minimum":1024,"maximum":8388608,"default":65536,"description":"The most of each of stdout and stderr given back."}`),
			runsAnything, runCommand),
		define("read_file", "Read a file",
			"Read a file in a machine. Text comes as it is (encoding text); any other bytes come as base64 (encoding "+
				"base64). At most max_bytes are given back, and truncated says when the file holds more.",
			object([]string{"machine", "path"}, pMachine, pPath,
				`"max_bytes":{"type":"integer","minimum":1,"maximum":8388608,"default":1048576,"description":"The most bytes given back."}`),
			reads, readFile),
		define("write_file", "Write a file",
			"Write text to a file in a machine, making the folders on the way; a file already there is replaced.",
			object([]string{"machine", "path", "content"}, pMachine, pPath,
				`"content":{"type":"string","description":"The file's text, whole."}`),
			destroys, writeFile),
		define("pause_machine", "Pause a machine",
			"Pause a machine with its memory kept: nothing runs and no running time is charged. start_machine brings it back where it was.",
			object([]string{"machine"}, pMachine), changes, pauseMachine),
		define("start_machine", "Start a machine",
			"Run a paused, stopped or archived machine again, and answer once it runs; when that takes longer than "+
				"wait_s, it is answered with state starting.",
			object([]string{"machine"}, pMachine, pWaitS), changes, startMachine),
		define("stop_machine", "Stop a machine",
			"Stop a machine: its files stay; its memory and what was running do not. start_machine runs it again.",
			object([]string{"machine"}, pMachine), destroys, stopMachine),
		define("fork_machine", "Fork a machine",
			"Fork a machine: a new machine of its own, made from it as it is now - or, with "+
				"backup, from one of its backups, starting afresh from its files, at the original's size or the one cpus asks. "+
				"Its name is the original's with -fork after it, and its labels the original's, unless given. In an "+
				"organisation, a member's fork is not made at once: it waits for the OK of a full manager of the machine's "+
				"team, an owner or an admin - and first for the OK of the person who made the key in the dashboard, as this server asks with a "+
				`key. The answer is then the request ("object":"request", its id rq_..., not a machine's), with a note `+
				"saying what it waits for; the fork is made, named, labelled and sized as asked, once it is approved.",
			object([]string{"machine"}, pMachine, pName, pLabels,
				`"backup":{"type":"string","minLength":1,"description":"One of the machine's backups to make the fork from: its id (list_backups), or latest for its newest. Left out, the fork is of the machine as it is now."}`,
				`"cpus":{"type":"integer","enum":[2,4,8],"description":"Only with backup: the fork's CPUs, with 2 GB of memory each; the original's when left out. A fork of the machine as it is now is always the original's size. Not for a GPU machine, whose size comes with its GPU."}`,
				pWaitS),
			makes, forkMachine),
		define("backup_machine", "Back up a machine",
			"Keep a machine's files as they are now, not what runs in it: fork_machine with backup makes a new machine from "+
				"them later, at the machine's size or another. Answers the backup, with the id to give fork_machine.",
			object([]string{"machine"}, pMachine, `"note":{"type":"string","maxLength":200,"description":"A note to know it by."}`),
			makes, backupMachine),
		define("list_backups", "List backups",
			"Backups, newest first: a machine's, or with no machine given, those of every machine this key reaches. Give an "+
				`id to fork_machine as backup. Answers {"object":"list","data":[backup...]}.`,
			object(nil,
				`"machine":{"type":"string","minLength":1,"description":"Only this machine's backups: its id (m_...) or its name."}`,
				`"limit":{"type":"integer","minimum":1,"maximum":200,"default":100,"description":"The most backups given."}`),
			reads, listBackups),
		define("delete_machine", "Delete a machine",
			"Delete a machine, all its files and its backups, for good: this cannot be undone.",
			object([]string{"machine"}, pMachine), destroys, deleteMachine),
		define("list_gpus", "List GPUs",
			"GPUs on offer now: id, model, memory, where, price an hour (in the account's currency, and in US dollars) and whether one is free. for_sale is false while GPU "+
				"machines are not open to every account. Give an id to create_machine as gpu.",
			object(nil), reads, listGPUs),
		define("list_regions", "List regions",
			"Places you can ask a machine to run in; give an id to create_machine as region. A machine says where it "+
				"actually runs in its location.",
			object(nil), reads, listRegions),
		define("list_secrets", "List secrets",
			"The account's secrets by name, each with the machines given it - never a value: values are set by a person "+
				"(wm secrets set, or the API), never through these tools. A machine's commands get each secret it was given "+
				"as an environment variable of that name; a variable given to run_command in env wins over a secret of the "+
				"same name. One kept outside the machines (outside) holds only a placeholder in a machine, and its value is "+
				"sent to outside.hosts alone. An AI key - outside.preset set, such as ANTHROPIC_API_KEY or OPENAI_API_KEY - "+
				"is on every machine (every_machine) unless it was set for the machines given it: a machine's ai_keys names "+
				"the ones that reach it, and the agents in it use them as they are.",
			object(nil), reads, listSecrets),
		define("give_secrets", "Give a machine secrets",
			"Say which of the account's secrets a machine gets, by their names: every command run in it gets each as an "+
				"environment variable of that name. The names given take the place of the ones it had; an empty list takes "+
				"them all away. Each must be set already (list_secrets). Answers with the machine.",
			object([]string{"machine", "secrets"}, pMachine,
				`"secrets":{"type":"array","maxItems":200,"uniqueItems":true,"items":{"type":"string","pattern":"^[A-Z_][A-Z0-9_]{0,63}$"},"description":"Every secret it is to get, by name; [] for none. Giving needs a key with the cloud:secrets:write scope, of the whole account."}`),
			changes, giveSecrets),
		define("set_network", "Set a machine's internet",
			"Set what a machine may reach on the internet - open (the whole internet), closed (no internet, though "+
				"name lookups and some sites we host still work), or allowed only to the names and ranges you "+
				"list. It takes effect at once, without a restart. "+
				"Answers with the machine.",
			object([]string{"machine", "network"}, pMachine, pNetwork),
			changes, setNetwork),
		define("list_apps", "List a machine's apps",
			"The apps a machine carries and whether each is on: web apps (a desktop, a code editor, an agent's own page) "+
				"reached at the machine's private web address for their port, services reached from inside the machine "+
				"(a model server at its own address there), and agents that each keep a session of their own in the "+
				"machine's terminal. An agent with keys_changed true began its session before the machine's AI keys changed: "+
				"turning it off and on again starts it with them, in a new session. A new machine runs them by default, but "+
				"the desktop and Gemini CLI, which stay off until turned on; the code editor is always on. Answers "+
				`{"object":"list","data":[app...]}.`,
			object([]string{"machine"}, pMachine), reads, listApps),
		define("turn_app_on", "Turn an app on",
			"Turn an app on; it may take up to a minute. A web app answers a link that lets one browser into its private address once, within five "+
				"minutes, landing on its page: give it to the person, and do not keep it. An app with a token of its own "+
				"answers it this once, in token. Each call makes a new link.",
			object([]string{"machine", "app"}, pMachine, pApp, pAccess), makes, turnAppOn),
		define("turn_app_off", "Turn an app off",
			"Turn an app off, which frees the machine's memory. A web app's address is closed with it when it was opened "+
				"for the app and is still private; an agent's session ends. The code editor (editor) is always on: turning "+
				"it off is refused (conflict).",
			object([]string{"machine", "app"}, pMachine, pApp), changes, turnAppOff),
		define("show_app", "Show an app on the machine's screen",
			"Open an app on the machine's own desktop - its window, signed in where it signs in - so a person who "+
				"watches the machine's screen in the dashboard sees it there, with nothing to open or approve. It may "+
				"take up to two minutes. Answers with the app.",
			object([]string{"machine", "app"}, pMachine, pApp), changes, showApp),
		define("open_app", "Open a web app",
			"A fresh link into a web app that is on - its address opened private first when it is not open: it lets one "+
				"browser into the app's private address once, within five minutes, landing on its page. Give it to the "+
				"person, and do not keep it. Each call makes a new link.",
			object([]string{"machine", "app"}, pMachine, pApp, pAccess), makes, openApp),
		define("whoami", "Who am I",
			"The account and API key in use: the key's scopes and projects, and whether the account can spend.",
			object(nil), reads, whoami),
	}
}

// inputError is arguments that are not right: the model can put them right.
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func inputf(format string, args ...any) error { return &inputError{msg: fmt.Sprintf(format, args...)} }

// decodeArgs reads a tool's arguments: a JSON object with no argument the tool does not know.
func decodeArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			if te.Field == "" {
				return inputf("the arguments are a JSON object")
			}
			return inputf("%s must be %s", te.Field, kindOf(te.Type))
		}
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return inputf("no argument %s here", name)
		}
		return inputf("the arguments are not a JSON object")
	}
	if dec.More() {
		return inputf("the arguments are one JSON object")
	}
	return nil
}

func kindOf(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int64:
		return "a whole number"
	case reflect.Float64:
		return "a number"
	case reflect.Slice:
		return "an array of " + strings.TrimPrefix(kindOf(t.Elem()), "a ") + "s"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return "something else"
}

// failure is what a tool answers when it fails: the API's status, code, detail and request id, as it gave them.
type failure struct {
	Status      int     `json:"status,omitempty"`
	Code        string  `json:"code"`
	Detail      string  `json:"detail"`
	RequestID   string  `json:"request_id,omitempty"`
	RetryAfterS float64 `json:"retry_after_s,omitempty"`
}

func failureOf(err error) failure {
	var ie *inputError
	var ae *api.Error
	var ce *api.ConnError
	switch {
	case errors.As(err, &ie):
		return failure{Code: api.CodeInvalidRequest, Detail: ie.msg}
	case errors.Is(err, api.ErrNoKey):
		return failure{Code: api.CodeUnauthorized,
			Detail: "no API key: set " + api.EnvAPIKey + " in this server's environment, or run `wm login` on this computer"}
	case errors.As(err, &ae):
		f := failure{Status: ae.Status, Code: ae.Code, Detail: ae.Detail, RequestID: ae.RequestID}
		if f.Code == "" {
			f.Code = codeOfStatus(ae.Status)
		}
		if f.Detail == "" {
			f.Detail = strings.ToLower(http.StatusText(ae.Status))
		}
		if ae.HasRetryAfter() {
			f.RetryAfterS = ae.RetryAfter.Seconds()
		}
		return f
	case errors.As(err, &ce):
		return failure{Code: api.CodeUnavailable, Detail: ce.Error()}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return failure{Code: api.CodeTimeout, Detail: "the call ended before it was answered"}
	}
	return failure{Code: api.CodeInternal, Detail: err.Error()}
}

// codeOfStatus is a code for an answer that carried none.
func codeOfStatus(status int) string {
	switch {
	case status == 400 || status == 411 || status == 422:
		return api.CodeInvalidRequest
	case status == 401:
		return api.CodeUnauthorized
	case status == 402:
		return api.CodePaymentRequired
	case status == 403:
		return api.CodeForbidden
	case status == 404:
		return api.CodeNotFound
	case status == 408 || status == 504:
		return api.CodeTimeout
	case status == 409:
		return api.CodeConflict
	case status == 413:
		return api.CodeTooLarge
	case status == 429:
		return api.CodeRateLimited
	case status >= 500:
		return api.CodeUnavailable
	}
	return api.CodeInternal
}

func needMachine(m string) error {
	if strings.TrimSpace(m) == "" {
		return inputf("give the machine: its id (m_...) or its name")
	}
	return nil
}

func checkWait(w *float64) error {
	if w != nil && (*w < 0 || *w > 600) {
		return inputf("wait_s is between 0 and 600")
	}
	return nil
}

func checkPath(p string) error {
	if !strings.HasPrefix(p, "home/") && !strings.HasPrefix(p, "work/") {
		return inputf("path begins home/ (the machine's home) or work/ (where commands start), not %q", p)
	}
	return nil
}

type machineArgs struct {
	Machine string `json:"machine"`
}

func (a *machineArgs) check() error { return needMachine(a.Machine) }

type listArgs struct {
	Project string            `json:"project"`
	State   string            `json:"state"`
	Labels  map[string]string `json:"labels"`
	Limit   *int              `json:"limit"`
	After   string            `json:"after"`
}

func (a *listArgs) check() error {
	if a.Limit != nil && (*a.Limit < 1 || *a.Limit > 200) {
		return inputf("limit is between 1 and 200")
	}
	if a.State != "" && !slices.Contains(api.States, a.State) {
		return inputf("no state %q: the states are %s", a.State, strings.Join(api.ListedStates, ", "))
	}
	return nil
}

func listMachines(ctx context.Context, c *api.Client, a *listArgs) (any, error) {
	p := api.ListMachinesParams{State: a.State, Labels: a.Labels, Limit: 50, After: a.After}
	if a.Limit != nil {
		p.Limit = *a.Limit
	}
	if a.Project != "" {
		id, err := c.FindProject(ctx, a.Project)
		if err != nil {
			return nil, err
		}
		p.Project = id
	}
	page, err := c.ListMachines(ctx, p)
	if err != nil {
		return nil, err
	}
	if page.Data == nil {
		page.Data = []api.Machine{}
	}
	return page, nil
}

type createArgs struct {
	Name          string            `json:"name"`
	Project       string            `json:"project"`
	Region        string            `json:"region"`
	GPU           string            `json:"gpu"`
	CPUs          int               `json:"cpus"`
	Labels        map[string]string `json:"labels"`
	Secrets       []string          `json:"secrets"`
	SleepWhenIdle *bool             `json:"sleep_when_idle"`
	IdleAfterS    *int              `json:"idle_after_s"`
	Network       *api.Network      `json:"network"`
	WaitS         *float64          `json:"wait_s"`
}

func (a *createArgs) check() error {
	if err := checkSecretNames(a.Secrets); err != nil {
		return err
	}
	if err := checkNetwork(a.Network); err != nil {
		return err
	}
	return checkWait(a.WaitS)
}

func createMachine(ctx context.Context, c *api.Client, a *createArgs) (any, error) {
	mc := api.MachineCreate{Name: a.Name, Region: a.Region, GPU: a.GPU, CPUs: a.CPUs, Labels: a.Labels, Secrets: a.Secrets,
		Network: a.Network, SleepWhenIdle: a.SleepWhenIdle, IdleAfterS: a.IdleAfterS, WaitS: a.WaitS}
	if a.Project != "" {
		id, err := c.FindProject(ctx, a.Project)
		if err != nil {
			return nil, err
		}
		mc.Project = id
	}
	return c.CreateMachine(ctx, mc)
}

func getMachine(ctx context.Context, c *api.Client, a *machineArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.GetMachine(ctx, id)
}

type commandArgs struct {
	Machine        string            `json:"machine"`
	Command        []string          `json:"command"`
	Shell          *string           `json:"shell"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env"`
	Root           bool              `json:"root"`
	MaxOutputBytes *int              `json:"max_output_bytes"`
}

func (a *commandArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	hasShell := a.Shell != nil && strings.TrimSpace(*a.Shell) != ""
	switch {
	case len(a.Command) > 0 && hasShell:
		return inputf("give command or shell, not both")
	case len(a.Command) == 0 && !hasShell:
		return inputf(`say what to run: command, such as ["ls", "-la"], or shell, such as "ls -la | wc -l"`)
	}
	if a.MaxOutputBytes != nil && (*a.MaxOutputBytes < 1024 || *a.MaxOutputBytes > 8<<20) {
		return inputf("max_output_bytes is between 1024 and 8388608")
	}
	return nil
}

// commandResult is how a command ended and what it wrote.
type commandResult struct {
	ExitCode   int     `json:"exit_code"`
	Stdout     string  `json:"stdout"`
	Stderr     string  `json:"stderr"`
	DurationMS float64 `json:"duration_ms"`
	Truncated  bool    `json:"truncated"`
}

func runCommand(ctx context.Context, c *api.Client, a *commandArgs) (any, error) {
	req := api.ExecRequest{Command: a.Command, Cwd: a.Cwd, Env: a.Env}
	if len(a.Command) == 0 {
		req.Shell = *a.Shell
	}
	if a.Root {
		req.User = "root"
	}
	limit := 65536
	if a.MaxOutputBytes != nil {
		limit = *a.MaxOutputBytes
	}
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	res, err := c.Exec(ctx, id, req)
	if err != nil {
		return nil, err
	}
	stdout, cutOut := clip(res.Stdout, limit)
	stderr, cutErr := clip(res.Stderr, limit)
	return commandResult{ExitCode: res.ExitCode, Stdout: stdout, Stderr: stderr, DurationMS: res.DurationMS,
		Truncated: res.Truncated || cutOut || cutErr}, nil
}

// clip keeps at most limit bytes of s: its start and its end, cut on whole characters, with what was left out said.
func clip(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	head := limit / 2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tail := len(s) - (limit - head)
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:head] + fmt.Sprintf("\n[... %d bytes left out ...]\n", tail-head) + s[tail:], true
}

type readArgs struct {
	Machine  string `json:"machine"`
	Path     string `json:"path"`
	MaxBytes *int   `json:"max_bytes"`
}

func (a *readArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if a.MaxBytes != nil && (*a.MaxBytes < 1 || *a.MaxBytes > 8<<20) {
		return inputf("max_bytes is between 1 and 8388608")
	}
	return checkPath(a.Path)
}

// fileResult is a file read: its bytes as text, or as base64 when they are not text.
type fileResult struct {
	Path      string `json:"path"`
	Size      *int64 `json:"size,omitempty"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

func readFile(ctx context.Context, c *api.Client, a *readArgs) (any, error) {
	limit := 1 << 20
	if a.MaxBytes != nil {
		limit = *a.MaxBytes
	}
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	f, err := c.ReadFile(ctx, id, a.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("the file could not be read whole: %w", err)
	}
	out := fileResult{Path: a.Path, Encoding: "text"}
	if out.Truncated = len(b) > limit; out.Truncated {
		b = b[:limit]
	}
	switch {
	case f.Size >= 0:
		out.Size = api.Ptr(f.Size)
	case !out.Truncated:
		out.Size = api.Ptr(int64(len(b)))
	}
	text := b
	if out.Truncated {
		text = wholeRunes(b)
	}
	if utf8.Valid(text) {
		out.Content = string(text)
	} else {
		out.Encoding, out.Content = "base64", base64.StdEncoding.EncodeToString(b)
	}
	return out, nil
}

// wholeRunes leaves off a character cut in two at the end.
func wholeRunes(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

type writeArgs struct {
	Machine string  `json:"machine"`
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func (a *writeArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if a.Content == nil {
		return inputf("give the content to write; it may be empty")
	}
	return checkPath(a.Path)
}

func writeFile(ctx context.Context, c *api.Client, a *writeArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	if err := c.WriteFile(ctx, id, a.Path, strings.NewReader(*a.Content), int64(len(*a.Content))); err != nil {
		return nil, err
	}
	return map[string]any{"machine": id, "path": a.Path, "bytes": len(*a.Content), "written": true}, nil
}

func pauseMachine(ctx context.Context, c *api.Client, a *machineArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.PauseMachine(ctx, id)
}

type startArgs struct {
	Machine string   `json:"machine"`
	WaitS   *float64 `json:"wait_s"`
}

func (a *startArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	return checkWait(a.WaitS)
}

func startMachine(ctx context.Context, c *api.Client, a *startArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.StartMachine(ctx, id, a.WaitS)
}

func stopMachine(ctx context.Context, c *api.Client, a *machineArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.StopMachine(ctx, id)
}

type forkArgs struct {
	Machine string            `json:"machine"`
	Name    *string           `json:"name"`
	Labels  map[string]string `json:"labels"`
	Backup  string            `json:"backup"`
	CPUs    int               `json:"cpus"`
	WaitS   *float64          `json:"wait_s"`
}

func (a *forkArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if a.CPUs != 0 && a.Backup == "" {
		return inputf("cpus is only for a fork from a backup: a fork of the machine as it is now keeps its size")
	}
	return checkWait(a.WaitS)
}

// forkWaits is a member's fork that waits for an OK, in an organisation: the request as the API gave it, and what it
// waits for in words, so that its id is never taken for a machine's.
type forkWaits struct {
	*api.Request
	Note string `json:"note"`
}

func forkMachine(ctx context.Context, c *api.Client, a *forkArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	f, err := c.ForkMachine(ctx, id, api.MachineFork{Name: a.Name, Labels: a.Labels, Backup: a.Backup, CPUs: a.CPUs, WaitS: a.WaitS})
	if err != nil {
		return nil, err
	}
	if f.Request == nil {
		return f.Machine, nil
	}
	note := "No machine is made yet: the fork waits for the OK of a full manager of the machine's team, an owner or an admin, " +
		"and is made once they approve it; list_machines shows it then."
	switch {
	case f.Request.State != "waiting":
		note = "No machine is made yet: the fork is approved and being made; list_machines shows it once it is."
	case f.Request.WaitingOn != nil && f.Request.WaitingOn.Who == "asker":
		note = "No machine is made yet: the fork waits for the OK of the person who made the key, signed in to the dashboard at " +
			"https://app.wheelmachine.ai/requests, then for the OK of a full manager of the machine's team, an owner or an admin, " +
			"and is made once they approve it; list_machines shows it then."
	}
	return forkWaits{Request: f.Request, Note: note}, nil
}

type backupArgs struct {
	Machine string `json:"machine"`
	Note    string `json:"note"`
}

func (a *backupArgs) check() error { return needMachine(a.Machine) }

func backupMachine(ctx context.Context, c *api.Client, a *backupArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.BackupMachine(ctx, id, api.BackupCreate{Note: a.Note})
}

type backupsArgs struct {
	Machine string `json:"machine"` // every machine's when left out
	Limit   *int   `json:"limit"`
}

func (a *backupsArgs) check() error {
	if a.Limit != nil && (*a.Limit < 1 || *a.Limit > 200) {
		return inputf("limit is between 1 and 200")
	}
	return nil
}

func listBackups(ctx context.Context, c *api.Client, a *backupsArgs) (any, error) {
	limit := 0 // the API's own, 100
	if a.Limit != nil {
		limit = *a.Limit
	}
	var list *api.BackupList
	var err error
	if a.Machine == "" {
		list, err = c.ListBackups(ctx, limit)
	} else {
		var id string
		if id, err = c.FindMachine(ctx, a.Machine); err == nil {
			list, err = c.ListMachineBackups(ctx, id, limit)
		}
	}
	if err != nil {
		return nil, err
	}
	if list.Data == nil {
		list.Data = []api.Backup{}
	}
	return list, nil
}

func deleteMachine(ctx context.Context, c *api.Client, a *machineArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	if err := c.DeleteMachine(ctx, id); err != nil {
		return nil, err
	}
	return map[string]any{"machine": id, "deleted": true}, nil
}

func checkSecretNames(names []string) error {
	for _, n := range names {
		if !api.ValidSecretName(n) {
			return inputf("%q is not a secret's name: capitals, digits and _, such as DATABASE_URL", n)
		}
	}
	return nil
}

func listSecrets(ctx context.Context, c *api.Client, _ *noArgs) (any, error) {
	list, err := c.Secrets(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []api.Secret{}
	}
	return struct {
		Object string       `json:"object"`
		Data   []api.Secret `json:"data"`
	}{"list", list}, nil
}

type giveArgs struct {
	Machine string    `json:"machine"`
	Secrets *[]string `json:"secrets"`
}

func (a *giveArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if a.Secrets == nil {
		return inputf("give secrets: every secret the machine is to get, by name; [] for none")
	}
	return checkSecretNames(*a.Secrets)
}

func giveSecrets(ctx context.Context, c *api.Client, a *giveArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.UpdateMachine(ctx, id, api.MachineUpdate{Secrets: a.Secrets})
}

type networkArgs struct {
	Machine string       `json:"machine"`
	Network *api.Network `json:"network"`
}

func (a *networkArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if a.Network == nil {
		return inputf("set_network needs network: {\"internet\": \"open\"|\"closed\"|\"allowed\", \"allow\": [...]}")
	}
	return checkNetwork(a.Network)
}

type appArgs struct {
	Machine string `json:"machine"`
	App     string `json:"app"`
	AccessS *int   `json:"access_s"`
}

func (a *appArgs) check() error {
	if err := needMachine(a.Machine); err != nil {
		return err
	}
	if !api.ValidAppName(a.App) {
		return inputf("app is an app's name, as list_apps gives it, such as desktop")
	}
	if a.AccessS != nil && (*a.AccessS < 60 || *a.AccessS > 3600) {
		return inputf("access_s is between 60 and 3600")
	}
	return nil
}

func (a *appArgs) link() api.AppLinkCreate {
	if a.AccessS == nil {
		return api.AppLinkCreate{}
	}
	return api.AppLinkCreate{AccessS: *a.AccessS}
}

func listApps(ctx context.Context, c *api.Client, a *machineArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	apps, err := c.ListApps(ctx, id)
	if err != nil {
		return nil, err
	}
	if apps == nil {
		apps = []api.App{}
	}
	return map[string]any{"object": "list", "data": apps}, nil
}

func turnAppOn(ctx context.Context, c *api.Client, a *appArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.TurnAppOn(ctx, id, a.App, a.link())
}

func turnAppOff(ctx context.Context, c *api.Client, a *appArgs) (any, error) {
	if a.AccessS != nil {
		return nil, inputf("turn_app_off takes no access_s")
	}
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.TurnAppOff(ctx, id, a.App)
}

func showApp(ctx context.Context, c *api.Client, a *appArgs) (any, error) {
	if a.AccessS != nil {
		return nil, inputf("show_app takes no access_s")
	}
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.ShowApp(ctx, id, a.App)
}

func openApp(ctx context.Context, c *api.Client, a *appArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.LinkApp(ctx, id, a.App, a.link())
}

func setNetwork(ctx context.Context, c *api.Client, a *networkArgs) (any, error) {
	id, err := c.FindMachine(ctx, a.Machine)
	if err != nil {
		return nil, err
	}
	return c.UpdateMachine(ctx, id, api.MachineUpdate{Network: a.Network})
}

// checkNetwork is the thin client's own guard on a policy before the call (the API checks it fully).
func checkNetwork(n *api.Network) error {
	if n == nil {
		return nil
	}
	switch n.Internet {
	case "open", "closed", "allowed":
	default:
		return inputf("network.internet is open, closed or allowed")
	}
	if n.Internet != "allowed" && len(n.Allow) > 0 {
		return inputf("network.allow is only for internet allowed")
	}
	return nil
}

type noArgs struct{}

func listGPUs(ctx context.Context, c *api.Client, _ *noArgs) (any, error) {
	list, err := c.GPUs(ctx)
	if err == nil && list.Data == nil {
		list.Data = []api.GpuOffer{}
	}
	return list, err
}

func listRegions(ctx context.Context, c *api.Client, _ *noArgs) (any, error) {
	list, err := c.Regions(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []api.Region{}
	}
	return struct {
		Object string       `json:"object"`
		Data   []api.Region `json:"data"`
	}{"list", list}, nil
}

func whoami(ctx context.Context, c *api.Client, _ *noArgs) (any, error) { return c.Me(ctx) }
