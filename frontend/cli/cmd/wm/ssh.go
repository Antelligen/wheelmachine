package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"wheelmachine/frontend/cli/api"
	"wheelmachine/frontend/cli/internal/term"
)

// wm ssh <machine> opens an interactive shell in a machine - a terminal of its own, over the API's terminal stream.
// With `-- <command>` it runs one command on a terminal instead of a shell. The local terminal goes into raw mode so
// keys reach the machine as they are typed, follows window changes, and is put back on exit; wm exits with the
// command's own exit code.
func (a *app) ssh(ctx context.Context, args []string) error {
	fs := a.flags("ssh", "wm ssh <machine> [flags] [-- <command> [args...]]", "Open an interactive shell in a machine, or "+
		"run one command on a terminal. The local terminal is put into raw mode and follows window changes; wm exits with "+
		"the command's exit code.")
	root := fs.Bool("root", false, "open it as root inside the machine")
	cwd := fs.String("cwd", "", "where it starts, an absolute `path`; the work folder when left out")
	termKind := fs.String("term", "", "the terminal's kind (TERM); the local $TERM when left out")
	machine, command, err := parseHead(fs, args)
	if err != nil {
		return err
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	opts := api.TerminalOptions{Command: command, Cwd: *cwd, Term: or(*termKind, a.getenv("TERM"))}
	if *root {
		opts.User = "root"
	}

	stdin, stdinIsTTY := a.stdin.(*os.File)
	stdout, stdoutIsTTY := a.stdout.(*os.File)
	interactive := stdinIsTTY && term.IsTerminal(stdin) && stdoutIsTTY && term.IsTerminal(stdout)
	if interactive {
		if cols, rows, err := term.Size(stdout); err == nil {
			opts.Cols, opts.Rows = cols, rows
		}
	}

	t, err := c.OpenTerminal(ctx, id, opts)
	if err != nil {
		return err
	}
	defer t.Close()

	if interactive {
		restore, err := term.Raw(stdin)
		if err == nil {
			defer restore()
			stop := term.OnResize(stdout, func(cols, rows int) { _ = t.Resize(cols, rows) })
			defer stop()
		}
	}

	// keystrokes to the machine, until stdin ends or the context is cancelled
	typing := make(chan error, 1)
	go func() { typing <- pumpStdin(ctx, a.stdin, t) }()

	// the program's output to the screen, until it ends
	out := a.stdout.(io.Writer)
	for {
		data, err := t.Receive()
		if err != nil {
			if api.IsTerminalEnd(err) {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if len(data) > 0 {
			if _, werr := out.Write(data); werr != nil {
				return werr
			}
		}
	}
	select {
	case <-typing:
	default:
	}

	end := t.End()
	switch {
	case end == nil:
		return errors.New("the terminal ended without saying how")
	case end.ExitCode != nil:
		if *end.ExitCode != 0 {
			return &exitError{code: exitCode(*end.ExitCode)}
		}
		return nil
	default:
		// closed, not ended by the program: say why, and exit non-zero
		fmt.Fprintf(a.stderr, "wm: %s\n", or(end.Message, "the terminal closed"))
		return &exitError{code: 1}
	}
}

// pumpStdin sends what is typed to the terminal until stdin ends or the context is cancelled.
func pumpStdin(ctx context.Context, in io.Reader, t *api.Terminal) error {
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := in.Read(buf)
		if n > 0 {
			if serr := t.Send(buf[:n]); serr != nil {
				return serr
			}
		}
		if err != nil {
			return err
		}
	}
}
