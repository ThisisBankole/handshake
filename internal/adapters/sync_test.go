package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"handshake/internal/db"
)

func newSyncTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// writeClaudeTranscript writes a minimal Claude Code JSONL transcript with
// one user and one assistant message at the given time.
func writeClaudeTranscript(t *testing.T, homeDir, project, sessionID string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(homeDir, ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	line := func(typ, role, text string, ts time.Time) string {
		return fmt.Sprintf(
			`{"type":%q,"uuid":"%s-%s","timestamp":%q,"cwd":"/tmp/proj","sessionId":%q,"message":{"role":%q,"content":%q}}`,
			typ, sessionID, typ, ts.Format(time.RFC3339), sessionID, role, text)
	}
	content := line("user", "user", "fix the port bug", at) + "\n" +
		line("assistant", "assistant", "done, port bug fixed", at.Add(time.Minute)) + "\n"
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestSyncAgentClaudeCode(t *testing.T) {
	homeDir := t.TempDir()
	database := newSyncTestDB(t)
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeClaudeTranscript(t, homeDir, "-tmp-proj", "sess-1", start)

	// First pull imports the session with its parsed content.
	res := SyncAgent(database, homeDir, "claude-code")
	if res.Err != nil {
		t.Fatalf("SyncAgent: %v", res.Err)
	}
	if res.Scanned != 1 || res.Imported != 1 || res.Updated != 0 || res.Failed != 0 {
		t.Fatalf("first pull: %+v", res)
	}
	session, err := database.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.Agent != "claude-code" || session.Title != "fix the port bug" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.ProjectID == "" {
		t.Fatalf("sync did not assign project identity: %+v", session)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".handshake", "knowledge", session.ProjectID, "index.md")); err != nil {
		t.Fatalf("sync did not write knowledge bundle: %v", err)
	}

	// A second pull with nothing new is a no-op, even though the file's
	// mtime is later than the last message timestamp.
	res = SyncAgent(database, homeDir, "claude-code")
	if res.Imported != 0 || res.Updated != 0 || res.Unchanged != 1 {
		t.Fatalf("repeat pull: %+v", res)
	}

	// New content updates the existing session instead of duplicating it.
	writeClaudeTranscript(t, homeDir, "-tmp-proj", "sess-1", start.Add(30*time.Minute))
	res = SyncAgent(database, homeDir, "claude-code")
	if res.Imported != 0 || res.Updated != 1 {
		t.Fatalf("pull after change: %+v", res)
	}
}

func TestSyncPreservesCheckpointSummary(t *testing.T) {
	homeDir := t.TempDir()
	database := newSyncTestDB(t)
	writeClaudeTranscript(t, homeDir, "-tmp-proj", "sess-1", time.Now().Add(-time.Hour))

	// A checkpoint recorded a summary before the pull re-reads the transcript.
	if err := database.StoreSession(&db.Session{
		ID: "sess-1", Title: "checkpointed", Agent: "claude-code",
		Summary: "handoff summary from checkpoint", UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	res := SyncAgent(database, homeDir, "claude-code")
	if res.Updated != 1 {
		t.Fatalf("pull: %+v", res)
	}
	session, err := database.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.Summary != "handoff summary from checkpoint" {
		t.Fatalf("pull clobbered the checkpoint summary: %+v", session)
	}
}

func TestSyncAllHandlesAbsentStores(t *testing.T) {
	homeDir := t.TempDir() // no agent has any local storage
	database := newSyncTestDB(t)

	results := SyncAll(database, homeDir)
	if len(results) != len(PullableAgents) {
		t.Fatalf("expected %d results, got %d", len(PullableAgents), len(results))
	}
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("%s: unexpected error: %v", res.Agent, res.Err)
		}
		if res.Scanned != 0 || res.Total() != 0 {
			t.Errorf("%s: expected empty result, got %+v", res.Agent, res)
		}
	}
}

func TestSyncAgentUnknown(t *testing.T) {
	res := SyncAgent(newSyncTestDB(t), t.TempDir(), "cursor")
	if res.Err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
}

func TestSyncWarnings_ReportsFatalAndPartialFailures(t *testing.T) {
	warnings := SyncWarnings([]SyncResult{
		{Agent: "claude-code", Err: fmt.Errorf("storage unavailable")},
		{Agent: "codex", Failed: 2},
		{Agent: "opencode", Imported: 1},
	})

	if len(warnings) != 2 {
		t.Fatalf("warning count = %d, want 2: %v", len(warnings), warnings)
	}
	if warnings[0] != "claude-code: storage unavailable" {
		t.Errorf("warning 0 = %q", warnings[0])
	}
	if warnings[1] != "codex: 2 session(s) failed to import" {
		t.Errorf("warning 1 = %q", warnings[1])
	}
}
