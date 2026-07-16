package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"handshake/internal/db"
)

func newTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestChecker_CachesReleaseAndUsesETag(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			if got := r.Header.Get("If-None-Match"); got != `"release-1"` {
				t.Errorf("If-None-Match = %q, want release ETag", got)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"release-1"`)
		_, _ = w.Write([]byte(`{"tag_name":"v0.19.0","html_url":"https://example.test/releases/v0.19.0"}`))
	}))
	defer server.Close()

	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	checker := &Checker{Client: server.Client(), ReleaseURL: server.URL, Now: func() time.Time { return now }}
	database := newTestDB(t)

	status, checked, err := checker.Check(context.Background(), database, "v0.18.1", true)
	if err != nil || !checked {
		t.Fatalf("first check = (%+v, %t, %v)", status, checked, err)
	}
	if status.LatestVersion != "v0.19.0" || status.ReleaseURL == "" || status.ETag == "" {
		t.Fatalf("unexpected first status: %+v", status)
	}

	now = now.Add(time.Hour)
	status, checked, err = checker.Check(context.Background(), database, "v0.18.1", true)
	if err != nil || !checked {
		t.Fatalf("second check = (%+v, %t, %v)", status, checked, err)
	}
	if status.LatestVersion != "v0.19.0" || status.LastError != "" {
		t.Fatalf("304 lost cached release: %+v", status)
	}
}

func TestDueAndVersionComparison(t *testing.T) {
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	if !Due(&db.UpdateStatus{}, now) {
		t.Fatal("empty status should be due")
	}
	if Due(&db.UpdateStatus{LastCheckedAt: now.Add(-6 * 24 * time.Hour).Unix()}, now) {
		t.Fatal("successful six-day-old check should not be due")
	}
	if !Due(&db.UpdateStatus{LastCheckedAt: now.Add(-2 * time.Hour).Unix(), LastError: "offline"}, now) {
		t.Fatal("failed two-hour-old check should be due")
	}
	if !IsNewer("v0.18.1", "v0.19.0") {
		t.Fatal("expected newer release")
	}
	if IsNewer("0.19.0", "v0.19.0") || IsNewer("0.19.0-dev", "v0.20.0") {
		t.Fatal("equal and development versions must not notify")
	}
}
