package knowledge

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"handshake/internal/db"
)

const maxAIDocumentBytes = 256 * 1024

// DocumentInput is the agent-authored body and evidence for one required OKF
// document. Handshake owns the frontmatter so agents cannot claim a different
// project, revision, or provenance than the facts they used.
type DocumentInput struct {
	ProjectID       string
	Type            string
	FactsRevision   int64
	Content         string
	SourceSessionID string
	SourceCommit    string
	Evidence        string
	GeneratedBy     string
}

type documentDefinition struct {
	path  string
	title string
}

func definitionForDocumentType(documentType string) (documentDefinition, error) {
	switch documentType {
	case db.KnowledgeDocumentProjectBrief:
		return documentDefinition{path: "project-brief.md", title: "Project Brief"}, nil
	case db.KnowledgeDocumentRepoMap:
		return documentDefinition{path: "repo-map.md", title: "Repository Map"}, nil
	default:
		return documentDefinition{}, fmt.Errorf("unsupported knowledge document type %q", documentType)
	}
}

// PublishDocument writes an AI-authored document to its fixed OKF path and
// records its provenance. A stale agent response is rejected rather than
// replacing a newer project's knowledge with an old interpretation.
func (w *Writer) PublishDocument(input *DocumentInput) (*db.KnowledgeState, error) {
	if w.database == nil || input == nil || input.ProjectID == "" || input.Type == "" {
		return nil, fmt.Errorf("knowledge document requires database, project ID, and type")
	}
	if strings.TrimSpace(w.root) == "" {
		return nil, fmt.Errorf("knowledge document requires bundle root")
	}
	definition, err := definitionForDocumentType(input.Type)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(input.Content)
	if body == "" {
		return nil, fmt.Errorf("knowledge document content is empty")
	}
	if len(body) > maxAIDocumentBytes {
		return nil, fmt.Errorf("knowledge document exceeds %d KiB", maxAIDocumentBytes/1024)
	}
	state, err := w.database.GetKnowledgeState(input.ProjectID)
	if err != nil {
		return nil, err
	}
	if input.FactsRevision != state.FactsRevision {
		return nil, fmt.Errorf("%w: document=%d current=%d", db.ErrKnowledgeFactsStale, input.FactsRevision, state.FactsRevision)
	}
	bundleDir, err := safeBundlePath(w.root, input.ProjectID)
	if err != nil {
		return nil, err
	}
	content := authoredDocumentContent(definition, input, body)
	sum := sha256.Sum256([]byte(content))
	document := &db.KnowledgeDocument{
		ProjectID:       input.ProjectID,
		Path:            definition.path,
		Type:            input.Type,
		FactsRevision:   input.FactsRevision,
		SourceSessionID: input.SourceSessionID,
		SourceCommit:    input.SourceCommit,
		Evidence:        input.Evidence,
		ContentHash:     fmt.Sprintf("%x", sum[:]),
		GeneratedBy:     valueOr(input.GeneratedBy, "agent"),
		GeneratedAt:     time.Now().Unix(),
	}
	documentPath := filepath.Join(bundleDir, definition.path)
	previous, readErr := os.ReadFile(documentPath)
	hadPrevious := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read existing knowledge document: %w", readErr)
	}
	updatedState, err := w.database.PublishKnowledgeDocument(document, func() error {
		return writePrivateFile(documentPath, content)
	})
	if err != nil {
		if rollbackErr := rollbackPublishedDocument(documentPath, previous, hadPrevious); rollbackErr != nil {
			return nil, errors.Join(err, fmt.Errorf("roll back knowledge document: %w", rollbackErr))
		}
		return nil, err
	}
	if err := w.refreshRootIndex(bundleDir, input.ProjectID, updatedState); err != nil {
		return nil, err
	}
	return updatedState, nil
}

func rollbackPublishedDocument(path string, previous []byte, hadPrevious bool) error {
	if hadPrevious {
		return writePrivateFile(path, string(previous))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func authoredDocumentContent(definition documentDefinition, input *DocumentInput, body string) string {
	return fmt.Sprintf("---\ntype: %s\ntitle: %s\nproject_id: %s\nfacts_revision: %d\nsource: %s\ngenerated_by: %s\nsource_session_id: %s\nsource_commit: %s\ngenerated_at: %s\n---\n\n# %s\n\n%s\n",
		strconv.Quote(input.Type), strconv.Quote(definition.title), strconv.Quote(input.ProjectID), input.FactsRevision,
		strconv.Quote("agent-authored"), strconv.Quote(valueOr(input.GeneratedBy, "agent")),
		strconv.Quote(input.SourceSessionID), strconv.Quote(input.SourceCommit),
		formatTime(time.Now().Unix()), definition.title, body)
}

func documentLinks(documents []*db.KnowledgeDocument) []string {
	links := make(map[string]string)
	for _, document := range documents {
		definition, err := definitionForDocumentType(document.Type)
		if err != nil || document.Path != definition.path {
			continue
		}
		links[document.Type] = fmt.Sprintf("- [%s](%s) (%s)\n", definition.title, definition.path, document.Status)
	}
	orderedTypes := []string{db.KnowledgeDocumentProjectBrief, db.KnowledgeDocumentRepoMap}
	var result []string
	for _, documentType := range orderedTypes {
		if link, ok := links[documentType]; ok {
			result = append(result, link)
		}
	}
	return result
}
