# Wheelmachine command line and MCP server

`wm` drives the Wheelmachine API from a terminal. `wm-mcp` offers the same machines to any MCP client as tools.
Both are built on `api`, a Go client for the whole API (`contracts/openapi.yaml`). Standard library only.

## Install

    curl -fsSL https://wheelmachine.ai/install.sh | sh

It puts `wm` and `wm-mcp` in `~/.local/bin`. Or download an archive from
https://github.com/Antelligen/wheelmachine/releases.

## Quick start

    wm login                         # opens your browser: sign in, type the code, approve
    wm machines create --name box    # answers when it runs, usually in about a second
    wm exec box -- uname -a          # prints what it wrote, exits with its exit code
    wm sh box 'ls -la | wc -l'
    wm ssh box                       # an interactive shell, as on your own machine
    wm ssh box -- top                # one command on a terminal of its own
    wm cp ./data.csv box:work/       # streams the file; work/ is where commands start
    wm cp box:home/out.log .
    wm top box                       # its latest numbers: CPUs, memory, disk, network
    wm metrics box --since 6h --step 5m
    wm ports open box 8080           # prints the web address of what listens on port 8080 in box
    wm ports link box 8080           # a link that lets one browser into that address for 30 minutes
    wm apps ls box                   # its apps: a desktop and a code editor in the browser, agents in its terminal
    wm apps on box desktop           # prints a one-time link that opens its desktop in your browser
    wm machines screenshot box       # its desktop as a picture, in screen.png; see also click, type, keys and scroll
    wm machines pause box            # memory kept, no running time charged
    wm machines rm --yes box

`wm top` and `wm metrics` show a point a minute, for up to the last day. A paused or stopped machine has no points for
that time, and the numbers can start over at any time.

## Secrets

    wm secrets set API_TOKEN < token.txt         # or at a terminal: asked for, not shown as you type
    wm machines create --name box --secret API_TOKEN
    wm exec box -- sh -c 'curl -H "Authorization: Bearer $API_TOKEN" https://example.com'
    wm machines secrets box API_TOKEN DB_URL     # the names it gets, said whole; --none takes them away
    wm secrets ls                                # names, the machines given each, where each is sent: never a value
    wm secrets rm API_TOKEN

Your own AI keys, set once under the names agents read them by:

    wm secrets set ANTHROPIC_API_KEY             # on every machine; sent only to api.anthropic.com
    wm secrets set --only-given OPENAI_API_KEY   # only on the machines given it
    wm secrets set --inside GEMINI_API_KEY       # an ordinary secret: the key itself in the machines given it

An AI key never enters a machine: each holds a placeholder in its place, and the key is added on the way out, only to
requests going to its provider's own hosts. A machine that reaches only the names on its list gets a key when
the list allows one of the key's hosts; a closed machine gets none.

Choose what a machine can reach on the internet:

    wm machines create --internet closed                                   # no internet
    wm machines create --internet allowed --allow api.github.com --allow 140.82.112.0/20
    wm machines network box --internet allowed --allow '*.githubusercontent.com'   # changes it at once, no restart

`open` (the default): the whole internet. `closed`: none of it, in or out, and no name lookups: a public port answers
only as a private one. `allowed`: only the addresses of the names you list (HTTP and HTTPS; other sites at those
addresses can still answer) and the IP ranges you list (other ports). Whatever it is, your team's repositories and
folders still work where given.

Every command run in a machine gets the secrets it was given as environment variables; a variable the command is
given itself (`env`) wins over a secret of the same name. A value is never taken from the command line - where the
shell's history and the process list keep it - and the API never shows it again once set. Setting, deleting and giving secrets needs the `secrets:write` scope on a key not limited to some projects;
a command can read its own environment, so any key that may run commands in a machine can read what it was given.

A machine is named by its id (`m_...`) or its name. Every read command takes `--json`, and `-q` prints ids only;
`wm -h` lists the rest.

A port's address is `https://<port>-<the machine's id without m_>.run.wheelmachine.ai`. It is private unless opened
`--public`: it answers a call with an API key that can see the machine, and a browser that opened a link from
`wm ports link`, which opens once, within five minutes, and lets that browser in for as long as `--for` says (up to an
hour). `wm ports ls box` lists a machine's ports, and `wm ports close box 8080` closes one.

`wm login` shows a short code and opens https://app.wheelmachine.ai/device. Sign in, type the code and approve; a key
with every scope also needs a code we e-mail you. wm gets a key of its own, named `wm on <this computer>`; revoke it on
the Keys page. With no browser here, open the address on any device, or keep a key you already have with
`wm login --key`.

The key comes from `WHEELMACHINE_API_KEY`, else from the file `wm login` writes: `$XDG_CONFIG_HOME/wheelmachine/config.json`,
else `~/.config/wheelmachine/config.json`, mode 0600. `WHEELMACHINE_BASE_URL` gives another address (the default is
`https://api.wheelmachine.ai`).

Exit codes: 0 done, 1 the API or the network refused, 2 the command was not right. `wm exec`, `wm sh` and `wm ssh`
exit with the command's own code.

## Deploy

    wm deploy                                            # the folder you are in: shows what goes, asks in a terminal, waits until it is live
    wm deploy acme/shop --branch main                    # a repository of your connections
    wm deploy https://github.com/acme/shop --start "npm start" --port 3000
    wm deployments ls                                    # their states and addresses
    wm deployments logs shop --kind app                  # what the app writes
    wm deployments redeploy shop                         # fetch and build it again: a build that fails leaves the live version
    wm deployments rm shop                               # it and its machine, with all its files

Each deployment runs on a machine of its own at a public address, and is charged as any machine is, builds included. It
pauses once nobody has visited for 30 minutes and its app is quiet, and the next visit wakes it, unless `--always-on`; one
made from a team's machine type pauses as the type says. A folder goes without keys, tokens and settings, what its
`.gitignore` files name, `node_modules` and links: wm shows what goes and, in a terminal, asks first; without one it asks
nothing, and the folder you are in goes only with `--yes`. `wm-mcp` has Deploy's tools too. See
https://docs.wheelmachine.ai/deploy.html.

## Credit

    wm credit                                            # what is left, and its latest entries
    wm credit --all                                      # every entry, the newest first
    wm credit -q                                         # the balance alone
    wm credit buy 25                                     # buy 25 of the account's currency: prints a link to pay it
    wm credit currency GBP                               # count the credit in pounds, until your first purchase, or until we change it for you

Credit is bought in 10, 25, 50 or 100 of your account's currency through Paddle. See
https://docs.wheelmachine.ai/billing.html.

## Organisations

    wm orgs                                              # the organisations you are in (with a key of your own account)
    wm login --account acc_01jb2xk3n4p5q6r7s8t9v0w1x2    # a key made in one of them: you choose as you approve
    wm me                                                # there: the organisation, you, your role, teams and limits
    wm audit --action machine. --since 7d                # what was done, by whom, from where
    wm audit --out audit.csv                             # all of it as a file: a year at most, readable by you only

A key made in an organisation belongs to the person who made it: it does no more than they may do there now, and
stops working when they leave. In an organisation, `wm keys ls` says who made each key (MADE BY), and `wm machines ls` and `wm usage`
whose each machine is (WHOSE: a name and their role there, as `Ravi · Member` or `Meera · Team manager`). The audit
is read with a key that holds every scope, and in an organisation by its owners and admins. People, invitations and
teams are managed in the dashboard.

A member's `wm machines fork` makes nothing at once: it prints the request, which waits for your own OK in the
dashboard and then for a full manager of the machine's team, an owner or an admin; `wm requests` follows it, and the
fork is made, named, labelled and sized as you asked, once it is approved.

## Interactive shell

`wm ssh <machine>` opens an interactive shell in a machine - a terminal of its own, over the API's terminal
endpoint (a WebSocket). The local terminal goes into raw mode so keys reach the machine as they are typed, follows
window changes, and is put back on exit; `--root` opens it as root inside the machine, `--cwd` starts it elsewhere.
`wm ssh <machine> -- <command>` runs one command on a terminal instead of a shell (`wm ssh box -- top`).

## scp, sftp and your own ssh

`wm ssh-config <machine>` makes your own `ssh`, `scp` and `sftp` reach a machine directly. It signs a short-lived
certificate for your SSH key (`~/.ssh/id_ed25519.pub`, or `--identity <file.pub>`) and prints a `~/.ssh/config` block
with no `ProxyCommand`:

    wm ssh-config box >> ~/.ssh/config
    ssh box
    scp ./data.csv box:work/
    sftp box

The certificate lasts an hour; run `wm ssh-config` again to refresh it.

## MCP server

`wm-mcp` speaks MCP over standard input and output (JSON-RPC 2.0, one message a line) and logs to standard error
only. It implements revision 2026-07-28, where every request carries its version in `_meta`, and the revisions
that begin with `initialize`: 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05.

Add it to your MCP client's settings as a server it starts itself. Most clients take an entry of this shape:

    {
      "mcpServers": {
        "wheelmachine": {
          "command": "/path/to/wm-mcp",
          "args": [],
          "env": { "WHEELMACHINE_API_KEY": "ak_live_..." }
        }
      }
    }

After `wm login` on the same computer, leave `env` out and the key stays out of the client's settings. A key made
for the server alone, with only the scopes it needs, is the safest:

    wm keys create agent --scope machines:read,machines:write,machines:exec

Tools: `list_machines`, `create_machine`, `get_machine`, `run_command`, `read_file`, `write_file`, `pause_machine`,
`start_machine`, `stop_machine`, `fork_machine`, `backup_machine`, `list_backups`, `delete_machine`, `list_gpus`,
`list_regions`, `list_secrets`, `give_secrets`, `set_network`, `list_apps`, `turn_app_on`, `turn_app_off`, `show_app`, `open_app`,
`take_screenshot`, `use_screen`, `list_repositories`, `open_pull_request`, `whoami`. Each answers compact JSON; `take_screenshot`, and `use_screen` when asked for a
picture, answer a real picture beside it, which shows whatever is on the screen and goes on to your model's provider. A failed
call comes back as a result with `isError` and the API's `code` and `detail`. The secrets tools name secrets only: no tool takes
or answers a value.

`search_docs` looks in the docs, and reads a page of them, so that an agent need not guess a flag or a route. It needs no key. It
reads the docs from `https://docs.wheelmachine.ai` (`WHEELMACHINE_DOCS_URL` to read them from elsewhere), and keeps a copy in this
computer's cache folder, which it goes on using when they cannot be reached.

## The Go client

    c, err := api.NewFromEnv(os.Getenv)
    m, err := c.CreateMachine(ctx, api.MachineCreate{Name: "box"})
    res, err := c.Exec(ctx, m.ID, api.ExecRequest{Command: []string{"uname", "-a"}})
    p, err := c.OpenPort(ctx, m.ID, 8080, false)    // p.URL is its address
    a, err := c.TurnAppOn(ctx, m.ID, "desktop", api.AppLinkCreate{})   // a.Link opens it in a browser, once
    for m, err := range c.AllMachines(ctx, api.ListMachinesParams{}) { ... }
    for e, err := range c.AllAudit(ctx, api.AuditQuery{Action: "machine."}) { ... }

Calls are tried again on 429, 503, 504 and failed connections, waiting as `Retry-After` says or backing off with
full jitter; an error coded `not_open` never is. A call that makes something carries one `Idempotency-Key` across its
tries, and a command or a port's link is never sent twice. An error from the API is an `*api.Error` with its status, `code`,
`detail` and request id.

A program of your own signs in through the browser as wm does, with no key to start from:

    login, err := api.StartDeviceLogin(ctx, api.DeviceLoginRequest{Name: "my-tool on " + host})
    fmt.Println("Open", login.VerificationURI, "and enter", login.UserCode)
    made, err := login.Wait(ctx) // made.Secret is the new key, given this once

## Tests

    go vet ./... && go test -race ./...
