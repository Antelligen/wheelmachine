package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"wheelmachine/frontend/cli/api"
)

// wm models: the models the account's AI keys can use, as each model's provider lists them for the key, and the model each
// agent in the account's machines starts on - chosen for one agent, or one for every agent. A choice reaches
// running machines at once, and the others when they next start; an agent already running uses it from its
// next start.

var modelCommands = map[string]subcommand{
	"chosen":  (*app).modelsChosen,
	"use":     (*app).modelsUse,
	"reset":   (*app).modelsReset,
	"allowed": (*app).modelsAllowed,
	"allow":   (*app).modelsAllow,
}

// models runs a command of wm models, or lists the models when it is given none - only flags.
func (a *app) models(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "-help", "help":
			fmt.Fprint(a.stdout, modelsHelp)
			return nil
		}
		if run, ok := modelCommands[args[0]]; ok {
			return run(a, ctx, args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			return usagef("no command %q in wm models: see wm models -h", args[0])
		}
	}
	return a.modelsList(ctx, args)
}

func (a *app) modelsList(ctx context.Context, args []string) error {
	fs := a.flags("models", "wm models [--agent NAME] [--refresh]", "List the models your AI keys can use, as each model's provider "+
		"lists them for the key, grouped by provider in the order agents choose between keys - with --agent, those of the providers "+
		"that agent can use - each with its provider's list price and its score on LMArena's text leaderboard, where they are "+
		"known. A provider that refused its key, turned the request away or did not answer is said under them.")
	agent := fs.String("agent", "", "only the models this agent can use (wm models chosen lists the agents)")
	refresh := fs.Bool("refresh", false, "read each list again from its provider, when it was read more than a minute ago")
	team := fs.String("team", "", "in an organisation that keeps lists of allowed models, the team whose list applies to you; left out, your one team")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Models(ctx, api.ModelsQuery{Agent: *agent, Refresh: *refresh, Team: *team})
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, m := range list.Data {
			fmt.Fprintln(a.stdout, m.ID)
		}
		return nil
	}
	if len(list.Suppliers) == 0 {
		fmt.Fprintln(a.stdout, "No AI key yet: set one with wm secrets set OPENAI_API_KEY (or another provider's).")
		return nil
	}
	t := a.table("MODEL", "NAME", "PRICE", "SCORE")
	var priced bool
	var scored *api.ModelIntelligence
	for _, m := range list.Data {
		name := m.Name
		if m.RetiringAt != nil {
			name += " (retiring " + m.RetiringAt.Format("2006-01-02") + ")"
		}
		price, score := "-", "-"
		if p := m.Price; p != nil {
			price, priced = dollars(p.InputPerMillionUSD)+" / "+dollars(p.OutputPerMillionUSD), true
		}
		if s := m.Intelligence; s != nil {
			score, scored = strconv.FormatFloat(s.Score, 'f', -1, 64), s
		}
		t.row(m.ID, name, price, score)
	}
	if err := t.flush(); err != nil {
		return err
	}
	for _, s := range list.Suppliers {
		if s.State != "listed" && s.Detail != nil {
			fmt.Fprintln(a.stderr, *s.Detail)
		}
	}
	if list.Allowed != nil {
		fmt.Fprintf(a.stderr, "You may use these models alone, as %s says.\n", allowedWhose(list.Allowed))
	}
	// what the prices and the scores are, and whose: under the list, never in it
	if priced {
		fmt.Fprintln(a.stderr, "Price: a million tokens in / out, each provider's own list price. Wheelmachine charges nothing for it.")
	}
	if scored != nil {
		fmt.Fprintf(a.stderr, "Score: %s, %s (%s), %s\n", scored.Source, scored.AsOf, scored.License, scored.URL)
	}
	return nil
}

// An amount of dollars as wm says it: $2, $0.3, $1.32.
func dollars(v float64) string { return "$" + strconv.FormatFloat(v, 'f', -1, 64) }

// The words for how an agent stands, as wm says them.
var agentStates = map[string]string{"chosen": "as chosen", "default": "its default", "own": "its own model", "no_key": "no key it can use",
	"needs_key": "key missing", "cannot_use": "cannot use it", "retired": "model retired", "not_allowed": "no model allowed"}

// Whose a list of allowed models is, as a sentence says it.
func allowedWhose(l *api.AllowedModels) string {
	switch l.Level {
	case api.AllowOrganisation:
		return "your organisation's list"
	case api.AllowTeam:
		return "the list of your team, " + or2(l.Name, l.ID)
	case api.AllowRole:
		return "the list for " + strings.ToLower(or2(l.Name, l.ID))
	}
	return "your own list"
}

func (a *app) modelsChosen(ctx context.Context, args []string) error {
	fs := a.flags("models chosen", "wm models chosen [--team PROJECT]", "List the agents of your machines, the model chosen for "+
		"each - for it, or for every agent - and what it starts on with your keys. Where an agent cannot take the model "+
		"chosen for it, it says why and starts on its default.")
	team := fs.String("team", "", "in an organisation, the team whose choices these are; left out, your one team")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Agents(ctx, *team)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, ag := range list.Data {
			fmt.Fprintln(a.stdout, ag.Name)
		}
		return nil
	}
	if list.EveryAgent != nil {
		fmt.Fprintf(a.stdout, "Every agent: %s\n", list.EveryAgent.Model)
	}
	if list.Allowed != nil {
		fmt.Fprintf(a.stdout, "You may use %s, as %s says.\n", wordsAnd(list.Allowed.Models), allowedWhose(list.Allowed))
	}
	if len(list.Data) == 0 {
		fmt.Fprintln(a.stdout, "No agents yet: they show once one of your machines has said them (wm apps ls <machine>).")
		return nil
	}
	t := a.table("AGENT", "CHOSEN", "STARTS ON", "STATE")
	for _, ag := range list.Data {
		chosen := "-"
		if ag.Model != nil {
			chosen = *ag.Model
			switch {
			case ag.ChosenBy == nil:
			case *ag.ChosenBy == "every_agent":
				chosen += " (every agent)"
			case *ag.ChosenBy == "allowed":
				chosen += " (first allowed)"
			}
		}
		on := "-"
		if ag.StartsOn != nil {
			on = *ag.StartsOn
		}
		t.row(ag.Name, chosen, on, or2(agentStates[ag.State], ag.State))
	}
	if err := t.flush(); err != nil {
		return err
	}
	for _, ag := range list.Data {
		if ag.Note != nil {
			fmt.Fprintln(a.stdout, *ag.Note)
		}
	}
	return nil
}

func or2(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (a *app) modelsUse(ctx context.Context, args []string) error {
	fs := a.flags("models use", "wm models use <agent> <model> | --every <model> [--team PROJECT]", "Choose the model an agent "+
		"starts on, as wm models lists its id (openai/gpt-6.1-sol) - or, with --every, one model for every agent, which takes "+
		"the place of each agent's own. It reaches your running machines at once, and the others when they next "+
		"start; an agent already running uses it from its next start.")
	every := fs.Bool("every", false, "one model for every agent: each that can use its provider starts on it")
	team := fs.String("team", "", "in an organisation, the team the choice is for; left out, your one team")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	want, what := 2, "which agent, and which model (wm models use codex openai/gpt-6.1-sol)"
	if *every {
		want, what = 1, "which model, as wm models lists it"
	}
	if err := count(words, want, want, what); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if *every {
		list, err := c.ChooseEveryAgentModel(ctx, words[0], *team)
		if err != nil {
			return err
		}
		switch {
		case a.json:
			return a.printJSON(list)
		case a.quiet:
			fmt.Fprintln(a.stdout, words[0])
			return nil
		}
		fmt.Fprintf(a.stdout, "Every agent that can use it starts on %s from its next start.\n", words[0])
		for _, ag := range list.Data {
			if ag.Note != nil {
				fmt.Fprintln(a.stdout, *ag.Note)
			}
		}
		return nil
	}
	ag, err := c.ChooseAgentModel(ctx, words[0], words[1], *team)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(ag)
	case a.quiet:
		fmt.Fprintln(a.stdout, words[1])
		return nil
	}
	fmt.Fprintf(a.stdout, "%s starts on %s from its next start.\n", ag.Title, words[1])
	if ag.Note != nil {
		fmt.Fprintln(a.stdout, *ag.Note)
	}
	return nil
}

func (a *app) modelsReset(ctx context.Context, args []string) error {
	fs := a.flags("models reset", "wm models reset <agent> | --every [--team PROJECT]", "Take an agent's choice away: it starts on the "+
		"model chosen for every agent where it can use it, else on its default. With --every, take the one model for every "+
		"agent away.")
	every := fs.Bool("every", false, "take the one model for every agent away")
	team := fs.String("team", "", "in an organisation, the team the choice is for; left out, your one team")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *every {
		if err := count(words, 0, 0, ""); err != nil {
			return err
		}
	} else if err := count(words, 1, 1, "which agent"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if *every {
		if err := c.ClearEveryAgentModel(ctx, *team); err != nil {
			return err
		}
		if !a.quiet && !a.json {
			fmt.Fprintln(a.stdout, "No one model for every agent: each starts on its own choice, else its default, from its next start.")
		}
		return nil
	}
	if err := c.ClearAgentModel(ctx, words[0], *team); err != nil {
		return err
	}
	if !a.quiet && !a.json {
		fmt.Fprintf(a.stdout, "%s starts on the model for every agent where it can, else its default, from its next start.\n", words[0])
	}
	return nil
}

// Words joined as a sentence lists them: "a", "a and b", "a, b and c".
func wordsAnd(words []string) string {
	switch len(words) {
	case 0:
		return "none"
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// The level a list is named at on the command line: --org, --team, --role or --person, one alone.
func allowedLevel(org bool, team, role, person string) (level, id string, err error) {
	var named []string
	if org {
		named, level = append(named, "--org"), api.AllowOrganisation
	}
	for _, at := range []struct{ flag, level, id string }{{"--team", api.AllowTeam, team}, {"--role", api.AllowRole, role}, {"--person", api.AllowPerson, person}} {
		if at.id != "" {
			named, level, id = append(named, at.flag), at.level, at.id
		}
	}
	if len(named) != 1 {
		return "", "", usagef("name one list: --org, --team <project>, --role <role> or --person <person>")
	}
	return level, id, nil
}

func (a *app) modelsAllowed(ctx context.Context, args []string) error {
	fs := a.flags("models allowed", "wm models allowed", "List the lists of the models your organisation's people may use, "+
		"as its owners and admins keep them: the organisation's, each team's, each role's and each person's. A person's "+
		"choices are limited to the most specific list they have - their own, their role's in the team, their team's, the "+
		"organisation's - and with none to every model. Owners and admins are never limited to one.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	lists, err := c.AllowedModels(ctx)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(lists)
	case a.quiet:
		for _, l := range lists.Data {
			fmt.Fprintln(a.stdout, l.Level, l.ID)
		}
		return nil
	}
	if len(lists.Data) == 0 {
		fmt.Fprintln(a.stdout, "No lists: everyone may use every model. Set one with wm models allow --org <model>...")
		return nil
	}
	t := a.table("LIST", "FOR", "MODELS")
	for _, l := range lists.Data {
		t.row(l.Level, or2(l.Name, l.ID), strings.Join(l.Models, ", "))
	}
	return t.flush()
}

func (a *app) modelsAllow(ctx context.Context, args []string) error {
	fs := a.flags("models allow", "wm models allow (--org | --team PROJECT | --role ROLE | --person PERSON) <model>... | --any",
		"Set the models people may use, one to ten as wm models lists their ids, in order: the first is the one an agent "+
			"starts on where its person chose none of them. --org for everyone in the organisation, --team for a team's "+
			"people, --role for everyone of a level in their team (member, approver, manager or full), --person for one "+
			"person. A choice a person made that is not on the list is set aside: their agent starts on the list's "+
			"first model it can use. With --any, take the list away. For owners and admins, who are never limited to a list.")
	org := fs.Bool("org", false, "the organisation's list, for everyone without a list of their team, role or own")
	team := fs.String("team", "", "a team's list, by its project's id")
	role := fs.String("role", "", "a role's list: member, approver, manager or full - a person's level in the team")
	person := fs.String("person", "", "one person's list, by their id")
	anyModel := fs.Bool("any", false, "take the list away: those it applied to may use every model again, unless another list applies to them")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	level, id, err := allowedLevel(*org, *team, *role, *person)
	if err != nil {
		return err
	}
	if *anyModel {
		if err := count(words, 0, 0, ""); err != nil {
			return err
		}
	} else if err := count(words, 1, 10, "which models, one to ten, as wm models lists them"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if *anyModel {
		if err := c.ClearAllowedModels(ctx, level, id); err != nil {
			return err
		}
		if !a.quiet && !a.json {
			fmt.Fprintln(a.stdout, "List taken away: those it applied to may use every model again, unless another list applies to them.")
		}
		return nil
	}
	l, err := c.AllowModels(ctx, level, id, words)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(l)
	case a.quiet:
		fmt.Fprintln(a.stdout, l.Level, l.ID)
		return nil
	}
	who := map[string]string{api.AllowOrganisation: "Everyone in " + or2(l.Name, "the organisation"), api.AllowTeam: "The people of " + or2(l.Name, l.ID),
		api.AllowRole: or2(l.Name, l.ID), api.AllowPerson: or2(l.Name, l.ID)}[l.Level]
	fmt.Fprintf(a.stdout, "%s may use %s.\n", who, wordsAnd(l.Models))
	return nil
}
