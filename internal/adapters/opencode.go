package adapters

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// OpenCodeAdapter reads sessions directly from OpenCode's SQLite database:
// ~/.local/share/opencode/opencode.db (session / message / part tables).
type OpenCodeAdapter struct {
	sourceDBPath string
}

func NewOpenCodeAdapter(homeDir string) *OpenCodeAdapter {
	return &OpenCodeAdapter{
		sourceDBPath: filepath.Join(homeDir, ".local", "share", "opencode", "opencode.db"),
	}
}

type openCodeMessageData struct {
	Role    string `json:"role"`
	ModelID string `json:"modelID"`
	Time    struct {
		Created int64 `json:"created"` // milliseconds
	} `json:"time"`
}

type openCodePartData struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Tool string `json:"tool"`
}

func (a *OpenCodeAdapter) ReadSession(sessionID string) (*SessionData, error) {
	conn, err := sql.Open("sqlite", "file:"+a.sourceDBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open opencode.db: %w", err)
	}
	defer conn.Close()

	session := &SessionData{Agent: "opencode"}
	var createdMs, updatedMs int64
	var model sql.NullString
	row := conn.QueryRow(
		"SELECT id, title, directory, COALESCE(model, ''), time_created, time_updated FROM session WHERE id = ?",
		sessionID,
	)
	if err := row.Scan(&session.ID, &session.Title, &session.WorkingDir, &model, &createdMs, &updatedMs); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("OpenCode session %s not found", sessionID)
		}
		return nil, fmt.Errorf("failed to read OpenCode session: %w", err)
	}
	session.Model = model.String
	session.CreatedAt = createdMs / 1000
	session.UpdatedAt = updatedMs / 1000

	rows, err := conn.Query(
		`SELECT m.id, m.data, m.time_created,
		        COALESCE((SELECT json_group_array(p.data)
		                  FROM (SELECT data FROM part WHERE message_id = m.id ORDER BY id) p), '[]')
		 FROM message m WHERE m.session_id = ? ORDER BY m.time_created, m.id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to read OpenCode messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, data, partsJSON string
		var timeCreatedMs int64
		if err := rows.Scan(&id, &data, &timeCreatedMs, &partsJSON); err != nil {
			return nil, fmt.Errorf("failed to scan OpenCode message: %w", err)
		}

		var meta openCodeMessageData
		if err := json.Unmarshal([]byte(data), &meta); err != nil {
			continue
		}
		if meta.ModelID != "" && session.Model == "" {
			session.Model = meta.ModelID
		}

		content := flattenOpenCodeParts(partsJSON)
		if content == "" {
			continue
		}

		session.Messages = append(session.Messages, MessageData{
			ID:        id,
			Role:      meta.Role,
			Content:   content,
			CreatedAt: timeCreatedMs / 1000,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating OpenCode messages: %w", err)
	}

	if len(session.Messages) == 0 {
		return nil, fmt.Errorf("OpenCode session %s contains no messages", sessionID)
	}

	return session, nil
}

func flattenOpenCodeParts(partsJSON string) string {
	var rawParts []string
	if err := json.Unmarshal([]byte(partsJSON), &rawParts); err != nil {
		return ""
	}

	var parts []string
	for _, raw := range rawParts {
		var p openCodePartData
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			continue
		}
		switch p.Type {
		case "text":
			if t := strings.TrimSpace(p.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool":
			parts = append(parts, "[tool: "+p.Tool+"]")
		}
	}
	return strings.Join(parts, "\n")
}
