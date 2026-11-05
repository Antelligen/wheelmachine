package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wheelmachine/frontend/cli/api"
)

const foldersHelp = `Usage: wm folders [command]

  (none)                              the folders you see: your own (My files) first, then your teams'
  ls <folder>[:<path>]                what is in a folder, or in a folder of it
  get <folder>:<path> [file]          download a file (to its own name here, or to file; - for standard output)
  put <file> <folder>:<path>          upload a file, in the place of the one there (- for standard input)
  rm <folder>:<path>                  delete a file or a folder of it: it waits in the folder's Trash for 30 days
  mv <folder>:<from> <to>             move or rename a file or a folder of it

A folder is named by its id or its name, such as wm folders put plan.md "Design:notes/plan.md". In a machine, your
team's folders are at ~/folders, with your own (My files) in a machine that has only ever been yours. Owners and
admins make team folders, on the dashboard; a team's full managers give them to teams.

Your own folder ("My files") is reached from wm with a key not limited to a team: your own account's, or an owner's or an
admin's of an organisation. A member's keys are limited to their teams, so a member uses My files on the dashboard.
`

// wm folders: team folders, and each person's own.
func (a *app) foldersCmd(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(a.stdout, foldersHelp)
		return nil
	}
	fs := a.flags("folders", "wm folders [command]", "The folders you see, and what is in them.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if len(words) == 0 {
		list, err := c.Folders(ctx)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(list)
		}
		if !list.On {
			fmt.Fprintln(a.stdout, "Folders are not set up here yet.")
			return nil
		}
		t := a.table("FOLDER", "ID", "ACCESS", "KEEPS")
		for _, f := range list.Data {
			t.row(f.Name, f.ID, f.Access, storedWords(f.Bytes))
		}
		t.flush()
		return nil
	}
	// a folder by its id, or by its name as the list says it
	folderOf := func(ref string) (string, error) {
		if strings.HasPrefix(ref, "fld_") {
			return ref, nil
		}
		list, err := c.Folders(ctx)
		if err != nil {
			return "", err
		}
		for _, f := range list.Data {
			if strings.EqualFold(f.Name, ref) {
				return f.ID, nil
			}
		}
		return "", usagef("no folder called %q: wm folders lists them", ref)
	}
	place := func(word string, needPath bool) (string, string, error) {
		name, path, found := strings.Cut(word, ":")
		if needPath && (!found || strings.Trim(path, "/") == "") {
			return "", "", usagef("say the folder and a path in it, such as \"My files:notes/plan.md\"")
		}
		id, err := folderOf(name)
		return id, strings.Trim(path, "/"), err
	}
	switch words[0] {
	case "ls":
		if len(words) != 2 {
			return usagef("%s", "wm folders ls <folder>[:<path>]")
		}
		id, path, err := place(words[1], false)
		if err != nil {
			return err
		}
		entries := []api.FolderEntry{}
		for e, err := range c.AllFolderEntries(ctx, id, path) {
			if err != nil {
				return err
			}
			entries = append(entries, e)
		}
		if a.json {
			return a.printJSON(api.FolderListing{Object: "shared_listing", Folder: id, Path: path, Entries: entries})
		}
		t := a.table("NAME", "SIZE", "CHANGED")
		for _, e := range entries {
			size, name := storedWords(e.Size), e.Name
			if e.Type == "folder" {
				size, name = "-", name+"/"
			}
			t.row(name, size, e.ChangedAt)
		}
		t.flush()
		return nil
	case "get":
		if len(words) != 2 && len(words) != 3 {
			return usagef("%s", "wm folders get <folder>:<path> [file]")
		}
		id, path, err := place(words[1], true)
		if err != nil {
			return err
		}
		f, err := c.ReadFolderFile(ctx, id, path)
		if err != nil {
			return err
		}
		defer f.Close()
		to := filepath.Base(path)
		if len(words) == 3 {
			to = words[2]
		}
		if to == "-" {
			_, err = io.Copy(a.stdout, f)
			return err
		}
		out, err := os.Create(to)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, f); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	case "put":
		if len(words) != 3 {
			return usagef("%s", "wm folders put <file> <folder>:<path>")
		}
		id, path, err := place(words[2], true)
		if err != nil {
			return err
		}
		var r io.Reader
		size := int64(-1)
		if words[1] == "-" {
			b, err := io.ReadAll(a.stdin)
			if err != nil {
				return err
			}
			r, size = strings.NewReader(string(b)), int64(len(b))
		} else {
			f, err := os.Open(words[1])
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			r, size = f, st.Size()
		}
		e, err := c.WriteFolderFile(ctx, id, path, r, size)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(e)
		}
		fmt.Fprintf(a.stdout, "put %s (%s)\n", path, storedWords(e.Size))
		return nil
	case "rm":
		if len(words) != 2 {
			return usagef("%s", "wm folders rm <folder>:<path>")
		}
		id, path, err := place(words[1], true)
		if err != nil {
			return err
		}
		t, err := c.DeleteFolderFile(ctx, id, path)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(t)
		}
		fmt.Fprintf(a.stdout, "%s is in the Trash until %s: put it back on the dashboard\n", t.Path, t.GoesAt)
		return nil
	case "mv":
		if len(words) != 3 {
			return usagef("%s", "wm folders mv <folder>:<from> <to>")
		}
		id, from, err := place(words[1], true)
		if err != nil {
			return err
		}
		return c.MoveFolderItem(ctx, id, from, strings.Trim(words[2], "/"))
	}
	return usagef("%s", "wm folders [ls|get|put|rm|mv]: see wm folders -h")
}

// What a folder keeps, in the units its limit and price are said in: 1,000 of each to the next.
func storedWords(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + " B"
	}
	return strings.TrimSuffix(strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), "0"), ".") + " " + units[i]
}
