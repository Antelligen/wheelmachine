package api

import (
	"context"
	"net/http"
	"net/url"
)

// ModelsQuery narrows the models listed to one agent's providers (Agent), has the lists read again from their providers
// when they are more than a minute old (Refresh), and names the team whose list of allowed models applies to them in an
// organisation (Team, a project's id; "" for the caller's one team).
type ModelsQuery struct {
	Agent   string
	Refresh bool
	Team    string
}

// Models is the models the account's AI keys can use, as each model's provider lists them for the key, grouped by provider
// in the order agents choose between keys, and how each provider's list stands.
func (c *Client) Models(ctx context.Context, q ModelsQuery) (*ModelList, error) {
	query := url.Values{}
	if q.Agent != "" {
		a, err := agentName(q.Agent)
		if err != nil {
			return nil, err
		}
		query.Set("agent", a)
	}
	if q.Refresh {
		query.Set("refresh", "true")
	}
	if q.Team != "" {
		query.Set("project", q.Team)
	}
	var out ModelList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/models", query: query, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Agents is the agents of the account's machines, each with the model chosen for it and what it starts on; in an
// organisation, the caller's own choices in the team (a project's id; "" for their one team).
func (c *Client) Agents(ctx context.Context, team string) (*AgentList, error) {
	var out AgentList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/agents", query: teamQuery(team), timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChooseAgentModel chooses the model an agent starts on, as GET /v1/models gives its id (openai/gpt-6.1-sol): it
// reaches running machines at once, and the others when they next start.
func (c *Client) ChooseAgentModel(ctx context.Context, agent, model, team string) (*Agent, error) {
	a, err := agentName(agent)
	if err != nil {
		return nil, err
	}
	var out Agent
	in := map[string]string{"model": model}
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/agents/" + a + "/model", query: teamQuery(team), in: in, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChooseEveryAgentModel chooses one model for every agent: each that can use its provider starts on it, and each
// agent's own choice is taken away.
func (c *Client) ChooseEveryAgentModel(ctx context.Context, model, team string) (*AgentList, error) {
	var out AgentList
	in := map[string]string{"model": model}
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/agents/model", query: teamQuery(team), in: in, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearAgentModel takes an agent's choice away: it starts on the one for every agent where it can, else its default.
func (c *Client) ClearAgentModel(ctx context.Context, agent, team string) error {
	a, err := agentName(agent)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/agents/" + a + "/model", query: teamQuery(team), timeout: callTimeout}, nil)
}

// ClearEveryAgentModel takes the one model for every agent away.
func (c *Client) ClearEveryAgentModel(ctx context.Context, team string) error {
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/agents/model", query: teamQuery(team), timeout: callTimeout}, nil)
}

// An agent's name as a route names it: an app's.
func agentName(name string) (string, error) {
	if !ValidAppName(name) {
		return "", &Error{Code: CodeInvalidRequest, Detail: "an agent's name is lower-case letters, digits and -, such as codex"}
	}
	return name, nil
}

func teamQuery(team string) url.Values {
	if team == "" {
		return nil
	}
	return url.Values{"project": {team}}
}

// The levels a list of allowed models is kept at.
const (
	AllowOrganisation = "organisation"
	AllowTeam         = "team"
	AllowRole         = "role"
	AllowPerson       = "person"
)

// AllowedModels is every list of the models the organisation's people may use: for its owners and admins.
func (c *Client) AllowedModels(ctx context.Context) (*AllowedModelsList, error) {
	var out AllowedModelsList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/models/allowed", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AllowModels sets the models people may use at a level - the organisation (id ""), a team (its project's id), a role
// (member, approver, manager or full) or a person (their id) - one to ten, in order: the first is the one an agent
// starts on where its person chose none of them.
func (c *Client) AllowModels(ctx context.Context, level, id string, models []string) (*AllowedModels, error) {
	path, err := allowedPath(level, id)
	if err != nil {
		return nil, err
	}
	var out AllowedModels
	in := map[string][]string{"models": models}
	if err := c.decode(ctx, call{method: http.MethodPut, path: path, in: in, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearAllowedModels takes a list away: its people are limited to the next list they have, or to none.
func (c *Client) ClearAllowedModels(ctx context.Context, level, id string) error {
	path, err := allowedPath(level, id)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: path, timeout: callTimeout}, nil)
}

// The route of a list at a level.
func allowedPath(level, id string) (string, error) {
	switch {
	case level == AllowOrganisation && id == "":
		return "/v1/models/allowed/organisation", nil
	case id == "":
	case level == AllowTeam:
		return "/v1/models/allowed/teams/" + url.PathEscape(id), nil
	case level == AllowRole:
		return "/v1/models/allowed/roles/" + url.PathEscape(id), nil
	case level == AllowPerson:
		return "/v1/models/allowed/people/" + url.PathEscape(id), nil
	}
	return "", &Error{Code: CodeInvalidRequest, Detail: "a list is the organisation's, a team's, a role's or a person's, named by its id"}
}
