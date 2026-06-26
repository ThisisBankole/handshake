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
