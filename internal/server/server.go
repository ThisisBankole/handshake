package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"handshake/internal/adapters"
	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/git"
)

// Server bundles the canonical database, the MCP tool surface, and the plain
// HTTP ingest endpoint into one process listening on a single address.
type Server struct {
	db      *db.Database
	mcp     *mcpserver.MCPServer
	engine  *engine.BriefGenerator
	homeDir string
}

func New(name, version, homeDir string, database *db.Database) *Server {
	s := &Server{
		db:      database,
		mcp:     mcpserver.NewMCPServer(name, version),
		engine:  engine.NewBriefGenerator(database),
		homeDir: homeDir,
	}
	s.addTools()
	return s
}

// Serve blocks, listening on addr. MCP is served on /mcp, session ingestion
// on /ingest, health on /health.
func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(s.mcp))
	mux.HandleFunc("/ingest", s.handleIngest)
	mux.HandleFunc("/generate-brief", s.handleGenerateBrief)
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	return http.ListenAndServe(addr, mux)
}

func (s *Server) addTools() {
	s.mcp.AddTool(
		mcp.NewTool("checkpoint_session",
			mcp.WithDescription("Save the current session into Handshake so it can be restored from another agent. Reads the full conversation from the agent's native session storage."),
			mcp.WithString("session_id",
				mcp.Description("The agent's native session ID"),
				mcp.Required(),
			),
			mcp.WithString("agent",
				mcp.Description("Which agent this session belongs to"),
				mcp.Enum("claude-code", "opencode", "hermes", "codex"),
				mcp.Required(),
			),
			mcp.WithString("title",
				mcp.Description("Optional human-readable title; overrides the session's native title"),
			),
			mcp.WithString("summary",
				mcp.Description("Strongly recommended: a handoff summary written by you, the checkpointing agent — current state (what is done and verified, what is broken), and concrete next steps in order. This becomes the core of the handoff brief."),
			),
			mcp.WithString("decisions",
				mcp.Description("Key decisions made during this session that must not be relitigated, one per line — e.g. 'Use JWT over sessions\\nPostgres not SQLite'. Rendered as a dedicated Settled Decisions section in the handoff brief."),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleCheckpointSession(request)
		},
	)

	s.mcp.AddTool(
		mcp.NewTool("list_sessions",
			mcp.WithDescription("List recent checkpointed sessions as human-readable titles with relative timestamps"),
			mcp.WithString("agent",
				mcp.Description("Filter by agent type (optional)"),
				mcp.Enum("claude-code", "opencode", "hermes"),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleListSessions(request)
		},
	)

	s.mcp.AddTool(
		mcp.NewTool("restore_session",
			mcp.WithDescription("Restore a session by title (fuzzy matched). First call returns a restore packet showing git drift since checkpoint. Call again with confirmed=true to inject the handoff brief."),
			mcp.WithString("title",
				mcp.Description("Title (or part of the title) of the session to restore"),
				mcp.Required(),
			),
			mcp.WithBoolean("confirmed",
				mcp.Description("Set to true after reviewing the restore packet to inject the handoff brief"),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleBriefRequest(request, true)
		},
	)

	s.mcp.AddTool(
		mcp.NewTool("generate_brief",
			mcp.WithDescription("Generate a handoff brief for a session without restoring it — for preview"),
			mcp.WithString("title",
				mcp.Description("Title (or part of the title) of the session"),
				mcp.Required(),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleBriefRequest(request, false)
		},
	)

	s.mcp.AddTool(
		mcp.NewTool("search_session",
			mcp.WithDescription("Full-text search within a specific session's messages. Use this when the handoff brief doesn't contain the context you need — search for specific decisions, file names, error messages, or topics discussed in that session."),
			mcp.WithString("title",
				mcp.Description("Title (or part of the title) of the session to search within"),
				mcp.Required(),
			),
			mcp.WithString("query",
				mcp.Description("Search terms — words or phrases to find in the session messages"),
				mcp.Required(),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results to return (default 5)"),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleSearchSession(request)
		},
	)

	s.mcp.AddTool(
		mcp.NewTool("search_all_sessions",
			mcp.WithDescription("Full-text search across all checkpointed sessions. Use this to find which session covered a topic, or to retrieve context from a session you haven't identified yet."),
			mcp.WithString("query",
				mcp.Description("Search terms — words or phrases to find across all session messages"),
				mcp.Required(),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results to return (default 10)"),
			),
			mcp.WithString("agent",
				mcp.Description("Filter results to a specific agent (optional)"),
				mcp.Enum("claude-code", "opencode", "hermes", "codex"),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return s.handleSearchAllSessions(request)
		},
	)
}

func (s *Server) handleCheckpointSession(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID := request.GetString("session_id", "")
	agent := request.GetString("agent", "")
	title := request.GetString("title", "")
	summary := request.GetString("summary", "")
	decisions := request.GetString("decisions", "")
	if sessionID == "" || agent == "" {
		return mcp.NewToolResultError("session_id and agent are required"), nil
	}

	sessionData, err := s.readNativeSession(agent, sessionID)
	if err != nil {
		// Native storage unavailable — fall back to a metadata-only checkpoint.
		if title == "" {
			title = agent + " session " + sessionID
		}
		if storeErr := s.db.StoreSession(&db.Session{ID: sessionID, Title: title, Agent: agent, Summary: summary, Decisions: decisions}); storeErr != nil {
			return mcp.NewToolResultError(storeErr.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"Session '%s' checkpointed (metadata only — could not read native storage: %v)", title, err)), nil
	}

	if title != "" {
		sessionData.Title = title
	}
	sessionData.Summary = summary
	sessionData.Decisions = decisions

	// Capture git state from the session's working directory.
	// Silently skipped if the directory has no git repo or git isn't installed.
	if sessionData.WorkingDir != "" {
		if state, err := git.CaptureState(sessionData.WorkingDir); err == nil && state != nil {
			encoded, jsonErr := json.Marshal(state)
			if jsonErr == nil {
				sessionData.GitState = string(encoded)
			}
		}
	}

	if err := adapters.Ingest(s.db, sessionData); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Session '%s' checkpointed (%d messages). Restore it from another agent with restore_session(\"%s\").",
		sessionData.Title, len(sessionData.Messages), sessionData.Title)), nil
}

func (s *Server) readNativeSession(agent, sessionID string) (*adapters.SessionData, error) {
	switch agent {
	case "claude-code":
		return adapters.NewClaudeCodeAdapter(s.homeDir).ReadSession("", sessionID)
	case "opencode":
		return adapters.NewOpenCodeAdapter(s.homeDir).ReadSession(sessionID)
	case "hermes":
		return adapters.NewHermesAdapter(s.homeDir).ReadSession(sessionID)
	case "codex":
		return adapters.NewCodexAdapter(s.homeDir).ReadSession(sessionID)
	default:
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
}

func (s *Server) handleListSessions(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agent := request.GetString("agent", "")

	if agent == "" || agent == "codex" {
		adapters.IngestCodexSessions(s.db, s.homeDir)
	}

	sessions, err := s.db.ListSessions(agent)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(sessions) == 0 {
		return mcp.NewToolResultText("No sessions found"), nil
	}

	var b strings.Builder
	b.WriteString("Recent sessions:\n")
	for _, session := range sessions {
		fmt.Fprintf(&b, "- %s (%s, %s)\n", session.Title, session.Agent, relativeTime(session.UpdatedAt))
	}
	return mcp.NewToolResultText(b.String()), nil
}

func (s *Server) handleBriefRequest(request mcp.CallToolRequest, restore bool) (*mcp.CallToolResult, error) {
	title := request.GetString("title", "")
	if title == "" {
		return mcp.NewToolResultError("title is required"), nil
	}

	confirmed := request.GetBool("confirmed", false)

	session, alternatives, err := s.findSessionByTitle(title)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// On the first restore call, show the drift packet before injecting the brief.
	if restore && !confirmed && session.GitState != "" {
		packet := s.buildRestorePacket(session)
		if packet != "" {
			var b strings.Builder
			b.WriteString(packet)
			b.WriteString("\n\nReview the drift above, then call restore_session again with confirmed=true to inject the handoff brief.")
			if len(alternatives) > 0 {
				b.WriteString("\n_Note: other sessions also matched: " + strings.Join(alternatives, "; ") + "_")
			}
			return mcp.NewToolResultText(b.String()), nil
		}
	}

	brief, err := s.engine.GenerateBrief(session.ID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var b strings.Builder
	if restore {
		fmt.Fprintf(&b, "Restoring session '%s'. Continue the work described in this handoff brief:\n\n", session.Title)
	}
	b.WriteString(brief.Brief)
	if len(alternatives) > 0 {
		b.WriteString("\n_Note: other sessions also matched: " + strings.Join(alternatives, "; ") + "_\n")
	}
	return mcp.NewToolResultText(b.String()), nil
}

// buildRestorePacket decodes the stored git state for a session, captures
// the current state of the working directory, and formats the drift packet.
// Returns an empty string if git state is missing or capture fails.
func (s *Server) buildRestorePacket(session *db.Session) string {
	var checkpoint git.State
	if err := json.Unmarshal([]byte(session.GitState), &checkpoint); err != nil {
		return ""
	}
	workingDir := session.WorkingDir
	if workingDir == "" {
		workingDir = s.homeDir
	}
	return git.BuildRestorePacket(session.Title, session.Agent, session.UpdatedAt, workingDir, &checkpoint, session.Summary, session.Decisions)
}

// findSessionByTitle fuzzy-matches a title against checkpointed sessions:
// exact match first, then substring, then all-words. Among equal matches the
// most recently updated session wins; the rest are reported as alternatives.
func (s *Server) findSessionByTitle(query string) (*db.Session, []string, error) {
	sessions, err := s.db.ListSessions("")
	if err != nil {
		return nil, nil, err
	}

	q := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(q)

	var exact, substr, allWords []*db.Session
	for _, session := range sessions {
		t := strings.ToLower(session.Title)
		switch {
		case t == q:
			exact = append(exact, session)
		case strings.Contains(t, q):
			substr = append(substr, session)
		case containsAllWords(t, words):
			allWords = append(allWords, session)
		}
	}

	matches := exact
	if len(matches) == 0 {
		matches = substr
	}
	if len(matches) == 0 {
		matches = allWords
	}
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("no session matching %q — use list_sessions to see what is available", query)
	}

	// ListSessions orders by updated_at DESC, so matches[0] is the most recent.
	var alternatives []string
	for _, m := range matches[1:] {
		alternatives = append(alternatives, fmt.Sprintf("%s (%s, %s)", m.Title, m.Agent, relativeTime(m.UpdatedAt)))
	}
	return matches[0], alternatives, nil
}

func containsAllWords(title string, words []string) bool {
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(title, w) {
			return false
		}
	}
	return true
}

func (s *Server) handleSearchSession(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := request.GetString("title", "")
	query := request.GetString("query", "")
	limit := request.GetInt("limit", 5)
	if title == "" || query == "" {
		return mcp.NewToolResultError("title and query are required"), nil
	}

	session, _, err := s.findSessionByTitle(title)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	results, err := s.db.SearchMessages(session.ID, query, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v — check query syntax", err)), nil
	}
	if len(results) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No results for %q in session %q", query, session.Title)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Search: %q in %q — %d result(s)\n\n", query, session.Title, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "[%s]\n%s\n\n", r.Role, clipSearchResult(r.Content))
	}
	return mcp.NewToolResultText(strings.TrimSpace(b.String())), nil
}

func (s *Server) handleSearchAllSessions(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := request.GetString("query", "")
	limit := request.GetInt("limit", 10)
	agent := request.GetString("agent", "")
	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}

	results, err := s.db.SearchAllMessages(query, limit, agent)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v — check query syntax", err)), nil
	}
	if len(results) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No results for %q", query)), nil
	}

	var b strings.Builder
	label := "all sessions"
	if agent != "" {
		label = agent + " sessions"
	}
	fmt.Fprintf(&b, "Search: %q across %s — %d result(s)\n\n", query, label, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "[%s / %s / %s]\n%s\n\n", r.SessionTitle, r.SessionAgent, r.Role, clipSearchResult(r.Content))
	}
	return mcp.NewToolResultText(strings.TrimSpace(b.String())), nil
}

// clipSearchResult trims tool noise from a message and clips it to a readable length.
func clipSearchResult(content string) string {
	// Strip the same tool markers that the brief engine removes.
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[tool: ") && strings.HasSuffix(t, "]") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "tool result(s)]") {
			continue
		}
		if strings.HasPrefix(t, "[IMPORTANT:") {
			continue
		}
		kept = append(kept, line)
	}
	clean := strings.TrimSpace(strings.Join(kept, "\n"))
	runes := []rune(clean)
	if len(runes) <= 500 {
		return clean
	}
	return string(runes[:500]) + " […]"
}

func relativeTime(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
