package db

import (
	"path/filepath"
	"testing"
)

func openTelemetryTestDB(t *testing.T) *Database {
	t.Helper()
	database, err := New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestTelemetryCounters_IncrementAndDrain(t *testing.T) {
	database := openTelemetryTestDB(t)

	for range 3 {
		if err := database.IncrementTelemetryCounter("checkpoints"); err != nil {
			t.Fatalf("increment: %v", err)
		}
	}
	if err := database.IncrementTelemetryCounter("restores"); err != nil {
		t.Fatalf("increment: %v", err)
	}

	counters, err := database.DrainTelemetryCounters()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if counters["checkpoints"] != 3 || counters["restores"] != 1 {
		t.Fatalf("counters = %+v", counters)
	}

	// Draining resets: a second drain is empty.
	counters, err = database.DrainTelemetryCounters()
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if len(counters) != 0 {
		t.Fatalf("drain did not reset counters: %+v", counters)
	}
}

func TestActiveAgentsSince(t *testing.T) {
	database := openTelemetryTestDB(t)

	sessions := []*Session{
		{ID: "s1", Title: "one", Agent: "claude-code", CreatedAt: 100, UpdatedAt: 100},
		{ID: "s2", Title: "two", Agent: "codex", CreatedAt: 200, UpdatedAt: 200},
		{ID: "s3", Title: "three", Agent: "claude-code", CreatedAt: 300, UpdatedAt: 300},
	}
	for _, session := range sessions {
		if err := database.StoreSession(session); err != nil {
			t.Fatalf("store %s: %v", session.ID, err)
		}
	}

	agents, err := database.ActiveAgentsSince(150)
	if err != nil {
		t.Fatalf("ActiveAgentsSince: %v", err)
	}
	if len(agents) != 2 || agents[0] != "claude-code" || agents[1] != "codex" {
		t.Fatalf("agents since 150 = %v", agents)
	}

	agents, err = database.ActiveAgentsSince(250)
	if err != nil {
		t.Fatalf("ActiveAgentsSince: %v", err)
	}
	if len(agents) != 1 || agents[0] != "claude-code" {
		t.Fatalf("agents since 250 = %v", agents)
	}
}
