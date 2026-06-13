#!/usr/bin/env python3
"""
Handshake Stop hook for Claude Code.

Fires when the agent finishes a turn and goes idle.
Syncs the session and regenerates the brief after every
completed response so it's always fresh.

Input (stdin): JSON with session_id, transcript_path, cwd
Output: JSON on stdout (Stop hooks require JSON output)
"""

import json
import sys
import urllib.request
import urllib.error

BASE_URL = "http://localhost:8765"


def mcp_checkpoint(session_id: str) -> None:
    init = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "handshake-claudecode-hook", "version": "1"}
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

    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({"jsonrpc": "2.0",
                         "method": "notifications/initialized"}).encode(),
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 "mcp-session-id": sid}
    ), timeout=5)

    urllib.request.urlopen(urllib.request.Request(
        f"{BASE_URL}/mcp",
        data=json.dumps({
            "jsonrpc": "2.0", "id": 2, "method": "tools/call",
            "params": {
                "name": "checkpoint_session",
                "arguments": {
                    "session_id": session_id,
                    "agent": "claude-code",
                }
            }
        }).encode(),
        headers={"Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 "mcp-session-id": sid}
    ), timeout=10)


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print("{}")
        sys.exit(0)

    session_id = data.get("session_id", "")

    if session_id:
        try:
            mcp_checkpoint(session_id)
        except (urllib.error.URLError, OSError):
            pass

    # Stop hooks must output JSON — omitting "decision" lets agent exit normally
    print("{}")
    sys.exit(0)


if __name__ == "__main__":
    main()