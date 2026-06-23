#!/usr/bin/env python3
"""
Handshake PreCompact hook for Claude Code.

Fires before Claude Code runs a compact operation (manual or auto).
Checkpoints the current session into Handshake so it can be restored
from another agent after compaction.

Input (stdin): JSON with session_id, transcript_path, cwd, trigger
Output: none (exit 0 to allow compaction to proceed)
"""

import json
import sys
import urllib.request
import urllib.error

BASE_URL = "http://localhost:8765"


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        # Bad input — don't block compaction
        sys.exit(0)

    session_id = data.get("session_id", "")
    trigger = data.get("trigger", "unknown")

    if not session_id:
        sys.exit(0)

    try:
        # Checkpoint the session into Handshake before compaction runs.
        # This reads the full JSONL transcript and stores it in the canonical DB.
        payload = json.dumps({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "handshake-hook", "version": "1"}
            }
        }).encode()

        req = urllib.request.Request(
            f"{BASE_URL}/mcp",
            data=payload,
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream"
            }
        )
        resp = urllib.request.urlopen(req, timeout=5)

        # Extract the MCP session ID from the response headers
        mcp_session_id = resp.headers.get("mcp-session-id", "")

        if not mcp_session_id:
            sys.exit(0)

        # Send initialized notification
        notify_payload = json.dumps({
            "jsonrpc": "2.0",
            "method": "notifications/initialized"
        }).encode()

        notify_req = urllib.request.Request(
            f"{BASE_URL}/mcp",
            data=notify_payload,
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream",
                "mcp-session-id": mcp_session_id
            }
        )
        urllib.request.urlopen(notify_req, timeout=5)

        # Call checkpoint_session
        checkpoint_payload = json.dumps({
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/call",
            "params": {
                "name": "checkpoint_session",
                "arguments": {
                    "session_id": session_id,
                    "agent": "claude-code",
                    "summary": f"Auto-checkpointed before {trigger} compaction"
                }
            }
        }).encode()

        checkpoint_req = urllib.request.Request(
            f"{BASE_URL}/mcp",
            data=checkpoint_payload,
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream",
                "mcp-session-id": mcp_session_id
            }
        )
        urllib.request.urlopen(checkpoint_req, timeout=25)

    except (urllib.error.URLError, OSError):
        # Handshake daemon not running — don't block compaction
        pass

    # Always exit 0 — never block compaction
    sys.exit(0)


if __name__ == "__main__":
    main()