package db

import (
	"database/sql"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Session struct {
	ID         string
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

type Database struct {
	db *sql.DB
}

func New(dbPath string) (*Database, error) {
	if err := os.MkdirAll(path.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
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

	return db, nil
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
// conflict so re-checkpointing the same session is idempotent.
func (d *Database) StoreSession(session *Session) error {
	now := time.Now().Unix()
	if session.CreatedAt == 0 {
		session.CreatedAt = now
	}
	if session.UpdatedAt == 0 {
		session.UpdatedAt = now
	}

	_, err := d.db.Exec(
		`INSERT INTO sessions (id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(id) DO UPDATE SET
       title       = excluded.title,
       agent       = excluded.agent,
       working_dir = excluded.working_dir,
       model       = excluded.model,
       summary     = CASE WHEN excluded.summary != '' THEN excluded.summary ELSE sessions.summary END,
       decisions   = CASE WHEN excluded.decisions != '' THEN excluded.decisions ELSE sessions.decisions END,
       git_state   = CASE WHEN excluded.git_state != '' THEN excluded.git_state ELSE sessions.git_state END,
       updated_at  = excluded.updated_at`,
		session.ID, session.Title, session.Agent, session.WorkingDir, session.Model, session.Summary, session.Decisions, session.GitState, session.CreatedAt, session.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store session: %w", err)
	}

	return nil
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
		"SELECT id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at FROM sessions WHERE id = ?",
		id,
	)

	session := &Session{}
	if err := row.Scan(&session.ID, &session.Title, &session.Agent, &session.WorkingDir, &session.Model, &session.Summary, &session.Decisions, &session.GitState, &session.CreatedAt, &session.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found")
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return session, nil
}

func (d *Database) ListSessions(agent string) ([]*Session, error) {
	query := "SELECT id, title, agent, working_dir, model, summary, decisions, git_state, created_at, updated_at FROM sessions"
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
		if err := rows.Scan(&session.ID, &session.Title, &session.Agent, &session.WorkingDir, &session.Model, &session.Summary, &session.Decisions, &session.GitState, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
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
