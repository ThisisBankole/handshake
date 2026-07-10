// Package sessionmatch resolves a user-provided session ID or title safely.
package sessionmatch

import (
	"fmt"
	"strings"
	"time"

	"handshake/internal/db"
)

// AmbiguousError is returned when a query identifies more than one session.
// Callers should present its candidates and ask for an exact session ID.
type AmbiguousError struct {
	Query      string
	Candidates []*db.Session
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "multiple sessions match %q; rerun with one of these session IDs:", e.Query)
	for _, session := range e.Candidates {
		updated := "unknown"
		if session.UpdatedAt > 0 {
			updated = time.Unix(session.UpdatedAt, 0).Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "\n- %s (%s, %s, updated %s)", session.ID, session.Title, session.Agent, updated)
	}
	return b.String()
}

// Find resolves query to one session. It accepts an exact session ID, an exact
// title, a title substring, or a title containing all query words. A query
// must identify exactly one session; this function never chooses by recency.
func Find(sessions []*db.Session, query string) (*db.Session, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("session ID or title is required")
	}

	for _, session := range sessions {
		if session.ID == q {
			return session, nil
		}
	}

	q = strings.ToLower(q)
	words := strings.Fields(q)
	var exact, substring, allWords []*db.Session
	for _, session := range sessions {
		title := strings.ToLower(session.Title)
		switch {
		case title == q:
			exact = append(exact, session)
		case strings.Contains(title, q):
			substring = append(substring, session)
		case containsAllWords(title, words):
			allWords = append(allWords, session)
		}
	}

	matches := exact
	if len(matches) == 0 {
		matches = substring
	}
	if len(matches) == 0 {
		matches = allWords
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matching %q", query)
	case 1:
		return matches[0], nil
	default:
		return nil, &AmbiguousError{Query: query, Candidates: matches}
	}
}

func containsAllWords(title string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(title, word) {
			return false
		}
	}
	return len(words) > 0
}
