package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrSessionNotFound = errors.New("session not found")

type Session struct {
	ID         string
	ProjectID  string
	Title      string
	Agent      string
	WorkingDir string
	Model      string
	// Summary is optional handoff state written by the source agent at
	// checkpoint time (current state, next steps, constraints).
	Summary string
	// Decisions is a newline-separated list of key decisions made during the
	// session, written by the source agent at checkpoint time.
	Decisions string
	GitState  string
	CreatedAt int64
	UpdatedAt int64
}

// SessionListItem is the enriched read model used by session discovery. Project
// metadata is joined at read time so the canonical session record remains
// independent of presentation concerns.
type SessionListItem struct {
	ID               string
	ProjectID        string
	Title            string
	Agent            string
	WorkingDir       string
	UpdatedAt        int64
	ProjectRemoteURL string
	ProjectRoot      string
}

type Message struct {
	ID        string
	SessionID string
	Role      string
	Content   string
	CreatedAt int64
}

type Brief struct {
	SessionID   string
	Content     string
	GeneratedAt int64
}

// Project identifies one logical codebase. Remote-backed projects can span
// local clones; local-only projects are scoped to a canonical directory.
type Project struct {
	ID        string
	RemoteURL string
	LocalOnly bool
	CreatedAt int64
	UpdatedAt int64
}

// ProjectInstance identifies one local clone or worktree of a project.
type ProjectInstance struct {
	ID        string
	ProjectID string
	RootPath  string
	CreatedAt int64
	UpdatedAt int64
}

// GitSnapshot is an immutable factual checkpoint. It intentionally keeps the
// source summary and decisions alongside the Git state that produced them.
type GitSnapshot struct {
	ID              int64
	ProjectID       string
	InstanceID      string
	SessionID       string
	Fingerprint     string
	HasGit          bool
	Commit          string
	Branch          string
	Message         string
	Status          string
	RemoteURL       string
	Summary         string
	Decisions       string
	SourceUpdatedAt int64
	CapturedAt      int64
}

const (
	KnowledgeRefreshPending = "refresh-pending"
	KnowledgeCurrent        = "current"
	KnowledgeFactsAIStale   = "facts-current-ai-stale"

	KnowledgeDocumentProjectBrief = "project-brief"
	KnowledgeDocumentRepoMap      = "repo-map"
	KnowledgeDocumentCurrent      = "current"
	KnowledgeDocumentStale        = "stale"
)

// KnowledgeState compares the latest deterministic facts with the facts
// represented by the most recent AI-authored knowledge document.
type KnowledgeState struct {
	ProjectID      string
	FactsRevision  int64
	AIRevision     int64
	Status         string
	LastSnapshotID int64
	// AgentRequestedAt is when a live agent was last handed a refresh
	// instruction. The authoring check treats it as a cooldown so lifecycle
	// hooks do not interrupt the agent every turn; background authoring is
	// not throttled by it.
	AgentRequestedAt int64
	UpdatedAt        int64
}

// KnowledgeDocument records provenance for a future OKF document. The
// document writer is introduced in the next phase; this schema establishes
// the provenance contract now.
type KnowledgeDocument struct {
	ProjectID       string
	Path            string
	Type            string
	FactsRevision   int64
	SourceSessionID string
	SourceCommit    string
	Evidence        string
	Status          string
	ContentHash     string
	GeneratedBy     string
	GeneratedAt     int64
}

// KnowledgeCheckpoint is the one transactional input for a session update,
// its project identity, and its immutable factual snapshot.
type KnowledgeCheckpoint struct {
	Session  *Session
	Project  *Project
	Instance *ProjectInstance
	Snapshot *GitSnapshot
}

type Database struct {
	db *sql.DB
}

func New(dbPath string) (*Database, error) {
	databaseDir := path.Dir(dbPath)
	if err := os.MkdirAll(databaseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}
	if err := os.Chmod(databaseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to protect database directory: %w", err)
	}
	// Harden existing installations before SQLite opens the file. New database,
	// WAL, and shared-memory files are protected again after schema setup.
	for _, candidate := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := chmodIfExists(candidate, 0600); err != nil {
			return nil, fmt.Errorf("failed to protect database file: %w", err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// SQLite serializes writes on a single database file. Limiting the pool to
	// one connection prevents concurrent writers from multiple pooled
	// connections raising SQLITE_BUSY (which busy_timeout only sometimes
	// resolves). Reads and writes queue on the same connection; with WAL and
	// fast indexed queries this is not a bottleneck for this workload.
	conn.SetMaxOpenConns(1)

	db := &Database{db: conn}
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}
	for _, candidate := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := chmodIfExists(candidate, 0600); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to protect database file: %w", err)
		}
	}

	return db, nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Database) initSchema() error {
	schema := `CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    agent       TEXT NOT NULL,
    working_dir TEXT,
    model       TEXT,
    summary     TEXT NOT NULL DEFAULT '',
    decisions   TEXT NOT NULL DEFAULT '',
    git_state   TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

	CREATE TABLE IF NOT EXISTS messages (
		id          TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		role        TEXT NOT NULL,
		content     TEXT NOT NULL,
		created_at  INTEGER NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);

	CREATE TABLE IF NOT EXISTS briefs (
		session_id    TEXT PRIMARY KEY,
		content       TEXT NOT NULL,
		generated_at  INTEGER NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT);

	CREATE TABLE IF NOT EXISTS projects (
		id          TEXT PRIMARY KEY,
		remote_url  TEXT NOT NULL DEFAULT '',
		local_only  INTEGER NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS project_instances (
		id          TEXT PRIMARY KEY,
		project_id  TEXT NOT NULL,
		root_path   TEXT NOT NULL,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_project_instances_project ON project_instances(project_id);

	CREATE TABLE IF NOT EXISTS git_snapshots (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id        TEXT NOT NULL,
		instance_id       TEXT NOT NULL,
		session_id        TEXT NOT NULL,
		fingerprint       TEXT NOT NULL,
		has_git           INTEGER NOT NULL DEFAULT 0,
		commit_hash       TEXT NOT NULL DEFAULT '',
		branch            TEXT NOT NULL DEFAULT '',
		message           TEXT NOT NULL DEFAULT '',
		worktree_status   TEXT NOT NULL DEFAULT '',
		remote_url        TEXT NOT NULL DEFAULT '',
		summary           TEXT NOT NULL DEFAULT '',
		decisions         TEXT NOT NULL DEFAULT '',
		source_updated_at INTEGER NOT NULL DEFAULT 0,
		captured_at       INTEGER NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
		FOREIGN KEY (instance_id) REFERENCES project_instances(id) ON DELETE CASCADE,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_git_snapshots_project ON git_snapshots(project_id, id DESC);
	CREATE INDEX IF NOT EXISTS idx_git_snapshots_session ON git_snapshots(session_id, id DESC);

	CREATE TABLE IF NOT EXISTS knowledge_state (
		project_id         TEXT PRIMARY KEY,
		facts_revision     INTEGER NOT NULL DEFAULT 0,
		ai_revision        INTEGER NOT NULL DEFAULT 0,
		status             TEXT NOT NULL,
		last_snapshot_id   INTEGER NOT NULL DEFAULT 0,
		agent_requested_at INTEGER NOT NULL DEFAULT 0,
		updated_at         INTEGER NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS knowledge_documents (
		project_id        TEXT NOT NULL,
		path              TEXT NOT NULL,
		type              TEXT NOT NULL,
		facts_revision    INTEGER NOT NULL,
		source_session_id TEXT NOT NULL DEFAULT '',
		source_commit     TEXT NOT NULL DEFAULT '',
		evidence          TEXT NOT NULL DEFAULT '',
		status            TEXT NOT NULL,
		content_hash      TEXT NOT NULL DEFAULT '',
		generated_by      TEXT NOT NULL DEFAULT '',
		generated_at      INTEGER NOT NULL,
		PRIMARY KEY (project_id, path),
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS knowledge_authoring_jobs (
		project_id      TEXT PRIMARY KEY,
		target_revision INTEGER NOT NULL,
		state           TEXT NOT NULL,
		attempts        INTEGER NOT NULL DEFAULT 0,
		not_before      INTEGER NOT NULL DEFAULT 0,
		claimed_at      INTEGER NOT NULL DEFAULT 0,
		last_error      TEXT NOT NULL DEFAULT '',
		updated_at      INTEGER NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_knowledge_authoring_jobs_due
		ON knowledge_authoring_jobs(state, not_before, updated_at);

	CREATE TABLE IF NOT EXISTS update_status (
		id                INTEGER PRIMARY KEY CHECK (id = 1),
		last_checked_at   INTEGER NOT NULL DEFAULT 0,
		etag              TEXT NOT NULL DEFAULT '',
		installed_version TEXT NOT NULL DEFAULT '',
		latest_version    TEXT NOT NULL DEFAULT '',
		release_url       TEXT NOT NULL DEFAULT '',
		last_error        TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS telemetry_counters (
		name  TEXT PRIMARY KEY,
		count INTEGER NOT NULL DEFAULT 0
	);

	CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		content='messages',
		content_rowid='rowid'
	);

	CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
	END;
	CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
	END;
	CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
		INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
	END;`

	if _, err := d.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	// Migration for databases created before the summary column existed.
	if _, err := d.db.Exec("ALTER TABLE sessions ADD COLUMN summary TEXT NOT NULL DEFAULT ''"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate sessions table: %w", err)
	}

	// Migration for databases created before git_state existed.
	if _, err := d.db.Exec("ALTER TABLE sessions ADD COLUMN git_state TEXT NOT NULL DEFAULT ''"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate sessions git_state: %w", err)
	}

	// Migration for databases created before decisions existed.
	if _, err := d.db.Exec("ALTER TABLE sessions ADD COLUMN decisions TEXT NOT NULL DEFAULT ''"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate sessions decisions: %w", err)
	}

	// Migration for databases created before the agent-request cooldown existed.
	if _, err := d.db.Exec("ALTER TABLE knowledge_state ADD COLUMN agent_requested_at INTEGER NOT NULL DEFAULT 0"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate knowledge_state agent_requested_at: %w", err)
	}

	// Existing databases gain an initially empty project link. Historical sessions are
	// linked lazily when their working directory is observed again; this avoids
	// fabricating Git history that Handshake never captured.
	if _, err := d.db.Exec("ALTER TABLE sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT ''"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate sessions project_id: %w", err)
	}
	if _, err := d.db.Exec("CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id)"); err != nil {
		return fmt.Errorf("failed to index session projects: %w", err)
	}

	// Populate FTS index from messages that existed before the FTS table was added.
	// Triggers handle all inserts going forward; this runs exactly once.
	var ftsBuilt string
	d.db.QueryRow("SELECT value FROM schema_meta WHERE key = 'fts_built'").Scan(&ftsBuilt)
	if ftsBuilt == "" {
		if _, err := d.db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('rebuild')"); err != nil {
			return fmt.Errorf("failed to build FTS index: %w", err)
		}
		if _, err := d.db.Exec("INSERT OR REPLACE INTO schema_meta VALUES ('fts_built', '1')"); err != nil {
			return fmt.Errorf("failed to record FTS migration: %w", err)
		}
	}

	return nil
}

// SearchResult is a single message match returned from a full-text search.
type SearchResult struct {
	SessionID    string
	SessionTitle string
	SessionAgent string
	Role         string
	Content      string
	CreatedAt    int64
}

// SearchMessages searches within a single session's messages using FTS5.
func (d *Database) SearchMessages(sessionID, query string, limit int) ([]*SearchResult, error) {
	rows, err := d.db.Query(`
		SELECT m.session_id, s.title, s.agent, m.role, m.content, m.created_at
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		JOIN sessions s ON m.session_id = s.id
		WHERE messages_fts MATCH ? AND m.session_id = ?
		ORDER BY messages_fts.rank
		LIMIT ?`,
		query, sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows)
}

// SearchAllMessages searches across all sessions' messages using FTS5.
// If agent is non-empty, results are filtered to that agent.
func (d *Database) SearchAllMessages(query string, limit int, agent string) ([]*SearchResult, error) {
	q := `
		SELECT m.session_id, s.title, s.agent, m.role, m.content, m.created_at
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		JOIN sessions s ON m.session_id = s.id
		WHERE messages_fts MATCH ?`
	args := []any{query}
	if agent != "" {
		q += " AND s.agent = ?"
		args = append(args, agent)
	}
	q += " ORDER BY messages_fts.rank LIMIT ?"
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows)
}

func scanSearchResults(rows *sql.Rows) ([]*SearchResult, error) {
	var results []*SearchResult
	for rows.Next() {
		r := &SearchResult{}
		if err := rows.Scan(&r.SessionID, &r.SessionTitle, &r.SessionAgent, &r.Role, &r.Content, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan search result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// StoreSession inserts or updates a session. created_at is preserved on
// conflict so re-checkpointing the same session is idempotent. Empty fields
// in a checkpoint do not erase metadata recorded by an earlier checkpoint.
func prepareSession(session *Session) {
	now := time.Now().Unix()
	if session.CreatedAt == 0 {
		session.CreatedAt = now
	}
	if session.UpdatedAt == 0 {
		session.UpdatedAt = now
	}
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func storeSession(exec sqlExecer, session *Session) error {
	_, err := exec.Exec(
		`INSERT INTO sessions (id, project_id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at)
	     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	     ON CONFLICT(id) DO UPDATE SET
	       project_id  = CASE WHEN excluded.project_id != '' THEN excluded.project_id ELSE sessions.project_id END,
	       title       = CASE WHEN excluded.title != '' THEN excluded.title ELSE sessions.title END,
       agent       = CASE WHEN excluded.agent != '' THEN excluded.agent ELSE sessions.agent END,
       working_dir = CASE WHEN excluded.working_dir != '' THEN excluded.working_dir ELSE sessions.working_dir END,
       model       = CASE WHEN excluded.model != '' THEN excluded.model ELSE sessions.model END,
       summary     = CASE WHEN excluded.summary != '' THEN excluded.summary ELSE sessions.summary END,
       decisions   = CASE WHEN excluded.decisions != '' THEN excluded.decisions ELSE sessions.decisions END,
       git_state   = CASE WHEN excluded.git_state != '' THEN excluded.git_state ELSE sessions.git_state END,
       updated_at  = excluded.updated_at`,
		session.ID, session.ProjectID, session.Title, session.Agent, session.WorkingDir, session.Model, session.Summary, session.Decisions, session.GitState, session.CreatedAt, session.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store session: %w", err)
	}

	return nil
}

// StoreSession inserts or updates session metadata without creating a
// knowledge snapshot. Adapters should prefer RecordKnowledgeCheckpoint.
func (d *Database) StoreSession(session *Session) error {
	prepareSession(session)
	return storeSession(d.db, session)
}

func (d *Database) StoreMessage(message *Message) error {
	if message.CreatedAt == 0 {
		message.CreatedAt = time.Now().Unix()
	}

	_, err := d.db.Exec(
		`INSERT INTO messages (id, session_id, role, content, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   role       = excluded.role,
		   content    = excluded.content,
		   created_at = excluded.created_at`,
		message.ID, message.SessionID, message.Role, message.Content, message.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store message: %w", err)
	}

	return nil
}

func (d *Database) GetSession(id string) (*Session, error) {
	row := d.db.QueryRow(
		"SELECT id, project_id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at FROM sessions WHERE id = ?",
		id,
	)

	session := &Session{}
	if err := row.Scan(&session.ID, &session.ProjectID, &session.Title, &session.Agent, &session.WorkingDir, &session.Model, &session.Summary, &session.Decisions, &session.GitState, &session.CreatedAt, &session.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return session, nil
}

func (d *Database) ListSessions(agent string) ([]*Session, error) {
	query := "SELECT id, project_id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at FROM sessions"
	args := []any{}
	if agent != "" {
		query += " WHERE agent = ?"
		args = append(args, agent)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		session := &Session{}
		if err := rows.Scan(&session.ID, &session.ProjectID, &session.Title, &session.Agent, &session.WorkingDir, &session.Model, &session.Summary, &session.Decisions, &session.GitState, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
}

// ListSessionItems returns sessions with enough project metadata for clients to
// display stable, human-readable project and directory information.
func (d *Database) ListSessionItems(agent string) ([]*SessionListItem, error) {
	query := `SELECT s.id, s.project_id, s.title, s.agent, s.working_dir, s.updated_at,
		COALESCE(p.remote_url, ''),
		COALESCE((
			SELECT pi.root_path
			FROM git_snapshots gs
			JOIN project_instances pi ON pi.id = gs.instance_id
			WHERE gs.session_id = s.id
			ORDER BY gs.id DESC
			LIMIT 1
		), '')
		FROM sessions s
		LEFT JOIN projects p ON p.id = s.project_id`
	args := []any{}
	if agent != "" {
		query += " WHERE s.agent = ?"
		args = append(args, agent)
	}
	query += " ORDER BY s.updated_at DESC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list enriched sessions: %w", err)
	}
	defer rows.Close()

	var items []*SessionListItem
	for rows.Next() {
		item := &SessionListItem{}
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Title, &item.Agent,
			&item.WorkingDir, &item.UpdatedAt, &item.ProjectRemoteURL, &item.ProjectRoot); err != nil {
			return nil, fmt.Errorf("failed to scan enriched session: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating enriched sessions: %w", err)
	}
	return items, nil
}

func (d *Database) StoreBrief(sessionID string, content string) error {
	now := time.Now().Unix()

	_, err := d.db.Exec(
		`INSERT INTO briefs (session_id, content, generated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   content      = excluded.content,
		   generated_at = excluded.generated_at`,
		sessionID, content, now,
	)
	if err != nil {
		return fmt.Errorf("failed to store brief: %w", err)
	}

	return nil
}

func (d *Database) GetBrief(sessionID string) (*Brief, error) {
	row := d.db.QueryRow(
		"SELECT session_id, content, generated_at FROM briefs WHERE session_id = ?",
		sessionID,
	)

	brief := &Brief{}
	if err := row.Scan(&brief.SessionID, &brief.Content, &brief.GeneratedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("brief not found")
		}
		return nil, fmt.Errorf("failed to get brief: %w", err)
	}

	return brief, nil
}

func (d *Database) GetMessages(sessionID string) ([]*Message, error) {
	rows, err := d.db.Query(
		"SELECT id, session_id, role, content, created_at FROM messages WHERE session_id = ? ORDER BY created_at ASC, id ASC",
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	return messages, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}
