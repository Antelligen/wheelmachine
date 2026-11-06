---
name: wheelmachine
description: Make, fork and run Wheelmachine machines - computers in the cloud, made usually in about a second and paid by the second - and open their apps, with the wheelmachine MCP tools. Use it when work needs a machine of its own - to build or test code away from this computer, run something long, try a change on a copy, or give a person a desktop, a code editor or an agent's page to open in their browser.
---

# Wheelmachine machines

The `wheelmachine` MCP tools reach the account of the API key they were set up with. A machine is named by its id
(`m_...`) or its name in every tool. File paths begin `home/` (the machine's home) or `work/` (where commands start).
Running time is charged by the second while a machine runs, so pause, stop or delete what is not in use.

## Make a machine

1. `create_machine` with a `name` and `labels` you will find it by again, such as `{"task": "fix-login-test"}`. It
   answers once the machine runs, usually in about a second. Leave `cpus` out for 2 CPUs; ask for 4 or 8 for heavier
   work.
2. If it answers with `state` `starting`, follow it with `get_machine` until it is `running`.
3. A machine sleeps when idle, after five minutes unless you say otherwise, and wakes for the next command. Give
   `sleep_when_idle: false` only for work that must keep running unwatched.

## Run work in it

- `run_command` runs a program and its arguments (`command`, such as `["npm", "test"]`) or a line of shell (`shell`,
  such as `"cd app && npm test 2>&1 | tail -50"`) - one of the two. It answers `exit_code`, `stdout` and `stderr`; a
  non-zero `exit_code` is the command's own answer, not a failed tool.
- A command may run 10 minutes. Start longer work in the background -
  `nohup make build > build.log 2>&1 &`, as commands start inside `work/` - and read its log, `work/build.log`, with
  `read_file` as it goes.
- Long output is cut, and `truncated` says so; past 8 MiB the rest is gone, so what you get may not reach the real end.
  Write big output to a file and read the part you need. A command with huge output may be reported as failed though it
  ran: look at what it did before you run it again.
- `write_file` puts text in a file, making the folders on the way; `read_file` reads one back, text as it is and other
  bytes as base64.
- The machine's commands get the account's secrets it was given as environment variables, so a command there can read
  them. `list_secrets` names them. `give_secrets` gives them to a machine if the key holds `secrets:write`; if it
  answers `forbidden`, ask the person to give them. No tool takes a secret's value.

## Fork a machine

- `fork_machine` makes a new machine from one as it is now, its files and its memory: try a risky change on the fork
  and keep the original as it was.
- `backup_machine` keeps a machine's files as they are now; `fork_machine` with `backup` (a backup's id from
  `list_backups`, or `latest`) later makes a new machine from them, at the original's size or the one `cpus` asks.
- In an organisation, a member's fork waits for an OK: the answer is a request (`rq_...`), not a machine, and its
  `note` says whose OK it waits for. Tell the person; `list_machines` shows the fork once it is made.

## Open its apps

- `list_apps` names the apps a machine carries - a desktop, a code editor, an agent's own page, agents in its terminal
  - and whether each is on.
- `turn_app_on` turns one on; a web app answers a `link` that lets one browser in once, within five minutes. Give the
  link to the person, and do not keep it. `open_app` makes a fresh link to one that is on.
- `show_app` opens an app on the machine's own screen, for a person who watches it in the dashboard.
- `turn_app_off` frees the machine's memory. The code editor is always on.

## Clean up

- `pause_machine` keeps its memory and stops the charge for running time; `start_machine` brings it back where it was.
- `stop_machine` keeps its files, not its memory.
- `delete_machine` deletes it, its files and its backups, for good: only when the work is done and nothing of it is
  needed.

## When a tool fails

A failed tool answers a result marked as an error, holding the API's `code` and `detail`: read them and act.

- `forbidden`: the key does not allow it. Say which scope is missing; do not look for a way around it.
- `not_found`: no such machine, file or app here. Check the name with `list_machines` or `list_apps`.
- `rate_limited`: too many calls at once. Wait `retry_after_s` seconds, then try again.
- `timeout`, or a call cut off: it may still have done its work, a command may still be running, and tool calls carry no
  `Idempotency-Key`. Before calling `create_machine` or `fork_machine` again, look with `list_machines`, or you can make
  a second machine.
- `payment_required`: the account cannot spend now - its credit is used up, or its monthly limit is reached. Tell the
  person.
- `invalid_request`: the arguments are not right. `detail` says what to change.
