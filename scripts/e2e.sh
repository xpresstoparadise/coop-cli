#!/usr/bin/env bash
# End-to-end check: a real server, two project folders (two owners), one
# side driven through the CLI and the other through the MCP server.
set -euo pipefail

BIN=${BIN:-$(pwd)/bin/coop}
WORK=$(mktemp -d)
trap 'kill $SRV 2>/dev/null || true; rm -rf "$WORK"' EXIT
PORT=${PORT:-18470}
URL=http://127.0.0.1:$PORT

"$BIN" serve --db "$WORK/coop.db" --addr 127.0.0.1:$PORT --base-url "$URL" 2>"$WORK/server.log" &
SRV=$!
for _ in $(seq 50); do curl -fs "$URL/healthz" >/dev/null && break; sleep 0.1; done

KEY=$("$BIN" key add --name e2e --db "$WORK/coop.db" | tail -1)
mkdir -p "$WORK/alice" "$WORK/bob"

cd "$WORK/alice"
INV=$("$BIN" open --topic "Orders export" --server "$URL" --key "$KEY" | sed -n "s/^they run: coop join '\(.*\)'$/\1/p")
echo "invitation: $INV"

cd "$WORK/bob"
"$BIN" join "$INV"

# Bob's agent waits through MCP while Alice's agent proposes.
mcp() {
  printf '%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"e2e"}}}' \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
    "$1"
  sleep "${2:-1}"
}
(cd "$WORK/bob" && mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"coop_wait","arguments":{"timeout_seconds":20}}}' 4 \
  | "$BIN" mcp > "$WORK/bob-wait.jsonl") &
WAITER=$!
sleep 1
(cd "$WORK/alice" && "$BIN" say --agent --kind propose --resolution -- "Orders via GET /orders?cursor=..., 100 per page")
wait $WAITER
grep -q 'Orders via GET /orders' "$WORK/bob-wait.jsonl" || { echo "MCP wait did not deliver"; cat "$WORK/bob-wait.jsonl"; exit 1; }
echo "MCP wait delivered the proposal"

(cd "$WORK/bob" && mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"coop_say","arguments":{"kind":"accept","re":1,"body":"agreed"}}}' \
  | "$BIN" mcp | grep -q 'posted #2 accept') || { echo "MCP accept failed"; exit 1; }

# Approval needs a terminal: script(1) provides one.
for who in alice bob; do
  (cd "$WORK/$who" && { sleep 0.5; echo yes; sleep 2; } | script -qec "$BIN approve" /dev/null)
done
# Each side saves the agreement the next time it looks at the closed room.
for who in alice bob; do
  (cd "$WORK/$who" && "$BIN" status | grep -q "closed: approved")
  grep -q "100 per page" "$WORK/$who/.coop/agreement-orders-export.md"
done
echo "e2e: ok"
