package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"wheelmachine/frontend/cli/api"
)

// The protocol revisions spoken.
//
// The latest revision at modelcontextprotocol.io, 2026-07-28, has no handshake: each request carries its version
// and the client's capabilities in params._meta, results carry resultType, and server/discover says what a server
// speaks. Clients of the revisions before it open with initialize and are served for the rest of the process as
// the version agreed; initialize gets the version it asks for when it is one of those, else the newest of them.
const latestVersion = "2026-07-28"

var (
	perRequestVersions = []string{latestVersion}
	handshakeVersions  = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}
)

func supportedVersions() []string {
	return append(slices.Clone(perRequestVersions), handshakeVersions...)
}

// JSON-RPC's codes, and the one MCP defines for a version it does not speak.
const (
	codeParse              = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeUnsupportedVersion = -32022
)

const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"
)

// The longest message read; a tool's arguments (a file written) can be large, but not without end.
const maxMessage = 64 << 20

const instructions = "Wheelmachine machines on this account: make them, run commands in them, read and write their files, " +
	"see and work their desktops, pause, start, stop, fork, back up and delete them. A machine is named by its id (m_...) or its name; file paths " +
	"begin home/ (the machine's home) or work/ (where commands start). Running time is charged by the second while a " +
	"machine runs: pause or stop machines that are not in use. A machine's commands get the account's secrets it was given " +
	"as environment variables, so a command there can read them; these tools name secrets, and never take a value. Errors " +
	"come back as results with " +
	"isError and the API's code."

// What a client is told: Deploy's words with its tools, once they are offered.
func serverInstructions() string {
	if deployListed {
		return instructions + " " + deployInstructions
	}
	return instructions
}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  *string         `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

var nullID = json.RawMessage("null")

// era is how one request is served: the version, and whether it is of the revisions with no handshake.
type era struct {
	version string
	modern  bool
}

// A request being run, to cancel.
type running struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

type server struct {
	tools   []tool
	byName  map[string]*tool
	connect func() (*api.Client, error)
	log     *log.Logger
	info    implementation
	grace   time.Duration // how long requests still running may finish after standard input ends

	outMu sync.Mutex
	out   *json.Encoder

	mu      sync.Mutex
	session string // the version agreed by initialize; "" before one
	running map[string]*running
	client  *api.Client

	wg    sync.WaitGroup
	calls chan struct{} // tool calls at once
}

func newServer(logger *log.Logger, connect func() (*api.Client, error)) *server {
	s := &server{tools: offered(), connect: connect, log: logger, grace: 30 * time.Second, running: map[string]*running{},
		info: implementation{Name: "wheelmachine", Title: "Wheelmachine", Version: api.Version}, calls: make(chan struct{}, 16)}
	s.byName = make(map[string]*tool, len(s.tools))
	for i := range s.tools {
		s.byName[s.tools[i].Name] = &s.tools[i]
	}
	return s
}

// serve reads a message a line from in and writes a message a line to out, until in ends. Requests still running
// then may finish, for a while; a cancelled context ends them at once.
func (s *server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = json.NewEncoder(out)
	s.out.SetEscapeHTML(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := bufio.NewReaderSize(in, 64<<10)
	var failed error
	for {
		line, err := readMessage(r, maxMessage)
		if errors.Is(err, errTooLarge) {
			s.reply(nullID, nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid Request: a message is at most 64 MiB"})
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 {
			s.handle(ctx, line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				failed = err
			}
			break
		}
	}
	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		<-finished
	case <-time.After(s.grace):
		cancel()
		<-finished
	}
	return failed
}

var errTooLarge = errors.New("message too large")

// readMessage reads one line; a line longer than limit is passed over whole and errTooLarge is returned for it.
func readMessage(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			return nil, errTooLarge
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(line, "\r\n"), err
	}
}

func (s *server) reply(id json.RawMessage, result any, rerr *rpcError) {
	res := response{JSONRPC: "2.0", ID: id}
	if rerr != nil {
		res.Error = rerr
	} else {
		res.Result = result
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if err := s.out.Encode(res); err != nil {
		s.log.Printf("an answer could not be written: %v", err)
	}
}

func validID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

// idKey is a request's id as a key: its JSON, without spaces.
func idKey(id json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, id) != nil {
		return string(id)
	}
	return b.String()
}

func (s *server) handle(ctx context.Context, line []byte) {
	line = bytes.TrimSpace(line)
	if line[0] == '[' {
		s.reply(nullID, nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid Request: batches are not taken; send one message a line"})
		return
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		s.reply(nullID, nil, &rpcError{Code: codeParse, Message: "Parse error: the line is not a JSON object"})
		return
	}
	id := nullID
	if m.ID != nil && validID(m.ID) {
		id = m.ID
	}
	switch {
	case m.JSONRPC != "2.0":
		s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: `Invalid Request: jsonrpc must be "2.0"`})
		return
	case m.Method == nil && (m.Result != nil || m.Error != nil):
		return // an answer: this server asks nothing, so there is nothing it answers
	case m.Method == nil:
		s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid Request: no method"})
		return
	case m.ID == nil:
		s.notify(*m.Method, m.Params)
		return
	case !validID(m.ID):
		s.reply(nullID, nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid Request: an id is a string or a number"})
		return
	}
	method := *m.Method
	if method == "initialize" {
		// answered before the next line is read, so what follows is served as agreed
		res, rerr := s.initialize(m.Params)
		s.reply(id, res, rerr)
		return
	}
	key := idKey(id)
	ctx, cancel := context.WithCancel(ctx)
	run := &running{cancel: cancel}
	s.mu.Lock()
	if _, busy := s.running[key]; busy {
		s.mu.Unlock()
		cancel()
		s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid Request: a request with this id is still running"})
		return
	}
	s.running[key] = run
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		res, rerr := s.request(ctx, method, m.Params)
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
		if run.cancelled.Load() {
			return // a cancelled request is not answered
		}
		s.reply(id, res, rerr)
	}()
}

func (s *server) notify(method string, params json.RawMessage) {
	switch method {
	case "notifications/initialized":
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(params, &p) != nil || p.RequestID == nil {
			return
		}
		s.mu.Lock()
		run := s.running[idKey(p.RequestID)]
		s.mu.Unlock()
		if run != nil {
			run.cancelled.Store(true)
			run.cancel()
		}
	}
	// any other notification asks nothing of this server
}

func (s *server) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.ProtocolVersion == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: initialize needs a protocolVersion",
			Data: map[string]any{"supported": handshakeVersions}}
	}
	version := handshakeVersions[0]
	if slices.Contains(handshakeVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	s.mu.Lock()
	s.session = version
	s.mu.Unlock()
	who := "a client"
	if p.ClientInfo != nil {
		who = printable(p.ClientInfo.Name) + " " + printable(p.ClientInfo.Version) // as the client says, for the log only
	}
	s.log.Printf("initialized by %s: asked for %s, speaking %s", who, printable(p.ProtocolVersion), version)
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      s.info,
		"instructions":    serverInstructions(),
	}, nil
}

// printable is a word from the client, safe for a log line.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

var methods = []string{"ping", "server/discover", "tools/list", "tools/call"}

// request serves a request after the handshake, or one carrying its own version.
func (s *server) request(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	e, rerr := s.eraOf(method, params)
	if rerr != nil {
		return nil, rerr
	}
	var out map[string]any
	switch method {
	case "ping":
		out = map[string]any{}
	case "server/discover":
		out = map[string]any{"supportedVersions": supportedVersions(), "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions": serverInstructions(), "ttlMs": 3600000, "cacheScope": "public"}
	case "tools/list":
		out = map[string]any{"tools": s.tools}
		if e.modern {
			out["ttlMs"], out["cacheScope"] = 3600000, "public"
		}
	case "tools/call":
		if out, rerr = s.call(ctx, params); rerr != nil {
			return nil, rerr
		}
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "Method not found: " + printable(method)}
	}
	if e.modern {
		out["resultType"] = "complete"
		out["_meta"] = map[string]any{metaServerInfo: s.info}
	}
	return out, nil
}

// eraOf is how a request is served: by the version in its _meta, else as the handshake agreed.
func (s *server) eraOf(method string, params json.RawMessage) (era, *rpcError) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) != nil {
		return era{}, &rpcError{Code: codeInvalidParams, Message: "Invalid params: params is an object, and its _meta an object"}
	}
	if raw, ok := p.Meta[metaVersion]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return era{}, &rpcError{Code: codeInvalidParams, Message: "Invalid params: " + metaVersion + " is a string"}
		}
		switch {
		case slices.Contains(perRequestVersions, v):
			var caps map[string]json.RawMessage
			if raw, ok := p.Meta[metaCapabilities]; !ok || json.Unmarshal(raw, &caps) != nil || caps == nil {
				return era{}, &rpcError{Code: codeInvalidParams,
					Message: "Invalid params: params._meta needs " + metaCapabilities + " (an empty object when the client has none)"}
			}
			return era{version: v, modern: true}, nil
		case slices.Contains(handshakeVersions, v):
			return era{version: v}, nil
		}
		return era{}, &rpcError{Code: codeUnsupportedVersion, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": supportedVersions(), "requested": v}}
	}
	s.mu.Lock()
	agreed := s.session
	s.mu.Unlock()
	switch {
	case agreed != "":
		return era{version: agreed}, nil
	case method == "ping":
		return era{version: handshakeVersions[0]}, nil
	case !slices.Contains(methods, method):
		return era{}, &rpcError{Code: codeMethodNotFound, Message: "Method not found: " + printable(method)}
	}
	return era{}, &rpcError{Code: codeInvalidParams, Message: "Invalid params: no protocol version: send initialize first, or give " +
		metaVersion + " and " + metaCapabilities + " in params._meta",
		Data: map[string]any{"supported": supportedVersions()}}
}

// call runs a tool. What goes wrong in running it is a result with isError, for the model to read and act on; only
// a call that names no tool, or no known one, is refused as a request.
func (s *server) call(ctx context.Context, params json.RawMessage) (map[string]any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: tools/call needs the tool's name"}
	}
	t, ok := s.byName[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Unknown tool: " + printable(p.Name)}
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	select {
	case s.calls <- struct{}{}:
		defer func() { <-s.calls }()
	case <-ctx.Done():
		return errorResult(ctx.Err()), nil
	}
	began := time.Now()
	value, err := t.run(ctx, s.apiClient, args)
	took := time.Since(began).Round(time.Millisecond)
	if err != nil {
		f := failureOf(err)
		if f.Status == 401 {
			// a key that does not work is let go: the next call reads the key again, as `wm login` may have kept a new one
			s.mu.Lock()
			s.client = nil
			s.mu.Unlock()
		}
		s.log.Printf("%s: %s after %s", t.Name, f.Code, took)
		return errorResult(err), nil
	}
	s.log.Printf("%s: done in %s", t.Name, took)
	if p, ok := value.(*pictured); ok {
		return map[string]any{"content": []any{p.image, text(compact(p.said))}, "isError": false}, nil
	}
	return map[string]any{"content": []any{text(compact(value))}, "isError": false}, nil
}

// errorResult is a tool's failure as a result the model reads: isError, and the API's code and detail.
func errorResult(err error) map[string]any {
	return map[string]any{"content": []any{text(compact(map[string]any{"error": failureOf(err)}))}, "isError": true}
}

// apiClient is made when first needed, and again after a failure, so a key kept by `wm login` after this
// server started is found.
func (s *server) apiClient() (*api.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	c, err := s.connect()
	if err != nil {
		return nil, err
	}
	s.client = c
	return c, nil
}

// content is a block of a tool's result: words, or a picture and its type.
type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func text(s string) content { return content{Type: "text", Text: s} }

// compact is a value as JSON with no spaces, characters as they are.
func compact(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return `{"error":{"code":"internal","detail":"the answer could not be written as JSON"}}`
	}
	return strings.TrimRight(b.String(), "\n")
}
