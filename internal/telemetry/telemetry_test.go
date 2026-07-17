package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withTestKey simulates a release build, where the key is injected via
// ldflags. Local builds carry no key and telemetry is off.
func withTestKey(t *testing.T) {
	t.Helper()
	previous := apiKey
	apiKey = "phc_test"
	t.Cleanup(func() { apiKey = previous })
}

func captureEvents(t *testing.T) *[]map[string]any {
	t.Helper()
	withTestKey(t)
	events := &[]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		*events = append(*events, payload)
	}))
	t.Cleanup(server.Close)
	previous := captureURL
	captureURL = server.URL
	t.Cleanup(func() { captureURL = previous })
	return events
}

func TestAnonymousID_PersistsAcrossCalls(t *testing.T) {
	homeDir := t.TempDir()

	first, created, err := AnonymousID(homeDir)
	if err != nil || !created || first == "" {
		t.Fatalf("first call = %q, created %v, err %v", first, created, err)
	}
	second, created, err := AnonymousID(homeDir)
	if err != nil || created || second != first {
		t.Fatalf("second call = %q, created %v, err %v (want %q, false)", second, created, err, first)
	}

	data, err := os.ReadFile(filepath.Join(homeDir, ".handshake", "telemetry_id"))
	if err != nil {
		t.Fatalf("read id file: %v", err)
	}
	if string(data) != first+"\n" {
		t.Fatalf("id file = %q, want %q", data, first+"\n")
	}
}

func TestInstalled_SendsOnceWithPayload(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	Installed(homeDir, "0.21.0", []string{"claude-code", "codex"})
	if len(*events) != 1 {
		t.Fatalf("event count = %d, want 1", len(*events))
	}
	event := (*events)[0]
	if event["event"] != "handshake_installed" || event["api_key"] != apiKey {
		t.Fatalf("event = %+v", event)
	}
	if event["distinct_id"] == "" {
		t.Fatalf("missing distinct_id: %+v", event)
	}
	properties := event["properties"].(map[string]any)
	if properties["version"] != "0.21.0" || properties["os"] == "" || properties["arch"] == "" {
		t.Fatalf("properties = %+v", properties)
	}
	if properties["$process_person_profile"] != false {
		t.Fatalf("expected anonymous event, properties = %+v", properties)
	}
	agents := properties["agents"].([]any)
	if len(agents) != 2 || agents[0] != "claude-code" {
		t.Fatalf("agents = %+v", agents)
	}

	// A second setup run on the same machine must not send another install.
	Installed(homeDir, "0.21.0", nil)
	if len(*events) != 1 {
		t.Fatalf("re-run sent another install event: %d events", len(*events))
	}
}

func TestHeartbeat_SendsEveryCall(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	Heartbeat(homeDir, "0.21.0")
	Heartbeat(homeDir, "0.21.0")
	if len(*events) != 2 {
		t.Fatalf("event count = %d, want 2", len(*events))
	}
	if (*events)[0]["event"] != "handshake_heartbeat" {
		t.Fatalf("event = %+v", (*events)[0])
	}
	if (*events)[0]["distinct_id"] != (*events)[1]["distinct_id"] {
		t.Fatalf("heartbeats used different machine IDs")
	}
}

func TestOptOutSuppressesAllSends(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)
	t.Setenv("HANDSHAKE_NO_TELEMETRY", "1")

	if !Disabled() {
		t.Fatal("Disabled() = false with HANDSHAKE_NO_TELEMETRY set")
	}
	Installed(homeDir, "0.21.0", []string{"claude-code"})
	Heartbeat(homeDir, "0.21.0")
	if len(*events) != 0 {
		t.Fatalf("opt-out still sent %d events", len(*events))
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".handshake", "telemetry_id")); !os.IsNotExist(err) {
		t.Fatalf("opt-out still created an ID file (err = %v)", err)
	}
}

func TestCaptureSurvivesUnreachableEndpoint(t *testing.T) {
	homeDir := t.TempDir()
	withTestKey(t)
	previous := captureURL
	captureURL = "http://127.0.0.1:1/unreachable"
	t.Cleanup(func() { captureURL = previous })

	// Must return without panicking or blocking beyond the client timeout.
	Installed(homeDir, "0.21.0", nil)
	Heartbeat(homeDir, "0.21.0")
}

func TestMissingKeyDisablesTelemetry(t *testing.T) {
	homeDir := t.TempDir()
	// No withTestKey: this is a local build with no injected key.
	events := &[]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*events = append(*events, map[string]any{})
	}))
	t.Cleanup(server.Close)
	previous := captureURL
	captureURL = server.URL
	t.Cleanup(func() { captureURL = previous })

	if !Disabled() {
		t.Fatal("Disabled() = false with no API key")
	}
	Installed(homeDir, "0.21.0", []string{"claude-code"})
	Heartbeat(homeDir, "0.21.0")
	if len(*events) != 0 {
		t.Fatalf("keyless build sent %d events", len(*events))
	}
}
