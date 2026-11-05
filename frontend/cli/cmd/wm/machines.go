package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

var machineCommands = map[string]subcommand{
	"ls":        (*app).machinesList,
	"create":    (*app).machinesCreate,
	"new":       (*app).machinesNew,
	"types":     (*app).machinesTypes,
	"get":       (*app).machinesGet,
	"rm":        (*app).machinesRemove,
	"pause":     moveCommand("pause"),
	"start":     moveCommand("start"),
	"stop":      moveCommand("stop"),
	"archive":   moveCommand("archive"),
	"fork":      (*app).machinesFork,
	"hand-off":  (*app).machinesHandOff,
	"take-back": (*app).machinesTakeBack,
	"give-back": (*app).machinesGiveBack,
	"copy":      (*app).machinesFork, // (a fork's name before it was one: kept, and not shown in the help)
	"backup":    (*app).machinesBackup,
	"backups":   (*app).machinesBackups,
	"wait":      (*app).machinesWait,
	"label":     (*app).machinesLabel,
	"rename":    (*app).machinesRename,
	"secrets":   (*app).machinesSecrets,
	"network":   (*app).machinesNetwork,
}

// labelFlag gathers key=value flags.
type labelFlag map[string]string

func (l labelFlag) String() string { return labelsOf(l) }

func (l labelFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return errors.New("give it as key=value")
	}
	l[k] = val
	return nil
}

// listFlag gathers a flag given more than once, or with values joined by commas.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

func (a *app) machinesList(ctx context.Context, args []string) error {
	fs := a.flags("machines ls", "wm machines ls [flags]", "List machines, newest first. In an organisation, WHOSE says whose each is: "+
		"the person who holds it, and their role there.")
	project := fs.String("project", "", "only this project's machines")
	state := fs.String("state", "", "only machines in this `state`: "+strings.Join(api.ListedStates, ", ")+" (failed: the ones that never came up)")
	labels := labelFlag{}
	fs.Var(labels, "label", "only machines with this `key=value` label; repeat for more")
	limit := fs.Int("limit", 0, "show at most this many (0: all)")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	if *state != "" && !slices.Contains(api.States, *state) {
		return usagef("no state %q: the states are %s", *state, strings.Join(api.States, ", "))
	}
	if *limit < 0 {
		return usagef("--limit is 0 or more")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	p := api.ListMachinesParams{State: *state, Labels: labels, Limit: 200}
	if *limit > 0 {
		p.Limit = min(*limit, 200)
	}
	if *project != "" {
		if p.Project, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	list := []api.Machine{}
	for m, err := range c.AllMachines(ctx, p) {
		if err != nil {
			return err
		}
		list = append(list, m)
		if *limit > 0 && len(list) >= *limit {
			break
		}
	}
	return a.printMachines(list)
}

func (a *app) printMachines(list []api.Machine) error {
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, m := range list {
			fmt.Fprintln(a.stdout, m.ID)
		}
		return nil
	}
	now := a.now()
	held := slices.ContainsFunc(list, func(m api.Machine) bool { return m.HeldBy != nil })
	t := a.table(withColumn(held, 5, []string{"ID", "NAME", "STATE", "SIZE", "LOCATION", "AGE"}, "WHOSE")...)
	for _, m := range list {
		t.row(withColumn(held, 5, []string{m.ID, m.Name, m.State, sizeOf(m), place(m.Location), age(m.CreatedAt, now)}, whoseOf(m.HeldBy))...)
	}
	return t.flush()
}

// printMachine is one machine after something was done to it: as a row, its id, or its JSON.
func (a *app) printMachine(m *api.Machine) error {
	if a.json {
		return a.printJSON(m)
	}
	return a.printMachines([]api.Machine{*m})
}

// waiting is the flags of a command that waits for a machine to run.
type waiting struct {
	timeout *time.Duration
	noWait  *bool
}

func waitFlags(fs *flag.FlagSet) waiting {
	return waiting{
		timeout: fs.Duration("timeout", 10*time.Minute, "how long to wait for it to run"),
		noWait:  fs.Bool("no-wait", false, "answer at once, while it may still be starting"),
	}
}

// waitS is how long the API is asked to wait before answering that the machine is still starting.
func (w waiting) waitS() *float64 {
	if *w.noWait {
		return api.Ptr(0.0)
	}
	return api.Ptr(min(max(w.timeout.Seconds(), 0), api.MaxWait.Seconds()))
}

// settle follows a machine that is still starting until it runs, unless --no-wait. A machine still not running
// when the time is up is printed, so its id is known, and the wait's error is returned.
func (a *app) settle(ctx context.Context, c *api.Client, m *api.Machine, w waiting, deadline time.Time) error {
	if m.State != api.StateStarting || *w.noWait {
		return a.printMachine(m)
	}
	ready, err := a.waitFor(ctx, c, m.ID, api.StateRunning, deadline)
	if err != nil {
		if ready != nil {
			_ = a.printMachine(ready)
		}
		return err
	}
	return a.printMachine(ready)
}

// waitFor waits until the machine is in the state, asking the API to wait up to MaxWait at a time.
func (a *app) waitFor(ctx context.Context, c *api.Client, id, state string, deadline time.Time) (*api.Machine, error) {
	for {
		began := time.Now()
		m, err := c.WaitForMachine(ctx, id, state, time.Until(deadline))
		if err == nil || !api.IsCode(err, api.CodeTimeout) {
			return m, err
		}
		cur, gerr := c.GetMachine(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		if cur.State == state {
			return cur, nil
		}
		// (one that failed never comes up: said at once, with why, never waited on to the end)
		if cur.State == api.StateDeleted || cur.State == api.StateFailed || !time.Now().Before(deadline) {
			return cur, err
		}
		// an answer of "not yet" that came back at once is not asked again at once
		if pause := time.Second - time.Since(began); pause > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(min(pause, time.Until(deadline))):
			}
		}
	}
}

func (a *app) machinesCreate(ctx context.Context, args []string) error {
	fs := a.flags("machines create", "wm machines create [flags]", "Make a machine. It is answered when it runs, usually in "+
		"about a second. If that takes longer, wm waits up to --timeout.")
	name := fs.String("name", "", "a name for people (ids are made by the API)")
	project := fs.String("project", "", "its project; the account's first when left out")
	region := fs.String("region", "", "a region to run near (wm regions)")
	gpu := fs.String("gpu", "", "a GPU to give it (wm gpus)")
	cpus := fs.Int("cpus", 0, "its CPUs: 2, 4 or 8, with 2 GB of memory each (2 when left out; not with --gpu)")
	labels := labelFlag{}
	fs.Var(labels, "label", "a `key=value` label; repeat for more")
	var secrets listFlag
	fs.Var(&secrets, "secret", "a secret of the account its commands get, by its `NAME`; repeat for more (wm secrets)")
	internet := fs.String("internet", "", "what it may reach on the internet: `open` (the default), closed, or allowed")
	var allow listFlag
	fs.Var(&allow, "allow", "with --internet allowed, a `NAME` (exact or *.under it) for web, or an IP range (CIDR) for other ports; repeat for more")
	noSleep := fs.Bool("no-sleep", false, "never pause by itself: it runs, and is charged, until it is paused or stopped")
	idleAfter := fs.Duration("idle-after", 0, "pause by itself once nothing has reached it, and its programs have been quiet, this long "+
		"(1m to 24h; 5m when left out)")
	putAway := fs.Duration("put-away", 0, "archive it once nobody has used it this long (1h to 720h)")
	kind := fs.String("type", "", "make it as one of its team's machine types says, by name or id (wm machines types): not with "+
		"--cpus, --gpu, --internet, --no-sleep, --idle-after or --put-away")
	w := waitFlags(fs)
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 0, ""); err != nil {
		return err
	}
	switch {
	case *cpus == 16:
		return usagef("16 CPUs are not offered yet: choose 2, 4 or 8")
	case *cpus != 0 && *cpus != 2 && *cpus != 4 && *cpus != 8:
		return usagef("--cpus is 2, 4 or 8")
	case *cpus != 0 && *gpu != "":
		return usagef("--cpus is for a machine without a GPU: a GPU machine's size comes with it")
	}
	if *noSleep && *idleAfter != 0 {
		return usagef("--idle-after is for a machine that pauses when idle; --no-sleep never does")
	}
	if *idleAfter != 0 && (*idleAfter%time.Second != 0 || *idleAfter < time.Minute || *idleAfter > 24*time.Hour) {
		return usagef("--idle-after is whole seconds from 1m to 24h")
	}
	if *putAway != 0 && (*putAway%time.Second != 0 || *putAway < time.Hour || *putAway > 720*time.Hour) {
		return usagef("--put-away is whole seconds from 1h to 720h")
	}
	if *kind != "" && (*cpus != 0 || *gpu != "" || *internet != "" || len(allow) > 0 || *noSleep || *idleAfter != 0 || *putAway != 0) {
		return usagef("a machine of a type is made as the type says: leave out --cpus, --gpu, --internet, --allow, --no-sleep, --idle-after and --put-away")
	}
	for _, n := range secrets {
		if !api.ValidSecretName(n) {
			return usagef("--secret %q is not a secret's name: capitals, digits and _, such as DATABASE_URL", n)
		}
	}
	network, err := networkFrom(*internet, allow)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*w.timeout)
	mc := api.MachineCreate{Name: *name, Region: *region, GPU: *gpu, CPUs: *cpus, Secrets: secrets, Network: network, WaitS: w.waitS(), Type: *kind}
	if len(labels) > 0 {
		mc.Labels = labels
	}
	if *noSleep {
		never := false
		mc.SleepWhenIdle = &never
	}
	if *idleAfter != 0 {
		after := int(*idleAfter / time.Second)
		mc.IdleAfterS = &after
	}
	if *putAway != 0 {
		after := int(*putAway / time.Second)
		mc.PutAwayAfterS = &after
	}
	if *project != "" {
		if mc.Project, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	m, err := c.CreateMachine(ctx, mc)
	if err != nil {
		return err
	}
	return a.settle(ctx, c, m, w, deadline)
}

func (a *app) machinesGet(ctx context.Context, args []string) error {
	fs := a.flags("machines get", "wm machines get <machine>", "Show one machine.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	c, m, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	got, err := c.GetMachine(ctx, m)
	if err != nil {
		return err
	}
	return a.printDetail(got)
}

func (a *app) printDetail(m *api.Machine) error {
	switch {
	case a.json:
		return a.printJSON(m)
	case a.quiet:
		fmt.Fprintln(a.stdout, m.ID)
		return nil
	}
	now := a.now()
	d := a.detail()
	d.line("id", m.ID)
	d.line("name", m.Name)
	d.line("state", m.State)
	if m.Failure != "" {
		d.line("failure", m.Failure)
	}
	if m.PauseReason != nil {
		d.line("paused", m.PauseReason.Why)
	}
	d.line("project", m.Project)
	if m.MadeBy != nil {
		// in an organisation, who made it and whose it is - it runs within its holder's own limits there while they are
		// in it; in a person's own account nobody else is named, and nothing is said
		d.line("made by", *m.MadeBy)
	}
	if m.HeldBy != nil {
		d.line("whose", whoseOf(m.HeldBy))
	}
	switch {
	case m.Size != nil:
		size := num(m.Size.CPUs) + " CPU, " + num(m.Size.MemoryGB) + " GB memory"
		if m.Size.DiskGB > 0 {
			size += ", " + num(m.Size.DiskGB) + " GB disk"
		}
		d.line("size", size)
	case m.CPUs != nil && m.MemoryGB != nil:
		d.line("size", strconv.Itoa(*m.CPUs)+" CPU, "+num(*m.MemoryGB)+" GB memory")
	}
	if m.GPU != nil {
		d.line("gpu", gpuOf(m.GPU))
	}
	d.line("location", place(m.Location))
	d.line("pauses", pausesOf(m))
	d.line("labels", labelsOf(m.Labels))
	d.line("secrets", strings.Join(m.Secrets, ", "))
	d.line("ai keys", strings.Join(m.AIKeys, ", "))
	d.line("internet", networkLine(m.Network))
	d.line("files encrypted", yesNo(m.FilesSealed))
	d.line("running time", span(m.RunningSeconds))
	if m.DiskBytes > 0 {
		d.line("disk used", humanBytes(float64(m.DiskBytes)))
	}
	if m.CPUCores > 0 {
		d.line("CPUs used", num(float64(int(m.CPUCores*100))/100))
	}
	if m.MemoryBytes > 0 {
		d.line("memory used", humanBytes(float64(m.MemoryBytes)))
	}
	d.line("created", when(m.CreatedAt, now))
	d.line("last used", whenOr(m.LastUsedAt, now, "-"))
	if m.ReadyMS != nil {
		d.line("ready in", num(float64(int(*m.ReadyMS*10))/10)+" ms")
	}
	return d.flush()
}

// machine is the client and the id of the machine named by an id or a name.
func (a *app) machine(ctx context.Context, idOrName string) (*api.Client, string, error) {
	c, err := a.client()
	if err != nil {
		return nil, "", err
	}
	id, err := c.FindMachine(ctx, idOrName)
	return c, id, err
}

// machines is the ids of several machines named by ids or names.
func (a *app) machines(ctx context.Context, names []string) (*api.Client, []string, error) {
	c, err := a.client()
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(names))
	for _, n := range names {
		id, err := c.FindMachine(ctx, n)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	return c, ids, nil
}

func (a *app) machinesRemove(ctx context.Context, args []string) error {
	fs := a.flags("machines rm", "wm machines rm [--yes] <machine>...", "Delete machines, all their files and their backups. This cannot be undone.")
	yes := fs.Bool("yes", false, "delete without being asked")
	fs.BoolVar(yes, "y", false, "delete without being asked")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which machines"); err != nil {
		return err
	}
	c, ids, err := a.machines(ctx, words)
	if err != nil {
		return err
	}
	if !*yes {
		ok, err := a.confirm(fmt.Sprintf("Delete %s with all its files and its backups?", strings.Join(ids, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errNothingDone
		}
	}
	for _, id := range ids {
		if err := c.DeleteMachine(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, id)
	}
	return nil
}

// moveCommand is pause, start, stop or archive, for one machine or several.
func moveCommand(action string) subcommand {
	about := map[string]string{
		"pause":   "Pause machines with their memory kept: nothing runs and no running time is charged; start brings them back where they were.",
		"start":   "Run machines again, from paused, stopped or archived.",
		"stop":    "Stop machines: their files stay, their memory does not.",
		"archive": "Archive machines' files; start brings them back.",
	}[action]
	return func(a *app, ctx context.Context, args []string) error {
		fs := a.flags("machines "+action, "wm machines "+action+" <machine>...", about)
		var w waiting
		if action == "start" {
			w = waitFlags(fs)
		}
		words, err := parse(fs, args)
		if err != nil {
			return err
		}
		if err := count(words, 1, -1, "which machine"); err != nil {
			return err
		}
		c, ids, err := a.machines(ctx, words)
		if err != nil {
			return err
		}
		var out []api.Machine
		for _, id := range ids {
			var m *api.Machine
			switch action {
			case "pause":
				m, err = c.PauseMachine(ctx, id)
			case "stop":
				m, err = c.StopMachine(ctx, id)
			case "archive":
				m, err = c.ArchiveMachine(ctx, id)
			case "start":
				deadline := time.Now().Add(*w.timeout)
				if m, err = c.StartMachine(ctx, id, w.waitS()); err == nil && m.State == api.StateStarting && !*w.noWait {
					var ready *api.Machine
					if ready, err = a.waitFor(ctx, c, id, api.StateRunning, deadline); ready != nil {
						m = ready
					}
				}
			}
			if m != nil {
				out = append(out, *m)
			}
			if err != nil {
				_ = a.printMoved(out)
				return err
			}
		}
		return a.printMoved(out)
	}
}

func (a *app) printMoved(list []api.Machine) error {
	if len(list) == 0 {
		return nil
	}
	if a.json && len(list) == 1 {
		return a.printJSON(list[0])
	}
	return a.printMachines(list)
}

func (a *app) machinesFork(ctx context.Context, args []string) error {
	fs := a.flags("machines fork", "wm machines fork <machine> [flags]", "Fork a machine: a new machine of its own, made from it "+
		"as it is now - or, with --backup, from one of its backups, starting afresh from its files, at "+
		"the original's size or the one --cpus asks. Its name is the original's with -fork after it, and its labels the original's, unless given. "+
		"In an organisation, a member's fork waits for the OK of a full manager of its team, an owner or an admin: wm prints the request, "+
		"and the fork is made once it is approved (wm requests).")
	name := fs.String("name", "", "the fork's name")
	labels := labelFlag{}
	fs.Var(labels, "label", "a `key=value` label for the fork, in place of the original's; repeat for more")
	backup := fs.String("backup", "", "make it from one of the machine's backups: its `id`, or latest for its newest (wm machines backups)")
	cpus := fs.Int("cpus", 0, "with --backup: its CPUs, 2, 4 or 8, with 2 GB of memory each (the original's when left out; "+
		"not for a GPU machine)")
	forWhom := fs.String("for", "", "fork it for a teammate - their @handle, id, e-mail address or GitHub login - cleared before it is theirs; "+
		"it waits for your OK in the dashboard (wm requests)")
	team := fs.String("team", "", "with --for: the team of theirs it goes into, when it is another than its own")
	why := fs.String("why", "", "with --for: why, in your own words, at most 280 characters")
	w := waitFlags(fs)
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine to fork"); err != nil {
		return err
	}
	switch {
	case *cpus == 16:
		return usagef("16 CPUs are not offered yet: choose 2, 4 or 8")
	case *cpus != 0 && *cpus != 2 && *cpus != 4 && *cpus != 8:
		return usagef("--cpus is 2, 4 or 8")
	case *cpus != 0 && *backup == "":
		return usagef("--cpus is only for a fork from a backup (--backup): a fork of the machine as it is now keeps its size")
	}
	if *forWhom != "" {
		if *name != "" || len(labels) > 0 {
			return usagef("a fork for a teammate is named after the machine: leave --name and --label out")
		}
		if *backup != "" {
			return usagef("a fork for a teammate is of the machine as it is now: leave --backup and --cpus out")
		}
		return a.give(ctx, words[0], *forWhom, *team, *why, true)
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*w.timeout)
	mf := api.MachineFork{Backup: *backup, CPUs: *cpus, WaitS: w.waitS()}
	if *name != "" {
		mf.Name = name
	}
	if len(labels) > 0 {
		mf.Labels = labels
	}
	f, err := c.ForkMachine(ctx, id, mf)
	if err != nil {
		return err
	}
	if f.Request != nil {
		// (a member's fork, in an organisation: nothing is made until it is approved, so there is no machine to wait for)
		return a.printRequest(f.Request)
	}
	return a.settle(ctx, c, f.Machine, w, deadline)
}

func (a *app) machinesBackup(ctx context.Context, args []string) error {
	fs := a.flags("machines backup", "wm machines backup <machine> [--note <words>]", "Back up a machine: keep its files as they "+
		"are now, not what runs in it. Make a new machine from the backup later with wm machines fork <machine> --backup <backup>, "+
		"at its size or another (--cpus).")
	note := fs.String("note", "", "a note to know it by, at most 200 characters")
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
	b, err := c.BackupMachine(ctx, id, api.BackupCreate{Note: *note})
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(b)
	}
	return a.printBackups([]api.Backup{*b})
}

func (a *app) machinesBackups(ctx context.Context, args []string) error {
	fs := a.flags("machines backups", "wm machines backups [<machine>]", "List backups, newest first: a machine's, or with no "+
		"machine named, those of every machine you reach. Make a new machine from one with wm machines fork <machine> --backup <backup>.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 1, ""); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var list *api.BackupList
	if len(words) == 0 {
		list, err = c.ListBackups(ctx, 200)
	} else {
		var id string
		if id, err = c.FindMachine(ctx, words[0]); err == nil {
			list, err = c.ListMachineBackups(ctx, id, 200)
		}
	}
	if err != nil {
		return err
	}
	if list.Data == nil {
		list.Data = []api.Backup{}
	}
	return a.printBackups(list.Data)
}

func (a *app) printBackups(list []api.Backup) error {
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, b := range list {
			fmt.Fprintln(a.stdout, b.ID)
		}
		return nil
	}
	t := a.table("BACKUP", "MACHINE", "NOTE", "MADE")
	for _, b := range list {
		t.row(b.ID, b.Machine, b.Note, b.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return t.flush()
}

func (a *app) machinesWait(ctx context.Context, args []string) error {
	fs := a.flags("machines wait", "wm machines wait <machine> [--state <state>] [--timeout 10m]", "Wait until a machine is in a state.")
	state := fs.String("state", api.StateRunning, "the `state` to wait for")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	if !slices.Contains(api.States, *state) {
		return usagef("no state %q: the states are %s", *state, strings.Join(api.States, ", "))
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := a.waitFor(ctx, c, id, *state, time.Now().Add(*timeout))
	if err != nil {
		return err
	}
	return a.printMachine(m)
}

// networkFrom turns --internet and --allow into a policy, checked. Nil when neither is given (the machine is left as
// it is, or made open).
func networkFrom(internet string, allow []string) (*api.Network, error) {
	if internet == "" && len(allow) == 0 {
		return nil, nil
	}
	switch internet {
	case "open", "closed", "allowed":
	case "":
		return nil, usagef("--allow needs --internet allowed")
	default:
		return nil, usagef("--internet is open, closed or allowed")
	}
	if internet != "allowed" && len(allow) > 0 {
		return nil, usagef("--allow is only for --internet allowed")
	}
	n := &api.Network{Internet: internet, Allow: allow}
	if n.Allow == nil {
		n.Allow = []string{}
	}
	return n, nil
}

func (a *app) machinesNetwork(ctx context.Context, args []string) error {
	fs := a.flags("machines network", "wm machines network <machine> --internet open|closed|allowed [--allow NAME]...",
		"Set what a machine may reach on the internet - open, closed, or allowed only to the names and ranges you list. It takes effect at once, without a restart.")
	internet := fs.String("internet", "", "`open`, closed or allowed")
	var allow listFlag
	fs.Var(&allow, "allow", "with allowed, a `NAME` (exact or *.under it) for web, or an IP range (CIDR) for other ports; repeat for more")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which machine"); err != nil {
		return err
	}
	network, err := networkFrom(*internet, allow)
	if err != nil {
		return err
	}
	if network == nil {
		return usagef("say --internet open, closed or allowed")
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.UpdateMachine(ctx, id, api.MachineUpdate{Network: network})
	if err != nil {
		return err
	}
	return a.printDetail(m)
}

func (a *app) machinesLabel(ctx context.Context, args []string) error {
	fs := a.flags("machines label", "wm machines label <machine> key=value... [key-]...", "Set a machine's labels: key=value "+
		"sets one, key- takes one off. Labels not named stay as they are.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, -1, "which machine, and its labels as key=value"); err != nil {
		return err
	}
	changes := map[string]*string{}
	for _, w := range words[1:] {
		if k, ok := strings.CutSuffix(w, "-"); ok && !strings.Contains(w, "=") && k != "" {
			changes[k] = nil
			continue
		}
		k, v, ok := strings.Cut(w, "=")
		if !ok || k == "" {
			return usagef("a label is key=value, or key- to take it off: %q", w)
		}
		changes[k] = api.Ptr(v)
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.UpdateMachine(ctx, id, api.MachineUpdate{Labels: changes})
	if err != nil {
		return err
	}
	return a.printDetail(m)
}

func (a *app) machinesRename(ctx context.Context, args []string) error {
	fs := a.flags("machines rename", "wm machines rename <machine> <name>", "Give a machine another name.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which machine, and its new name"); err != nil {
		return err
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.UpdateMachine(ctx, id, api.MachineUpdate{Name: api.Ptr(words[1])})
	if err != nil {
		return err
	}
	return a.printMachine(m)
}
