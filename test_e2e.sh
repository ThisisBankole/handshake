#!/bin/bash
# End-to-end server test: serve, /health, /ingest, MCP initialize,
# tools/list, checkpoint (metadata fallback), restore with fuzzy title.
# Uses a temporary HOME so the real ~/.handshake is untouched.

set -euo pipefail
cd "$(dirname "$0")"

BASE="http://localhost:8765"

echo "=== Handshake server E2E test ==="

go build -o handshake ./cmd/handshake

TESTHOME="$(mktemp -d)"
HOME="$TESTHOME" ./handshake init > /dev/null
HOME="$TESTHOME" ./handshake serve > /dev/null &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null; rm -rf "$TESTHOME"' EXIT

for i in $(seq 1 20); do
  curl -sf "$BASE/health" > /dev/null && break
  sleep 0.25
done
curl -sf "$BASE/health" > /dev/null
# Guard against a stale server on the port: ours must still be alive,
# otherwise the health check above answered from a leftover process.
kill -0 $SERVER_PID 2>/dev/null || { echo "✗ port 8765 already in use by another handshake process"; exit 1; }
echo "✓ serve + /health"

curl -sf -X POST "$BASE/ingest" -H 'content-type: application/json' -d '{
  "agent": "opencode",
  "session": {"id":"e2e-1","title":"E2E council spending","working_dir":"/tmp/e2e","model":"m","created_at":1700000000,"updated_at":1700000100},
  "messages": [
    {"id":"e2e-m1","role":"user","content":"analyse the council spending data","created_at":1700000000},
    {"id":"e2e-m2","role":"assistant","content":"Loaded the CSVs and built summary tables.","created_at":1700000100}
  ]
}' | grep -q '"ok":true'
echo "✓ /ingest"

SID=$(curl -si -X POST "$BASE/mcp" \
  -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}' \
  | grep -i '^mcp-session-id:' | cut -d' ' -f2 | tr -d '\r')
test -n "$SID"
echo "✓ MCP initialize"

mcpcall() {
  curl -s -X POST "$BASE/mcp" \
    -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -H "mcp-session-id: $SID" -d "$1"
}
mcpcall '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null

TOOLS=$(mcpcall '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}')
for tool in checkpoint_session list_sessions restore_session generate_brief; do
  echo "$TOOLS" | grep -q "\"$tool\""
done
echo "✓ tools/list exposes all four tools"

mcpcall '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}' \
  | grep -q "E2E council spending"
echo "✓ list_sessions"

# Fuzzy restore: partial, differently-cased title.
mcpcall '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"restore_session","arguments":{"title":"council SPENDING"}}}' \
  | grep -q "analyse the council spending data"
echo "✓ restore_session (fuzzy title, brief carries goal)"

# Checkpoint with unreadable native storage falls back to metadata-only,
# and an agent-written summary becomes the brief's current-state section.
mcpcall '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"checkpoint_session","arguments":{"session_id":"nonexistent-xyz","agent":"hermes","title":"fallback test","summary":"Done: parser built. Next: wire up the importer."}}}' \
  | grep -q "metadata only"
echo "✓ checkpoint_session (metadata fallback)"

# Note: Go's JSON encoder escapes the ampersand as \u0026, so do not grep across it.
BRIEF=$(mcpcall '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"generate_brief","arguments":{"title":"fallback test"}}}')
echo "$BRIEF" | grep -q "Current State"
echo "$BRIEF" | grep -q "wire up the importer"
echo "✓ generate_brief (agent-written summary in brief)"

echo "=== Server E2E test passed ==="
