package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

// wm machines new: machines of one of a team's types - for yourself, or for others, several at once - each answered
// on its own. wm machines types: a team's types.

func (a *app) machinesNew(ctx context.Context, args []string) error {
	fs := a.flags("machines new", "wm machines new --type <type> [--for @ada,lin@example.com | --everyone] [flags]",
		"Make machines of one of a team's types: for yourself, or - as an owner, an admin or the team's manager - for others, several at "+
			"once. Each is its person's from the start; you watch it, and may take it back. Each is said on a line of its own: made, or why not.")
	kind := fs.String("type", "", "the machine type, by name or id (wm machines types)")
	team := fs.String("team", "", "its team; the account's first when left out")
	var people listFlag
	fs.Var(&people, "for", "whom for: @handle, an e-mail address or a person's id; repeat, or join with commas")
	everyone := fs.Bool("everyone", false, "for everyone in the team")
	each := fs.Int("each", 1, "how many for each person, 1 to 10")
	names := fs.String("names", "", "the `pattern` of their names, of <type>, <handle> and <n> (<type>-<handle> when left out)")
	region := fs.String("region", "", "a region to run near (wm regions)")
	labels := labelFlag{}
	fs.Var(labels, "label", "a `key=value` label for each; repeat for more")
	w := waitFlags(fs)
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	switch {
	case *kind == "":
		return usagef("say which type: --type <type> (wm machines types lists them)")
	case *everyone && len(people) > 0:
		return usagef("--for or --everyone, not both")
	case *each < 1 || *each > 10:
		return usagef("--each is from 1 to 10")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*w.timeout)
	b := api.MachineBatchCreate{Type: *kind, People: people, Everyone: *everyone, Each: *each, Names: *names, Region: *region, WaitS: api.Ptr(0.0)}
	if len(labels) > 0 {
		b.Labels = labels
	}
	if *team != "" {
		if b.Project, err = c.FindProject(ctx, *team); err != nil {
			return err
		}
	}
	out, err := c.CreateMachines(ctx, b)
	if err != nil {
		return err
	}
	// those on their way waited for, in turn, unless --no-wait
	for i, m := range out.Data {
		if m.Machine == nil || m.Machine.State != api.StateStarting || *w.noWait {
			continue
		}
		ready, err := a.waitFor(ctx, c, m.Machine.ID, api.StateRunning, deadline)
		if ready != nil {
			out.Data[i].Machine = ready
		}
		if err != nil && ready == nil {
			return err
		}
	}
	switch {
	case a.json:
		if err := a.printJSON(out); err != nil {
			return err
		}
	case a.quiet:
		for _, m := range out.Data {
			if m.Machine != nil {
				fmt.Fprintln(a.stdout, m.Machine.ID)
			}
		}
	default:
		t := a.table("NAME", "ID", "FOR", "STATE")
		for _, m := range out.Data {
			who := or(m.Person.Name, m.Person.ID)
			if m.Machine == nil {
				why := "not made"
				if m.Err != nil && m.Err.Detail != "" {
					why += ": " + m.Err.Detail
				}
				t.row(m.Name, "-", who, why)
				continue
			}
			t.row(m.Name, m.Machine.ID, who, m.Machine.State)
		}
		if err := t.flush(); err != nil {
			return err
		}
	}
	if out.Refused > 0 {
		return fmt.Errorf("%d of %d machines were not made", out.Refused, len(out.Data))
	}
	return nil
}

func (a *app) machinesTypes(ctx context.Context, args []string) error {
	fs := a.flags("machines types", "wm machines types [--team <team>]", "List a team's machine types: what a machine made from each is. "+
		"Its owners and admins save them on the dashboard's team page.")
	team := fs.String("team", "", "the team; the account's first when left out")
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
	id := ""
	if *team != "" {
		if id, err = c.FindProject(ctx, *team); err != nil {
			return err
		}
	} else {
		for p, err := range c.AllProjects(ctx, api.ListParams{Limit: 1}) {
			if err != nil {
				return err
			}
			id = p.ID
			break
		}
	}
	kinds, err := c.ListMachineTypes(ctx, id)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(kinds)
	}
	if a.quiet {
		for _, k := range kinds {
			fmt.Fprintln(a.stdout, k.ID)
		}
		return nil
	}
	t := a.table("NAME", "SIZE", "INTERNET", "SLEEPS", "ARCHIVES", "APPS")
	for _, k := range kinds {
		size := "GPU"
		switch {
		case k.GPU != nil:
			size = *k.GPU
		case k.CPUs != nil:
			size = strconv.Itoa(*k.CPUs) + " CPUs"
		}
		sleeps, away, apps := "never", "-", "as new"
		if k.IdleAfterS != nil {
			sleeps = "after " + (time.Duration(*k.IdleAfterS) * time.Second).String()
		}
		if k.PutAwayAfterS != nil {
			away = "after " + (time.Duration(*k.PutAwayAfterS) * time.Second).String()
		}
		if k.Apps != nil {
			apps = strings.Join(k.Apps, ",")
		}
		t.row(k.Name, size, k.Network.Internet, sleeps, away, apps)
	}
	return t.flush()
}
