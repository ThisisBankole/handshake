# Handshake



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
handshake handoff "my session title"
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

## Configuration

Handshake defaults to `localhost:8765`. If that port is already in use,
`handshake setup` detects the conflict automatically and picks the next
free port — no manual action needed. The setup wizard will tell you which
port was chosen and register all agents with the correct URL.

**To pin a specific port** (optional), set these before running `handshake setup`:

| Variable | Purpose |
|---|---|
| `HANDSHAKE_ADDR` | Address the daemon binds to (e.g. `localhost:8766`) |
| `HANDSHAKE_URL` | MCP endpoint registered with agents (e.g. `http://localhost:8766/mcp`) |

```bash
export HANDSHAKE_ADDR=localhost:8766
export HANDSHAKE_URL=http://localhost:8766/mcp
handshake setup
```

**Changing the port on an existing service install (macOS):**
Add an `EnvironmentVariables` block to the launchd plist at
`~/Library/LaunchAgents/com.handshake.daemon.plist`, then re-run `handshake setup`
to update agent registrations:

```xml
<key>EnvironmentVariables</key>
<dict>
  <key>HANDSHAKE_ADDR</key><string>localhost:8766</string>
  <key>HANDSHAKE_URL</key><string>http://localhost:8766/mcp</string>
</dict>
```

Then reload:
```bash
launchctl unload ~/Library/LaunchAgents/com.handshake.daemon.plist
launchctl load  ~/Library/LaunchAgents/com.handshake.daemon.plist
```

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
