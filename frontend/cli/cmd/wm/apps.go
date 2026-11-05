package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

var appCommands = map[string]subcommand{
	"ls":   (*app).appsList,
	"on":   (*app).appsOn,
	"off":  (*app).appsOff,
	"show": (*app).appsShow,
	"open": (*app).appsOpen,
}

// appState is an app's state in a word or two; a terminal agent has none of its own: it runs while it is typed. An
// agent that is on is offered, and runs once its session is opened; one whose session began before the machine's AI
// keys changed keeps the ones it began with, and says what puts that right.
func appState(p api.App) string {
	switch {
	case p.Kind == "terminal" || p.On == nil:
		return ""
	case p.Kind == "agent" && *p.On && changed(p.KeysChanged) && changed(p.ModelChanged):
		return "on, restart to use new keys and model"
	case p.Kind == "agent" && *p.On && changed(p.KeysChanged):
		return "on, restart to use new keys"
	case p.Kind == "agent" && *p.On && changed(p.ModelChanged):
		return "on, restart to use the new model"
	case p.Kind == "agent" && *p.On && p.Running != nil && *p.Running:
		return "on, running"
	case p.Kind == "agent" && *p.On:
		return "on"
	case *p.On && p.Running != nil && !*p.Running:
		return "on, not running yet"
	case *p.On:
		return "on"
	}
	return "off"
}

func changed(b *bool) bool { return b != nil && *b }

// appModel is what an app last started on, as a machine made since models were chosen says it: its provider and model,
// "-" for none yet, or "own" for one it picks itself.
func appModel(p api.App) string {
	switch {
	case p.Model != nil:
		return *p.Model
	case p.ModelBy != nil && *p.ModelBy == "agent":
		return "own"
	}
	return "-"
}

// appWhere is where an app is reached: a web app at its private address, a service inside the machine, an agent in a
// session of its own there, and a terminal agent by typing its command in the machine's terminal.
func appWhere(p api.App, machine string) string {
	switch {
	case p.Kind == "web" && p.URL != nil:
		return *p.URL
	case p.Kind == "agent":
		return "a session of its own in the machine"
	case p.Kind == "terminal" && p.Command != nil:
		return "type " + *p.Command + " in the machine's terminal (wm ssh " + machine + ")"
	}
	return "inside the machine"
}

// appArgs reads `<machine> <app>` and the flags every command of apps takes.
func appArgs(words []string) (string, string, error) {
	if err := count(words, 2, 2, "which machine, and which app"); err != nil {
		return "", "", err
	}
	if !api.ValidAppName(words[1]) {
		return "", "", usagef("%q is not an app's name: wm apps ls <machine> lists them", words[1])
	}
	return words[0], words[1], nil
}

// accessFor is how long the browser that opens a link may stay, as --for says it: whole seconds from 1m to 60m.
func accessFor(keep time.Duration) (int, error) {
	if keep%time.Second != 0 || keep < time.Minute || keep > time.Hour {
		return 0, usagef("--for is whole seconds from 1m to 60m")
	}
	return int(keep / time.Second), nil
}

func (a *app) appsList(ctx context.Context, args []string) error {
	fs := a.flags("apps ls", "wm apps ls <machine>", "List the apps in a machine, whether each is on, and where it is reached: a "+
		"web app at its private address, a service inside the machine, an agent in a session of its own there. An agent "+
		"started before your AI keys changed says so: wm apps off and then on starts it again with them, in a new "+
		"session. The machine is asked, so a paused one is woken for it.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	apps, err := c.ListApps(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if apps == nil {
			apps = []api.App{}
		}
		return a.printJSON(apps)
	case a.quiet:
		for _, p := range apps {
			fmt.Fprintln(a.stdout, p.Name)
		}
		return nil
	}
	// a machine made since models were chosen says what each of its agents started on: a column of its own
	if slices.ContainsFunc(apps, func(p api.App) bool { return p.Suppliers != nil }) {
		t := a.table("NAME", "APP", "KIND", "STATE", "MODEL", "WHERE")
		for _, p := range apps {
			model := ""
			if p.Suppliers != nil {
				model = appModel(p)
			}
			t.row(p.Name, p.Title, p.Kind, appState(p), or2(model, "-"), appWhere(p, words[0]))
		}
		return t.flush()
	}
	t := a.table("NAME", "APP", "KIND", "STATE", "WHERE")
	for _, p := range apps {
		t.row(p.Name, p.Title, p.Kind, appState(p), appWhere(p, words[0]))
	}
	return t.flush()
}

func (a *app) appsOn(ctx context.Context, args []string) error {
	fs := a.flags("apps on", "wm apps on <machine> <app> [--for 30m]", "Turn an app on; it may take up to a minute. For a web "+
		"app, print a link into its private address that lands on its page: it opens once, within five minutes, and lets "+
		"the browser that opens it stay as long as --for says. An app with a token of its own prints it this once, after "+
		"the link.")
	keep := fs.Duration("for", 30*time.Minute, "how long the browser that opens the link may reach the app, 1m to 60m")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	machine, name, err := appArgs(words)
	if err != nil {
		return err
	}
	access, err := accessFor(*keep)
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	p, err := c.TurnAppOn(ctx, id, name, api.AppLinkCreate{AccessS: access})
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(p)
	}
	if p.Link != "" {
		fmt.Fprintln(a.stdout, p.Link)
	}
	if p.Token != "" {
		// shown this once, even with -q: the API keeps it nowhere
		fmt.Fprintln(a.stdout, "token: "+p.Token)
	}
	if a.quiet {
		return nil
	}
	switch {
	case p.Link != "":
		fmt.Fprintf(a.stderr, "wm: %s is on. The link opens once, within five minutes (until %s), and lets the browser that opens it "+
			"reach the app for %s. A new one: wm apps open %s %s\n", p.Title, linkEnd(p), span(float64(p.AccessS)), machine, name)
	case p.Kind == "service":
		fmt.Fprintf(a.stderr, "wm: %s is on, inside the machine.\n", p.Title)
	default:
		fmt.Fprintf(a.stderr, "wm: %s is on.\n", p.Title)
	}
	if p.Token != "" {
		fmt.Fprintf(a.stderr, "wm: the token is shown this once: its page asks for it. Keep it as you keep a password.\n")
	}
	return nil
}

func (a *app) appsOff(ctx context.Context, args []string) error {
	fs := a.flags("apps off", "wm apps off <machine> <app>", "Turn an app off, which frees the machine's memory. A web app's "+
		"address is closed with it when it was opened for the app and is still private - a port you opened yourself, or "+
		"made public, stays open - and an agent's session ends. The code editor is always on, and is not turned off.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	machine, name, err := appArgs(words)
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	p, err := c.TurnAppOff(ctx, id, name)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(p)
	}
	fmt.Fprintln(a.stdout, p.Name)
	if !a.quiet {
		fmt.Fprintf(a.stderr, "wm: %s is off.\n", p.Title)
	}
	return nil
}

func (a *app) appsShow(ctx context.Context, args []string) error {
	fs := a.flags("apps show", "wm apps show <machine> <app>", "Open an app on the machine's own screen - its desktop, which the "+
		"dashboard shows on the machine's Screen tab - signed in where it signs in; it may take up to two minutes.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	machine, name, err := appArgs(words)
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	p, err := c.ShowApp(ctx, id, name)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(p)
	}
	fmt.Fprintln(a.stdout, p.Name)
	if !a.quiet {
		fmt.Fprintf(a.stderr, "wm: %s is open on the machine's screen: see it in the dashboard, on the machine's Screen tab.\n", p.Title)
	}
	return nil
}

func (a *app) appsOpen(ctx context.Context, args []string) error {
	fs := a.flags("apps open", "wm apps open <machine> <app> [--for 30m] [--browser]", "Print a fresh link into a web app that "+
		"is on, landing on its page - its address opened private first when it is not open: it opens once, within five "+
		"minutes, and lets the browser that opens it stay as long as --for says. With --browser it opens in your browser "+
		"here too.")
	keep := fs.Duration("for", 30*time.Minute, "how long the browser that opens the link may reach the app, 1m to 60m")
	browser := fs.Bool("browser", false, "open the link in your browser on this computer")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	machine, name, err := appArgs(words)
	if err != nil {
		return err
	}
	access, err := accessFor(*keep)
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	p, err := c.LinkApp(ctx, id, name, api.AppLinkCreate{AccessS: access})
	if err != nil {
		return err
	}
	if a.json {
		err = a.printJSON(p)
	} else {
		_, err = fmt.Fprintln(a.stdout, p.Link)
	}
	if err != nil {
		return err
	}
	if *browser {
		// only a web address of ours is opened, whatever the answer said (browser.go)
		if !strings.HasPrefix(p.Link, "https://") || a.openBrowser(p.Link) != nil {
			fmt.Fprintln(a.stderr, "wm: no browser could be opened here: open the link on any device.")
			return nil
		}
		if !a.quiet {
			fmt.Fprintf(a.stderr, "wm: %s is opening in your browser.\n", p.Title)
		}
		return nil
	}
	if !a.quiet && !a.json {
		fmt.Fprintf(a.stderr, "wm: the link opens once, within five minutes (until %s), and lets the browser that opens it reach %s for %s.\n",
			linkEnd(p), p.Title, span(float64(p.AccessS)))
	}
	return nil
}

// linkEnd is when a link stops opening, as a clock says it.
func linkEnd(p *api.App) string {
	if p.ExpiresAt == nil {
		return "five minutes from now"
	}
	return p.ExpiresAt.UTC().Format("15:04:05 UTC")
}
