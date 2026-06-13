#!/usr/bin/env python3
"""
Handshake PostCompact hook for Codex.

Fires after Codex completes a compact operation.
Captures the compact_summary Codex generated and stores it as the
session's current state in Handshake, then regenerates the brief.

Codex passes JSON on stdin:
{
  "session_id": "019cfd76-...",
  "transcript_path": "...",
  "cwd": "...",
  "hook_event_name": "PostCompact",
  "trigger": "auto",
  "compact_summary": "Summary of the compacted conversation..."
}

Output: none — PostCompact has no decision control.
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
        # 1. Store compaction summary as session current state.
        # This becomes "Current State & Next Steps" in the handoff brief.
        urllib.request.urlopen(urllib.request.Request(
            f"{BASE_URL}/ingest",
            data=json.dumps({
                "agent": "codex",
                "session": {
                    "id": session_id,
                    "summary": compact_summary,
                },
                "messages": []
            }).encode(),
            headers={"Content-Type": "application/json"}
        ), timeout=5)

        # 2. Regenerate brief immediately with the fresh summary.
        urllib.request.urlopen(urllib.request.Request(
            f"{BASE_URL}/generate-brief",
            data=json.dumps({"session_id": session_id}).encode(),
            headers={"Content-Type": "application/json"}
        ), timeout=10)

    except (urllib.error.URLError, OSError):
        # Handshake daemon not running — silently skip
        pass

    sys.exit(0)


if __name__ == "__main__":
    main()