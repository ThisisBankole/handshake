package adapters

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"handshake/internal/db"
)

// PullableAgents lists the agents whose sessions can be pulled straight from
// their native local storage, in the order they are synced.
var PullableAgents = []string{"claude-code", "codex", "opencode", "hermes"}

// SyncResult reports what one agent's pull did.
type SyncResult struct {
	Agent     string
	Scanned   int   // sessions found in the agent's native storage
	Imported  int   // new sessions added to the canonical DB
	Updated   int   // existing sessions refreshed with newer content
	Unchanged int   // already in the DB and current
	Failed    int   // sessions that could not be read or stored
	Err       error // fatal: the agent's storage could not be listed at all
}

// Total returns how many sessions the sync actually wrote.
func (r SyncResult) Total() int { return r.Imported + r.Updated }

// SyncWarnings summarizes failures without discarding successful work from
// other agents or sessions in the same pull.
func SyncWarnings(results []SyncResult) []string {
	var warnings []string
	for _, result := range results {
		if result.Err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", result.Agent, result.Err))
		}
		if result.Failed > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d session(s) failed to import", result.Agent, result.Failed))
		}
	}
	return warnings
}

// SyncAll pulls sessions from every pullable agent's native storage into the
// canonical database. Agents whose storage is absent report zero scanned.
func SyncAll(database *db.Database, homeDir string) []SyncResult {
	results := make([]SyncResult, 0, len(PullableAgents))
	for _, agent := range PullableAgents {
		results = append(results, SyncAgent(database, homeDir, agent))
	}
	return results
}

// SyncAgent pulls one agent's sessions from its native storage. Sessions
// already in the database are skipped unless the native copy is newer;
// summaries, decisions, and git state recorded at checkpoint time survive
// (StoreSession keeps them when the incoming values are empty).
func SyncAgent(database *db.Database, homeDir, agent string) SyncResult {
	knowledgeRoot := filepath.Join(homeDir, ".handshake", "knowledge")
	switch agent {
	case "claude-code":
		a := NewClaudeCodeAdapter(homeDir)
		stubs, err := a.ListSessions()
		return syncSessions(database, knowledgeRoot, agent, stubs, err,
			func(id string) (*SessionData, error) { return a.ReadSession("", id) }, false)
	case "codex":
		a := NewCodexAdapter(homeDir)
		stubs, err := a.ListSessions()
		if err == nil {
			a.buildPathIndex()
		}
		return syncSessions(database, knowledgeRoot, agent, stubs, err, a.ReadSession, true)
	case "opencode":
		a := NewOpenCodeAdapter(homeDir)
		stubs, err := a.ListSessions()
		return syncSessions(database, knowledgeRoot, agent, stubs, err, a.ReadSession, false)
	case "hermes":
		a := NewHermesAdapter(homeDir)
		stubs, err := a.ListSessions()
		return syncSessions(database, knowledgeRoot, agent, stubs, err, a.ReadSession, false)
	default:
		return SyncResult{Agent: agent, Err: fmt.Errorf(
			"unknown agent %q — pullable agents: %s", agent, strings.Join(PullableAgents, ", "))}
	}
}

// syncSessions runs the shared pull loop over an agent's session stubs.
// stubOnFail stores a metadata-only stub when a transcript cannot be read
// (Codex keeps an index of sessions whose transcript files may be gone).
func syncSessions(database *db.Database, knowledgeRoot, agent string, stubs []*SessionData, listErr error,
	read func(string) (*SessionData, error), stubOnFail bool) SyncResult {

	res := SyncResult{Agent: agent}
	if listErr != nil {
		res.Err = listErr
		return res
	}
	res.Scanned = len(stubs)

	for _, stub := range stubs {
		existing, err := database.GetSession(stub.ID)
		exists := err == nil
		if exists && stub.UpdatedAt > 0 && existing.UpdatedAt >= stub.UpdatedAt {
			res.Unchanged++
			continue
		}

		full, err := read(stub.ID)
		if err != nil {
			if stubOnFail && !exists {
				if storeErr := database.StoreSession(&db.Session{
					ID: stub.ID, Title: stub.Title, Agent: agent,
					CreatedAt: stub.CreatedAt, UpdatedAt: stub.UpdatedAt,
				}); storeErr == nil {
					res.Imported++
					continue
				}
			}
			res.Failed++
			continue
		}

		// A stub's UpdatedAt can overshoot the transcript content (file mtime
		// lands after the last message timestamp), so re-check what was parsed.
		if exists && full.UpdatedAt > 0 && existing.UpdatedAt >= full.UpdatedAt {
			res.Unchanged++
			continue
		}

		if err := Ingest(database, knowledgeRoot, full); err != nil {
			res.Failed++
			continue
		}
		if exists {
			res.Updated++
		} else {
			res.Imported++
		}
	}
	return res
}

// ListSessions enumerates Claude Code transcripts across all project
// directories as stubs (ID + file mtime; no messages). Returns nil when
// Claude Code has no local storage yet.
func (a *ClaudeCodeAdapter) ListSessions() ([]*SessionData, error) {
	entries, err := os.ReadDir(a.projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read Claude Code projects dir: %w", err)
	}

	var sessions []*SessionData
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(a.projectsDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			sessions = append(sessions, &SessionData{
				ID:        strings.TrimSuffix(f.Name(), ".jsonl"),
				Agent:     "claude-code",
				UpdatedAt: info.ModTime().Unix(),
			})
		}
	}
	return sessions, nil
}

// ListSessions enumerates OpenCode sessions from its SQLite database as stubs
// (no messages). Returns nil when OpenCode has no local storage yet.
func (a *OpenCodeAdapter) ListSessions() ([]*SessionData, error) {
	if _, err := os.Stat(a.sourceDBPath); os.IsNotExist(err) {
		return nil, nil
	}
	conn, err := sql.Open("sqlite", "file:"+a.sourceDBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open opencode.db: %w", err)
	}
	defer conn.Close()

	rows, err := conn.Query("SELECT id, title, time_created, time_updated FROM session")
	if err != nil {
		return nil, fmt.Errorf("failed to list OpenCode sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*SessionData
	for rows.Next() {
		s := &SessionData{Agent: "opencode"}
		var createdMs, updatedMs int64
		if err := rows.Scan(&s.ID, &s.Title, &createdMs, &updatedMs); err != nil {
			return nil, fmt.Errorf("failed to scan OpenCode session: %w", err)
		}
		s.CreatedAt = createdMs / 1000
		s.UpdatedAt = updatedMs / 1000
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// ListSessions enumerates Hermes sessions from its SQLite database as stubs
// (no messages). Returns nil when Hermes has no local storage yet.
func (a *HermesAdapter) ListSessions() ([]*SessionData, error) {
	if _, err := os.Stat(a.sourceDBPath); os.IsNotExist(err) {
		return nil, nil
	}
	conn, err := sql.Open("sqlite", "file:"+a.sourceDBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open Hermes state.db: %w", err)
	}
	defer conn.Close()

	rows, err := conn.Query("SELECT id, COALESCE(title, ''), started_at, ended_at FROM sessions")
	if err != nil {
		return nil, fmt.Errorf("failed to list Hermes sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*SessionData
	for rows.Next() {
		s := &SessionData{Agent: "hermes"}
		var startedAt float64
		var endedAt sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.Title, &startedAt, &endedAt); err != nil {
			return nil, fmt.Errorf("failed to scan Hermes session: %w", err)
		}
		s.CreatedAt = int64(startedAt)
		s.UpdatedAt = s.CreatedAt
		if endedAt.Valid {
			s.UpdatedAt = int64(endedAt.Float64)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
