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
	Summary    string // optional handoff state written by the source agent
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

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
