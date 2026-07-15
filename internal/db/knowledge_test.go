package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func newKnowledgeCheckpoint(fingerprint string) *KnowledgeCheckpoint {
	return &KnowledgeCheckpoint{
		Session: &Session{
			ID:         "session-1",
			Title:      "Knowledge foundation",
			Agent:      "claude-code",
			WorkingDir: "/work/project",
			Summary:    "Initial checkpoint",
			Decisions:  "Keep history immutable",
			UpdatedAt:  100,
		},
		Project:  &Project{ID: "project-1", RemoteURL: "https://example.com/project", LocalOnly: false},
		Instance: &ProjectInstance{ID: "instance-1", RootPath: "/work/project"},
		Snapshot: &GitSnapshot{
			Fingerprint:     fingerprint,
			HasGit:          true,
			Commit:          "abc123",
			Branch:          "main",
			Summary:         "Initial checkpoint",
			Decisions:       "Keep history immutable",
			SourceUpdatedAt: 100,
			CapturedAt:      100,
		},
	}
}

func TestRecordKnowledgeCheckpoint_TracksRevisionAndFreshness(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	state, err := database.RecordKnowledgeCheckpoint(newKnowledgeCheckpoint("snapshot-a"))
	if err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	if state.FactsRevision != 1 || state.AIRevision != 0 || state.Status != KnowledgeRefreshPending {
		t.Fatalf("first state = %+v", state)
	}

	state, err = database.RecordKnowledgeCheckpoint(newKnowledgeCheckpoint("snapshot-a"))
	if err != nil {
		t.Fatalf("repeat checkpoint: %v", err)
	}
	if state.FactsRevision != 1 || state.LastSnapshotID == 0 {
		t.Fatalf("unchanged checkpoint advanced state: %+v", state)
	}

	state, err = database.MarkKnowledgeCurrent("project-1")
	if err != nil {
		t.Fatalf("MarkKnowledgeCurrent: %v", err)
	}
	if state.FactsRevision != 1 || state.AIRevision != 1 || state.Status != KnowledgeCurrent {
		t.Fatalf("current state = %+v", state)
	}

	next := newKnowledgeCheckpoint("snapshot-b")
	next.Snapshot.Commit = "def456"
	next.Session.UpdatedAt = 200
	next.Snapshot.SourceUpdatedAt = 200
	state, err = database.RecordKnowledgeCheckpoint(next)
	if err != nil {
		t.Fatalf("changed checkpoint: %v", err)
	}
	if state.FactsRevision != 2 || state.AIRevision != 1 || state.Status != KnowledgeFactsAIStale {
		t.Fatalf("stale state = %+v", state)
	}

	session, err := database.GetSession("session-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.ProjectID != "project-1" {
		t.Fatalf("ProjectID = %q, want project-1", session.ProjectID)
	}

	var snapshots int
	if err := database.db.QueryRow("SELECT COUNT(*) FROM git_snapshots WHERE project_id = ?", "project-1").Scan(&snapshots); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshots != 2 {
		t.Fatalf("snapshot count = %d, want 2", snapshots)
	}
}

func TestGetKnowledgeState_NotFound(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	_, err = database.GetKnowledgeState("missing")
	if !errors.Is(err, ErrKnowledgeStateNotFound) {
		t.Fatalf("GetKnowledgeState error = %v, want ErrKnowledgeStateNotFound", err)
	}
}

func TestStoreKnowledgeDocument_RequiresBothDocumentsAndInvalidatesOlderFacts(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	state, err := database.RecordKnowledgeCheckpoint(newKnowledgeCheckpoint("snapshot-a"))
	if err != nil {
		t.Fatalf("RecordKnowledgeCheckpoint: %v", err)
	}
	brief := &KnowledgeDocument{
		ProjectID: "project-1", Path: "project-brief.md", Type: KnowledgeDocumentProjectBrief,
		FactsRevision: state.FactsRevision, GeneratedBy: "claude-code",
	}
	state, err = database.StoreKnowledgeDocument(brief)
	if err != nil || state.Status != KnowledgeRefreshPending {
		t.Fatalf("store brief = %+v, %v", state, err)
	}
	mapDocument := &KnowledgeDocument{
		ProjectID: "project-1", Path: "repo-map.md", Type: KnowledgeDocumentRepoMap,
		FactsRevision: state.FactsRevision, GeneratedBy: "codex",
	}
	state, err = database.StoreKnowledgeDocument(mapDocument)
	if err != nil || state.Status != KnowledgeCurrent || state.AIRevision != state.FactsRevision {
		t.Fatalf("store repo map = %+v, %v", state, err)
	}

	next := newKnowledgeCheckpoint("snapshot-b")
	next.Snapshot.Commit = "def456"
	next.Session.UpdatedAt = 200
	next.Snapshot.SourceUpdatedAt = 200
	state, err = database.RecordKnowledgeCheckpoint(next)
	if err != nil || state.Status != KnowledgeFactsAIStale {
		t.Fatalf("next checkpoint = %+v, %v", state, err)
	}
	documents, err := database.ListKnowledgeDocuments("project-1")
	if err != nil || len(documents) != 2 {
		t.Fatalf("ListKnowledgeDocuments = %d, %v", len(documents), err)
	}
	for _, document := range documents {
		if document.Status != KnowledgeDocumentStale {
			t.Fatalf("document %s status = %q, want stale", document.Path, document.Status)
		}
	}
	brief.FactsRevision = 1
	if _, err := database.StoreKnowledgeDocument(brief); !errors.Is(err, ErrKnowledgeFactsStale) {
		t.Fatalf("stale document error = %v, want ErrKnowledgeFactsStale", err)
	}
}

func TestRecordKnowledgeCheckpoint_ConcurrentWritesAdvanceEveryRevision(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	const count = 20
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			checkpoint := newKnowledgeCheckpoint(fmt.Sprintf("snapshot-%d", i))
			checkpoint.Session.ID = fmt.Sprintf("session-%d", i)
			checkpoint.Session.UpdatedAt = int64(i + 1)
			checkpoint.Snapshot.SourceUpdatedAt = int64(i + 1)
			if _, err := database.RecordKnowledgeCheckpoint(checkpoint); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("RecordKnowledgeCheckpoint: %v", err)
	}

	state, err := database.GetKnowledgeState("project-1")
	if err != nil {
		t.Fatalf("GetKnowledgeState: %v", err)
	}
	if state.FactsRevision != count || state.LastSnapshotID == 0 {
		t.Fatalf("concurrent state = %+v, want %d revisions", state, count)
	}
}

func TestNew_MigratesLegacySessionsWithProjectColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, title TEXT NOT NULL, agent TEXT NOT NULL,
		working_dir TEXT, model TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create legacy sessions: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO sessions VALUES ('old', 'Old session', 'codex', '/tmp/old', '', 1, 1)`); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	database, err := New(path)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	defer database.Close()
	session, err := database.GetSession("old")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if session.ProjectID != "" || session.Summary != "" || session.Decisions != "" {
		t.Fatalf("legacy session migration changed data: %+v", session)
	}
}
