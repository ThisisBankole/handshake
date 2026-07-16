package authoring

import (
	"context"
	"encoding/json"
	"errors"
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

func TestWorkerSkipsFallbackWhenActiveAgentPublishedDuringGrace(t *testing.T) {
	home := t.TempDir()
	database, err := db.New(filepath.Join(home, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	session := &db.Session{ID: "session-active", Agent: "test", Title: "Test", WorkingDir: t.TempDir(), CreatedAt: 1, UpdatedAt: 2}
	state, err := knowledge.RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	root := filepath.Join(home, ".handshake", "knowledge")
	if _, err := knowledge.NewWriter(database, root).WriteProject(session.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}
	if err := publishCurrentDocuments(database, root, session.ProjectID); err != nil {
		t.Fatalf("publishCurrentDocuments: %v", err)
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
	invoker := &unexpectedInvoker{}
	NewWorker(database, home, invoker).RunOnce(context.Background())

	if invoker.calls != 0 {
		t.Fatalf("fallback invoker was called %d time(s)", invoker.calls)
	}
	if _, err := database.GetKnowledgeAuthoringJob(session.ProjectID); !errors.Is(err, db.ErrKnowledgeAuthoringJobNotFound) {
		t.Fatalf("job should be completed after active-agent publication: %v", err)
	}
}

func TestScheduleQueuesFallbackOnlyWhenEnabled(t *testing.T) {
	home := t.TempDir()
	database, err := db.New(filepath.Join(home, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	session := &db.Session{ID: "session-schedule", Agent: "test", Title: "Test", WorkingDir: t.TempDir(), CreatedAt: 1, UpdatedAt: 2}
	state, err := knowledge.RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	if err := Schedule(database, home, session.ProjectID, state); err != nil {
		t.Fatalf("Schedule disabled: %v", err)
	}
	if _, err := database.GetKnowledgeAuthoringJob(session.ProjectID); !errors.Is(err, db.ErrKnowledgeAuthoringJobNotFound) {
		t.Fatalf("disabled writer should not queue a fallback job: %v", err)
	}

	config := DefaultConfig()
	config.Enabled = true
	config.Runner = RunnerOpenCode
	if err := SaveConfig(home, config); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	before := time.Now().Unix()
	if err := Schedule(database, home, session.ProjectID, state); err != nil {
		t.Fatalf("Schedule enabled: %v", err)
	}
	job, err := database.GetKnowledgeAuthoringJob(session.ProjectID)
	if err != nil {
		t.Fatalf("GetKnowledgeAuthoringJob: %v", err)
	}
	if job.NotBefore < before+int64(defaultActiveAgentGrace.Seconds())-1 {
		t.Fatalf("fallback job was not delayed for active-agent grace: not_before=%d", job.NotBefore)
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
	return publishCurrentDocuments(invoker.database, invoker.root, invoker.projectID)
}

func publishCurrentDocuments(database *db.Database, root, projectID string) error {
	state, err := database.GetKnowledgeState(projectID)
	if err != nil {
		return err
	}
	writer := knowledge.NewWriter(database, root)
	for _, documentType := range []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap} {
		if _, err := writer.PublishDocument(&knowledge.DocumentInput{ProjectID: projectID, Type: documentType, FactsRevision: state.FactsRevision, Content: "Current generated document.", GeneratedBy: "test"}); err != nil {
			return err
		}
	}
	return nil
}

type unexpectedInvoker struct{ calls int }

func (invoker *unexpectedInvoker) Invoke(_ context.Context, _ Runner, _ string, _ string) error {
	invoker.calls++
	return errors.New("fallback invoker should not run")
}
