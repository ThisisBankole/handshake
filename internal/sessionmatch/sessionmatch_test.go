package sessionmatch

import (
	"errors"
	"testing"

	"handshake/internal/db"
)

func TestFind_PrefersExactSessionID(t *testing.T) {
	sessions := []*db.Session{
		{ID: "session-1", Title: "Fix login"},
		{ID: "session-2", Title: "session-1 follow-up"},
	}

	session, err := Find(sessions, "session-1")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if session.ID != "session-1" {
		t.Errorf("session ID = %q, want %q", session.ID, "session-1")
	}
}

func TestFind_ReturnsOnlyMatch(t *testing.T) {
	sessions := []*db.Session{
		{ID: "session-1", Title: "Fix login flow"},
		{ID: "session-2", Title: "Update billing page"},
	}

	session, err := Find(sessions, "login")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if session.ID != "session-1" {
		t.Errorf("session ID = %q, want %q", session.ID, "session-1")
	}
}

func TestFind_RejectsAmbiguousMatch(t *testing.T) {
	sessions := []*db.Session{
		{ID: "session-1", Title: "Fix login flow"},
		{ID: "session-2", Title: "Fix login tests"},
	}

	_, err := Find(sessions, "Fix login")
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Find error = %v, want AmbiguousError", err)
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(ambiguous.Candidates))
	}
	if ambiguous.Candidates[0].ID != "session-1" || ambiguous.Candidates[1].ID != "session-2" {
		t.Errorf("candidate IDs = %q, %q, want session-1, session-2", ambiguous.Candidates[0].ID, ambiguous.Candidates[1].ID)
	}
}
