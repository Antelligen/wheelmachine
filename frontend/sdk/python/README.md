# Wheelmachine for Python

The official Python library for the Wheelmachine API: cloud machines for people and agents, paid by the second.
Python 3.9 or later, and nothing else: it uses only the standard library.

## Install

```sh
pip install wheelmachine
export WHEELMACHINE_API_KEY=ak_live_...
```

## Quick start

```python
from pathlib import Path
from wheelmachine import Wheelmachine

client = Wheelmachine()  # reads WHEELMACHINE_API_KEY
machine = client.machines.create(name="demo")
machine = client.machines.wait(machine.id, "running")  # at once if it already is
print(client.machines.exec(machine.id, ["uname", "-a"]).stdout)
client.machines.write_file(machine.id, "work/notes.txt", Path("notes.txt"))  # a file in...
client.machines.read_file(machine.id, "work/notes.txt", to="notes-back.txt")  # ...and back out
client.machines.delete(machine.id)
```

## Signing in through the browser

A program with no key yet can sign in the way `wm login` does: the person approves it in their browser, signed in
with GitHub or Google, and the program is given a key of its own, once.

```python
from wheelmachine import DeviceLogin, Wheelmachine

login = DeviceLogin.start(name="my-tool on laptop")      # scopes=[...] asks for less than everything
print("Open", login.verification_uri, "and enter", login.user_code)
key = login.wait()                                        # key.secret is the new key: keep it now
client = Wheelmachine(api_key=key.secret)
```

## Credit

```python
credit = client.credit.get()                    # what is left: running time taken from it
print(credit.balance_usd, credit.empty)
for entry in client.credit.entries():           # every entry, the newest first
    print(entry.at, entry.kind, entry.amount_usd, entry.machine_name)
purchase = client.credit.buy(25)                # buy $25: pay it at purchase.checkout.url
print(purchase.checkout.url)
```

At zero the account's machines are paused, and none starts until the balance is above $0. Credit is bought in $10, $25, $50
or $100, from Paddle, our reseller, which adds any sales tax or VAT and e-mails the receipt and tax invoice: open
`checkout.url` in a browser signed in to the dashboard, in the same account, and the credit is there once Paddle
confirms the payment. In an organisation the credit is the organisation's, read by its owners, admins and billing, and
bought by its owners and billing.

## Organisations and the audit

```python
from datetime import datetime, timezone

for org in client.orgs.list():                 # with a key of your own account
    print(org.id, org.name, org.role)
me = client.me()                                # with a key made in an organisation
print(me.in_org, me.role, me.teams, me.limits)
since = datetime(2026, 10, 1, tzinfo=timezone.utc)
for event in client.audit.list(action="machine.", since=since):
    print(event.at, event.person.name if event.person else event.by, event.action, event.target)
client.audit.export("csv", since=since, to="audit.csv")  # whole: a year at most, 100,000 events at most
```

`DeviceLogin.start(account="acc_...")` asks for a key made in an organisation. The audit is read with a key that holds
every scope, and in an organisation by its owners and admins.

## Handing off and forking, in an organisation

```python
q = client.machines.fork_for(machine.id, "ravi@example.com")    # cleared before it is theirs; hand_off() goes exactly as it is
print(q.state, q.waiting_on)                                      # waiting on you ("asker"): approve it in the dashboard
q = client.requests.get(q.id)                                     # then done and the fork in q.result, or on whom it waits
asked = client.requests.ask("fork", "ravi", why="for the demo")   # Ravi picks which machine as they approve
client.requests.take_back(asked.id)
mine = client.machines.fork(machine.id)                           # a member's own: a MachineRequest until it is approved
print(client.projects.rules(machine.project).forks)              # ask or off
```

People approve and decline in the dashboard, and a team's rules are set there. A request made with a key waits first
for the OK there of the person who made the key. A machine handed off arrives paused, its memory kept, until its
receiver starts it. A member's own fork waits too, for the OK of a full manager of
its team, an owner or an admin: `machines.fork` answers it with the request, and the fork is its `result` once made.

## What to know

- **The client.** `Wheelmachine(api_key=None, base_url=None, timeout=60.0, max_retries=3, proxy=None)`. The key
  comes from `WHEELMACHINE_API_KEY` and the address from `WHEELMACHINE_BASE_URL` when not given. The key is never
  logged, printed, or put in an error or a repr. One client is safe to share between threads, and keeps its
  connections open between calls; `close()` or a `with` block closes them. HTTPS_PROXY and NO_PROXY are followed.
- **Errors.** Everything raised is a `WheelmachineError`, with `status`, `code` (the API's stable code), `detail`,
  `request_id` (give it when you ask us about a call) and `retry_after`. By family: `BadRequestError` (400),
  `AuthenticationError` (401), `PaymentRequiredError` (402), `PermissionDeniedError` (403), `NotFoundError` (404),
  `WaitTimeoutError` (408), `ConflictError` (409, 422), `RateLimitError` (429), `InternalServerError` (500),
  `UnavailableError` (502, 503, 504), and `APIConnectionError` / `APITimeoutError` when no answer came.
- **Retries.** A call refused for a moment (429, 503) is tried again after the API's `Retry-After`, or a backoff with
  full jitter; so is a call that lost its connection - but a command (`exec`), `start`, `pause` and the other
  actions are not sent again once they went out, since they may have run. A call that creates something (a
  machine, fork, backup, project or key, a hand-off or an ask) carries an `Idempotency-Key`, the same on every retry, so a retry never
  makes two; pass your own with `idempotency_key=` to retry across restarts. A 503 `not_open` is something not
  open, not a temporary error: it is never tried again.
- **Long calls.** Calls the API holds by design - making a machine ready (`wait_s`), `wait()`, `exec()` (up to 10
  minutes), `archive()` - are given the time they need on top of `timeout`.
- **Lists** page by themselves: `for m in client.machines.list(state="running", labels={"team": "ml"})`.
- **Files** are `home/...` or `work/...` in the machine. `write_file` streams bytes, a `pathlib.Path` or a binary
  file object with its length; `read_file` gives bytes, or streams into a path (whole or not at all) or a file object.
- **Backups** keep a machine's files as they are: `backup = client.machines.backup(machine.id, note="before")`. A fork
  is made from one, starting afresh, at the machine's size or another: `client.machines.fork(machine.id,
  backup=backup, cpus=8)`, or `backup="latest"` for its newest. `client.machines.backups(machine.id)` lists a
  machine's, the newest first, and `client.machines.backups()` every one the key reaches.
- **Answers** are typed objects with the API's own field names; each keeps its JSON in `raw`, and one a call answered
  with says how it came in `last_response` (`request_id`, `replayed`, `rate_limit`).
- **What a machine used**: `client.machines.metrics(machine.id, since="6h", step=300)` gives its CPUs (a share
  of its own size), memory, disk and network a point a step; `since` is also a `timedelta` or an aware `datetime`.
  A point a minute is kept, for up to the last day.
- **Secrets** are values every command in a machine gets as environment variables:
  `client.secrets.set("API_TOKEN", token)`, then `client.machines.create(secrets=["API_TOKEN"])` or
  `client.machines.update(machine.id, secrets=["API_TOKEN"])` (the names said whole; `[]` takes them away).
  Hold what a machine reaches on the internet with `network=` on `create`/`update` - `{"internet": "closed"}`, or
  `{"internet": "allowed", "allow": ["api.github.com", "*.githubusercontent.com", "140.82.112.0/20"]}` (names for web,
  ranges for other ports); a running machine's policy changes at once, without a restart. `machine.network` shows it.
  `client.secrets.list()` gives names and the machines given each - a value is never answered again, and never put
  in an error. Read the value from somewhere safe (a file, a vault, the environment), never write it in code.
- **Your own AI keys** are set once, under the names agents read them by: `client.secrets.set("ANTHROPIC_API_KEY",
  key)` keeps it outside every machine - each holds a placeholder, and the key is sent only to its provider's hosts -
  and every machine gets it (`machine.ai_keys`). `every_machine=False` keeps it on the machines given it, and
  `outside=False` makes it an ordinary secret. `client.secrets.presets()` lists the names and where each key goes.

## Ports

A program listening on a port in a machine is reached at a web address of its own,
`https://<port>-<the machine's id without m_>.run.wheelmachine.ai`. HTTP, WebSockets and streams pass through, and a
paused, stopped or archived machine is started by the first visit. The address is private unless the port is opened with
`public=True`: it answers a call carrying an API key that can see the machine (the key never reaches the program),
and a browser that opened a link made for it. Anyone else is answered 401.

```python
import os
import urllib.request
import webbrowser

port = client.ports.open(machine.id, 8080)  # a program in the machine listens on 8080
headers = {"Authorization": "Bearer " + os.environ["WHEELMACHINE_API_KEY"]}
print(urllib.request.urlopen(urllib.request.Request(port.url, headers=headers)).status)  # in with the key
link = client.ports.link(machine.id, 8080)  # or in with a link, for one browser
webbrowser.open(link.url)  # it opens once, within five minutes
print([(p.port, p.public) for p in client.ports.list(machine.id)])  # lowest first
client.ports.close(machine.id, 8080)  # the address answers 404 from then on
```

`link()` also takes `access_s`, how long the browser that opens it stays in (1800 seconds by default, 60 to 3600),
and `path`, where it lands (`/` by default). A link's `url` is in that answer only, and never in a repr. A port open
already is made public or private as asked, so `open` is sent again after a lost connection; `link` never is once
it may have reached the API, not even when refused for a moment, since each call makes a new link. A machine has at
most 20 ports open, and the few it uses itself cannot be opened.

## Apps

A machine carries apps, on by default and turned on and off: a desktop and a code editor in the browser and an agent's
own page (`kind` `web`), a model server reached from inside the machine (`service`), and agents that each keep a
session of their own in its terminal (`agent`). The desktop and Gemini CLI start off, until they are turned on; the code editor is always on, and turning it
off is refused with `ConflictError`. A web app is reached at the machine's private address for its port; asking for a
link into one that is on opens that address private when it is not open.

```python
import webbrowser

for app in client.apps.list(machine.id):
    print(app.name, app.kind, app.on)
desktop = client.apps.turn_on(machine.id, "desktop")  # its port opened private, if it was not
webbrowser.open(desktop.link)  # it opens once, within five minutes, on the app's own page
again = client.apps.link(machine.id, "desktop")  # a fresh link, while it is on
client.apps.show(machine.id, "openclaw")  # its window on the machine's own screen, signed in
client.apps.turn_off(machine.id, "desktop")
```

An app with a token of its own answers it once, in `token`, in the answer to `turn_on`. `link` and `token` are
never in a repr. `turn_on` and `link` are never sent again once they may have reached the API; `turn_off` is. A machine
with no apps answers `ConflictError` with the code `no_apps`.

## Terminals

An interactive terminal in a machine (a login shell, or one command on a terminal of its own) is a WebSocket, not a
call this library makes: a WebSocket client is a dependency, and this library has none. Open one at

```
GET wss://api.wheelmachine.ai/v1/machines/{machine_id}/terminal
    Authorization: Bearer ak_live_...
```

with the `cloud:machines:exec` scope. The query sets it up: `cols`, `rows`, `user` (`user` or `root`), `term`, `cwd`,
and `cmd` repeated for each word of a command (left out, a login shell opens). A paused machine is woken for it.

Over the socket, each message is one event. **From you:** a *binary* message is typed in (keystrokes, bytes as they
are); a *text* message `{"resize":{"cols":C,"rows":R}}` sets the window. **From us:** a *binary* message is what the
program wrote; a final *text* message `{"exit_code":N}` (or `{"reason":...,"message":...}` when it did not end by
itself) says how it ended, just before the socket closes. We never log what is typed or shown.

For a ready-made interactive client, `wm ssh <machine>` (the `wm` command-line tool) drives this endpoint: raw local
terminal, window changes followed, and your shell's own exit code.

For `scp`, `sftp` and editors' remote-SSH, `POST /v1/ssh/certificates` signs a short-lived SSH certificate (give a
public key and a machine; good for at most an hour, for that machine alone) to present to the SSH server. `wm
ssh-config <machine>` prints the matching `~/.ssh/config` block.

## Tests

```sh
cd frontend/sdk/python && python3 -m pytest -q      # or: PYTHONPATH=src:tests python3 -m unittest discover -s tests
```

They run against a stand-in for the API on 127.0.0.1; nothing reaches the network.
