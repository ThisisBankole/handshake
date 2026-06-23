
#
# Handshake plugin for Hermes.
# Installed by `handshake init` into ~/.hermes/hooks/handshake/
#
# What it does:
#   post_llm_call      → reads the session from ~/.hermes/state.db,
#                        sends messages to Handshake /ingest,
#                        then calls /generate-brief to keep the brief fresh
#
#   on_session_finalize → same as post_llm_call but fires on session teardown
#                        (compaction, /exit, gateway reset) — ensures the final
#                        state is always captured before the session closes

import json
import os
import sqlite3
import urllib.request
import urllib.error
import time
import logging

logger = logging.getLogger(__name__)

BASE_URL = os.environ.get("HANDSHAKE_URL", "http://localhost:8765")

# How long to wait between syncs for the same session.
# post_llm_call fires on every turn — debounce to avoid hammering Handshake.
_last_sync: dict[str, float] = {}
SYNC_INTERVAL_SECONDS = 5


def _hermes_db_path() -> str:
    """Find ~/.hermes/state.db regardless of HOME."""
    home = os.path.expanduser("~")
    return os.path.join(home, ".hermes", "state.db")


def _read_session(session_id: str) -> dict | None:
    """
    Read session metadata and messages from ~/.hermes/state.db.

    Hermes uses WAL mode so reads are safe while Hermes is writing.
    We open read-only to avoid any risk of corrupting the live DB.
    """
    db_path = _hermes_db_path()
    if not os.path.exists(db_path):
        return None

    try:
        # file:path?mode=ro opens in read-only mode — safe for concurrent reads
        conn = sqlite3.connect(
            f"file:{db_path}?mode=ro",
            uri=True,
            timeout=5,
            check_same_thread=False,
        )
        conn.row_factory = sqlite3.Row

        # Read session metadata
        row = conn.execute(
            """
            SELECT id, title, model, cwd, started_at, ended_at
            FROM sessions WHERE id = ?
            """,
            (session_id,),
        ).fetchone()

        if not row:
            conn.close()
            return None

        session = {
            "id": row["id"],
            "title": row["title"] or "",
            "model": row["model"] or "",
            "working_dir": row["cwd"] or "",
            "created_at": int(row["started_at"] or 0),
            "updated_at": int(row["ended_at"] or row["started_at"] or 0),
        }

        # Read messages — active=1 filters out soft-deleted messages
        msg_rows = conn.execute(
            """
            SELECT id, role, COALESCE(content, '') as content,
                   COALESCE(tool_name, '') as tool_name, timestamp
            FROM messages
            WHERE session_id = ? AND active = 1
            ORDER BY id ASC
            """,
            (session_id,),
        ).fetchall()

        conn.close()

        messages = []
        for m in msg_rows:
            content = (m["content"] or "").strip()
            if not content:
                continue
            # Prefix tool calls with their name so the brief shows what ran
            if m["role"] == "tool" and m["tool_name"]:
                content = f"[tool: {m['tool_name']}]\n{content}"
            messages.append({
                "id": f"hermes:{session_id}:{m['id']}",
                "role": m["role"],
                "content": content,
                "created_at": int(m["timestamp"] or 0),
            })

        session["messages"] = messages
        return session

    except Exception as e:
        logger.debug("Handshake: could not read Hermes session %s: %s", session_id, e)
        return None


def _ingest(session: dict) -> bool:
    """Send session data to Handshake /ingest endpoint."""
    try:
        payload = json.dumps({
            "agent": "hermes",
            "session": {
                "id": session["id"],
                "title": session["title"],
                "working_dir": session["working_dir"],
                "model": session["model"],
                "created_at": session["created_at"],
                "updated_at": session["updated_at"],
            },
            "messages": session["messages"],
        }).encode()

        req = urllib.request.Request(
            f"{BASE_URL}/ingest",
            data=payload,
            headers={"Content-Type": "application/json"},
        )
        urllib.request.urlopen(req, timeout=5)
        return True

    except (urllib.error.URLError, OSError):
        # Handshake daemon not running — silently skip
        return False


def _generate_brief(session_id: str) -> None:
    """Ask Handshake to regenerate the brief for this session."""
    try:
        payload = json.dumps({"session_id": session_id}).encode()
        req = urllib.request.Request(
            f"{BASE_URL}/generate-brief",
            data=payload,
            headers={"Content-Type": "application/json"},
        )
        urllib.request.urlopen(req, timeout=25)
    except (urllib.error.URLError, OSError):
        pass


def _sync(session_id: str, force: bool = False) -> None:
    """
    Core sync function — reads session from Hermes DB and pushes to Handshake.

    force=True bypasses the debounce (used on session finalize so the
    final state is always captured regardless of timing).
    """
    now = time.time()
    last = _last_sync.get(session_id, 0)

    # Debounce — skip if synced recently and not forced
    if not force and (now - last) < SYNC_INTERVAL_SECONDS:
        return

    _last_sync[session_id] = now

    session = _read_session(session_id)
    if not session:
        return

    if _ingest(session):
        _generate_brief(session_id)


# --- Hook callbacks ---
# Hermes calls these directly with keyword arguments.
# Always accept **kwargs for forward compatibility — Hermes may add
# new parameters in future versions.

def post_llm_call(session_id: str, **kwargs) -> None:
    """
    Fires once per turn after the tool-calling loop completes.
    The agent has finished responding and is waiting for user input.

    We sync here to keep the brief fresh after every response.
    Debounced to SYNC_INTERVAL_SECONDS to avoid hammering Handshake
    during rapid multi-turn exchanges.
    """
    _sync(session_id, force=False)


def on_session_finalize(session_id: str, **kwargs) -> None:
    """
    Fires when Hermes tears down an active session:
    - User runs /exit
    - Context compression completes
    - Gateway session resets (/new, /reset)
    - CLI session ends

    We force-sync here regardless of debounce to ensure the final
    state is always captured before the session closes.
    """
    _sync(session_id, force=True)


def register(ctx) -> None:
    """
    Entry point Hermes calls at startup to register hook callbacks.
    ctx.register_hook(event_name, callback_function)
    """
    ctx.register_hook("post_llm_call", post_llm_call)
    ctx.register_hook("on_session_finalize", on_session_finalize)