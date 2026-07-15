package knowledge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"handshake/internal/db"
)

func TestWriter_WritesDeterministicBundleOncePerRevision(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	project := t.TempDir()
	session := &db.Session{
		ID: "session-one", Agent: "codex", Title: "Bundle facts", WorkingDir: project,
		Summary: "The factual checkpoint is stored.", Decisions: "Keep documents private", UpdatedAt: 100,
	}
	if _, err := RecordCheckpoint(database, session); err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}
	bundleRoot := filepath.Join(t.TempDir(), "knowledge")
	writer := NewWriter(database, bundleRoot)
	written, err := writer.WriteProject(session.ProjectID)
	if err != nil || !written {
		t.Fatalf("first WriteProject = %v, %v", written, err)
	}
	written, err = writer.WriteProject(session.ProjectID)
	if err != nil || written {
		t.Fatalf("repeat WriteProject = %v, %v", written, err)
	}

	indexPath := filepath.Join(bundleRoot, session.ProjectID, "index.md")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(index), "facts_revision: 1") || !strings.Contains(string(index), "[Git timeline](git-timeline.md)") {
		t.Fatalf("index missing deterministic metadata or links:\n%s", index)
	}
	info, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("index permissions = %#o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, session.ProjectID, "sessions", session.ID, "timeline.md")); err != nil {
		t.Fatalf("session timeline missing: %v", err)
	}
}

func TestWriter_WritesCommittedPerFileDiff(t *testing.T) {
	repo := newCommittedRepository(t)
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	bundleRoot := filepath.Join(t.TempDir(), "knowledge")

	first := &db.Session{ID: "session-one", Agent: "codex", Title: "First", WorkingDir: repo, UpdatedAt: 100}
	if _, err := RecordCheckpoint(database, first); err != nil {
		t.Fatalf("first RecordCheckpoint: %v", err)
	}
	if _, err := NewWriter(database, bundleRoot).WriteProject(first.ProjectID); err != nil {
		t.Fatalf("write first bundle: %v", err)
	}
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() { println(\"after\") }\n")
	runGit(t, repo, "add", "main.go")
	runGit(t, repo, "commit", "-m", "change main")

	second := &db.Session{ID: "session-two", Agent: "claude-code", Title: "Second", WorkingDir: repo, UpdatedAt: 200}
	if _, err := RecordCheckpoint(database, second); err != nil {
		t.Fatalf("second RecordCheckpoint: %v", err)
	}
	if written, err := NewWriter(database, bundleRoot).WriteProject(second.ProjectID); err != nil || !written {
		t.Fatalf("write second bundle = %v, %v", written, err)
	}

	snapshots, err := database.ListGitSnapshots(second.ProjectID, 0)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("ListGitSnapshots = %d, %v", len(snapshots), err)
	}
	diffDir := filepath.Join(bundleRoot, second.ProjectID, "diffs", strconv.FormatInt(snapshots[1].ID, 10))
	entries, err := os.ReadDir(diffDir)
	if err != nil {
		t.Fatalf("read diff directory: %v", err)
	}
	var diff string
	for _, entry := range entries {
		if entry.Name() != "index.md" {
			content, readErr := os.ReadFile(filepath.Join(diffDir, entry.Name()))
			if readErr != nil {
				t.Fatalf("read diff: %v", readErr)
			}
			diff = string(content)
		}
	}
	if !strings.Contains(diff, "```diff") || !strings.Contains(diff, "+func main() { println(\"after\") }") {
		t.Fatalf("per-file diff was not formatted or complete:\n%s", diff)
	}
}

func TestWriter_OmitsSensitiveWorkingTreeFiles(t *testing.T) {
	repo := newCommittedRepository(t)
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	bundleRoot := filepath.Join(t.TempDir(), "knowledge")

	first := &db.Session{ID: "session-one", Agent: "codex", Title: "First", WorkingDir: repo, UpdatedAt: 100}
	if _, err := RecordCheckpoint(database, first); err != nil {
		t.Fatalf("first RecordCheckpoint: %v", err)
	}
	if _, err := NewWriter(database, bundleRoot).WriteProject(first.ProjectID); err != nil {
		t.Fatalf("write first bundle: %v", err)
	}
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() { println(\"changed\") }\n")
	writeFile(t, filepath.Join(repo, ".env"), "TOKEN=must-not-leak\n")
	second := &db.Session{ID: "session-two", Agent: "codex", Title: "Second", WorkingDir: repo, UpdatedAt: 200}
	if _, err := RecordCheckpoint(database, second); err != nil {
		t.Fatalf("second RecordCheckpoint: %v", err)
	}
	if _, err := NewWriter(database, bundleRoot).WriteProject(second.ProjectID); err != nil {
		t.Fatalf("write second bundle: %v", err)
	}

	snapshots, err := database.ListGitSnapshots(second.ProjectID, 0)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("ListGitSnapshots = %d, %v", len(snapshots), err)
	}
	index, err := os.ReadFile(filepath.Join(bundleRoot, second.ProjectID, "diffs", strconv.FormatInt(snapshots[1].ID, 10), "index.md"))
	if err != nil {
		t.Fatalf("read diff index: %v", err)
	}
	if !strings.Contains(string(index), ".env: sensitive filename") {
		t.Fatalf("sensitive omission missing:\n%s", index)
	}
	if err := filepath.Walk(filepath.Join(bundleRoot, second.ProjectID), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), "must-not-leak") {
			return os.ErrPermission
		}
		return nil
	}); err != nil {
		t.Fatalf("sensitive content leaked into bundle: %v", err)
	}
}

func TestSessionDocumentDirectoryNeverTraverses(t *testing.T) {
	for _, sessionID := range []string{"../outside", "a/b", "session with spaces", ".."} {
		directory := sessionDocumentDirectory(sessionID)
		if strings.Contains(directory, "/") || directory == "." || directory == ".." {
			t.Fatalf("session directory for %q is unsafe: %q", sessionID, directory)
		}
	}
}

func TestWriter_PublishesAIAuthoredDocumentsWithFreshness(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	project := t.TempDir()
	first := &db.Session{ID: "session-one", Agent: "claude-code", Title: "Knowledge", WorkingDir: project, UpdatedAt: 100}
	state, err := RecordCheckpoint(database, first)
	if err != nil {
		t.Fatalf("first RecordCheckpoint: %v", err)
	}
	bundleRoot := filepath.Join(t.TempDir(), "knowledge")
	writer := NewWriter(database, bundleRoot)
	if _, err := writer.WriteProject(first.ProjectID); err != nil {
		t.Fatalf("WriteProject: %v", err)
	}
	if state, err = writer.PublishDocument(&DocumentInput{
		ProjectID: first.ProjectID, Type: db.KnowledgeDocumentProjectBrief, FactsRevision: state.FactsRevision,
		Content: "The project coordinates agent handoffs.", SourceSessionID: first.ID, GeneratedBy: "claude-code",
	}); err != nil || state.Status != db.KnowledgeRefreshPending {
		t.Fatalf("PublishDocument brief = %+v, %v", state, err)
	}
	if state, err = writer.PublishDocument(&DocumentInput{
		ProjectID: first.ProjectID, Type: db.KnowledgeDocumentRepoMap, FactsRevision: state.FactsRevision,
		Content: "- `internal/knowledge`: bundle lifecycle", SourceSessionID: first.ID, GeneratedBy: "codex",
	}); err != nil || state.Status != db.KnowledgeCurrent {
		t.Fatalf("PublishDocument repo map = %+v, %v", state, err)
	}
	brief, err := os.ReadFile(filepath.Join(bundleRoot, first.ProjectID, "project-brief.md"))
	if err != nil {
		t.Fatalf("read project brief: %v", err)
	}
	if !strings.Contains(string(brief), "source: \"agent-authored\"") || !strings.Contains(string(brief), "facts_revision: 1") {
		t.Fatalf("brief frontmatter missing provenance:\n%s", brief)
	}
	index, err := os.ReadFile(filepath.Join(bundleRoot, first.ProjectID, "index.md"))
	if err != nil {
		t.Fatalf("read project index: %v", err)
	}
	if !strings.Contains(string(index), "[Project Brief](project-brief.md) (current)") || !strings.Contains(string(index), "[Repository Map](repo-map.md) (current)") {
		t.Fatalf("index missing current AI documents:\n%s", index)
	}

	second := &db.Session{ID: "session-two", Agent: "codex", Title: "Knowledge", WorkingDir: project, UpdatedAt: 200}
	if _, err := RecordCheckpoint(database, second); err != nil {
		t.Fatalf("second RecordCheckpoint: %v", err)
	}
	if _, err := writer.WriteProject(second.ProjectID); err != nil {
		t.Fatalf("WriteProject after facts changed: %v", err)
	}
	index, err = os.ReadFile(filepath.Join(bundleRoot, second.ProjectID, "index.md"))
	if err != nil {
		t.Fatalf("read stale project index: %v", err)
	}
	if !strings.Contains(string(index), "[Project Brief](project-brief.md) (stale)") || !strings.Contains(string(index), "[Repository Map](repo-map.md) (stale)") {
		t.Fatalf("index did not mark AI documents stale:\n%s", index)
	}
}

func newCommittedRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Handshake Test")
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() { println(\"before\") }\n")
	runGit(t, repo, "add", "main.go")
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
