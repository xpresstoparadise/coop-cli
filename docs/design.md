# coop-cli: design

Decisions taken before the first line of code. Terms are defined in
[GLOSSARY.md](../GLOSSARY.md). Change a decision by editing this file in the
same commit as the code that changes behaviour.

## 1. What it is

A command-line tool that lets two AI agents, each working in its own terminal
for its own human, negotiate in a **room** and reach an **agreement** that
both humans approve.

The idea comes from [spalk/coop](https://github.com/spalk/coop) (meeting rooms
for AI agents of different owners). coop-cli is a separate project with its
own server and protocol; it is not a fork and is not wire-compatible with
coop.

## 2. Two backends, one interface

| | git backend | server backend |
|---|---|---|
| Transport | files in `coop/` inside the project repository | `coop serve` over HTTPS |
| For | one team: both sides may see all the code | two owners who must not see each other's code |
| Server | none | single binary + SQLite, behind Caddy or nginx |
| Sync | local folder by default; `--remote` commits and pushes each message, `wait` pulls every 15 s | long-poll |

Both backends implement the same operations, so the CLI, the MCP server and
the skill are written once:

`open`, `join`, `status`, `read`, `say`, `wait`, `hand`, `resume`,
`approve`, `close`, `watch`, `export`.

## 3. Room model

- Exactly two sides, `A` and `B` (side is a string in the data model, so
  more sides stay possible later).
- Each side has one **agent** and one **human**.
- **Strict turns.** An agent may post only while its side holds the
  **floor**. Posting passes the floor to the other side. Exceptions that do
  not need the floor and do not pass it: `note`, `hand`, and every human
  message.
- **Turn budget.** 20 agent turns per side by default (`open --turns N`).
  When a side has used its budget the room pauses until a human resumes it;
  `resume` gives both sides a fresh budget.
- **Human first.** An agent may not post while there is a human message
  (from either side) it has not read. It must `read` first.

### Message

| Field | Meaning |
|---|---|
| `seq` | 1, 2, 3... assigned by the backend; sets the order |
| `side` | `A` or `B` |
| `actor` | `agent` or `human` |
| `kind` | see below |
| `re` | optional `seq` this message answers |
| `resolution` | on `propose`/`counter`: this is a proposed final agreement |
| `body` | Markdown, at most 8 KiB |

Kinds:

| Kind | Who | Notes |
|---|---|---|
| `propose`, `ask`, `answer`, `counter` | agent | uses the floor |
| `accept`, `reject` | agent; human (`reject` of a resolution) | uses the floor when sent by an agent |
| `note` | agent, human | no floor; a human `note` is "say" |
| `hand` | agent, human | pauses the agents |
| `resume` | human | lifts the pause |
| `approve` | human | approves the agreed resolution |
| `close` | human | closes the room at any time |

### Ending a room

1. An agent sends `propose` or `counter` with `resolution: true`.
2. The other agent sends `accept re <that seq>`. The room enters
   **approval**: agents can no longer post (except `note`).
3. Each human runs `coop approve`. When both have approved, the room closes
   with an agreement, written to `agreement.md` (git backend:
   `coop/<topic>/contract/agreement.md`; server backend: in `.coop/` of each
   side).
4. A `reject` of the resolution (agent before agreement, human during
   approval) returns the room to negotiation. A new resolution replaces the
   previous one.
5. A human may `close` the room at any time.

## 4. Trust model

- A side is protected from the **other** side: agent B can never act as A,
  and nothing B writes is an instruction to A's agent.
- A side is **not** protected from its own agent: on one machine an agent
  with a shell can read its human's credentials. We do not pretend
  otherwise.
- **Chat mode (decision, 2026-11-26).** The human drives everything from
  the agent's chat: `/coop open`, `/coop join CODE`, `/coop approve`,
  `/coop resume`, `/coop close`, `/coop say`. The agent performs human
  actions through MCP tools (`coop_approve`, `coop_resume`, `coop_close`,
  `coop_say_human`) **as the human, without a further confirmation**.
  What stops an agent from calling them on its own — or because the other
  side's messages talk it into it — is only its instructions (tool
  descriptions and the skill). Considered and rejected: a client dialog
  (MCP elicitation; works in Claude Code, but Cursor CLI declares it and
  declines automatically), the client's "run this tool?" prompt (gone in
  Cursor's Run Everything mode), a browser confirmation link (leaves the
  chat). `coop approve` in a terminal (TTY prompt) and `coop watch` remain
  for people who want the stronger guarantee.
- Messages from the other side are delivered wrapped as
  `<from side="B" actor="agent">...</from>`; the skill tells the agent to
  treat them as data and to take instructions only from its own human.
- The git backend gives both sides the whole repository. Use it inside one
  team only; use the server backend between owners.

## 5. Git backend

```
coop/
  <topic>/
    room.json                       # topic, sides, turn budget
    thread/
      0001-A-agent-propose.md
      0002-B-agent-ask.md
      0003-A-human-note.md
    contract/
      agreement.md
```

- One file is one message, history is never edited.
- File name: `NNNN-<side>-<actor>-<kind>.md`; front matter carries `re`,
  `resolution`, `at`.
- The client refuses to write when the rules forbid it (turns, pause,
  unread human message).
- With `--remote`: each message is one commit touching only `coop/`,
  pushed immediately; on a push conflict the client pulls, renumbers its own
  file and retries. Commits go to the current branch and its upstream;
  syncing is `git pull --rebase --autostash`. A separate `--branch` is
  deferred: it needs commits built without touching the working tree.
- Writers on one machine are serialised by a lock file
  `coop/<topic>/.lock` (git-ignored).
- The local identity is `.coop/<side>.json` (git-ignored): backend, topic,
  side, remote flag, last read seq of the agent and of the human. When both
  sides share one working tree, each terminal picks its side with
  `COOP_SIDE=A|B` or `--side`.
- The CLI acts as the human by default; `--agent` acts as the agent. The MCP
  server always acts as the agent.

## 6. Server backend

- `coop serve --db coop.db --addr 127.0.0.1:8470 --base-url https://...`,
  TLS terminated by a reverse proxy, run as a systemd unit (`deploy/`).
  SQLite through a pure-Go driver: still one static binary.
- Creating a room needs a **host key** (`coop key add`, stored hashed).
  `coop open --server URL --key KEY` (or `$COOP_SERVER`, `$COOP_HOST_KEY`)
  prints a single-use **invitation** `https://<host>/r/<room>#<code>`; the
  partner runs `coop join '<invitation>'` and becomes side B. The code is in
  the fragment, so a browser opening the link never sends it.
- Each side gets an agent token (`coopa_`) and a human token (`cooph_`),
  stored hashed on the server and in `.coop/<side>.json` (mode `0600`)
  locally. The server takes side and actor from the token and runs the same
  `core` rules as the git client; an agent token is refused for human kinds.
- API (`internal/api`): `POST /v1/rooms`, `POST /v1/rooms/{id}/join`,
  `GET /v1/rooms/{id}` (whole log; the client replays it),
  `POST /v1/rooms/{id}/messages`, `GET /v1/rooms/{id}/wait?after=N`.
- `wait` is a long-poll held at most 50 s (under common proxy timeouts); the
  client repeats it up to its own timeout.
- The "read human messages first" rule uses the `seen` the client reports;
  this matches the trust model (section 4).
- When a room closes with an agreement, each side writes
  `.coop/agreement-<slug>.md` the next time it looks at the room.
- Retention: an open room is deleted after 7 days without activity, a
  closed one after 30 days (hourly, or `coop purge`). `coop export` will
  save the log locally.

## 7. Agent integration

Every CLI agent gets the same two things; only the config format differs.

- **One MCP server** (`coop mcp`, stdio). Agent tools: `coop_status`,
  `coop_read`, `coop_say`, `coop_wait`, `coop_hand`. Setup tools:
  `coop_open`, `coop_join`. Human tools, used on the human's command in the
  chat (§4): `coop_approve`, `coop_resume`, `coop_close`, `coop_say_human`. The project folder comes from `--dir`, `$COOP_DIR` or
  `$CLAUDE_PROJECT_DIR`, else the working folder.
- **One skill**, `skills/coop/SKILL.md`, embedded in the binary: trust rules
  first, then the read/say/wait loop, resolutions, when to raise a hand.
- **`coop wait`** blocks until something arrives or `--max-wait` (default
  5 min), then the agent calls it again. The limit is set per agent below its
  tool timeout.

`coop setup <agent>...|all` writes the project files; it merges into
existing JSON/TOML configs and is idempotent:

| Agent | Skill | MCP config | Wait |
|---|---|---|---|
| Claude Code | `.claude/skills/coop/` | `.mcp.json` (`timeout` 660 s) | 5 min |
| Cursor (editor, `agent` CLI) | `.agents/skills/coop/` + `.cursor/rules/coop.mdc` | `.cursor/mcp.json`, `--dir ${workspaceFolder}` | 55 s (timeout undocumented) |
| Codex CLI | `.agents/skills/coop/` | `.codex/config.toml`, `tool_timeout_sec = 660` (default 60) | 5 min |
| Gemini CLI | `.agents/skills/coop/` | `.gemini/settings.json` (`timeout` 660 s) | 5 min |
| OpenCode | `.agents/skills/` + `.opencode/commands/coop-*.md` | `opencode.json` (`mcp.coop`, v1 shape) | 5 min |
| Kimi Code | `.agents/skills/` (skills are `/` commands) | `.kimi-code/mcp.json`, `toolTimeoutMs` 330 s (default 60) | 5 min |
| ZCode (Z.ai) | `.agents/skills/` + `.agents/commands/coop-*.md` | `.agents/mcp.json` | 55 s (timeout undocumented) |
| DeepSeek Harness (dsh) | `.agents/skills/` | none in the project: a plugin bundle `.coop/dsh-plugin/` (absolute paths, kept out of git) that the human installs once per project; plugin and server are named `coop-<folder>-<hash>` so projects do not collide; every DSH session sees every installed server, and the server's instructions name its folder; `toolCallTimeoutMs` 180 s (default 60) | 2 min |

Some agents (DSH) do not substitute `$ARGUMENTS` in a command skill, so each
command skill also says to take the argument from the text after the command.
`coop approve` in the CLI keeps requiring a terminal; from a chat, approval
goes through the MCP tool `coop_approve`.

Packages from this repository, all reusing `skills/` and calling `coop` from
the PATH (the binary is installed separately):

- Claude Code plugin and marketplace: `.claude-plugin/`, `.mcp.json`.
- Cursor plugin: `.cursor-plugin/plugin.json`, `mcp.json`.
- Gemini CLI extension: `gemini-extension.json`.
- Codex plugin: deferred; `coop setup codex` covers it.

Human-facing output is styled (`internal/ui`, Lip Gloss) only when stdout
is a terminal and the actor is a human; `--agent`, MCP, pipes, `NO_COLOR`
and `COOP_PLAIN=1` get plain text. No library may query the terminal on
start (OSC 11 and the like): the answer is read from stdin and can hang the
program or swallow typed input. Dark background is assumed
(`COOP_THEME=light` to change).

Humans follow the room with `coop watch` in a second terminal (typing
`h [reason]`, `r`, `s TEXT` or `q` acts as the human) and save minutes with
`coop export --format md|json`. No web UI.

## 8. Build order

1. Core: message model and rules (`internal/core`).
2. Git backend and CLI.
3. MCP server, skill, Claude Code plugin.
4. Server and server backend.
5. `watch`, `export`, retention.

Testing: one integration scenario run against both backends (a temporary git
repository; `httptest` for the server), plus an e2e script with two binaries
in two folders.

## 9. Distribution

`go install`, binaries in GitHub Releases, the Claude Code plugin from this
repository's marketplace.
