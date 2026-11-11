package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/term"
)

// app is one run of wm: its streams and environment (so tests run it in-process), and the flags every command takes.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	now            func() time.Time   // for showing ages; waits use the real clock
	options        []api.Option       // added to the client's own (tests)
	open           func(string) error // opens an address in a browser; nil: the system's own (browser.go)

	json, quiet bool

	deployLeftOut []string // what a folder sent left out, for what is answered of it (nil: no folder was sent)

	in    *bufio.Reader // stdin, a line at a time, when asking
	c     *api.Client
	fs    *flag.FlagSet // the command's flags, for its help
	usage string
	about string
}

// A command that was not given right: exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return &usageError{msg: fmt.Sprintf(format, args...)} }

// An exit code of its own: a command run in a machine ended with it.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// errHelp: help was asked for and given.
var errHelp = errors.New("help")

func (a *app) run(ctx context.Context, args []string) int {
	err := a.dispatch(ctx, args)
	var ee *exitError
	var ue *usageError
	var ae *api.Error
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, errHelp):
		a.printHelp()
		return 0
	case errors.Is(err, context.Canceled):
		return 130
	case errors.As(err, &ue):
		fmt.Fprintf(a.stderr, "wm: %s\n", ue.msg)
		if a.usage != "" {
			fmt.Fprintf(a.stderr, "usage: %s\n", a.usage)
		}
		return 2
	case errors.As(err, &ae):
		fmt.Fprintf(a.stderr, "wm: %s\n", err)
		if hint := a.hint(ae); hint != "" {
			fmt.Fprintf(a.stderr, "wm: %s\n", hint)
		}
		return 1
	}
	fmt.Fprintf(a.stderr, "wm: %s\n", err)
	return 1
}

// What to do about an error, when there is something to do.
func (a *app) hint(e *api.Error) string {
	switch e.Code {
	case api.CodeUnauthorized, api.CodeKeyRevoked, api.CodeKeyExpired:
		if a.getenv(api.EnvAPIKey) != "" {
			return "the key is the one in " + api.EnvAPIKey + ": set a working one, or unset it and run wm login"
		}
		return "sign in again: wm login"
	}
	return ""
}

func (a *app) dispatch(ctx context.Context, args []string) error {
flags:
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		switch strings.TrimLeft(args[0], "-") {
		case "":
			args = args[1:]
			break flags
		case "json":
			a.json = true
		case "q", "quiet":
			a.quiet = true
		case "version", "v":
			fmt.Fprintf(a.stdout, "wm %s\n", api.Version)
			return nil
		case "h", "help":
			fmt.Fprint(a.stdout, wmHelp())
			return nil
		default:
			return usagef("no flag %s here: see wm -h", args[0])
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprint(a.stderr, wmHelp())
		return &exitError{code: 2}
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help":
		return a.help(rest)
	case "version":
		fmt.Fprintf(a.stdout, "wm %s\n", api.Version)
		return nil
	case "login":
		return a.login(ctx, rest)
	case "logout":
		return a.logout(rest)
	case "me":
		return a.me(ctx, rest)
	case "usage":
		return a.usageCmd(ctx, rest)
	case "credit":
		return a.creditCmd(ctx, rest)
	case "limits":
		return a.limitsCmd(ctx, rest)
	case "code":
		return a.codeCmd(ctx, rest)
	case "folders":
		return a.foldersCmd(ctx, rest)
	case "orgs":
		return a.orgs(ctx, rest)
	case "audit":
		return a.audit(ctx, rest)
	case "machines":
		return a.group(ctx, "machines", machinesHelp, machineCommands, rest)
	case "projects":
		return a.group(ctx, "projects", projectsHelp, projectCommands, rest)
	case "requests":
		return a.requests(ctx, rest)
	case "keys":
		return a.group(ctx, "keys", keysHelp, keyCommands, rest)
	case "secrets":
		return a.group(ctx, "secrets", secretsHelp, secretCommands, rest)
	case "deploy":
		return a.deployCmd(ctx, rest)
	case "deployments":
		return a.group(ctx, "deployments", deploymentsHelp, deploymentCommands, rest)
	case "ports":
		return a.group(ctx, "ports", portsHelp, portCommands, rest)
	case "apps":
		return a.group(ctx, "apps", appsHelp, appCommands, rest)
	case "models":
		return a.models(ctx, rest)
	case "exec":
		return a.exec(ctx, rest)
	case "sh":
		return a.sh(ctx, rest)
	case "ssh":
		return a.ssh(ctx, rest)
	case "ssh-config":
		return a.sshConfig(ctx, rest)
	case "cp":
		return a.cp(ctx, rest)
	case "top":
		return a.top(ctx, rest)
	case "metrics":
		return a.metrics(ctx, rest)
	case "gpus":
		return a.gpus(ctx, rest)
	case "regions":
		return a.regions(ctx, rest)
	}
	return usagef("no command %q: see wm -h", name)
}

func (a *app) help(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, wmHelp())
		return nil
	}
	switch args[0] {
	case "machines":
		fmt.Fprint(a.stdout, machinesHelp)
	case "projects":
		fmt.Fprint(a.stdout, projectsHelp)
	case "keys":
		fmt.Fprint(a.stdout, keysHelp)
	case "secrets":
		fmt.Fprint(a.stdout, secretsHelp)
	case "ports":
		fmt.Fprint(a.stdout, portsHelp)
	case "apps":
		fmt.Fprint(a.stdout, appsHelp)
	case "models":
		fmt.Fprint(a.stdout, modelsHelp)
	case "limits":
		fmt.Fprint(a.stdout, limitsHelp)
	case "code":
		fmt.Fprint(a.stdout, codeHelp)
	case "folders":
		fmt.Fprint(a.stdout, foldersHelp)
	default:
		return a.dispatch(context.Background(), []string{args[0], "-h"})
	}
	return nil
}

type subcommand func(a *app, ctx context.Context, args []string) error

// group runs a command of a group: wm machines ls.
func (a *app) group(ctx context.Context, name, help string, cmds map[string]subcommand, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, help)
		return &exitError{code: 2}
	}
	switch args[0] {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(a.stdout, help)
		return nil
	}
	if run, ok := cmds[args[0]]; ok {
		return run(a, ctx, args[1:])
	}
	return usagef("no command %q in wm %s: see wm %s -h", args[0], name, name)
}

// flags is a command's flag set, with the flags every command takes.
func (a *app) flags(name, usage, about string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.BoolVar(&a.json, "json", a.json, "print JSON")
	fs.BoolVar(&a.quiet, "quiet", a.quiet, "print ids only")
	fs.BoolVar(&a.quiet, "q", a.quiet, "print ids only")
	a.fs, a.usage, a.about = fs, usage, about
	return fs
}

func (a *app) printHelp() {
	fmt.Fprintf(a.stdout, "Usage: %s\n", a.usage)
	if a.about != "" {
		fmt.Fprintf(a.stdout, "\n%s\n", wrap(a.about, 80))
	}
	if a.fs == nil {
		return
	}
	fmt.Fprintln(a.stdout, "\nFlags:")
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	a.fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "q" {
			return
		}
		kind, usage := flag.UnquoteUsage(f)
		name := "--" + f.Name
		if f.Name == "quiet" {
			name = "-q, --quiet"
		}
		if kind != "" {
			name += " " + kind
		}
		// a zero is no default of its own: the flag left out means what its words say (--idle-after: 30m)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "0s" && f.DefValue != "[]" {
			usage += " (" + f.DefValue + ")"
		}
		fmt.Fprintf(tw, "  %s\t%s\n", name, usage)
	})
	_ = tw.Flush()
}

// parse reads flags wherever they come among the words, as people type them (wm machines get web --json); after
// `--` nothing is a flag.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, flagError(err)
		}
		rest := fs.Args()
		if used := len(args) - len(rest); used > 0 && args[used-1] == "--" {
			return append(words, rest...), nil
		}
		if len(rest) == 0 {
			return words, nil
		}
		words, args = append(words, rest[0]), rest[1:]
	}
}

// parseHead is for commands whose last words are a command to run: flags, the machine, then the command - after
// `--` when flags come between the two.
func parseHead(fs *flag.FlagSet, args []string) (machine string, words []string, err error) {
	if err := fs.Parse(args); err != nil {
		return "", nil, flagError(err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil, usagef("say which machine")
	}
	machine, rest = rest[0], rest[1:]
	switch {
	case len(rest) > 0 && rest[0] == "--":
		rest = rest[1:]
	case len(rest) > 0 && strings.HasPrefix(rest[0], "-"):
		i := slices.Index(rest, "--")
		if i < 0 {
			return "", nil, usagef("put the command after --")
		}
		if err := fs.Parse(rest[:i]); err != nil {
			return "", nil, flagError(err)
		}
		if fs.NArg() > 0 {
			return "", nil, usagef("put the command after --, not %q", fs.Arg(0))
		}
		rest = rest[i+1:]
	}
	return machine, rest, nil
}

func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return errHelp
	}
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: "); ok {
		msg = "no flag " + name + " here"
	} else if name, ok := strings.CutPrefix(msg, "flag needs an argument: "); ok {
		msg = "the flag " + name + " needs a value"
	}
	return &usageError{msg: msg}
}

// count checks how many words a command was given.
func count(words []string, least, most int, what string) error {
	switch {
	case len(words) < least:
		return usagef("say %s", what)
	case most >= 0 && len(words) > most:
		return usagef("too many words: %s", strings.Join(words[most:], " "))
	}
	return nil
}

// client is made once a run, from the environment.
func (a *app) client() (*api.Client, error) {
	if a.c != nil {
		return a.c, nil
	}
	opts := []api.Option{api.WithUserAgent("wm/" + api.Version)}
	if isTerminal(a.stderr) && !a.quiet {
		opts = append(opts, api.WithRetryHook(func(e api.RetryEvent) {
			if e.Wait >= time.Second {
				fmt.Fprintf(a.stderr, "wm: %s; trying again in %s\n", reason(e.Err), e.Wait.Round(time.Second))
			}
		}))
	}
	c, err := api.NewFromEnv(a.getenv, append(opts, a.options...)...)
	if err != nil {
		return nil, err
	}
	a.c = c
	return c, nil
}

// reason is why a try failed, in a few words.
func reason(err error) string {
	var e *api.Error
	if errors.As(err, &e) {
		return or(e.Detail, e.Code)
	}
	return "the API could not be reached"
}

// line reads a line of stdin.
func (a *app) line() (string, error) {
	if a.in == nil {
		a.in = bufio.NewReader(a.stdin)
	}
	s, err := a.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// confirm asks a yes or no question at a terminal; anywhere else, --yes is needed instead.
func (a *app) confirm(question string) (bool, error) {
	if !isTerminal(a.stdin) {
		return false, usagef("add --yes to do this without being asked")
	}
	fmt.Fprintf(a.stderr, "%s [y/N] ", question)
	answer, err := a.line()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(f)
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
