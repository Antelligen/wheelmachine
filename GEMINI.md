# Wheelmachine machines

The `wheelmachine` MCP tools make, fork and run Wheelmachine machines - computers in the cloud, made usually in about
a second and paid by the second - and open their apps, in the account of the API key they were set up with. Use them when work
needs a machine of its own: to build or test code away from this computer, run something long, try a change on a copy,
or give a person a desktop or a code editor to open in their browser.

- A machine is named by its id (`m_...`) or its name. File paths begin `home/` or `work/` (where commands start).
- `create_machine` with a `name` and `labels` to find it by again; it answers once the machine runs.
- `run_command` runs `command` (a program and its arguments) or `shell` (a line of shell), for up to 10 minutes; start
  longer work in the background and read its log with `read_file`. A non-zero `exit_code` is the command's own answer.
- `write_file` and `read_file` put files in and read them back.
- `fork_machine` makes a new machine from one as it is now, or, with `backup`, from one of its backups
  (`backup_machine`, `list_backups`). In an organisation a member's fork may wait for an OK: its answer's `note` says so.
- `list_apps`, `turn_app_on`, `open_app`, `show_app` and `turn_app_off` work its apps. A web app's `link` lets one
  browser in once, within five minutes: give it to the person, and do not keep it.
- Running time is charged by the second: `pause_machine` or `stop_machine` what is not in use, and `delete_machine` -
  for good, with its files and backups - what is done with.
- A failed tool answers the API's `code` and `detail`: `forbidden` (the key does not allow it), `not_found`,
  `rate_limited` (wait `retry_after_s`), `payment_required` (the account cannot spend now), `invalid_request`.
- A call that timed out may still have done its work, and a command may still be running: look with `list_machines` before
  calling `create_machine` or `fork_machine` again, or you can make a second machine.
- Long output is cut, with `truncated` true, and a command with huge output may be reported as failed though it ran: write
  big output to a file, and do not just run the command again.
