#!/usr/bin/env python3
"""
Handshake PostCompact hook for Claude Code.

Fires after Claude Code completes a compact operation (manual or auto).
Captures the compact_summary Claude Code generated and stores it as the
session's current state in Handshake, then regenerates the brief.

Input (stdin): JSON with session_id, transcript_path, cwd, trigger, compact_summary
Output: none
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
        sys.exit(0)

    session_id = data.get("session_id", "")
    compact_summary = data.get("compact_summary", "")

    if not session_id:
        sys.exit(0)

    try:
        # 1. Store the compaction summary as the session's current state.
        # This becomes the "Current State & Next Steps" section of the brief.
        ingest_payload = json.dumps({
            "agent": "claude-code",
            "session": {
                "id": session_id,
                "summary": compact_summary,
            },
            "messages": []
        }).encode()

        ingest_req = urllib.request.Request(
            f"{BASE_URL}/ingest",
            data=ingest_payload,
            headers={"Content-Type": "application/json"}
        )
        urllib.request.urlopen(ingest_req, timeout=5)

        # 2. Regenerate the brief immediately with the fresh summary.
        brief_payload = json.dumps({
            "session_id": session_id
        }).encode()

        brief_req = urllib.request.Request(
            f"{BASE_URL}/generate-brief",
            data=brief_payload,
            headers={"Content-Type": "application/json"}
        )
        urllib.request.urlopen(brief_req, timeout=10)

    except (urllib.error.URLError, OSError):
        # Handshake daemon not running — silently skip
        pass

    sys.exit(0)


if __name__ == "__main__":
    main()