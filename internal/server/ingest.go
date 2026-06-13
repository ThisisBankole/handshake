package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"handshake/internal/adapters"
)

// ingestPayload is the JSON body POSTed to /ingest, primarily by the bundled
// OpenCode plugin on session.updated events.
type ingestPayload struct {
	Agent   string `json:"agent"`
	Session struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		WorkingDir string `json:"working_dir"`
		Model      string `json:"model"`
		Summary    string `json:"summary"`
		CreatedAt  int64  `json:"created_at"`
		UpdatedAt  int64  `json:"updated_at"`
	} `json:"session"`
	Messages []struct {
		ID        string `json:"id"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		CreatedAt int64  `json:"created_at"`
	} `json:"messages"`
}

// generateBriefPayload is the JSON body POSTed to /generate-brief.
// Called autonomously by agent plugins (e.g. OpenCode session.idle hook)
// to regenerate and cache the brief for a session without a full checkpoint.
type generateBriefPayload struct {
	SessionID string `json:"session_id"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload ingestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Agent == "" || payload.Session.ID == "" {
		http.Error(w, "agent and session.id are required", http.StatusBadRequest)
		return
	}

	session := &adapters.SessionData{
		ID:         payload.Session.ID,
		Agent:      payload.Agent,
		Title:      payload.Session.Title,
		WorkingDir: payload.Session.WorkingDir,
		Model:      payload.Session.Model,
		Summary:    payload.Session.Summary,
		CreatedAt:  payload.Session.CreatedAt,
		UpdatedAt:  payload.Session.UpdatedAt,
	}
	for _, m := range payload.Messages {
		if m.Content == "" {
			continue
		}
		session.Messages = append(session.Messages, adapters.MessageData{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		})
	}

	if err := adapters.Ingest(s.db, session); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"session_id":%q,"messages":%d}`+"\n", session.ID, len(session.Messages))
}

// handleGenerateBrief regenerates and caches the brief for a session.
// Called autonomously by agent plugins when a session goes idle or compacts,
// so the brief is always fresh and ready when the user switches agents.
func (s *Server) handleGenerateBrief(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload generateBriefPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if payload.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	brief, err := s.engine.GenerateBrief(payload.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"session_id":%q,"title":%q}`+"\n", brief.SessionID, brief.Title)
}