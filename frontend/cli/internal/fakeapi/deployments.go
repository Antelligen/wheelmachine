package fakeapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"wheelmachine/frontend/cli/api"
)

// Deploy, as the API answers it. A run goes on by being looked at, one step a look: a run ends at the look that finds
// it at its last step, or at a wait.

// deployFake is what the fake keeps of Deploy.
type deployFake struct {
	// Closed answers every route 501 not_open, as the API does until Deploy opens.
	Closed bool
	// Fails makes each run end so, with these words; empty: each goes live.
	Fails string
	// Stuck makes a wait answer that it is still deploying, and a look find it at the step it is at.
	Stuck bool
	// Guess is what detect answers.
	Guess api.DeployGuess
	// Repos is what can be deployed.
	Repos []api.DeployRepo
	// Log is what the logs say.
	Log string
	// FilesAfter is how many looks at a new deployment of files find it still getting its machine ready: files sent until
	// then are refused, as the API refuses them.
	FilesAfter int

	list  []*api.Deployment
	runs  map[string][]api.DeployRun
	sent  map[string][]string
	asked []api.DeploymentCreate
}

// The steps a run goes through once it has what it needs.
var deployRunSteps = []string{"machine", "fetch", "build", "start", "check", "open"}

// DeployFiles is what a deployment was last sent, by path.
func (s *Server) DeployFiles(deployment string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Deploy.sent[deployment])
}

// DeployAsked is what each deployment was made with, oldest first.
func (s *Server) DeployAsked() []api.DeploymentCreate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Deploy.asked)
}

func (s *Server) deployRoutes(mux *http.ServeMux) {
	s.Deploy.runs, s.Deploy.sent = map[string][]api.DeployRun{}, map[string][]string{}
	s.Deploy.Guess = api.DeployGuess{Object: "deploy_guess", Runtime: "node", Build: "npm ci", Start: "npm start", Port: 3000}
	for pattern, fn := range map[string]http.HandlerFunc{
		"GET /v1/deployments":                           s.listDeployments,
		"POST /v1/deployments":                          s.createDeployment,
		"POST /v1/deployments/detect":                   s.detectDeployment,
		"GET /v1/deployments/repos":                     s.deployRepos,
		"GET /v1/deployments/{deployment_id}":           s.getDeployment,
		"PATCH /v1/deployments/{deployment_id}":         s.updateDeployment,
		"DELETE /v1/deployments/{deployment_id}":        s.deleteDeployment,
		"POST /v1/deployments/{deployment_id}/redeploy": s.redeploy,
		"PUT /v1/deployments/{deployment_id}/files":     s.deploymentFiles,
		"GET /v1/deployments/{deployment_id}/wait":      s.waitDeployment,
		"GET /v1/deployments/{deployment_id}/runs":      s.deploymentRuns,
		"GET /v1/deployments/{deployment_id}/logs":      s.deploymentLogs,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			closed := s.Deploy.Closed
			s.mu.Unlock()
			if closed {
				refuse(w, r, Refusal{Status: 501, Code: "not_open", Detail: "Deploy is not open yet"})
				return
			}
			fn(w, r)
		})
	}
}

// The deployment a route names, or nil once it is answered. The caller holds s.mu.
func (s *Server) deploymentAt(w http.ResponseWriter, r *http.Request) *api.Deployment {
	id := r.PathValue("deployment_id")
	for _, d := range s.Deploy.list {
		if d.ID == id {
			return d
		}
	}
	refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such deployment"})
	return nil
}

// A new run of a deployment. The caller holds s.mu.
func (s *Server) beginDeployRun(d *api.Deployment, why, step string) {
	run := api.DeployRun{Object: "deploy_run", ID: s.newID("dpr_"), Why: why, State: "working", Step: step, CreatedAt: s.Now().UTC()}
	d.Run = &run
	s.Deploy.runs[d.ID] = append([]api.DeployRun{run}, s.Deploy.runs[d.ID]...)
	if d.State != api.DeployLive {
		d.State = api.DeployBuilding
	}
}

// One look at a run under way: it goes on a step, and ends at its last. The caller holds s.mu.
func (s *Server) goOn(d *api.Deployment, toEnd bool) {
	if d.State == api.DeployWaitingForFiles && d.Run != nil && d.Run.Step == api.DeployStepMachine {
		if s.Deploy.FilesAfter--; s.Deploy.FilesAfter <= 0 {
			d.Run.Step = "files"
		}
		return
	}
	if s.Deploy.Stuck || d.Run == nil || d.Run.EndedAt != nil || d.State == api.DeployWaitingForFiles {
		return
	}
	at := slices.Index(deployRunSteps, d.Run.Step)
	if !toEnd && at+1 < len(deployRunSteps) {
		d.Run.Step = deployRunSteps[at+1]
		return
	}
	now := s.Now().UTC()
	d.Run.EndedAt = &now
	if s.Deploy.Fails != "" {
		d.Run.State, d.Run.Failure = "failed", api.Ptr(s.Deploy.Fails)
		if d.State != api.DeployLive {
			d.State = api.DeployFailed
		}
	} else {
		d.Run.State, d.State, d.LiveAt = "live", api.DeployLive, &now
		d.URL = api.Ptr("https://3000-" + strings.TrimPrefix(text(d.Machine), "m_") + ".run.wheelmachine.ai")
	}
	s.Deploy.runs[d.ID][0] = *d.Run
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*api.Deployment{}
	for _, d := range slices.Backward(s.Deploy.list) {
		if p := r.URL.Query().Get("project"); p == "" || p == d.Project {
			out = append(out, d)
		}
	}
	reply(w, 200, map[string]any{"object": "list", "data": out, "next": nil})
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in api.DeploymentCreate
	if !decode(w, r, &in, false) {
		return
	}
	s.once(w, r, body, func() (int, any, *Refusal) {
		src := in.Source
		kinds := 0
		for _, given := range []bool{src.Connection != "" || src.Repo != "", src.URL != "", src.Upload} {
			if given {
				kinds++
			}
		}
		if kinds != 1 || (src.Repo == "") != (src.Connection == "") {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "say which code: one of a connection and a repository, an address, or files"}
		}
		if strings.TrimSpace(in.Start) == "" || in.Port < 1 {
			return 0, nil, &Refusal{Status: 400, Code: "invalid_request", Detail: "say how the app starts, and on which port"}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		m := s.newID("m_")
		d := &api.Deployment{Object: "deployment", ID: s.newID("dep_"), Name: or(in.Name, api.SourceName(src.Repo, src.URL)), Project: or(in.Project, "prj_first"), Kind: "production",
			Build: api.Ptr(in.Build), Start: api.Ptr(in.Start), Port: in.Port, KeepAwake: in.KeepAwake, Secrets: in.Secrets, Machine: &m,
			CreatedAt: s.Now().UTC(), UpdatedAt: s.Now().UTC()}
		if d.Secrets == nil {
			d.Secrets = []string{}
		}
		switch {
		case src.Upload:
			d.Source.Kind = "upload"
		case src.URL != "":
			d.Source.Kind, d.Source.URL = "url", api.Ptr(src.URL)
		default:
			d.Source.Kind, d.Source.Connection, d.Source.Repo = "repo", api.Ptr(src.Connection), api.Ptr(src.Repo)
		}
		s.beginDeployRun(d, "first", "machine")
		if src.Upload {
			d.State, d.Run.Step = api.DeployWaitingForFiles, "files"
			if s.Deploy.FilesAfter > 0 {
				d.Run.Step = api.DeployStepMachine
			}
		}
		s.Deploy.list = append(s.Deploy.list, d)
		s.Deploy.asked = append(s.Deploy.asked, in)
		return 201, d, nil
	})
}

func (s *Server) detectDeployment(w http.ResponseWriter, r *http.Request) {
	var in api.DeployDetect
	if !decode(w, r, &in, false) {
		return
	}
	if in.Source == nil && in.Files == nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "say which code"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, 200, s.Deploy.Guess)
}

func (s *Server) deployRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []api.DeployRepo{}
	for _, repo := range s.Deploy.Repos {
		if c := r.URL.Query().Get("connection"); c == "" || c == repo.Connection {
			out = append(out, repo)
		}
	}
	reply(w, 200, map[string]any{"object": "list", "data": out, "next": nil})
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.deploymentAt(w, r); d != nil {
		s.goOn(d, false)
		reply(w, 200, d)
	}
}

func (s *Server) updateDeployment(w http.ResponseWriter, r *http.Request) {
	var in api.DeploymentChange
	if !decode(w, r, &in, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deploymentAt(w, r)
	if d == nil {
		return
	}
	if in.Name != nil {
		d.Name = *in.Name
	}
	if in.Branch != nil {
		d.Branch = in.Branch
	}
	if in.Build != nil {
		d.Build = in.Build
	}
	if in.Start != nil {
		d.Start = in.Start
	}
	if in.Port != nil {
		d.Port = *in.Port
	}
	if in.KeepAwake != nil {
		d.KeepAwake = *in.KeepAwake
	}
	reply(w, 200, d)
}

func (s *Server) deleteDeployment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.deploymentAt(w, r); d != nil {
		s.Deploy.list = slices.DeleteFunc(s.Deploy.list, func(o *api.Deployment) bool { return o == d })
		w.WriteHeader(204)
	}
}

func (s *Server) redeploy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deploymentAt(w, r)
	if d == nil {
		return
	}
	if d.Run != nil && d.Run.EndedAt == nil {
		refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "a deploy of it is under way: wait for it to end"})
		return
	}
	s.beginDeployRun(d, "redeploy", "fetch")
	reply(w, 202, d)
}

// The paths in a tar archive, gzipped or not.
func tarPaths(body []byte) ([]string, error) {
	var from io.Reader = bytes.NewReader(body)
	if zipped, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
		from = zipped
	}
	var paths []string
	archive := tar.NewReader(from)
	for {
		head, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return paths, nil
		}
		if err != nil {
			return nil, err
		}
		if head.Typeflag != tar.TypeReg && head.Typeflag != tar.TypeDir {
			return nil, errors.New(head.Name + " is neither a file nor a folder")
		}
		paths = append(paths, head.Name)
	}
}

func (s *Server) deploymentFiles(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength < 0 {
		refuse(w, r, Refusal{Status: 411, Code: "invalid_request", Detail: "say how long the archive is (Content-Length)"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	paths, err := tarPaths(body)
	if err != nil {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deploymentAt(w, r)
	if d == nil {
		return
	}
	if d.Source.Kind != "upload" {
		refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "its code comes from a repository: redeploy it instead"})
		return
	}
	if d.Run != nil && d.Run.EndedAt == nil && d.Run.Step == api.DeployStepMachine {
		refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "its machine is not ready yet: send the files again in a moment"})
		return
	}
	s.Deploy.sent[d.ID] = paths
	if d.State == api.DeployWaitingForFiles {
		d.State, d.Run.Step = api.DeployBuilding, "fetch"
	} else {
		s.beginDeployRun(d, "files", "fetch")
	}
	// (answered as the API answers: its run has not gone on from waiting for them yet, and does so by the next look)
	answer, run := *d, *d.Run
	run.Step = "files"
	answer.Run = &run
	reply(w, 202, &answer)
}

func (s *Server) waitDeployment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deploymentAt(w, r)
	if d == nil {
		return
	}
	if s.Deploy.Stuck {
		refuse(w, r, Refusal{Status: 408, Code: "timeout", Detail: "it is still deploying"})
		return
	}
	s.goOn(d, true)
	reply(w, 200, d)
}

func (s *Server) deploymentRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.deploymentAt(w, r); d != nil {
		reply(w, 200, map[string]any{"object": "list", "data": s.Deploy.runs[d.ID], "next": nil})
	}
}

func (s *Server) deploymentLogs(w http.ResponseWriter, r *http.Request) {
	kind := or(r.URL.Query().Get("kind"), "build")
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deploymentAt(w, r)
	if d == nil {
		return
	}
	reply(w, 200, api.DeployLog{Object: "deploy_log", Kind: kind, Run: api.Ptr(s.Deploy.runs[d.ID][0].ID), Text: s.Deploy.Log,
		Done: d.Run == nil || d.Run.EndedAt != nil})
}
