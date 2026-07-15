// Package knowledge resolves repository identity and turns a session update
// into the factual checkpoint stored by the database layer.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"handshake/internal/db"
	"handshake/internal/git"
)

// Identity separates a logical project from one local clone or worktree.
type Identity struct {
	ProjectID  string
	InstanceID string
	RootPath   string
	RemoteURL  string
	LocalOnly  bool
}

// ResolveProject returns a stable local identity without creating files in the
// repository. A remote identifies a project across clones; a non-Git path is
// deliberately local-only.
func ResolveProject(workingDir, remote string) (*Identity, error) {
	if strings.TrimSpace(workingDir) == "" {
		return nil, nil
	}
	root, isGit, err := git.RepositoryRoot(workingDir)
	if err != nil {
		return nil, err
	}
	if !isGit {
		root, err = canonicalPath(workingDir)
		if err != nil {
			return nil, err
		}
	}
	if root == "" {
		return nil, nil
	}

	normalizedRemote := normalizeRemote(remote)
	identity := &Identity{
		RootPath:  root,
		RemoteURL: normalizedRemote,
		LocalOnly: normalizedRemote == "",
	}
	if identity.LocalOnly {
		identity.ProjectID = "local-" + hash("local-project", root)
	} else {
		identity.ProjectID = "project-" + hash("remote-project", normalizedRemote)
	}
	identity.InstanceID = "instance-" + hash("project-instance", root)
	return identity, nil
}

// RecordCheckpoint is the single knowledge path used after an adapter has
// normalized native session data. Sessions without a working directory remain
// valid legacy checkpoints but cannot be assigned to a project yet.
func RecordCheckpoint(database *db.Database, session *db.Session) (*db.KnowledgeState, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	if err := hydrateSession(database, session); err != nil {
		return nil, err
	}
	state, err := decodeOrCaptureGitState(session)
	if err != nil {
		return nil, err
	}
	remote := ""
	if state != nil {
		remote = state.Remote
	}
	identity, err := ResolveProject(session.WorkingDir, remote)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, database.StoreSession(session)
	}

	snapshot := &db.GitSnapshot{
		HasGit:          state != nil,
		Summary:         session.Summary,
		Decisions:       session.Decisions,
		SourceUpdatedAt: session.UpdatedAt,
		CapturedAt:      time.Now().Unix(),
	}
	if state != nil {
		snapshot.Commit = state.Commit
		snapshot.Branch = state.Branch
		snapshot.Message = state.Message
		snapshot.Status = state.Status
		snapshot.RemoteURL = normalizeRemote(state.Remote)
		if state.CapturedAt > 0 {
			snapshot.CapturedAt = state.CapturedAt
		}
	}
	snapshot.Fingerprint = checkpointFingerprint(session, identity, snapshot)

	return database.RecordKnowledgeCheckpoint(&db.KnowledgeCheckpoint{
		Session: session,
		Project: &db.Project{
			ID:        identity.ProjectID,
			RemoteURL: identity.RemoteURL,
			LocalOnly: identity.LocalOnly,
		},
		Instance: &db.ProjectInstance{
			ID:       identity.InstanceID,
			RootPath: identity.RootPath,
		},
		Snapshot: snapshot,
	})
}

// hydrateSession carries source-authored metadata forward when native stores
// omit it during a later sync. It deliberately does not reuse git_state so a
// new checkpoint captures the current repository state.
func hydrateSession(database *db.Database, session *db.Session) error {
	existing, err := database.GetSession(session.ID)
	if errors.Is(err, db.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load existing session: %w", err)
	}
	if session.Title == "" {
		session.Title = existing.Title
	}
	if session.Agent == "" {
		session.Agent = existing.Agent
	}
	if session.WorkingDir == "" {
		session.WorkingDir = existing.WorkingDir
	}
	if session.Model == "" {
		session.Model = existing.Model
	}
	if session.Summary == "" {
		session.Summary = existing.Summary
	}
	if session.Decisions == "" {
		session.Decisions = existing.Decisions
	}
	if session.CreatedAt == 0 {
		session.CreatedAt = existing.CreatedAt
	}
	return nil
}

func decodeOrCaptureGitState(session *db.Session) (*git.State, error) {
	if session.GitState != "" {
		var state git.State
		if err := json.Unmarshal([]byte(session.GitState), &state); err == nil {
			return &state, nil
		}
	}
	if session.WorkingDir == "" {
		return nil, nil
	}
	state, err := git.CaptureState(session.WorkingDir)
	if err != nil || state == nil {
		return state, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode captured git state: %w", err)
	}
	session.GitState = string(encoded)
	return state, nil
}

func checkpointFingerprint(session *db.Session, identity *Identity, snapshot *db.GitSnapshot) string {
	parts := []string{
		session.ID,
		session.Agent,
		identity.ProjectID,
		identity.InstanceID,
		session.Title,
		session.Summary,
		session.Decisions,
		fmt.Sprintf("%d", session.UpdatedAt),
		snapshot.Commit,
		snapshot.Branch,
		snapshot.Message,
		snapshot.Status,
		snapshot.RemoteURL,
	}
	return hash("checkpoint", strings.Join(parts, "\x00"))
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

func normalizeRemote(remote string) string {
	remote = strings.TrimSpace(strings.TrimSuffix(remote, "/"))
	remote = strings.TrimSuffix(remote, ".git")
	if parsed, err := url.Parse(remote); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host) + "/" + strings.Trim(parsed.Path, "/")
	}
	if at := strings.Index(remote, "@"); at > 0 && !strings.Contains(remote[:at], "://") {
		if colon := strings.Index(remote[at:], ":"); colon >= 0 {
			host := strings.ToLower(remote[at+1 : at+colon])
			path := strings.TrimPrefix(remote[at+colon+1:], "/")
			return host + "/" + path
		}
	}
	return remote
}

func hash(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + value))
	return hex.EncodeToString(sum[:])
}
