package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"handshake/internal/adapters"
	"handshake/internal/authoring"
	"handshake/internal/db"
	"handshake/internal/git"
	"handshake/internal/knowledge"
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
		GitState   string `json:"git_state"`
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

type knowledgeAuthoringCheckPayload struct {
	WorkingDir string `json:"working_dir"`
}

type knowledgeAuthoringCheckResponse struct {
	Pending       bool   `json:"pending"`
	ProjectID     string `json:"project_id,omitempty"`
	FactsRevision int64  `json:"facts_revision,omitempty"`
	Status        string `json:"status,omitempty"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.count("ingests")

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
		GitState:   payload.Session.GitState,
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

	s.enrichIngestedSession(session)

	if err := adapters.Ingest(s.db, filepath.Join(s.homeDir, ".handshake", "knowledge"), session); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"session_id":%q,"messages":%d}`+"\n", session.ID, len(session.Messages))
}

// handleKnowledgeAuthoringCheck is intentionally small enough for lifecycle
// hooks. It reports only whether the current project needs its AI-authored OKF
// documents refreshed; the agent skill reads the actual factual documents.
func (s *Server) handleKnowledgeAuthoringCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload knowledgeAuthoringCheckPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	context, err := knowledge.GetProjectContext(s.db, payload.WorkingDir)
	if errors.Is(err, db.ErrKnowledgeStateNotFound) {
		writeKnowledgeAuthoringCheck(w, knowledgeAuthoringCheckResponse{})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pending := context.State.Status != db.KnowledgeCurrent
	if pending && !s.agentRefreshAllowed(context.State) {
		// Stale, but the agent was asked recently. Report not-pending so the
		// hook stays silent; the background authoring job still covers it.
		pending = false
	}
	if pending {
		if err := s.db.MarkKnowledgeAgentRequested(context.ProjectID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeKnowledgeAuthoringCheck(w, knowledgeAuthoringCheckResponse{
		Pending:       pending,
		ProjectID:     context.ProjectID,
		FactsRevision: context.State.FactsRevision,
		Status:        context.State.Status,
	})
}

// agentRefreshAllowed decides whether a live agent may be interrupted with a
// refresh instruction. First-ever authoring always may: no documents exist
// yet, so silence would leave the project without knowledge. After that, one
// interruption per cooldown window.
func (s *Server) agentRefreshAllowed(state *db.KnowledgeState) bool {
	if state.AIRevision == 0 {
		return true
	}
	config, err := authoring.LoadConfig(s.homeDir)
	if err != nil {
		config = authoring.DefaultConfig()
	}
	if config.AgentCooldownSeconds == 0 {
		return false
	}
	cooldown := time.Duration(config.AgentCooldownSeconds) * time.Second
	return time.Since(time.Unix(state.AgentRequestedAt, 0)) >= cooldown
}

func writeKnowledgeAuthoringCheck(w http.ResponseWriter, response knowledgeAuthoringCheckResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

func (s *Server) enrichIngestedSession(session *adapters.SessionData) {
	if session.Agent == "opencode" {
		if native, err := s.readNativeSession(session.Agent, session.ID); err == nil {
			if session.Title == "" {
				session.Title = native.Title
			}
			if session.WorkingDir == "" {
				session.WorkingDir = native.WorkingDir
			}
			if session.Model == "" {
				session.Model = native.Model
			}
			if session.CreatedAt == 0 {
				session.CreatedAt = native.CreatedAt
			}
			if session.UpdatedAt == 0 {
				session.UpdatedAt = native.UpdatedAt
			}
			if len(session.Messages) == 0 {
				session.Messages = native.Messages
			}
		}
	}

	if session.GitState != "" || session.WorkingDir == "" {
		return
	}
	state, err := git.CaptureState(session.WorkingDir)
	if err != nil || state == nil {
		return
	}
	if session.CreatedAt > 0 {
		state.Commits = git.CommitsBetween(session.WorkingDir,
			session.CreatedAt-300, session.UpdatedAt+300)
	}
	encoded, err := json.Marshal(state)
	if err == nil {
		session.GitState = string(encoded)
	}
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
