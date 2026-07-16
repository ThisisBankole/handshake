package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cursorplugin "handshake/plugins/cursor"
)

func TestCursorMCPConfig_PreservesOtherServers(t *testing.T) {
	config := map[string]any{
		"mcpServers": map[string]any{
			"other": map[string]any{"command": "other-mcp"},
		},
	}

	cursorInjectMCP(config, "http://localhost:8766/mcp")
	servers := config["mcpServers"].(map[string]any)
	if servers["other"] == nil {
		t.Fatal("existing MCP server was removed")
	}
	handshake := servers["handshake"].(map[string]any)
	if handshake["url"] != "http://localhost:8766/mcp" {
		t.Fatalf("Handshake URL = %q", handshake["url"])
	}
	if !cursorRemoveMCP(config) {
		t.Fatal("cursorRemoveMCP() = false, want true")
	}
	if servers["other"] == nil || servers["handshake"] != nil {
		t.Fatalf("unexpected MCP servers after removal: %#v", servers)
	}
}

func TestCursorStopHookConfig_IsIdempotentAndPreservesOtherHooks(t *testing.T) {
	config := map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"stop": []any{map[string]any{"command": "node user-stop.js"}},
		},
	}
	command := "/usr/bin/python3 /tmp/handshake_cursor_stop.py"
	if !cursorInjectStopHook(config, command) {
		t.Fatal("first cursorInjectStopHook() = false, want true")
	}
	if cursorInjectStopHook(config, command) {
		t.Fatal("second cursorInjectStopHook() = true, want false")
	}
	hooks := config["hooks"].(map[string]any)
	stops := hooks["stop"].([]any)
	if len(stops) != 2 {
		t.Fatalf("stop hooks = %d, want 2", len(stops))
	}
	if !cursorRemoveStopHook(config) {
		t.Fatal("cursorRemoveStopHook() = false, want true")
	}
	stops = hooks["stop"].([]any)
	if len(stops) != 1 || stops[0].(map[string]any)["command"] != "node user-stop.js" {
		t.Fatalf("user stop hook was not preserved: %#v", stops)
	}
}

func TestCursorStopHook_IngestsTranscriptAndRequestsAuthoring(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to execute the embedded Cursor hook")
	}

	ingested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ingest":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode ingest payload: %v", err)
			}
			if payload["agent"] != "cursor" {
				t.Errorf("agent = %q, want cursor", payload["agent"])
			}
			ingested = true
		case "/knowledge-authoring-check":
			_, _ = w.Write([]byte(`{"pending":true,"project_id":"project-1","facts_revision":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "handshake_cursor_stop.py")
	if err := os.WriteFile(scriptPath, injectBaseURL(cursorplugin.StopHook, server.URL), 0700); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	transcriptPath := filepath.Join(dir, "session.jsonl")
	transcript := "{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"<user_query>Map the repository</user_query>\"}]}}\n" +
		"{\"role\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"I will inspect it.\"}]}}\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	input, err := json.Marshal(map[string]any{
		"conversation_id": "cursor-1",
		"status":          "completed",
		"loop_count":      0,
		"workspace_roots": []string{"/work/project"},
		"transcript_path": transcriptPath,
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	command := exec.Command(python, scriptPath)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if !ingested {
		t.Fatal("hook did not ingest Cursor transcript")
	}
	var response map[string]string
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode hook response %q: %v", output, err)
	}
	if !strings.Contains(response["followup_message"], "publish both project-brief and repo-map") {
		t.Fatalf("unexpected hook response: %q", output)
	}
}
