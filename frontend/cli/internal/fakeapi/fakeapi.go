// Package fakeapi is an in-memory stand-in for the Wheelmachine API, as contracts/openapi.yaml describes it, for the
// tests of the commands. It answers as the API does: application/problem+json errors with a code, X-Request-Id
// on every answer, pages of {object, data, next} walked with ?after=, 201 or 202 for what is made, 204 for what is
// deleted, and the first answer again for an Idempotency-Key used before.
package fakeapi

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"wheelmachine/frontend/cli/api"
)

// Refusal is an answer of application/problem+json.
type Refusal struct {
	Status     int
	Code       string
	Detail     string
	RetryAfter int // seconds, sent as Retry-After when above 0
	// what the code a risky step needs must be for (confirm_required, confirm_wrong)
	Confirm *api.ConfirmAsk
}

// Request is a request as the fake received it.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// Server is the fake API. Its fields may be set before the first request.
type Server struct {
	URL string // where it listens, for WHEELMACHINE_BASE_URL
	Key string // the one API key that works

	// Exec answers a command; when nil, a command writes its words (or its shell line) and a newline to stdout.
	Exec func(machine string, req api.ExecRequest) api.ExecResult
	// Refuse, when it gives an error for a request, is answered in its place.
	Refuse func(r *http.Request) *Refusal
	// Slow makes new and started machines answer 202 (starting); they run once waited for - or, with Fail, never
	// come up, and fail with its words instead.
	Slow    bool
	Fail    string
	Account string
	Usage   api.Usage
	// Credit is the account's credit as GET /v1/credit answers it, its entries the newest first and given a page at
	// a time (its Data, Next and Object are the stand-in's own).
	Credit api.Credit
	// Limits is what GET /v1/usage/limits answers; PUT /v1/usage/limits/account sets its account's own limit
	Limits api.UsageLimits
	GPUs   api.GpuList
	// Code is what GET /v1/code answers; Repos what a connection reaches; PUT /v1/projects/{id}/code gives a team one
	Code  api.Code
	Repos []api.CodeRepo
	// Folders is what GET /v1/folders answers; FolderFiles each folder's files, by "<folder>/<path>", which the routes of
	// a folder's files read and write
	Folders     []api.SharedFolder
	FolderFiles map[string][]byte
	// FolderPage is how many entries a page of a folder's listing holds; all of them for 0
	FolderPage int
	// ConfirmCode, when set, is the code a key with full access needs (X-Confirm-Code), as e-mailed by
	// POST /v1/confirm-codes; unset, none is asked, as on an API that sends no e-mail. ConfirmsSent counts the codes sent.
	ConfirmCode  string
	ConfirmsSent int
	// Metrics is each machine's points, by its id, as GET /v1/machines/{id}/metrics answers them (none when unset).
	Metrics map[string][]api.MetricsPoint
	Regions []api.Region
	Now     func() time.Time
	// A program signing in through the browser: LoginsShut answers that it is not open; Decide says, at each ask for
	// the key (from 1), what the person did - "pending", "approved", "denied" or "expired" (nil: approved at once).
	// An approved sign-in is given the one key that works, under the name it asked for.
	LoginsShut bool
	Decide     func(ask int) string

	// PortsNotOpen refuses opening a port, and making a link to one, as the API does where it is not open:
	// 503 not_open.
	PortsNotOpen bool
	// NoApps answers every machine's apps as a machine with no apps does: 409 no_apps.
	NoApps bool

	// Models is what GET /v1/models answers - the models of the account's AI keys, and their providers - narrowed to an
	// agent's providers when one is asked for; Agents is the agents of the machines, each with the providers it can use. A
	// model chosen here stands on them as the API's choices do (models.go).
	Models api.ModelList
	Agents []api.Agent
	// Allowed is the lists of the models the organisation's people may use, as its owners and admins keep them through
	// /v1/models/allowed (an organisation's key alone, and an owner's or an admin's); HeldTo is the one that holds the
	// key's own choices, which the models listed, the agents and a choice stand by as the API's do.
	Allowed []api.AllowedModels
	HeldTo  *api.AllowedModels

	// Org, when set, makes the one key that works a key made in an organisation, by its Person: GET /v1/me answers as
	// the API does there, keys and machines made carry who made them, GET /v1/orgs is refused as an organisation's key
	// is, and the audit is read by an owner or an admin alone.
	Org *Org
	// Orgs is what GET /v1/orgs answers a key of a person's own account.
	Orgs []api.Org
	// Audit is the account's events, the newest first, as GET /v1/audit pages them and GET /v1/audit/export gives
	// them whole.
	Audit []api.AuditEvent
	// A fork for someone, a hand-off and an ask made with a key wait for the OK of the person who made the key first, as the API's do: the
	// dashboard's, signed in, which Approve stands in for. GivesWait makes a hand-off wait for a manager after it, as a
	// team whose rules ask does; else it is carried out then, as an owner's give is. In an organisation a member's
	// fork of their own machine - but a full manager's of its team - is such a request too, answered 202 with nothing
	// made, and after the OK of the person who made the key it waits for a full manager whatever GivesWait says. Rules is each team's rules,
	// by its project (the defaults when not set). The requests asked are kept, the newest last.
	GivesWait bool
	Rules     map[string]api.TeamRules
	Asks      []api.Request
	// Types is each team's machine types, by its project, as GET /v1/projects/{id}/types lists them: a machine made of
	// one is made as it says. People is the organisation's people a batch of machines may be for (POST
	// /v1/machines/batch), named by @handle, e-mail address or id, each with the teams they are in.
	Types  map[string][]api.MachineType
	People []Person

	mu       sync.Mutex
	srv      *httptest.Server
	ids      int
	calls    int
	machines []*api.Machine // in the order made; lists give the newest first
	backups  []keptBackup   // in the order taken; lists give the newest first
	files    map[string][]byte
	ports    map[string][]api.Port // by machine, lowest first
	apps     map[string][]api.App  // by machine, as the machine says them: made the first time they are asked for
	appPorts map[string]string     // the ports an app opened as it was turned on, by "machine port": the app
	projects []api.Project
	keys     []api.Key
	secrets  map[string]*keptSecret // by name
	requests []Request
	kept     map[string]keptAnswer // by Idempotency-Key
	logins   map[string]*login     // by device code

	terminalQuery   map[string][]string
	terminalResizes []string
	sshSignedFor    string
	chosen          map[string]string // the model chosen, by agent ("*": every agent)
}

// keptSecret is a secret as the fake holds it: its value is never in an answer, as the API's is not.
type keptSecret struct {
	value            string
	created, updated time.Time
	outside          *api.Outside // kept outside the machines; nil for an ordinary secret
	every            bool         // on every machine, not only on those that name it
}

// keptBackup is a backup as the fake holds it: what the API shows of it, and the machine's files as they were then, by
// path, which a fork from it starts from.
type keptBackup struct {
	api.Backup
	files map[string][]byte
}

// presets is a part of the API's table of AI keys (GET /v1/secrets/presets), as much of it as the tests ask: a secret
// set under one of these names is kept outside the machines and on every one of them, unless it is said otherwise.
var presets = []api.SecretPreset{
	{Object: "secret_preset", ID: "anthropic", Title: "Anthropic", Names: []string{"ANTHROPIC_API_KEY"}, Hosts: []string{"api.anthropic.com"},
		Headers: []api.HeaderRule{{Name: "x-api-key", Format: "{value}"}, {Name: "authorization", Format: "Bearer {value}"}}, Query: []string{}},
	{Object: "secret_preset", ID: "openai", Title: "OpenAI", Names: []string{"OPENAI_API_KEY"},
		Hosts: []string{"api.openai.com", "us.api.openai.com", "eu.api.openai.com"}, Headers: []api.HeaderRule{{Name: "authorization", Format: "Bearer {value}"}},
		Query: []string{}},
	{Object: "secret_preset", ID: "gemini", Title: "Gemini", Names: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, Hosts: []string{"generativelanguage.googleapis.com"},
		Headers: []api.HeaderRule{{Name: "x-goog-api-key", Format: "{value}"}, {Name: "authorization", Format: "Bearer {value}"}}, Query: []string{"key"}},
	{Object: "secret_preset", ID: "huggingface", Title: "Hugging Face", Names: []string{"HF_TOKEN"}, Hosts: []string{"router.huggingface.co", "huggingface.co"},
		Headers: []api.HeaderRule{{Name: "authorization", Format: "Bearer {value}"}}, Query: []string{}},
}

// presetOutside is how a secret of this name is kept by its preset; nil for a name with none.
func presetOutside(name string) *api.Outside {
	for _, p := range presets {
		if slices.Contains(p.Names, name) {
			id := p.ID
			return &api.Outside{Preset: &id, Hosts: slices.Clone(p.Hosts), Header: p.Headers[0].Name, Format: p.Headers[0].Format,
				Headers: slices.Clone(p.Headers), Query: slices.Clone(p.Query)}
		}
	}
	return nil
}

// reaches says whether a secret on every machine reaches this one, as the API says it: an open machine always, one
// that reaches only its list when the list names one of the secret's hosts, a closed one never.
func reaches(k *keptSecret, m *api.Machine) bool {
	if !k.every || k.outside == nil || m.State == api.StateDeleted {
		return false
	}
	switch m.Network.Internet {
	case "closed":
		return false
	case "allowed":
		for _, h := range k.outside.Hosts {
			for _, a := range m.Network.Allow {
				if a == h || strings.HasPrefix(a, "*.") && (h == a[2:] || strings.HasSuffix(h, a[1:])) {
					return true
				}
			}
		}
		return false
	}
	return true
}

// Org is the organisation a key is made in, and the person who made it, as the API knows them there.
type Org struct {
	ID     string
	Name   string
	Person api.Person
	Role   string
	Teams  []string // the projects of their teams, for a member; nil for any other role
	Limits api.PersonLimits
	Levels map[string]string // their level in each team they manage, by its project
}

type login struct {
	name   string
	scopes []string
	asks   int
	done   bool
}

type keptAnswer struct {
	hash   string
	status int
	body   []byte
}

// New starts a fake API for the test, with one project and the key that works; it stops when the test ends.
func New(t testing.TB) *Server {
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := &Server{
		Key:      "ak_live_" + strings.Repeat("abcd", 10),
		Account:  "acc_test",
		Now:      func() time.Time { return base },
		files:    map[string][]byte{},
		secrets:  map[string]*keptSecret{},
		ports:    map[string][]api.Port{},
		apps:     map[string][]api.App{},
		appPorts: map[string]string{},
		kept:     map[string]keptAnswer{},
		logins:   map[string]*login{},
		chosen:   map[string]string{},
		Models:   defaultModels(),
		Agents:   defaultAgents(),
		Regions:  []api.Region{{ID: "helsinki", City: "Helsinki", Country: "FI"}, {ID: "mumbai", City: "Mumbai", Country: "IN"}},
		GPUs: api.GpuList{Object: "list", ForSale: true, Data: []api.GpuOffer{{ID: "h100-80", Model: "H100 80GB", Count: 1,
			MemoryGB: 80, CPUs: 16, RAMGB: 128, Location: &api.Location{City: "Helsinki", Country: "FI"}, PricePerHourUSD: 2.49,
			Available: true}}},
	}
	s.projects = []api.Project{{ID: s.newID("prj_"), Object: "project", Name: "Default", CreatedAt: base}}
	rate := 600
	s.keys = []api.Key{{ID: "key_00000000000000a1", Object: "key", Name: "laptop", Scopes: []string{api.ScopeAll}, Projects: []string{},
		Shown: "ak_live_abcd…abcd", State: "active", RatePerMinute: &rate, CreatedAt: base}}
	mux := http.NewServeMux()
	for pattern, fn := range map[string]http.HandlerFunc{
		"GET /v1/me":                       s.me,
		"GET /v1/usage":                    s.usage,
		"GET /v1/credit":                   s.credit,
		"POST /v1/credit/purchases":        s.buyCredit,
		"GET /v1/usage/limits":             s.limits,
		"PUT /v1/usage/limits/account":     s.setAccountLimit,
		"GET /v1/regions":                  s.regions,
		"GET /v1/gpus":                     s.gpus,
		"GET /v1/projects":                 s.listProjects,
		"POST /v1/projects":                s.createProject,
		"GET /v1/projects/{id}":            s.getProject,
		"PATCH /v1/projects/{id}":          s.renameProject,
		"DELETE /v1/projects/{id}":         s.deleteProject,
		"GET /v1/keys":                     s.listKeys,
		"POST /v1/keys":                    s.createKey,
		"POST /v1/confirm-codes":           s.sendConfirmCode,
		"DELETE /v1/keys/{id}":             s.revokeKey,
		"GET /v1/machines":                 s.listMachines,
		"POST /v1/machines":                s.createMachine,
		"GET /v1/machines/{id}":            s.getMachine,
		"PATCH /v1/machines/{id}":          s.updateMachine,
		"DELETE /v1/machines/{id}":         s.deleteMachine,
		"POST /v1/machines/{id}/start":     s.move("start"),
		"POST /v1/machines/{id}/pause":     s.move("pause"),
		"POST /v1/machines/{id}/stop":      s.move("stop"),
		"POST /v1/machines/{id}/archive":   s.move("archive"),
		"POST /v1/machines/{id}/fork":      s.forkMachine,
		"POST /v1/machines/{id}/copy":      s.forkMachine, // a fork's route before it was one, kept
		"POST /v1/machines/{id}/fork-for":  s.give(api.RequestFork),
		"POST /v1/machines/{id}/hand-off":  s.give(api.RequestHandOff),
		"POST /v1/machines/{id}/take-back": s.back(true),
		"POST /v1/machines/{id}/give-back": s.back(false),
		"GET /v1/requests":                 s.listRequests,
		"POST /v1/requests":                s.askRequest,
		"DELETE /v1/requests/{id}":         s.takeBack,
		"GET /v1/projects/{id}/rules":      s.teamRules,
		"GET /v1/projects/{id}/types":      s.listTypes,
		"POST /v1/machines/batch":          s.createMachines,
		"POST /v1/machines/{id}/backups":   s.backup,
		"GET /v1/machines/{id}/backups":    s.listMachineBackups,
		"GET /v1/backups":                  s.listBackups,
		"GET /v1/machines/{id}/wait":       s.wait,
		"GET /v1/machines/{id}/metrics":    s.metrics,
		"POST /v1/machines/{id}/exec":      s.exec,
		"POST /v1/ssh/certificates":        s.sshCertificate,
		"GET /v1/machines/{id}/terminal":   s.terminal,
		"GET /v1/machines/{id}/files":      s.readFile,
		"PUT /v1/machines/{id}/files":      s.writeFile,
		"GET /v1/secrets":                  s.listSecrets,
		"GET /v1/secrets/presets":          s.listPresets,
		"PUT /v1/secrets/{name}":           s.setSecret,
		"DELETE /v1/secrets/{name}":        s.deleteSecret,

		"GET /v1/code":                        s.code,
		"GET /v1/code/connections/{id}/repos": s.codeRepos,
		"PUT /v1/projects/{id}/code":          s.grantCode,

		"GET /v1/folders":               s.listFolders,
		"GET /v1/folders/{id}/list":     s.listFolder,
		"GET /v1/folders/{id}/files":    s.getFolderFile,
		"PUT /v1/folders/{id}/files":    s.putFolderFile,
		"DELETE /v1/folders/{id}/files": s.deleteFolderFile,
		"POST /v1/folders/{id}/move":    s.moveFolderItem,

		"GET /v1/machines/{id}/ports":              s.listPorts,
		"POST /v1/machines/{id}/ports":             s.openPort,
		"DELETE /v1/machines/{id}/ports/{port}":    s.closePort,
		"POST /v1/machines/{id}/ports/{port}/link": s.linkPort,

		"GET /v1/machines/{id}/apps":             s.listApps,
		"POST /v1/machines/{id}/apps/{app}/on":   s.turnApp("on"),
		"POST /v1/machines/{id}/apps/{app}/off":  s.turnApp("off"),
		"POST /v1/machines/{id}/apps/{app}/link": s.turnApp("link"),
		"POST /v1/machines/{id}/apps/{app}/show": s.turnApp("show"),

		"POST /v1/login/devices":       s.startLogin,
		"POST /v1/login/devices/token": s.loginToken,

		"GET /v1/models":                               s.listModels,
		"GET /v1/agents":                               s.listAgents,
		"PUT /v1/agents/model":                         s.chooseModel(true),
		"DELETE /v1/agents/model":                      s.clearModel(true),
		"PUT /v1/agents/{agent}/model":                 s.chooseModel(false),
		"DELETE /v1/agents/{agent}/model":              s.clearModel(false),
		"GET /v1/models/allowed":                       s.listAllowed,
		"PUT /v1/models/allowed/organisation":          s.setAllowed,
		"DELETE /v1/models/allowed/organisation":       s.clearAllowed,
		"PUT /v1/models/allowed/teams/{project_id}":    s.setAllowed,
		"DELETE /v1/models/allowed/teams/{project_id}": s.clearAllowed,
		"PUT /v1/models/allowed/roles/{role}":          s.setAllowed,
		"DELETE /v1/models/allowed/roles/{role}":       s.clearAllowed,
		"PUT /v1/models/allowed/people/{person_id}":    s.setAllowed,
		"DELETE /v1/models/allowed/people/{person_id}": s.clearAllowed,

		"GET /v1/orgs":         s.listOrgs,
		"GET /v1/audit":        s.listAudit,
		"GET /v1/audit/export": s.exportAudit,
	} {
		mux.HandleFunc(pattern, fn)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such route"})
	})
	s.srv = httptest.NewServer(s.front(mux))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Requests is every request received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Last is the last request received for this method and path, and whether there was one.
func (s *Server) Last(method, path string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if r := s.requests[i]; r.Method == method && r.Path == path {
			return r, true
		}
	}
	return Request{}, false
}

// Project is the first project's id.
func (s *Server) Project() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects[0].ID
}

// AddMachine makes a machine of this name and state directly, as if made before.
func (s *Server) AddMachine(name, state string) api.Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.newMachine(name, s.projects[0].ID, nil)
	m.State = state
	return *m
}

// Machine is the machine as the fake holds it now, and whether it holds it.
func (s *Server) Machine(id string) (api.Machine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(id); m != nil {
		return *m, true
	}
	return api.Machine{}, false
}

// SetPauseReason says why a machine is paused, as the API says it while a limit holds it so: for the tests alone.
func (s *Server) SetPauseReason(machine string, r *api.PauseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(machine); m != nil {
		m.PauseReason = r
	}
}

// SetHeldBy says whose a machine is, as an organisation's machine list would: for the tests alone.
func (s *Server) SetHeldBy(machine string, h *api.HeldBy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(machine); m != nil {
		m.HeldBy = h
	}
}

// Secret is the value a secret was set to, and whether it is set: what reached the API, for the tests alone.
func (s *Server) Secret(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.secrets[name]; ok {
		return k.value, true
	}
	return "", false
}

// File is a file in a machine, and whether there is one.
func (s *Server) File(machine, path string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[machine+" "+path]
	return b, ok
}

// SetFile puts a file in a machine.
func (s *Server) SetFile(machine, path string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[machine+" "+path] = content
}

// Ports is a machine's open ports as the fake holds them now, lowest first.
func (s *Server) Ports(machine string) []api.Port {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ports[machine])
}

type ctxKey struct{}

// front records the request, gives it an id, checks the key and the Refuse hook, as the API's front does.
func (s *Server) front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.calls++
		id := "req_" + strconv.Itoa(s.calls)
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body})
		hook := s.Refuse
		s.mu.Unlock()
		w.Header().Set("X-Request-Id", id)
		// a program signing in has no key yet
		open := r.Method == http.MethodPost && (r.URL.Path == "/v1/login/devices" || r.URL.Path == "/v1/login/devices/token")
		if !open && r.Header.Get("Authorization") != "Bearer "+s.Key {
			refuse(w, r, Refusal{Status: 401, Code: "unauthorized", Detail: "this key is not known"})
			return
		}
		if hook != nil {
			if rf := hook(r); rf != nil {
				refuse(w, r, *rf)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

var titles = map[string]string{"invalid_request": "The request is not right", "unauthorized": "No key, or a key that does not work",
	"forbidden": "This key may not do that", "not_found": "Not found", "conflict": "Not possible in the state it is in",
	"project_not_empty": "The project still holds machines", "rate_limited": "Too many calls just now",
	"limit_reached": "A limit is reached", "unavailable": "Not available just now", "timeout": "Not in time", "internal": "Something went wrong on our side",
	"confirm_required": "A confirm code is needed", "confirm_wrong": "The confirm code is not right",
	"not_open": "Not available", "login_denied": "The sign-in was refused", "login_expired": "The sign-in's code is no longer good"}

func refuse(w http.ResponseWriter, _ *http.Request, rf Refusal) {
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	if rf.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(rf.RetryAfter))
	}
	w.WriteHeader(rf.Status)
	p := map[string]any{"type": "https://docs.wheelmachine.ai/errors/" + rf.Code, "title": titles[rf.Code], "status": rf.Status,
		"detail": rf.Detail, "code": rf.Code, "request_id": h.Get("X-Request-Id")}
	if rf.RetryAfter > 0 {
		p["retry_after_s"] = rf.RetryAfter
	}
	if rf.Confirm != nil {
		p["confirm"] = rf.Confirm
	}
	_ = json.NewEncoder(w).Encode(p)
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a request's JSON as the API does: no field it does not know, an empty body allowed when `optional`.
func decode(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	raw, _ := io.ReadAll(r.Body)
	if len(bytes.TrimSpace(raw)) == 0 {
		if optional {
			return true
		}
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "this needs a JSON body"})
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the body is not right: " + err.Error()})
		return false
	}
	return true
}

// once answers a call that makes something: the first answer again for an Idempotency-Key used before with the
// same request, an error for one used with another.
func (s *Server) once(w http.ResponseWriter, r *http.Request, body []byte, answer func() (int, any, *Refusal)) {
	key := r.Header.Get("Idempotency-Key")
	hash := r.Method + " " + r.URL.Path + "\n" + string(body)
	if key != "" {
		s.mu.Lock()
		kept, ok := s.kept[key]
		s.mu.Unlock()
		if ok {
			if kept.hash != hash {
				refuse(w, r, Refusal{Status: 422, Code: "invalid_request", Detail: "this Idempotency-Key was used with a different request"})
				return
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(kept.status)
			_, _ = w.Write(kept.body)
			return
		}
	}
	status, v, rf := answer()
	if rf != nil {
		refuse(w, r, *rf)
		return
	}
	out, _ := json.Marshal(v)
	out = append(out, '\n')
	if key != "" {
		s.mu.Lock()
		s.kept[key] = keptAnswer{hash: hash, status: status, body: out}
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

func (s *Server) newID(prefix string) string {
	s.ids++
	if prefix == "key_" {
		return fmt.Sprintf("key_%016x", s.ids)
	}
	return prefix + fmt.Sprintf("%026d", s.ids)
}

func (s *Server) newMachine(name, project string, labels map[string]string) *api.Machine {
	if labels == nil {
		labels = map[string]string{}
	}
	after, cpus, memory := 300, 2, 4.0
	m := &api.Machine{ID: s.newID("m_"), Object: "machine", Name: name, Project: project, MadeBy: s.madeBy(), HeldBy: s.heldBy(),
		State: api.StateRunning,
		Size:  &api.Size{CPUs: 2, MemoryGB: 4, DiskGB: 32}, Location: &api.Location{City: "Helsinki", Country: "FI"},
		CPUs: &cpus, MemoryGB: &memory, SleepWhenIdle: true, IdleAfterS: &after, Labels: labels, FilesSealed: true,
		Network: api.Network{Internet: "open", Allow: []string{}}, CreatedAt: s.Now().UTC()}
	s.machines = append(s.machines, m)
	return m
}

func (s *Server) find(id string) *api.Machine {
	for _, m := range s.machines {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// machine is the route's machine, or an error answered.
func (s *Server) machine(w http.ResponseWriter, r *http.Request) *api.Machine {
	s.mu.Lock()
	m := s.find(r.PathValue("id"))
	s.mu.Unlock()
	if m == nil {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such machine"})
	}
	return m
}

func (s *Server) view(m *api.Machine) api.Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyOf(m)
}

// copyOf is a machine as answered, sharing nothing with the one kept, with the AI keys that reach it: the lock held.
func (s *Server) copyOf(m *api.Machine) api.Machine {
	out := *m
	out.Labels = maps.Clone(m.Labels)
	out.Secrets = append([]string{}, m.Secrets...)
	out.AIKeys = []string{}
	for _, name := range slices.Sorted(maps.Keys(s.secrets)) {
		if reaches(s.secrets[name], m) {
			out.AIKeys = append(out.AIKeys, name)
		}
	}
	return out
}

// secretNames checks the secrets a machine is to get, as the API does: each set already; nil when none are asked for.
// checkPolicy and normPolicy stand in for the API's own reading of a policy: a known word, allow only for "allowed",
// and the list always present.
func checkPolicy(n *api.Network) *Refusal {
	if n == nil {
		return nil
	}
	switch n.Internet {
	case "open", "closed", "allowed":
	default:
		return &Refusal{Status: 400, Code: "invalid_request", Detail: `network.internet is "open", "closed" or "allowed"`}
	}
	if n.Internet != "allowed" && len(n.Allow) > 0 {
		return &Refusal{Status: 400, Code: "invalid_request", Detail: `network.allow is only for "allowed"`}
	}
	return nil
}

func normPolicy(n api.Network) api.Network {
	if n.Allow == nil {
		n.Allow = []string{}
	}
	return n
}

func (s *Server) secretNames(asked []string) ([]string, *Refusal) {
	names := slices.Clone(asked)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, n := range names {
		if _, ok := s.secrets[n]; !ok {
			return nil, &Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("no secret %s: set it first (PUT /v1/secrets/%s)", n, n)}
		}
	}
	return names, nil
}

func page(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "limit is between 1 and 200"})
			return 0, false
		}
		limit = n
	}
	return limit, true
}

// me answers who is calling as the API does: in a person's own account they are its owner, with no teams and no limits
// of their own; in an organisation, the person who made the key, with their role, teams and limits there.
func (s *Server) me(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	k, org := s.keys[0], s.Org
	s.mu.Unlock()
	out := map[string]any{"object": "me", "account": map[string]string{"id": s.Account, "kind": "person", "name": "Test Person"},
		"person": api.Person{ID: s.Account, Name: "Test Person"}, "role": api.RoleOwner, "teams": nil, "limits": nil, "scopes": k.Scopes,
		"key": k, "standing": map[string]any{"can_spend": true, "balance_usd": 10.5, "credit_usd": s.Credit.BalanceUSD}}
	if org != nil {
		out["account"] = map[string]string{"id": org.ID, "kind": "org", "name": org.Name}
		out["person"], out["role"], out["teams"], out["limits"] = org.Person, org.Role, org.Teams, org.Limits
		out["levels"] = org.Levels
	}
	reply(w, 200, out)
}

// madeBy is who what is made now is said to be made by: in an organisation, the person behind the key; nobody in a
// person's own account. The lock held.
func (s *Server) madeBy() *string {
	if s.Org == nil {
		return nil
	}
	id := s.Org.Person.ID
	return &id
}

// heldBy is whose what is made now is said to be: in an organisation, the person behind the key, with their role there;
// nobody in a person's own account. The lock held.
func (s *Server) heldBy() *api.HeldBy {
	if s.Org == nil {
		return nil
	}
	role := s.Org.Role
	return &api.HeldBy{ID: s.Org.Person.ID, Name: s.Org.Person.Name, Role: &role}
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	if month == "" {
		month = s.Now().UTC().Format("2006-01")
	} else if _, err := time.Parse("2006-01", month); err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "month is YYYY-MM"})
		return
	}
	u := s.Usage
	u.Object, u.Month = "usage", month
	if u.Machines == nil {
		u.Machines = []api.UsageLine{}
	}
	reply(w, 200, u)
}

// buyCredit starts a checkout to buy credit, as the API does: one of the amounts the credit says, while buying is open
// (Credit.Buy); refused as not open otherwise.
func (s *Server) buyCredit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AmountUSD int64 `json:"amount_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the body is not JSON"})
		return
	}
	s.mu.Lock()
	add := s.Credit.Buy
	account := s.Account
	s.mu.Unlock()
	if !add.Open || add.Paddle == nil {
		refuse(w, r, Refusal{Status: 501, Code: "not_open", Detail: "buying credit is not open yet"})
		return
	}
	if !slices.Contains(add.AmountsUSD, body.AmountUSD) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "amount_usd is 10, 25, 50 or 100"})
		return
	}
	var t api.CreditPurchase
	t.Object, t.ID, t.Account, t.AmountUSD, t.CreatedAt = "credit_purchase", "cp_01jb2xk3n4p5q6r7s8t9v0w1x2", account, float64(body.AmountUSD), time.Now().UTC()
	t.Checkout.Provider, t.Checkout.Environment, t.Checkout.ClientToken = "paddle", add.Paddle.Environment, add.Paddle.ClientToken
	// as the API: no payment until Paddle's checkout makes one, and the price of each amount for it to move between
	for _, usd := range add.AmountsUSD {
		price := fmt.Sprintf("pri_01jb2xq4c6d8e1f3g5h7j9k%03d", usd)
		t.Checkout.Prices = append(t.Checkout.Prices, api.CreditPrice{AmountUSD: float64(usd), PriceID: price})
		if usd == body.AmountUSD {
			t.Checkout.PriceID = price
		}
	}
	t.Checkout.CustomData = map[string]string{"wheelmachine_account": account, "wheelmachine_purchase": t.ID}
	t.Checkout.URL = "https://app.wheelmachine.ai/billing?purchase=" + t.ID
	reply(w, 201, t)
}

// credit answers the credit a page at a time, as the API does: `after` is the place in its entries the page before
// ended at.
func (s *Server) credit(w http.ResponseWriter, r *http.Request) {
	limit, ok := page(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	out := s.Credit
	entries := append([]api.CreditEntry{}, s.Credit.Data...)
	s.mu.Unlock()
	from := 0
	if after := r.URL.Query().Get("after"); after != "" {
		n, err := strconv.Atoi(after)
		if err != nil || n < 1 || n > len(entries) {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "after is the `next` of the page before"})
			return
		}
		from = n
	}
	to := min(from+limit, len(entries))
	out.Object, out.Data, out.Next = "credit", entries[from:to], nil
	if out.Account == "" {
		out.Account = s.Account
	}
	if to < len(entries) {
		next := strconv.Itoa(to)
		out.Next = &next
	}
	reply(w, 200, out)
}

func (s *Server) limits(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, 200, s.limitsNow())
}

// What the limits answer says: those set, with the month's start, as the API counts it.
func (s *Server) limitsNow() api.UsageLimits {
	l := s.Limits
	now := s.Now().UTC()
	l.Object, l.Month = "usage_limits", now.Format("2006-01")
	l.ResetsAt = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	for _, list := range []*[]int{&l.AlertPercents} {
		if *list == nil {
			*list = []int{50, 75, 90, 100}
		}
	}
	if l.Teams == nil {
		l.Teams = []api.TeamLimit{}
	}
	if l.People == nil {
		l.People = []api.PersonLimit{}
	}
	if l.Yours == nil {
		l.Yours = []api.LimitNear{}
	}
	if l.Passed == nil {
		l.Passed = []api.LimitPassed{}
	}
	return l
}

func (s *Server) setAccountLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MonthCapUSD *float64 `json:"month_cap_usd"`
	}
	raw, _ := io.ReadAll(r.Body)
	if !bytes.Contains(raw, []byte(`"month_cap_usd"`)) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "say month_cap_usd: dollars a month, or null for no limit of the account's own"})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if !decode(w, r, &body, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Limits.Account.MonthCapUSD = body.MonthCapUSD
	s.Limits.Account.InForceUSD, s.Limits.Account.Limited = body.MonthCapUSD, body.MonthCapUSD != nil
	s.Limits.Account.InForce = ""
	if body.MonthCapUSD != nil {
		s.Limits.Account.InForce = "account"
	}
	reply(w, 200, s.limitsNow())
}

func (s *Server) regions(w http.ResponseWriter, _ *http.Request) {
	reply(w, 200, map[string]any{"object": "list", "data": s.Regions})
}

func (s *Server) gpus(w http.ResponseWriter, _ *http.Request) { reply(w, 200, s.GPUs) }

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	limit, ok := page(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after")
	s.mu.Lock()
	var rows []api.Project
	for _, p := range s.projects {
		if after == "" || p.ID > after {
			rows = append(rows, p)
		}
	}
	s.mu.Unlock()
	out := map[string]any{"object": "list", "data": []api.Project{}, "next": nil}
	if len(rows) > limit {
		out["next"] = rows[limit-1].ID
		rows = rows[:limit]
	}
	if rows != nil {
		out["data"] = rows
	}
	reply(w, 200, out)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		if strings.TrimSpace(in.Name) == "" {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "a project needs a name"}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		p := api.Project{ID: s.newID("prj_"), Object: "project", Name: in.Name, CreatedAt: s.Now().UTC()}
		s.projects = append(s.projects, p)
		w.Header().Set("Location", "/v1/projects/"+p.ID)
		return 201, p, nil
	})
}

func (s *Server) projectAt(id string) int {
	return slices.IndexFunc(s.projects, func(p api.Project) bool { return p.ID == id })
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	i := s.projectAt(r.PathValue("id"))
	var p api.Project
	if i >= 0 {
		p = s.projects[i]
	}
	s.mu.Unlock()
	if i < 0 {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such project"})
		return
	}
	reply(w, 200, p)
}

func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	s.mu.Lock()
	i := s.projectAt(r.PathValue("id"))
	var p api.Project
	if i >= 0 {
		s.projects[i].Name = in.Name
		p = s.projects[i]
	}
	s.mu.Unlock()
	if i < 0 {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such project"})
		return
	}
	reply(w, 200, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	i := s.projectAt(id)
	busy := slices.ContainsFunc(s.machines, func(m *api.Machine) bool { return m.Project == id })
	if i >= 0 && !busy {
		s.projects = slices.Delete(s.projects, i, i+1)
	}
	s.mu.Unlock()
	switch {
	case i < 0:
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such project"})
	case busy:
		refuse(w, r, Refusal{Status: 409, Code: "project_not_empty", Detail: "delete its machines first"})
	default:
		w.WriteHeader(204)
	}
}

func (s *Server) listKeys(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	keys := slices.Clone(s.keys)
	s.mu.Unlock()
	reply(w, 200, map[string]any{"object": "list", "data": keys})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		Projects      []string `json:"projects"`
		ExpiresAt     *string  `json:"expires_at"`
		RatePerMinute *int     `json:"rate_per_minute"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	step := ""
	switch {
	case slices.Contains(in.Scopes, api.ScopeAll):
		step = "full_access_key"
	case slices.Contains(in.Scopes, "cloud:keys:write"):
		step = "key_making_key"
	}
	if s.ConfirmCode != "" && step != "" {
		ask := &api.ConfirmAsk{Step: step, Account: s.Account}
		switch got := r.Header.Get("X-Confirm-Code"); {
		case got == "":
			refuse(w, r, Refusal{Status: 403, Code: "confirm_required", Detail: "this needs a code we e-mail you", Confirm: ask})
			return
		case got != s.ConfirmCode:
			refuse(w, r, Refusal{Status: 403, Code: "confirm_wrong", Detail: "that code is not right: 4 tries left", Confirm: ask})
			return
		}
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		if in.Name == "" || len(in.Scopes) == 0 {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "a key needs a name and its scopes"}
		}
		for _, sc := range in.Scopes {
			if !slices.Contains(api.Scopes, sc) {
				return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("no scope %q", sc)}
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		k := api.Key{ID: s.newID("key_"), Object: "key", Name: in.Name, Scopes: in.Scopes, Projects: in.Projects, State: "active",
			RatePerMinute: in.RatePerMinute, CreatedAt: s.Now().UTC(), MadeBy: s.madeBy()}
		if k.Projects == nil {
			k.Projects = []string{}
		}
		if in.ExpiresAt != nil {
			t, err := time.Parse(time.RFC3339, *in.ExpiresAt)
			if err != nil {
				return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "expires_at is a time like 2026-12-31T23:59:59Z"}
			}
			k.ExpiresAt = &t
		}
		secret := "ak_live_" + strings.Repeat("z", 36) + fmt.Sprintf("%04d", s.ids)
		k.Shown = secret[:12] + "…" + secret[len(secret)-4:]
		s.keys = append(s.keys, k)
		w.Header().Set("Location", "/v1/keys/"+k.ID)
		return 201, api.KeyCreated{Key: k, Secret: secret}, nil
	})
}

// A confirm code "e-mailed" for a risky step: the one ConfirmCode says, to the account's address.
func (s *Server) sendConfirmCode(w http.ResponseWriter, r *http.Request) {
	var in api.ConfirmAsk
	if !decode(w, r, &in, false) {
		return
	}
	if in.Step != "full_access_key" && in.Step != "key_making_key" {
		refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: "this is done signed in to the dashboard, never with a key"})
		return
	}
	s.mu.Lock()
	s.ConfirmsSent++
	s.mu.Unlock()
	reply(w, 201, api.ConfirmCodeSent{Object: "confirm_code", ConfirmAsk: api.ConfirmAsk{Step: in.Step, Account: or(in.Account, s.Account)},
		ExpiresAt: s.Now().UTC().Add(10 * time.Minute), SentTo: "a…@example.com"})
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	i := slices.IndexFunc(s.keys, func(k api.Key) bool { return k.ID == r.PathValue("id") })
	if i >= 0 {
		s.keys = slices.Delete(s.keys, i, i+1)
	}
	s.mu.Unlock()
	if i < 0 {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such key"})
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	limit, ok := page(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	state, project, after := q.Get("state"), q.Get("project"), q.Get("after")
	if state != "" && !slices.Contains(api.States, state) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("no state %q", state)})
		return
	}
	want := map[string]string{}
	for _, l := range q["label"] {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "a label is asked for as name=value"})
			return
		}
		want[k] = v
	}
	s.mu.Lock()
	var rows []api.Machine
	for i := len(s.machines) - 1; i >= 0; i-- {
		m := s.machines[i]
		if after != "" && m.ID >= after || state != "" && m.State != state || project != "" && m.Project != project {
			continue
		}
		match := true
		for k, v := range want {
			if m.Labels[k] != v {
				match = false
			}
		}
		if match {
			rows = append(rows, s.copyOf(m))
		}
	}
	s.mu.Unlock()
	out := map[string]any{"object": "list", "data": []api.Machine{}, "next": nil}
	if len(rows) > limit {
		out["next"] = rows[limit-1].ID
		rows = rows[:limit]
	}
	if rows != nil {
		out["data"] = rows
	}
	reply(w, 200, out)
}

func waitOK(w http.ResponseWriter, r *http.Request, v *float64) bool {
	if v != nil && (*v < 0 || *v > 600) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "wait_s is between 0 and 600"})
		return false
	}
	return true
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Name       string            `json:"name"`
		Project    string            `json:"project"`
		Region     string            `json:"region"`
		GPU        string            `json:"gpu"`
		Labels     map[string]string `json:"labels"`
		WaitS      *float64          `json:"wait_s"`
		SleepIdle  *bool             `json:"sleep_when_idle"`
		IdleAfterS *int              `json:"idle_after_s"`
		CPUs       *int              `json:"cpus"`
		Secrets    []string          `json:"secrets"`
		Network    *api.Network      `json:"network"`
		PutAwayS   *int              `json:"put_away_after_s"`
		Type       string            `json:"type"`
	}
	if !decode(w, r, &in, true) || !waitOK(w, r, in.WaitS) {
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if in.Type != "" {
			if in.CPUs != nil || in.GPU != "" || in.Network != nil || in.SleepIdle != nil || in.IdleAfterS != nil || in.PutAwayS != nil {
				return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "a machine made from a type is made as the type says"}
			}
			project := s.projects[0].ID
			if in.Project != "" {
				project = in.Project
			}
			k, rf := s.typeIn(project, in.Type)
			if rf != nil {
				return 0, nil, rf
			}
			m := s.ofType(k, or(in.Name, k.Name), in.Labels, s.heldBy())
			w.Header().Set("Location", "/v1/machines/"+m.ID)
			return 201, s.copyOf(m), nil
		}
		secrets, rf := s.secretNames(in.Secrets)
		if rf != nil {
			return 0, nil, rf
		}
		project := s.projects[0].ID
		if in.Project != "" {
			if s.projectAt(in.Project) < 0 {
				return 0, nil, &Refusal{Status: 404, Code: "not_found", Detail: "no such project"}
			}
			project = in.Project
		}
		if in.Region != "" && !slices.ContainsFunc(s.Regions, func(g api.Region) bool { return g.ID == in.Region }) {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("no region %q; GET /v1/regions lists them", in.Region)}
		}
		if len(in.Labels) > 32 {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "a machine holds at most 32 labels"}
		}
		if in.IdleAfterS != nil && (*in.IdleAfterS < 60 || *in.IdleAfterS > 86400 || in.SleepIdle != nil && !*in.SleepIdle) {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "idle_after_s is between 60 and 86400, for a machine that sleeps when idle"}
		}
		if in.CPUs != nil && *in.CPUs == 16 && in.GPU == "" {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "16 CPUs are not offered yet: choose 2, 4 or 8"}
		}
		if in.CPUs != nil && (!slices.Contains([]int{2, 4, 8}, *in.CPUs) || in.GPU != "") {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "cpus is 2, 4 or 8, for a machine without a GPU"}
		}
		if rf := checkPolicy(in.Network); rf != nil {
			return 0, nil, rf
		}
		m := s.newMachine(in.Name, project, in.Labels)
		m.Secrets = secrets
		if in.Network != nil {
			m.Network = normPolicy(*in.Network)
		}
		if in.GPU != "" {
			m.GPU = &api.GpuRef{ID: in.GPU}
			m.CPUs, m.MemoryGB = nil, nil
		}
		if in.CPUs != nil {
			resize(m, *in.CPUs)
		}
		switch {
		case in.SleepIdle != nil && !*in.SleepIdle:
			m.SleepWhenIdle, m.IdleAfterS = false, nil
		case in.IdleAfterS != nil:
			after := *in.IdleAfterS
			m.IdleAfterS = &after
		}
		if in.PutAwayS != nil && *in.PutAwayS > 0 {
			after := *in.PutAwayS
			m.PutAwayAfterS = &after
		}
		w.Header().Set("Location", "/v1/machines/"+m.ID)
		out := s.copyOf(m)
		if s.Slow {
			m.State = api.StateStarting
			out.State = api.StateStarting
			return 202, out, nil
		}
		ready := 12.5
		out.ReadyMS = &ready
		return 201, out, nil
	})
}

// resize gives a machine without a GPU its CPUs, with 2 GB of memory each.
func resize(m *api.Machine, cpus int) {
	memory := float64(2 * cpus)
	m.CPUs, m.MemoryGB = &cpus, &memory
	m.Size = &api.Size{CPUs: float64(cpus), MemoryGB: memory, DiskGB: 32}
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	if m := s.machine(w, r); m != nil {
		reply(w, 200, s.view(m))
	}
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	var in struct {
		Name    *string            `json:"name"`
		Labels  map[string]*string `json:"labels"`
		Secrets *[]string          `json:"secrets"`
		Network *api.Network       `json:"network"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if rf := checkPolicy(in.Network); rf != nil {
		refuse(w, r, *rf)
		return
	}
	s.mu.Lock()
	if in.Secrets != nil {
		names, rf := s.secretNames(*in.Secrets)
		if rf != nil {
			s.mu.Unlock()
			refuse(w, r, *rf)
			return
		}
		m.Secrets = names
	}
	if in.Network != nil {
		m.Network = normPolicy(*in.Network)
	}
	if in.Name != nil {
		m.Name = *in.Name
	}
	for k, v := range in.Labels {
		if v == nil {
			delete(m.Labels, k)
		} else {
			m.Labels[k] = *v
		}
	}
	s.mu.Unlock()
	reply(w, 200, s.view(m))
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	s.machines = slices.DeleteFunc(s.machines, func(x *api.Machine) bool { return x == m })
	delete(s.ports, m.ID) // its ports close with it, and its backups go
	s.backups = slices.DeleteFunc(s.backups, func(b keptBackup) bool { return b.Machine == m.ID })
	s.mu.Unlock()
	w.WriteHeader(204)
}

// move is an action that puts a machine in a state.
func (s *Server) move(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := s.machine(w, r)
		if m == nil {
			return
		}
		var in struct {
			WaitS *float64 `json:"wait_s"`
		}
		if action == "start" && (!decode(w, r, &in, true) || !waitOK(w, r, in.WaitS)) {
			return
		}
		s.mu.Lock()
		status, from := 200, m.State
		switch action {
		case "start":
			m.State = api.StateRunning
			if s.Slow {
				m.State, status = api.StateStarting, 202
			}
		case "pause":
			if from != api.StateRunning && from != api.StatePaused {
				s.mu.Unlock()
				refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "the machine cannot be paused in the state it is in"})
				return
			}
			m.State = api.StatePaused
		case "stop":
			m.State = api.StateStopped
		case "archive":
			m.State = api.StateArchived
		}
		s.mu.Unlock()
		reply(w, status, s.view(m))
	}
}

func (s *Server) forkMachine(w http.ResponseWriter, r *http.Request) {
	src := s.machine(w, r)
	if src == nil {
		return
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Name   *string           `json:"name"`
		Labels map[string]string `json:"labels"`
		Backup *string           `json:"backup"`
		CPUs   *int              `json:"cpus"`
		WaitS  *float64          `json:"wait_s"`
	}
	if !decode(w, r, &in, true) || !waitOK(w, r, in.WaitS) {
		return
	}
	if in.CPUs != nil && in.Backup == nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "cpus is only for a fork from a backup: a fork of the machine as it is now keeps its size"})
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		s.mu.Lock()
		defer s.mu.Unlock()
		// from one of its backups, as the API checks it: the backup first, then the size asked of it
		var from *keptBackup
		if in.Backup != nil {
			var rf *Refusal
			if from, rf = s.backupOf(src.ID, *in.Backup); rf != nil {
				return 0, nil, rf
			}
		}
		switch {
		case in.CPUs != nil && src.GPU != nil:
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "a GPU machine's CPUs come with its GPU: leave cpus out"}
		case in.CPUs != nil && *in.CPUs == 16:
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "16 CPUs are not offered yet: choose 2, 4 or 8"}
		case in.CPUs != nil && !slices.Contains([]int{2, 4, 8}, *in.CPUs):
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "cpus is 2, 4 or 8"}
		}
		if s.Org != nil && s.Org.Role == api.RoleMember && s.Org.Levels[src.Project] != api.LevelFull {
			return s.askToFork(w, src, from, in.CPUs)
		}
		name, labels := "", maps.Clone(src.Labels)
		if src.Name != "" {
			name = src.Name + "-fork"
		}
		if in.Name != nil {
			name = *in.Name
		}
		if in.Labels != nil {
			labels = in.Labels
		}
		m := s.newMachine(name, src.Project, labels)
		m.Secrets = slices.Clone(src.Secrets)
		// the original's size - or, from a backup, the one asked - and its files as they are now, or as the backup kept them
		switch {
		case in.CPUs != nil:
			resize(m, *in.CPUs)
		case src.GPU != nil:
			gpu := *src.GPU
			m.GPU, m.CPUs, m.MemoryGB = &gpu, nil, nil
		case src.CPUs != nil:
			resize(m, *src.CPUs)
		}
		files := s.filesOf(src.ID)
		if from != nil {
			files = from.files
		}
		for path, b := range files {
			s.files[m.ID+" "+path] = b
		}
		w.Header().Set("Location", "/v1/machines/"+m.ID)
		return 201, s.copyOf(m), nil
	})
}

// backupOf is the backup of a machine a fork is asked from - by its id, or its newest for "latest" - or the API's
// refusal. The lock held.
func (s *Server) backupOf(machine, asked string) (*keptBackup, *Refusal) {
	for i := len(s.backups) - 1; i >= 0; i-- {
		if b := &s.backups[i]; b.Machine == machine && (asked == "latest" || b.ID == asked) {
			return b, nil
		}
	}
	if asked == "latest" {
		return nil, &Refusal{Status: 409, Code: "conflict", Detail: "no backup of it yet: back it up first"}
	}
	return nil, &Refusal{Status: 404, Code: "not_found", Detail: "no such backup"}
}

// filesOf is a machine's files as they are now, by path. The lock held.
func (s *Server) filesOf(machine string) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range s.files {
		if id, path, ok := strings.Cut(k, " "); ok && id == machine {
			out[path] = v
		}
	}
	return out
}

// askToFork is a member's fork of a machine they hold, as the API answers it in an organisation: nothing made, but a
// request of theirs to them, 202, waiting first for the OK of the person who made the key as one asked with a key does - and asked again
// while it waits, the one there is, 200. A fork from a backup says which, and the size asked of it. The lock held.
func (s *Server) askToFork(w http.ResponseWriter, src *api.Machine, from *keptBackup, cpus *int) (int, any, *Refusal) {
	me := s.personName()
	for _, q := range s.Asks {
		if q.State == "waiting" && q.Kind == api.RequestFork && q.Holder.ID == me.ID && q.Receiver.ID == me.ID && q.Machine != nil &&
			q.Machine.ID != nil && *q.Machine.ID == src.ID {
			return 200, q, nil
		}
	}
	now, id, team := s.Now().UTC(), src.ID, src.Project
	q := api.Request{ID: s.newID("rq_"), Object: "request", Kind: api.RequestFork, Way: "give", Machine: &api.RequestMachine{ID: &id, Name: src.Name},
		Holder: me, Receiver: me, AskedBy: me, FromTeam: &team, State: "waiting", WaitingOn: &api.WaitingOn{Who: "asker", Name: &me.Name},
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if from != nil {
		backup := from.ID
		q.Backup = &backup
	}
	if cpus != nil {
		size := *cpus
		q.CPUs = &size
	}
	s.Asks = append(s.Asks, q)
	w.Header().Set("Location", "/v1/requests/"+q.ID)
	return 202, q, nil
}

// give forks a machine for someone, or hands it off, as the API does with a key: it waits for the OK of the person who made the key
// (Approve).
func (s *Server) give(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		src := s.machine(w, r)
		if src == nil {
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var in api.GiveTo
		if !decode(w, r, &in, false) {
			return
		}
		if in.To == "" {
			refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "No such teammate"})
			return
		}
		s.once(w, r, body, func() (int, any, *Refusal) {
			s.mu.Lock()
			defer s.mu.Unlock()
			now := s.Now().UTC()
			id := src.ID
			asker := s.personName()
			q := api.Request{ID: s.newID("rq_"), Object: "request", Kind: kind, Way: "give", Machine: &api.RequestMachine{ID: &id, Name: src.Name},
				Holder: asker, Receiver: api.PersonName{ID: in.To, Name: in.To}, AskedBy: asker, Why: in.Why, State: "waiting",
				WaitingOn: &api.WaitingOn{Who: "asker", Name: &asker.Name}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
			s.Asks = append(s.Asks, q)
			return 201, q, nil
		})
	}
}

// back answers a machine handed off taken back by the person behind the key (taking), or given back by them, as the API
// does with a key: a request of the way back, waiting for the OK of the person who made the key.
func (s *Server) back(taking bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		src := s.machine(w, r)
		if src == nil {
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var in api.BackTo
		if !decode(w, r, &in, true) {
			return
		}
		s.once(w, r, body, func() (int, any, *Refusal) {
			s.mu.Lock()
			defer s.mu.Unlock()
			now := s.Now().UTC()
			id := src.ID
			me, other := s.personName(), api.PersonName{ID: "acc_other", Name: "Ravi"}
			holder, receiver := other, me
			if !taking {
				holder, receiver = me, other
			}
			q := api.Request{ID: s.newID("rq_"), Object: "request", Kind: api.RequestHandOff, Way: "back", Machine: &api.RequestMachine{ID: &id,
				Name: src.Name}, Holder: holder, Receiver: receiver, AskedBy: me, State: "waiting", WaitingOn: &api.WaitingOn{Who: "asker",
				Name: &me.Name}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
			s.Asks = append(s.Asks, q)
			return 201, q, nil
		})
	}
}

// personName is the person behind the key, by id and name: the account in a person's own. The lock held.
func (s *Server) personName() api.PersonName {
	if s.Org != nil {
		return api.PersonName{ID: s.Org.Person.ID, Name: s.Org.Person.Name}
	}
	return api.PersonName{ID: s.Account, Name: "You"}
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	open := r.URL.Query().Get("open") == "true"
	s.mu.Lock()
	out := []api.Request{}
	for i := len(s.Asks) - 1; i >= 0; i-- {
		if q := s.Asks[i]; !open || q.State == "waiting" || q.State == "approved" {
			out = append(out, q)
		}
	}
	s.mu.Unlock()
	reply(w, 200, api.RequestList{Object: "list", Data: out})
}

func (s *Server) askRequest(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in api.RequestAsk
	if !decode(w, r, &in, false) {
		return
	}
	if in.Kind != api.RequestFork && in.Kind != api.RequestHandOff {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: `kind is "fork" or "hand_off"`})
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := s.Now().UTC()
		asker := s.personName()
		q := api.Request{ID: s.newID("rq_"), Object: "request", Kind: in.Kind, Way: "ask", Holder: api.PersonName{ID: in.Of, Name: in.Of},
			Receiver: asker, AskedBy: asker, Why: in.Why, State: "waiting", WaitingOn: &api.WaitingOn{Who: "asker", Name: &asker.Name},
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		s.Asks = append(s.Asks, q)
		return 201, q, nil
	})
}

// Approve is the OK of the person who made the key, of a request waiting for it, as they give it signed in to the dashboard: an ask then waits
// for its holder's pick; a member's fork of their own machine for a full manager; a hand-off for a manager when GivesWait,
// else it is carried out - a fork made for its receiver, or the machine theirs. False when no request of that id waits
// for the person who made the key.
func (s *Server) Approve(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.Asks {
		if q.ID != id || q.State != "waiting" || q.WaitingOn == nil || q.WaitingOn.Who != "asker" {
			continue
		}
		now := s.Now().UTC()
		switch {
		case q.Way == "ask":
			name := q.Holder.Name
			q.WaitingOn = &api.WaitingOn{Who: "holder", Name: &name}
		case s.GivesWait, q.Kind == api.RequestFork && q.Holder.ID == q.Receiver.ID:
			q.WaitingOn = &api.WaitingOn{Who: "manager"}
		default:
			q.State, q.WaitingOn, q.DoneAt = "done", nil, &now
			for _, m := range s.machines {
				if q.Machine == nil || q.Machine.ID == nil || m.ID != *q.Machine.ID {
					continue
				}
				if q.Kind == api.RequestFork {
					fork := s.newMachine(m.Name+"-fork", m.Project, maps.Clone(m.Labels))
					fork.HeldBy = &api.HeldBy{ID: q.Receiver.ID, Name: q.Receiver.Name}
					q.Result = &fork.ID
				} else {
					m.HeldBy = &api.HeldBy{ID: q.Receiver.ID, Name: q.Receiver.Name}
				}
				break
			}
		}
		s.Asks[i] = q
		return true
	}
	return false
}

// SetRules keeps a team's rules as GET /v1/projects/{id}/rules answers them, by their project.
func (s *Server) SetRules(r api.TeamRules) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Rules == nil {
		s.Rules = map[string]api.TeamRules{}
	}
	s.Rules[r.Project] = r
}

func (s *Server) takeBack(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.Asks {
		if q.ID != r.PathValue("id") {
			continue
		}
		if q.State != "waiting" {
			refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "Decided already"})
			return
		}
		q.State, q.WaitingOn = "taken_back", nil
		s.Asks[i] = q
		reply(w, 200, q)
		return
	}
	refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "No such request"})
}

func (s *Server) teamRules(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if s.projectAt(id) < 0 {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such project"})
		return
	}
	rules, ok := s.Rules[id]
	if !ok {
		rules = api.TeamRules{Object: "team_rules", Project: id, HandOffs: "ask", Forks: "ask", GPUs: true, OpenInternet: true, Models: "any"}
	}
	reply(w, 200, rules)
}

// backup keeps a machine's files as they are now, under an id shaped as the API's are: when it was taken, and a number.
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ids++
		now := s.Now().UTC()
		b := api.Backup{ID: fmt.Sprintf("%s-%08x", now.Format("20060102T150405Z"), s.ids), Object: "backup", Machine: m.ID, Note: in.Note,
			CreatedAt: now}
		s.backups = append(s.backups, keptBackup{Backup: b, files: s.filesOf(m.ID)})
		return 201, b, nil
	})
}

func (s *Server) listMachineBackups(w http.ResponseWriter, r *http.Request) {
	if m := s.machine(w, r); m != nil {
		s.listBackupsOf(w, r, m.ID)
	}
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) { s.listBackupsOf(w, r, "") }

// listBackupsOf answers a machine's backups, or every machine's for "", the newest first: as many as limit asks, 1 to
// 200, else the API's 100.
func (s *Server) listBackupsOf(w http.ResponseWriter, r *http.Request, machine string) {
	limit := 100
	if r.URL.Query().Get("limit") != "" {
		var ok bool
		if limit, ok = page(w, r); !ok {
			return
		}
	}
	s.mu.Lock()
	out := []api.Backup{}
	for i := len(s.backups) - 1; i >= 0 && len(out) < limit; i-- {
		if b := s.backups[i]; machine == "" || b.Machine == machine {
			out = append(out, b.Backup)
		}
	}
	s.mu.Unlock()
	reply(w, 200, api.BackupList{Object: "list", Data: out})
}

func (s *Server) wait(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("state")
	if !slices.Contains(api.States, want) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "say which state to wait for: state=running"})
		return
	}
	if t := r.URL.Query().Get("timeout_s"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err != nil || v < 0 || v > 600 {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "timeout_s is between 0 and 600"})
			return
		}
	}
	m := s.machine(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	if m.State == api.StateStarting && s.Fail != "" {
		m.State, m.Failure = api.StateFailed, s.Fail
	}
	if m.State == api.StateStarting && want == api.StateRunning {
		m.State = api.StateRunning
	}
	state, failure := m.State, m.Failure
	s.mu.Unlock()
	if state == api.StateFailed && want != api.StateFailed {
		refuse(w, r, Refusal{Status: 408, Code: "timeout", Detail: "the machine failed: " + failure})
		return
	}
	if state != want {
		refuse(w, r, Refusal{Status: 408, Code: "timeout", Detail: fmt.Sprintf("the machine is %s, not %s", state, want)})
		return
	}
	reply(w, 200, s.view(m))
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	var in api.ExecRequest
	if !decode(w, r, &in, false) {
		return
	}
	switch {
	case len(in.Command) > 0 && in.Shell != "":
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "give `command` or `shell`, not both"})
		return
	case len(in.Command) == 0 && in.Shell == "":
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: `say what to run: {"command": ["ls", "-la"]} or {"shell": "ls -la | wc -l"}`})
		return
	}
	s.mu.Lock()
	if m.State == api.StatePaused {
		m.State = api.StateRunning // a paused machine is woken for it
	}
	state, run := m.State, s.Exec
	s.mu.Unlock()
	if state != api.StateRunning {
		refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "the machine cannot run the command in the state it is in"})
		return
	}
	var res api.ExecResult
	if run != nil {
		res = run(m.ID, in)
	} else {
		words := in.Shell
		if words == "" {
			words = strings.Join(in.Command, " ")
		}
		res = api.ExecResult{Stdout: words + "\n", DurationMS: 3}
	}
	out := map[string]any{"exit_code": res.ExitCode, "stderr": res.Stderr, "duration_ms": res.DurationMS, "truncated": res.Truncated}
	if in.BinaryOutput {
		out["stdout_base64"] = base64.StdEncoding.EncodeToString([]byte(res.Stdout))
	} else {
		out["stdout"] = res.Stdout
	}
	reply(w, 200, out)
}

func filePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.URL.Query().Get("path")
	if !strings.HasPrefix(p, "home/") && !strings.HasPrefix(p, "work/") || strings.Contains(p, "..") {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "a path begins with home/ (the machine's home) or work/ (where commands start)"})
		return "", false
	}
	return p, true
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	p, ok := filePath(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	b, found := s.files[m.ID+" "+p]
	s.mu.Unlock()
	if !found {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such file in the machine"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	p, ok := filePath(w, r)
	if !ok {
		return
	}
	if r.ContentLength < 0 {
		refuse(w, r, Refusal{Status: 411, Code: "invalid_request", Detail: "say how long the file is (Content-Length)"})
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the file could not be read"})
		return
	}
	s.mu.Lock()
	s.files[m.ID+" "+p] = b
	s.mu.Unlock()
	w.WriteHeader(204)
}

// secretView is a secret as answered: never its value.
func (s *Server) secretView(name string, k *keptSecret) api.Secret {
	used := []string{}
	for _, m := range s.machines {
		if slices.Contains(m.Secrets, name) {
			used = append(used, m.ID)
		}
	}
	return api.Secret{Object: "secret", Name: name, Machines: used, Outside: k.outside, EveryMachine: k.every, CreatedAt: k.created, UpdatedAt: k.updated}
}

func (s *Server) listSecrets(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := slices.Sorted(maps.Keys(s.secrets))
	out := []api.Secret{}
	for _, n := range names {
		out = append(out, s.secretView(n, s.secrets[n]))
	}
	reply(w, 200, map[string]any{"object": "list", "data": out})
}

func (s *Server) listPresets(w http.ResponseWriter, _ *http.Request) {
	reply(w, 200, map[string]any{"object": "list", "data": presets})
}

// setSecret keeps a secret as the API does: `outside` left out (or null) is the name's preset when it has one, true is
// the preset (refused for a name with none), false an ordinary secret, an object a policy of its own; `every_machine`
// left out is true for a preset's, and only a secret kept outside may be on every machine.
func (s *Server) setSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !api.ValidSecretName(name) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "a secret's name is capitals, digits and _"})
		return
	}
	var in struct {
		Value        *string         `json:"value"`
		Outside      json.RawMessage `json:"outside"`
		EveryMachine *bool           `json:"every_machine"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.Value == nil || *in.Value == "" || len(*in.Value) > api.SecretValueMax {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "give the secret's value in `value`, at most 32 KiB"})
		return
	}
	var outside *api.Outside
	switch given := string(bytes.TrimSpace(in.Outside)); given {
	case "", "null":
		outside = presetOutside(name)
	case "false":
	case "true":
		if outside = presetOutside(name); outside == nil {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: name + " is not a name the API knows as an AI key, so `outside: true` has no hosts to send it to"})
			return
		}
	default:
		var own api.OutsideSet
		if err := json.Unmarshal(in.Outside, &own); err != nil || len(own.Hosts) == 0 || own.Header == "" {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "`outside` is true, false, or an object of hosts, header and format"})
			return
		}
		format := own.Format
		if format == "" {
			format = "{value}"
		}
		outside = &api.Outside{Hosts: own.Hosts, Header: own.Header, Format: format, Headers: []api.HeaderRule{{Name: own.Header, Format: format}}, Query: []string{}}
	}
	every := outside != nil && outside.Preset != nil
	if in.EveryMachine != nil {
		every = *in.EveryMachine
	}
	if every && outside == nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "only a secret kept outside the machines can be on every machine"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UTC()
	k, had := s.secrets[name]
	if !had {
		k = &keptSecret{created: now}
		s.secrets[name] = k
	}
	k.value, k.updated, k.outside, k.every = *in.Value, now, outside, every
	status := 200
	if !had {
		status = 201
	}
	reply(w, status, s.secretView(name, k))
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.secrets[name]; !ok {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such secret"})
		return
	}
	delete(s.secrets, name)
	for _, m := range s.machines {
		m.Secrets = slices.DeleteFunc(m.Secrets, func(x string) bool { return x == name })
	}
	w.WriteHeader(204)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	step := 60
	switch r.URL.Query().Get("step") {
	case "", "60", "1m":
	case "300", "5m":
		step = 300
	default:
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "step is whole minutes from 1 to 60: 60, 300, 5m, 1h"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	points := slices.Clone(s.Metrics[m.ID])
	if points == nil {
		points = []api.MetricsPoint{}
	}
	cpus := float64(2)
	if m.CPUs != nil {
		cpus = float64(*m.CPUs)
	}
	out := api.MachineMetrics{Object: "metrics", Machine: m.ID, StepS: step, CPUs: &cpus, Points: points}
	if len(points) > 0 {
		out.KeptSince = &points[0].At
	}
	reply(w, 200, out)
}

// The ports a machine itself uses, which no address may reach, and how many one machine may have open.
var reservedPorts = []int{5900, 6080, 7070, 9222, 9223, 9224}

const mostPorts = 20

// notOpen is the answer about a port's address while web addresses are not served.
var notOpen = Refusal{Status: 503, Code: "not_open", Detail: "web addresses for machines are not open yet"}

// portURL is a port's address: https://<port>-<the machine's id without m_>.run.wheelmachine.ai.
func portURL(machine string, port int) string {
	return "https://" + strconv.Itoa(port) + "-" + strings.TrimPrefix(machine, "m_") + ".run.wheelmachine.ai"
}

// routePort is the route's port, written as a plain number, or an error answered.
func routePort(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.PathValue("port")
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != v {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "a port is a number from 1 to 65535"})
		return 0, false
	}
	return n, true
}

// landing says whether a link may land on a path: the address's own, one / and then neither another / nor a \ (either
// would name another site), no space or control character, and not the address's own pages under /.wheelmachine/.
func landing(p string) bool {
	if len(p) == 0 || len(p) > 2048 || p[0] != '/' || len(p) > 1 && (p[1] == '/' || p[1] == '\\') {
		return false
	}
	if strings.ContainsFunc(p, func(c rune) bool { return c == '\\' || unicode.IsSpace(c) || unicode.IsControl(c) }) {
		return false
	}
	return !strings.HasPrefix(p, "/.wheelmachine/")
}

func (s *Server) listPorts(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	ports := append([]api.Port{}, s.ports[m.ID]...)
	s.mu.Unlock()
	reply(w, 200, map[string]any{"object": "list", "data": ports})
}

// openPort opens a port (201), or makes one open already public or private as asked (200).
func (s *Server) openPort(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	var in struct {
		Port   *int `json:"port"`
		Public bool `json:"public"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	switch {
	case in.Port == nil:
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: `say which port: {"port": 8080}`})
		return
	case *in.Port < 1 || *in.Port > 65535:
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "a port is a number from 1 to 65535"})
		return
	case slices.Contains(reservedPorts, *in.Port):
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("port %d is used by the machine itself; open another", *in.Port)})
		return
	}
	n := *in.Port
	s.mu.Lock()
	if s.PortsNotOpen {
		s.mu.Unlock()
		refuse(w, r, notOpen)
		return
	}
	delete(s.appPorts, m.ID+" "+strconv.Itoa(n))
	ports := s.ports[m.ID]
	i, status := slices.IndexFunc(ports, func(p api.Port) bool { return p.Port == n }), 200
	switch {
	case i >= 0:
		ports[i].Public = in.Public
	case len(ports) >= mostPorts:
		s.mu.Unlock()
		refuse(w, r, Refusal{Status: 429, Code: "limit_reached", Detail: fmt.Sprintf("a machine has at most %d ports open; close one first", mostPorts)})
		return
	default:
		ports = append(ports, api.Port{Object: "port", Machine: m.ID, Port: n, Public: in.Public, URL: portURL(m.ID, n), CreatedAt: s.Now().UTC()})
		slices.SortFunc(ports, func(a, b api.Port) int { return cmp.Compare(a.Port, b.Port) })
		s.ports[m.ID] = ports
		i, status = slices.IndexFunc(ports, func(p api.Port) bool { return p.Port == n }), 201
	}
	out := ports[i]
	s.mu.Unlock()
	reply(w, status, out)
}

// closePort closes a port's address; one that is not open is closed all the same.
func (s *Server) closePort(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	n, ok := routePort(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	s.ports[m.ID] = slices.DeleteFunc(s.ports[m.ID], func(p api.Port) bool { return p.Port == n })
	delete(s.appPorts, m.ID+" "+strconv.Itoa(n))
	s.mu.Unlock()
	w.WriteHeader(204)
}

// linkPort makes a new link to an open port each time it is called, one that opens once within five minutes; its
// token has the shape of the API's (43 characters a URL carries as they are).
func (s *Server) linkPort(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	n, ok := routePort(w, r)
	if !ok {
		return
	}
	in := struct {
		AccessS int    `json:"access_s"`
		Path    string `json:"path"`
	}{AccessS: 1800, Path: "/"}
	if !decode(w, r, &in, true) {
		return
	}
	switch {
	case in.AccessS < 60 || in.AccessS > 3600:
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "access_s is between 60 and 3600"})
		return
	case !landing(in.Path):
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "path is a path on the address, beginning with a single /"})
		return
	}
	s.mu.Lock()
	served, open := !s.PortsNotOpen, slices.ContainsFunc(s.ports[m.ID], func(p api.Port) bool { return p.Port == n })
	s.ids++
	token := fmt.Sprintf("%043d", s.ids)
	s.mu.Unlock()
	switch {
	case !served:
		refuse(w, r, notOpen)
	case !open:
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: fmt.Sprintf("port %d of this machine is not open", n)})
	default:
		reply(w, 201, api.PortLink{Object: "port_link", Machine: m.ID, Port: n, URL: portURL(m.ID, n) + "/.wheelmachine/link/" + token,
			ExpiresAt: s.Now().UTC().Add(5 * time.Minute), AccessS: in.AccessS})
	}
}

// AppToken is what turning openclaw on answers in `token`: made up, nobody's.
const AppToken = "made-up-app-token"

// appsOf is a machine's apps as the machine says them, as each is when the machine is new.
func appsOf(machine string) []api.App {
	web := func(name, title string, port int, path string) api.App {
		off, url := false, portURL(machine, port)
		return api.App{Object: "app", Machine: machine, Name: name, Title: title, Kind: "web", On: &off, Running: &off, Port: &port, Path: &path, URL: &url}
	}
	off, on, ollama, codex, gemini := false, true, false, "codex", "gemini"
	return []api.App{
		web("desktop", "Desktop", 6901, "/vnc.html?autoconnect=1&resize=remote&reconnect=1"),
		web("editor", "Code editor", 13337, "/"),
		web("openclaw", "OpenClaw", 18789, "/"),
		{Object: "app", Machine: machine, Name: "ollama", Title: "Ollama", Kind: "service", On: &ollama, Running: &off},
		{Object: "app", Machine: machine, Name: "codex", Title: "Codex", Kind: "agent", On: &on, Running: &off, Command: &codex},
		// as a terminal agent is said
		{Object: "app", Machine: machine, Name: "gemini", Title: "Gemini CLI", Kind: "terminal", Command: &gemini},
	}
}

// Apps is a machine's apps as the fake holds them now.
func (s *Server) Apps(machine string) []api.App {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.apps[machine])
}

// SetApp puts an app in a machine as the machine would say it now, in place of the one of its name.
func (s *Server) SetApp(machine string, a api.App) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apps := s.heldApps(machine)
	if i := slices.IndexFunc(apps, func(x api.App) bool { return x.Name == a.Name }); i >= 0 {
		apps[i] = a
	}
}

// heldApps is a machine's apps, made the first time they are asked for; s.mu is held.
func (s *Server) heldApps(machine string) []api.App {
	if _, ok := s.apps[machine]; !ok {
		s.apps[machine] = appsOf(machine)
	}
	return s.apps[machine]
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	m := s.machine(w, r)
	if m == nil {
		return
	}
	s.mu.Lock()
	no := s.NoApps
	apps := slices.Clone(s.heldApps(m.ID))
	s.mu.Unlock()
	if no {
		refuse(w, r, noApps)
		return
	}
	reply(w, 200, map[string]any{"object": "list", "data": apps})
}

// noApps is the answer about a machine with no apps.
var noApps = Refusal{Status: 409, Code: "no_apps", Detail: "this machine has no apps; make a new machine to have them"}

// turnApp turns an app on (a web app's port opened private when it is not, a new link into it, openclaw's token) or
// off (its port closed when it was opened for the app and is still private), or makes a new link into a web app that
// is on (its port opened private first when it is not, as turning it on does), or shows one on the machine's screen.
func (s *Server) turnApp(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := s.machine(w, r)
		if m == nil {
			return
		}
		in := struct {
			AccessS int `json:"access_s"`
		}{AccessS: 1800}
		if verb == "off" || verb == "show" {
			var none struct{}
			if !decode(w, r, &none, true) {
				return
			}
		} else if !decode(w, r, &in, true) {
			return
		}
		if in.AccessS < 60 || in.AccessS > 3600 {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "access_s is between 60 and 3600"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.NoApps {
			refuse(w, r, noApps)
			return
		}
		apps := s.heldApps(m.ID)
		i := slices.IndexFunc(apps, func(a api.App) bool { return a.Name == r.PathValue("app") })
		if i < 0 {
			refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such app in this machine"})
			return
		}
		a := &apps[i]
		if verb == "show" {
			// shown on the machine's screen when it is on: nothing opened, linked or given
			if a.On != nil && !*a.On {
				refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: a.Name + " could not be shown on the machine's screen: " + a.Title + " is off: turn it on first"})
				return
			}
			reply(w, 200, *a)
			return
		}
		switch {
		case verb == "link" && a.Kind != "web":
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: a.Title + " has no web address"})
			return
		case verb == "link" && !*a.On:
			refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: a.Title + " is off: turn it on first"})
			return
		case verb != "link" && a.Kind != "terminal":
			on := verb == "on"
			a.On, a.Running = &on, &on
			if a.KeysChanged != nil {
				// its session ended, or begun again: never one begun with the keys from before
				no := false
				a.KeysChanged = &no
			}
		}
		out := *a
		if a.Kind == "web" {
			at := m.ID + " " + strconv.Itoa(*a.Port)
			open := slices.ContainsFunc(s.ports[m.ID], func(p api.Port) bool { return p.Port == *a.Port })
			switch {
			case verb != "off" && !open:
				ports := append(s.ports[m.ID], api.Port{Object: "port", Machine: m.ID, Port: *a.Port, URL: *a.URL, CreatedAt: s.Now().UTC()})
				slices.SortFunc(ports, func(x, y api.Port) int { return cmp.Compare(x.Port, y.Port) })
				s.ports[m.ID], s.appPorts[at], open = ports, a.Name, true
			case verb == "off" && s.appPorts[at] == a.Name:
				if j := slices.IndexFunc(s.ports[m.ID], func(p api.Port) bool { return p.Port == *a.Port }); j >= 0 && !s.ports[m.ID][j].Public {
					s.ports[m.ID] = slices.Delete(s.ports[m.ID], j, j+1)
					delete(s.appPorts, at)
				}
			}
			if verb != "off" {
				s.ids++
				expires := s.Now().UTC().Add(5 * time.Minute)
				out.Link, out.ExpiresAt, out.AccessS = *a.URL+"/.wheelmachine/link/"+fmt.Sprintf("%043d", s.ids), &expires, in.AccessS
			}
		}
		if verb == "on" && a.Name == "openclaw" {
			out.Token = AppToken
		}
		status := 200
		if verb == "link" {
			status = 201
		}
		reply(w, status, out)
	}
}

// sshCertificate signs - in the test's sense, makes up - a certificate for a machine, so wm ssh-config can be driven
// without the real certificate authority. It records the public key it was given.
func (s *Server) sshCertificate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicKey string `json:"public_key"`
		Machine   string `json:"machine"`
		TTLs      *int   `json:"ttl_s"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if in.Machine == "" || s.find(in.Machine) == nil {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such machine"})
		return
	}
	if !strings.HasPrefix(in.PublicKey, "ssh-") && !strings.HasPrefix(in.PublicKey, "ecdsa-") {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "public_key is one OpenSSH line"})
		return
	}
	s.mu.Lock()
	s.sshSignedFor = in.PublicKey
	s.mu.Unlock()
	reply(w, 201, map[string]any{
		"object":          "ssh_certificate",
		"certificate":     "ssh-ed25519-cert-v01@openssh.com AAAAtest " + in.Machine,
		"principal":       in.Machine,
		"ssh_user":        in.Machine,
		"expires_at":      "2026-10-23T13:00:00Z",
		"ca_public_key":   "ssh-ed25519 AAAAca",
		"host_public_key": "ssh-ed25519 AAAAhost",
		"ssh_host":        "ssh.wheelmachine.ai:2222",
	})
}

// SSHSignedFor is the public key the last certificate was signed for.
func (s *Server) SSHSignedFor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sshSignedFor
}

func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Scopes  []string `json:"scopes"`
		Account *string  `json:"account"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	if in.Account != nil && !api.IsAccountID(*in.Account) {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "account is an organisation's id, acc_ and 26 letters and digits"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LoginsShut {
		refuse(w, r, Refusal{Status: 403, Code: "not_open", Detail: "signing in through the browser is not open yet"})
		return
	}
	device := "wmd_" + strings.Repeat("d", 40) + strconv.Itoa(len(s.logins))
	s.logins[device] = &login{name: in.Name, scopes: in.Scopes}
	reply(w, 200, map[string]any{"object": "device_login_start", "device_code": device, "user_code": "BCDF-GHJK",
		"verification_uri": s.URL + "/device", "expires_in": 600, "expires_at": s.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "interval": 1})
}

func (s *Server) loginToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.logins[in.DeviceCode]
	if !ok {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such sign-in: start again"})
		return
	}
	if l.done {
		refuse(w, r, Refusal{Status: 410, Code: "login_expired", Detail: "this sign-in was used already: start again"})
		return
	}
	l.asks++
	said := "approved"
	if s.Decide != nil {
		said = s.Decide(l.asks)
	}
	switch said {
	case "pending":
		reply(w, 202, map[string]any{"object": "device_login", "state": "pending", "interval": 1})
	case "unavailable":
		refuse(w, r, Refusal{Status: 503, Code: "unavailable", Detail: "the sign-in could not be checked just now"})
	case "denied":
		refuse(w, r, Refusal{Status: 403, Code: "login_denied", Detail: "the sign-in was refused at the dashboard"})
	case "expired":
		refuse(w, r, Refusal{Status: 410, Code: "login_expired", Detail: "this sign-in's code expired: start again"})
	default:
		l.done = true
		scopes := l.scopes
		if len(scopes) == 0 {
			scopes = []string{api.ScopeAll}
		}
		k := api.Key{ID: s.newID("key_"), Object: "key", Name: l.name, Scopes: scopes, Projects: []string{}, State: "active",
			Shown: s.Key[:12] + "…" + s.Key[len(s.Key)-4:], CreatedAt: s.Now().UTC(), MadeBy: s.madeBy()}
		s.keys = append(s.keys, k)
		reply(w, 201, api.KeyCreated{Key: k, Secret: s.Key})
	}
}

// The account's organisations, as the API lists them: a key made in an organisation lists none, and is refused.
func (s *Server) listOrgs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	inOrg, orgs := s.Org != nil, slices.Clone(s.Orgs)
	s.mu.Unlock()
	if inOrg {
		refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: "an organisation's key does not list the other organisations of the person who made it"})
		return
	}
	if orgs == nil {
		orgs = []api.Org{}
	}
	reply(w, 200, map[string]any{"object": "list", "data": orgs})
}

// What the audit is asked for, as the API reads it.
type auditAsk struct {
	person, action string
	since, until   *time.Time
}

// Whether the key may read the audit, an error answered when not: a key of every scope, and in an organisation an
// owner's or an admin's alone.
func (s *Server) auditMay(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	k, org := s.keys[0], s.Org
	s.mu.Unlock()
	if !slices.Contains(k.Scopes, api.ScopeAll) {
		refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: "this key does not hold the cloud:* scope"})
		return false
	}
	if org != nil && org.Role != api.RoleOwner && org.Role != api.RoleAdmin {
		refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: "your role in this organisation does not allow this"})
		return false
	}
	return true
}

// What the audit is asked for, an error answered when it cannot be read.
func auditAsked(w http.ResponseWriter, r *http.Request) (auditAsk, bool) {
	q := r.URL.Query()
	ask := auditAsk{person: q.Get("person"), action: q.Get("action")}
	for name, into := range map[string]**time.Time{"since": &ask.since, "until": &ask.until} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: name + " is a time like 2026-10-01T00:00:00Z"})
				return auditAsk{}, false
			}
			*into = &t
		}
	}
	return ask, true
}

// The events the ask picks, the newest first.
func (s *Server) auditOf(ask auditAsk) []api.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []api.AuditEvent
	for _, e := range s.Audit {
		if ask.person != "" && (e.Person == nil || e.Person.ID != ask.person) || !strings.HasPrefix(e.Action, ask.action) ||
			ask.since != nil && e.At.Before(*ask.since) || ask.until != nil && !e.At.Before(*ask.until) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	if !s.auditMay(w, r) {
		return
	}
	ask, ok := auditAsked(w, r)
	if !ok {
		return
	}
	limit, ok := page(w, r)
	if !ok {
		return
	}
	rows := s.auditOf(ask)
	if after := r.URL.Query().Get("after"); after != "" {
		i := slices.IndexFunc(rows, func(e api.AuditEvent) bool { return e.ID == after })
		if i < 0 {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "after is the `next` of the page before"})
			return
		}
		rows = rows[i+1:]
	}
	out := map[string]any{"object": "list", "data": []api.AuditEvent{}, "next": nil}
	if len(rows) > limit {
		out["next"] = rows[limit-1].ID
		rows = rows[:limit]
	}
	if rows != nil {
		out["data"] = rows
	}
	reply(w, 200, out)
}

// The audit as a file, as the API exports it: a year at most, the last one when since is left out; CSV with every
// field quoted, or JSON, the newest first.
func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request) {
	if !s.auditMay(w, r) {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "say format=csv or format=json"})
		return
	}
	ask, ok := auditAsked(w, r)
	if !ok {
		return
	}
	const span = 366 * 24 * time.Hour
	until := s.Now().UTC()
	if ask.until != nil {
		until = ask.until.UTC()
	}
	since := until.Add(-span)
	if ask.since != nil {
		since = ask.since.UTC()
	}
	if !since.Before(until) || until.Sub(since) > span {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "an export covers a year at most, from since to until"})
		return
	}
	ask.since, ask.until = &since, &until
	rows := s.auditOf(ask)
	if rows == nil {
		rows = []api.AuditEvent{}
	}
	s.mu.Lock()
	account := s.Account
	if s.Org != nil {
		account = s.Org.ID
	}
	s.mu.Unlock()
	w.Header().Set("Content-Disposition", `attachment; filename="audit-`+account+"-"+since.Format("20060102")+"-"+until.Format("20060102")+"."+format+`"`)
	if format == "json" {
		reply(w, 200, rows)
		return
	}
	var b strings.Builder
	writeCSV(&b, "id", "at", "action", "target", "person", "person_name", "by", "address", "request_id", "detail")
	for _, e := range rows {
		person, name := "", ""
		if e.Person != nil {
			person, name = e.Person.ID, e.Person.Name
		}
		keys := slices.Sorted(maps.Keys(e.Detail))
		detail := make([]string, 0, len(keys))
		for _, k := range keys {
			detail = append(detail, k+"="+e.Detail[k])
		}
		writeCSV(&b, e.ID, e.At.UTC().Format(time.RFC3339Nano), e.Action, e.Target, person, name, e.By, e.Address, e.RequestID,
			strings.Join(detail, "; "))
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, b.String())
}

// A line of CSV as the API writes it: every field quoted, and one a spreadsheet would run as a formula - beginning
// with =, +, -, @, a tab or a carriage return - begun with ', so that it is shown as it is, never run.
func writeCSV(b *strings.Builder, fields ...string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		if f != "" && strings.ContainsRune("=+-@\t\r", rune(f[0])) {
			f = "'" + f
		}
		b.WriteString(`"` + strings.ReplaceAll(f, `"`, `""`) + `"`)
	}
	b.WriteString("\r\n")
}

// GET /v1/code: the account's code hosts and its teams' repositories, as set.
func (s *Server) code(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.Code
	c.Object = "code"
	if c.Connections == nil {
		c.Connections = []api.CodeConnection{}
	}
	if c.Grants == nil {
		c.Grants = []api.CodeGrant{}
	}
	reply(w, 200, c)
}

// GET /v1/code/connections/{id}/repos: what the connection reaches.
func (s *Server) codeRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := []api.CodeRepo{}
	for _, rp := range s.Repos {
		if rp.Connection == r.PathValue("id") {
			data = append(data, rp)
		}
	}
	reply(w, 200, map[string]any{"object": "list", "data": data, "next": nil})
}

// PUT /v1/projects/{id}/code: a repository given to a team.
func (s *Server) grantCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connection, Repo, Access string
	}
	if !decode(w, r, &body, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := api.CodeGrant{Object: "code_grant", ID: "cg_1", Project: r.PathValue("id"), Connection: body.Connection, Host: "github.com", Repo: body.Repo,
		Access: body.Access}
	s.Code.Grants = append(s.Code.Grants, g)
	reply(w, 200, g)
}

// GET /v1/folders: the folders set, on.
func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := append([]api.SharedFolder{}, s.Folders...)
	reply(w, 200, map[string]any{"object": "list", "on": true, "data": data, "file_most_bytes": 5000000000})
}

// The folder a route names: one of those set, else refused as the API refuses one it does not reach.
func (s *Server) folderOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	for _, f := range s.Folders {
		if f.ID == id {
			return id, true
		}
	}
	refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such folder"})
	return "", false
}

// GET /v1/folders/{id}/list: what is directly in a folder of a folder, folders first.
func (s *Server) listFolder(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.folderOf(w, r)
	if !ok {
		return
	}
	under := strings.Trim(r.URL.Query().Get("path"), "/")
	prefix := id + "/"
	if under != "" {
		prefix += under + "/"
	}
	dirs, files := map[string]bool{}, []api.FolderEntry{}
	for k, b := range s.FolderFiles {
		rest, found := strings.CutPrefix(k, prefix)
		if !found {
			continue
		}
		if name, _, deeper := strings.Cut(rest, "/"); deeper {
			dirs[name] = true
		} else {
			files = append(files, api.FolderEntry{Name: name, Type: "file", Size: int64(len(b)), ChangedAt: "2026-11-04T00:00:00Z"})
		}
	}
	entries := []api.FolderEntry{}
	for name := range dirs {
		entries = append(entries, api.FolderEntry{Name: name, Type: "folder", ChangedAt: "2026-11-04T00:00:00Z"})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	entries = append(entries, files...)
	// a page at a time, FolderPage entries each (all at once for none): `after` the place in the listing to start at
	start, _ := strconv.Atoi(r.URL.Query().Get("after"))
	start = min(max(start, 0), len(entries))
	end, next := len(entries), (*string)(nil)
	if s.FolderPage > 0 && start+s.FolderPage < len(entries) {
		end = start + s.FolderPage
		at := strconv.Itoa(end)
		next = &at
	}
	reply(w, 200, api.FolderListing{Object: "shared_listing", Folder: id, Path: under, Entries: entries[start:end], Next: next})
}

func (s *Server) getFolderFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.folderOf(w, r)
	if !ok {
		return
	}
	b, found := s.FolderFiles[id+"/"+strings.Trim(r.URL.Query().Get("path"), "/")]
	if !found {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no file is there"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(b)
}

func (s *Server) putFolderFile(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the file could not be read"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.folderOf(w, r)
	if !ok {
		return
	}
	if s.FolderFiles == nil {
		s.FolderFiles = map[string][]byte{}
	}
	path := strings.Trim(r.URL.Query().Get("path"), "/")
	s.FolderFiles[id+"/"+path] = b
	reply(w, 200, api.FolderEntry{Name: path[strings.LastIndexByte(path, '/')+1:], Type: "file", Size: int64(len(b)), ChangedAt: "2026-11-04T00:00:00Z"})
}

func (s *Server) deleteFolderFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.folderOf(w, r)
	if !ok {
		return
	}
	path := strings.Trim(r.URL.Query().Get("path"), "/")
	if _, found := s.FolderFiles[id+"/"+path]; !found {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "nothing is there"})
		return
	}
	delete(s.FolderFiles, id+"/"+path)
	reply(w, 200, api.FolderTrash{Object: "shared_trash", ID: "ftr_1", Folder: id, Path: path, Type: "file", GoesAt: "2026-12-04T00:00:00Z"})
}

func (s *Server) moveFolderItem(w http.ResponseWriter, r *http.Request) {
	var body struct{ From, To string }
	if !decode(w, r, &body, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.folderOf(w, r)
	if !ok {
		return
	}
	b, found := s.FolderFiles[id+"/"+body.From]
	if !found {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "nothing is there"})
		return
	}
	delete(s.FolderFiles, id+"/"+body.From)
	s.FolderFiles[id+"/"+body.To] = b
	reply(w, 200, map[string]any{"object": "shared_moved", "folder": id, "path": body.To})
}

// Person is one of the organisation's people, as a batch of machines names them: their id, name, e-mail address and
// handle, and the projects of the teams they are in.
type Person struct {
	ID, Name, Email, Handle string
	Teams                   []string
}

func (s *Server) listTypes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if s.projectAt(id) < 0 {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such project"})
		return
	}
	kinds := s.Types[id]
	if kinds == nil {
		kinds = []api.MachineType{}
	}
	reply(w, 200, map[string]any{"object": "list", "data": kinds, "apps": []any{}})
}

// typeIn is the team's type of this id or name, whatever its capitals. The lock held.
func (s *Server) typeIn(project, named string) (api.MachineType, *Refusal) {
	for _, k := range s.Types[project] {
		if k.ID == named || strings.EqualFold(k.Name, named) {
			return k, nil
		}
	}
	return api.MachineType{}, &Refusal{Status: 400, Code: "invalid_request", Detail: fmt.Sprintf("no machine type %q in this team", named)}
}

// ofType is a new machine made as the type says, held by `held`. The lock held.
func (s *Server) ofType(k api.MachineType, name string, labels map[string]string, held *api.HeldBy) *api.Machine {
	m := s.newMachine(name, k.Project, maps.Clone(labels))
	if k.CPUs != nil {
		resize(m, *k.CPUs)
	}
	m.Network = normPolicy(k.Network)
	m.SleepWhenIdle, m.IdleAfterS, m.PutAwayAfterS = k.SleepWhenIdle, k.IdleAfterS, k.PutAwayAfterS
	id := k.ID
	m.Type, m.HeldBy = &id, held
	return m
}

func (s *Server) createMachines(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Project  string            `json:"project"`
		Type     string            `json:"type"`
		People   []string          `json:"people"`
		Everyone bool              `json:"everyone"`
		Each     int               `json:"each"`
		Names    string            `json:"names"`
		Region   string            `json:"region"`
		Labels   map[string]string `json:"labels"`
		WaitS    *float64          `json:"wait_s"`
	}
	if !decode(w, r, &in, false) || !waitOK(w, r, in.WaitS) {
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		s.mu.Lock()
		defer s.mu.Unlock()
		project := s.projects[0].ID
		if in.Project != "" {
			if s.projectAt(in.Project) < 0 {
				return 0, nil, &Refusal{Status: 404, Code: "not_found", Detail: "no such project"}
			}
			project = in.Project
		}
		k, rf := s.typeIn(project, in.Type)
		if rf != nil {
			return 0, nil, rf
		}
		each := max(in.Each, 1)
		var whom []Person
		switch {
		case in.Everyone:
			for _, p := range s.People {
				if slices.Contains(p.Teams, project) {
					whom = append(whom, p)
				}
			}
		default:
			for _, named := range in.People {
				i := slices.IndexFunc(s.People, func(p Person) bool { return p.ID == named || p.Email == named || "@"+p.Handle == named })
				if i < 0 {
					return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "nobody in this organisation is " + named}
				}
				whom = append(whom, s.People[i])
			}
		}
		pattern := or(in.Names, "<type>-<handle>")
		if each > 1 && !strings.Contains(pattern, "<n>") {
			pattern += "-<n>"
		}
		var data []map[string]any
		made := 0
		for _, p := range whom {
			for n := 1; n <= each; n++ {
				name := strings.NewReplacer("<type>", k.Name, "<handle>", p.Handle, "<n>", strconv.Itoa(n)).Replace(pattern)
				item := map[string]any{"person": map[string]string{"id": p.ID, "name": p.Name}, "name": name}
				if !slices.Contains(p.Teams, project) {
					item["status"], item["machine"] = 403, nil
					item["error"] = map[string]any{"status": 403, "code": "forbidden", "detail": p.Name + " is not in this team: put them in it first"}
				} else {
					m := s.ofType(k, name, in.Labels, &api.HeldBy{ID: p.ID, Name: p.Name})
					status := 201
					if s.Slow {
						m.State, status = api.StateStarting, 202
					}
					item["status"], item["machine"], item["error"] = status, s.copyOf(m), nil
					made++
				}
				data = append(data, item)
			}
		}
		return 200, map[string]any{"object": "machine_batch", "project": project, "type": k.ID, "data": data, "made": made, "refused": len(data) - made}, nil
	})
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
