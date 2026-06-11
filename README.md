# Handshake

**Carry your AI coding sessions between agents.**

Handshake is a local-first session portability daemon for AI coding agents.
Checkpoint a session in Claude Code, OpenCode, or Hermes, and restore it in
any of the others — with full context: the goal, decisions made, current
state, and next steps. No cloud, no accounts, no data leaving your machine.

```
# In Claude Code, hitting a token limit:
"checkpoint this session"

# Later, in OpenCode or Hermes:
"restore my council spending session"
# → full handoff brief injected; the agent continues where you left off
```

## Why

AI coding agents are siloed. Each stores sessions in its own format, in its
own location. When you hit a token limit mid-task, or want to switch to a
cheaper local model, all the context the agent built up — decisions, files
read, approaches tried — is stranded. Handshake normalises sessions from
each agent's native storage into one local database and generates structured
handoff briefs when you switch.

## Install

Requires Go 1.25+.

```bash
git clone https://github.com/ThisisBankole/handshake.git
cd handshake
go build -o handshake ./cmd/handshake

./handshake init    # one-time setup
./handshake serve   # start the daemon (leave running)
```

`init` does everything:

- creates the canonical database at `~/.handshake/sessions.db`
- installs the OpenCode sync plugin
- auto-registers Handshake as an MCP server with Claude Code, OpenCode,
  and Hermes (config backups saved; prints manual snippets if an agent
  isn't installed)

## Usage

Once the daemon is running, your agents have four new tools — you use them
in plain English:

| Say to your agent | Tool called | What happens |
|---|---|---|
| "checkpoint this session" | `checkpoint_session` | Reads the full conversation from the agent's native storage into Handshake. The agent also writes a current-state/next-steps summary. |
| "list my sessions" | `list_sessions` | Recent sessions by title with relative timestamps. No IDs to remember. |
| "restore my auth refactor session" | `restore_session` | Fuzzy-matches the title, returns a handoff brief, and the agent continues the work. |
| "preview the brief for X" | `generate_brief` | Shows the brief without restoring. |

There's also a CLI: `handshake list` and `handshake restore <title>`.

### The handoff brief

A restored session arrives as structured markdown:

- **Original goal** — the first user message
- **Current state & next steps** — written by the source agent at
  checkpoint time (or the last substantive assistant message as fallback)
- **Recent conversation** — excerpt scaled to session length, tool noise
  collapsed to `[N tool interactions omitted]`
- **Instructions** — working directory, "decisions are settled, don't
  relitigate"

## How it reads each agent

| Agent | Method |
|---|---|
| Claude Code | Parses session JSONL from `~/.claude/projects/` |
| OpenCode | Bundled plugin streams `session.updated` events in real time; direct SQLite read as fallback |
| Hermes | WAL-safe read of `~/.hermes/state.db` |

Everything lands in one SQLite database with a canonical schema
(sessions / messages / briefs). Handshake does no LLM summarisation
itself — briefs carry real conversation content, and state summaries are
authored by the agent that has the context.

## Status

v0.1.0 — core daemon, all three agent integrations, and the handoff
engine are implemented and tested end-to-end. Not yet done: packaged
releases, auto-start service (`install-service`), Homebrew formula.

Mac and Linux. Local use only — the server binds to localhost and has no
authentication.
