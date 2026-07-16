package knowledge

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"handshake/internal/db"
)

const (
	maxRestoreProjectBrief = 6000
	maxRestoreRepoMap      = 4000
	maxRestoreKnowledge    = 12000
)

// BuildRestoreContext returns the bounded project-level context that follows a
// restored session handoff. It injects only current, hash-verified AI documents
// and directs the receiving agent to deeper factual records on disk.
func BuildRestoreContext(database *db.Database, knowledgeRoot string, session *db.Session) string {
	if database == nil || session == nil || session.ProjectID == "" || strings.TrimSpace(knowledgeRoot) == "" {
		return ""
	}
	state, err := database.GetKnowledgeState(session.ProjectID)
	if err != nil {
		return ""
	}
	bundleDir, err := safeBundlePath(knowledgeRoot, session.ProjectID)
	if err != nil {
		return ""
	}
	snapshots, err := database.ListGitSnapshots(session.ProjectID, 0)
	if err != nil {
		return ""
	}
	documents, err := database.ListKnowledgeDocuments(session.ProjectID)
	if err != nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Project Knowledge\n\n- Facts revision: %d (%s)\n", state.FactsRevision, state.Status)
	if len(snapshots) > 0 {
		latest := snapshots[len(snapshots)-1]
		fmt.Fprintf(&b, "- Latest checkpoint: commit `%s` on `%s`\n", shortHash(latest.Commit), valueOr(latest.Branch, "unknown"))
	}
	fmt.Fprintf(&b, "- Bundle: `%s`\n", bundleDir)
	b.WriteString("- Deeper factual records: `log.md`, `git-timeline.md`, `diffs/index.md`\n\n")
	b.WriteString("The source-session handoff remains authoritative for immediate work. Treat this as project context and verify before acting.\n")

	remaining := maxRestoreKnowledge - b.Len()
	for _, documentType := range []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap} {
		document := findKnowledgeDocument(documents, documentType)
		if document == nil || document.Status != db.KnowledgeDocumentCurrent || document.FactsRevision != state.FactsRevision {
			fmt.Fprintf(&b, "\n- %s is not current and was not injected.\n", documentTitle(documentType))
			continue
		}
		limit := maxRestoreProjectBrief
		if documentType == db.KnowledgeDocumentRepoMap {
			limit = maxRestoreRepoMap
		}
		if remaining <= 0 {
			break
		}
		if limit > remaining {
			limit = remaining
		}
		content, ok := verifiedDocumentBody(bundleDir, document, limit)
		if !ok {
			fmt.Fprintf(&b, "\n- %s could not be verified and was not injected.\n", documentTitle(documentType))
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", documentTitle(documentType), content)
		remaining = maxRestoreKnowledge - b.Len()
	}
	return b.String()
}

func findKnowledgeDocument(documents []*db.KnowledgeDocument, documentType string) *db.KnowledgeDocument {
	for _, document := range documents {
		if document.Type == documentType {
			return document
		}
	}
	return nil
}

func verifiedDocumentBody(bundleDir string, document *db.KnowledgeDocument, limit int) (string, bool) {
	definition, err := definitionForDocumentType(document.Type)
	if err != nil || document.Path != definition.path || document.ContentHash == "" {
		return "", false
	}
	contents, err := os.ReadFile(filepath.Join(bundleDir, definition.path))
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(contents)
	if fmt.Sprintf("%x", sum[:]) != document.ContentHash {
		return "", false
	}
	return clipRestoreText(stripFrontmatter(string(contents)), limit), true
}

func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return strings.TrimSpace(content)
	}
	if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
		return strings.TrimSpace(content[end+9:])
	}
	return strings.TrimSpace(content)
}

func clipRestoreText(content string, limit int) string {
	content = strings.TrimSpace(content)
	if len(content) <= limit {
		return content
	}
	if limit <= 3 {
		return content[:limit]
	}
	return content[:limit-3] + "..."
}

func documentTitle(documentType string) string {
	definition, err := definitionForDocumentType(documentType)
	if err != nil {
		return documentType
	}
	return definition.title
}
