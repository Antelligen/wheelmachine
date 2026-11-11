package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"wheelmachine/frontend/cli/api"
)

const codeHelp = `Usage: wm code [command]

  (none)                                      the code hosts connected, and the repositories your teams are given
  connect github                              where to install Wheelmachine's GitHub App (open it signed in to Wheelmachine)
  connect gitlab <token> [address]            connect a GitLab group by its access token (address: https://gitlab.com)
  disconnect <connection>                     take a connection away, with every repository given of it
  repos <connection>                          the repositories a connection reaches
  grant <team> <connection> <repo> <read|write>   give a team a repository, to read or to push too
  revoke <team> <grant>                       take a repository from a team

A machine reaches its team's repositories with plain git (git clone https://github.com/acme/api.git) and never holds
a token. A fetch works only for a repository its team may read, and a push only for one it may write.
Owners and admins connect GitHub and GitLab; a team's full managers, owners and admins give it repositories.

wm machines repos <machine> says what one machine reaches; wm machines pull-request opens a pull request from it.
`

// wm code: code access - the GitHub and GitLab an account connects, and the repositories its teams are given.
func (a *app) codeCmd(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(a.stdout, codeHelp)
		return nil
	}
	fs := a.flags("code", "wm code [command]", "The code hosts connected, and the repositories your teams are given.")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if len(words) == 0 {
		code, err := c.Code(ctx)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(code)
		}
		names := map[string]string{}
		t := a.table("CONNECTION", "ID", "HOST", "")
		for _, cn := range code.Connections {
			names[cn.ID] = cn.Name
			note := ""
			if cn.Suspended {
				note = "suspended on GitHub"
			}
			t.row(cn.Name, cn.ID, cn.Host, note)
		}
		t.flush()
		if len(code.Grants) > 0 {
			fmt.Fprintln(a.stdout)
			g := a.table("TEAM", "REPOSITORY", "ACCESS", "GRANT")
			for _, gr := range code.Grants {
				access := "read"
				if gr.Access == "write" {
					access = "read and push"
				}
				g.row(gr.Project, gr.Host+"/"+gr.Repo, access, gr.ID)
			}
			g.flush()
		}
		return nil
	}
	switch words[0] {
	case "connect":
		if len(words) >= 2 && words[1] == "github" && len(words) == 2 {
			link, err := c.GitHubInstallURL(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.stdout, link)
			return nil
		}
		if len(words) >= 3 && words[1] == "gitlab" && len(words) <= 4 {
			address := ""
			if len(words) == 4 {
				address = words[3]
			}
			cn, err := c.ConnectGitLab(ctx, address, words[2])
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(cn)
			}
			fmt.Fprintf(a.stdout, "connected %s (%s): %s\n", cn.Name, cn.Host, cn.ID)
			return nil
		}
		return usagef("%s", "wm code connect github | wm code connect gitlab <token> [address]")
	case "disconnect":
		if len(words) != 2 {
			return usagef("%s", "wm code disconnect <connection>")
		}
		return c.DisconnectCode(ctx, words[1])
	case "repos":
		if len(words) != 2 {
			return usagef("%s", "wm code repos <connection>")
		}
		repos, err := c.CodeRepos(ctx, words[1])
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(repos)
		}
		for _, r := range repos {
			fmt.Fprintln(a.stdout, r.Repo)
		}
		return nil
	case "grant":
		if len(words) != 5 || words[4] != "read" && words[4] != "write" {
			return usagef("%s", "wm code grant <team> <connection> <repo> <read|write>")
		}
		gr, err := c.GrantCode(ctx, words[1], words[2], words[3], words[4])
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(gr)
		}
		fmt.Fprintf(a.stdout, "%s given %s/%s to %s: %s\n", gr.Project, gr.Host, gr.Repo, gr.Access, gr.ID)
		return nil
	case "revoke":
		if len(words) != 3 {
			return usagef("%s", "wm code revoke <team> <grant>")
		}
		return c.RevokeCode(ctx, words[1], words[2])
	}
	return usagef("%s", "wm code [connect|disconnect|repos|grant|revoke]: see wm code -h")
}

// wm machines repos: the repositories a machine reaches, and whether a push will go.
func (a *app) machinesRepos(ctx context.Context, args []string) error {
	fs := a.flags("machines repos", "wm machines repos <machine>", "The GitHub and GitLab repositories a machine reaches now, each to read or to "+
		"push too, and whether a push will go: its holder's own account linked there. The branches no machine pushes to are said below them.")
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
	list, err := c.MachineRepositories(ctx, id)
	if err != nil {
		return err
	}
	if list.Data == nil {
		list.Data = []api.MachineRepository{}
	}
	switch {
	case a.json:
		return a.printJSON(list)
	case a.quiet:
		for _, r := range list.Data {
			fmt.Fprintln(a.stdout, r.Repository)
		}
		return nil
	}
	t := a.table("REPOSITORY", "HOST", "ACCESS", "A PUSH")
	for _, r := range list.Data {
		access, push := "read", "not to this one"
		if r.Access == "write" {
			access, push = "read and push", "goes"
			if !r.CanPush {
				push = "needs its holder's own account linked: the Code page, Your accounts"
			}
		}
		t.row(r.Repository, r.Host, access, push)
	}
	t.flush()
	if len(list.Data) > 0 && len(list.Protected) > 0 {
		fmt.Fprintf(a.stdout, "\nNo machine pushes to %s: changes reach them through a pull request, or a merge request on GitLab.\n", strings.Join(list.Protected, ", "))
	}
	return nil
}

// wm machines pull-request: a pull request from a machine, or on GitLab the line of git that opens one.
func (a *app) machinesPullRequest(ctx context.Context, args []string) error {
	fs := a.flags("machines pull-request", "wm machines pull-request <machine> <repository> --head <branch> --base <branch> --title <words>",
		"Open a pull request from a branch of a repository the machine may push to. Only the person who holds the machine may ask. This does not "+
			"merge, approve or close anything: people do. On GitHub it opens under the holder's own linked account. On GitLab it prints the line of "+
			"git to run in the clone in the machine, which opens a merge request as it pushes; a body with a line beginning with / is refused there, "+
			"as GitLab would run that line as a command.")
	head := fs.String("head", "", "the branch with the changes")
	base := fs.String("base", "", "the branch to merge them into; it may be a protected one")
	title := fs.String("title", "", "one line saying what it changes")
	body := fs.String("body", "", "what it changes and how it was tested")
	bodyFile := fs.String("body-file", "", "read the body from this file; - for standard input")
	draft := fs.Bool("draft", false, "open it as a draft")
	host := fs.String("host", "", "the repository's host, only where the same path is on two of them")
	words, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := count(words, 2, 2, "which machine and which repository"); err != nil {
		return err
	}
	if *head == "" || *base == "" || *title == "" {
		return usagef("%s", "wm machines pull-request needs --head, --base and --title")
	}
	if *body != "" && *bodyFile != "" {
		return usagef("%s", "give --body or --body-file, not both")
	}
	text := *body
	if *bodyFile != "" {
		var read []byte
		if *bodyFile == "-" {
			read, err = io.ReadAll(a.stdin)
		} else {
			read, err = os.ReadFile(*bodyFile)
		}
		if err != nil {
			return err
		}
		text = string(read)
	}
	c, id, err := a.machine(ctx, words[0])
	if err != nil {
		return err
	}
	p, err := c.OpenPullRequest(ctx, id, api.PullRequestCreate{Repository: words[1], Host: *host, Head: *head, Base: *base, Title: *title, Body: text, Draft: *draft})
	if err != nil {
		return err
	}
	switch {
	case a.json:
		return a.printJSON(p)
	case p.Opened && p.URL != nil:
		fmt.Fprintln(a.stdout, *p.URL)
	case p.Run != nil:
		if p.Note != nil && !a.quiet {
			fmt.Fprintln(a.stderr, *p.Note)
		}
		fmt.Fprintln(a.stdout, *p.Run)
	}
	return nil
}
