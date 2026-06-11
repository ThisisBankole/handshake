package engine

import (
	"fmt"
	"strings"
	"time"

	"handshake/internal/db"
)

type HandoffBrief struct {
	SessionID string
	Title     string
	Agent     string
	Brief     string
}

type BriefGenerator struct {
	db *db.Database
}

func NewBriefGenerator(database *db.Database) *BriefGenerator {
	return &BriefGenerator{db: database}
}

// GenerateBrief returns the handoff brief for a session. A cached brief is
// reused only if it was generated after the session's last update; otherwise
// it is regenerated from the stored messages.
func (g *BriefGenerator) GenerateBrief(sessionID string) (*HandoffBrief, error) {
	session, err := g.db.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	if cached, err := g.db.GetBrief(sessionID); err == nil && cached.GeneratedAt >= session.UpdatedAt {
		return &HandoffBrief{
			SessionID: session.ID,
			Title:     session.Title,
			Agent:     session.Agent,
			Brief:     cached.Content,
		}, nil
	}

	messages, err := g.db.GetMessages(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	brief := buildBrief(session, messages)
	if err := g.db.StoreBrief(sessionID, brief); err != nil {
		return nil, fmt.Errorf("failed to store brief: %w", err)
	}

	return &HandoffBrief{
		SessionID: session.ID,
		Title:     session.Title,
		Agent:     session.Agent,
		Brief:     brief,
	}, nil
}

const (
	minRecentMessages = 12
	maxRecentMessages = 30
	goalMaxChars      = 600
	excerptMaxChars   = 400
	stateMaxChars     = 1200
)

// excerptMessage is a conversation message after tool-noise filtering:
// tool-role messages and tool-call markers are collapsed into toolActivity
// counts attached to the preceding substantive message.
type excerptMessage struct {
	role         string
	content      string
	toolActivity int
}

// buildBrief produces a structured markdown handoff document. Handshake does
// no LLM summarisation itself: the authoritative state comes from the source
// agent's checkpoint summary when present; otherwise the latest substantive
// assistant message stands in for it.
func buildBrief(session *db.Session, messages []*db.Message) string {
	var b strings.Builder

	b.WriteString("# Handoff Brief: " + session.Title + "\n\n")
	b.WriteString(fmt.Sprintf("- **Source agent:** %s\n", session.Agent))
	if session.WorkingDir != "" {
		b.WriteString(fmt.Sprintf("- **Working directory:** %s\n", session.WorkingDir))
	}
	if session.Model != "" {
		b.WriteString(fmt.Sprintf("- **Model:** %s\n", session.Model))
	}
	b.WriteString(fmt.Sprintf("- **Started:** %s\n", time.Unix(session.CreatedAt, 0).Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("- **Last active:** %s\n", time.Unix(session.UpdatedAt, 0).Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("- **Messages:** %d\n\n", len(messages)))

	excerpts := filterForExcerpt(messages)

	if goal := firstUserContent(excerpts); goal != "" {
		b.WriteString("## Original Goal\n\n")
		b.WriteString(clip(goal, goalMaxChars) + "\n\n")
	}

	// Current state: authored by the source agent at checkpoint time when
	// available, otherwise the most recent substantive assistant message.
	if session.Summary != "" {
		b.WriteString("## Current State & Next Steps (written by the source agent)\n\n")
		b.WriteString(strings.TrimSpace(session.Summary) + "\n\n")
	} else if last := lastAssistantContent(excerpts); last != "" {
		b.WriteString("## Latest State (last assistant message)\n\n")
		b.WriteString(clip(last, stateMaxChars) + "\n\n")
	}

	recent := excerpts
	window := recentWindow(len(excerpts))
	if len(recent) > window {
		b.WriteString(fmt.Sprintf("## Recent Conversation\n\n_(last %d of %d substantive messages; tool output omitted)_\n\n", window, len(excerpts)))
		recent = recent[len(recent)-window:]
	} else if len(recent) > 0 {
		b.WriteString("## Recent Conversation\n\n_(tool output omitted)_\n\n")
	}
	for _, msg := range recent {
		b.WriteString(fmt.Sprintf("**%s:** %s\n\n", msg.role, clip(msg.content, excerptMaxChars)))
		if msg.toolActivity > 0 {
			b.WriteString(fmt.Sprintf("_[%d tool interaction(s) omitted]_\n\n", msg.toolActivity))
		}
	}

	b.WriteString("## Instructions for the Receiving Agent\n\n")
	b.WriteString("You are continuing a session that was started in " + session.Agent + ". ")
	if session.WorkingDir != "" {
		b.WriteString("Work in `" + session.WorkingDir + "`. ")
	}
	b.WriteString("Read the goal and current state above, then pick up where the previous agent left off. ")
	b.WriteString("Treat decisions already made in the conversation as settled — do not relitigate them. ")
	b.WriteString("Before making changes, state your understanding of the current state and your intended next step to the user in one or two sentences.\n")

	return b.String()
}

// filterForExcerpt keeps user/assistant messages with real text, strips
// tool-call markers from their content, and folds tool-role messages (and
// messages that were nothing but tool markers) into toolActivity counts.
func filterForExcerpt(messages []*db.Message) []*excerptMessage {
	var out []*excerptMessage
	pendingTools := 0

	for _, msg := range messages {
		if msg.Role == "tool" {
			pendingTools++
			continue
		}
		content := stripToolMarkers(msg.Content)
		if content == "" {
			pendingTools++
			continue
		}
		out = append(out, &excerptMessage{role: msg.Role, content: content})
		if len(out) > 1 {
			out[len(out)-2].toolActivity = pendingTools
		}
		pendingTools = 0
	}
	if len(out) > 0 {
		out[len(out)-1].toolActivity = pendingTools
	}
	return out
}

// stripToolMarkers removes lines that are only tool-call placeholders
// produced by the adapters, e.g. "[tool: Bash]" or "[3 tool result(s)]".
func stripToolMarkers(content string) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[tool: ") && strings.HasSuffix(trimmed, "]") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "tool result(s)]") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// recentWindow scales the excerpt size with session length: an eighth of the
// substantive messages, clamped to [minRecentMessages, maxRecentMessages].
func recentWindow(total int) int {
	window := total / 8
	if window < minRecentMessages {
		return minRecentMessages
	}
	if window > maxRecentMessages {
		return maxRecentMessages
	}
	return window
}

func firstUserContent(messages []*excerptMessage) string {
	for _, msg := range messages {
		if msg.role == "user" {
			return msg.content
		}
	}
	return ""
}

func lastAssistantContent(messages []*excerptMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].role == "assistant" {
			return messages[i].content
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + " […]"
}
