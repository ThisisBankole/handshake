package knowledge

import (
	"os/exec"
	"path/filepath"
	"testing"

	"handshake/internal/db"
)

func initRepository(t *testing.T, dir, remote string) {
	t.Helper()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}

func TestResolveProject_RemoteSharesProjectButNotInstance(t *testing.T) {
	remote := "git@github.com:Example/Project.git"
	first := t.TempDir()
	second := t.TempDir()
	initRepository(t, first, remote)
	initRepository(t, second, remote)

	firstIdentity, err := ResolveProject(first, remote)
	if err != nil {
		t.Fatalf("ResolveProject first: %v", err)
	}
	secondIdentity, err := ResolveProject(second, "https://github.com/Example/Project.git")
	if err != nil {
		t.Fatalf("ResolveProject second: %v", err)
	}
	if firstIdentity.ProjectID != secondIdentity.ProjectID {
		t.Fatalf("project IDs differ: %q and %q", firstIdentity.ProjectID, secondIdentity.ProjectID)
	}
	if firstIdentity.InstanceID == secondIdentity.InstanceID {
		t.Fatal("different clones must have different instance IDs")
	}
	if firstIdentity.LocalOnly || secondIdentity.LocalOnly {
		t.Fatal("remote repositories must not be local-only")
	}
}

func TestResolveProject_NonGitDirectoryIsLocalOnly(t *testing.T) {
	dir := t.TempDir()
	identity, err := ResolveProject(dir, "")
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	if !identity.LocalOnly || identity.RemoteURL != "" {
		t.Fatalf("non-Git identity = %+v", identity)
	}
	wantRoot, err := canonicalPath(dir)
	if err != nil {
		t.Fatalf("canonicalPath: %v", err)
	}
	if identity.RootPath != wantRoot {
		t.Fatalf("RootPath = %q, want %q", identity.RootPath, wantRoot)
	}
}

func TestRecordCheckpoint_CapturesGitAndMarksProjectPending(t *testing.T) {
	repo := t.TempDir()
	initRepository(t, repo, "https://github.com/example/project.git")
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	session := &db.Session{
		ID:         "session-1",
		Agent:      "codex",
		Title:      "Capture Git",
		WorkingDir: repo,
		Summary:    "Repository state captured",
		UpdatedAt:  100,
	}
	state, err := RecordCheckpoint(database, session)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	if state.Status != db.KnowledgeRefreshPending || state.FactsRevision != 1 {
		t.Fatalf("knowledge state = %+v", state)
	}
	if session.GitState == "" || session.ProjectID == "" {
		t.Fatalf("session was not enriched: %+v", session)
	}

	stored, err := database.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.ProjectID != session.ProjectID || stored.GitState == "" {
		t.Fatalf("stored session = %+v", stored)
	}
}

func TestRecordCheckpoint_PreservesCheckpointSummaryInSnapshot(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	first := &db.Session{
		ID: "session-1", Agent: "claude-code", Title: "Preserve summary",
		WorkingDir: dir, Summary: "Producer summary", Decisions: "Use SQLite", UpdatedAt: 100,
	}
	if _, err := RecordCheckpoint(database, first); err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	// The retitled sync omits summary and decisions; hydration must carry
	// them into the new snapshot.
	second := &db.Session{
		ID: "session-1", Agent: "claude-code", Title: "Preserve summary (retitled)",
		WorkingDir: dir, UpdatedAt: 200,
	}
	if _, err := RecordCheckpoint(database, second); err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}

	snapshots, err := database.ListGitSnapshots(second.ProjectID, 0)
	if err != nil {
		t.Fatalf("ListGitSnapshots: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
	if snapshots[1].Summary != "Producer summary" || snapshots[1].Decisions != "Use SQLite" {
		t.Fatalf("latest snapshot lost source metadata: %+v", snapshots[1])
	}
}

func TestRecordCheckpoint_TimestampOnlyCheckpointDoesNotBumpFacts(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	first := &db.Session{
		ID: "session-1", Agent: "claude-code", Title: "Conversation only",
		WorkingDir: dir, Summary: "Producer summary", UpdatedAt: 100,
	}
	initial, err := RecordCheckpoint(database, first)
	if err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}

	// A conversation-only turn: nothing changed except time. This must not
	// create a snapshot or bump facts_revision, or AI documents go stale on
	// every agent response.
	repeat := &db.Session{
		ID: "session-1", Agent: "claude-code", Title: "Conversation only",
		WorkingDir: dir, Summary: "Producer summary", UpdatedAt: 200,
	}
	state, err := RecordCheckpoint(database, repeat)
	if err != nil {
		t.Fatalf("repeat checkpoint: %v", err)
	}
	if state.FactsRevision != initial.FactsRevision {
		t.Fatalf("timestamp-only checkpoint bumped facts_revision: %d -> %d", initial.FactsRevision, state.FactsRevision)
	}
	snapshots, err := database.ListGitSnapshots(repeat.ProjectID, 0)
	if err != nil {
		t.Fatalf("ListGitSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}

	// A real change (new summary) still advances the facts.
	material := &db.Session{
		ID: "session-1", Agent: "claude-code", Title: "Conversation only",
		WorkingDir: dir, Summary: "Implemented the parser", UpdatedAt: 300,
	}
	state, err = RecordCheckpoint(database, material)
	if err != nil {
		t.Fatalf("material checkpoint: %v", err)
	}
	if state.FactsRevision != initial.FactsRevision+1 {
		t.Fatalf("material checkpoint did not bump facts_revision: %+v", state)
	}
}

func TestGetProjectContext_ReturnsExistingProjectFacts(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	session := &db.Session{ID: "session-1", Agent: "codex", Title: "Context", WorkingDir: dir, UpdatedAt: 100}
	if _, err := RecordCheckpoint(database, session); err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}

	context, err := GetProjectContext(database, dir)
	if err != nil {
		t.Fatalf("GetProjectContext: %v", err)
	}
	if context.ProjectID != session.ProjectID || context.State.FactsRevision != 1 {
		t.Fatalf("context = %+v, want project %q at revision 1", context, session.ProjectID)
	}
}
