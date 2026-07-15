package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"handshake/internal/db"
)

func TestBuildRestoreContext_InjectsOnlyCurrentVerifiedDocuments(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	workingDir := t.TempDir()
	session := &db.Session{ID: "session-1", Agent: "codex", Title: "Restore", WorkingDir: workingDir, UpdatedAt: 100}
	state, err := RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	writer := NewWriter(database, knowledgeRoot)
	if _, err := writer.WriteProject(session.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}
	for documentType, content := range map[string]string{
		db.KnowledgeDocumentProjectBrief: "Brief body: current objective.",
		db.KnowledgeDocumentRepoMap:      "Repo map body: internal/knowledge.",
	} {
		if _, err := writer.PublishDocument(&DocumentInput{
			ProjectID: session.ProjectID, Type: documentType, FactsRevision: state.FactsRevision,
			Content: content, GeneratedBy: "test",
		}); err != nil {
			t.Fatalf("PublishDocument %s: %v", documentType, err)
		}
	}

	context := BuildRestoreContext(database, knowledgeRoot, session)
	if !strings.Contains(context, "## Project Knowledge") || !strings.Contains(context, "Brief body: current objective.") || !strings.Contains(context, "Repo map body: internal/knowledge.") {
		t.Fatalf("restore context missing current documents:\n%s", context)
	}
	if strings.Contains(context, "diff --git") {
		t.Fatalf("restore context must not inject diffs:\n%s", context)
	}

	briefPath := filepath.Join(knowledgeRoot, session.ProjectID, "project-brief.md")
	if err := os.WriteFile(briefPath, []byte("tampered"), 0600); err != nil {
		t.Fatalf("tamper project brief: %v", err)
	}
	context = BuildRestoreContext(database, knowledgeRoot, session)
	if !strings.Contains(context, "Project Brief could not be verified and was not injected.") {
		t.Fatalf("tampered document was not rejected:\n%s", context)
	}
}

func TestBuildRestoreContext_LeavesStaleDocumentsOut(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	workingDir := t.TempDir()
	first := &db.Session{ID: "session-1", Agent: "codex", Title: "Restore", WorkingDir: workingDir, UpdatedAt: 100}
	state, err := RecordCheckpoint(database, first)
	if err != nil {
		t.Fatalf("first RecordCheckpoint: %v", err)
	}
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	writer := NewWriter(database, knowledgeRoot)
	if _, err := writer.WriteProject(first.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}
	for _, documentType := range []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap} {
		if _, err := writer.PublishDocument(&DocumentInput{
			ProjectID: first.ProjectID, Type: documentType, FactsRevision: state.FactsRevision,
			Content: "Old knowledge must not be injected.", GeneratedBy: "test",
		}); err != nil {
			t.Fatalf("PublishDocument: %v", err)
		}
	}
	second := &db.Session{ID: "session-2", Agent: "codex", Title: "Restore", WorkingDir: workingDir, UpdatedAt: 200}
	if _, err := RecordCheckpoint(database, second); err != nil {
		t.Fatalf("second RecordCheckpoint: %v", err)
	}
	if _, err := writer.WriteProject(second.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}

	context := BuildRestoreContext(database, knowledgeRoot, second)
	if strings.Contains(context, "Old knowledge must not be injected.") || !strings.Contains(context, "Project Brief is not current and was not injected.") {
		t.Fatalf("stale document was injected:\n%s", context)
	}
}
