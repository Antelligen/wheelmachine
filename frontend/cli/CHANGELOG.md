# Changelog

`wm`, the Wheelmachine command line, and `wm-mcp`, its MCP server for agents. Each release is tagged `wm-v<version>`.

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
