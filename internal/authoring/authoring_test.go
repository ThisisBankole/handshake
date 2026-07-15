package authoring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handshake/internal/db"
	"handshake/internal/knowledge"
)

func TestBuildCommandUsesNonInteractiveEntrypoints(t *testing.T) {
	tests := []struct {
		runner Runner
		want   []string
	}{
		{RunnerClaude, []string{"-p", "prompt"}},
		{RunnerCodex, []string{"exec", "prompt"}},
		{RunnerOpenCode, []string{"run", "--auto", "prompt"}},
		{RunnerHermes, []string{"-z", "prompt"}},
	}
	for _, test := range tests {
		_, args, err := BuildCommand(test.runner, "prompt")
		if err != nil || strings.Join(args, "|") != strings.Join(test.want, "|") {
			t.Fatalf("BuildCommand(%s) = %v, %v", test.runner, args, err)
		}
	}
}

func TestWorkerCompletesJobOnlyAfterDocumentsArePublished(t *testing.T) {
	home := t.TempDir()
	database, err := db.New(filepath.Join(home, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	session := &db.Session{ID: "session-1", Agent: "test", Title: "Test", WorkingDir: t.TempDir(), CreatedAt: 1, UpdatedAt: 2}
	state, err := knowledge.RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	root := filepath.Join(home, ".handshake", "knowledge")
	if _, err := knowledge.NewWriter(database, root).WriteProject(session.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}
	if err := database.EnqueueKnowledgeAuthoring(session.ProjectID, state.FactsRevision, time.Now()); err != nil {
		t.Fatalf("EnqueueKnowledgeAuthoring: %v", err)
	}
	config := DefaultConfig()
	config.Enabled = true
	config.Runner = RunnerOpenCode
	if err := SaveConfig(home, config); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	worker := NewWorker(database, home, publishingInvoker{database: database, root: root, projectID: session.ProjectID})
	worker.RunOnce(context.Background())

	updated, err := database.GetKnowledgeState(session.ProjectID)
	if err != nil || updated.Status != db.KnowledgeCurrent {
		t.Fatalf("knowledge state = %+v, %v", updated, err)
	}
	if _, err := database.GetKnowledgeAuthoringJob(session.ProjectID); err == nil {
		t.Fatal("job was not completed")
	}
}

func TestCLIInvokerWritesClaudeMCPConfig(t *testing.T) {
	invoker := CLIInvoker{MCPURL: "http://localhost:9876/mcp", RuntimeDir: t.TempDir()}
	path, err := invoker.writeClaudeMCPConfig()
	if err != nil {
		t.Fatalf("writeClaudeMCPConfig: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers = %#v", config["mcpServers"])
	}
	handshake, ok := servers["handshake"].(map[string]any)
	if !ok || handshake["url"] != "http://localhost:9876/mcp" {
		t.Fatalf("handshake config = %#v", servers["handshake"])
	}
}

type publishingInvoker struct {
	database  *db.Database
	root      string
	projectID string
}

func (invoker publishingInvoker) Invoke(_ context.Context, _ Runner, _ string, _ string) error {
	// This stands in for the MCP publication the real CLI must perform.
	state, err := invoker.database.GetKnowledgeState(invoker.projectID)
	if err != nil {
		return err
	}
	writer := knowledge.NewWriter(invoker.database, invoker.root)
	for _, documentType := range []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap} {
		if _, err := writer.PublishDocument(&knowledge.DocumentInput{ProjectID: invoker.projectID, Type: documentType, FactsRevision: state.FactsRevision, Content: "Current generated document.", GeneratedBy: "test"}); err != nil {
			return err
		}
	}
	return nil
}
