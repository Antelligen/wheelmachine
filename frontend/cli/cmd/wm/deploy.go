package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"wheelmachine/frontend/cli/api"
)

// wmHelp is wm's help: Deploy's lines are in it once Deploy opens.
func wmHelp() string {
	if !api.DeployOpen {
		return mainHelp
	}
	return strings.Replace(mainHelp, "\nAccount:\n", "\nDeploy:\n"+
		"  deploy     put an app on the internet from a folder, a repository or an address: wm deploy [<source>]\n"+
		"  deployments\n"+
		"             its address, logs, settings and what it did; redeploy and delete it (wm deployments -h)\n"+
		"\nAccount:\n", 1)
}

const deploymentsHelp = `Usage: wm deployments <command> [arguments] [flags]

  ls                          list deployments, newest first
  get <deployment>            show one deployment
  runs <deployment>           its last runs, newest first
  logs <deployment>           a run's log, or the end of what the app writes (--kind app)
  redeploy <deployment>       fetch and build it again; --files sends new files in place of its last
  set <deployment>            change its settings
  rm <deployment>...          delete deployments and their machines
  repos                       the repositories you may deploy into a team

Make one with wm deploy. A deployment is named by its id (dep_...) or its name.
Run wm deployments <command> -h for a command's flags.
`

var deploymentCommands = map[string]subcommand{
	"ls":       (*app).deploymentsList,
	"get":      (*app).deploymentsGet,
	"runs":     (*app).deploymentsRuns,
	"logs":     (*app).deploymentsLogs,
	"redeploy": (*app).deploymentsRedeploy,
	"set":      (*app).deploymentsSet,
	"rm":       (*app).deploymentsRemove,
	"repos":    (*app).deploymentsRepos,
}

// deployFlags are the flags of a command that waits for a run to end.
func deployFlags(fs *flag.FlagSet) (timeout *time.Duration, noWait *bool) {
	timeout = fs.Duration("timeout", time.Hour, "how long to wait for its run to end")
	noWait = fs.Bool("no-wait", false, "answer at once, while it is still deploying")
	return timeout, noWait
}

// What a run is doing at each step, for a person watching.
var deploySteps = map[string]string{
	"machine": "Getting its machine ready",
	"files":   "Waiting for its files",
	"fetch":   "Fetching its code",
	"unpack":  "Unpacking its files",
	"build":   "Building",
	"start":   "Starting",
	"check":   "Waiting for it to answer",
	"open":    "Opening its address",
	"back":    "Putting the last version back",
}

// say tells a person watching how it goes; never in what a program reads.
func (a *app) say(format string, args ...any) {
	if !a.json && !a.quiet {
		a.tell(format, args...)
	}
}

// tell says what must be known whatever was asked, on standard error, where a program does not read it. What it says may
// be another's words, or a file's name.
func (a *app) tell(format string, args ...any) {
	fmt.Fprintln(a.stderr, clean(fmt.Sprintf(format, args...)))
}

// What a terminal acts on rather than shows: a colour, the clipboard, its title.
var escapeSequence = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)?|[PX^_][^\x1b]*(\x1b\\)?|[ -/]*[0-~])`)

// clean is text from elsewhere (an app's log, what a repository suggests) as a terminal may be given it: its lines and
// tabs, and nothing else that is not meant to be read.
func clean(s string) string {
	s = strings.ReplaceAll(escapeSequence.ReplaceAllString(s, ""), "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return '\n'
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// The folder a word names, or "".
func folderNamed(word string) string {
	if info, err := os.Stat(word); err == nil && info.IsDir() {
		return word
	}
	return ""
}

// The connection of the one repository of this path that the person may deploy into the team.
func deployRepoOf(ctx context.Context, c *api.Client, repo, project string) (string, error) {
	list, err := c.DeployRepos(ctx, project, "")
	if err != nil {
		return "", err
	}
	var found []string
	for _, r := range list {
		if strings.EqualFold(r.Repo, repo) {
			found = append(found, r.Connection)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no repository %s that you may deploy: wm deployments repos lists them, and a folder or an https address works too", repo)
	case 1:
		return found[0], nil
	}
	return "", usagef("several connections have %s: say which with --connection (%s)", repo, strings.Join(found, ", "))
}

// What of a folder stays here, as a person says it.
const deployLeftOutWords = "A folder goes without keys, tokens and settings (.env and the like, .git, .npmrc, .ssh, *.pem), what its .gitignore files " +
	"name, node_modules and links. wm shows what goes first."

// The flags of a command that sends a folder.
func folderFlags(fs *flag.FlagSet) (options func() api.FolderOptions, said func() bool) {
	var exclude listFlag
	fs.Var(&exclude, "exclude", "a folder: leave out what matches this `PATTERN`, written as in a .gitignore; repeat for more")
	noIgnore := fs.Bool("no-gitignore", false, "a folder: send what its .gitignore files name too")
	return func() api.FolderOptions { return api.FolderOptions{Exclude: exclude, NoGitignore: *noIgnore} },
		func() bool { return len(exclude) > 0 || *noIgnore }
}

// What is said of a folder about to go: how much, what is at its top, and what stays. All of it to whoever is about to be
// asked; what stays even to a program that asked for quiet, on standard error, as it can change what the app does (--json
// has it in its answer).
func (a *app) sayWhatGoes(folder string, p *api.PackedFolder, asking bool) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		abs = folder
	}
	say := a.say
	if asking {
		say = a.tell
	}
	say("What goes from %s: %s, %s", abs, plural(p.Files, "file"), sizeWords(p.Bytes))
	say("  %s", strings.Join(topOf(p.Paths), ", "))
	switch {
	case len(p.LeftOut) == 0:
	case a.json && !asking:
	default:
		a.tell("Left out: %s", strings.Join(p.LeftOut, ", "))
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

func sizeWords(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10) + " bytes"
	case n < 1000*1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + " KB"
	}
	return strconv.FormatFloat(float64(n)/(1000*1000), 'f', 1, 64) + " MB"
}

// The most named of what is at a folder's top, before the rest is counted.
const deployTopMost = 20

// What is at a folder's top: each file, and each folder with how many files go from it.
func topOf(paths []string) []string {
	folders := map[string]int{}
	var names []string
	for _, path := range paths {
		top, _, inFolder := strings.Cut(path, "/")
		switch {
		case !inFolder:
			names = append(names, top)
		case folders[top] == 0:
			names = append(names, top+"/")
			folders[top] = 1
		default:
			folders[top]++
		}
	}
	slices.Sort(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		if len(out) == deployTopMost {
			out = append(out, fmt.Sprintf("and %d more", len(names)-deployTopMost))
			break
		}
		if n := folders[strings.TrimSuffix(name, "/")]; strings.HasSuffix(name, "/") {
			name += " (" + plural(n, "file") + ")"
		}
		out = append(out, name)
	}
	return out
}

// What --dry-run answers: what would go of a folder, and what would stay.
func (a *app) printWhatGoes(folder string, o api.FolderOptions) error {
	p, err := api.PackFolder(folder, o)
	if err != nil {
		return err
	}
	defer p.Close()
	if a.json {
		return a.printJSON(struct {
			Files   int      `json:"files"`
			Bytes   int64    `json:"bytes"`
			Paths   []string `json:"paths"`
			LeftOut []string `json:"left_out"`
		}{p.Files, p.Bytes, p.Paths, append([]string{}, p.LeftOut...)})
	}
	for _, path := range p.Paths {
		fmt.Fprintln(a.stdout, clean(path))
	}
	if len(p.LeftOut) > 0 {
		a.tell("Left out: %s", strings.Join(p.LeftOut, ", "))
	}
	a.say("Nothing was sent: %s, %s would go.", plural(p.Files, "file"), sizeWords(p.Bytes))
	return nil
}

// A deployment of that name exists: another would be another machine, charged, and a name that no longer says which.
func deployedAlready(d *api.Deployment, folder string) error {
	how := "wm deployments redeploy " + d.ID
	if d.Source.Kind == "upload" && folder != "" {
		how += " --files " + strconv.Quote(folder)
	}
	return usagef("a deployment named %s is there already (%s, %s): %s deploys it again as it is set, wm deployments set %s changes how it builds and starts first, and --name makes another",
		d.Name, d.ID, d.State, how, d.ID)
}

func (a *app) deployCmd(ctx context.Context, args []string) error {
	fs := a.flags("deploy", "wm deploy [<folder, repository or address>] [flags]", "Put an app on the internet, on a machine of its "+
		"own, at a public address. It comes from a folder here (this one when left out), a repository of your connections (acme/shop), or a public "+
		"repository's https address. wm reads how it builds and starts from its files, unless you say, and waits until it is live, or says why "+
		"it is not. It is charged "+
		"as any machine. "+deployLeftOutWords+" Give its secrets with --secret.")
	name := fs.String("name", "", "its name and its machine's; its folder's, repository's or address's last part when left out")
	project := fs.String("project", "", "its team; your first when left out")
	connection := fs.String("connection", "", "the code connection its repository is in (code_...), when several have one of that name")
	branch := fs.String("branch", "", "the branch to build; its repository's default when left out")
	build := fs.String("build", "", "what builds it, run in its code's folder; read from its files when left out")
	start := fs.String("start", "", "what starts it, with its port in $PORT and HOST as 0.0.0.0; read from its files when left out")
	port := fs.Int("port", 0, "the port it listens on; read from its files when left out")
	cpus := fs.Int("cpus", 0, "its CPUs: 2, 4 or 8, with 2 GB of memory each (2 when left out)")
	kind := fs.String("type", "", "make its machine as one of its team's machine types says, by name or id, in place of --cpus")
	region := fs.String("region", "", "a region to run near (wm regions)")
	var secrets listFlag
	fs.Var(&secrets, "secret", "a secret of the account it gets, by its `NAME`; repeat for more (wm secrets)")
	keepAwake := fs.Bool("always-on", false, "never sleep: it runs, and is charged, all the time")
	options, optionsSaid := folderFlags(fs)
	dryRun := fs.Bool("dry-run", false, "a folder: say what would go, and send nothing")
	yes := fs.Bool("yes", false, "send a folder without being asked")
	fs.BoolVar(yes, "y", false, "send a folder without being asked")
	timeout, noWait := deployFlags(fs)
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 0, 1, "which folder, repository or address"); err != nil {
		return err
	}
	said := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { said[f.Name] = true })
	word := "."
	if len(words) == 1 {
		word = words[0]
	}
	if *port < 0 || *port > 65535 {
		return usagef("a port is a number from 1 to 65535")
	}
	for _, n := range secrets {
		if !api.ValidSecretName(n) {
			return usagef("--secret is a secret's name and not its value: capitals, digits and _, such as DATABASE_URL (wm secrets set keeps its value)")
		}
	}
	in := api.DeploymentCreate{Name: *name, Branch: *branch, CPUs: *cpus, Type: *kind, Region: *region, Secrets: secrets, KeepAwake: *keepAwake}
	folder := ""
	switch {
	case strings.Contains(word, "://") || strings.HasPrefix(word, "git@"):
		in.Source.URL = word
	case folderNamed(word) != "":
		folder = word
	case strings.HasPrefix(word, ".") || filepath.IsAbs(word) || !strings.Contains(word, "/"):
		return usagef("no folder %s here, and a repository is named as acme/shop", word)
	default:
		in.Source.Repo, in.Source.Connection = word, *connection
	}
	if *connection != "" && in.Source.Repo == "" {
		return usagef("--connection is for a repository of a connection")
	}
	if folder == "" && (optionsSaid() || *dryRun) {
		return usagef("--exclude, --no-gitignore and --dry-run are for a folder")
	}
	if folder != "" {
		if *branch != "" {
			return usagef("--branch is for a repository: files have none")
		}
		if err := api.FolderRefused(folder, a.getenv); err != nil {
			return usagef("%v", err)
		}
		if *dryRun {
			return a.printWhatGoes(folder, options())
		}
		// (what goes from the folder you happen to be in is asked for outright, by name or with --yes, where nobody is there to be asked)
		if len(words) == 0 && !*yes && !isTerminal(a.stdin) {
			return usagef("add --yes to send the folder you are in, or name the folder")
		}
		in.Source.Upload = true
		if in.Name == "" {
			in.Name = api.FolderName(folder)
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if *project != "" {
		if in.Project, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	if in.Source.Repo != "" && *connection == "" {
		if in.Source.Connection, err = deployRepoOf(ctx, c, word, in.Project); err != nil {
			return err
		}
	}
	named := in.Name
	if named == "" {
		named = api.SourceName(in.Source.Repo, in.Source.URL)
	}
	there, err := c.DeploymentNamed(ctx, named)
	if err != nil {
		return err
	}
	if there != nil {
		return deployedAlready(there, folder)
	}
	var packed *api.PackedFolder
	asking := folder != "" && !*yes && isTerminal(a.stdin)
	if folder != "" {
		if packed, err = api.PackFolder(folder, options()); err != nil {
			return err
		}
		defer packed.Close()
		a.sayWhatGoes(folder, packed, asking)
		a.deployLeftOut = append([]string{}, packed.LeftOut...)
	}
	in.Build, in.Start, in.Port = *build, *start, *port
	if !said["build"] || !said["start"] || !said["port"] {
		ask := api.DeployDetect{Source: &in.Source, Branch: *branch}
		if folder != "" {
			ask = api.FolderDetect(folder, options())
		}
		guess, err := c.DetectDeployment(ctx, ask)
		if err != nil {
			return err
		}
		if !said["build"] {
			in.Build = guess.Build
		}
		if !said["start"] {
			in.Start = guess.Start
		}
		if !said["port"] {
			in.Port = guess.Port
		}
		if strings.TrimSpace(in.Start) == "" || in.Port == 0 {
			return usagef("wm could not read how it starts from its files: say with --start and --port")
		}
		a.say("Build: %s\nStart: %s\nPort: %d", or(in.Build, "nothing"), in.Start, in.Port)
		if guess.Note != nil {
			a.say("%s", *guess.Note)
		}
	}
	if asking {
		ok, err := a.confirm("Send these files, and put them at a public address?")
		if err != nil {
			return err
		}
		if !ok {
			return errNothingDone
		}
	}
	d, err := c.CreateDeployment(ctx, in)
	if err != nil {
		return err
	}
	// (told at once and whatever was asked: from here on there is a machine, and the next try must not make another)
	a.tell("Made %s: %s", d.Name, d.ID)
	if err := a.sendAndFollow(ctx, c, d, packed, folder, *timeout, *noWait); err != nil {
		var ended *exitError
		if errors.As(err, &ended) || strings.Contains(err.Error(), d.ID) {
			return err
		}
		return fmt.Errorf("%w (%s is made: wm deployments get %s says how it stands)", err, d.ID, d.ID)
	}
	return nil
}

// The files sent, if there are any, once its machine is ready for them; and its run followed to its end.
func (a *app) sendAndFollow(ctx context.Context, c *api.Client, d *api.Deployment, packed *api.PackedFolder, folder string, timeout time.Duration, noWait bool) error {
	if packed == nil {
		return a.endOfRun(ctx, c, d, timeout, noWait, false)
	}
	sent, err := a.sendFolder(ctx, c, d.ID, packed, timeout)
	if err != nil {
		return fmt.Errorf("%w (%s is made and waits for its files: wm deployments redeploy %s --files %s)", err, d.ID, d.ID, strconv.Quote(folder))
	}
	return a.endOfRun(ctx, c, sent, timeout, noWait, true)
}

// The files sent once its machine is ready for them, and how that goes said.
func (a *app) sendFolder(ctx context.Context, c *api.Client, id string, packed *api.PackedFolder, timeout time.Duration) (*api.Deployment, error) {
	told := false
	return c.SendFolder(ctx, id, packed, timeout, deployPollEvery, func(d *api.Deployment) {
		if d.Run == nil || d.Run.EndedAt != nil || d.Run.Step != api.DeployStepMachine {
			a.say("Sending %s...", plural(packed.Files, "file"))
		} else if !told {
			told = true
			a.say("%s...", deploySteps[api.DeployStepMachine])
		}
	})
}

// endOfRun follows a run until it ends, unless asked not to, and says how it ended. filesIn: its files have just been sent,
// so it no longer waits for them, whatever the answer to sending them says.
func (a *app) endOfRun(ctx context.Context, c *api.Client, d *api.Deployment, timeout time.Duration, noWait, filesIn bool) error {
	if noWait {
		return a.printDeployment(d)
	}
	deadline := time.Now().Add(timeout)
	step := ""
	for d.Run != nil && d.Run.EndedAt == nil {
		if d.Run.Step != step {
			step = d.Run.Step
			if !filesIn || step != "files" {
				a.say("%s...", or(deploySteps[step], step))
			}
		}
		if !time.Now().Before(deadline) {
			_ = a.printDeployment(d)
			return fmt.Errorf("it is still deploying after %s: follow it with wm deployments get %s", timeout, d.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(deployPollEvery):
		}
		var err error
		if d, err = c.GetDeployment(ctx, d.ID); err != nil {
			return err
		}
	}
	if err := a.printDeployment(d); err != nil {
		return err
	}
	if d.Run != nil && d.Run.State != "live" {
		a.say("See what it wrote: wm deployments logs %s", d.ID)
		return &exitError{code: 1}
	}
	return nil
}

// How often a run being followed is looked at.
var deployPollEvery = 2 * time.Second

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// printDeployment is one deployment: its fields, its id, or its JSON.
func (a *app) printDeployment(d *api.Deployment) error {
	switch {
	case a.json:
		if a.deployLeftOut != nil {
			return a.printJSON(struct {
				*api.Deployment
				LeftOut []string `json:"left_out"`
			}{d, a.deployLeftOut})
		}
		return a.printJSON(d)
	case a.quiet:
		fmt.Fprintln(a.stdout, d.ID)
		return nil
	}
	out := a.detail()
	out.line("ID", d.ID)
	out.line("Name", d.Name)
	out.line("State", d.State)
	out.line("Address", text(d.URL))
	switch d.Source.Kind {
	case "repo":
		out.line("Repository", text(d.Source.Repo))
	case "url":
		out.line("Repository", text(d.Source.URL))
	case "upload":
		out.line("Files", "sent")
	}
	if d.Branch != nil {
		out.line("Branch", *d.Branch)
	}
	if d.Commit != nil {
		out.line("Commit", *d.Commit)
	}
	out.line("Port", strconv.Itoa(d.Port))
	if d.Machine != nil {
		out.line("Machine", *d.Machine)
	}
	if d.KeepAwake {
		out.line("Always on", "yes")
	}
	if len(d.Secrets) > 0 {
		out.line("Secrets", strings.Join(d.Secrets, ", "))
	}
	if d.Run != nil && d.Run.EndedAt == nil && d.Run.Note != nil {
		out.line("Now", *d.Run.Note)
	}
	if d.Run != nil && d.Run.Failure != nil {
		out.line("Failure", *d.Run.Failure)
	}
	return out.flush()
}

func (a *app) deploymentsList(ctx context.Context, args []string) error {
	fs := a.flags("deployments ls", "wm deployments ls [flags]", "List the deployments you see, newest first.")
	project := fs.String("project", "", "only this team's deployments")
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
	if *project != "" {
		if id, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	list, err := c.ListDeployments(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if list == nil {
			list = []api.Deployment{}
		}
		return a.printJSON(list)
	case a.quiet:
		for _, d := range list {
			fmt.Fprintln(a.stdout, d.ID)
		}
		return nil
	}
	now := a.now()
	t := a.table("ID", "NAME", "STATE", "ADDRESS", "AGE")
	for _, d := range list {
		t.row(d.ID, d.Name, d.State, text(d.URL), age(d.CreatedAt, now))
	}
	return t.flush()
}

// deployment is the client and the id of a deployment named by its id or its name.
func (a *app) deployment(ctx context.Context, word string) (*api.Client, string, error) {
	c, err := a.client()
	if err != nil {
		return nil, "", err
	}
	id, err := c.FindDeployment(ctx, word)
	return c, id, err
}

func (a *app) deploymentsGet(ctx context.Context, args []string) error {
	fs := a.flags("deployments get", "wm deployments get <deployment>", "Show a deployment: its state, its address and its "+
		"current or last run.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which deployment"); err != nil {
		return err
	}
	c, id, err := a.deployment(ctx, words[0])
	if err != nil {
		return err
	}
	d, err := c.GetDeployment(ctx, id)
	if err != nil {
		return err
	}
	return a.printDeployment(d)
}

func (a *app) deploymentsRuns(ctx context.Context, args []string) error {
	fs := a.flags("deployments runs", "wm deployments runs <deployment>", "List a deployment's last runs, newest first.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which deployment"); err != nil {
		return err
	}
	c, id, err := a.deployment(ctx, words[0])
	if err != nil {
		return err
	}
	runs, err := c.DeploymentRuns(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if runs == nil {
			runs = []api.DeployRun{}
		}
		return a.printJSON(runs)
	case a.quiet:
		for _, r := range runs {
			fmt.Fprintln(a.stdout, r.ID)
		}
		return nil
	}
	now := a.now()
	t := a.table("ID", "WHY", "STATE", "STEP", "COMMIT", "AGE", "FAILURE")
	for _, r := range runs {
		t.row(r.ID, r.Why, r.State, r.Step, text(r.Commit), age(r.CreatedAt, now), text(r.Failure))
	}
	return t.flush()
}

func (a *app) deploymentsLogs(ctx context.Context, args []string) error {
	fs := a.flags("deployments logs", "wm deployments logs <deployment> [--kind build|app] [--run <run>]", "Print a deployment's "+
		"log: a run's own, fetching its code and building it, or the end of what the app itself writes (--kind app), which wakes its machine. "+
		"Only the person who deployed it can.")
	kind := fs.String("kind", "build", "`build`, a run's own log, or app, what the app writes")
	run := fs.String("run", "", "with build, a run's own (dpr_...); its last run's when left out")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which deployment"); err != nil {
		return err
	}
	if *kind != "build" && *kind != "app" {
		return usagef("--kind is build or app, not %q", *kind)
	}
	c, id, err := a.deployment(ctx, words[0])
	if err != nil {
		return err
	}
	log, err := c.DeploymentLog(ctx, id, *kind, *run)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(log)
	}
	// (an app's log holds what anyone who visits it can make it write: --json gives it as it is)
	text := clean(log.Text)
	fmt.Fprint(a.stdout, text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(a.stdout)
	}
	return nil
}

func (a *app) deploymentsRedeploy(ctx context.Context, args []string) error {
	fs := a.flags("deployments redeploy", "wm deployments redeploy <deployment> [--files <folder>] [flags]", "Fetch a deployment's "+
		"branch as it is now, or use its files again, or send new ones (--files), and build it. A build that fails leaves the live version "+
		"running. Once built, the live version stops and the new one starts, and one that does not answer is replaced by the last one.")
	files := fs.String("files", "", "a `folder` to send in place of its last files")
	options, optionsSaid := folderFlags(fs)
	yes := fs.Bool("yes", false, "send the folder without being asked")
	fs.BoolVar(yes, "y", false, "send the folder without being asked")
	timeout, noWait := deployFlags(fs)
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which deployment"); err != nil {
		return err
	}
	if *files == "" && optionsSaid() {
		return usagef("--exclude and --no-gitignore go with --files")
	}
	if *files != "" {
		if err := api.FolderRefused(*files, a.getenv); err != nil {
			return usagef("%v", err)
		}
	}
	c, id, err := a.deployment(ctx, words[0])
	if err != nil {
		return err
	}
	var d *api.Deployment
	filesIn := false
	if *files != "" {
		// (that it is there, and that Deploy is open, are said before a folder is packed or anything is said of it)
		if _, err := c.GetDeployment(ctx, id); err != nil {
			return err
		}
		packed, err := api.PackFolder(*files, options())
		if err != nil {
			return err
		}
		defer packed.Close()
		asking := !*yes && isTerminal(a.stdin)
		a.sayWhatGoes(*files, packed, asking)
		a.deployLeftOut = append([]string{}, packed.LeftOut...)
		if asking {
			ok, err := a.confirm("Send these files, and put them at its public address?")
			if err != nil {
				return err
			}
			if !ok {
				return errNothingDone
			}
		}
		if d, err = a.sendFolder(ctx, c, id, packed, *timeout); err != nil {
			return err
		}
		filesIn = true
	} else if d, err = c.Redeploy(ctx, id); err != nil {
		return err
	}
	return a.endOfRun(ctx, c, d, *timeout, *noWait, filesIn)
}

func (a *app) deploymentsSet(ctx context.Context, args []string) error {
	fs := a.flags("deployments set", "wm deployments set <deployment> [flags]", "Change a deployment's settings. Its name and its "+
		"machine's change at once, as does --always-on. Its branch, commands and port apply from its next run: the live version stays as it is "+
		"until it is redeployed. Only the person who deployed it can.")
	name := fs.String("name", "", "its name and its machine's")
	branch := fs.String("branch", "", "the branch to build")
	build := fs.String("build", "", "what builds it")
	start := fs.String("start", "", "what starts it")
	port := fs.Int("port", 0, "the port it listens on; changing it changes its address")
	keepAwake := fs.Bool("always-on", false, "never sleep; --always-on=false lets it sleep once nobody visits and its app is quiet")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "which deployment"); err != nil {
		return err
	}
	var change api.DeploymentChange
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			change.Name = name
		case "branch":
			change.Branch = branch
		case "build":
			change.Build = build
		case "start":
			change.Start = start
		case "port":
			change.Port = port
		case "always-on":
			change.KeepAwake = keepAwake
		}
	})
	if change == (api.DeploymentChange{}) {
		return usagef("say what to change: see wm deployments set -h")
	}
	if change.Port != nil && (*port < 1 || *port > 65535) {
		return usagef("a port is a number from 1 to 65535")
	}
	c, id, err := a.deployment(ctx, words[0])
	if err != nil {
		return err
	}
	d, err := c.UpdateDeployment(ctx, id, change)
	if err != nil {
		return err
	}
	return a.printDeployment(d)
}

func (a *app) deploymentsRemove(ctx context.Context, args []string) error {
	fs := a.flags("deployments rm", "wm deployments rm [--yes] <deployment>...", "Delete deployments, and their machines with all "+
		"their files. Their addresses answer nothing from then on. This cannot be undone.")
	yes := fs.Bool("yes", false, "delete without being asked")
	fs.BoolVar(yes, "y", false, "delete without being asked")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which deployments"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(words))
	for _, word := range words {
		id, err := c.FindDeployment(ctx, word)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if !*yes {
		ok, err := a.confirm(fmt.Sprintf("Delete %s with its machine and all its files?", strings.Join(ids, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errNothingDone
		}
	}
	for _, id := range ids {
		if err := c.DeleteDeployment(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, id)
	}
	return nil
}

func (a *app) deploymentsRepos(ctx context.Context, args []string) error {
	fs := a.flags("deployments repos", "wm deployments repos [flags]", "List the repositories you may deploy into a team, the most "+
		"recently pushed to first. SHARED says the team has it already; if not, deploying it gives it to the team to read first.")
	project := fs.String("project", "", "the team; your first when left out")
	connection := fs.String("connection", "", "one connection's alone (code_...)")
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
	if *project != "" {
		if id, err = c.FindProject(ctx, *project); err != nil {
			return err
		}
	}
	list, err := c.DeployRepos(ctx, id, *connection)
	if err != nil {
		return err
	}
	switch {
	case a.json:
		if list == nil {
			list = []api.DeployRepo{}
		}
		return a.printJSON(list)
	case a.quiet:
		for _, r := range list {
			fmt.Fprintln(a.stdout, r.Repo)
		}
		return nil
	}
	t := a.table("REPOSITORY", "PROVIDER", "CONNECTION", "SHARED")
	for _, r := range list {
		shared := "no"
		if r.Shared {
			shared = "yes"
		}
		t.row(r.Repo, r.Provider, r.Connection, shared)
	}
	return t.flush()
}
