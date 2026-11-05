# Wheelmachine's tools

Wheelmachine is computers in the cloud for people and their agents, paid by the second, through one API. This
repository holds the tools that call that API, open under the Apache License 2.0:

| Tool | Here |
|---|---|
| `wm`, the command line, and `wm-mcp`, the MCP server for agents | [`frontend/cli`](frontend/cli) |
| The Python library | [`frontend/sdk/python`](frontend/sdk/python) |
| The TypeScript library | [`frontend/sdk/typescript`](frontend/sdk/typescript) |

To install them:

    curl -fsSL https://wheelmachine.ai/install.sh | sh    # wm and wm-mcp, on Linux or macOS
    pip install wheelmachine                              # the Python library
    npm install wheelmachine                              # the TypeScript library

Archives of `wm` for Linux, macOS and Windows are in this repository's [releases](../../releases).

The service itself is not here. The API's contract is [`contracts/openapi.yaml`](contracts/openapi.yaml), and the
docs are at https://docs.wheelmachine.ai.

Each release is built and published from this repository, so every download can be traced to its source. To check one
of `wm`'s archives:

    sha256sum --check --ignore-missing SHA256SUMS
    gh attestation verify wm_<version>_linux_amd64.tar.gz --repo Antelligen/wheelmachine

Found a weakness? Write to security@wheelmachine.ai rather than opening an issue.

Copyright 2026 Antelligen Private Limited. Each tool's folder holds its LICENSE and NOTICE.
