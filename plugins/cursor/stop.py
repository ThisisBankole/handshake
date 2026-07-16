#!/usr/bin/env python3
"""Handshake Stop hook for Cursor.

Cursor sends the completed conversation's ID, transcript path, and workspace
roots as JSON on stdin. The hook imports the local transcript into Handshake,
then returns one follow-up instruction when project knowledge is stale.
"""

import json
import sys
import time
import urllib.error
import urllib.request

BASE_URL = "http://localhost:8765"


def text_content(content):
    if isinstance(content, str):
        return content.strip()
    if not isinstance(content, list):
        return ""
    parts = []
    for block in content:
        if isinstance(block, dict) and block.get("type") == "text":
            text = block.get("text", "")
            if isinstance(text, str) and text.strip():
                parts.append(text.strip())
    return "\n".join(parts)


def read_transcript(path, session_id):
    messages = []
    title = ""
    now = int(time.time())
    try:
        with open(path, "r", encoding="utf-8") as transcript:
            for index, line in enumerate(transcript):
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    continue
                role = record.get("role", "")
                message = record.get("message", {})
                if role not in ("user", "assistant") or not isinstance(message, dict):
                    continue
                content = text_content(message.get("content"))
                if not content:
                    continue
                if role == "user" and not title:
                    title = content.replace("<user_query>", "").replace("</user_query>", "").strip()
                messages.append({
                    "id": f"cursor:{session_id}:{index}",
                    "role": role,
                    "content": content,
                    "created_at": now,
                })
    except (OSError, UnicodeError):
        return None
    if not messages:
        return None
    return {"title": title, "messages": messages, "created_at": now, "updated_at": now}


def post_json(path, payload, timeout):
    request = urllib.request.Request(
        f"{BASE_URL}{path}",
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.read()


def ingest_session(session_id, transcript_path, working_dir, model):
    transcript = read_transcript(transcript_path, session_id)
    if not transcript:
        return False
    post_json("/ingest", {
        "agent": "cursor",
        "session": {
            "id": session_id,
            "title": transcript["title"],
            "working_dir": working_dir,
            "model": model,
            "created_at": transcript["created_at"],
            "updated_at": transcript["updated_at"],
        },
        "messages": transcript["messages"],
    }, 25)
    return True


def knowledge_authoring_instruction(working_dir):
    if not working_dir:
        return ""
    response = post_json("/knowledge-authoring-check", {"working_dir": working_dir}, 5)
    state = json.loads(response.decode())
    if not state.get("pending"):
        return ""
    return (
        "Handshake project knowledge is stale after this checkpoint. Before ending this turn, "
        "call get_project_knowledge_context with the current workspace, read the factual OKF "
        "bundle it lists, then publish both project-brief and repo-map through "
        "publish_project_knowledge. Use the exact project_id and facts_revision returned by "
        "the context tool. If publication reports a stale revision, fetch the context again "
        "instead of publishing older facts."
    )


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print("{}")
        return

    # Cursor's stop hook only permits follow-up work after normal completion.
    # loop_count prevents another continuation if the model does not publish.
    if data.get("status") != "completed":
        print("{}")
        return

    session_id = data.get("conversation_id") or data.get("session_id", "")
    roots = data.get("workspace_roots", [])
    working_dir = roots[0] if isinstance(roots, list) and roots else ""
    transcript_path = data.get("transcript_path", "")

    if not session_id or not transcript_path:
        print("{}")
        return

    try:
        if not ingest_session(session_id, transcript_path, working_dir, data.get("model", "")):
            print("{}")
            return
        if int(data.get("loop_count", 0)) == 0:
            instruction = knowledge_authoring_instruction(working_dir)
            if instruction:
                print(json.dumps({"followup_message": instruction}))
                return
    except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
        pass

    print("{}")


if __name__ == "__main__":
    main()
