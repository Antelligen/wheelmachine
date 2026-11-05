# Wheelmachine for TypeScript

The official TypeScript library for the Wheelmachine API: cloud machines for people and agents, paid by the second.
Node 18 or later, ES modules, and nothing else: it calls the API with the runtime's own `fetch`.

## Install

```sh
npm install wheelmachine
export WHEELMACHINE_API_KEY=ak_live_...
```

## Quick start

```ts
import { Wheelmachine } from "wheelmachine";

const client = new Wheelmachine(); // reads WHEELMACHINE_API_KEY
let machine = await client.machines.create({ name: "demo" });
machine = await client.machines.wait(machine.id, "running"); // at once if it already is
console.log((await client.machines.exec(machine.id, ["uname", "-a"])).stdout);
await client.machines.writeFile(machine.id, "work/notes.txt", { path: "notes.txt" }); // a file in...
await client.machines.readFile(machine.id, "work/notes.txt", { to: "notes-back.txt" }); // ...and back out
await client.machines.delete(machine.id);
```

## Signing in through the browser

A program with no key yet can sign in the way `wm login` does: the person approves it in their browser, signed in
with GitHub or Google, and the program is given a key of its own, once.

```ts
import { DeviceLogin, Wheelmachine } from "wheelmachine";

const login = await DeviceLogin.start({ name: "my-tool on laptop" }); // scopes: [...] asks for less than everything
console.log(`Open ${login.verificationUri} and enter ${login.userCode}`);
const key = await login.wait(); // key.secret is the new key: keep it now
const client = new Wheelmachine({ apiKey: key.secret });
```

## Credit

```ts
const credit = await client.credit.get(); // what is left: running time taken from it
console.log(credit.balance_usd, credit.empty);
for await (const entry of client.credit.entries()) console.log(entry.at, entry.kind, entry.amount_usd, entry.machine_name);
const purchase = await client.credit.buy(25); // buy $25: pay it at purchase.checkout.url
console.log(purchase.checkout.url);
```

At zero the account's machines are paused, and none starts until the balance is above $0. Credit is bought in $10, $25, $50
or $100, from Paddle, our reseller, which adds any sales tax or VAT and e-mails the receipt and tax invoice: open
`checkout.url` in a browser signed in to the dashboard, in the same account, and the credit is there once Paddle
confirms the payment. In an organisation the credit is the organisation's: read by its owners, admins and billing, and
bought by its owners and billing.

## Organisations and the audit

```ts
for (const org of await client.orgs.list()) console.log(org.id, org.name, org.role); // a key of your own account
const me = await client.me(); // a key made in an organisation
console.log(me.account.kind, me.role, me.teams, me.limits);
const since = new Date(Date.UTC(2026, 9, 1));
for await (const event of client.audit.list({ action: "machine.", since })) {
  console.log(event.at, event.person?.name ?? event.by, event.action, event.target);
}
await client.audit.export({ format: "csv", since }, { to: "audit.csv" }); // whole: a year at most, 100,000 events at most
```

`DeviceLogin.start({ account: "acc_..." })` asks for a key made in an organisation. The audit is read with a key that
holds every scope, and in an organisation by its owners and admins.

## Handing off and forking, in an organisation

```ts
let q = await client.machines.forkFor(machine.id, { to: "ravi@example.com" }); // cleared before it is theirs; handOff() goes as it is
console.log(q.state, q.waiting_on); // waiting on you ("asker"): approve it in the dashboard
q = await client.requests.get(q.id); // then done and the fork in q.result, or on whom it waits
const asked = await client.requests.ask({ kind: "fork", of: "ravi", why: "for the demo" }); // Ravi picks which machine
await client.requests.takeBack(asked.id);
const mine = await client.machines.fork(machine.id); // a member's own: object "request" until it is approved
console.log((await client.projects.rules(machine.project)).forks); // ask or off
```

People approve and decline in the dashboard, and a team's rules are set there. A request made with a key waits first
for the OK there of the person who made the key. A machine handed off arrives paused, its memory kept, until its
receiver starts it. A member's own fork waits too, for the OK of a full manager of
its team, an owner or an admin: `machines.fork` answers it with the request, and the fork is its `result` once made.

## What to know

- **The client.** `new Wheelmachine({ apiKey, baseURL, timeout: 60_000, maxRetries: 3, fetch })`. The key comes from
  `WHEELMACHINE_API_KEY` and the address from `WHEELMACHINE_BASE_URL` when not given; it is kept in a private field,
  out of JSON, `console.log` and every error. To go through a proxy, pass a `fetch` that does.
- **Not in a web page.** The library refuses to run in a browser, where the API key would reach everyone who opens the
  page: call the API from your server. `dangerouslyAllowBrowser: true` turns this off, for a page only you open.
- **Names.** Methods are camelCase (`readFile`, `writeFile`); what calls take and give keeps the API's own field
  names (`wait_s`, `exit_code`), so the API's reference reads one to one. Every call takes a last `options`
  argument: `timeout`, `maxRetries`, `signal` (an AbortSignal), and for creates `idempotencyKey`.
- **Errors.** Everything a call throws is a `WheelmachineError`, with `status`, `code` (the API's stable code),
  `detail`, `requestId` and `retryAfter`. By family: `BadRequestError` (400), `AuthenticationError` (401),
  `PaymentRequiredError` (402), `PermissionDeniedError` (403), `NotFoundError` (404), `WaitTimeoutError` (408),
  `ConflictError` (409, 422), `RateLimitError` (429), `InternalServerError` (500), `UnavailableError` (502, 503,
  504), and `APIConnectionError` / `APITimeoutError` when no answer came. An argument it cannot use is a TypeError.
- **Retries.** A call refused for a moment (429, 503) is tried again after the API's `Retry-After`, or a backoff with
  full jitter; so is a call that lost its connection - but `exec`, `start`, `pause` and the other actions are not
  sent again once they went out, since they may have run. A call that creates something carries an
  `Idempotency-Key`, the same on every retry, so a retry never makes two. A 503 `not_open` is something that is not
  open yet: it is not tried again.
- **Long calls.** Calls the API holds by design are given the time they need. Node's fetch waits at most 300 seconds
  for an answer, so waits longer than 240 seconds are asked for in steps, and a command that runs longer than five
  minutes is best started in the background and followed with its output in a file.
- **Lists** page by themselves: `for await (const m of client.machines.list({ state: "running" }))`.
- **Files** are `home/...` or `work/...` in the machine. `writeFile` streams bytes, a Blob, a file on this computer
  (`{ path }`), or a stream with `contentLength`; `readFile` gives bytes, or streams into a local path (whole or not
  at all) or a WritableStream.
- **Backups** keep a machine's files as they are: `const backup = await client.machines.backup(machine.id, { note:
  "before" })`. A fork is made from one, starting afresh, at the machine's size or another:
  `client.machines.fork(machine.id, { backup, cpus: 8 })`, or `{ backup: "latest" }` for its newest.
  `client.machines.backups(machine.id)` lists a machine's, newest first, and `client.machines.backups()` every one the
  key reaches.
- **Answers** are plain objects; one a call answered with says how it came in `lastResponse` (`requestId`,
  `replayed`, `rateLimit`), which JSON leaves out.
- **What a machine used**: `client.machines.metrics(id, { since: "6h", step: 300 })` gives its CPUs (a share
  of its own size), memory, disk and network a point a step; `since` is also a Date. A point a minute is kept, for
  up to the last day.
- **Secrets** are values every command in a machine gets as environment variables:
  `await client.secrets.set("API_TOKEN", token)`, then `client.machines.create({ secrets: ["API_TOKEN"] })` or
  `client.machines.update(id, { secrets: ["API_TOKEN"] })` (the names said whole; `[]` takes them away).
  Hold what a machine reaches on the internet with `network` on `create`/`update` - `{ internet: "closed" }`, or
  `{ internet: "allowed", allow: ["api.github.com", "*.githubusercontent.com", "140.82.112.0/20"] }` (names for web,
  ranges for other ports); a running machine's policy changes at once, without a restart. `machine.network` shows it.
  `client.secrets.list()` gives names and the machines given each - a value is never answered again, and never put
  in an error. Read the value from somewhere safe (a file, a vault, the environment), never write it in code.
- **Your own AI keys** are set once, under the names agents read them by: `await client.secrets.set("ANTHROPIC_API_KEY",
  key)` keeps it outside every machine - each holds a placeholder, and the key is sent only to its provider's hosts -
  and every machine gets it (`machine.ai_keys`). `{ every_machine: false }` keeps it on the machines given it, and
  `{ outside: false }` makes it an ordinary secret. `client.secrets.presets()` lists the names and where each key goes.

## Ports

A program listening on a port inside a machine is reached at a web address of its own,
`https://<port>-<the machine's id without m_>.run.wheelmachine.ai`. HTTP, WebSockets and streams pass through, and a
paused, stopped or archived machine is started by the first visit.

```ts
const port = await client.ports.open(machine.id, 8080); // private; port.url is its address
const link = await client.ports.link(machine.id, 8080, { access_s: 600, path: "/app" }); // link.url: one browser in
await client.ports.open(machine.id, 8080, { public: true }); // anyone may reach it now
console.log((await client.ports.list(machine.id)).map((p) => p.url)); // lowest port first
await client.ports.close(machine.id, 8080); // the address answers 404 from then on
```

- **Private unless `public`.** A private address answers a call with an API key that can see the machine (the key
  never reaches the program) and a browser that opened a link made for it; anyone else gets 401. Opening a port open
  already makes it public or private as asked (private when `public` is left out), so `open` is sent again after a
  lost connection.
- **A link is a secret.** It opens once, within five minutes, and lets the browser that opens it in for `access_s`
  seconds (1800). Its `url` is in that answer only, like a new key's `secret`: give it to the one person it is for.
  Each call makes a new link, so `link` is never sent again once it may have reached the API.

## Apps

A machine carries apps, on by default and turned on and off: a desktop and a code editor in the browser and an agent's
own page (`kind` `"web"`), a model server reached from inside the machine (`"service"`), and agents that each keep a
session of their own in its terminal (`"agent"`). The desktop and Gemini CLI start off, until they are turned on; the code
editor is always on, and turning it off is refused with `ConflictError`. A web app is reached at the machine's private address for its
port; asking for a link into one that is on opens that address private when it is not open.

```ts
for (const app of await client.apps.list(machine.id)) console.log(app.name, app.kind, app.on);
const desktop = await client.apps.turnOn(machine.id, "desktop"); // its port opened private, if it was not
console.log(desktop.link); // opens once, within five minutes, on the app's own page
const again = await client.apps.link(machine.id, "desktop"); // a fresh link, while it is on
await client.apps.show(machine.id, "openclaw"); // its window on the machine's own screen, signed in
await client.apps.turnOff(machine.id, "desktop");
```

An app with a token of its own answers it once, in `token`, in the answer to `turnOn`. `turnOn` and `link` are never
sent again once they may have reached the API; `turnOff` is.

## Terminals

An interactive terminal in a machine (a login shell, or one command on a terminal of its own) is a WebSocket, not a
call this library makes: a WebSocket client would be a dependency, and this library has none (it uses only the runtime's
`fetch`). Open one at

```
GET wss://api.wheelmachine.ai/v1/machines/{machine_id}/terminal
    Authorization: Bearer ak_live_...
```

with the `cloud:machines:exec` scope. The query sets it up: `cols`, `rows`, `user` (`user` or `root`), `term`, `cwd`,
and `cmd` repeated for each word of a command (left out, a login shell opens). A paused machine is woken for it.

Each message is one event. **From you:** a *binary* message is typed in (keystrokes, bytes as they are); a *text*
message `{"resize":{"cols":C,"rows":R}}` sets the window. **From us:** a *binary* message is what the program wrote;
a final *text* message `{"exit_code":N}` (or `{"reason":...,"message":...}` when it did not end by itself) says how it
ended, just before the socket closes. We never log what is typed or shown. In the browser, open it with the native
`WebSocket` and carry the key in the URL only over `wss`; in Node, use a WebSocket library such as `ws`.

For a ready-made interactive client, `wm ssh <machine>` (the `wm` command-line tool) drives this endpoint: raw local
terminal, window changes followed, and your shell's own exit code.

For `scp`, `sftp` and editors' remote-SSH, `POST /v1/ssh/certificates` signs a short-lived SSH certificate (give a
public key and a machine; good for at most an hour, for that machine alone) to present to the SSH server. `wm
ssh-config <machine>` prints the matching `~/.ssh/config` block.

## Tests

```sh
cd frontend/sdk/typescript && npx tsc --noEmit && npm test
```

They run against a stand-in for the API on 127.0.0.1; nothing reaches the network.
