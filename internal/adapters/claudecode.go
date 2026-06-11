package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeCodeAdapter reads sessions from Claude Code's native storage:
// ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl
type ClaudeCodeAdapter struct {
	projectsDir string
}

func NewClaudeCodeAdapter(homeDir string) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{
		projectsDir: filepath.Join(homeDir, ".claude", "projects"),
	}
}

// jsonlRecord is the subset of a Claude Code transcript line we care about.
type jsonlRecord struct {
	Type        string          `json:"type"`
	Summary     string          `json:"summary"`
	IsSidechain bool            `json:"isSidechain"`
	UUID        string          `json:"uuid"`
	Timestamp   string          `json:"timestamp"`
	CWD         string          `json:"cwd"`
	SessionID   string          `json:"sessionId"`
	Message     json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"` // tool_use
}

// ReadSession parses a session transcript. projectID is the encoded-cwd
// directory name; when empty, all project directories are scanned for the
// session file.
func (a *ClaudeCodeAdapter) ReadSession(projectID, sessionID string) (*SessionData, error) {
	path, err := a.findSessionFile(projectID, sessionID)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	session := &SessionData{
		ID:    sessionID,
		Agent: "claude-code",
	}
	var firstUserMessage string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rec jsonlRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // tolerate unknown/partial lines
		}

		if rec.Type == "summary" && rec.Summary != "" {
			session.Title = rec.Summary
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}
		if rec.IsSidechain {
			continue
		}
		if rec.CWD != "" {
			session.WorkingDir = rec.CWD
		}

		var msg claudeMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			continue
		}
		if msg.Model != "" {
			session.Model = msg.Model
		}

		role, content := flattenClaudeContent(msg.Role, msg.Content)
		if content == "" {
			continue
		}

		ts := parseRFC3339(rec.Timestamp)
		if session.CreatedAt == 0 || (ts != 0 && ts < session.CreatedAt) {
			session.CreatedAt = ts
		}
		if ts > session.UpdatedAt {
			session.UpdatedAt = ts
		}
		if firstUserMessage == "" && role == "user" {
			firstUserMessage = content
		}

		session.Messages = append(session.Messages, MessageData{
			ID:        rec.UUID,
			Role:      role,
			Content:   content,
			CreatedAt: ts,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	if len(session.Messages) == 0 {
		return nil, fmt.Errorf("session %s contains no messages", sessionID)
	}
	if session.Title == "" {
		session.Title = titleFromMessage(firstUserMessage)
	}

	return session, nil
}

func (a *ClaudeCodeAdapter) findSessionFile(projectID, sessionID string) (string, error) {
	if projectID != "" {
		path := filepath.Join(a.projectsDir, projectID, sessionID+".jsonl")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("session file not found: %s", path)
		}
		return path, nil
	}

	entries, err := os.ReadDir(a.projectsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read Claude Code projects dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(a.projectsDir, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("session %s not found in any Claude Code project", sessionID)
}

// flattenClaudeContent turns a message content (plain string or block array)
// into text. Messages that are purely tool results are reported as role "tool".
func flattenClaudeContent(role string, raw json.RawMessage) (string, string) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return role, strings.TrimSpace(asString)
	}

	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return role, ""
	}

	var parts []string
	textBlocks, toolResults := 0, 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
				textBlocks++
			}
		case "tool_use":
			parts = append(parts, "[tool: "+b.Name+"]")
		case "tool_result":
			toolResults++
		}
	}

	if textBlocks == 0 && toolResults > 0 {
		return "tool", fmt.Sprintf("[%d tool result(s)]", toolResults)
	}
	return role, strings.TrimSpace(strings.Join(parts, "\n"))
}

func parseRFC3339(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func titleFromMessage(content string) string {
	line := content
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "untitled session"
	}
	return truncate(line, 80)
}
