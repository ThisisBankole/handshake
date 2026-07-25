// Handshake session-sync plugin for OpenCode.
// Installed by `handshake init` into ~/.config/opencode/plugins/handshake.js
//
// Autonomous session portability:
//   session.updated           → sync messages to Handshake in real time
//   session.idle              → regenerate brief after every completed turn
//   session.compacted         → capture OpenCode's compaction summary
//   experimental.session.compacting → inject handoff brief format into compaction

// HANDSHAKE_URL is the MCP endpoint (normally ending in /mcp), while this
// plugin calls the daemon's plain HTTP routes. Normalize either form.
const BASE_URL = (process.env.HANDSHAKE_BASE_URL ?? process.env.HANDSHAKE_URL ?? "http://localhost:8765")
  .replace(/\/mcp\/?$/, "")
  .replace(/\/$/, "")

// Debounce timers keyed by session ID
const syncPending = new Map()
const briefPending = new Map()

const SYNC_DEBOUNCE_MS = 1500   // wait 1.5s after last update before syncing
const BRIEF_DEBOUNCE_MS = 5000  // wait 5s after idle before regenerating brief

const asSeconds = (value) => {
  if (!value) return Math.floor(Date.now() / 1000)
  return Math.floor(value > 10_000_000_000 ? value / 1000 : value)
}

const firstString = (...values) => values.find((v) => typeof v === "string" && v.length > 0) ?? ""

const collectSessionIDs = (value, out = []) => {
  if (!value || typeof value !== "object") return out
  for (const [key, nested] of Object.entries(value)) {
    if (typeof nested === "string") {
      const normalized = key.toLowerCase().replace(/[_-]/g, "")
      if (normalized === "sessionid" || normalized === "id") out.push(nested)
      continue
    }
    collectSessionIDs(nested, out)
  }
  return out
}

const sessionIDFromEvent = (event) => {
  const props = event.properties ?? {}
  const candidates = [
    props.sessionID,
    props.sessionId,
    props.session_id,
    props.session?.id,
    props.session?.sessionID,
    props.session?.sessionId,
    props.info?.sessionID,
    props.info?.sessionId,
    props.info?.session_id,
    ...collectSessionIDs(props),
    props.info?.id,
  ].filter((id) => typeof id === "string" && id.length > 0)

  return candidates.find((id) => id.startsWith("ses_")) ?? candidates[0] ?? ""
}

// ── Sync messages to Handshake ────────────────────────────────────────────────

const syncSession = async (sessionID, client) => {
  syncPending.delete(sessionID)
  try {
    const info = (await client.session.get({ path: { id: sessionID } })).data
    if (!info) return

    const entries = (await client.session.messages({ path: { id: sessionID } })).data ?? []

    const messages = entries
      .map((entry) => {
        const m = entry.info ?? entry
        const parts = entry.parts ?? []
        const content = parts
          .map((p) => {
            if (p.type === "text" && p.text) return p.text
            if (p.type === "tool") return `[tool: ${p.tool ?? ""}]`
            return ""
          })
          .filter(Boolean)
          .join("\n")
        return {
          id: m.id,
          role: m.role,
          content,
          created_at: Math.floor((m.time?.created ?? Date.now()) / 1000),
        }
      })
      .filter((m) => m.id && m.content)

    await fetch(`${BASE_URL}/ingest`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        agent: "opencode",
        session: {
          id: sessionID,
          title: info.title ?? "",
          working_dir: firstString(info.directory, info.cwd, info.path, info.workspace),
          model: firstString(info.model, info.modelID, info.modelId),
          created_at: asSeconds(info.time?.created ?? info.time_created ?? info.created_at),
          updated_at: asSeconds(info.time?.updated ?? info.time_updated ?? info.updated_at),
        },
        messages,
      }),
    })
  } catch {
    // Handshake daemon not running or transient error — never break OpenCode.
  }
}

// ── Regenerate brief ──────────────────────────────────────────────────────────

const generateBrief = async (sessionID) => {
  briefPending.delete(sessionID)
  try {
    await fetch(`${BASE_URL}/generate-brief`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ session_id: sessionID }),
    })
  } catch {
    // Handshake daemon not running — silently skip.
  }
}

// ── Plugin export ─────────────────────────────────────────────────────────────

export const HandshakeSync = async ({ client }) => {
  return {
    // 1. Sync messages in real time on every session update.
    event: async ({ event }) => {
      if (event.type === "session.updated") {
        const id = sessionIDFromEvent(event)
        if (!id) return
        clearTimeout(syncPending.get(id))
        syncPending.set(id, setTimeout(() => syncSession(id, client), SYNC_DEBOUNCE_MS))
        return
      }

      // 2. Regenerate brief when session goes idle (agent finished responding).
      if (event.type === "session.idle") {
        const id = sessionIDFromEvent(event)
        if (!id) return
        clearTimeout(briefPending.get("brief:" + id))
        briefPending.set("brief:" + id, setTimeout(() => generateBrief(id), BRIEF_DEBOUNCE_MS))
        return
      }

      // 3. Capture compaction summary when OpenCode finishes compacting.
      if (event.type === "session.compacted") {
        const id = sessionIDFromEvent(event)
        const summary = event.properties?.summary ?? event.properties?.info?.summary ?? ""
        if (!id) return

        try {
          // Store the compaction summary as the session summary in Handshake.
          // This becomes the "Current State & Next Steps" section of the brief.
          await fetch(`${BASE_URL}/ingest`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({
              agent: "opencode",
              session: {
                id,
                summary,
                updated_at: Math.floor(Date.now() / 1000),
              },
              messages: [],
            }),
          })

          // Regenerate brief immediately after capturing the summary.
          await generateBrief(id)
        } catch {
          // Silently skip.
        }
        return
      }
    },

    // 4. Inject handoff brief format into OpenCode's compaction prompt.
    // This shapes the summary OpenCode generates — captured above in session.compacted.
    "experimental.session.compacting": async (_input, output) => {
      output.context.push(`## Handshake handoff state

Structure the summary so another agent could pick this session up cold. Include, under explicit headings:
- Goal — what the user is trying to achieve
- Decisions made — with the reasoning behind each
- Current state — what is done and verified
- Files modified — paths and what changed
- Next steps — concrete, in order
- Constraints — things already settled that must not be relitigated`)
    },
  }
}
