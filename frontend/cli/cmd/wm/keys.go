package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

var keyCommands = map[string]subcommand{
	"ls":     (*app).keysList,
	"create": (*app).keysCreate,
	"revoke": (*app).keysRevoke,
}

func (a *app) keysList(ctx context.Context, args []string) error {
	fs := a.flags("keys ls", "wm keys ls", "List the account's API keys. A key's secret is never shown again after it is made. "+
		"In an organisation, MADE BY says whose each is: a member sees their own, owners and admins every key.")
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
	keys, err := c.ListKeys(ctx)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if keys == nil {
			keys = []api.Key{}
		}
		return a.printJSON(keys)
	case a.quiet:
		for _, k := range keys {
			fmt.Fprintln(a.stdout, k.ID)
		}
		return nil
	}
	now := a.now()
	made := slices.ContainsFunc(keys, func(k api.Key) bool { return k.MadeBy != nil })
	t := a.table(withColumn(made, 6, []string{"ID", "NAME", "KEY", "STATE", "SCOPES", "PROJECTS", "LAST USED"}, "MADE BY")...)
	for _, k := range keys {
		used := "never"
		if k.LastUsedAt != nil {
			used = age(*k.LastUsedAt, now) + " ago"
		}
		t.row(withColumn(made, 6, []string{k.ID, k.Name, k.Shown, k.State, strings.Join(k.Scopes, ","), projectsOf(k.Projects), used},
			madeByOf(k.MadeBy))...)
	}
	return t.flush()
}

// scope is a scope as typed: machines:read for cloud:machines:read, all for cloud:*.
func scope(s string) string {
	switch s = strings.TrimSpace(s); s {
	case "all", "*":
		return api.ScopeAll
	}
	if !strings.HasPrefix(s, "cloud:") {
		s = "cloud:" + s
	}
	return s
}

// expiry reads when a key ends: in a while (30d, 12h) or at a time (2026-12-31, 2026-12-31T18:00:00Z).
func expiry(v string, now time.Time) (time.Time, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.Add(time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, usagef("--expires is a while (30d, 12h) or a time (2026-12-31, 2026-12-31T18:00:00Z), not %q", v)
}

func (a *app) keysCreate(ctx context.Context, args []string) error {
	fs := a.flags("keys create", "wm keys create <name> --scope <scope>... [flags]", "Make a key. It can hold only scopes "+
		"and projects the key in use holds. Its secret is shown this once: store it then. A key with full access (all), "+
		"or that makes keys (keys:write) or gives secrets (secrets:write), needs a code we e-mail you: asked for here at a "+
		"terminal, or given with --code."+
		"\n\nScopes: machines:read, "+
		"machines:write, machines:exec, gpus, projects:write, keys:write, secrets:write, folders:read, folders:write, or all.")
	var scopes, projects listFlag
	fs.Var(&scopes, "scope", "what it may do, a `scope`; repeat, or join with commas")
	fs.Var(&projects, "project", "a `project` it is limited to; repeat for more (every project when left out)")
	expires := fs.String("expires", "", "the `time` it stops working: in a while (30d, 12h) or on a date (2026-12-31)")
	rate := fs.Int("rate", 0, "calls a minute it may make (the key in use's own when left out)")
	code := fs.String("code", "", "the `code` we e-mailed you for a key with full access (all), or that makes keys (keys:write) or gives secrets "+
		"(secrets:write), when not asked for it in a terminal")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "the key's name"); err != nil {
		return err
	}
	if len(scopes) == 0 {
		return usagef("say what the key may do: --scope machines:read (or machines:write, machines:exec, gpus, projects:write, keys:write, secrets:write, folders:read, folders:write, all)")
	}
	k := api.KeyCreate{Name: words[0]}
	for _, s := range scopes {
		if s = scope(s); !slices.Contains(api.Scopes, s) {
			return usagef("no scope %q: the scopes are machines:read, machines:write, machines:exec, gpus, projects:write, keys:write, secrets:write, folders:read, folders:write and all", s)
		}
		k.Scopes = append(k.Scopes, s)
	}
	if *expires != "" {
		// a while from now as the program's own clock tells it - the one it shows ages by
		t, err := expiry(*expires, a.now())
		if err != nil {
			return err
		}
		k.ExpiresAt = &t
	}
	if *rate != 0 {
		if *rate < 1 || *rate > 6000 {
			return usagef("--rate is between 1 and 6000 calls a minute")
		}
		k.RatePerMinute = rate
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	for _, p := range projects {
		id, err := c.FindProject(ctx, p)
		if err != nil {
			return err
		}
		k.Projects = append(k.Projects, id)
	}
	k.ConfirmCode = strings.TrimSpace(*code)
	made, err := c.CreateKey(ctx, k)
	var refused *api.Error
	if errors.As(err, &refused) && refused.Code == api.CodeConfirmRequired && refused.Confirm != nil && k.ConfirmCode == "" {
		// a key with full access, that makes keys or that gives secrets: the code we e-mail its person, asked for at a terminal - and asked
		// again while it is typed wrong and has tries left
		ask := *refused.Confirm
		if err = a.sendCode(ctx, c, ask); err != nil {
			return err
		}
		for {
			if k.ConfirmCode, err = a.typedCode(); err != nil {
				return err
			}
			made, err = c.CreateKey(ctx, k)
			if !errors.As(err, &refused) || refused.Code != api.CodeConfirmWrong || strings.Contains(refused.Detail, "spent") {
				break
			}
			fmt.Fprintf(a.stderr, "wm: %s\n", refused.Detail)
		}
	}
	if err != nil {
		return err
	}
	switch {
	case a.quiet:
		fmt.Fprintln(a.stdout, made.Secret)
		return nil
	case a.json:
		if err := a.printJSON(made); err != nil {
			return err
		}
	default:
		d := a.detail()
		d.line("id", made.ID)
		d.line("name", made.Name)
		d.line("scopes", strings.Join(made.Scopes, ", "))
		d.line("projects", projectsOf(made.Projects))
		d.line("expires", whenOr(made.ExpiresAt, a.now(), "never"))
		d.line("secret", made.Secret)
		if err := d.flush(); err != nil {
			return err
		}
	}
	fmt.Fprintln(a.stderr, "wm: store the secret now: it is shown only this once, and cannot be shown again.")
	return nil
}

func (a *app) keysRevoke(ctx context.Context, args []string) error {
	fs := a.flags("keys revoke", "wm keys revoke [--yes] <key>...", "Revoke keys: they stop working within seconds, everywhere. "+
		"This cannot be undone.")
	yes := fs.Bool("yes", false, "revoke without being asked")
	fs.BoolVar(yes, "y", false, "revoke without being asked")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which keys"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(words))
	for _, w := range words {
		id, err := c.FindKey(ctx, w)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if !*yes {
		ok, err := a.confirm(fmt.Sprintf("Revoke %s? Whatever uses it stops working.", strings.Join(ids, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errNothingDone
		}
	}
	for _, id := range ids {
		if err := c.RevokeKey(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, id)
	}
	return nil
}

// sendCode asks for a code for a risky step, which the API e-mails the key's person, and says where it went - off a
// terminal, that and how to give it with --code instead.
func (a *app) sendCode(ctx context.Context, c *api.Client, ask api.ConfirmAsk) error {
	sent, err := c.SendConfirmCode(ctx, ask)
	if err != nil {
		return err
	}
	if !isTerminal(a.stdin) {
		return usagef("we e-mailed a code to %s: run this again with --code <code>, within 10 minutes", sent.SentTo)
	}
	fmt.Fprintf(a.stderr, "We e-mailed a code to %s. It works once, for 10 minutes.\n", sent.SentTo)
	return nil
}

// typedCode reads a code at the terminal: none typed is no code, and nothing is done.
func (a *app) typedCode() (string, error) {
	fmt.Fprint(a.stderr, "Code: ")
	typed, err := a.line()
	if err != nil {
		return "", err
	}
	if typed = strings.TrimSpace(typed); typed == "" {
		return "", errNothingDone
	}
	return typed, nil
}
