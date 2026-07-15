package knowledge

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"handshake/internal/db"
)

const (
	maxDiffSourceBytes = 128 * 1024
	maxDiffDocument    = 64 * 1024
	maxDiffTotal       = 512 * 1024
)

// Writer creates the deterministic portion of a project's OKF bundle. It
// deliberately excludes AI-authored documents and raw conversation content.
type Writer struct {
	database *db.Database
	root     string
}

func NewWriter(database *db.Database, root string) *Writer {
	return &Writer{database: database, root: root}
}

// WriteProject materializes a project's factual OKF bundle. It returns false
// when the current facts revision has already been written successfully.
func (w *Writer) WriteProject(projectID string) (bool, error) {
	if w.database == nil || strings.TrimSpace(w.root) == "" || strings.TrimSpace(projectID) == "" {
		return false, fmt.Errorf("knowledge writer requires database, root, and project ID")
	}
	state, err := w.database.GetKnowledgeState(projectID)
	if err != nil {
		return false, err
	}
	snapshots, err := w.database.ListGitSnapshots(projectID, 0)
	if err != nil {
		return false, err
	}
	documents, err := w.database.ListKnowledgeDocuments(projectID)
	if err != nil {
		return false, err
	}
	bundleDir, err := safeBundlePath(w.root, projectID)
	if err != nil {
		return false, err
	}
	indexPath := filepath.Join(bundleDir, "index.md")
	if hasFactsRevision(indexPath, state.FactsRevision) {
		return false, nil
	}

	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		return false, fmt.Errorf("create knowledge bundle: %w", err)
	}
	if err := os.Chmod(bundleDir, 0700); err != nil {
		return false, fmt.Errorf("protect knowledge bundle: %w", err)
	}

	docs := map[string]string{}
	docs["log.md"] = w.logDocument(projectID, state, snapshots)
	docs[filepath.Join("sessions", "index.md")] = w.sessionsIndexDocument(projectID, state, snapshots)
	docs["git-timeline.md"] = w.gitTimelineDocument(projectID, state, snapshots)

	for _, sessionID := range uniqueSessionIDs(snapshots) {
		filtered := snapshotsForSession(snapshots, sessionID)
		sessionDir := sessionDocumentDirectory(sessionID)
		docs[filepath.Join("sessions", sessionDir, "index.md")] = w.sessionIndexDocument(projectID, state, sessionID, filtered)
		docs[filepath.Join("sessions", sessionDir, "timeline.md")] = w.sessionTimelineDocument(projectID, state, sessionID, filtered)
	}

	diffDocs, err := w.diffDocuments(projectID, state, snapshots)
	if err != nil {
		return false, err
	}
	for path, content := range diffDocs {
		docs[path] = content
	}

	paths := make([]string, 0, len(docs))
	for path := range docs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := writePrivateFile(filepath.Join(bundleDir, path), docs[path]); err != nil {
			return false, err
		}
	}
	// The root index is the completion marker. Write it last so a partial write
	// is retried rather than being mistaken for a complete revision.
	if err := writePrivateFile(indexPath, w.rootIndexDocument(projectID, state, snapshots, documents)); err != nil {
		return false, err
	}
	return true, nil
}

func (w *Writer) rootIndexDocument(projectID string, state *db.KnowledgeState, snapshots []*db.GitSnapshot, documents []*db.KnowledgeDocument) string {
	var b strings.Builder
	b.WriteString(documentHeader("project-index", "Project Knowledge Index", projectID, state))
	b.WriteString("# Project Knowledge Index\n\n")
	fmt.Fprintf(&b, "Deterministic project facts for `%s`. AI-authored documents are linked with their freshness status.\n\n", projectID)
	b.WriteString("## Documents\n\n")
	b.WriteString("- [Checkpoint log](log.md)\n")
	b.WriteString("- [Session index](sessions/index.md)\n")
	b.WriteString("- [Git timeline](git-timeline.md)\n")
	b.WriteString("- [Diff index](diffs/index.md)\n\n")
	if links := documentLinks(documents); len(links) > 0 {
		b.WriteString("## AI-Authored Documents\n\n")
		for _, link := range links {
			b.WriteString(link)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Snapshots recorded: %d\n", len(snapshots))
	return b.String()
}

func (w *Writer) refreshRootIndex(bundleDir, projectID string, state *db.KnowledgeState) error {
	snapshots, err := w.database.ListGitSnapshots(projectID, 0)
	if err != nil {
		return err
	}
	documents, err := w.database.ListKnowledgeDocuments(projectID)
	if err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(bundleDir, "index.md"), w.rootIndexDocument(projectID, state, snapshots, documents))
}

func (w *Writer) logDocument(projectID string, state *db.KnowledgeState, snapshots []*db.GitSnapshot) string {
	var b strings.Builder
	b.WriteString(documentHeader("checkpoint-log", "Checkpoint Log", projectID, state))
	b.WriteString("# Checkpoint Log\n\n")
	if len(snapshots) == 0 {
		b.WriteString("No checkpoints recorded.\n")
		return b.String()
	}
	for _, snapshot := range snapshots {
		fmt.Fprintf(&b, "- %s: session [`%s`](sessions/%s/timeline.md), commit `%s`, branch `%s`\n",
			formatTime(snapshot.CapturedAt), snapshot.SessionID, sessionDocumentDirectory(snapshot.SessionID), shortHash(snapshot.Commit), valueOr(snapshot.Branch, "unknown"))
	}
	return b.String()
}

func (w *Writer) sessionsIndexDocument(projectID string, state *db.KnowledgeState, snapshots []*db.GitSnapshot) string {
	var b strings.Builder
	b.WriteString(documentHeader("session-index", "Session Index", projectID, state))
	b.WriteString("# Session Index\n\n")
	for _, sessionID := range uniqueSessionIDs(snapshots) {
		fmt.Fprintf(&b, "- [`%s`](%s/index.md)\n", sessionID, sessionDocumentDirectory(sessionID))
	}
	return b.String()
}

func (w *Writer) sessionIndexDocument(projectID string, state *db.KnowledgeState, sessionID string, snapshots []*db.GitSnapshot) string {
	var b strings.Builder
	b.WriteString(documentHeader("session-index-entry", "Session "+sessionID, projectID, state))
	b.WriteString("# Session `" + sessionID + "`\n\n")
	fmt.Fprintf(&b, "- [Checkpoint timeline](timeline.md)\n- Checkpoints: %d\n", len(snapshots))
	return b.String()
}

func (w *Writer) sessionTimelineDocument(projectID string, state *db.KnowledgeState, sessionID string, snapshots []*db.GitSnapshot) string {
	var b strings.Builder
	b.WriteString(documentHeader("session-timeline", "Session Timeline", projectID, state))
	b.WriteString("# Session Timeline\n\n")
	for _, snapshot := range snapshots {
		fmt.Fprintf(&b, "## %s\n\n", formatTime(snapshot.CapturedAt))
		fmt.Fprintf(&b, "- Commit: `%s`\n- Branch: `%s`\n", shortHash(snapshot.Commit), valueOr(snapshot.Branch, "unknown"))
		if snapshot.Summary != "" {
			b.WriteString("- Source summary:\n\n")
			b.WriteString(indentMarkdown(snapshot.Summary))
			b.WriteString("\n")
		}
		if snapshot.Decisions != "" {
			b.WriteString("- Source decisions:\n")
			for _, decision := range nonEmptyLines(snapshot.Decisions) {
				fmt.Fprintf(&b, "  - %s\n", decision)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (w *Writer) gitTimelineDocument(projectID string, state *db.KnowledgeState, snapshots []*db.GitSnapshot) string {
	var b strings.Builder
	b.WriteString(documentHeader("git-timeline", "Git Timeline", projectID, state))
	b.WriteString("# Git Timeline\n\n")
	for _, snapshot := range snapshots {
		fmt.Fprintf(&b, "## Snapshot %d: %s\n\n", snapshot.ID, formatTime(snapshot.CapturedAt))
		fmt.Fprintf(&b, "- Session: [`%s`](sessions/%s/timeline.md)\n- Commit: `%s`\n- Branch: `%s`\n",
			snapshot.SessionID, sessionDocumentDirectory(snapshot.SessionID), shortHash(snapshot.Commit), valueOr(snapshot.Branch, "unknown"))
		if snapshot.Message != "" {
			fmt.Fprintf(&b, "- Commit message: %s\n", snapshot.Message)
		}
		fmt.Fprintf(&b, "- [Diff record](diffs/%d/index.md)\n\n", snapshot.ID)
	}
	return b.String()
}

func (w *Writer) diffDocuments(projectID string, state *db.KnowledgeState, snapshots []*db.GitSnapshot) (map[string]string, error) {
	docs := make(map[string]string)
	var index strings.Builder
	index.WriteString(documentHeader("diff-index", "Diff Index", projectID, state))
	index.WriteString("# Diff Index\n\n")
	previous := make(map[string]*db.GitSnapshot)
	for _, snapshot := range snapshots {
		base := previous[snapshot.InstanceID]
		documents, label, err := w.snapshotDiffDocuments(projectID, state, snapshot, base, snapshot == snapshots[len(snapshots)-1])
		if err != nil {
			return nil, err
		}
		for path, content := range documents {
			docs[path] = content
		}
		fmt.Fprintf(&index, "- [Snapshot %d](%d/index.md): %s\n", snapshot.ID, snapshot.ID, label)
		previous[snapshot.InstanceID] = snapshot
	}
	docs[filepath.Join("diffs", "index.md")] = index.String()
	return docs, nil
}

func (w *Writer) snapshotDiffDocuments(projectID string, state *db.KnowledgeState, snapshot, base *db.GitSnapshot, latest bool) (map[string]string, string, error) {
	docs := make(map[string]string)
	prefix := filepath.Join("diffs", strconv.FormatInt(snapshot.ID, 10))
	var changed, omitted []string
	var source string
	if base != nil && base.Commit != "" && snapshot.Commit != "" && base.Commit != snapshot.Commit {
		source = "commit comparison"
		instance, err := w.database.GetProjectInstance(snapshot.InstanceID)
		if err == nil {
			paths, diffErr := gitOutputLines(instance.RootPath, "diff", "--name-only", "--find-renames", base.Commit, snapshot.Commit)
			if diffErr == nil {
				changed, omitted = buildCommitDiffs(instance.RootPath, base.Commit, snapshot.Commit, paths, maxDiffTotal, docs, prefix, projectID, state, snapshot, omitted)
			}
		} else if !errors.Is(err, db.ErrProjectInstanceNotFound) {
			return nil, "", err
		}
	} else if latest && snapshot.Status != "" {
		source = "working tree comparison"
		instance, err := w.database.GetProjectInstance(snapshot.InstanceID)
		if err == nil {
			paths := statusPaths(snapshot.Status)
			changed, omitted = buildWorktreeDiffs(instance.RootPath, paths, maxDiffTotal, docs, prefix, projectID, state, snapshot, omitted)
		} else if !errors.Is(err, db.ErrProjectInstanceNotFound) {
			return nil, "", err
		}
	}

	var b strings.Builder
	b.WriteString(documentHeader("diff-snapshot", fmt.Sprintf("Snapshot %d Diffs", snapshot.ID), projectID, state))
	fmt.Fprintf(&b, "# Snapshot %d Diffs\n\n", snapshot.ID)
	if base == nil {
		b.WriteString("No earlier checkpoint exists for this local worktree; there is no comparison base.\n")
	} else if source == "" {
		b.WriteString("No safe Git diff was available for this checkpoint.\n")
	} else {
		fmt.Fprintf(&b, "Source: %s.\n\n", source)
	}
	if len(changed) > 0 {
		b.WriteString("## Included Files\n\n")
		for _, path := range changed {
			fmt.Fprintf(&b, "- [%s](%s)\n", path, diffDocumentName(path))
		}
	}
	if len(omitted) > 0 {
		b.WriteString("\n## Omitted Files\n\n")
		for _, entry := range omitted {
			fmt.Fprintf(&b, "- %s\n", entry)
		}
	}
	if len(changed) == 0 && len(omitted) == 0 && base != nil && source != "" {
		b.WriteString("No changed files were recorded.\n")
	}
	docs[filepath.Join(prefix, "index.md")] = b.String()
	return docs, fmt.Sprintf("%d included, %d omitted", len(changed), len(omitted)), nil
}

func buildCommitDiffs(root, base, target string, paths []string, budget int, docs map[string]string, prefix, projectID string, state *db.KnowledgeState, snapshot *db.GitSnapshot, omitted []string) ([]string, []string) {
	var included []string
	for _, path := range paths {
		reason := excludedPathReason(path)
		if reason != "" {
			omitted = append(omitted, path+": "+reason)
			continue
		}
		diff, err := gitOutput(root, "diff", "--no-ext-diff", "--unified=3", base, target, "--", path)
		if err != nil || diff == "" {
			if err != nil {
				omitted = append(omitted, path+": Git could not produce a text diff")
			}
			continue
		}
		if strings.Contains(diff, "Binary files ") || len(diff) > maxDiffDocument || len(diff) > budget {
			omitted = append(omitted, path+": diff exceeds safety limits or is binary")
			continue
		}
		budget -= len(diff)
		included = append(included, path)
		docs[filepath.Join(prefix, diffDocumentName(path))] = diffDocument(projectID, state, snapshot, path, diff)
	}
	return included, omitted
}

func buildWorktreeDiffs(root string, paths []string, budget int, docs map[string]string, prefix, projectID string, state *db.KnowledgeState, snapshot *db.GitSnapshot, omitted []string) ([]string, []string) {
	var included []string
	for _, path := range paths {
		reason := excludedPathReason(path)
		if reason != "" {
			omitted = append(omitted, path+": "+reason)
			continue
		}
		absolute, err := safeRepositoryPath(root, path)
		if err != nil {
			omitted = append(omitted, path+": unsafe path")
			continue
		}
		if info, statErr := os.Stat(absolute); statErr == nil && info.Size() > maxDiffSourceBytes {
			omitted = append(omitted, path+": source file exceeds 128 KiB")
			continue
		}
		diff, err := gitOutput(root, "diff", "--no-ext-diff", "--unified=3", "HEAD", "--", path)
		if err != nil || diff == "" {
			omitted = append(omitted, path+": no tracked Git diff (untracked files are not exported)")
			continue
		}
		if strings.Contains(diff, "Binary files ") || len(diff) > maxDiffDocument || len(diff) > budget {
			omitted = append(omitted, path+": diff exceeds safety limits or is binary")
			continue
		}
		budget -= len(diff)
		included = append(included, path)
		docs[filepath.Join(prefix, diffDocumentName(path))] = diffDocument(projectID, state, snapshot, path, diff)
	}
	return included, omitted
}

func diffDocument(projectID string, state *db.KnowledgeState, snapshot *db.GitSnapshot, path, diff string) string {
	return documentHeader("file-diff", "Diff "+path, projectID, state) + "# Diff: `" + path + "`\n\n```diff\n" + strings.TrimSuffix(diff, "\n") + "\n```\n"
}

func documentHeader(kind, title, projectID string, state *db.KnowledgeState) string {
	return fmt.Sprintf("---\ntype: %s\ntitle: %s\nproject_id: %s\nfacts_revision: %d\nsource: deterministic\ngenerated_at: %s\n---\n\n",
		strconv.Quote(kind), strconv.Quote(title), strconv.Quote(projectID), state.FactsRevision, formatTime(state.UpdatedAt))
}

func writePrivateFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create knowledge directory: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".handshake-*.tmp")
	if err != nil {
		return fmt.Errorf("create knowledge document: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect knowledge document: %w", err)
	}
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write knowledge document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync knowledge document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close knowledge document: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish knowledge document: %w", err)
	}
	return nil
}

func safeBundlePath(root, projectID string) (string, error) {
	if filepath.Base(projectID) != projectID || projectID == "." || strings.Contains(projectID, string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe project ID")
	}
	return filepath.Join(root, projectID), nil
}

func safeRepositoryPath(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute path")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside repository")
	}
	absolute := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside repository")
	}
	return absolute, nil
}

func hasFactsRevision(path string, revision int64) bool {
	content, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(content), fmt.Sprintf("facts_revision: %d\n", revision))
}

func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func gitOutputLines(root string, args ...string) ([]string, error) {
	output, err := gitOutput(root, args...)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(output), nil
}

func statusPaths(status string) []string {
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if renamed := strings.LastIndex(path, " -> "); renamed >= 0 {
			path = strings.TrimSpace(path[renamed+4:])
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func excludedPathReason(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	base := strings.ToLower(filepath.Base(clean))
	for _, segment := range strings.Split(clean, "/") {
		switch strings.ToLower(segment) {
		case "node_modules", "vendor", "dist", "build", ".next", "coverage", "target", ".cache", ".git", ".aws", ".ssh", "secrets", "credentials":
			return "generated or sensitive path"
		}
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" {
		return "sensitive filename"
	}
	for _, extension := range []string{".pem", ".key", ".p12", ".pfx", ".min.js", ".map"} {
		if strings.HasSuffix(base, extension) {
			return "sensitive or generated filename"
		}
	}
	return ""
}

func diffDocumentName(path string) string {
	safe := strings.NewReplacer("/", "__", "\\", "__", " ", "_", ":", "_").Replace(path)
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%s-%x.md", safe, sum[:6])
}

func sessionDocumentDirectory(sessionID string) string {
	if sessionID != "" {
		safe := true
		for _, character := range sessionID {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
				safe = false
				break
			}
		}
		if safe && sessionID != "." && sessionID != ".." {
			return sessionID
		}
	}
	sum := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("session-%x", sum[:8])
}

func uniqueSessionIDs(snapshots []*db.GitSnapshot) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, snapshot := range snapshots {
		if !seen[snapshot.SessionID] {
			seen[snapshot.SessionID] = true
			ids = append(ids, snapshot.SessionID)
		}
	}
	return ids
}

func snapshotsForSession(snapshots []*db.GitSnapshot, sessionID string) []*db.GitSnapshot {
	var filtered []*db.GitSnapshot
	for _, snapshot := range snapshots {
		if snapshot.SessionID == sessionID {
			filtered = append(filtered, snapshot)
		}
	}
	return filtered
}

func nonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func indentMarkdown(value string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}

func formatTime(unix int64) string {
	if unix <= 0 {
		return "unknown"
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func shortHash(value string) string {
	if value == "" {
		return "none"
	}
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
