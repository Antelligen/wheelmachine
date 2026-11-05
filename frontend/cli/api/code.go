package api

import (
	"context"
	"net/http"
	"net/url"
)

// Code access: the GitHub and GitLab an account connects, and the repositories its teams' machines may read and
// write, with no token ever in a machine.

// CodeConnection is a code host connected to the account: a GitHub App installation, or a GitLab group.
type CodeConnection struct {
	Object    string `json:"object"`
	ID        string `json:"id"`
	Provider  string `json:"provider"` // github or gitlab
	Host      string `json:"host"`
	Name      string `json:"name"`
	Suspended bool   `json:"suspended"`
	AddedAt   string `json:"added_at"`
}

// CodeGrant is a repository a team is given: to read, or to write (push) too.
type CodeGrant struct {
	Object     string `json:"object"`
	ID         string `json:"id"`
	Project    string `json:"project"`
	Connection string `json:"connection"`
	Host       string `json:"host"`
	Repo       string `json:"repo"`
	Access     string `json:"access"` // read or write
	GrantedAt  string `json:"granted_at"`
}

// CodeRepo is a repository a connection reaches.
type CodeRepo struct {
	Object     string `json:"object"`
	Repo       string `json:"repo"`
	Host       string `json:"host"`
	Connection string `json:"connection"`
}

// Code is the account's connections, and the repositories given to the teams the caller is in.
type Code struct {
	Object      string           `json:"object"`
	GitHub      bool             `json:"github"`
	Connections []CodeConnection `json:"connections"`
	Grants      []CodeGrant      `json:"grants"`
}

// Code returns the account's code hosts and its teams' repositories.
func (c *Client) Code(ctx context.Context) (*Code, error) {
	var out Code
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/code", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GitHubInstallURL returns where to install Wheelmachine's GitHub App for the account (good for half an hour).
func (c *Client) GitHubInstallURL(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/code/github", timeout: callTimeout}, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// ConnectGitLab connects a GitLab group by its access token; address is the GitLab's https address ("" for gitlab.com).
func (c *Client) ConnectGitLab(ctx context.Context, address, token string) (*CodeConnection, error) {
	in := map[string]any{"token": token}
	if address != "" {
		in["address"] = address
	}
	var out CodeConnection
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/code/gitlab", in: in, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisconnectCode takes a connection away, with every repository a team was given of it.
func (c *Client) DisconnectCode(ctx context.Context, id string) error {
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/code/connections/" + url.PathEscape(id), timeout: callTimeout}, nil)
}

// CodeRepos lists the repositories a connection reaches.
func (c *Client) CodeRepos(ctx context.Context, connection string) ([]CodeRepo, error) {
	var out Page[CodeRepo]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/code/connections/" + url.PathEscape(connection) + "/repos", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// TeamCode lists the repositories a team is given.
func (c *Client) TeamCode(ctx context.Context, project string) ([]CodeGrant, error) {
	var out Page[CodeGrant]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/projects/" + url.PathEscape(project) + "/code", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GrantCode gives a team a repository of a connection, to "read" or to "write".
func (c *Client) GrantCode(ctx context.Context, project, connection, repo, access string) (*CodeGrant, error) {
	var out CodeGrant
	in := map[string]any{"connection": connection, "repo": repo, "access": access}
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/projects/" + url.PathEscape(project) + "/code", in: in, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeCode takes a repository from a team.
func (c *Client) RevokeCode(ctx context.Context, project, grant string) error {
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/projects/" + url.PathEscape(project) + "/code/" + url.PathEscape(grant), timeout: callTimeout}, nil)
}
