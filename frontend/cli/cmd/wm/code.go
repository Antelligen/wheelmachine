package main

import (
	"context"
	"fmt"
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
