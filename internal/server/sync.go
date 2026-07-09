package server

import (
	"encoding/json"
	"io"
	"net/http"

	"handshake/internal/adapters"
)

// syncPayload is the JSON body POSTed to /sync. An empty body (or empty
// agent) pulls every pullable agent.
type syncPayload struct {
	Agent string `json:"agent"`
}

// syncResultJSON is one agent's pull outcome in the /sync response.
type syncResultJSON struct {
	Agent     string `json:"agent"`
	Scanned   int    `json:"scanned"`
	Imported  int    `json:"imported"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	Failed    int    `json:"failed"`
	Error     string `json:"error,omitempty"`
}

// handleSync pulls sessions from agents' native local storage on demand.
// Used by the TUI's pull action and `handshake pull` when the daemon owns
// the database.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload syncPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && err != io.EOF {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var results []adapters.SyncResult
	if payload.Agent != "" {
		results = []adapters.SyncResult{adapters.SyncAgent(s.db, s.homeDir, payload.Agent)}
	} else {
		results = adapters.SyncAll(s.db, s.homeDir)
	}

	out := make([]syncResultJSON, 0, len(results))
	for _, res := range results {
		j := syncResultJSON{
			Agent:     res.Agent,
			Scanned:   res.Scanned,
			Imported:  res.Imported,
			Updated:   res.Updated,
			Unchanged: res.Unchanged,
			Failed:    res.Failed,
		}
		if res.Err != nil {
			j.Error = res.Err.Error()
		}
		out = append(out, j)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "results": out})
}
