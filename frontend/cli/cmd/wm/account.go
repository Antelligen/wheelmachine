package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/term"
)

func (a *app) login(ctx context.Context, args []string) error {
	fs := a.flags("login", "wm login [--scope <scope>...] [--name <name>] [--account <acc_id>] [--no-browser] | wm login --key",
		"Sign in through your browser, and keep a key of your own on this computer, for "+
			"wm and wm-mcp. wm opens the sign-in page and shows a code; type it there, approve, and the "+
			"key is kept in a file only you can read. On a computer without a browser, open the address on any other "+
			"device. The key is made in your own account, or in an organisation you are in: --account asks for that "+
			"organisation by its id (wm orgs lists them), and the page you approve on shows it first; you choose there. "+
			"--key keeps a key you already have instead: asked for at a terminal and not shown as you type, "+
			"else read from standard input (wm login --key < keyfile).")
	withKey := fs.Bool("key", false, "keep a key you already have, instead of signing in through the browser")
	var scopes listFlag
	fs.Var(&scopes, "scope", "what the new key may do, a `scope` such as machines:read; repeat, or join with commas (all when left out)")
	name := fs.String("name", "", "what the new key is called (`wm on <this computer>` when left out)")
	account := fs.String("account", "", "the organisation to make the key in, by its id (`acc_...`); your own account when left out")
	noBrowser := fs.Bool("no-browser", false, "show the address to open, without opening a browser here")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	if *withKey {
		if len(scopes) > 0 || *name != "" || *account != "" || *noBrowser {
			return usagef("--key keeps a key you have: --scope, --name, --account and --no-browser are for signing in through the browser")
		}
		return a.loginWithKey(ctx)
	}
	if *account != "" && !api.IsAccountID(*account) {
		return usagef("--account is an organisation's id, acc_ and 26 letters and digits, as wm orgs lists them; not %q", *account)
	}
	if f, ok := a.stdin.(*os.File); ok && !term.IsTerminal(f) {
		// a key piped in, as before --key: said, rather than waited on for ten minutes in silence
		fmt.Fprintln(a.stderr, "wm: signing in through the browser; to keep a key given on standard input, use wm login --key")
	}
	asked := api.DeviceLoginRequest{Name: *name, Account: *account}
	if asked.Name == "" {
		asked.Name = "wm on " + a.hostname()
	}
	for _, s := range scopes {
		if s = scope(s); !slices.Contains(api.Scopes, s) {
			return usagef("no scope %q: the scopes are machines:read, machines:write, machines:exec, gpus, projects:write, keys:write, secrets:write, folders:read, folders:write and all", s)
		}
		if !slices.Contains(asked.Scopes, s) {
			asked.Scopes = append(asked.Scopes, s)
		}
	}
	opts := append([]api.Option{api.WithBaseURL(api.BaseURL(a.getenv)), api.WithUserAgent("wm/" + api.Version)}, a.options...)
	login, err := api.StartDeviceLogin(ctx, asked, opts...)
	if err != nil {
		if api.IsCode(err, api.CodeNotOpen) {
			return fmt.Errorf("%w\nwm: signing in through the browser is not open yet: give a key with wm login --key", err)
		}
		return err
	}
	fmt.Fprintf(a.stderr, "To sign in, open %s and enter this code:\n\n    %s\n\n", login.VerificationURI, login.UserCode)
	opened := false
	if !*noBrowser {
		if err := a.openBrowser(login.VerificationURI); err == nil {
			opened = true
		}
	}
	if opened {
		fmt.Fprintln(a.stderr, "Your browser is opening it. Sign in, type the code, then approve.")
	} else {
		fmt.Fprintln(a.stderr, "Open it on any device, sign in, type the code, then approve.")
	}
	fmt.Fprintf(a.stderr, "Waiting for you to approve (the code works until %s; Ctrl-C to stop)...\n", login.ExpiresAt.Local().Format("15:04"))
	made, err := login.Wait(ctx)
	switch {
	case api.IsCode(err, api.CodeLoginDenied):
		return fmt.Errorf("%w\nwm: nothing was given; run wm login again to sign in", err)
	case api.IsCode(err, api.CodeLoginExpired):
		return fmt.Errorf("%w\nwm: run wm login again for a new code", err)
	case err != nil:
		return err
	}
	return a.keep(ctx, made.Secret, made)
}

// The key given or made, kept for wm and wm-mcp after it is checked with the API. `made`: the key a sign-in through
// the browser was given, said by its name; nil for one given with --key.
func (a *app) keep(ctx context.Context, key string, made *api.KeyCreated) error {
	c, err := api.New(append([]api.Option{api.WithBaseURL(api.BaseURL(a.getenv)), api.WithKey(key),
		api.WithUserAgent("wm/" + api.Version)}, a.options...)...)
	if err != nil {
		return err
	}
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	path, err := api.ConfigPath(a.getenv)
	if err != nil {
		return err
	}
	cfg, _ := api.LoadConfig(path) // a file that cannot be read is written again whole
	cfg.APIKey = key
	if err := api.SaveConfig(path, cfg); err != nil {
		return err
	}
	if !a.quiet {
		// the person chooses the account at the page they approve on, whatever was asked: where the key is, is said
		where := me.Account.ID
		if me.InOrg() {
			where += ", the organisation " + strconv.Quote(me.Account.Name)
		}
		if made == nil {
			fmt.Fprintf(a.stdout, "Signed in to account %s with key %s (%s).\nThe key is kept in %s, readable by you only.\n",
				where, strconv.Quote(me.Key.Name), me.Key.ID, path)
		} else {
			fmt.Fprintf(a.stdout, "Signed in to account %s.\nA key of your own, %s (%s), is kept in %s, readable by you only.\n",
				where, strconv.Quote(made.Name), made.ID, path)
		}
	}
	if a.getenv(api.EnvAPIKey) != "" {
		fmt.Fprintf(a.stderr, "wm: %s is set, and is used before the kept key while it is.\n", api.EnvAPIKey)
	}
	return nil
}

// The old way, for a computer without a browser near: a key made elsewhere, pasted or piped in.
func (a *app) loginWithKey(ctx context.Context) error {
	key, err := a.readKey("API key (not shown as you type): ")
	if err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key == "" {
		return usagef("no key given")
	}
	if !strings.HasPrefix(key, "ak_") {
		return usagef("that is not an API key: keys begin ak_live_")
	}
	return a.keep(ctx, key, nil)
}

// This computer's name, for the key's: "laptop" from laptop.local; "this computer" when it has none.
func (a *app) hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "this computer"
	}
	h, _, _ = strings.Cut(h, ".")
	h = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, h)
	if h == "" {
		return "this computer"
	}
	if len(h) > 60 {
		h = h[:60]
	}
	return h
}

// readKey asks for the key at a terminal without showing it, or reads the first line of what is piped in.
func (a *app) readKey(prompt string) (string, error) {
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(f) {
		fmt.Fprint(a.stderr, prompt)
		b, err := term.ReadPassword(f)
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", fmt.Errorf("the key could not be read: %w", err)
		}
		return string(b), nil
	}
	s, err := a.line()
	if err != nil {
		return "", usagef("give the key on standard input, or run wm login at a terminal")
	}
	return s, nil
}

func (a *app) logout(args []string) error {
	fs := a.flags("logout", "wm logout", "Forget the key kept on this computer. The key itself still works until it is revoked "+
		"(wm keys revoke).")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	path, err := api.ConfigPath(a.getenv)
	if err != nil {
		return err
	}
	removed, err := api.RemoveConfig(path)
	if err != nil {
		return err
	}
	if a.quiet {
		return nil
	}
	if !removed {
		fmt.Fprintln(a.stdout, "No key is kept on this computer.")
	} else {
		fmt.Fprintln(a.stdout, "Signed out: the key is no longer kept on this computer. It still works until it is revoked (wm keys revoke).")
	}
	if a.getenv(api.EnvAPIKey) != "" {
		fmt.Fprintf(a.stderr, "wm: %s is still set, and wm uses it.\n", api.EnvAPIKey)
	}
	return nil
}

func (a *app) me(ctx context.Context, args []string) error {
	fs := a.flags("me", "wm me", "The account and key in use, and whether the account can spend. In an organisation, "+
		"also the organisation, the person who made the key, their role and teams there, and their own limits there.")
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
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(me)
	case a.quiet:
		fmt.Fprintln(a.stdout, me.Account.ID)
		return nil
	}
	now := a.now()
	d := a.detail()
	d.line("account", me.Account.ID)
	if me.InOrg() {
		// in a person's own account all of this goes without saying: it is theirs, and they hold everything in it
		d.line("organisation", me.Account.Name+" ("+me.Account.ID+")")
		if me.Person != nil {
			d.line("person", personOf(*me.Person))
		}
		d.line("role", me.Role)
		d.line("teams", teamsOf(me.Teams))
		if len(me.Levels) > 0 {
			d.line("manages", levelsOf(me.Levels))
		}
		d.line("limits", limitsOf(me.Limits))
	}
	d.line("key", me.Key.ID+" "+strconv.Quote(me.Key.Name))
	d.line("shown as", me.Key.Shown)
	d.line("scopes", strings.Join(me.Key.Scopes, ", "))
	d.line("projects", projectsOf(me.Key.Projects))
	d.line("state", me.Key.State)
	if me.Key.RatePerMinute != nil {
		d.line("rate", strconv.Itoa(*me.Key.RatePerMinute)+" calls a minute")
	}
	d.line("expires", whenOr(me.Key.ExpiresAt, now, "never"))
	spend := "yes"
	if !me.Standing.CanSpend {
		spend = "no: " + or(me.Standing.Reason, "the balance is used up")
	}
	d.line("can spend", spend)
	cur := me.Standing.Currency
	if me.Standing.Credit != nil {
		d.line("credit", balanceMoney(*me.Standing.Credit, cur))
	}
	if me.Standing.Balance != nil {
		d.line("balance", moneyIn(*me.Standing.Balance, cur)+" left this month")
	} else {
		d.line("balance", "no limit this month")
	}
	return d.flush()
}

func projectsOf(ids []string) string {
	if len(ids) == 0 {
		return "every project"
	}
	return strings.Join(ids, ", ")
}

// personOf is a person by their name and their id: "Mira Okafor (acc_...)".
func personOf(p api.Person) string {
	if p.Name == "" {
		return p.ID
	}
	return p.Name + " (" + p.ID + ")"
}

// teamsOf says the projects whose teams a person is in: nil is a role not limited to teams, which reaches every project,
// and none is a person in no team yet, who reaches nothing until an owner or admin puts them in one.
func teamsOf(ids []string) string {
	switch {
	case ids == nil:
		return "every project (the role is not limited to teams)"
	case len(ids) == 0:
		return "none yet: an owner or admin puts people in teams"
	}
	return strings.Join(ids, ", ")
}

// levelsOf says the teams a person manages, each with their level there, in the order of the teams' ids:
// "prj_... (Full manager), prj_... (Approver)".
func levelsOf(levels map[string]string) string {
	projects := slices.Sorted(maps.Keys(levels))
	said := make([]string, 0, len(projects))
	for _, p := range projects {
		said = append(said, p+" ("+or(levelWords(levels[p]), levels[p])+")")
	}
	return strings.Join(said, ", ")
}

// limitsOf says a person's own limits in an organisation, beside its own: each one they have, and whether they make
// GPU machines - plainly "no limit of their own" when they have none.
func limitsOf(l *api.PersonLimits) string {
	if l == nil {
		return "no limit of their own"
	}
	var held []string
	if l.MaxMachines != nil {
		held = append(held, "at most "+strconv.Itoa(*l.MaxMachines)+" machines")
	}
	if l.MaxVCPUs != nil {
		held = append(held, "at most "+strconv.Itoa(*l.MaxVCPUs)+" CPUs at once")
	}
	if l.MonthCap != nil {
		held = append(held, "at most "+moneyIn(*l.MonthCap, l.Currency)+" a month")
	}
	words := "no limit of their own"
	if len(held) > 0 {
		words = strings.Join(held, ", ")
	}
	return words + "; GPU machines: " + yesNo(l.GPUs)
}

func (a *app) usageCmd(ctx context.Context, args []string) error {
	fs := a.flags("usage", "wm usage [--month YYYY-MM] [--project <project>]", "Each machine's running time and cost in a month.")
	month := fs.String("month", "", "the month, `YYYY-MM`; this month when left out")
	project := fs.String("project", "", "only this project's machines")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	if *month != "" {
		if _, err := time.Parse("2006-01", *month); err != nil {
			return usagef("--month is a month like 2026-10")
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	p := api.UsageParams{Month: *month}
	if *project != "" {
		if p.Project, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	u, err := c.Usage(ctx, p)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(u)
	case a.quiet:
		for _, l := range u.Machines {
			fmt.Fprintln(a.stdout, l.Machine)
		}
		return nil
	}
	held := slices.ContainsFunc(u.Machines, func(l api.UsageLine) bool { return l.HeldBy != nil })
	t := a.table(withColumn(held, 3, []string{"MACHINE", "NAME", "PROJECT", "RAN", "COST"}, "WHOSE")...)
	for _, l := range u.Machines {
		t.row(withColumn(held, 3, []string{l.Machine, l.Name, l.Project, span(l.RunningSeconds), moneyIn(l.Amount, u.Currency)}, whoseOf(l.HeldBy))...)
	}
	t.row(withColumn(held, 3, []string{"total " + u.Month, "", "", "", moneyIn(u.Total, u.Currency)}, "")...)
	if u.Folders != nil {
		t.row(withColumn(held, 3, []string{"folders " + u.Month, "", "", "-", moneyIn(*u.Folders, u.Currency)}, "")...)
	}
	return t.flush()
}

func (a *app) creditCmd(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "buy" {
		return a.creditBuy(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "currency" {
		return a.creditCurrency(ctx, args[1:])
	}
	fs := a.flags("credit", "wm credit [--limit <n>] [--all]",
		"The account's credit, and its latest entries: credit put in, and machines' running time taken from it - one "+
			"entry a machine a day - in the account's currency. Your own account gets 5 of it in free credit once ($5, "+
			"£5 or €5) - signed up with Google, once you link a GitHub account - and a new organisation none; at zero the "+
			"account's machines are paused until the balance is above zero (wm credit buy). In an organisation the credit "+
			"is the organisation's, read by its owners, admins and billing. wm credit currency changes the currency "+
			"until your first purchase, or until we change it for you.")
	limit := fs.Int("limit", 20, "how many entries to show, the newest first (1 to 200)")
	every := fs.Bool("all", false, "every entry, page after page")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	if *limit < 1 || *limit > 200 {
		return usagef("--limit is 1 to 200")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	credit, err := c.Credit(ctx, api.ListParams{Limit: *limit})
	if err != nil {
		return err
	}
	entries := credit.Data
	if *every && credit.Next != nil {
		entries = nil
		for e, err := range c.AllCreditEntries(ctx, 200) {
			if err != nil {
				return err
			}
			entries = append(entries, e)
		}
	}
	switch {
	case a.json:
		if *every {
			credit.Data, credit.Next = entries, nil
		}
		return a.printJSON(credit)
	case a.quiet:
		fmt.Fprintf(a.stdout, "%.6f\n", credit.Balance)
		return nil
	}
	cur := credit.Currency
	d := a.detail()
	d.line("balance", balanceMoney(credit.Balance, cur))
	if credit.CurrencyFixed {
		d.line("currency", currencyWords(cur))
	} else {
		d.line("currency", currencyWords(cur)+"; wm credit currency changes it until your first purchase, or until we change it for you")
	}
	switch credit.Charging.State {
	case "off":
		d.line("charging", "not charged: the company's own account")
	case "from":
		if credit.Charging.From != nil && len(*credit.Charging.From) >= 10 {
			d.line("charging", "starts "+(*credit.Charging.From)[:10]+": until then nothing is taken, and nothing pauses for credit")
		} else {
			d.line("charging", "not started: you are e-mailed at least 30 days before it does")
		}
	default:
		if credit.Empty {
			d.line("state", "used up: machines are paused, folders are read-only, and nothing starts until the balance is above zero")
		}
	}
	if fc := credit.FreeCredit; fc != nil {
		line := balanceMoney(fc.Amount, cur) + " once you link your GitHub account on the dashboard's Code page"
		if fc.GitHubMinDays > 0 {
			line += " (at least " + strconv.Itoa(fc.GitHubMinDays) + " days old)"
		}
		if fc.TooNewUntil != nil {
			line = balanceMoney(fc.Amount, cur) + " once your GitHub account is old enough: link it again from " + (*fc.TooNewUntil)[:10]
		}
		d.line("free credit", line)
	}
	if credit.Buy.Open {
		d.line("buy credit", "wm credit buy <"+amountsOf(credit.Buy.Amounts)+">, or on the dashboard's Billing page")
	} else {
		d.line("buy credit", "not open just now")
	}
	if err := d.flush(); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout)
	t := a.table("WHEN (UTC)", "WHAT", "MACHINE", "AMOUNT")
	for _, e := range entries {
		when := e.At.UTC().Format("2006-01-02 15:04")
		if e.Kind == api.EntryMachineTime || e.Kind == api.EntryFolders {
			when = e.At.UTC().Format("2006-01-02")
		}
		machine := "-"
		if e.Machine != nil {
			machine = *e.Machine
			if e.MachineName != nil && *e.MachineName != "" {
				machine = *e.MachineName
			}
		}
		t.row(when, e.Reason, machine, signedMoney(e.Amount, cur))
	}
	if err := t.flush(); err != nil {
		return err
	}
	if !*every && credit.Next != nil {
		fmt.Fprintf(a.stderr, "wm: the latest %d entries; --all shows every one\n", len(entries))
	}
	return nil
}

// creditBuy starts a checkout to buy credit, and prints where to pay it: the dashboard's Billing page, which opens
// Paddle's checkout on it for a person signed in there. Nothing is charged until it is paid.
func (a *app) creditBuy(ctx context.Context, args []string) error {
	fs := a.flags("credit buy", "wm credit buy <10|25|50|100>",
		"Buy credit for the account: 10, 25, 50 or 100 of its currency (wm credit says which). It prints a link to pay it in Paddle's checkout, on the "+
			"dashboard's Billing page - open it in a browser signed in to the dashboard, in this account. Paddle takes "+
			"the payment, adds any sales tax or VAT, and e-mails you the receipt and tax invoice; the credit is there once "+
			"Paddle says the payment is complete. A link is paid once, within a day: for any other payment of it we ask "+
			"Paddle to refund it in full. In an organisation, its owners and billing buy credit.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "the amount: 10, 25, 50 or 100"); err != nil {
		return err
	}
	amount, err := strconv.Atoi(strings.TrimLeft(words[0], "$£€"))
	if err != nil || (amount != 10 && amount != 25 && amount != 50 && amount != 100) {
		return usagef("the amount is 10, 25, 50 or 100")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := c.BuyCredit(ctx, amount)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(t)
	}
	// what a buyer agrees to, said before they pay, as the dashboard says it by its buttons (Paddle asks it): the
	// checkout this link opens covers the Billing page, line and all
	fmt.Fprintln(a.stderr, "wm: by buying credit, you agree to the Terms of Service (https://wheelmachine.ai/terms/) and the Refund "+
		"Policy (https://wheelmachine.ai/refunds/); Paddle.com is the seller of this order (Merchant of Record), and its Buyer "+
		"Terms apply (https://www.paddle.com/legal/buyer-terms)")
	if a.quiet {
		fmt.Fprintln(a.stdout, t.Checkout.URL)
		return nil
	}
	d := a.detail()
	d.line("amount", moneyIn(t.Amount, t.Currency))
	d.line("pay at", t.Checkout.URL)
	if err := d.flush(); err != nil {
		return err
	}
	fmt.Fprintln(a.stderr, "wm: open the link in a browser signed in to the dashboard, in this account, and pay it within a day; the "+
		"credit shows in wm credit once Paddle confirms the payment")
	return nil
}

// creditCurrency says the currency the account's credit is counted in, or changes it - until your first purchase, or until we change it for you.
func (a *app) creditCurrency(ctx context.Context, args []string) error {
	fs := a.flags("credit currency", "wm credit currency [USD|GBP|EUR]",
		"The currency the account's credit is counted in, or, given one, change it to it: US dollars, pounds sterling or "+
			"euros. It can be changed until your first purchase, or until we change it for you; after that it stays. The credit keeps its value "+
			"at Wheelmachine's rates, and free credit becomes 5 of the new currency; limits and alerts are turned too. In "+
			"an organisation, its owners and billing change it.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 1, "the currency: USD, GBP or EUR"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var credit *api.Credit
	if len(words) == 0 {
		if credit, err = c.Credit(ctx, api.ListParams{Limit: 1}); err != nil {
			return err
		}
	} else {
		to := strings.ToUpper(strings.TrimSpace(words[0]))
		if to != api.CurrencyUSD && to != api.CurrencyGBP && to != api.CurrencyEUR {
			return usagef("the currency is USD, GBP or EUR")
		}
		if credit, err = c.SetCurrency(ctx, to); err != nil {
			return err
		}
	}
	credit.Data, credit.Next = nil, nil
	switch {
	case a.json:
		return a.printJSON(credit)
	case a.quiet:
		fmt.Fprintln(a.stdout, credit.Currency)
		return nil
	}
	d := a.detail()
	d.line("currency", currencyWords(credit.Currency))
	d.line("balance", balanceMoney(credit.Balance, credit.Currency))
	if credit.CurrencyFixed {
		d.line("change", "fixed")
	} else {
		d.line("change", "wm credit currency <USD|GBP|EUR>, until your first purchase, or until we change it for you")
	}
	return d.flush()
}

// amountsOf is the amounts credit is bought in, as wm credit buy takes them: 10|25|50|100.
func amountsOf(amounts []int64) string {
	out := make([]string, 0, len(amounts))
	for _, v := range amounts {
		out = append(out, strconv.FormatInt(v, 10))
	}
	return strings.Join(out, "|")
}

// signedMoney is an amount with its sign: +$5.00 put in, -£0.0400 taken.
func signedMoney(v float64, currency string) string {
	if v < 0 {
		return "-" + moneyIn(-v, currency)
	}
	return "+" + moneyIn(v, currency)
}

// balanceMoney is a balance, below zero said so: -$0.0400.
func balanceMoney(v float64, currency string) string {
	if v < 0 {
		return "-" + moneyIn(-v, currency)
	}
	return moneyIn(v, currency)
}

// errNothingDone ends a command the person said no to: nothing was done, and nothing failed.
var errNothingDone = errors.New("nothing done")
