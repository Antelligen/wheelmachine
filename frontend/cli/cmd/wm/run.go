package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"strings"

	"wheelmachine/frontend/cli/api"
)

// runFlags is the flags of exec and sh.
type runFlags struct {
	root *bool
	cwd  *string
	env  labelFlag
}

func commandFlags(fs *flag.FlagSet) runFlags {
	f := runFlags{
		root: fs.Bool("root", false, "run it as root inside the machine"),
		cwd:  fs.String("cwd", "", "where it starts, an absolute `path`; the work folder when left out"),
		env:  labelFlag{},
	}
	fs.Var(f.env, "env", "a `KEY=VALUE` for its environment; repeat for more")
	return f
}

func (a *app) exec(ctx context.Context, args []string) error {
	fs := a.flags("exec", "wm exec <machine> [flags] -- <command> [args...]", "Run a command in a machine, without a shell. "+
		"What it wrote is printed as it came, stdout and stderr apart, and wm exits with its exit code. A paused machine "+
		"is woken for it.")
	f := commandFlags(fs)
	machine, words, err := parseHead(fs, args)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return usagef("say what to run: wm exec %s -- <command> [args...]", machine)
	}
	return a.runIn(ctx, machine, api.ExecRequest{Command: words}, f)
}

func (a *app) sh(ctx context.Context, args []string) error {
	fs := a.flags("sh", "wm sh <machine> [flags] '<shell line>'", "Run a line of shell in a machine (sh -lc), with its pipes "+
		"and redirections. What it wrote is printed as it came, and wm exits with its exit code.")
	f := commandFlags(fs)
	machine, words, err := parseHead(fs, args)
	if err != nil {
		return err
	}
	line := strings.Join(words, " ")
	if strings.TrimSpace(line) == "" {
		return usagef("say what to run: wm sh %s 'ls -la | wc -l'", machine)
	}
	return a.runIn(ctx, machine, api.ExecRequest{Shell: line}, f)
}

func (a *app) runIn(ctx context.Context, machine string, req api.ExecRequest, f runFlags) error {
	if *f.root {
		req.User = "root"
	}
	req.Cwd = *f.cwd
	if len(f.env) > 0 {
		req.Env = f.env
	}
	// the bytes as they came, unless JSON is asked for, where text reads better
	req.BinaryOutput = !a.json
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	res, err := c.Exec(ctx, id, req)
	if err != nil {
		return err
	}
	if a.json {
		if err := a.printJSON(res); err != nil {
			return err
		}
	} else {
		out := []byte(res.Stdout)
		if res.StdoutBase64 != "" {
			if out, err = base64.StdEncoding.DecodeString(res.StdoutBase64); err != nil {
				return fmt.Errorf("the command's output could not be read: %w", err)
			}
		}
		if _, err := a.stdout.Write(out); err != nil {
			return err
		}
		if _, err := io.WriteString(a.stderr, res.Stderr); err != nil {
			return err
		}
		// too long to come back whole: cut (past 8 MiB of a stream, or as little as 5 MiB), or, with no output at all, left out
		switch {
		case res.Truncated && len(out) == 0 && res.Stderr == "":
			fmt.Fprintln(a.stderr, "wm: the command ran, but its output was too long to bring back and was left out; for long output, "+
				"write it to a file and copy it with wm cp")
		case res.Truncated:
			fmt.Fprintln(a.stderr, "wm: the output was too long and was cut; for long output, write it to a file and copy it with wm cp")
		}
	}
	if res.ExitCode != 0 {
		return &exitError{code: exitCode(res.ExitCode)}
	}
	return nil
}

// exitCode is a command's exit code as this process can end with it: one that does not fit in a byte is 255, so a
// failure never reads as success.
func exitCode(code int) int {
	if code < 0 || code > 255 {
		return 255
	}
	return code
}
