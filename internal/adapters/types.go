package adapters

import (
	"fmt"

	"handshake/internal/db"
)

// SessionData is the intermediate representation an adapter produces from an
// agent's native session storage, before normalisation into the canonical DB.
type SessionData struct {
	ID         string
	Agent      string // "claude-code" | "opencode" | "hermes"
	Title      string
	WorkingDir string
	Model      string
	Summary    string
	Decisions  string // newline-separated settled decisions, written by source agent at checkpoint
	GitState   string // optional handoff state written by the source agent
	Messages   []MessageData
	CreatedAt  int64 // unix seconds
	UpdatedAt  int64 // unix seconds
}

type MessageData struct {
	ID        string
	Role      string // "user" | "assistant" | "tool"
	Content   string
	CreatedAt int64 // unix seconds
}

// Ingest normalises a SessionData into the canonical database.
func Ingest(database *db.Database, session *SessionData) error {
	if session.ID == "" {
		return fmt.Errorf("session has no ID")
	}
	if session.Title == "" {
		session.Title = session.Agent + " session " + session.ID
	}

	canonical := &db.Session{
		ID:         session.ID,
		Title:      session.Title,
		Agent:      session.Agent,
		WorkingDir: session.WorkingDir,
		Model:      session.Model,
		Summary:    session.Summary,
		Decisions:  session.Decisions,
		GitState:   session.GitState,
		CreatedAt:  session.CreatedAt,
		UpdatedAt:  session.UpdatedAt,
	}
	if err := database.StoreSession(canonical); err != nil {
		return err
	}

	for _, msg := range session.Messages {
		if err := database.StoreMessage(&db.Message{
			ID:        msg.ID,
			SessionID: session.ID,
			Role:      msg.Role,
			Content:   msg.Content,
			CreatedAt: msg.CreatedAt,
		}); err != nil {
			return err
		}
	}

	return nil
}

// IngestCodexSessions reads all sessions from the Codex session index
// and ingests any that are not already in the canonical DB.
// Called during list_sessions so Codex sessions appear without needing
// an explicit checkpoint first — same behaviour as OpenCode's plugin
// auto-sync and Claude Code's JSONL watcher.
func IngestCodexSessions(database *db.Database, homeDir string) error {
	adapter := NewCodexAdapter(homeDir)

	// List all sessions from the index
	stubs, err := adapter.ListSessions()
	if err != nil {
		return fmt.Errorf("failed to list Codex sessions: %w", err)
	}

	// Build the transcript path index once so each ReadSession below is an
	// O(1) lookup instead of its own directory walk.
	adapter.buildPathIndex()

	for _, stub := range stubs {
		// Check if already in canonical DB and up to date
		existing, err := database.GetSession(stub.ID)
		if err == nil && existing.UpdatedAt >= stub.UpdatedAt {
			// Already synced and current — skip
			continue
		}

		// Read full session from disk
		session, err := adapter.ReadSession(stub.ID)
		if err != nil {
			// Session file missing or unreadable — store stub only
			if storeErr := database.StoreSession(&db.Session{
				ID:        stub.ID,
				Title:     stub.Title,
				Agent:     "codex",
				UpdatedAt: stub.UpdatedAt,
				CreatedAt: stub.CreatedAt,
			}); storeErr != nil {
				continue
			}
			continue
		}

		// Ingest full session into canonical DB
		if err := Ingest(database, session); err != nil {
			continue
		}
	}

	return nil
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
