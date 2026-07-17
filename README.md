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

When installed through curl in a terminal, the guided setup starts
automatically. In a non-interactive environment, run it afterwards:
```bash
handshake setup
```

Takes about 30 seconds. Handshake registers with your agents, installs
itself as a login service, and starts the daemon. After that it runs
invisibly in the background.

## Updates

The daemon checks GitHub Releases in the background once a week and stores only
the release result locally. It sends no project or session data. A newer release
appears in the session browser, `handshake version`, and the
`get_handshake_update_status` MCP tool.

## Testing

Run the regular suite from the repository root:

```bash
go test ./...
```

On Apple silicon Macs with the Apple Container CLI, run the same suite in a
clean Linux environment:

```bash
# One-time machine setup
container system start

# Builds an isolated image and runs the tests without mounting your home directory.
sh scripts/test-container.sh
```

The container test installs Go and Git, copies only the repository source into
the image, and runs `go test ./...`. It does not mount your Handshake database
or agent configuration, so it is suitable for migration and release checks.

## Project Knowledge

Each Git-aware checkpoint also writes a private, deterministic OKF-compatible
bundle at `~/.handshake/knowledge/<project-id>/`. It contains checkpoint and
Git timelines plus size-limited, per-file text diffs. Session transcripts are
not exported. Sensitive and generated paths are omitted and recorded as such
in each diff index.

Agents can publish a `project-brief.md` and `repo-map.md` through the local
`publish_project_knowledge` MCP tool. Each document declares the factual
revision it used; Handshake rejects stale output and marks published documents
stale after later checkpoints.

Optionally enable a background writer to keep these two AI-authored documents
current without asking the interactive agent to do the work:

```bash
handshake knowledge author setup
handshake knowledge author show
```

Setup detects Claude Code, Codex, OpenCode, and Hermes, lets you choose one
fallback writer, and asks for final approval because its model runs may consume
quota. The daemon starts that CLI only after an active agent has had a short
chance to publish both documents itself. Before starting the CLI, Handshake
verifies that the documents are still stale for the latest factual revision. It
never trusts terminal output as proof of success. Disable background model runs
at any time with:

```bash
handshake knowledge author off
```

`handshake setup` and `handshake init` install Handshake's general and
`knowledge-authoring` skills into the global skill location for Claude Code,
Codex, OpenCode, and Hermes. Existing user-owned skills with the same name are
left untouched. The general skill asks agents to report a cached available
Handshake update before relevant work. Skill installation makes the workflow
available to an agent;
Claude Code also receives one Stop-hook continuation when a checkpoint leaves
these documents stale, directing it to use the skill before the turn ends.
OpenCode, Codex, and Hermes have the skill available as well. Background
authoring uses the corresponding non-interactive CLI command, so it works even
when that agent is not the one currently being used interactively.

Cursor receives Handshake's MCP server and a managed Stop hook. The hook imports
Cursor's local transcript after a completed turn. If project knowledge is stale,
it sends Cursor one follow-up instruction to publish the two documents through
MCP. Cursor is not yet available as an unattended background writer.

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
and is not accessible from the network. No cloud sync, no accounts. Your
sessions are stored locally at `~/.handshake/sessions.db`.

### Anonymous usage ping

To count installs and active versions, Handshake sends two anonymous events:
one when setup completes, and a weekly heartbeat piggybacked on the existing
release check. Each event contains only the Handshake version, operating
system, architecture, on install the names of detected agents, and a random
per-machine ID stored at `~/.handshake/telemetry_id`. No session content,
project names, paths, or account information is ever sent.

Opt out at any time:

```sh
export HANDSHAKE_NO_TELEMETRY=1
```
