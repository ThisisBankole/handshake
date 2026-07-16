---
name: handshake
description: Use Handshake to checkpoint, restore, search, and hand off coding-agent sessions.
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
