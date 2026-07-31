# Handshake

Handshake is a daemon that moves coding sessions between AI coding agents.
It operates on your machine only. It synchronizes your sessions between
Claude Code, OpenCode, Hermes, Codex, and Cursor. If one agent stops at a
token limit, you can continue the session in a different agent. Your context
is already there.

**Full documentation: [docs.gethandshake.dev](https://docs.gethandshake.dev)**

## How it works

Handshake operates as a background daemon. It connects to the native event
system of each agent. When an agent completes a response, Handshake saves the
session automatically. No manual action is necessary.

To move a session to a different agent, give this instruction to the new
agent:

```
restore my council spending session
```

The new agent receives a full handoff brief. The brief contains the initial
goal, the decisions, the current state, the changed files, and the next
steps.

## Install

**Mac and Linux (curl):**
```bash
curl -fsSL https://gethandshake.dev/install.sh | sh
```

**Mac (Homebrew):**
```bash
brew install ThisisBankole/tools/handshake
```

**From source (Go 1.25 or later is necessary):**
```bash
git clone https://github.com/ThisisBankole/handshake.git
cd handshake
go build -o handshake ./cmd/handshake
```

If you install Handshake with curl in a terminal, the guided setup starts
automatically. If your environment is not interactive, start the setup
manually:

```bash
handshake setup
```

The setup takes approximately 30 seconds. The setup does these steps:

1. It registers Handshake with your agents.
2. It installs Handshake as a login service.
3. It starts the daemon.

After the setup, the daemon operates in the background. No attention is
necessary.

## Updates

The daemon checks for a new release each week. If a new release is
available, the daemon updates itself. The daemon does these steps:

1. It downloads the new release from GitHub.
2. It calculates the SHA-256 checksum of the download. It compares the
   checksum with the value in the release `checksums.txt` file. If the
   values are different, the daemon stops and keeps the current version.
3. It replaces the binary with the new version in one atomic step.
4. It exits. The login service (launchd or systemd) starts the daemon
   again with the new version.

Automatic updates operate only for a login-service install. The daemon
does not change a Homebrew binary; use `brew upgrade handshake` for a
Homebrew install. To stop automatic updates, set this variable before the
daemon starts:

```bash
export HANDSHAKE_NO_AUTO_UPDATE=1
```

## Project Knowledge

Each checkpoint that has Git data writes a knowledge bundle to
`~/.handshake/knowledge/<project-id>/`. The bundle is private and
deterministic. It agrees with the Open Knowledge Format (OKF). The bundle
contains a checkpoint timeline, a Git timeline, and text diffs for each
file. Each diff has a size limit. Handshake does not export session
transcripts. Handshake removes sensitive paths and generated paths. The
diff index records each removed path.

Agents can publish a `project-brief.md` and a `repo-map.md` with the local
MCP tool `publish_project_knowledge`. Each document declares the factual
revision that the agent used. Handshake rejects a document that uses an old
revision. When a new checkpoint occurs, Handshake sets the published
documents to stale.

A background writer is available as an option. The background writer keeps
the two AI-authored documents current. Then the interactive agent does not
do this work. To configure the background writer and to see its
configuration, use these commands:

```bash
handshake knowledge author setup
handshake knowledge author show
```

The setup finds Claude Code, Codex, OpenCode, and Hermes. You select one
fallback writer. The setup asks for your approval, because model runs can
use your quota. The daemon first gives the active agent a short time to
publish the two documents. Before the daemon starts the CLI, it makes sure
that the documents are stale for the latest factual revision. The daemon
does not accept terminal output as proof of success. To stop background
model runs, use this command:

```bash
handshake knowledge author off
```

The commands `handshake setup` and `handshake init` install two skills: the
general skill and the `knowledge-authoring` skill. The skills go into the
global skill location for Claude Code, Codex, OpenCode, and Hermes.
Handshake does not change your skills that have the same name. The general
skill tells each agent to report a cached Handshake update before
applicable work. When a checkpoint sets the documents to stale, Claude Code
receives one Stop-hook instruction. The instruction tells Claude Code to
use the skill before the turn ends. The skill is also available in
OpenCode, Codex, and Hermes. Background authoring uses the non-interactive
CLI command of the selected agent. Because of this, background authoring
operates when that agent is not in interactive use.

Cursor receives the Handshake MCP server and a managed Stop hook. The hook
imports the local Cursor transcript after each completed turn. If the
project knowledge is stale, the hook sends one instruction to Cursor. The
instruction tells Cursor to publish the two documents through MCP. Cursor
is not available as an unattended background writer.

## Switching agents

To continue your work in a different agent, give these instructions to the
agent:

```
list my sessions
```

```
restore my auth refactor session
```

The new agent continues with the full context.

A CLI is also available:

```bash
handshake list
handshake handoff "my session title"
```

## Manual checkpoint

To save the session at a specific point, give this instruction to your
agent:

```
checkpoint this session
```

## Supported agents

- Claude Code
- OpenCode
- Hermes
- Codex

## Configuration

The default address of the daemon is `localhost:8765`. If this port is in
use, `handshake setup` finds the conflict and selects the next free port.
No manual action is necessary. The setup shows the selected port. The setup
registers all agents with the correct URL.

To set a specific port, set these variables before you run
`handshake setup`:

| Variable | Purpose |
|---|---|
| `HANDSHAKE_ADDR` | The address that the daemon attaches to (example: `localhost:8766`) |
| `HANDSHAKE_URL` | The MCP endpoint that Handshake registers with the agents (example: `http://localhost:8766/mcp`) |

```bash
export HANDSHAKE_ADDR=localhost:8766
export HANDSHAKE_URL=http://localhost:8766/mcp
handshake setup
```

To change the port of an installed service on macOS, do these steps:

1. Add an `EnvironmentVariables` block to the launchd plist at
   `~/Library/LaunchAgents/com.handshake.serve.plist`:

   ```xml
   <key>EnvironmentVariables</key>
   <dict>
     <key>HANDSHAKE_ADDR</key><string>localhost:8766</string>
     <key>HANDSHAKE_URL</key><string>http://localhost:8766/mcp</string>
   </dict>
   ```

2. Run `handshake setup` again to update the agent registrations.

3. Load the service again:

   ```bash
   launchctl unload ~/Library/LaunchAgents/com.handshake.serve.plist
   launchctl load  ~/Library/LaunchAgents/com.handshake.serve.plist
   ```

## Uninstall

```bash
handshake uninstall
```

The command removes Handshake from all agent configurations. It stops the
daemon. The command asks if you want to delete the session database and the
binary.

## Privacy

Handshake operates on your machine only. The daemon attaches to
`localhost:8765`. The network cannot get access to the daemon. Handshake
has no cloud synchronization and no accounts. Handshake keeps your sessions
in the local file `~/.handshake/sessions.db`.
