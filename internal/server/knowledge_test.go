package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"handshake/internal/db"
	"handshake/internal/knowledge"
)

func TestKnowledgeAuthoringCheck_TracksDocumentFreshness(t *testing.T) {
	homeDir := t.TempDir()
	database, err := db.New(filepath.Join(homeDir, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	workingDir := t.TempDir()
	session := &db.Session{ID: "session-1", Agent: "claude-code", Title: "Knowledge", WorkingDir: workingDir, UpdatedAt: 100}
	state, err := knowledge.RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	server := New("handshake", "test", homeDir, database)

	response := callKnowledgeAuthoringCheck(t, server, workingDir)
	if !response.Pending || response.ProjectID != session.ProjectID || response.FactsRevision != state.FactsRevision {
		t.Fatalf("pending response = %+v", response)
	}

	writer := knowledge.NewWriter(database, filepath.Join(homeDir, ".handshake", "knowledge"))
	for _, documentType := range []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap} {
		if _, err := writer.PublishDocument(&knowledge.DocumentInput{
			ProjectID: session.ProjectID, Type: documentType, FactsRevision: state.FactsRevision,
			Content: "Current factual knowledge.", GeneratedBy: "test",
		}); err != nil {
			t.Fatalf("PublishDocument %s: %v", documentType, err)
		}
	}
	response = callKnowledgeAuthoringCheck(t, server, workingDir)
	if response.Pending || response.Status != db.KnowledgeCurrent {
		t.Fatalf("current response = %+v", response)
	}

	result, err := server.handleBriefRequest(mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"title": session.ID, "confirmed": true},
	}}, true)
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("confirmed restore = %+v, %v", result, err)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "## Project Knowledge") || !strings.Contains(text.Text, "Current factual knowledge.") {
		t.Fatalf("confirmed restore omitted project knowledge: %+v", result.Content)
	}
}

func callKnowledgeAuthoringCheck(t *testing.T, server *Server, workingDir string) knowledgeAuthoringCheckResponse {
	t.Helper()
	body, err := json.Marshal(knowledgeAuthoringCheckPayload{WorkingDir: workingDir})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/knowledge-authoring-check", bytes.NewReader(body))
	server.handleKnowledgeAuthoringCheck(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("check status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response knowledgeAuthoringCheckResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}
