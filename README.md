# Handshake

**Carry your AI coding sessions between agents.**

Handshake is a local-first session portability daemon for AI coding agents.
It runs silently in the background, keeping your sessions synced across
Claude Code, OpenCode, Hermes, and Codex. Hit a token limit and switch
agents, your context is already there.

## How it works

Handshake runs as a background daemon and hooks into each agent's native
event system. Every time an agent finishes a response, Handshake syncs the
session automatically. You never have to think about it.

When you want to switch agents, just say:

```
restore my council spending session
```

The new agent gets a full handoff brief; the original goal, decisions made,
current state, files modified, and next steps.

## Install

**Mac and Linux (curl):**
```bash
curl -fsSL https://raw.githubusercontent.com/ThisisBankole/handshake/main/install.sh | sh
```


**Mac (Homebrew):**
```bash
brew install ThisisBankole/tools/handshake
```


**From source (requires Go 1.25+):**
```bash
git clone https://github.com/ThisisBankole/handshake.git
cd handshake
go build -o handshake ./cmd/handshake
```

After installing, run the setup wizard:
```bash
handshake setup
```

Takes about 30 seconds. Handshake registers with your agents, installs
itself as a login service, and starts the daemon. After that it runs
invisibly in the background.

## Switching agents

When you want to continue work in a different agent:

```
list my sessions
```

```
restore my auth refactor session
```

That's it. The receiving agent picks up with full context.

There's also a CLI:
```bash
handshake list
handshake restore "my session title"
```

## Manual checkpoint

If you want to save a specific moment before switching agents, ask your agent:

```
checkpoint this session
```

## Supported agents

- Claude Code
- OpenCode
- Hermes
- Codex

## Uninstall

```bash
handshake uninstall
```

Removes Handshake from all agent configs, stops the daemon, and optionally
deletes the session database and binary.

## Privacy

Handshake runs entirely on your machine. The daemon binds to `localhost:8765`
and is not accessible from the network. No telemetry, no cloud sync, no
accounts. Your sessions are stored locally at `~/.handshake/sessions.db`.