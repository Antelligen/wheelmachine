package api

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// How long one try of a call is given, beyond what the API itself takes for it.
const (
	callTimeout = 3 * time.Minute  // most calls: the API gives itself two minutes at most
	longTimeout = 35 * time.Minute // archive and backup: the API gives them 30 minutes
	execTimeout = 12 * time.Minute // a command: the API gives it 10 minutes
)

// readyTimeout is for a call that waits for a machine to run: the wait asked for, the API's own default 60 seconds
// when none is, and room for the rest.
func readyTimeout(waitS *float64) time.Duration {
	w := 60.0
	if waitS != nil {
		w = *waitS
	}
	return time.Duration(w*float64(time.Second)) + 2*time.Minute
}

// ListMachinesParams narrows a list of machines. Every field may be left out.
type ListMachinesParams struct {
	Project string            // only this project's machines
	State   string            // only machines in this state
	Labels  map[string]string // only machines carrying every one of these labels
	Limit   int               // machines a page, 1 to 200; 0: the API's 50
	After   string            // where the page begins: the Next of the page before
}

func (p ListMachinesParams) query() url.Values {
	q := url.Values{}
	if p.Project != "" {
		q.Set("project", p.Project)
	}
	if p.State != "" {
		q.Set("state", p.State)
	}
	keys := make([]string, 0, len(p.Labels))
	for k := range p.Labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		q.Add("label", k+"="+p.Labels[k])
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		q.Set("after", p.After)
	}
	return q
}

// ListMachines is one page of machines, newest first.
func (c *Client) ListMachines(ctx context.Context, p ListMachinesParams) (*Page[Machine], error) {
	var page Page[Machine]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines", query: p.query(), timeout: callTimeout}, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// AllMachines walks every machine the parameters ask for, newest first, asking for each page only when the one
// before it is used up. Stop early by breaking out of the loop.
func (c *Client) AllMachines(ctx context.Context, p ListMachinesParams) iter.Seq2[Machine, error] {
	return all(p.After, func(after string) (*Page[Machine], error) {
		q := p
		q.After = after
		return c.ListMachines(ctx, q)
	})
}

// CreateMachine makes a machine. It answers when the machine runs; when that
// takes longer than WaitS, the machine comes back in StateStarting and WaitForMachine follows it.
func (c *Client) CreateMachine(ctx context.Context, m MachineCreate) (*Machine, error) {
	var out Machine
	cl := call{method: http.MethodPost, path: "/v1/machines", in: m, create: true, idemKey: m.IdempotencyKey, timeout: readyTimeout(m.WaitS)}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateMachines makes machines of one of a team's types for its people, several at once, and answers each: made,
// on its way, or refused - one refused leaves the others made.
func (c *Client) CreateMachines(ctx context.Context, b MachineBatchCreate) (*MachineBatch, error) {
	var out MachineBatch
	// (a batch makes its machines a few at a time: given as long as a hundred of them take)
	cl := call{method: http.MethodPost, path: "/v1/machines/batch", in: b, create: true, idemKey: b.IdempotencyKey, timeout: readyTimeout(b.WaitS) + 10*time.Minute}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMachineTypes is a team's machine types, by name.
func (c *Client) ListMachineTypes(ctx context.Context, project string) ([]MachineType, error) {
	id, err := segment("project", project)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []MachineType `json:"data"`
	}
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/projects/" + id + "/types", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetMachine is one machine as it is now.
func (c *Client) GetMachine(ctx context.Context, machine string) (*Machine, error) {
	return c.machineCall(ctx, http.MethodGet, machine, "", nil, callTimeout, false)
}

// UpdateMachine renames a machine or changes its labels.
func (c *Client) UpdateMachine(ctx context.Context, machine string, u MachineUpdate) (*Machine, error) {
	return c.machineCall(ctx, http.MethodPatch, machine, "", u, callTimeout, true)
}

// DeleteMachine deletes a machine and its files.
func (c *Client) DeleteMachine(ctx context.Context, machine string) error {
	id, err := segment("machine", machine)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/machines/" + id, timeout: callTimeout}, nil)
}

// StartMachine runs a paused, stopped or archived machine again. waitS is as in MachineCreate.
func (c *Client) StartMachine(ctx context.Context, machine string, waitS *float64) (*Machine, error) {
	var body any
	if waitS != nil {
		body = map[string]float64{"wait_s": *waitS}
	}
	return c.machineCall(ctx, http.MethodPost, machine, "/start", body, readyTimeout(waitS), true)
}

// PauseMachine pauses a machine with its memory kept: nothing runs and no running time is charged.
func (c *Client) PauseMachine(ctx context.Context, machine string) (*Machine, error) {
	return c.machineCall(ctx, http.MethodPost, machine, "/pause", nil, callTimeout, true)
}

// StopMachine stops a machine; its files stay, its memory does not.
func (c *Client) StopMachine(ctx context.Context, machine string) (*Machine, error) {
	return c.machineCall(ctx, http.MethodPost, machine, "/stop", nil, callTimeout, true)
}

// ArchiveMachine puts a machine's files away in storage, the cheapest way to keep it.
func (c *Client) ArchiveMachine(ctx context.Context, machine string) (*Machine, error) {
	return c.machineCall(ctx, http.MethodPost, machine, "/archive", nil, longTimeout, true)
}

// ForkResult is how a fork is answered: the fork itself (Machine), running or still starting - or, in an organisation,
// a member's fork that waits for the OK of a full manager of the machine's team, an owner or an admin (Request). Then
// nothing is made yet: the fork is made once the request is approved, and its id is the request's Result. One of the
// two is set.
type ForkResult struct {
	Machine *Machine
	Request *Request
}

// UnmarshalJSON reads either answer of a fork, telling them apart by their object: a request's is "request".
func (f *ForkResult) UnmarshalJSON(b []byte) error {
	var head struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	if head.Object == "request" {
		f.Machine, f.Request = nil, &Request{}
		return json.Unmarshal(b, f.Request)
	}
	f.Machine, f.Request = &Machine{}, nil
	return json.Unmarshal(b, f.Machine)
}

// ForkMachine forks a machine: a new machine of its own, made from it as it is now - or from one of its backups
// (MachineFork.Backup), at its size or another. In an organisation, a member's fork waits for an OK instead, and is
// answered with its request (ForkResult).
func (c *Client) ForkMachine(ctx context.Context, machine string, m MachineFork) (*ForkResult, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out ForkResult
	cl := call{method: http.MethodPost, path: "/v1/machines/" + id + "/fork", in: m, create: true, idemKey: m.IdempotencyKey,
		timeout: readyTimeout(m.WaitS)}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BackupCreate is a note for a backup, both optional.
type BackupCreate struct {
	Note string `json:"note,omitempty"`

	IdempotencyKey string `json:"-"`
}

// BackupMachine backs a machine up: its files as they are now, not what runs in it. A fork of the machine is made from
// the backup with MachineFork.Backup.
func (c *Client) BackupMachine(ctx context.Context, machine string, b BackupCreate) (*Backup, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Backup
	cl := call{method: http.MethodPost, path: "/v1/machines/" + id + "/backups", in: b, create: true, idemKey: b.IdempotencyKey,
		timeout: longTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMachineBackups is a machine's backups, the newest first: at most limit of them, 1 to 200 (0: the API's 100).
func (c *Client) ListMachineBackups(ctx context.Context, machine string, limit int) (*BackupList, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	return c.backups(ctx, "/v1/machines/"+id+"/backups", limit)
}

// ListBackups is the backups of every machine the key reaches, the newest first, as ListMachineBackups is a machine's.
func (c *Client) ListBackups(ctx context.Context, limit int) (*BackupList, error) {
	return c.backups(ctx, "/v1/backups", limit)
}

func (c *Client) backups(ctx context.Context, path string, limit int) (*BackupList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out BackupList
	if err := c.decode(ctx, call{method: http.MethodGet, path: path, query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MaxWait is the longest one WaitForMachine call waits; wait again for longer.
const MaxWait = 600 * time.Second

// WaitForMachine answers when the machine reaches the state, or refuses with CodeTimeout (408) when it has not
// within timeout (at most MaxWait) - at once when the machine is deleted.
func (c *Client) WaitForMachine(ctx context.Context, machine, state string, timeout time.Duration) (*Machine, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	timeout = min(max(timeout, 0), MaxWait)
	q := url.Values{"state": {state}, "timeout_s": {strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64)}}
	var out Machine
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/wait", query: q, timeout: timeout + time.Minute}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Exec runs a command in a machine and gives what it wrote and how it ended. A paused machine is woken for it.
// A command is never sent twice: it is tried again only when the API refused it before it could run.
func (c *Client) Exec(ctx context.Context, machine string, req ExecRequest) (*ExecResult, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out ExecResult
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/machines/" + id + "/exec", in: req, timeout: execTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MetricsParams says from when, and in what steps. Since is a time (RFC 3339) or how long ago ("15m", "6h", "1d"):
// the last hour when empty, a day back at most. Step is how long each point covers, whole minutes from one to sixty
// ("5m", "300"): a minute when empty.
type MetricsParams struct {
	Since string
	Step  string
}

// Metrics is what a machine used - its CPUs, memory, disk and network - a point a step.
func (c *Client) Metrics(ctx context.Context, machine string, p MetricsParams) (*MachineMetrics, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if p.Since != "" {
		q.Set("since", p.Since)
	}
	if p.Step != "" {
		q.Set("step", p.Step)
	}
	var out MachineMetrics
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/metrics", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SignSSHCertificate signs a short-lived SSH certificate for a machine (the API's POST /v1/ssh/certificates): give a
// public key and the machine, get a certificate good for at most an hour, for that machine alone.
func (c *Client) SignSSHCertificate(ctx context.Context, req SSHCertificateRequest) (*SSHCertificate, error) {
	var out SSHCertificate
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/ssh/certificates", in: req, create: true, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// machineCall is a call on one machine whose answer is the machine. `replay`: a move to a state, safe to send again.
func (c *Client) machineCall(ctx context.Context, method, machine, action string, body any, timeout time.Duration, replay bool) (*Machine, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Machine
	if err := c.decode(ctx, call{method: method, path: "/v1/machines/" + id + action, in: body, timeout: timeout, replay: replay}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// File is a file being read from a machine, as it comes. Close it when done.
type File struct {
	io.ReadCloser
	Size int64 // its length in bytes, or -1 when the API did not say
}

// ReadFile reads a file in a machine, as a stream: path is home/... (the machine's home) or work/... (where
// commands start).
func (c *Client) ReadFile(ctx context.Context, machine, path string) (*File, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/files", query: url.Values{"path": {path}},
		accept: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return &File{ReadCloser: res.Body, Size: res.ContentLength}, nil
}

// WriteFile writes size bytes from r to a file in a machine, making the folders on the way, as a stream. The
// file is sent again after a failed try only when r can be wound back (an *os.File, a *bytes.Reader).
func (c *Client) WriteFile(ctx context.Context, machine, path string, r io.Reader, size int64) error {
	id, err := segment("machine", machine)
	if err != nil {
		return err
	}
	if size < 0 {
		return &Error{Code: CodeInvalidRequest, Detail: "the file's length is needed to write it"}
	}
	if r == nil {
		r = http.NoBody
	}
	return c.decode(ctx, call{method: http.MethodPut, path: "/v1/machines/" + id + "/files", query: url.Values{"path": {path}},
		stream: r, size: size}, nil)
}

// ListPorts is a machine's ports open at web addresses, lowest first.
func (c *Client) ListPorts(ctx context.Context, machine string) ([]Port, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Page[Port]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/ports", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// OpenPort opens a port of a machine at a web address of its own, private unless public. A port open already is made
// public or private as asked, so the call is safe to send again.
func (c *Client) OpenPort(ctx context.Context, machine string, port int, public bool) (*Port, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	if err := checkPort(port); err != nil {
		return nil, err
	}
	var out Port
	cl := call{method: http.MethodPost, path: "/v1/machines/" + id + "/ports", in: PortOpen{Port: port, Public: public},
		replay: true, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClosePort closes a port's web address: it answers 404 from then on, and links made for it no longer open it. A
// port that is not open is closed all the same.
func (c *Client) ClosePort(ctx context.Context, machine string, port int) error {
	path, err := portPath(machine, port)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: path, timeout: callTimeout}, nil)
}

// LinkPort makes a link that lets one browser into a port's private address for a while. Each call makes a new link,
// shown in its answer only, so a call that may have reached the API is never sent again.
func (c *Client) LinkPort(ctx context.Context, machine string, port int, l PortLinkCreate) (*PortLink, error) {
	path, err := portPath(machine, port)
	if err != nil {
		return nil, err
	}
	var out PortLink
	if err := c.decode(ctx, call{method: http.MethodPost, path: path + "/link", in: l, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// How long turning an app on may take: the app starts, and the API waits for it up to a minute.
const appOnTimeout = 2 * time.Minute

// How long showing an app may take: the API waits two minutes for the machine, which may start its desktop first.
const appShowTimeout = 3 * time.Minute

// ListApps is a machine's apps and whether each is on. The machine is asked, so a paused one is woken for it; one
// with no apps is refused with CodeNoApps.
func (c *Client) ListApps(ctx context.Context, machine string) ([]App, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Page[App]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/machines/" + id + "/apps", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// TurnAppOn turns an app on, with the machine's secrets in its environment as a command has them. A web app's port
// is opened private when it is not open, and the answer's Link lands on its page; an app with a token of its own
// answers it once, in Token. The answer is shown once, so a call that may have reached the API is never sent again.
func (c *Client) TurnAppOn(ctx context.Context, machine, app string, l AppLinkCreate) (*App, error) {
	path, err := appPath(machine, app)
	if err != nil {
		return nil, err
	}
	var out App
	if err := c.decode(ctx, call{method: http.MethodPost, path: path + "/on", in: l, timeout: appOnTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TurnAppOff turns an app off; a web app's port is closed with it when turning it on opened it and it is still
// private. Turning off an app that is off is the same, so the call is safe to send again.
func (c *Client) TurnAppOff(ctx context.Context, machine, app string) (*App, error) {
	path, err := appPath(machine, app)
	if err != nil {
		return nil, err
	}
	var out App
	if err := c.decode(ctx, call{method: http.MethodPost, path: path + "/off", replay: true, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ShowApp shows an app on the machine's own screen: its window opened on the desktop, signed in where it signs in. It
// may take up to two minutes while the app comes up; showing one that is shown already does no harm, so a call that lost
// its connection is sent again.
func (c *Client) ShowApp(ctx context.Context, machine, app string) (*App, error) {
	path, err := appPath(machine, app)
	if err != nil {
		return nil, err
	}
	var out App
	if err := c.decode(ctx, call{method: http.MethodPost, path: path + "/show", replay: true, timeout: appShowTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkApp makes a fresh one-time link into a web app that is on, in the answer's Link. Each call makes a new link,
// shown in its answer only, so a call that may have reached the API is never sent again.
func (c *Client) LinkApp(ctx context.Context, machine, app string, l AppLinkCreate) (*App, error) {
	path, err := appPath(machine, app)
	if err != nil {
		return nil, err
	}
	var out App
	if err := c.decode(ctx, call{method: http.MethodPost, path: path + "/link", in: l, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// appPath is the route of one app of a machine. An app's name is lower-case letters, digits and -, at most 32.
func appPath(machine, app string) (string, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return "", err
	}
	if !ValidAppName(app) {
		return "", &Error{Code: CodeInvalidRequest, Detail: strconv.Quote(app) + " is not an app's name: lower-case letters, digits and -, at most 32"}
	}
	return "/v1/machines/" + id + "/apps/" + app, nil
}

// ValidAppName reports whether s is written as an app's name is: lower-case letters, digits and -, 1 to 32 of them.
func ValidAppName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// portPath is the route of one port of a machine.
func portPath(machine string, port int) (string, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return "", err
	}
	if err := checkPort(port); err != nil {
		return "", err
	}
	return "/v1/machines/" + id + "/ports/" + strconv.Itoa(port), nil
}

// checkPort refuses what is not a port before any call: a port is 1 to 65535.
func checkPort(port int) error {
	if port < 1 || port > 65535 {
		return &Error{Code: CodeInvalidRequest, Detail: strconv.Itoa(port) + " is not a port: a port is 1 to 65535"}
	}
	return nil
}
