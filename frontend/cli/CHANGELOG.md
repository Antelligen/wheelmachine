# Changelog

`wm`, the Wheelmachine command line, and `wm-mcp`, its MCP server for agents. Each release is tagged `wm-v<version>`.

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
