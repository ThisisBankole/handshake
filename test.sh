#!/bin/bash
# CLI smoke test: build, init, ingest a fixture via the db layer, list, restore.
# The handshake binary runs with a temporary HOME so the real ~/.handshake is
# untouched; go itself keeps the real HOME (toolchain/module caches).

set -euo pipefail
cd "$(dirname "$0")"

echo "=== Handshake CLI smoke test ==="

go build -o handshake ./cmd/handshake
echo "✓ build"

TESTHOME="$(mktemp -d)"
FIXTURE_DIR=".smoke-fixture"
trap 'rm -rf "$TESTHOME" "$FIXTURE_DIR"' EXIT

HOME="$TESTHOME" ./handshake init > /dev/null
test -f "$TESTHOME/.handshake/sessions.db"
test -f "$TESTHOME/.config/opencode/plugins/handshake.js"
echo "✓ init (db + opencode plugin created)"

mkdir -p "$FIXTURE_DIR"
cat > "$FIXTURE_DIR/main.go" <<'EOF'
package main

import (
	"os"

	"handshake/internal/adapters"
	"handshake/internal/db"
)

func main() {
	database, err := db.New(os.Getenv("HANDSHAKE_TEST_DB"))
	if err != nil {
		panic(err)
	}
	defer database.Close()

	session := &adapters.SessionData{
		ID: "smoke-1", Agent: "claude-code", Title: "Smoke Test Session",
		WorkingDir: "/tmp/smoke", Model: "test-model",
		CreatedAt: 1700000000, UpdatedAt: 1700000100,
		Messages: []adapters.MessageData{
			{ID: "sm-1", Role: "user", Content: "fix the flaky login test", CreatedAt: 1700000000},
			{ID: "sm-2", Role: "assistant", Content: "Found the race in auth_test.go; added a wait.", CreatedAt: 1700000100},
		},
	}
	// Ingest twice to prove idempotency (upserts, not plain INSERTs).
	if err := adapters.Ingest(database, session); err != nil {
		panic(err)
	}
	if err := adapters.Ingest(database, session); err != nil {
		panic(err)
	}
}
EOF
HANDSHAKE_TEST_DB="$TESTHOME/.handshake/sessions.db" go run "./$FIXTURE_DIR"
echo "✓ ingest (idempotent)"

HOME="$TESTHOME" ./handshake list | grep -q "Smoke Test Session"
echo "✓ list"

HOME="$TESTHOME" ./handshake restore "Smoke Test Session" | grep -q "fix the flaky login test"
echo "✓ restore (brief contains goal)"

echo "=== CLI smoke test passed ==="
