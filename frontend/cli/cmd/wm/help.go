package main

const mainHelp = `wm - cloud machines for people and agents, paid by the second

Usage: wm <command> [arguments] [flags]

Machines:
  machines   list, make, pause, fork and delete machines (wm machines -h)
  exec       run a command in a machine: wm exec <machine> -- <command> [args...]
  sh         run a shell line in a machine: wm sh <machine> '<shell line>'
  ssh        open an interactive shell in a machine: wm ssh <machine>
  ssh-config print a ~/.ssh/config block for scp, sftp and your own ssh: wm ssh-config <machine>
  cp         copy a file to or from a machine: wm cp <file> <machine>:work/<path>
  top        a machine's latest numbers: CPUs, memory, disk and network
  metrics    what a machine used, step by step: wm metrics <machine> --since 6h --step 5m
  ports      open a machine's ports at web addresses of their own (wm ports -h)
  apps       turn a machine's apps on and off, and open the web ones (wm apps -h)
  secrets    set, list and delete the secrets machines get as environment variables (wm secrets -h)
  models     the models your AI keys can use, and the one each agent starts on (wm models -h)

Account:
  login      sign in through your browser and keep a key on this computer
  logout     forget the key kept on this computer
  me         the account and key in use
  usage      each machine's running time and cost in a month
  credit     the account's credit and its latest entries; wm credit buy buys more,
             wm credit currency says its currency and changes it until your first purchase, or until we change it for you
  limits     this month's limits, and how close the account is to each (wm limits -h)
  code       the GitHub and GitLab repositories your teams' machines may read and push to (wm code -h)
  folders    the files your teams share, and your own: list, get, put, rm, mv (wm folders -h)
  projects   list, make, rename and delete projects, and read a team's rules (wm projects -h)
  requests   forks and hand-offs: those asked, waiting and done (wm requests -h)
  keys       list, make and revoke API keys (wm keys -h)
  orgs       the organisations you are in, and your role in each
  audit      what was done in the account, by whom and from where; or all of it as a file

Catalogue:
  gpus       GPUs on offer, where they are and what an hour costs
  regions    places you can ask a machine to run in

Flags:
  --json        print JSON
  -q, --quiet   print ids only
  --version     print the version

A machine is named by its id (m_...) or its name. The key comes from WHEELMACHINE_API_KEY,
else from wm login; the API is at WHEELMACHINE_BASE_URL, else https://api.wheelmachine.ai.
An organisation's people, invitations and teams are managed in the dashboard,
https://app.wheelmachine.ai.
Run wm <command> -h for a command's flags.
`

const machinesHelp = `Usage: wm machines <command> [arguments] [flags]

  ls                          list machines, newest first
  create                      make a machine
  new --type <type>           make machines of a team's type: for yourself, or for others
                              (--for @ada,@lin or --everyone, --each N), several at once
  types                       list a team's machine types
  get <machine>               show one machine
  rm <machine>...             delete machines, their files and their backups
  pause <machine>...          pause, memory kept; no running time is charged
  start <machine>...          run again, from paused, stopped or archived
  stop <machine>...           stop; its files stay, its memory does not
  archive <machine>...        archive its files; start brings them back
  fork <machine>              fork it: a new machine of its own, made from it as it is now, or from
                              a backup (--backup) at its size or another (--cpus); --for <person>
                              forks it for a teammate, with sign-ins taken out first. In an organisation, a member's
                              fork waits for a full manager's, an owner's or an admin's OK
  hand-off <machine>          hand off one you made to a teammate (--to <person>), exactly as it is
  take-back <machine>         take back one you handed off, as it is then
  give-back <machine>         give one handed to you back to the person who handed it off
  backup <machine>            back it up: keep its files as they are now, to fork it from later
  backups [<machine>]         list backups, newest first: a machine's, or every one you reach
  wait <machine>              wait until it is running, or in the state --state says
  label <machine> k=v... k-   set labels; k- takes one off
  rename <machine> <name>     give it another name
  secrets <machine> NAME...   say which secrets it gets; --none takes them all away
  screenshot <machine> [file] see its desktop as a picture; it shows whatever is on the screen
  click <machine> <x> <y>     click on its desktop, at a place in pixels of a picture of it
  type <machine> [text]       type on its desktop: the text, or standard input with none
  keys <machine> <keys>...    press keys on its desktop, such as ctrl+c
  scroll <machine> <x> <y>    scroll on its desktop: --dy or --dx notches

Run wm machines <command> -h for its flags.
`

const portsHelp = `Usage: wm ports <command> [arguments] [flags]

  ls <machine>                list its open ports and their addresses, lowest first
  open <machine> <port>       open a port at a web address of its own; --public for anyone
  close <machine> <port>      close a port's address
  link <machine> <port>       make a link that lets one browser into a private address

A port's address is https://<port>-<the machine's id without m_>.run.wheelmachine.ai.
It is private unless opened --public: it answers a call with an API key that can see
the machine, and a browser that opened a link from wm ports link.
Run wm ports <command> -h for its flags.
`

const appsHelp = `Usage: wm apps <command> [arguments] [flags]

  ls <machine>                list its apps, whether each is on, and where each is reached
  on <machine> <app>          turn an app on; a web app prints a link into its private address
  off <machine> <app>         turn an app off
  show <machine> <app>        open an app on the machine's own screen, signed in
  open <machine> <app>        print a fresh link into a web app that is on; --browser opens it

A web app (a desktop, a code editor, an agent's own page) is reached at the machine's private
address for its port, which only your keys and the browsers you send a link to can reach. A
service is reached from inside the machine, and a terminal agent by typing its command in the
machine's terminal. An agent started before your AI keys changed says so: turn it off and on
again to start it with them, in a new session.
Run wm apps <command> -h for its flags.
`

const modelsHelp = `Usage: wm models [command] [arguments] [flags]

  (none)                      list the models your AI keys can use, by provider; --agent for one
                              agent's, --refresh to read the lists again
  chosen                      list the agents of your machines, the model chosen for each, and
                              what each starts on
  use <agent> <model>         choose the model an agent starts on (wm models use codex
                              openai/gpt-6.1-sol); --every <model> for every agent
  reset <agent>               let an agent start on its default again; --every takes the one
                              model for every agent away
  allowed                     list the models your organisation's people may use, list by list
  allow <model>...            set the models people may use: --org, --team, --role or --person;
                              --any takes a list away

The lists are your AI keys' own, as each model's provider gives them. A choice reaches your
running machines at once, and the others when they next start; an agent already
running uses it from its next start. Inside an agent, its own model setting still works, and
stays until a model is chosen for it here again. In an organisation a project is a team: --team names the one
your choices are for, and its owners and admins may limit people to a list of
the models they may use: the most specific list a person has applies to them.
Run wm models <command> -h for its flags.
`

const projectsHelp = `Usage: wm projects <command> [arguments] [flags]

  ls                          list projects
  create <name>               make a project
  rename <project> <name>     give a project another name
  rm <project>...             delete empty projects
  rules <project>             a team's rules: hand-offs, forks, GPUs, open internet, its budget, its largest
                              machine, longest idle and public ports

A project is named by its id (prj_...) or its name.
`

const requestsHelp = `Usage: wm requests [command] [arguments] [flags]

  (none), ls [--all]          the requests still open - or every one - newest first
  ask fork|hand-off --of <person>
                              ask a teammate for a fork, or a hand-off, of one of their machines
  take-back <request>         withdraw a request you asked, while it waits

A fork, a hand-off or an ask made with wm's key waits first for your own OK in the dashboard.
Then whoever the team's rules name decides: for a member's fork, a full manager of the team, an
owner or an admin. Decide at https://app.wheelmachine.ai/requests.
`

const keysHelp = `Usage: wm keys <command> [arguments] [flags]

  ls                          list the account's API keys
  create <name> --scope ...   make a key; its secret is shown this once
  revoke <key>...             stop keys working, everywhere

Scopes: machines:read, machines:write, machines:exec, gpus, projects:write, keys:write,
secrets:write, folders:read, folders:write, or all.
`

const secretsHelp = `Usage: wm secrets <command> [arguments] [flags]

  ls                          list the account's secrets, the machines that get each, and
                              the hosts each one kept outside the machines is sent to
  set <NAME>                  set a secret: its value from standard input, or asked for
  rm <NAME>...                delete secrets, and take them off every machine

A machine gets secrets when it is made (wm machines create --secret NAME) or later
(wm machines secrets <machine> NAME...): every command run in it gets each as an environment
variable of that name, and a variable the command itself is given wins over a secret of the
same name. A value is never taken from the command line, and never shown again once set.

An AI key - a secret named as its provider's agents read it, such as ANTHROPIC_API_KEY,
OPENAI_API_KEY or GEMINI_API_KEY - is set once and is on every machine: each holds a
placeholder in its place, and the key is added on the way out, only to its provider's hosts; it
never enters a machine. wm secrets set --only-given keeps one on the machines given it, and
--inside keeps one in the machines as an ordinary secret, for a tool that cannot work with a
placeholder.
`
