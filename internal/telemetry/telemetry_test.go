package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	first, err := AnonymousID(homeDir)
	if err != nil || first == "" {
		t.Fatalf("first call = %q, err %v", first, err)
	}
	second, err := AnonymousID(homeDir)
	if err != nil || second != first {
		t.Fatalf("second call = %q, err %v (want %q)", second, err, first)
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

	Installed(homeDir, "0.21.0", []string{"claude-code", "codex"}, true, true)
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
	if properties["service_installed"] != true || properties["daemon_started"] != true {
		t.Fatalf("funnel booleans missing: %+v", properties)
	}

	// A second setup run on the same machine must not send another install.
	Installed(homeDir, "0.21.0", nil, true, true)
	if len(*events) != 1 {
		t.Fatalf("re-run sent another install event: %d events", len(*events))
	}
}

// TestEarlyHeartbeatDoesNotSwallowInstall reproduces the real setup ordering:
// setup starts the daemon before sending the install ping, and the daemon's
// first release check fires a heartbeat immediately. That heartbeat creates
// the anonymous ID, which must not stop the install event from sending.
func TestEarlyHeartbeatDoesNotSwallowInstall(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	Heartbeat(homeDir, "0.22.0", nil)
	Installed(homeDir, "0.22.0", []string{"claude-code"}, true, true)
	if len(*events) != 2 {
		t.Fatalf("event count = %d, want heartbeat + install", len(*events))
	}
	if (*events)[0]["event"] != "handshake_heartbeat" || (*events)[1]["event"] != "handshake_installed" {
		t.Fatalf("events = %+v", *events)
	}
	if (*events)[0]["distinct_id"] != (*events)[1]["distinct_id"] {
		t.Fatalf("heartbeat and install used different machine IDs")
	}

	// The marker, not ID creation, gates re-sends: a second setup run on the
	// same machine must still not send another install.
	Installed(homeDir, "0.22.0", nil, true, true)
	if len(*events) != 2 {
		t.Fatalf("re-run sent another install event: %d events", len(*events))
	}
}

func TestHeartbeat_SendsEveryCall(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	Heartbeat(homeDir, "0.21.0", nil)
	Heartbeat(homeDir, "0.21.0", nil)
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

func TestHeartbeatCarriesExtraProperties(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	Heartbeat(homeDir, "0.22.0", map[string]any{"checkpoints": 3, "active_agents": []string{"codex"}})
	if len(*events) != 1 {
		t.Fatalf("event count = %d, want 1", len(*events))
	}
	properties := (*events)[0]["properties"].(map[string]any)
	if properties["checkpoints"] != float64(3) {
		t.Fatalf("checkpoints = %v, want 3", properties["checkpoints"])
	}
	if properties["version"] != "0.22.0" || properties["$process_person_profile"] != false {
		t.Fatalf("base properties missing: %+v", properties)
	}
}

func TestMaybeSendHeartbeat_DailyCadence(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)
	now := time.Now()

	gatherCalls := 0
	gather := func(sinceUnix int64) map[string]any {
		gatherCalls++
		return map[string]any{"checkpoints": 2}
	}

	// No timestamp file yet: send immediately.
	if !maybeSendHeartbeat(homeDir, "0.22.0", now, gather) {
		t.Fatal("first call did not send")
	}
	// Same day: due nothing, and gather (which drains counters) must not run.
	if maybeSendHeartbeat(homeDir, "0.22.0", now.Add(time.Hour), gather) {
		t.Fatal("second call within 24h sent")
	}
	if maybeSendHeartbeat(homeDir, "0.22.0", now.Add(23*time.Hour), gather) {
		t.Fatal("call at 23h sent")
	}
	// Past the interval: send again.
	if !maybeSendHeartbeat(homeDir, "0.22.0", now.Add(25*time.Hour), gather) {
		t.Fatal("call at 25h did not send")
	}
	if gatherCalls != 2 || len(*events) != 2 {
		t.Fatalf("gather calls = %d, events = %d, want 2 and 2", gatherCalls, len(*events))
	}
	properties := (*events)[0]["properties"].(map[string]any)
	if properties["checkpoints"] != float64(2) {
		t.Fatalf("gathered properties not attached: %+v", properties)
	}
}

func TestUninstalledSendsOnlyWithExistingID(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)

	// No ID: uninstall must send nothing and must not create telemetry state.
	Uninstalled(homeDir, "0.22.0")
	if len(*events) != 0 {
		t.Fatalf("uninstall without ID sent %d events", len(*events))
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".handshake", "telemetry_id")); !os.IsNotExist(err) {
		t.Fatalf("uninstall created an ID file (err = %v)", err)
	}

	Installed(homeDir, "0.22.0", nil, true, true)
	Uninstalled(homeDir, "0.22.0")
	if len(*events) != 2 || (*events)[1]["event"] != "handshake_uninstalled" {
		t.Fatalf("events = %+v", *events)
	}
	if (*events)[0]["distinct_id"] != (*events)[1]["distinct_id"] {
		t.Fatalf("install and uninstall used different machine IDs")
	}
}

func TestOptOutSuppressesAllSends(t *testing.T) {
	homeDir := t.TempDir()
	events := captureEvents(t)
	t.Setenv("HANDSHAKE_NO_TELEMETRY", "1")

	if !Disabled() {
		t.Fatal("Disabled() = false with HANDSHAKE_NO_TELEMETRY set")
	}
	Installed(homeDir, "0.21.0", []string{"claude-code"}, true, true)
	Heartbeat(homeDir, "0.21.0", nil)
	if len(*events) != 0 {
		t.Fatalf("opt-out still sent %d events", len(*events))
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".handshake", "telemetry_id")); !os.IsNotExist(err) {
		t.Fatalf("opt-out still created an ID file (err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".handshake", "telemetry_install_sent")); !os.IsNotExist(err) {
		t.Fatalf("opt-out still created the install marker (err = %v)", err)
	}
}

func TestCaptureSurvivesUnreachableEndpoint(t *testing.T) {
	homeDir := t.TempDir()
	withTestKey(t)
	previous := captureURL
	captureURL = "http://127.0.0.1:1/unreachable"
	t.Cleanup(func() { captureURL = previous })

	// Must return without panicking or blocking beyond the client timeout.
	Installed(homeDir, "0.21.0", nil, true, true)
	Heartbeat(homeDir, "0.21.0", nil)
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
	Installed(homeDir, "0.21.0", []string{"claude-code"}, true, true)
	Heartbeat(homeDir, "0.21.0", nil)
	if len(*events) != 0 {
		t.Fatalf("keyless build sent %d events", len(*events))
	}
}
