#!/usr/bin/env python3
"""
Handshake PreCompact hook for Codex.

Fires before Codex runs a compact operation (manual or auto).
Checkpoints the session into Handshake before context is lost.

Codex passes JSON on stdin:
{
  "session_id": "019cfd76-...",
  "transcript_path": "/Users/.../.codex/sessions/.../rollout-....jsonl",
  "cwd": "/Users/m/docs/my-project",
  "hook_event_name": "PreCompact",
  "trigger": "auto"
}

Output: exit 0 always — never block compaction.
"""

import json
import sys
import urllib.request
import urllib.error

BASE_URL = "http://localhost:8765"


def mcp_checkpoint(session_id: str, trigger: str) -> None:
    """Call Handshake MCP checkpoint_session tool."""

    # Step 1 — initialise MCP session
    init = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "handshake-codex-hook", "version": "1"}
        }
    }).encode()

    req = urllib.request.Request(
        f"{BASE_URL}/mcp", data=init,
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream"}
    )
    resp = urllib.request.urlopen(req, timeout=5)
    sid = resp.headers.get("mcp-session-id", "")
    if not sid:
        return

    # Step 2 — send initialized notification
    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({"jsonrpc": "2.0",
                         "method": "notifications/initialized"}).encode(),
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 "mcp-session-id": sid}
    ), timeout=5)

    # Step 3 — checkpoint the session
    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({
            "jsonrpc": "2.0", "id": 2, "method": "tools/call",
            "params": {
                "name": "checkpoint_session",
                "arguments": {
                    "session_id": session_id,
                    "agent": "codex",
                    "summary": f"Auto-checkpointed before {trigger} compaction"
                }
            }
        }).encode(),
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 "mcp-session-id": sid}
    ), timeout=25)


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        sys.exit(0)

    session_id = data.get("session_id", "")
    trigger = data.get("trigger", "unknown")

    if not session_id:
        sys.exit(0)

    try:
        mcp_checkpoint(session_id, trigger)
    except (urllib.error.URLError, OSError):
        # Handshake daemon not running — never block compaction
        pass

    sys.exit(0)


if __name__ == "__main__":
    main()