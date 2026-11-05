package main

import (
	"context"
	"fmt"
	"time"

	"wheelmachine/frontend/cli/api"
)

var requestCommands = map[string]subcommand{
	"ls":        (*app).requestsList,
	"ask":       (*app).requestsAsk,
	"take-back": (*app).requestsTakeBack,
}

// wm requests: the requests to fork a machine - a member's own fork, or one for someone - or to hand it off, as a list,
// and asked or withdrawn by its words. Approve and decline are a person's, signed in to the dashboard.
func (a *app) requests(ctx context.Context, args []string) error {
	if len(args) == 0 || len(args[0]) > 0 && args[0][0] == '-' && args[0] != "-h" && args[0] != "--help" && args[0] != "-help" {
		return a.requestsList(ctx, args)
	}
	return a.group(ctx, "requests", requestsHelp, requestCommands, args)
}

func (a *app) requestsList(ctx context.Context, args []string) error {
	fs := a.flags("requests", "wm requests [--all]", "The requests you asked, of machines you hold, coming to you, and those you decide: "+
		"those still open, or with --all every one, the newest first. Approve and decline them signed in to the dashboard.")
	all := fs.Bool("all", false, "every request, not only those still open")
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
	list, err := c.ListRequests(ctx, !*all, api.ListParams{Limit: 200})
	if err != nil {
		return err
	}
	return a.printRequests(list.Data)
}

func (a *app) printRequests(list []api.Request) error {
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, q := range list {
			fmt.Fprintln(a.stdout, q.ID)
		}
		return nil
	}
	now := a.now()
	t := a.table("ID", "WHAT", "MACHINE", "ASKED BY", "FOR", "STATE", "AGE")
	for _, q := range list {
		what := "fork"
		if q.Kind == api.RequestHandOff {
			what = "hand-off"
		}
		switch {
		case q.Way == "ask":
			what = "asks for a " + what
		case q.Way == "back" && q.AskedBy.ID == q.Receiver.ID:
			what = "take-back"
		case q.Way == "back":
			what = "give-back"
		}
		machine := "-"
		if q.Machine != nil {
			machine = q.Machine.Name
		}
		t.row(q.ID, what, machine, q.AskedBy.Named(), q.Receiver.Named(), q.StateWords(), age(q.CreatedAt, now))
	}
	return t.flush()
}

func (a *app) printRequest(q *api.Request) error {
	switch {
	case a.json:
		return a.printJSON(q)
	case a.quiet:
		fmt.Fprintln(a.stdout, q.ID)
		return nil
	}
	if err := a.printRequests([]api.Request{*q}); err != nil {
		return err
	}
	if q.Backup != nil {
		// (a fork from one of the machine's backups: made from its files, at the size asked or the original's)
		size := "the original's size"
		if q.CPUs != nil {
			size = fmt.Sprintf("%d CPU, %d GB", *q.CPUs, 2**q.CPUs)
		}
		fmt.Fprintln(a.stdout, "from the backup "+*q.Backup+", at "+size)
	}
	if q.Failure != nil {
		fmt.Fprintln(a.stdout, *q.Failure)
	}
	if q.Result != nil {
		fmt.Fprintln(a.stdout, "the fork:", *q.Result)
	}
	if q.KeysRevoked > 0 {
		fmt.Fprintln(a.stdout, "Wheelmachine keys found in it and revoked:", q.KeysRevoked)
	}
	if q.State == "waiting" && q.WaitingOn != nil && q.WaitingOn.Who == "asker" {
		fmt.Fprintln(a.stdout, "made with a key, it waits for your own OK: approve it signed in to the dashboard, https://app.wheelmachine.ai/requests")
	}
	if q.State == "waiting" && q.Kind == api.RequestFork && q.Way == "give" && q.Holder.ID == q.Receiver.ID {
		// (a member's fork of a machine they hold, as wm machines fork asks it: nothing is made before it is approved)
		fmt.Fprintln(a.stdout, "the fork waits for the OK of a full manager of its team, an owner or an admin, and is made once they approve it: "+
			"follow it with wm requests")
	}
	return nil
}

func (a *app) requestsAsk(ctx context.Context, args []string) error {
	fs := a.flags("requests ask", "wm requests ask fork|hand-off --of <person> [--why <words>]", "Ask a teammate for a fork of one of "+
		"their machines, or for one of them. Asked with wm's key, it waits for your own OK in the dashboard first; then they pick "+
		"which as they approve, and what the team's rules need follows.")
	of := fs.String("of", "", "the teammate whose machine: their @handle, id, e-mail address or GitHub login")
	why := fs.String("why", "", "why, in your own words, at most 280 characters")
	team := fs.String("team", "", "the team of yours it goes into, when it is another than the machine's")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "fork or hand-off"); err != nil {
		return err
	}
	kind := ""
	switch words[0] {
	case "fork":
		kind = api.RequestFork
	case "hand-off":
		kind = api.RequestHandOff
	default:
		return usagef("ask for a fork or a hand-off, not %q", words[0])
	}
	if *of == "" {
		return usagef("say whose machine: --of <person>")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ask := api.RequestAsk{Kind: kind, Of: *of, Why: *why}
	if *team != "" {
		if ask.Team, err = c.FindProject(ctx, *team); err != nil {
			return err
		}
	}
	q, err := c.AskRequest(ctx, ask)
	if err != nil {
		return err
	}
	return a.printRequest(q)
}

func (a *app) requestsTakeBack(ctx context.Context, args []string) error {
	fs := a.flags("requests take-back", "wm requests take-back <request>", "Withdraw a request you asked, while it waits.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which request"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	q, err := c.TakeBackRequest(ctx, words[0])
	if err != nil {
		return err
	}
	return a.printRequest(q)
}

// A machine given to a teammate - forked for them, or handed off - with the words `route` says it in.
func (a *app) give(ctx context.Context, machine, to, team, why string, fork bool) error {
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	g := api.GiveTo{To: to, Why: why}
	if team != "" {
		if g.Team, err = c.FindProject(ctx, team); err != nil {
			return err
		}
	}
	var q *api.Request
	if fork {
		q, err = c.ForkMachineFor(ctx, id, g)
	} else {
		q, err = c.HandOffMachine(ctx, id, g)
	}
	if err != nil {
		return err
	}
	return a.printRequest(q)
}

func (a *app) machinesHandOff(ctx context.Context, args []string) error {
	fs := a.flags("machines hand-off", "wm machines hand-off <machine> --to <person> [--team <project>] [--why <words>]", "Hand a "+
		"machine you made on to a teammate, exactly as it is: its programs, terminals, files and every sign-in in it, its "+
		"browsers' too - so whoever holds it can sign in to Wheelmachine as you through GitHub or Google there: sign out of them "+
		"first if that is not what you want. Your Wheelmachine keys found in it are revoked, and your sign-ins to the dashboard in "+
		"it end. It arrives paused, its memory kept; you watch it, and take it back whenever you choose (wm machines take-back). "+
		"Given with wm's key, it waits for your own OK in the dashboard first; then what the team's rules need follows (wm requests).")
	to := fs.String("to", "", "the teammate: their @handle, id, e-mail address or GitHub login")
	team := fs.String("team", "", "the team of theirs it goes into, when it is another than its own")
	why := fs.String("why", "", "why, in your own words, at most 280 characters")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine to hand off"); err != nil {
		return err
	}
	if *to == "" {
		return usagef("say to whom: --to <person>")
	}
	return a.give(ctx, words[0], *to, *team, *why, false)
}

// A machine handed off, back with the one who handed it off: taken back by them, or given back by the person who holds
// it, with the words `taking` says it in.
func (a *app) back(ctx context.Context, args []string, taking bool) error {
	name, usage, about := "machines give-back", "wm machines give-back <machine> [--team <project>]", "Give a machine handed to "+
		"you back to the person who handed it off, exactly as it is: what you put in it goes back with it, your sign-ins in it "+
		"too - but for your Wheelmachine keys found in it, revoked, and your sign-ins to the dashboard in it, ended. A machine "+
		"handed to you is given back, never handed off again. Given with wm's key, it waits for your own OK in the dashboard first."
	if taking {
		name, usage, about = "machines take-back", "wm machines take-back <machine> [--team <project>]", "Take back a machine "+
			"you handed off, whenever you choose, with nobody's OK: exactly as it is, what its holder put in it with it, their "+
			"sign-ins in it too - but for their Wheelmachine keys found in it, revoked, and their sign-ins to the dashboard in it, "+
			"ended. It arrives paused, its memory kept. Given with wm's key, it waits for your own OK in the dashboard first."
	}
	fs := a.flags(name, usage, about)
	team := fs.String("team", "", "the team it goes back into, when it should be another than where it is or was handed off from")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	what := "which machine to give back"
	if taking {
		what = "which machine to take back"
	}
	if err := count(words, 1, 1, what); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	var b api.BackTo
	if *team != "" {
		if b.Team, err = c.FindProject(ctx, *team); err != nil {
			return err
		}
	}
	var q *api.Request
	if taking {
		q, err = c.TakeBackMachine(ctx, id, b)
	} else {
		q, err = c.GiveBackMachine(ctx, id, b)
	}
	if err != nil {
		return err
	}
	return a.printRequest(q)
}

func (a *app) machinesTakeBack(ctx context.Context, args []string) error {
	return a.back(ctx, args, true)
}

func (a *app) machinesGiveBack(ctx context.Context, args []string) error {
	return a.back(ctx, args, false)
}

func (a *app) projectsRules(ctx context.Context, args []string) error {
	fs := a.flags("projects rules", "wm projects rules <project>", "A team's rules: hand-offs Free, Ask or Off; forks Ask - every "+
		"member's fork waits for the OK of a full manager of the team, an owner or an admin - or Off; GPUs and open internet on or off; "+
		"its budget for the month; the largest machine it holds; the longest its machines stay up idle; and whether their ports "+
		"may be public (on), only shared by link (links_only) or neither (off). Set them signed in to the dashboard.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which team"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	id, err := c.FindProject(ctx, words[0])
	if err != nil {
		return err
	}
	r, err := c.GetTeamRules(ctx, id)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(r)
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	budget := "none"
	if r.MonthBudgetUSD != nil {
		budget = fmt.Sprintf("$%.2f", *r.MonthBudgetUSD)
	}
	largest, idle, ports := "none", "none", r.PublicPorts
	if r.MaxMachineVCPUs != nil {
		largest = fmt.Sprintf("%d CPUs", *r.MaxMachineVCPUs)
	}
	if r.IdleMaxSeconds != nil {
		idle = (time.Duration(*r.IdleMaxSeconds) * time.Second).String()
	}
	if ports == "" {
		ports = "on"
	}
	if r.SpentUSD == nil {
		// (a member of the team: its rules, and what its machines came to is its managers' to see)
		t := a.table("HAND-OFFS", "FORKS", "GPUS", "OPEN INTERNET", "BUDGET", "LARGEST", "IDLE STOP", "PUBLIC PORTS", "TYPES ONLY")
		t.row(r.HandOffs, r.Forks, onOff(r.GPUs), onOff(r.OpenInternet), budget, largest, idle, ports, onOff(r.TypesOnly))
		return t.flush()
	}
	t := a.table("HAND-OFFS", "FORKS", "GPUS", "OPEN INTERNET", "BUDGET", "SPENT", "LARGEST", "IDLE STOP", "PUBLIC PORTS", "TYPES ONLY")
	t.row(r.HandOffs, r.Forks, onOff(r.GPUs), onOff(r.OpenInternet), budget, fmt.Sprintf("$%.2f", *r.SpentUSD), largest, idle, ports, onOff(r.TypesOnly))
	return t.flush()
}
