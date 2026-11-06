package fakeapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

// The models of the account's AI keys and the agents of its machines, as the API answers them (contracts/openapi.yaml,
// GET /v1/models and /v1/agents): OpenAI's and DeepSeek's lists, and Codex and Aider with the providers the machine's
// own tables give them. A choice is checked as the API checks one - a model a provider lists, an agent it can use - and
// stands on the agents as the API's does.

func defaultModels() api.ModelList {
	m := func(supplier, id, name string) api.Model {
		return api.Model{Object: "model", ID: supplier + "/" + id, Supplier: supplier, Model: id, Name: name, Agents: []string{"codex", "aider"}}
	}
	sol, luna, pro := m("openai", "gpt-6.1-sol", "gpt-6.1-sol"), m("openai", "gpt-6-luna", "gpt-6-luna"), m("deepseek", "deepseek-v4-pro", "DeepSeek-V4-Pro")
	pro.Agents = []string{"aider"}
	// each with its provider's list price, and the leaderboard's score where it has one
	sol.Price = &api.ModelPrice{InputPerMillionUSD: 2, OutputPerMillionUSD: 10, Source: "OpenAI", URL: "https://developers.openai.com/api/docs/pricing", AsOf: "2026-11-02"}
	luna.Price = &api.ModelPrice{InputPerMillionUSD: 0.1, OutputPerMillionUSD: 0.5, Source: "OpenAI", URL: "https://developers.openai.com/api/docs/pricing", AsOf: "2026-11-02"}
	pro.Price = &api.ModelPrice{InputPerMillionUSD: 1.32, OutputPerMillionUSD: 3.96, Source: "DeepSeek", URL: "https://api-docs.deepseek.com/quick_start/pricing", AsOf: "2026-11-02"}
	sol.Intelligence = &api.ModelIntelligence{Score: 1481, Source: "LMArena text leaderboard", URL: "https://lmarena.ai/leaderboard/text", License: "CC BY 4.0", AsOf: "2026-10-28"}
	pro.Intelligence = &api.ModelIntelligence{Score: 1465, Source: "LMArena text leaderboard", URL: "https://lmarena.ai/leaderboard/text", License: "CC BY 4.0", AsOf: "2026-10-28"}
	return api.ModelList{Object: "list", Data: []api.Model{sol, luna, pro},
		Suppliers: []api.ModelSupplier{
			{Object: "model_supplier", ID: "openai", Title: "OpenAI", Key: "OPENAI_API_KEY", Whose: "account", State: "listed"},
			{Object: "model_supplier", ID: "deepseek", Title: "DeepSeek", Key: "DEEPSEEK_API_KEY", Whose: "account", State: "listed"},
		}}
}

func defaultAgents() []api.Agent {
	sol := "gpt-6.1-sol"
	via := "openai/gpt-6.1-sol"
	return []api.Agent{
		{Object: "agent", Name: "codex", Title: "Codex", Suppliers: []api.AgentSupplier{{ID: "openai", Title: "OpenAI"}, {ID: "openrouter", Title: "OpenRouter", Default: &via}}},
		{Object: "agent", Name: "aider", Title: "Aider", Suppliers: []api.AgentSupplier{{ID: "openai", Title: "OpenAI", Default: &sol}, {ID: "deepseek", Title: "DeepSeek"}}},
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := api.ModelList{Object: "list", Currency: s.Models.Currency, Rate: s.Models.Rate, Data: slices.Clone(s.Models.Data),
		Suppliers: slices.Clone(s.Models.Suppliers)}
	if name := r.URL.Query().Get("agent"); name != "" {
		a := s.agentLocked(name)
		if a == nil {
			refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such agent"})
			return
		}
		out.Data = slices.DeleteFunc(out.Data, func(m api.Model) bool { return !uses(a, m.Supplier) })
		out.Suppliers = slices.DeleteFunc(out.Suppliers, func(m api.ModelSupplier) bool { return !uses(a, m.ID) })
	}
	if held := s.HeldTo; held != nil {
		out.Data = slices.DeleteFunc(out.Data, func(m api.Model) bool { return !slices.Contains(held.Models, m.ID) })
		out.Allowed = held
	}
	reply(w, 200, out)
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, 200, s.agentListLocked())
}

func (s *Server) agentLocked(name string) *api.Agent {
	for i := range s.Agents {
		if s.Agents[i].Name == name {
			return &s.Agents[i]
		}
	}
	return nil
}

func uses(a *api.Agent, supplier string) bool {
	return slices.ContainsFunc(a.Suppliers, func(x api.AgentSupplier) bool { return x.ID == supplier })
}

// The agents as the API shows them with the choices made: an agent's own, then the one for every agent where it can
// use its provider, else its default with its first provider's key (OpenAI's, here).
func (s *Server) agentListLocked() api.AgentList {
	out := api.AgentList{Object: "list", Data: []api.Agent{}, Allowed: s.HeldTo}
	held := func(model string) bool { return s.HeldTo == nil || slices.Contains(s.HeldTo.Models, model) }
	if every, ok := s.chosen["*"]; ok && held(every) {
		out.EveryAgent = &api.EveryAgent{Model: every, ChosenBy: "you"}
	}
	for _, a := range s.Agents {
		v := a
		v.State = "default"
		first := a.Suppliers[0]
		if first.Default != nil {
			on := first.ID + "/" + *first.Default
			v.StartsOn = &on
		} else {
			v.State = "own"
		}
		taken := false
		for _, who := range []string{a.Name, "*"} {
			model, ok := s.chosen[who]
			if !ok || !uses(&a, strings.SplitN(model, "/", 2)[0]) || !held(model) {
				continue
			}
			by := "you"
			if who == "*" {
				by = "every_agent"
			}
			v.Model, v.ChosenBy, v.StartsOn, v.State, taken = &model, &by, &model, "chosen", true
			break
		}
		// limited to a list, with no choice it holds: the list's first model the agent can use, else none
		if s.HeldTo != nil && !taken {
			v.State = "not_allowed"
			for _, model := range s.HeldTo.Models {
				if uses(&a, strings.SplitN(model, "/", 2)[0]) {
					by := "allowed"
					v.Model, v.ChosenBy, v.StartsOn, v.State = &model, &by, &model, "chosen"
					break
				}
			}
			if v.State == "not_allowed" {
				note := "None of the models you may use works with " + a.Title + "."
				v.Note = &note
			}
		}
		out.Data = append(out.Data, v)
	}
	return out
}

func (s *Server) chooseModel(every bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model *string `json:"model"`
		}
		if !decode(w, r, &in, false) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if in.Model == nil || !slices.ContainsFunc(s.Models.Data, func(m api.Model) bool { return m.ID == *in.Model }) {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the provider does not list that model for your key"})
			return
		}
		if s.HeldTo != nil && !slices.Contains(s.HeldTo.Models, *in.Model) {
			refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: *in.Model + " is not one of the models you may use: " + strings.Join(s.HeldTo.Models, ", ")})
			return
		}
		if every {
			for k := range s.chosen {
				delete(s.chosen, k)
			}
			s.chosen["*"] = *in.Model
			reply(w, 200, s.agentListLocked())
			return
		}
		name := r.PathValue("agent")
		a := s.agentLocked(name)
		if a == nil {
			refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such agent"})
			return
		}
		if !uses(a, strings.SplitN(*in.Model, "/", 2)[0]) {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: a.Title + " cannot use that provider's models"})
			return
		}
		s.chosen[name] = *in.Model
		for _, v := range s.agentListLocked().Data {
			if v.Name == name {
				reply(w, 200, v)
			}
		}
	}
}

func (s *Server) clearModel(every bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if every {
			delete(s.chosen, "*")
		} else {
			delete(s.chosen, r.PathValue("agent"))
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ChangeModels changes what GET /v1/models answers, as its providers' lists come to stand.
func (s *Server) ChangeModels(change func(*api.ModelList)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.Models)
}

// The lists of allowed models are an organisation's owners' and admins' to keep.
func (s *Server) keepsLists(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case s.Org == nil:
		refuse(w, r, Refusal{Status: 409, Code: "conflict", Detail: "lists of the models people may use are an organisation's: a person's own account keeps none"})
	case s.Org.Role != "owner" && s.Org.Role != "admin":
		refuse(w, r, Refusal{Status: 403, Code: "forbidden", Detail: "the models people may use are set by the organisation's owners and admins"})
	default:
		return true
	}
	return false
}

// The level and id of the list a route names, as the API reads them.
func allowedAt(r *http.Request) (level, id string) {
	switch {
	case r.PathValue("project_id") != "":
		return api.AllowTeam, r.PathValue("project_id")
	case r.PathValue("role") != "":
		return api.AllowRole, r.PathValue("role")
	case r.PathValue("person_id") != "":
		return api.AllowPerson, r.PathValue("person_id")
	}
	return api.AllowOrganisation, ""
}

func (s *Server) listAllowed(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keepsLists(w, r) {
		return
	}
	reply(w, 200, api.AllowedModelsList{Object: "list", Data: append([]api.AllowedModels{}, s.Allowed...)})
}

func (s *Server) setAllowed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Models []string `json:"models"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keepsLists(w, r) {
		return
	}
	level, id := allowedAt(r)
	if level == api.AllowRole && !slices.Contains([]string{"member", "approver", "manager", "full"}, id) {
		refuse(w, r, Refusal{Status: 404, Code: "not_found", Detail: "no such role"})
		return
	}
	if len(in.Models) == 0 || len(in.Models) > 10 {
		refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "name the models people may use in `models`, one to 10"})
		return
	}
	for _, m := range in.Models {
		if !slices.ContainsFunc(s.Models.Data, func(x api.Model) bool { return x.ID == m }) {
			refuse(w, r, Refusal{Status: 400, Code: "invalid_request", Detail: "the provider does not list " + m + " for the organisation's key"})
			return
		}
	}
	name := map[string]string{api.AllowOrganisation: s.Org.Name, api.AllowRole: map[string]string{"member": "Members", "approver": "Approvers",
		"manager": "Team managers", "full": "Full managers"}[id]}[level]
	if level == api.AllowOrganisation {
		id = s.Org.ID
	}
	l := api.AllowedModels{Object: "allowed_models", Level: level, ID: id, Name: name, Models: in.Models,
		SetBy: &api.PersonName{ID: s.Org.Person.ID, Name: s.Org.Person.Name}, SetAt: time.Now().UTC().Truncate(time.Second)}
	s.Allowed = slices.DeleteFunc(s.Allowed, func(x api.AllowedModels) bool { return x.Level == level && x.ID == id })
	s.Allowed = append(s.Allowed, l)
	reply(w, 200, l)
}

func (s *Server) clearAllowed(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keepsLists(w, r) {
		return
	}
	level, id := allowedAt(r)
	if level == api.AllowOrganisation {
		id = s.Org.ID
	}
	s.Allowed = slices.DeleteFunc(s.Allowed, func(x api.AllowedModels) bool { return x.Level == level && x.ID == id })
	w.WriteHeader(http.StatusNoContent)
}
