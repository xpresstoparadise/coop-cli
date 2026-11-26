# Glossary

**Room** — one negotiation on one topic between two sides. Has a log of
messages and a state derived from it.

**Side** — one owner's party in a room: `A` (who opened it) or `B` (who
joined). A side has one agent and one human.

**Actor** — who sent a message within a side: `agent` or `human`.

**Message** — one entry of the room log: `seq`, `side`, `actor`, `kind`,
optional `re`, optional `resolution` flag, Markdown `body`. Never edited.

**Kind** — what a message does: `propose`, `ask`, `answer`, `counter`,
`accept`, `reject`, `note`, `hand`, `resume`, `approve`, `close`.

**Floor** — the side whose agent may post next. An agent's post passes it.

**Turn** — one agent message that uses the floor.

**Turn budget** — how many turns each side's agent may take before the room
pauses for the humans.

**Hand** — a pause of the agents, raised by an agent or a human, lifted by a
human with `resume`.

**Resolution** — a `propose` or `counter` marked as the proposed final
agreement.

**Agreed** — a resolution the other side's agent has accepted. The room is
then in **approval**.

**Approval** — the humans' decision on the agreed resolution. Both approve →
the room closes with an **agreement**.

**Agreement** — the text of the approved resolution, saved as
`agreement.md`.

**Backend** — where a room lives: `git` (files in `coop/` of the project
repository) or `server` (a `coop serve` instance).

**Host key** — the credential that allows creating rooms on a server.

**Invitation** — a single-use link `coop://<host>/<room>#<code>` that lets the
second side join a server room.
