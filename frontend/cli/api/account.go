package api

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Me is the account and key this client calls with, and whether the account can spend.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/me", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Orgs is the organisations the key's person is in, the newest first, with their role in each. A key made in an
// organisation lists none - what else the person who made it is in is theirs alone - and is refused (CodeForbidden): a key of the
// person's own account lists them.
func (c *Client) Orgs(ctx context.Context) ([]Org, error) {
	var out Page[Org]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/orgs", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// UsageParams picks a month (YYYY-MM; this month when empty) and, optionally, a project.
type UsageParams struct {
	Month   string
	Project string
}

// Usage is each machine's running time and cost in a month.
func (c *Client) Usage(ctx context.Context, p UsageParams) (*Usage, error) {
	q := url.Values{}
	if p.Month != "" {
		q.Set("month", p.Month)
	}
	if p.Project != "" {
		q.Set("project", p.Project)
	}
	var out Usage
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/usage", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Limits is every limit for the month that applies to the account, as the key's person may see it.
func (c *Client) Limits(ctx context.Context) (*UsageLimits, error) {
	var out UsageLimits
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/usage/limits", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetMonthLimit sets the account's own limit for the month, in dollars, or takes it away with nil: in your own
// account, with a key that holds cloud:*; in an organisation, an owner's.
func (c *Client) SetMonthLimit(ctx context.Context, usd *float64) (*UsageLimits, error) {
	var out UsageLimits
	err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/usage/limits/account", in: map[string]any{"month_cap_usd": usd},
		timeout: callTimeout}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Regions is the places you can ask a machine to run in.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var out Page[Region]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/regions", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GPUs is the GPUs on offer now, where they are and what an hour costs.
func (c *Client) GPUs(ctx context.Context) (*GpuList, error) {
	var out GpuList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/gpus", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListParams is a page of a list: its size (0: the API's 50) and where it begins.
type ListParams struct {
	Limit int
	After string
}

// ListProjects is one page of the account's projects.
func (c *Client) ListProjects(ctx context.Context, p ListParams) (*Page[Project], error) {
	q := url.Values{}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		q.Set("after", p.After)
	}
	var page Page[Project]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/projects", query: q, timeout: callTimeout}, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// AllProjects walks every project, a page at a time.
func (c *Client) AllProjects(ctx context.Context, p ListParams) iter.Seq2[Project, error] {
	return all(p.After, func(after string) (*Page[Project], error) {
		return c.ListProjects(ctx, ListParams{Limit: p.Limit, After: after})
	})
}

// ProjectCreate names a new project.
type ProjectCreate struct {
	Name string `json:"name"`

	IdempotencyKey string `json:"-"`
}

// CreateProject makes a project. A key limited to some projects cannot.
func (c *Client) CreateProject(ctx context.Context, p ProjectCreate) (*Project, error) {
	var out Project
	cl := call{method: http.MethodPost, path: "/v1/projects", in: p, create: true, idemKey: p.IdempotencyKey, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProject is one project.
func (c *Client) GetProject(ctx context.Context, project string) (*Project, error) {
	id, err := segment("project", project)
	if err != nil {
		return nil, err
	}
	var out Project
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/projects/" + id, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameProject gives a project a new name.
func (c *Client) RenameProject(ctx context.Context, project, name string) (*Project, error) {
	id, err := segment("project", project)
	if err != nil {
		return nil, err
	}
	var out Project
	cl := call{method: http.MethodPatch, path: "/v1/projects/" + id, in: map[string]string{"name": name}, replay: true, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteProject deletes an empty project; one that still holds machines is refused (CodeProjectNotEmpty).
func (c *Client) DeleteProject(ctx context.Context, project string) error {
	id, err := segment("project", project)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/projects/" + id, timeout: callTimeout}, nil)
}

// ListKeys is the account's API keys (those holding a cloud: scope) that this key may see.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var out Page[Key]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/keys", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// CreateKey makes a key. Its secret is in this answer only: keep it now.
func (c *Client) CreateKey(ctx context.Context, k KeyCreate) (*KeyCreated, error) {
	body := map[string]any{"name": k.Name, "scopes": k.Scopes}
	if len(k.Projects) > 0 {
		body["projects"] = k.Projects
	}
	if k.ExpiresAt != nil {
		body["expires_at"] = k.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if k.RatePerMinute != nil {
		body["rate_per_minute"] = *k.RatePerMinute
	}
	var out KeyCreated
	cl := call{method: http.MethodPost, path: "/v1/keys", in: body, create: true, idemKey: k.IdempotencyKey, timeout: callTimeout, confirm: k.ConfirmCode}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendConfirmCode e-mails the key's person a code for a risky step, as an error that needs one says it
// (Error.Confirm): it works once, for 10 minutes, for that step alone.
func (c *Client) SendConfirmCode(ctx context.Context, ask ConfirmAsk) (*ConfirmCodeSent, error) {
	var out ConfirmCodeSent
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/confirm-codes", in: ask, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey stops a key working, within seconds, everywhere. A key can revoke itself.
func (c *Client) RevokeKey(ctx context.Context, key string) error {
	id, err := segment("key", key)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/keys/" + id, timeout: callTimeout}, nil)
}
