# Changelog

`wm`, the Wheelmachine command line, and `wm-mcp`, its MCP server for agents. Each release is tagged `wm-v<version>`.

## 0.1.5

- Deploy is open: `wm deploy` puts an app on the internet from the folder you are in, a repository of your connections or
  a public repository's https address, on a machine of its own at a public address, and `wm deployments` lists, reads,
  changes, redeploys and deletes deployments. Both are in wm's help now, and no longer say they are not open yet.
- `wm-mcp` lists Deploy's six tools: `deploy_app`, `list_deployments`, `get_deployment`, `redeploy`, `delete_deployment`
  and `deployment_logs`. It sends a folder only from where it was started or the folders named in
  `WHEELMACHINE_DEPLOY_ROOTS`, never a home folder or one of settings.
- `wm machines repos` says which repositories a machine reaches, to read or to push too, and whether a push will go, and
  `wm machines pull-request` opens a pull request from a branch: on GitHub at once, under the name of the person who holds
  the machine, and on GitLab it prints the line of git to run in the machine's clone, which opens a merge request as it
  pushes. Only the person who holds the machine may open one, and neither merges, approves or closes anything.
  `wm-mcp` has `list_repositories` and `open_pull_request`, and the Go client `MachineRepositories` and `OpenPullRequest`.
- `wm-mcp` has `search_docs`: it looks in the docs and reads a page of them, so that an agent need not guess a flag or a
  route. It needs no key, and keeps a copy of the docs to use when they cannot be reached.
- A machine pauses no sooner than 30 minutes idle, and one set to less now waits 30 minutes. `wm machines create`'s
  `--idle-after` is 30m to 24h, and 30m when left out, where it was 5m; `wm-mcp`'s `create_machine` takes `idle_after_s`
  from 1800, and 1800 when left out.
- A machine whose internet is `closed` now has none, in or out, and no name lookups: a public port of it answers only as a
  private one. Whatever a machine's internet, its team's repositories and folders still work where given. The words of
  `wm` and `wm-mcp` say so.
- Built with Go 1.27.4, for its security fixes.

## 0.1.4

- `wm` sees a machine's desktop and works it: `wm machines screenshot` writes a picture of it to a file, `click` and
  `scroll` act at places read off a picture, and `type` and `keys` type and press keys. `wm machines create --desktop`
  turns the desktop on behind the answer, so that a first picture finds it ready.
- `wm-mcp` has two new tools, `take_screenshot` and `use_screen`, for an agent to see a machine's desktop as a picture and
  work it: click, drag, scroll, press keys, type and wait. A picture shows whatever is on the screen and goes on to your
  agent's model provider. `take_screenshot` wakes a paused machine and turns its desktop on, so it is not marked as only
  reading: an agent that runs reading tools without asking still asks before it. `create_machine` takes `desktop`.
- A click, a key or a word is never sent again by `wm`, `wm-mcp` or the Go client once it may have reached the API, not
  even when refused for a moment, so it is never done twice.
- Characters that aren't on an English keyboard, such as accents, Russian, Chinese or emoji, can come out wrong when typed.
  Take a picture after typing them to check; a password box can't be checked this way.
- The Go client has `TakeScreenPicture` and `WorkScreen`, and `MachineCreate` has `Desktop`.
- When a command's output was too long, `wm exec` and `wm sh` say whether it was cut or left out. They said it was cut at
  8 MiB even when none of it came back: it can be cut at as little as 5 MiB, or left out when it cannot be brought back,
  and the command's exit code is kept either way. The Go client's `ExecResult.Truncated` says the same.

## 0.1.3

- An account counted in pounds or euros reads its balance, its credit and its prices in that currency.
- `wm credit currency` says the account's currency and changes it, until your first purchase or until we change it for
  you. `wm credit buy 25` now buys 25 of that currency.
- `closed` now says what still works, and the words across `wm` and `wm-mcp` are plainer.

## 0.1.2

First release.

- `wm` drives the whole Wheelmachine API from a terminal: machines, commands, files, ports and their links, apps,
  secrets, network rules, metrics, keys, projects, organisations, requests and the audit.
- `wm-mcp` offers machines to any MCP client as tools, over standard input and output.
- Builds for Linux, macOS and Windows, on amd64 and arm64, each archive checked by the release's `SHA256SUMS`.
