package adapters

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// HermesAdapter reads sessions directly from Hermes' SQLite database:
// ~/.hermes/state.db (sessions / messages tables, WAL mode — safe for
// concurrent reads while Hermes is writing).
type HermesAdapter struct {
	sourceDBPath string
}

func NewHermesAdapter(homeDir string) *HermesAdapter {
	return &HermesAdapter{
		sourceDBPath: filepath.Join(homeDir, ".hermes", "state.db"),
	}
}

func (a *HermesAdapter) ReadSession(sessionID string) (*SessionData, error) {
	conn, err := sql.Open("sqlite", "file:"+a.sourceDBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open Hermes state.db: %w", err)
	}
	defer conn.Close()

	session := &SessionData{Agent: "hermes"}
	var title, model, cwd sql.NullString
	var startedAt float64
	var endedAt sql.NullFloat64
	row := conn.QueryRow(
		"SELECT id, title, model, cwd, started_at, ended_at FROM sessions WHERE id = ?",
		sessionID,
	)
	if err := row.Scan(&session.ID, &title, &model, &cwd, &startedAt, &endedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("Hermes session %s not found", sessionID)
		}
		return nil, fmt.Errorf("failed to read Hermes session: %w", err)
	}
	session.Title = title.String
	session.Model = model.String
	session.WorkingDir = cwd.String
	session.CreatedAt = int64(startedAt)
	session.UpdatedAt = int64(startedAt)
	if endedAt.Valid {
		session.UpdatedAt = int64(endedAt.Float64)
	}

	rows, err := conn.Query(
		`SELECT id, role, COALESCE(content, ''), COALESCE(tool_name, ''), timestamp
		 FROM messages WHERE session_id = ? AND active = 1 ORDER BY id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to read Hermes messages: %w", err)
	}
	defer rows.Close()

	var firstUserMessage string
	for rows.Next() {
		var id int64
		var role, content, toolName string
		var ts float64
		if err := rows.Scan(&id, &role, &content, &toolName, &ts); err != nil {
			return nil, fmt.Errorf("failed to scan Hermes message: %w", err)
		}

		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if role == "tool" && toolName != "" {
			content = "[tool: " + toolName + "]\n" + content
		}
		if firstUserMessage == "" && role == "user" {
			firstUserMessage = content
		}

		tsUnix := int64(ts)
		if tsUnix > session.UpdatedAt {
			session.UpdatedAt = tsUnix
		}

		session.Messages = append(session.Messages, MessageData{
			// Hermes message IDs are per-table integers; namespace them so
			// they are unique in the canonical messages table.
			ID:        fmt.Sprintf("hermes:%s:%d", sessionID, id),
			Role:      role,
			Content:   content,
			CreatedAt: tsUnix,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating Hermes messages: %w", err)
	}

	if len(session.Messages) == 0 {
		return nil, fmt.Errorf("Hermes session %s contains no messages", sessionID)
	}
	if session.Title == "" {
		session.Title = titleFromMessage(firstUserMessage)
	}

	return session, nil
}
