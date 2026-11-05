package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PersonName is a person by their id and their name alone.
type PersonName struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Handle string `json:"handle"` // their @handle, without its @
}

// Named is the person as wm prints them: their name and @handle, "Ada Lovelace · @ada" - either alone when the other is
// not known.
func (p PersonName) Named() string {
	switch {
	case p.Handle == "":
		return p.Name
	case p.Name == "":
		return "@" + p.Handle
	}
	return p.Name + " · @" + p.Handle
}

// RequestMachine is the machine a request is of: its name as it was picked, and its id to those who reach it.
type RequestMachine struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
}

// WaitingOn is whose OK a request waits on: the person who made the key (asker), by name, for one given or asked with a key - approved in
// the dashboard, signed in - its holder, by name, a manager (for a fork, a full manager) or an admin.
type WaitingOn struct {
	Who  string  `json:"who"`
	Name *string `json:"name"`
}

// Request is a request, in an organisation, to fork a machine for someone or to hand it off to them: given by its holder
// (or one above them), or asked for by the person who would get it - or a member's fork of a machine they hold, its
// Holder and Receiver both them, made once it is approved: from the machine as it is then, or from the Backup asked.
type Request struct {
	ID          string          `json:"id"`
	Object      string          `json:"object"`
	Kind        string          `json:"kind"` // fork or hand_off
	Way         string          `json:"way"`  // give, ask, or back: a machine handed off, back with the one who handed it off
	Machine     *RequestMachine `json:"machine"`
	Holder      PersonName      `json:"holder"`
	Receiver    PersonName      `json:"receiver"`
	AskedBy     PersonName      `json:"asked_by"`
	FromTeam    *string         `json:"from_team"`
	ToTeam      *string         `json:"to_team"`
	GPU         bool            `json:"gpu"`
	Why         string          `json:"why"`
	State       string          `json:"state"`
	WaitingOn   *WaitingOn      `json:"waiting_on"`
	DecidedBy   *PersonName     `json:"decided_by"`
	DecisionWhy string          `json:"decision_why"`
	Result      *string         `json:"result"`
	Backup      *string         `json:"backup"` // a fork from one of the machine's backups: its id; nil for one of the machine as it is
	CPUs        *int            `json:"cpus"`   // a fork from a backup asked at another size: that size; nil for the original's
	Failure     *string         `json:"failure"`
	Restarted   bool            `json:"restarted"`
	KeysRevoked int             `json:"keys_revoked"` // Wheelmachine keys found in it as it was cleared, and revoked
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	DoneAt      *time.Time      `json:"done_at"`
}

// The kinds and states of a request.
const (
	RequestFork    = "fork"
	RequestHandOff = "hand_off"
)

// StateWords is a request's state in the words the dashboard says it: whose OK it waits for, or how it ended.
func (q Request) StateWords() string {
	switch q.State {
	case "waiting":
		if q.WaitingOn != nil && q.WaitingOn.Who == "asker" {
			name := q.AskedBy.Name
			if q.WaitingOn.Name != nil && *q.WaitingOn.Name != "" {
				name = *q.WaitingOn.Name
			}
			return "Waiting for " + or(name, "the person who made the key")
		}
		if q.WaitingOn != nil && q.WaitingOn.Who == "holder" {
			name := q.Holder.Name
			if q.WaitingOn.Name != nil && *q.WaitingOn.Name != "" {
				name = *q.WaitingOn.Name
			}
			return "Waiting for " + or(name, "its holder")
		}
		if q.WaitingOn != nil && q.WaitingOn.Who == "admin" {
			return "Waiting for an admin"
		}
		if q.Kind == RequestFork {
			// (a manager below full decides no fork: an owner or an admin does, and so does a full manager of its team)
			return "Waiting for a full manager"
		}
		return "Waiting for a manager"
	case "approved":
		return "Approved"
	case "done":
		if q.Kind == RequestFork {
			return "Forked"
		}
		return "Handed off"
	case "declined":
		return "Declined"
	case "expired":
		return "Expired"
	case "taken_back":
		return "Withdrawn"
	}
	return "Failed"
}

// RequestList is a page of requests, the newest first.
type RequestList struct {
	Object string    `json:"object"`
	Data   []Request `json:"data"`
	Next   *string   `json:"next"`
}

// GiveTo is whom a machine is forked for, or handed off to: a teammate by their id, e-mail address or GitHub login - and
// the team of theirs it goes into, when it is another than the machine's.
type GiveTo struct {
	To   string `json:"to"`
	Team string `json:"team,omitempty"`
	Why  string `json:"why,omitempty"`

	IdempotencyKey string `json:"-"`
}

// BackTo is where a machine handed off goes back to the one who handed it off: the team of theirs it goes into, when it
// should be another than the API picks (where it is, else where it was handed off from, else their only team).
type BackTo struct {
	Team string `json:"team,omitempty"`

	IdempotencyKey string `json:"-"`
}

// RequestAsk is an ask for a fork, or for a hand-off, of a machine of a teammate's (Of): they pick which as they
// approve.
type RequestAsk struct {
	Kind string `json:"kind"`
	Of   string `json:"of"`
	Team string `json:"team,omitempty"`
	Why  string `json:"why,omitempty"`

	IdempotencyKey string `json:"-"`
}

// TeamRules is a team's rules: whether its machines are handed off freely (free), with a manager's OK (ask) or not at all
// (off); whether its members fork them - every fork waiting for the OK of a full manager of the team, an owner or an
// admin (ask) - or not at all (off); whether GPU machines and open internet are allowed in it; and its budget.
type TeamRules struct {
	Object         string      `json:"object"`
	Project        string      `json:"project"`
	HandOffs       string      `json:"hand_offs"`
	Forks          string      `json:"forks"` // ask or off, never free
	GPUs           bool        `json:"gpus"`
	OpenInternet   bool        `json:"open_internet"`
	MonthBudgetUSD *float64    `json:"month_budget_usd"`
	SpentUSD       *float64    `json:"spent_usd"`     // nil to the team's members: its managers, owners, admins and billing see it
	OpenMachines   *int        `json:"open_machines"` // nil as SpentUSD is
	Models         string      `json:"models"`
	SetBy          *PersonName `json:"set_by"`
	SetAt          *time.Time  `json:"set_at"`
	// the largest machine it holds (CPUs) and the longest its machines stay up idle (seconds), nil for no rule; and
	// whether their ports may be opened to the whole internet: on, links_only or off
	MaxMachineVCPUs *int   `json:"max_machine_vcpus"`
	IdleMaxSeconds  *int   `json:"idle_max_seconds"`
	PublicPorts     string `json:"public_ports"`
	// whether its members and managers make machines from its types alone (wm machines types)
	TypesOnly bool `json:"types_only"`
}

// How long a fork for someone or a hand-off is waited for: it is carried out in the call for a minute at most, read or cleared first.
const giveTimeout = 2 * time.Minute

func (c *Client) give(ctx context.Context, machine, route string, g GiveTo) (*Request, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Request
	cl := call{method: http.MethodPost, path: "/v1/machines/" + id + "/" + route, in: g, create: true, idemKey: g.IdempotencyKey, timeout: giveTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ForkMachineFor forks a machine for a teammate: carried out at once when nobody else need approve it, cleared before
// it is theirs - else a request that waits for those who decide it.
func (c *Client) ForkMachineFor(ctx context.Context, machine string, g GiveTo) (*Request, error) {
	return c.give(ctx, machine, "fork-for", g)
}

// HandOffMachine hands a machine off to a teammate, as ForkMachineFor forks one for them.
func (c *Client) HandOffMachine(ctx context.Context, machine string, g GiveTo) (*Request, error) {
	return c.give(ctx, machine, "hand-off", g)
}

// TakeBackMachine takes back a machine the key's person handed off, exactly as it is: a request done at once - made with
// a key, waiting first for the OK of the person who made the key in the dashboard.
func (c *Client) TakeBackMachine(ctx context.Context, machine string, b BackTo) (*Request, error) {
	return c.back(ctx, machine, "take-back", b)
}

// GiveBackMachine gives a machine handed to the key's person back to the one who handed it off, as TakeBackMachine
// takes one back.
func (c *Client) GiveBackMachine(ctx context.Context, machine string, b BackTo) (*Request, error) {
	return c.back(ctx, machine, "give-back", b)
}

func (c *Client) back(ctx context.Context, machine, route string, b BackTo) (*Request, error) {
	id, err := segment("machine", machine)
	if err != nil {
		return nil, err
	}
	var out Request
	cl := call{method: http.MethodPost, path: "/v1/machines/" + id + "/" + route, in: b, create: true, idemKey: b.IdempotencyKey, timeout: giveTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRequests is a page of the requests the key sees, the newest first: those still open alone when `open`.
func (c *Client) ListRequests(ctx context.Context, open bool, p ListParams) (*RequestList, error) {
	q := url.Values{}
	if open {
		q.Set("open", "true")
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		q.Set("after", p.After)
	}
	var out RequestList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/requests", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AskRequest asks a teammate for a fork, or a hand-off, of one of their machines.
func (c *Client) AskRequest(ctx context.Context, a RequestAsk) (*Request, error) {
	var out Request
	cl := call{method: http.MethodPost, path: "/v1/requests", in: a, create: true, idemKey: a.IdempotencyKey, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TakeBackRequest withdraws a request the key's person asked, while it waits.
func (c *Client) TakeBackRequest(ctx context.Context, request string) (*Request, error) {
	id, err := segment("request", request)
	if err != nil {
		return nil, err
	}
	var out Request
	if err := c.decode(ctx, call{method: http.MethodDelete, path: "/v1/requests/" + id, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTeamRules is a team's rules: those set, else the defaults.
func (c *Client) GetTeamRules(ctx context.Context, project string) (*TeamRules, error) {
	id, err := segment("project", project)
	if err != nil {
		return nil, err
	}
	var out TeamRules
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/projects/" + id + "/rules", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
