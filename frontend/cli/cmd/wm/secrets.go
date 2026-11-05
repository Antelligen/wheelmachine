package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/term"
)

var secretCommands = map[string]subcommand{
	"ls":  (*app).secretsList,
	"set": (*app).secretsSet,
	"rm":  (*app).secretsRemove,
}

// What wm secrets set says when it is given more than a name: never the words it was given, one of which may be the
// value itself.
const valueNotOnTheLine = "wm secrets set takes the secret's name alone (and --inside, --only-given, --json or -q): the " +
	"value is read from standard input, or asked for at a terminal, never taken from the command line"

func (a *app) secretsSet(ctx context.Context, args []string) error {
	fs := a.flags("secrets set", "wm secrets set [--inside] [--only-given] <NAME>", "Set a secret, or set it anew: every "+
		"command run in a machine given it gets it as an environment variable of that name, from its next command on. "+
		"An AI key - a secret named as its provider's agents read it, such as ANTHROPIC_API_KEY, OPENAI_API_KEY or "+
		"GEMINI_API_KEY - is kept outside the machines and is on every one of them: each holds a placeholder in its "+
		"place, and the key is sent only to its provider's hosts. The value is never taken from the command line, where the "+
		"shell's history and the process list would keep it: at a terminal it is asked for and not shown as you type; "+
		"otherwise it is all of standard input (wm secrets set DATABASE_URL < file), one newline at its end left off. The "+
		"API never shows it again. Set again, a secret is kept as these flags say, whatever it was before.")
	inside := fs.Bool("inside", false, "keep it an ordinary secret, its value in the machines given it, even under an AI key's name: for a tool that cannot work with a placeholder")
	onlyGiven := fs.Bool("only-given", false, "an AI key on the machines given it alone, not on every machine")
	words, err := parse(fs, args)
	if errors.Is(err, errHelp) {
		return err
	}
	if err != nil || len(words) > 1 {
		return usagef(valueNotOnTheLine)
	}
	if len(words) == 0 {
		return usagef("say the secret's name: wm secrets set DATABASE_URL")
	}
	name := words[0]
	if !api.ValidSecretName(name) {
		return usagef("%q is not a secret's name: capitals, digits and _, not beginning with a digit, such as DATABASE_URL", name)
	}
	value, err := a.readSecret(name)
	if err != nil {
		return err
	}
	switch {
	case value == "":
		return usagef("no value given")
	case len(value) > api.SecretValueMax:
		return usagef("a secret's value is at most 32 KiB")
	case !utf8.ValidString(value):
		return usagef("a secret's value is text (UTF-8): put other bytes in base64 first")
	case strings.ContainsRune(value, 0):
		return usagef("a secret's value cannot hold a NUL: an environment variable cannot carry one")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	s, err := c.SetSecretAs(ctx, name, value, api.SecretSet{Inside: *inside, OnlyGiven: *onlyGiven})
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(s)
	case a.quiet:
		fmt.Fprintln(a.stdout, s.Name)
	case s.Outside != nil && s.EveryMachine:
		fmt.Fprintf(a.stdout, "Set %s%s: on every machine. Sent only to %s. The key never enters a machine.\n", s.Name, aiKeyWord(s), strings.Join(s.Outside.Hosts, ", "))
	case s.Outside != nil && len(s.Machines) == 0:
		fmt.Fprintf(a.stdout, "Set %s%s. Sent only to %s; it never enters a machine. No machine gets it yet: give it to one with wm machines secrets <machine> %s\n",
			s.Name, aiKeyWord(s), strings.Join(s.Outside.Hosts, ", "), s.Name)
	case s.Outside != nil:
		fmt.Fprintf(a.stdout, "Set %s%s. Sent only to %s; it never enters a machine. The machines given it: %s\n", s.Name, aiKeyWord(s),
			strings.Join(s.Outside.Hosts, ", "), strings.Join(s.Machines, ", "))
	case len(s.Machines) == 0:
		fmt.Fprintf(a.stdout, "Set %s. No machine gets it yet: give it to one with wm machines secrets <machine> %s\n", s.Name, s.Name)
	default:
		fmt.Fprintf(a.stdout, "Set %s. The machines given it get it from their next command: %s\n", s.Name, strings.Join(s.Machines, ", "))
	}
	return nil
}

// ", an AI key" for a secret kept outside by its name's preset; "" for any other.
func aiKeyWord(s *api.Secret) string {
	if s.AIKey() {
		return ", an AI key"
	}
	return ""
}

// readSecret asks for a secret's value at a terminal without showing it, or reads all of standard input: one newline
// at its end, as echo and editors leave one, is left off.
func (a *app) readSecret(name string) (string, error) {
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(f) {
		fmt.Fprintf(a.stderr, "Value of %s (not shown as you type): ", name)
		b, err := term.ReadPassword(f)
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", fmt.Errorf("the value could not be read: %w", err)
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(a.stdin, api.SecretValueMax+3))
	if err != nil {
		return "", fmt.Errorf("standard input could not be read: %w", err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

func (a *app) secretsList(ctx context.Context, args []string) error {
	fs := a.flags("secrets ls", "wm secrets ls", "List the account's secrets by name, the machines that get each, and the "+
		"hosts each one kept outside the machines is sent to: an AI key is marked so, and is on every machine unless it "+
		"was set --only-given. A value is never shown.")
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
	list, err := c.Secrets(ctx)
	if err != nil {
		return err
	}
	if list == nil {
		list = []api.Secret{}
	}
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, s := range list {
			fmt.Fprintln(a.stdout, s.Name)
		}
		return nil
	}
	now := a.now()
	t := a.table("NAME", "MACHINES", "SENT ONLY TO", "SET")
	for _, s := range list {
		machines, sent := or(strings.Join(s.Machines, ","), "-"), "-"
		if s.EveryMachine {
			machines = "every machine"
		}
		if s.Outside != nil {
			sent = strings.Join(s.Outside.Hosts, ",")
			if s.AIKey() {
				sent += " (AI key)"
			}
		}
		t.row(s.Name, machines, sent, age(s.UpdatedAt, now)+" ago")
	}
	return t.flush()
}

func (a *app) secretsRemove(ctx context.Context, args []string) error {
	fs := a.flags("secrets rm", "wm secrets rm [--yes] <NAME>...", "Delete secrets, and take them off every machine given "+
		"them: one set again later goes to none of them until it is given anew. This cannot be undone.")
	yes := fs.Bool("yes", false, "delete without being asked")
	fs.BoolVar(yes, "y", false, "delete without being asked")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which secrets"); err != nil {
		return err
	}
	for _, name := range words {
		if !api.ValidSecretName(name) {
			return usagef("%q is not a secret's name: capitals, digits and _, such as DATABASE_URL", name)
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if !*yes {
		ok, err := a.confirm(fmt.Sprintf("Delete %s, and take it off every machine given it?", strings.Join(words, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errNothingDone
		}
	}
	for _, name := range words {
		if err := c.DeleteSecret(ctx, name); err != nil {
			return err
		}
		if !a.quiet {
			fmt.Fprintln(a.stdout, name)
		}
	}
	return nil
}

// machinesSecrets says which secrets a machine gets: the names given take the place of the ones it had.
func (a *app) machinesSecrets(ctx context.Context, args []string) error {
	fs := a.flags("machines secrets", "wm machines secrets <machine> <NAME>... | --none", "Say which of the account's "+
		"secrets a machine gets: every command run in it gets each as an environment variable of that name. The names "+
		"given take the place of the ones it had; --none takes them all away. Giving secrets needs a key with the "+
		"secrets:write scope, of the whole account.")
	none := fs.Bool("none", false, "take every secret away from it")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which machine, and the secrets it gets"); err != nil {
		return err
	}
	names := words[1:]
	switch {
	case *none && len(names) > 0:
		return usagef("give the secrets it gets, or --none, not both")
	case !*none && len(names) == 0:
		return usagef("say the secrets it gets, or --none to take them all away")
	}
	for _, n := range names {
		if !api.ValidSecretName(n) {
			return usagef("%q is not a secret's name: capitals, digits and _, such as DATABASE_URL", n)
		}
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	m, err := c.UpdateMachine(ctx, id, api.MachineUpdate{Secrets: &names})
	if err != nil {
		return err
	}
	return a.printDetail(m)
}
