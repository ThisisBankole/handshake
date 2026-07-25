package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNew_ProtectsDatabaseFilesAndDirectory(t *testing.T) {
	root := t.TempDir()
	databaseDir := filepath.Join(root, ".handshake")
	databasePath := filepath.Join(databaseDir, "sessions.db")

	database, err := New(databasePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer database.Close()

	for path, want := range map[string]os.FileMode{
		databaseDir:  0700,
		databasePath: 0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions = %#o, want %#o", path, got, want)
		}
	}
}

func TestNew_HardensExistingDatabasePermissions(t *testing.T) {
	root := t.TempDir()
	databaseDir := filepath.Join(root, ".handshake")
	databasePath := filepath.Join(databaseDir, "sessions.db")
	database, err := New(databasePath)
	if err != nil {
		t.Fatalf("initial New failed: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Chmod(databaseDir, 0755); err != nil {
		t.Fatalf("Chmod directory: %v", err)
	}
	if err := os.Chmod(databasePath, 0644); err != nil {
		t.Fatalf("Chmod database: %v", err)
	}

	database, err = New(databasePath)
	if err != nil {
		t.Fatalf("reopen New failed: %v", err)
	}
	defer database.Close()
	dirInfo, _ := os.Stat(databaseDir)
	fileInfo, _ := os.Stat(databasePath)
	if dirInfo.Mode().Perm() != 0700 || fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("permissions after reopen = dir %#o, file %#o", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestListSessionItemsFiltersAndOrdersByUpdatedTime(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	for _, session := range []*Session{
		{ID: "older", Title: "Older", Agent: "claude-code", UpdatedAt: 100},
		{ID: "other-agent", Title: "Other", Agent: "codex", UpdatedAt: 300},
		{ID: "newer", Title: "Newer", Agent: "claude-code", UpdatedAt: 200},
	} {
		if err := database.StoreSession(session); err != nil {
			t.Fatalf("StoreSession(%s): %v", session.ID, err)
		}
	}

	items, err := database.ListSessionItems("claude-code")
	if err != nil {
		t.Fatalf("ListSessionItems: %v", err)
	}
	if len(items) != 2 || items[0].ID != "newer" || items[1].ID != "older" {
		t.Fatalf("ListSessionItems order/filter = %+v", items)
	}
}

func TestNew_LimitsOpenConnections(t *testing.T) {
	d, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer d.Close()

	if got := d.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1 (unlimited pool causes SQLITE_BUSY under concurrent writes)", got)
	}
}

func TestConcurrentWrites_AllSucceed(t *testing.T) {
	d, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer d.Close()

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := d.StoreSession(&Session{
				ID:         fmt.Sprintf("sess-%d", i),
				Title:      "concurrent session",
				Agent:      "test",
				WorkingDir: "/tmp",
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent StoreSession failed: %v", err)
	}

	sessions, err := d.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != n {
		t.Fatalf("got %d sessions, want %d", len(sessions), n)
	}
}

func TestStoreSession_PreservesMetadataFromPartialCheckpoint(t *testing.T) {
	d, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer d.Close()

	if err := d.StoreSession(&Session{
		ID:         "session-1",
		Title:      "Fix login flow",
		Agent:      "claude",
		WorkingDir: "/projects/app",
		Model:      "claude-sonnet",
	}); err != nil {
		t.Fatalf("initial StoreSession failed: %v", err)
	}

	if err := d.StoreSession(&Session{ID: "session-1"}); err != nil {
		t.Fatalf("partial StoreSession failed: %v", err)
	}

	session, err := d.GetSession("session-1")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if session.Title != "Fix login flow" {
		t.Errorf("Title = %q, want %q", session.Title, "Fix login flow")
	}
	if session.Agent != "claude" {
		t.Errorf("Agent = %q, want %q", session.Agent, "claude")
	}
	if session.WorkingDir != "/projects/app" {
		t.Errorf("WorkingDir = %q, want %q", session.WorkingDir, "/projects/app")
	}
	if session.Model != "claude-sonnet" {
		t.Errorf("Model = %q, want %q", session.Model, "claude-sonnet")
	}
}

func TestStoreSession_ReplacesMetadataWhenProvided(t *testing.T) {
	d, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer d.Close()

	if err := d.StoreSession(&Session{
		ID:         "session-1",
		Title:      "Initial title",
		Agent:      "claude",
		WorkingDir: "/projects/old-app",
		Model:      "claude-haiku",
	}); err != nil {
		t.Fatalf("initial StoreSession failed: %v", err)
	}

	if err := d.StoreSession(&Session{
		ID:         "session-1",
		Title:      "Updated title",
		Agent:      "codex",
		WorkingDir: "/projects/new-app",
		Model:      "gpt-5",
	}); err != nil {
		t.Fatalf("updated StoreSession failed: %v", err)
	}

	session, err := d.GetSession("session-1")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if session.Title != "Updated title" {
		t.Errorf("Title = %q, want %q", session.Title, "Updated title")
	}
	if session.Agent != "codex" {
		t.Errorf("Agent = %q, want %q", session.Agent, "codex")
	}
	if session.WorkingDir != "/projects/new-app" {
		t.Errorf("WorkingDir = %q, want %q", session.WorkingDir, "/projects/new-app")
	}
	if session.Model != "gpt-5" {
		t.Errorf("Model = %q, want %q", session.Model, "gpt-5")
	}
}
