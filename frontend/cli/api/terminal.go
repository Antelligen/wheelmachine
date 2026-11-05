package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// TerminalOptions is what a terminal opens with: its window, as whom, the kind of terminal (TERM), where it starts,
// and a command to run instead of a login shell. Every field may be left at its zero value.
type TerminalOptions struct {
	Cols, Rows int
	User       string // "", "user" or "root"
	Term       string
	Cwd        string
	Command    []string // nil: a login shell
}

func (o TerminalOptions) query() (url.Values, error) {
	q := url.Values{}
	if o.Cols > 0 {
		q.Set("cols", strconv.Itoa(o.Cols))
	}
	if o.Rows > 0 {
		q.Set("rows", strconv.Itoa(o.Rows))
	}
	switch o.User {
	case "", "user", "root":
		if o.User != "" {
			q.Set("user", o.User)
		}
	default:
		return nil, &Error{Code: CodeInvalidRequest, Detail: "user is `user` or `root`"}
	}
	if o.Term != "" {
		q.Set("term", o.Term)
	}
	if o.Cwd != "" {
		q.Set("cwd", o.Cwd)
	}
	for _, word := range o.Command {
		q.Add("cmd", word)
	}
	return q, nil
}

// TerminalEnd says how a terminal's program ended: its exit code where it ended by itself, else why it was closed.
type TerminalEnd struct {
	ExitCode *int   `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Terminal is an open terminal inside a machine: output comes from Receive, keystrokes go through Send, the window
// through Resize, and Close ends it. It is driven by one reader (Receive) and any number of writers.
type Terminal struct {
	ws  *wsConn
	end *TerminalEnd
}

// OpenTerminal opens an interactive terminal in a machine (the API's GET /v1/machines/{id}/terminal, a WebSocket).
// The machine is woken for it. ctx bounds only the opening; the terminal then lives until it ends or Close is called.
func (c *Client) OpenTerminal(ctx context.Context, machine string, opts TerminalOptions) (*Terminal, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	q, err := opts.query()
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[c.base.Scheme]
	u.Path = strings.TrimRight(c.base.Path, "/") + "/v1/machines/" + id + "/terminal"
	u.RawQuery = q.Encode()
	header := http.Header{
		"Authorization": {"Bearer " + string(c.key)},
		"User-Agent":    {c.userAgent},
	}
	ws, res, err := wsDial(ctx, u.String(), header)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ConnError{Host: c.base.Host, Err: err}
	}
	if ws == nil {
		// the handshake was answered, not switched: the API's own problem
		defer res.Body.Close()
		return nil, errorFrom(res, c.now())
	}
	return &Terminal{ws: ws}, nil
}

// Send types bytes into the terminal (keystrokes, as they are).
func (t *Terminal) Send(b []byte) error {
	return t.ws.writeMessage(wsBinary, b)
}

// Resize sets the terminal's window.
func (t *Terminal) Resize(cols, rows int) error {
	msg, _ := json.Marshal(struct {
		Resize struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		} `json:"resize"`
	}{Resize: struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}{cols, rows}})
	return t.ws.writeMessage(wsText, msg)
}

// Receive returns the next chunk of the program's output. When the terminal ends, it returns a nil slice and io-style
// end: End() then says how it ended. An error other than a clean end is returned as it is.
func (t *Terminal) Receive() ([]byte, error) {
	op, payload, err := t.ws.readMessage()
	if err != nil {
		var closed *wsClosed
		if asClosed(err, &closed) {
			return nil, errTerminalEnded
		}
		return nil, err
	}
	switch op {
	case wsBinary:
		return payload, nil
	case wsText:
		// the end: how the program ended, the last thing before the close
		var end TerminalEnd
		if json.Unmarshal(payload, &end) == nil {
			t.end = &end
		}
		return nil, nil
	}
	return nil, nil
}

// End is how the program ended, once Receive has reported the end (nil before then).
func (t *Terminal) End() *TerminalEnd { return t.end }

// Close ends the terminal and shuts its connection.
func (t *Terminal) Close() error { return t.ws.close() }

// errTerminalEnded is what Receive returns when the terminal has closed cleanly.
var errTerminalEnded = &terminalEnded{}

type terminalEnded struct{}

func (*terminalEnded) Error() string { return "the terminal has ended" }

// IsTerminalEnd reports whether err is the clean end of a terminal (what Receive returns when it is over).
func IsTerminalEnd(err error) bool {
	return err == errTerminalEnded
}

func asClosed(err error, target **wsClosed) bool {
	c, ok := err.(*wsClosed)
	if ok {
		*target = c
	}
	return ok
}
