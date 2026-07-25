package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrKnowledgeStateNotFound    = errors.New("knowledge state not found")
	ErrProjectInstanceNotFound   = errors.New("project instance not found")
	ErrKnowledgeDocumentNotFound = errors.New("knowledge document not found")
	ErrKnowledgeFactsStale       = errors.New("knowledge document facts revision is stale")
)

// RecordKnowledgeCheckpoint stores a session update, project identity, and
// factual snapshot in one transaction. A repeated fingerprint refreshes the
// session record without advancing the project's facts revision.
func (d *Database) RecordKnowledgeCheckpoint(checkpoint *KnowledgeCheckpoint) (*KnowledgeState, error) {
	if checkpoint == nil || checkpoint.Session == nil || checkpoint.Project == nil || checkpoint.Instance == nil || checkpoint.Snapshot == nil {
		return nil, fmt.Errorf("knowledge checkpoint requires session, project, instance, and snapshot")
	}
	if checkpoint.Session.ID == "" || checkpoint.Project.ID == "" || checkpoint.Instance.ID == "" || checkpoint.Snapshot.Fingerprint == "" {
		return nil, fmt.Errorf("knowledge checkpoint has missing identity")
	}

	prepareSession(checkpoint.Session)
	now := time.Now().Unix()
	checkpoint.Session.ProjectID = checkpoint.Project.ID
	checkpoint.Instance.ProjectID = checkpoint.Project.ID
	checkpoint.Snapshot.ProjectID = checkpoint.Project.ID
	checkpoint.Snapshot.InstanceID = checkpoint.Instance.ID
	checkpoint.Snapshot.SessionID = checkpoint.Session.ID
	if checkpoint.Snapshot.CapturedAt == 0 {
		checkpoint.Snapshot.CapturedAt = now
	}

	tx, err := d.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin knowledge checkpoint: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO projects (id, remote_url, local_only, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  remote_url = CASE WHEN excluded.remote_url != '' THEN excluded.remote_url ELSE projects.remote_url END,
		  local_only = excluded.local_only,
		  updated_at = excluded.updated_at`,
		checkpoint.Project.ID, checkpoint.Project.RemoteURL, boolInt(checkpoint.Project.LocalOnly), now, now); err != nil {
		return nil, fmt.Errorf("store project: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO project_instances (id, project_id, root_path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  project_id = excluded.project_id,
		  root_path = excluded.root_path,
		  updated_at = excluded.updated_at`,
		checkpoint.Instance.ID, checkpoint.Instance.ProjectID, checkpoint.Instance.RootPath, now, now); err != nil {
		return nil, fmt.Errorf("store project instance: %w", err)
	}

	if err := storeSession(tx, checkpoint.Session); err != nil {
		return nil, err
	}

	var previous string
	err = tx.QueryRow(`SELECT fingerprint FROM git_snapshots WHERE project_id = ? ORDER BY id DESC LIMIT 1`, checkpoint.Project.ID).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("read latest snapshot: %w", err)
	}
	if previous == checkpoint.Snapshot.Fingerprint {
		state, err := getKnowledgeState(tx, checkpoint.Project.ID)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit unchanged knowledge checkpoint: %w", err)
		}
		return state, nil
	}

	result, err := tx.Exec(`INSERT INTO git_snapshots
		(project_id, instance_id, session_id, fingerprint, has_git, commit_hash, branch, message, worktree_status, remote_url, summary, decisions, source_updated_at, captured_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		checkpoint.Snapshot.ProjectID, checkpoint.Snapshot.InstanceID, checkpoint.Snapshot.SessionID,
		checkpoint.Snapshot.Fingerprint, boolInt(checkpoint.Snapshot.HasGit), checkpoint.Snapshot.Commit,
		checkpoint.Snapshot.Branch, checkpoint.Snapshot.Message, checkpoint.Snapshot.Status,
		checkpoint.Snapshot.RemoteURL, checkpoint.Snapshot.Summary, checkpoint.Snapshot.Decisions,
		checkpoint.Snapshot.SourceUpdatedAt, checkpoint.Snapshot.CapturedAt)
	if err != nil {
		return nil, fmt.Errorf("store git snapshot: %w", err)
	}
	snapshotID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read git snapshot ID: %w", err)
	}

	state, err := getKnowledgeState(tx, checkpoint.Project.ID)
	if err != nil && !errors.Is(err, ErrKnowledgeStateNotFound) {
		return nil, err
	}
	if state == nil {
		state = &KnowledgeState{ProjectID: checkpoint.Project.ID, Status: KnowledgeRefreshPending}
	}
	state.FactsRevision++
	state.LastSnapshotID = snapshotID
	state.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE knowledge_documents SET status = ?
		WHERE project_id = ? AND facts_revision < ? AND status = ?`,
		KnowledgeDocumentStale, state.ProjectID, state.FactsRevision, KnowledgeDocumentCurrent); err != nil {
		return nil, fmt.Errorf("mark stale knowledge documents: %w", err)
	}
	if state.AIRevision > 0 {
		state.Status = KnowledgeFactsAIStale
	} else {
		state.Status = KnowledgeRefreshPending
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_state (project_id, facts_revision, ai_revision, status, last_snapshot_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
		  facts_revision = excluded.facts_revision,
		  ai_revision = excluded.ai_revision,
		  status = excluded.status,
		  last_snapshot_id = excluded.last_snapshot_id,
		  updated_at = excluded.updated_at`,
		state.ProjectID, state.FactsRevision, state.AIRevision, state.Status, state.LastSnapshotID, state.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store knowledge state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit knowledge checkpoint: %w", err)
	}
	return state, nil
}

// StoreKnowledgeDocument records provenance for an AI-authored document. The
// document must be based on the latest factual revision; this prevents a late
// agent response from silently making stale knowledge appear current.
func (d *Database) StoreKnowledgeDocument(document *KnowledgeDocument) (*KnowledgeState, error) {
	return d.storeKnowledgeDocument(document, nil)
}

// PublishKnowledgeDocument runs publish after the facts revision has been
// validated while the database's single connection is reserved by the
// transaction. This prevents a checkpoint from advancing the revision between
// validation and the corresponding atomic filesystem publication.
func (d *Database) PublishKnowledgeDocument(document *KnowledgeDocument, publish func() error) (*KnowledgeState, error) {
	if publish == nil {
		return nil, fmt.Errorf("knowledge document publish callback is required")
	}
	return d.storeKnowledgeDocument(document, publish)
}

func (d *Database) storeKnowledgeDocument(document *KnowledgeDocument, publish func() error) (*KnowledgeState, error) {
	if document == nil || document.ProjectID == "" || document.Path == "" || document.Type == "" {
		return nil, fmt.Errorf("knowledge document requires project ID, path, and type")
	}
	if !isRequiredKnowledgeDocumentType(document.Type) {
		return nil, fmt.Errorf("unsupported knowledge document type %q", document.Type)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin knowledge document: %w", err)
	}
	defer tx.Rollback()
	state, err := getKnowledgeState(tx, document.ProjectID)
	if err != nil {
		return nil, err
	}
	if document.FactsRevision != state.FactsRevision {
		return nil, fmt.Errorf("%w: document=%d current=%d", ErrKnowledgeFactsStale, document.FactsRevision, state.FactsRevision)
	}
	if publish != nil {
		if err := publish(); err != nil {
			return nil, fmt.Errorf("publish knowledge document: %w", err)
		}
	}
	if document.GeneratedAt == 0 {
		document.GeneratedAt = time.Now().Unix()
	}
	document.Status = KnowledgeDocumentCurrent
	if _, err := tx.Exec(`INSERT INTO knowledge_documents
		(project_id, path, type, facts_revision, source_session_id, source_commit, evidence, status, content_hash, generated_by, generated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, path) DO UPDATE SET
		  type = excluded.type,
		  facts_revision = excluded.facts_revision,
		  source_session_id = excluded.source_session_id,
		  source_commit = excluded.source_commit,
		  evidence = excluded.evidence,
		  status = excluded.status,
		  content_hash = excluded.content_hash,
		  generated_by = excluded.generated_by,
		  generated_at = excluded.generated_at`,
		document.ProjectID, document.Path, document.Type, document.FactsRevision,
		document.SourceSessionID, document.SourceCommit, document.Evidence, document.Status,
		document.ContentHash, document.GeneratedBy, document.GeneratedAt); err != nil {
		return nil, fmt.Errorf("store knowledge document: %w", err)
	}

	currentCount, err := countCurrentRequiredDocuments(tx, document.ProjectID, state.FactsRevision)
	if err != nil {
		return nil, err
	}
	if currentCount == len(requiredKnowledgeDocumentTypes()) {
		state.AIRevision = state.FactsRevision
		state.Status = KnowledgeCurrent
	} else if state.AIRevision > 0 {
		state.Status = KnowledgeFactsAIStale
	} else {
		state.Status = KnowledgeRefreshPending
	}
	state.UpdatedAt = time.Now().Unix()
	if _, err := tx.Exec(`UPDATE knowledge_state SET ai_revision = ?, status = ?, updated_at = ? WHERE project_id = ?`,
		state.AIRevision, state.Status, state.UpdatedAt, state.ProjectID); err != nil {
		return nil, fmt.Errorf("update knowledge freshness: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit knowledge document: %w", err)
	}
	return state, nil
}

// ListKnowledgeDocuments returns document provenance in a stable order. File
// contents remain in the private OKF bundle, never in the database.
func (d *Database) ListKnowledgeDocuments(projectID string) ([]*KnowledgeDocument, error) {
	rows, err := d.db.Query(`SELECT project_id, path, type, facts_revision, source_session_id,
		source_commit, evidence, status, content_hash, generated_by, generated_at
		FROM knowledge_documents WHERE project_id = ? ORDER BY path ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list knowledge documents: %w", err)
	}
	defer rows.Close()
	var documents []*KnowledgeDocument
	for rows.Next() {
		document := &KnowledgeDocument{}
		if err := rows.Scan(&document.ProjectID, &document.Path, &document.Type, &document.FactsRevision,
			&document.SourceSessionID, &document.SourceCommit, &document.Evidence, &document.Status,
			&document.ContentHash, &document.GeneratedBy, &document.GeneratedAt); err != nil {
			return nil, fmt.Errorf("scan knowledge document: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge documents: %w", err)
	}
	return documents, nil
}

// GetKnowledgeState reports whether deterministic facts are newer than the
// latest AI-authored knowledge for a project.
func (d *Database) GetKnowledgeState(projectID string) (*KnowledgeState, error) {
	return getKnowledgeState(d.db, projectID)
}

// ListGitSnapshots returns immutable factual checkpoints in chronological
// order. The OKF timeline writer uses this in the next implementation phase.
func (d *Database) ListGitSnapshots(projectID string, limit int) ([]*GitSnapshot, error) {
	query := `SELECT id, project_id, instance_id, session_id, fingerprint, has_git,
		commit_hash, branch, message, worktree_status, remote_url, summary, decisions,
		source_updated_at, captured_at
		FROM git_snapshots WHERE project_id = ? ORDER BY id ASC`
	args := []any{projectID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list git snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []*GitSnapshot
	for rows.Next() {
		snapshot := &GitSnapshot{}
		var hasGit int
		if err := rows.Scan(&snapshot.ID, &snapshot.ProjectID, &snapshot.InstanceID, &snapshot.SessionID,
			&snapshot.Fingerprint, &hasGit, &snapshot.Commit, &snapshot.Branch, &snapshot.Message,
			&snapshot.Status, &snapshot.RemoteURL, &snapshot.Summary, &snapshot.Decisions,
			&snapshot.SourceUpdatedAt, &snapshot.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan git snapshot: %w", err)
		}
		snapshot.HasGit = hasGit != 0
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate git snapshots: %w", err)
	}
	return snapshots, nil
}

// GetProjectInstance returns the local clone or worktree associated with a
// snapshot. The OKF writer needs its root only while it is creating a bounded
// Git diff; root paths are not rendered into knowledge documents.
func (d *Database) GetProjectInstance(instanceID string) (*ProjectInstance, error) {
	instance := &ProjectInstance{}
	err := d.db.QueryRow(`SELECT id, project_id, root_path, created_at, updated_at
		FROM project_instances WHERE id = ?`, instanceID).Scan(
		&instance.ID, &instance.ProjectID, &instance.RootPath, &instance.CreatedAt, &instance.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrProjectInstanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project instance: %w", err)
	}
	return instance, nil
}

// MarkKnowledgeCurrent advances the AI revision to the latest facts revision.
// It is used by the OKF writer introduced in the next phase.
func (d *Database) MarkKnowledgeCurrent(projectID string) (*KnowledgeState, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin knowledge refresh: %w", err)
	}
	defer tx.Rollback()
	state, err := getKnowledgeState(tx, projectID)
	if err != nil {
		return nil, err
	}
	state.AIRevision = state.FactsRevision
	state.Status = KnowledgeCurrent
	state.UpdatedAt = time.Now().Unix()
	if _, err := tx.Exec(`UPDATE knowledge_state SET ai_revision = ?, status = ?, updated_at = ? WHERE project_id = ?`,
		state.AIRevision, state.Status, state.UpdatedAt, state.ProjectID); err != nil {
		return nil, fmt.Errorf("mark knowledge current: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit knowledge refresh: %w", err)
	}
	return state, nil
}

// MarkKnowledgeAgentRequested records that a live agent was just handed a
// refresh instruction, starting the cooldown that keeps lifecycle hooks from
// interrupting the agent again until it expires.
func (d *Database) MarkKnowledgeAgentRequested(projectID string) error {
	if _, err := d.db.Exec(`UPDATE knowledge_state SET agent_requested_at = ? WHERE project_id = ?`,
		time.Now().Unix(), projectID); err != nil {
		return fmt.Errorf("mark knowledge agent requested: %w", err)
	}
	return nil
}

type sqlQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getKnowledgeState(queryer sqlQueryer, projectID string) (*KnowledgeState, error) {
	state := &KnowledgeState{}
	err := queryer.QueryRow(`SELECT project_id, facts_revision, ai_revision, status, last_snapshot_id, agent_requested_at, updated_at
		FROM knowledge_state WHERE project_id = ?`, projectID).Scan(
		&state.ProjectID, &state.FactsRevision, &state.AIRevision, &state.Status, &state.LastSnapshotID, &state.AgentRequestedAt, &state.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrKnowledgeStateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get knowledge state: %w", err)
	}
	return state, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func requiredKnowledgeDocumentTypes() []string {
	return []string{KnowledgeDocumentProjectBrief, KnowledgeDocumentRepoMap}
}

func isRequiredKnowledgeDocumentType(documentType string) bool {
	for _, requiredType := range requiredKnowledgeDocumentTypes() {
		if documentType == requiredType {
			return true
		}
	}
	return false
}

func countCurrentRequiredDocuments(tx *sql.Tx, projectID string, factsRevision int64) (int, error) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(requiredKnowledgeDocumentTypes())), ",")
	args := []any{projectID, factsRevision, KnowledgeDocumentCurrent}
	for _, documentType := range requiredKnowledgeDocumentTypes() {
		args = append(args, documentType)
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT type) FROM knowledge_documents
		WHERE project_id = ? AND facts_revision = ? AND status = ? AND type IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count current knowledge documents: %w", err)
	}
	return count, nil
}
