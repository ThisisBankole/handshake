// Handshake session-sync plugin for OpenCode.
// Installed by `handshake init` into ~/.config/opencode/plugins/handshake.js
//
// Streams session updates to the local Handshake daemon (localhost:8765) so
// sessions can be restored from other agents, and injects the handoff-brief
// structure into OpenCode's own compaction.

const BASE_URL = process.env.HANDSHAKE_URL ?? "http://localhost:8765"
const SYNC_DEBOUNCE_MS = 1500

export const HandshakeSync = async ({ client }) => {
  const pending = new Map()

  const sync = async (sessionID) => {
    pending.delete(sessionID)
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
            id: info.id,
            title: info.title ?? "",
            working_dir: info.directory ?? "",
            model: info.model ?? "",
            created_at: Math.floor((info.time?.created ?? Date.now()) / 1000),
            updated_at: Math.floor((info.time?.updated ?? Date.now()) / 1000),
          },
          messages,
        }),
      })
    } catch {
      // Handshake daemon not running, or transient API error — never break OpenCode.
    }
  }

  return {
    event: async ({ event }) => {
      if (event.type !== "session.updated") return
      const id = event.properties?.info?.id
      if (!id) return
      clearTimeout(pending.get(id))
      pending.set(id, setTimeout(() => sync(id), SYNC_DEBOUNCE_MS))
    },

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
