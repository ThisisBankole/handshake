#!/usr/bin/env python3
"""
Handshake Stop hook for Codex.

Fires when the agent finishes a turn and goes idle — equivalent of
OpenCode's session.idle. Syncs the session and regenerates the brief
after every completed response so it's always fresh.

Codex passes JSON on stdin:
{
  "session_id": "019cfd76-...",
  "transcript_path": "...",
  "cwd": "...",
  "hook_event_name": "Stop",
  "last_assistant_message": "..."
}

Output: exit 0 always — never block the agent.
Note: Stop hooks expect JSON on stdout when exiting 0.
      Return empty JSON to satisfy that requirement.
"""

import json
import sys
import urllib.request
import urllib.error

BASE_URL = "http://localhost:8765"


def mcp_checkpoint(session_id: str) -> None:
    """Call Handshake MCP checkpoint_session to sync latest messages."""

    # Initialise MCP session
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

    # Send initialized notification
    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({"jsonrpc": "2.0",
                         "method": "notifications/initialized"}).encode(),
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 "mcp-session-id": sid}
    ), timeout=5)

    # Checkpoint — reads full JSONL transcript via CodexAdapter
    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({
            "jsonrpc": "2.0", "id": 2, "method": "tools/call",
            "params": {
                "name": "checkpoint_session",
                "arguments": {
                    "session_id": session_id,
                    "agent": "codex",
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
        # Stop hooks expect JSON output — return empty object
        print("{}")
        sys.exit(0)

    session_id = data.get("session_id", "")

    if session_id:
        try:
            mcp_checkpoint(session_id)
        except (urllib.error.URLError, OSError):
            # Handshake daemon not running — never block the agent
            pass

    # Stop hooks must output JSON on stdout
    # Omitting "decision" means allow the stop — agent exits normally
    print("{}")
    sys.exit(0)


if __name__ == "__main__":
    main()