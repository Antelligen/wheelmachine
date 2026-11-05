package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

// The organisations a person is in, and an account's audit: read here. An organisation's people, its invitations and
// its teams are managed in the dashboard, where a person is signed in as themselves - never with a key, which a
// program holds.

func (a *app) orgs(ctx context.Context, args []string) error {
	fs := a.flags("orgs", "wm orgs", "The organisations you are in: your role in each, how many people are in it, and "+
		"whether you are its primary owner, the owner who transfers it. A key made in an organisation lists none: a key "+
		"of your own account does. wm login --account <id> makes a key in one of them. An organisation's people, "+
		"invitations and teams are managed in the dashboard, https://app.wheelmachine.ai.")
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
	list, err := c.Orgs(ctx)
	if api.IsCode(err, api.CodeForbidden) {
		// the one error of the list: a key of an organisation's, which keeps what else the person who made it is in to them
		return fmt.Errorf("%w\nwm: a key of your own account lists them: wm login, choosing your own account as you approve", err)
	}
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if list == nil {
			list = []api.Org{}
		}
		return a.printJSON(list)
	case a.quiet:
		for _, o := range list {
			fmt.Fprintln(a.stdout, o.ID)
		}
		return nil
	}
	t := a.table("ID", "NAME", "ROLE", "PEOPLE", "PRIMARY OWNER")
	for _, o := range list {
		t.row(o.ID, o.Name, o.Role, strconv.Itoa(o.People), yesNo(o.PrimaryOwner))
	}
	return t.flush()
}

func (a *app) audit(ctx context.Context, args []string) error {
	fs := a.flags("audit", "wm audit [--person <id>] [--action <prefix>] [--since <time>] [--until <time>] [--limit <n>] "+
		"[--json | --csv] [--out <file>]",
		"What was done in the account, the newest first: when, by whom, what, to which thing, and from where. --csv "+
			"writes it as a file instead, whole, to standard output, and --out writes it to a file (CSV, or JSON with "+
			"--json); a new one is readable by you only. A file covers a year at most - the last year when --since is left "+
			"out - and 100,000 events at most. The audit is read with a key that holds every scope (all), and in an "+
			"organisation by its owners and admins.")
	person := fs.String("person", "", "only this person's events, by their `id`, as wm me or wm audit --json says it")
	action := fs.String("action", "", "only the events whose action begins with this `prefix`, such as machine. or member.")
	sinceFlag := fs.String("since", "", "from `when`: a date (2026-10-01, from midnight UTC), a time (2026-10-01T09:00:00Z) or how long ago (6h, 7d)")
	untilFlag := fs.String("until", "", "before `when`, said as --since is")
	limit := fs.Int("limit", 0, "show at most this many, the newest (0: all)")
	asCSV := fs.Bool("csv", false, "write the audit as CSV, whole, to standard output (or to --out)")
	out := fs.String("out", "", "write the audit, whole, to this `file`: CSV, or JSON with --json")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	now := a.now()
	q := api.AuditQuery{Person: *person, Action: *action}
	if q.Since, err = pointOf("since", *sinceFlag, now); err != nil {
		return err
	}
	if q.Until, err = pointOf("until", *untilFlag, now); err != nil {
		return err
	}
	export := *asCSV || *out != ""
	switch {
	case *asCSV && a.json:
		return usagef("--csv or --json: the file is one or the other")
	case *limit < 0:
		return usagef("--limit is 0 or more")
	case *limit > 0 && export:
		return usagef("--limit is for the list: a file holds the whole audit the other flags ask for")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if export {
		format := api.AuditCSV
		if a.json {
			format = api.AuditJSON
		}
		return auditRefused(a.exportAudit(ctx, c, q, format, *out))
	}
	q.Limit = 200
	if *limit > 0 {
		q.Limit = min(*limit, 200)
	}
	list := []api.AuditEvent{}
	for e, err := range c.AllAudit(ctx, q) {
		if err != nil {
			return auditRefused(err)
		}
		list = append(list, e)
		if *limit > 0 && len(list) >= *limit {
			break
		}
	}
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, e := range list {
			fmt.Fprintln(a.stdout, e.ID)
		}
		return nil
	}
	t := a.table("TIME (UTC)", "PERSON", "ACTION", "TARGET", "FROM")
	for _, e := range list {
		t.row(e.At.UTC().Format("2006-01-02 15:04:05"), whoDid(e), e.Action, e.Target, e.Address)
	}
	return t.flush()
}

// exportAudit writes the audit the query asks for as a file: to standard output, or whole to `path` - written beside
// it and put in its place at the end; a new file readable by you only, as it names people and where they were - and
// says where, and how much, on standard error.
func (a *app) exportAudit(ctx context.Context, c *api.Client, q api.AuditQuery, format, path string) error {
	body, err := c.ExportAudit(ctx, q, format)
	if err != nil {
		return err
	}
	defer body.Close()
	if path == "" || path == "-" {
		_, err := io.Copy(a.stdout, body)
		return err
	}
	n := &counter{r: body}
	if err := writeWholeAs(path, n, 0o600); err != nil {
		return err
	}
	if !a.quiet {
		fmt.Fprintf(a.stderr, "wm: wrote %d bytes of %s to %s\n", n.n, strings.ToUpper(format), path)
	}
	return nil
}

// auditRefused says, beside an error of the audit's, who may read it.
func auditRefused(err error) error {
	if api.IsCode(err, api.CodeForbidden) {
		return fmt.Errorf("%w\nwm: the audit is read with a key that holds every scope, and in an organisation by its owners and admins", err)
	}
	return err
}

// whoDid is who did what an event says: the person by their name (their id when they have none), else what did it -
// system for what Wheelmachine did by itself, wheelmachine for Wheelmachine's staff, or the key, for an event that names nobody.
func whoDid(e api.AuditEvent) string {
	if e.Person != nil {
		return or(e.Person.Name, e.Person.ID)
	}
	return e.By
}

// pointOf reads a time the audit is asked from or until: a date (2026-10-01, its midnight in UTC), a time
// (2026-10-01T09:00:00Z), or how long ago, as wm metrics takes it (15m, 6h, 7d), by the program's own clock - the one
// it shows ages by. Nil for none.
func pointOf(flag, v string, now time.Time) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		// (at most a hundred years back: more is no time a duration holds)
		if n, err := strconv.Atoi(days); err == nil && n > 0 && n <= 36600 {
			t := now.Add(-time.Duration(n) * 24 * time.Hour)
			return &t, nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		t := now.Add(-d)
		return &t, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return &t, nil
		}
	}
	return nil, usagef("--%s is a date (2026-10-01), a time (2026-10-01T09:00:00Z) or how long ago (6h, 7d), not %q", flag, v)
}

// counter counts the bytes read through it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
