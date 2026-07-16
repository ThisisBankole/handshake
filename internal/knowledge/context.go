package knowledge

import (
	"fmt"

	"handshake/internal/db"
	"handshake/internal/git"
)

// ProjectContext is the read-only factual state an agent needs before it can
// safely author a project knowledge document.
type ProjectContext struct {
	ProjectID string
	State     *db.KnowledgeState
	Documents []*db.KnowledgeDocument
}

// GetProjectContext resolves an existing Handshake project from a working
// directory. It does not create a checkpoint or read transcripts.
func GetProjectContext(database *db.Database, workingDir string) (*ProjectContext, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	state, err := git.CaptureState(workingDir)
	if err != nil {
		return nil, err
	}
	remote := ""
	if state != nil {
		remote = state.Remote
	}
	identity, err := ResolveProject(workingDir, remote)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, fmt.Errorf("working directory is required")
	}
	knowledgeState, err := database.GetKnowledgeState(identity.ProjectID)
	if err != nil {
		return nil, err
	}
	documents, err := database.ListKnowledgeDocuments(identity.ProjectID)
	if err != nil {
		return nil, err
	}
	return &ProjectContext{ProjectID: identity.ProjectID, State: knowledgeState, Documents: documents}, nil
}
