---
name: knowledge-authoring
description: Create or incrementally refresh Handshake project briefs and repository maps from a project's factual OKF bundle. Use when project knowledge is missing or stale after a checkpoint.
metadata:
  handshake-managed: "true"
---

# Handshake Knowledge Authoring

Author the two AI documents that supplement Handshake's deterministic project
facts: `project-brief.md` and `repo-map.md`.

At the start of a Handshake-related task, call
`get_handshake_update_status`. If it reports an available update, mention it
to the user briefly, then continue the requested work. Do not interrupt work
when no update is available and do not ask Handshake to perform a network check.

## Workflow

1. Call `get_project_knowledge_context` with the current working directory.
2. Read the listed factual bundle documents from
   `~/.handshake/knowledge/<project-id>/`. Start with `index.md`, `log.md`,
   and `git-timeline.md`; inspect only relevant per-file diffs or session
   timelines when they resolve a real ambiguity.
3. If both AI documents are `current`, stop unless meaningful new facts need a
   better explanation. Never rewrite documents just to change wording.
4. Draft each document from factual evidence. Treat source-agent summaries and
   decisions as authoritative handoff context. Do not claim unverified intent,
   ownership, or architecture.
5. Call `publish_project_knowledge` once for `project-brief` and once for
   `repo-map`, using the exact `project_id` and `facts_revision` returned by
   the context tool. Send Markdown body only: do not include a top-level
   `# Project Brief` or `# Repository Map` heading because Handshake adds it.

When Claude Code supplies a Handshake Stop-hook instruction, perform this
workflow in that continuation. It is intentionally requested at most once per
checkpoint, so complete the two publications before ending the turn.

If publication reports a stale revision, discard the draft, fetch context
again, and regenerate from the newer facts. Do not force an older brief over
newer work.

## Project Brief

Write concise sections for:

- Purpose and current product or engineering goal.
- Current state: completed work, active work, and verified results.
- Constraints and settled decisions.
- Immediate next steps and known risks.

Prefer concrete files, commits, tests, and source-agent statements. State
unknowns plainly.

## Repository Map

Write a navigational map, not a file dump:

- Major directories and each responsibility.
- Entry points, core data/control flow, and integration boundaries.
- Important tests, build commands, and configuration locations when evidenced.
- Areas most relevant to the current work.

Keep it stable across small edits. Update it when structure or ownership
boundaries change, not for routine line-level changes.

## Safety

- Never copy transcripts, secrets, credentials, or excluded diff content.
- Do not use HTML as agent context; Markdown is canonical.
- Keep evidence short in `publish_project_knowledge`; cite factual bundle files
  and snapshot IDs rather than copying their contents.
