package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const limitsHelp = `Usage: wm limits [command]

  (none)                       this month's limits, and how close the account is to each
  set month <dollars|none>     set the account's own limit for the month, or take it away

A limit pauses machines once it is reached, with everything in them kept, until the next month or a higher limit.
The account's own limit is set in your own account by you, and in an organisation by its owners (with a key) and
billing (in the dashboard). Teams' budgets and people's own limits are set in the dashboard. Limits for the month
start again on the first of each month, in UTC.
`

// wm limits: the limits for the month that apply to the account - its own and Wheelmachine's, each team's budget, each
// person's own - as the key's person may see them; wm limits set month: the account's own.
func (a *app) limitsCmd(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "set" {
		return a.limitsSet(ctx, args[1:])
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(a.stdout, limitsHelp)
		return nil
	}
	fs := a.flags("limits", "wm limits", "This month's limits, and how close the account is to each.")
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
	l, err := c.Limits(ctx)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(l)
	}
	if a.quiet {
		if l.Account.InForceUSD != nil {
			fmt.Fprintln(a.stdout, strconv.FormatFloat(*l.Account.InForceUSD, 'f', 2, 64))
		}
		return nil
	}
	t := a.table("LIMIT", "OF", "THIS MONTH", "USED")
	acct := l.Account
	whose := "your account"
	if acct.Kind == "org" {
		whose = acct.Name
	}
	switch {
	case !acct.Limited:
		t.row("monthly", whose, "no limit", "")
	default:
		t.row("monthly", whose+inForceWords(acct.InForce), spentOf(acct.SpentUSD, acct.InForceUSD), percentWords(acct.Percent))
	}
	for _, tm := range l.Teams {
		if tm.MonthBudgetUSD == nil {
			t.row("team budget", tm.Name, "none: the organisation's holds", "")
			continue
		}
		t.row("team budget", tm.Name, spentOf(tm.SpentUSD, tm.MonthBudgetUSD), percentWords(tm.Percent))
	}
	for _, p := range l.People {
		name := p.Person.Name
		if p.MonthCapUSD == nil {
			t.row("person", name, "none of their own", "")
			continue
		}
		t.row("person", name, spentOf(p.SpentUSD, p.MonthCapUSD), percentWords(p.Percent))
	}
	if err := t.flush(); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "Limits for the month start again on %d %s (UTC).\n", l.ResetsAt.Day(), l.ResetsAt.Month())
	return nil
}

// Whose limit holds the account, said after its name.
func inForceWords(whose string) string {
	if whose == "wheelmachine" {
		return " (set by Wheelmachine)"
	}
	return ""
}

// What was spent of a limit, as a cell says it: "$12.40 of $40", or the limit alone to one not shown what was spent.
func spentOf(spent, limit *float64) string {
	switch {
	case limit == nil:
		return "a limit holds it"
	case spent == nil:
		return money(*limit)
	}
	return money(*spent) + " of " + money(*limit)
}

func percentWords(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p) + "%"
}

// wm limits set month <dollars|none>: the account's own limit for the month.
func (a *app) limitsSet(ctx context.Context, args []string) error {
	fs := a.flags("limits set", "wm limits set month <dollars|none>", "Set the account's own limit for the month, or take it away with none.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "month and the dollars, or none"); err != nil {
		return err
	}
	if words[0] != "month" {
		return usagef("wm limits set month <dollars|none>: the account's own limit for the month is the one set here")
	}
	var usd *float64
	if v := strings.TrimPrefix(strings.TrimSpace(words[1]), "$"); v != "none" {
		n, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", ""), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 10_000_000 {
			return usagef("a month's limit is dollars from 0 to 10000000, such as 200, or none")
		}
		n = math.Round(n*100) / 100
		usd = &n
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	l, err := c.SetMonthLimit(ctx, usd)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(l)
	case a.quiet:
		return nil
	case usd == nil:
		fmt.Fprintln(a.stdout, "The account has no limit for the month of its own now.")
	default:
		fmt.Fprintf(a.stdout, "The account's own limit is %s a month now.\n", money(*usd))
	}
	if w := l.Account.Wheelmachine.MonthCapUSD; w != nil && l.Account.InForce == "wheelmachine" {
		fmt.Fprintf(a.stdout, "Wheelmachine's own limit of %s a month still applies.\n", money(*w))
	}
	return nil
}
