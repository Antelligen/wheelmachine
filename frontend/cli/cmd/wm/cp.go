package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"wheelmachine/frontend/cli/api"
)

func (a *app) cp(ctx context.Context, args []string) error {
	fset := a.flags("cp", "wm cp <file> <machine>:work/<path>   or   wm cp <machine>:home/<path> <file>",
		"Copy a file to or from a machine, as a stream. In a machine, a path begins home/ (its home) or work/ (where "+
			"commands start); a path ending in / is a folder, and the file keeps its name in it. - is standard input "+
			"or output. On a terminal, the bytes copied and the speed are shown as it goes.")
	words, err := parse(fset, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "what to copy, and where to"); err != nil {
		return err
	}
	srcM, srcPath, srcRemote := remote(words[0])
	dstM, dstPath, dstRemote := remote(words[1])
	switch {
	case srcRemote && dstRemote:
		return usagef("one side is a file on this computer: copy through it, from one machine and then to the other")
	case !srcRemote && !dstRemote:
		return usagef("one side is a machine: <machine>:work/<path> or <machine>:home/<path>")
	case dstRemote:
		return a.upload(ctx, words[0], dstM, dstPath)
	}
	return a.download(ctx, srcM, srcPath, words[1])
}

// remote reads <machine>:<path>. A word is one when the part before the colon is a machine's id, or a name (no
// slashes) followed by a path in a machine: so C:\file and ./a:b stay files on this computer.
func remote(word string) (machine, p string, ok bool) {
	machine, p, ok = strings.Cut(word, ":")
	if !ok || machine == "" || strings.ContainsAny(machine, `/\`) {
		return "", "", false
	}
	if api.IsMachineID(machine) || len(machine) > 1 && inMachine(p) {
		return machine, p, true
	}
	return "", "", false
}

func inMachine(p string) bool {
	return p == "home" || p == "work" || strings.HasPrefix(p, "home/") || strings.HasPrefix(p, "work/")
}

// folder says whether a path in a machine names a folder: home, work, or one ending in /.
func folder(p string) bool { return p == "home" || p == "work" || strings.HasSuffix(p, "/") }

func (a *app) upload(ctx context.Context, local, machine, dst string) error {
	if !inMachine(dst) {
		return usagef("a path in a machine begins home/ or work/, not %q", dst)
	}
	var f *os.File
	name := filepath.Base(local)
	if local == "-" {
		if folder(dst) {
			return usagef("name the file in the machine when copying standard input: %s:%s<name>", machine, strings.TrimSuffix(dst, "/")+"/")
		}
		// the API needs the length first: standard input is kept aside until it ends
		tmp, err := os.CreateTemp("", "wm-cp-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if _, err := io.Copy(tmp, a.stdin); err != nil {
			return fmt.Errorf("standard input could not be read: %w", err)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		f = tmp
	} else {
		opened, err := os.Open(local)
		if err != nil {
			return fmt.Errorf("%s could not be opened: %w", local, unwrapPath(err))
		}
		defer opened.Close()
		f = opened
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return usagef("%s is a folder: wm cp copies one file", local)
	}
	if folder(dst) {
		dst = strings.TrimSuffix(dst, "/") + "/" + name
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	var body io.Reader = f
	var m *meter
	if a.showMeter(false) {
		m = newMeter(a.stderr, "copying to "+id+":"+dst, st.Size(), time.Now)
		body = &metered{r: f, m: m}
	}
	err = c.WriteFile(ctx, id, dst, body, st.Size())
	if m != nil {
		m.finish()
	}
	return err
}

func (a *app) download(ctx context.Context, machine, src, local string) error {
	if !inMachine(src) {
		return usagef("a path in a machine begins home/ or work/, not %q", src)
	}
	if folder(src) {
		return usagef("name a file in the machine, not a folder: wm cp copies one file")
	}
	toStdout := local == "-"
	target := local
	if !toStdout {
		if st, err := os.Stat(local); err == nil && st.IsDir() || strings.HasSuffix(local, "/") || strings.HasSuffix(local, string(os.PathSeparator)) {
			target = filepath.Join(local, path.Base(src))
		}
	}
	c, id, err := a.machine(ctx, machine)
	if err != nil {
		return err
	}
	file, err := c.ReadFile(ctx, id, src)
	if err != nil {
		return err
	}
	defer file.Close()
	var body io.Reader = file
	var m *meter
	if a.showMeter(toStdout) {
		m = newMeter(a.stderr, "copying "+id+":"+src, file.Size, time.Now)
		body = &metered{r: file, m: m}
	}
	if toStdout {
		_, err = io.Copy(a.stdout, body)
	} else {
		err = writeWhole(target, body)
	}
	if m != nil {
		m.finish()
	}
	return err
}

// showMeter: the bytes and speed are shown on a terminal, never over what is written to it.
func (a *app) showMeter(toStdout bool) bool {
	return !a.quiet && isTerminal(a.stderr) && !(toStdout && isTerminal(a.stdout))
}

// writeWhole writes a file that is either whole or not there: into a file beside it, put in its place at the end.
// A new file is readable by everyone (0644), as a copy usually is; one that was there keeps its mode.
func writeWhole(target string, r io.Reader) error { return writeWholeAs(target, r, 0o644) }

// writeWholeAs is writeWhole with the mode a new file is given.
func writeWholeAs(target string, r io.Reader, mode fs.FileMode) error {
	if st, err := os.Stat(target); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file that can be written", target)
		}
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wm-cp-*")
	if err != nil {
		return fmt.Errorf("%s could not be written: %w", target, unwrapPath(err))
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("%s could not be put in place: %w", target, unwrapPath(err))
	}
	ok = true
	return nil
}

func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
