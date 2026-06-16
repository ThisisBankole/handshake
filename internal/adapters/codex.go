package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"handshake/internal/git"
)

// CodexAdapter reads sessions from Codex's native storage:
//
//	~/.codex/session_index.jsonl  — index of all sessions (id, title, updated_at)
//	~/.codex/sessions/YYYY/MM/DD/rollout-<datetime>-<id>.jsonl — transcripts
type CodexAdapter struct {
	codexDir string
}

func NewCodexAdapter(homeDir string) *CodexAdapter {
	return &CodexAdapter{
		codexDir: filepath.Join(homeDir, ".codex"),
	}
}

// codexIndexEntry is one line from session_index.jsonl
type codexIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

// codexRecord is one line from a session transcript JSONL file.
// Codex uses a wrapper format: type + payload, not direct message objects.
type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"` // "response_item", "event_msg"
	Payload   json.RawMessage `json:"payload"`
}

// codexPayload is the inner payload of a response_item record.
type codexPayload struct {
	Type    string         `json:"type"`    // "message", "function_call", "reasoning"
	Role    string         `json:"role"`    // "user", "assistant"
	Name    string         `json:"name"`    // tool name for function_call
	Content []codexContent `json:"content"` // message content blocks
}

// codexContent is one block inside a message's content array.
type codexContent struct {
	Type string `json:"type"` // "input_text", "output_text"
	Text string `json:"text"`
}

// ListSessions reads session_index.jsonl and returns all sessions as SessionData
// stubs (no messages — messages are loaded separately by ReadSession).
func (a *CodexAdapter) ListSessions() ([]*SessionData, error) {
	indexPath := filepath.Join(a.codexDir, "session_index.jsonl")

	f, err := os.Open(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Codex not used yet
		}
		return nil, fmt.Errorf("failed to open session_index.jsonl: %w", err)
	}
	defer f.Close()

	var sessions []*SessionData
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry codexIndexEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // tolerate malformed lines
		}
		if entry.ID == "" {
			continue
		}

		ts := parseRFC3339(entry.UpdatedAt)

		sessions = append(sessions, &SessionData{
			ID:        entry.ID,
			Agent:     "codex",
			Title:     entry.ThreadName,
			CreatedAt: ts,
			UpdatedAt: ts,
		})
	}

	return sessions, scanner.Err()
}

// ReadSession finds and parses the JSONL transcript for a given session ID.
func (a *CodexAdapter) ReadSession(sessionID string) (*SessionData, error) {
	path, err := a.findSessionFile(sessionID)
	if err != nil {
		return nil, err
	}

	// Get the title from the index first
	title := a.titleFromIndex(sessionID)

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open Codex session file: %w", err)
	}
	defer f.Close()

	session := &SessionData{
		ID:    sessionID,
		Agent: "codex",
		Title: title,
	}

	var firstUserMessage string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rec codexRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}

		// Only process response_item records — skip event_msg entirely
		if rec.Type != "response_item" {
			continue
		}

		var payload codexPayload
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			continue
		}

		ts := parseRFC3339(rec.Timestamp)
		if session.CreatedAt == 0 || (ts != 0 && ts < session.CreatedAt) {
			session.CreatedAt = ts
		}
		if ts > session.UpdatedAt {
			session.UpdatedAt = ts
		}

		switch payload.Type {

		case "session_meta":
			var meta struct {
				ID  string `json:"id"`
				CWD string `json:"cwd"`
				Git *struct {
					CommitHash string `json:"commit_hash"`
					Branch     string `json:"branch"`
				} `json:"git"`
			}
			if err := json.Unmarshal(rec.Payload, &meta); err == nil {
				if meta.CWD != "" && session.WorkingDir == "" {
					session.WorkingDir = meta.CWD
				}
				if meta.Git != nil && session.GitState == "" {
					// Use Codex's own git capture rather than running git ourselves
					state := git.State{
						Commit:     meta.Git.CommitHash,
						Branch:     meta.Git.Branch,
						CapturedAt: session.CreatedAt,
					}
					encoded, _ := json.Marshal(state)
					session.GitState = string(encoded)
				}
			}
			continue

		case "message":
			// Extract text from content blocks
			content := flattenCodexContent(payload.Content)
			if content == "" {
				continue
			}

			role := payload.Role
			if role == "" || role == "developer" {
				continue
			}

			if firstUserMessage == "" && role == "user" {
				firstUserMessage = content
			}

			session.Messages = append(session.Messages, MessageData{
				ID:        fmt.Sprintf("codex:%s:%d", sessionID, len(session.Messages)),
				Role:      role,
				Content:   content,
				CreatedAt: ts,
			})

		case "function_call":
			// Tool call — record as a tool message
			toolName := payload.Name
			if toolName == "" {
				continue
			}

			// Try to extract workdir as the session working directory
			if session.WorkingDir == "" {
				session.WorkingDir = extractWorkdir(rec.Payload)
			}

			session.Messages = append(session.Messages, MessageData{
				ID:        fmt.Sprintf("codex:%s:%d", sessionID, len(session.Messages)),
				Role:      "tool",
				Content:   "[tool: " + toolName + "]",
				CreatedAt: ts,
			})

		case "reasoning":
			// Encrypted reasoning — skip entirely
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read Codex session: %w", err)
	}

	if len(session.Messages) == 0 {
		return nil, fmt.Errorf("Codex session %s contains no messages", sessionID)
	}
	if session.Title == "" {
		session.Title = titleFromMessage(firstUserMessage)
	}

	return session, nil
}

// findSessionFile searches for the JSONL file matching the given session ID.
// Codex organises files as sessions/YYYY/MM/DD/rollout-<datetime>-<id>.jsonl
// so we glob rather than computing the path.
func (a *CodexAdapter) findSessionFile(sessionID string) (string, error) {
	sessionsDir := filepath.Join(a.codexDir, "sessions")

	// Walk the sessions directory looking for a file containing the session ID
	var found string
	err := filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, sessionID+".jsonl") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to search Codex sessions: %w", err)
	}
	if found == "" {
		return "", fmt.Errorf("Codex session %s not found", sessionID)
	}
	return found, nil
}

// titleFromIndex reads session_index.jsonl to find the title for a session ID.
func (a *CodexAdapter) titleFromIndex(sessionID string) string {
	indexPath := filepath.Join(a.codexDir, "session_index.jsonl")
	f, err := os.Open(indexPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry codexIndexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.ID == sessionID {
			return entry.ThreadName
		}
	}
	return ""
}

// flattenCodexContent joins text blocks from a message's content array.
func flattenCodexContent(blocks []codexContent) string {
	var parts []string
	for _, b := range blocks {
		// input_text = user message, output_text = assistant message
		if (b.Type == "input_text" || b.Type == "output_text") && b.Text != "" {
			text := strings.TrimSpace(b.Text)
			// Strip Codex system XML injected into user messages
			if strings.HasPrefix(text, "<environment_context>") ||
				strings.HasPrefix(text, "<permissions") {
				continue
			}
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// extractWorkdir tries to pull the workdir field from a function_call payload.
func extractWorkdir(raw json.RawMessage) string {
	var payload struct {
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	var args struct {
		Workdir string `json:"workdir"`
	}
	if err := json.Unmarshal([]byte(payload.Arguments), &args); err != nil {
		return ""
	}
	return args.Workdir
}

// codexTimeFromFilename extracts the timestamp from a Codex session filename.
// Format: rollout-2026-04-14T19-41-24-<uuid>.jsonl
func codexTimeFromFilename(filename string) int64 {
	base := filepath.Base(filename)
	// Strip prefix and suffix
	base = strings.TrimPrefix(base, "rollout-")
	base = strings.TrimSuffix(base, ".jsonl")
	// The last 36 chars are the UUID — strip them plus the preceding dash
	if len(base) > 37 {
		base = base[:len(base)-37]
	}
	// Replace dashes in time portion with colons to make valid RFC3339
	// Format is: 2026-04-14T19-41-24 → 2026-04-14T19:41:24Z
	if len(base) >= 19 {
		base = base[:13] + ":" + base[14:16] + ":" + base[17:19] + "Z"
		t, err := time.Parse(time.RFC3339, base)
		if err == nil {
			return t.Unix()
		}
	}
	return 0
}
