package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Deploy: an app put on the internet from a repository on GitHub or GitLab, the https address of a public repository,
// or files, each on a machine of its own at a public address.

// DeployOpen says whether people can deploy yet. Until they can, the API answers each of these calls 501 not_open, and
// wm's help and wm-mcp's list of tools leave Deploy out: a wm released before then has these commands, and they say so.
const DeployOpen = false

// A deployment's states.
const (
	// the step of a run that is getting its machine ready
	DeployStepMachine = "machine"

	DeployBuilding        = "building"
	DeployWaitingForFiles = "waiting_for_files"
	DeployLive            = "live"
	DeployNotAnswering    = "not_answering"
	DeployFailed          = "failed"
)

// Deployment is an app on a machine of its own, with its current or last run.
type Deployment struct {
	Object     string       `json:"object"`
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Project    string       `json:"project"`
	HeldBy     *HeldBy      `json:"held_by"`
	Kind       string       `json:"kind"`
	Source     DeploySource `json:"source"`
	Branch     *string      `json:"branch"`
	Commit     *string      `json:"commit"`
	Build      *string      `json:"build"` // shown only to whoever uses its machine
	Start      *string      `json:"start"` // and so is this
	Port       int          `json:"port"`
	CPUs       *int         `json:"cpus"`
	Type       *string      `json:"type"`
	Region     *string      `json:"region"`
	KeepAwake  bool         `json:"keep_awake"`
	Secrets    []string     `json:"secrets"`
	Machine    *string      `json:"machine"`
	URL        *string      `json:"url"`
	State      string       `json:"state"`
	LastStatus *int         `json:"last_status"`
	Run        *DeployRun   `json:"run"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	LiveAt     *time.Time   `json:"live_at"`
}

// DeploySource is where a deployment's code comes from.
type DeploySource struct {
	Kind       string  `json:"kind"` // repo, url or upload
	Provider   *string `json:"provider"`
	Connection *string `json:"connection"`
	Host       *string `json:"host"`
	Repo       *string `json:"repo"`
	URL        *string `json:"url"`
}

// DeployRun is one go at putting a version on a deployment's machine.
type DeployRun struct {
	Object    string     `json:"object"`
	ID        string     `json:"id"`
	Why       string     `json:"why"`
	State     string     `json:"state"` // queued, working, live, failed or cancelled
	Step      string     `json:"step"`
	Progress  int        `json:"progress"`
	Note      *string    `json:"note"`
	Failure   *string    `json:"failure"`
	Commit    *string    `json:"commit"`
	Branch    *string    `json:"branch"`
	Files     *int       `json:"files"`
	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

// DeploySourceCreate says where the code of a new deployment comes from: a repository of a connection (Connection and
// Repo), a public repository's https address (URL), or files sent next (Upload).
type DeploySourceCreate struct {
	Connection string `json:"connection,omitempty"`
	Repo       string `json:"repo,omitempty"`
	URL        string `json:"url,omitempty"`
	Upload     bool   `json:"upload,omitempty"`
}

// DeploymentCreate is a new deployment. Source, Start and Port are needed.
type DeploymentCreate struct {
	Name      string             `json:"name,omitempty"`
	Project   string             `json:"project,omitempty"`
	Source    DeploySourceCreate `json:"source"`
	Branch    string             `json:"branch,omitempty"`
	Build     string             `json:"build,omitempty"`
	Start     string             `json:"start"`
	Port      int                `json:"port"`
	CPUs      int                `json:"cpus,omitempty"`
	Type      string             `json:"type,omitempty"`
	Region    string             `json:"region,omitempty"`
	Secrets   []string           `json:"secrets,omitempty"`
	KeepAwake bool               `json:"keep_awake,omitempty"`
}

// DeploymentChange is what changes in a deployment: what is nil stays.
type DeploymentChange struct {
	Name      *string `json:"name,omitempty"`
	Branch    *string `json:"branch,omitempty"`
	Build     *string `json:"build,omitempty"`
	Start     *string `json:"start,omitempty"`
	Port      *int    `json:"port,omitempty"`
	KeepAwake *bool   `json:"keep_awake,omitempty"`
}

// DeployDetect asks how code most likely builds and starts: a source's, or that of files about to be sent, by name,
// each with its contents or nil for one that is there, unread.
type DeployDetect struct {
	Source *DeploySourceCreate `json:"source,omitempty"`
	Branch string              `json:"branch,omitempty"`
	Files  map[string]*string  `json:"files,omitzero"` // (empty is said: a folder with none of them)
}

// DeployGuess is how code most likely builds and starts, to change as needed.
type DeployGuess struct {
	Object  string  `json:"object"`
	Runtime string  `json:"runtime"`
	Build   string  `json:"build"`
	Start   string  `json:"start"`
	Port    int     `json:"port"`
	Note    *string `json:"note"`
}

// DeployRepo is a repository that can be deployed into a team.
type DeployRepo struct {
	Object     string     `json:"object"`
	Connection string     `json:"connection"`
	Provider   string     `json:"provider"`
	Host       string     `json:"host"`
	Repo       string     `json:"repo"`
	Shared     bool       `json:"shared"` // given to the team already; false: deploying it gives it the team to read first
	UpdatedAt  *time.Time `json:"updated_at"`
}

// DeployLog is a deployment's log: a run's own (build), or the end of what the app itself writes (app).
type DeployLog struct {
	Object string  `json:"object"`
	Kind   string  `json:"kind"`
	Run    *string `json:"run"`
	Text   string  `json:"text"`
	Done   bool    `json:"done"`
}

// ListDeployments gives the deployments the key sees, the newest first; of one project alone, if one is given by id.
func (c *Client) ListDeployments(ctx context.Context, project string) ([]Deployment, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	var out Page[Deployment]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// IsDeploymentID says whether a word has the shape of a deployment's id.
func IsDeploymentID(s string) bool { return hasIDShape(s, "dep_") }

// FindDeployment gives the id of the deployment named by idOrName: an id as it is, else the one deployment of that name.
func (c *Client) FindDeployment(ctx context.Context, idOrName string) (string, error) {
	if IsDeploymentID(idOrName) {
		return idOrName, nil
	}
	return find("deployment", idOrName, func(yield func(string, string) bool) error {
		list, err := c.ListDeployments(ctx, "")
		if err != nil {
			return err
		}
		for _, d := range list {
			if !yield(d.ID, d.Name) {
				return nil
			}
		}
		return nil
	})
}

// DeploymentNamed gives the deployment of this name the key sees, in any case; nil when there is none. Deploying again in
// the same folder must not make a second machine, each charged, and name-the-same besides.
func (c *Client) DeploymentNamed(ctx context.Context, name string) (*Deployment, error) {
	list, err := c.ListDeployments(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// CreateDeployment makes a deployment and begins its first run, which goes on after this answer. One of files then
// waits for them: SendDeploymentFiles.
func (c *Client) CreateDeployment(ctx context.Context, in DeploymentCreate) (*Deployment, error) {
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/deployments", in: in, create: true, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDeployment gives a deployment as it is now.
func (c *Client) GetDeployment(ctx context.Context, deployment string) (*Deployment, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments/" + id, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateDeployment changes a deployment's settings. Its name, branch, commands and port apply from its next run;
// keep_awake at once.
func (c *Client) UpdateDeployment(ctx context.Context, deployment string, change DeploymentChange) (*Deployment, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodPatch, path: "/v1/deployments/" + id, in: change, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDeployment deletes a deployment and its machine.
func (c *Client) DeleteDeployment(ctx context.Context, deployment string) error {
	id, err := segment("deployment", deployment)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodDelete, path: "/v1/deployments/" + id, timeout: callTimeout}, nil)
}

// Redeploy begins a new run: its branch as it is now, or its files again. It is not sent twice: a call that lost its
// connection is told by the run under way (conflict).
func (c *Client) Redeploy(ctx context.Context, deployment string) (*Deployment, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/deployments/" + id + "/redeploy", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendDeploymentFiles sends size bytes of a tar archive, or a gzipped one, as a deployment's files, its first or new
// ones in place of its last; it is then built and started. A folder goes through SendFolder. r can be read again from
// its start if it can seek, so that a call cut short is sent again.
func (c *Client) SendDeploymentFiles(ctx context.Context, deployment string, r io.Reader, size int64) (*Deployment, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/deployments/" + id + "/files", stream: r, size: size}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendFolder sends a packed folder as a deployment's files once its run waits for them. The API holds a call only a minute
// for a machine to be ready, and a machine may take longer: rather than send it all to be refused, this waits until its
// run has gone past getting the machine ready, at most timeout, looking every so often and telling seen of each look (seen
// may be nil). A run begun by the sending itself is waited for the same way, and the files sent again.
func (c *Client) SendFolder(ctx context.Context, deployment string, p *PackedFolder, timeout, every time.Duration, seen func(*Deployment)) (*Deployment, error) {
	deadline := time.Now().Add(timeout)
	for {
		if err := c.untilFilesWanted(ctx, deployment, deadline, every, seen); err != nil {
			return nil, err
		}
		if _, err := p.File.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		sent, err := c.SendDeploymentFiles(ctx, deployment, p.File, p.Size)
		if err == nil {
			return sent, nil
		}
		if !IsCode(err, CodeConflict) || !time.Now().Before(deadline) {
			return nil, err
		}
		now, again := c.GetDeployment(ctx, deployment)
		if again != nil || !gettingMachine(now) {
			return nil, err
		}
	}
}

// Whether its run is under way, and still getting its machine ready.
func gettingMachine(d *Deployment) bool {
	return d.Run != nil && d.Run.EndedAt == nil && d.Run.Step == DeployStepMachine
}

func (c *Client) untilFilesWanted(ctx context.Context, deployment string, deadline time.Time, every time.Duration, seen func(*Deployment)) error {
	for {
		d, err := c.GetDeployment(ctx, deployment)
		if err != nil {
			return err
		}
		if seen != nil {
			seen(d)
		}
		if !gettingMachine(d) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return &Error{Status: 408, Code: CodeTimeout, Detail: "its machine is not ready yet, so its files were not sent"}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// WaitForDeployment answers once its current run has ended, waiting at most timeout (and at most MaxWait), or with a
// timeout error while it is still deploying.
func (c *Client) WaitForDeployment(ctx context.Context, deployment string, timeout time.Duration) (*Deployment, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	timeout = min(max(timeout, 0), MaxWait)
	q := url.Values{"timeout_s": {strconv.Itoa(int(timeout.Seconds()))}}
	var out Deployment
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments/" + id + "/wait", query: q, timeout: timeout + time.Minute}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeploymentRuns gives a deployment's last runs, the newest first.
func (c *Client) DeploymentRuns(ctx context.Context, deployment string) ([]DeployRun, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	var out Page[DeployRun]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments/" + id + "/runs", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// DeploymentLog gives a deployment's log: kind build, a run's own (run, or its last when empty), or kind app, the end
// of what the app itself writes, which wakes its machine.
func (c *Client) DeploymentLog(ctx context.Context, deployment, kind, run string) (*DeployLog, error) {
	id, err := segment("deployment", deployment)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	if run != "" {
		q.Set("run", run)
	}
	var out DeployLog
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments/" + id + "/logs", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DetectDeployment says how code most likely builds and starts. Asking changes nothing, so it is sent again freely.
func (c *Client) DetectDeployment(ctx context.Context, in DeployDetect) (*DeployGuess, error) {
	var out DeployGuess
	if err := c.decode(ctx, call{method: http.MethodPost, path: "/v1/deployments/detect", in: in, replay: true, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeployRepos gives the repositories that can be deployed into a team (a project's id; the caller's first when empty),
// of one connection alone if one is given, the most recently pushed to first.
func (c *Client) DeployRepos(ctx context.Context, project, connection string) ([]DeployRepo, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if connection != "" {
		q.Set("connection", connection)
	}
	var out Page[DeployRepo]
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/deployments/repos", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
