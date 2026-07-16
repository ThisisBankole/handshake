#!/usr/bin/env python3
"""
Handshake Stop hook for Claude Code.

Fires when the agent finishes a turn and goes idle.
Syncs the session after every completed response. When project knowledge is
stale, it continues Claude once with an instruction to author the bounded OKF
documents using the installed knowledge-authoring skill.

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
    ), timeout=25)


def knowledge_authoring_instruction(working_dir: str) -> str:
    """Return one bounded continuation instruction when knowledge is stale."""
    if not working_dir:
        return ""
    payload = json.dumps({"working_dir": working_dir}).encode()
    request = urllib.request.Request(
        f"{BASE_URL}/knowledge-authoring-check",
        data=payload,
        headers={"Content-Type": "application/json"},
    )
    response = urllib.request.urlopen(request, timeout=5)
    state = json.loads(response.read().decode())
    if not state.get("pending"):
        return ""
    return (
        "Handshake project knowledge is stale after this checkpoint. "
        "Before ending this turn, use the installed knowledge-authoring skill "
        "to refresh project-brief and repo-map for project "
        f"{state.get('project_id', 'this project')} at factual revision "
        f"{state.get('facts_revision', 0)}. Read the factual OKF bundle first, "
        "then publish both documents through publish_project_knowledge. "
        "If the facts changed while authoring, fetch context again instead of "
        "publishing stale output."
    )


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print("{}")
        sys.exit(0)

    session_id = data.get("session_id", "")
    working_dir = data.get("cwd", "")

    if session_id:
        try:
            mcp_checkpoint(session_id)
        except (urllib.error.URLError, OSError):
            pass

    # A Stop hook can continue Claude with feedback. Only do this once: Claude
    # includes stop_hook_active on the continuation, which prevents a loop if
    # the model cannot or chooses not to refresh the knowledge in that turn.
    if not data.get("stop_hook_active", False):
        try:
            instruction = knowledge_authoring_instruction(working_dir)
            if instruction:
                print(json.dumps({"decision": "block", "reason": instruction}))
                sys.exit(0)
        except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
            pass

    # Stop hooks must output JSON — omitting "decision" lets agent exit normally.
    print("{}")
    sys.exit(0)


if __name__ == "__main__":
    main()
