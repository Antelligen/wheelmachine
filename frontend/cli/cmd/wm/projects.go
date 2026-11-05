package main

import (
	"context"
	"fmt"

	"wheelmachine/frontend/cli/api"
)

var projectCommands = map[string]subcommand{
	"ls":     (*app).projectsList,
	"create": (*app).projectsCreate,
	"rename": (*app).projectsRename,
	"rm":     (*app).projectsRemove,
	"rules":  (*app).projectsRules,
}

func (a *app) projectsList(ctx context.Context, args []string) error {
	fs := a.flags("projects ls", "wm projects ls", "List the account's projects.")
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
	list := []api.Project{}
	for p, err := range c.AllProjects(ctx, api.ListParams{Limit: 200}) {
		if err != nil {
			return err
		}
		list = append(list, p)
	}
	return a.printProjects(list)
}

func (a *app) printProjects(list []api.Project) error {
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, p := range list {
			fmt.Fprintln(a.stdout, p.ID)
		}
		return nil
	}
	now := a.now()
	t := a.table("ID", "NAME", "AGE")
	for _, p := range list {
		t.row(p.ID, p.Name, age(p.CreatedAt, now))
	}
	return t.flush()
}

func (a *app) printProject(p *api.Project) error {
	if a.json {
		return a.printJSON(p)
	}
	return a.printProjects([]api.Project{*p})
}

func (a *app) projectsCreate(ctx context.Context, args []string) error {
	fs := a.flags("projects create", "wm projects create <name>", "Make a project.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, 1, "the project's name"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := c.CreateProject(ctx, api.ProjectCreate{Name: words[0]})
	if err != nil {
		return err
	}
	return a.printProject(p)
}

func (a *app) projectsRename(ctx context.Context, args []string) error {
	fs := a.flags("projects rename", "wm projects rename <project> <name>", "Give a project another name.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which project, and its new name"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	id, err := c.FindProject(ctx, words[0])
	if err != nil {
		return err
	}
	p, err := c.RenameProject(ctx, id, words[1])
	if err != nil {
		return err
	}
	return a.printProject(p)
}

func (a *app) projectsRemove(ctx context.Context, args []string) error {
	fs := a.flags("projects rm", "wm projects rm <project>...", "Delete projects. Only an empty project can be deleted: delete "+
		"its machines first.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 1, -1, "which projects"); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	for _, w := range words {
		id, err := c.FindProject(ctx, w)
		if err != nil {
			return err
		}
		if err := c.DeleteProject(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, id)
	}
	return nil
}
