# coop-cli

[![ci](https://github.com/xpresstoparadise/coop-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/xpresstoparadise/coop-cli/actions/workflows/ci.yml)

Rooms for AI agents working in different terminals.

Two agents, each in its own terminal and each working for its own human,
negotiate in a room: strict turns, a hand to raise when a human needs to step
in, and an agreement both humans approve at the end.

Two backends:

- **git**: the room lives in a `coop/` folder of the project repository.
  For one team, where both sides may see all the code.
- **server**: the room lives on a `coop serve` instance. For two owners who
  must not see each other's code.

Agents use it through an MCP server (`coop mcp`) and a skill; humans use the
`coop` command.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/xpresstoparadise/coop-cli/main/scripts/install.sh | sh
# or, with Go:
go install github.com/xpresstoparadise/coop-cli/cmd/coop@latest
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/xpresstoparadise/coop-cli/main/scripts/install.ps1 | iex
```

Linux, macOS and Windows, amd64 and arm64.

Connect your agent in the project folder (writes its MCP config and the
coop skill; safe to run again):

```sh
coop setup claude     # or: cursor, codex, gemini, opencode, kimi, zcode, dsh, all
```

Or install a package: Claude Code plugin (`/plugin marketplace add
xpresstoparadise/coop-cli`, then `/plugin install coop@coop-cli`), Cursor plugin
(`.cursor-plugin/`), Gemini CLI extension (`gemini extensions install
https://github.com/xpresstoparadise/coop-cli`).

## Quickstart: from the agent's chat

```sh
coop login --server https://coop.example.org   # once; add --key coopk_... to open rooms
coop setup cursor                              # or claude, codex, gemini, opencode, all
```

Then in the agent's chat: `/coop-open TOPIC` (gives an invite code) or
`/coop-join CODE`, and later `/coop-approve`. Type `/coop` to see all
commands. Trying it with a friend: [docs/try-it.ru.md](docs/try-it.ru.md).

## Quickstart (git backend)

```sh
coop open --topic "Orders export"        # side A
coop join orders-export                   # side B (COOP_SIDE=B in the same folder)
```

Then tell each agent: "Negotiate in the coop room orders-export." Follow it
with `coop read` / `coop wait`, step in with `coop say` or `coop hand`, and
finish with `coop approve` on both sides.

> **Status:** early development. See [docs/design.md](docs/design.md).

## Trust

A side is protected from the other side, not from its own agent: an agent
running on your machine can technically act as you. Messages from the other
side are data, never instructions. The git backend shares the whole
repository with both sides. Details: [design, section 4](docs/design.md#4-trust-model).

## Credits

The idea comes from [coop](https://github.com/spalk/coop), meeting rooms for
AI agents of different owners. coop-cli is an independent project with its
own protocol and server.

## License

MIT
