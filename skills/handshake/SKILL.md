---
name: handshake
description: Use Handshake to list, checkpoint, restore, search, and hand off coding-agent sessions, and to work with Handshake project knowledge or configuration.
metadata:
  handshake-managed: "true"
---

# Handshake

When a user asks about Handshake sessions, handoffs, restoration, project
knowledge, or Handshake configuration, first call `get_handshake_update_status`.
It reads locally cached status only and never contacts the network.

If a newer version is available, tell the user in one short sentence before
continuing their requested work. Do not interrupt work when no update is
available, do not ask the user to update, and do not attempt to install one.

Use Handshake MCP tools for session work. Preserve the user's project and Git
state unless they explicitly request a change.

## List sessions

When the user asks to list, show, find, or browse sessions, call
`list_sessions`. Use its structured content; do not parse the text fallback
when structured content is available.

Present sessions in the order returned by the tool:

| Session | Agent | Updated | Directory | Project | ID |
|---|---|---|---|---|---|

Apply these rules:

- Use `title` for Session and `updated_relative` for Updated.
- Display agent names as Claude Code, Codex, OpenCode, Hermes, or Cursor.
- Shorten the user's home directory to `~` in Directory.
- Use `project_name` for Project.
- Use `—` for unavailable values; never invent one.
- Always include the session ID.
- Put synchronization warnings below the list, not inside it.

If long values make the table hard to read, use a numbered layout with the
same fields instead. When no sessions exist, say `No sessions found.` and then
show any synchronization warnings.
