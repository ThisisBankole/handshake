package db

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

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
