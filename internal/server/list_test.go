package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"handshake/internal/db"
)

func TestHandleListSessionsReturnsStructuredProjectDetailsAndTextFallback(t *testing.T) {
	homeDir := t.TempDir()
	database, err := db.New(filepath.Join(homeDir, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	workingDir := filepath.Join(homeDir, "code", "payments-worktree")
	updatedAt := time.Now().Add(-5 * time.Minute).Unix()
	checkpoint := &db.KnowledgeCheckpoint{
		Session: &db.Session{
			ID: "session-1", Title: "Fix authentication flow", Agent: "claude-code",
			WorkingDir: workingDir, UpdatedAt: updatedAt,
		},
		Project: &db.Project{
			ID: "project-1", RemoteURL: "github.com/acme/payments-api",
		},
		Instance: &db.ProjectInstance{
			ID: "instance-1", RootPath: filepath.Join(homeDir, "code", "payments-api"),
		},
		Snapshot: &db.GitSnapshot{
			Fingerprint: "snapshot-1", HasGit: true, RemoteURL: "github.com/acme/payments-api",
		},
	}
	if _, err := database.RecordKnowledgeCheckpoint(checkpoint); err != nil {
		t.Fatalf("RecordKnowledgeCheckpoint: %v", err)
	}
	items, err := database.ListSessionItems("claude-code")
	if err != nil || len(items) != 1 || items[0].ProjectRoot != filepath.Join(homeDir, "code", "payments-api") {
		t.Fatalf("ListSessionItems = %+v, %v", items, err)
	}

	server := New("handshake", "test", homeDir, database)
	result, err := server.handleListSessions(mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"agent": "claude-code"},
	}})
	if err != nil || result.IsError {
		t.Fatalf("handleListSessions = %+v, %v", result, err)
	}
	structured, ok := result.StructuredContent.(sessionListResult)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want sessionListResult", result.StructuredContent)
	}
	if len(structured.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(structured.Sessions))
	}
	session := structured.Sessions[0]
	if session.ID != "session-1" || session.Title != "Fix authentication flow" ||
		session.Agent != "claude-code" || session.WorkingDirectory != workingDir {
		t.Fatalf("structured session = %+v", session)
	}
	if session.ProjectName != "payments-api" || session.ProjectID != "project-1" {
		t.Fatalf("structured project = %+v", session)
	}
	if session.UpdatedAt == "" || session.UpdatedRelative == "" {
		t.Fatalf("structured timestamps = %+v", session)
	}

	textContent, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("fallback content type = %T", result.Content[0])
	}
	for _, expected := range []string{
		"| Session | Agent | Updated | Directory | Project | ID |",
		"Fix authentication flow",
		"claude-code",
		"~/code/payments-worktree",
		"payments-api",
		"session-1",
	} {
		if !strings.Contains(textContent.Text, expected) {
			t.Errorf("fallback omitted %q:\n%s", expected, textContent.Text)
		}
	}
}

func TestHandleListSessionsReturnsStructuredEmptyResult(t *testing.T) {
	homeDir := t.TempDir()
	database, err := db.New(filepath.Join(homeDir, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	server := New("handshake", "test", homeDir, database)
	result, err := server.handleListSessions(mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"agent": "claude-code"},
	}})
	if err != nil || result.IsError {
		t.Fatalf("handleListSessions = %+v, %v", result, err)
	}
	structured, ok := result.StructuredContent.(sessionListResult)
	if !ok || len(structured.Sessions) != 0 || structured.Warnings == nil {
		t.Fatalf("empty structured result = %#v (%T)", result.StructuredContent, result.StructuredContent)
	}
}

func TestDeriveProjectNamePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		root       string
		workingDir string
		want       string
	}{
		{name: "remote", remote: "github.com/acme/payments-api", root: "/work/other", workingDir: "/work/subdir", want: "payments-api"},
		{name: "remote git suffix", remote: "github.com/acme/payments-api.git", want: "payments-api"},
		{name: "project root", root: "/work/payments-api", workingDir: "/work/payments-api/subdir", want: "payments-api"},
		{name: "working directory", workingDir: "/work/prototype", want: "prototype"},
		{name: "unknown", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deriveProjectName(test.remote, test.root, test.workingDir); got != test.want {
				t.Fatalf("deriveProjectName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMarkdownCellKeepsSessionRowsValid(t *testing.T) {
	if got := markdownCell("Fix auth | tests\nnext"); got != `Fix auth \| tests next` {
		t.Fatalf("markdownCell = %q", got)
	}
}
