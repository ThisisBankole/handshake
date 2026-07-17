// Package telemetry sends anonymous usage pings to PostHog. Every event
// carries only the Handshake version, operating system, architecture, and a
// random per-machine ID — never project, session, or repository data. Setting
// HANDSHAKE_NO_TELEMETRY to any non-empty value disables all sends.
package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const timeout = 3 * time.Second

// apiKey is PostHog's public write-only project key. Release builds inject it
// with -ldflags "-X handshake/internal/telemetry.apiKey=..." from the
// POSTHOG_API_KEY repository secret; when it is empty (all local builds),
// telemetry is disabled entirely, so development never pollutes the numbers.
var apiKey = ""

// captureURL is a variable so tests can point sends at a local server.
var captureURL = "https://eu.i.posthog.com/i/v0/e/"

// Disabled reports whether telemetry is off — either because the user opted
// out or because this build carries no API key (local/dev builds).
func Disabled() bool {
	return apiKey == "" || os.Getenv("HANDSHAKE_NO_TELEMETRY") != ""
}

// AnonymousID returns the per-machine random ID, creating and persisting it
// on first use. The second result reports whether this call created the ID,
// which callers use to distinguish a first-time install from a re-run.
func AnonymousID(homeDir string) (string, bool, error) {
	path := filepath.Join(homeDir, ".handshake", "telemetry_id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, false, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	id := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0600); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// Installed reports a completed first-time setup. It sends only when the
// anonymous ID did not exist before this call, so re-running setup does not
// inflate install counts. Failures are silent: telemetry must never affect
// setup.
func Installed(homeDir, version string, agents []string) {
	if Disabled() {
		return
	}
	id, created, err := AnonymousID(homeDir)
	if err != nil || !created {
		return
	}
	capture("handshake_installed", id, version, map[string]any{"agents": agents})
}

// Heartbeat reports that an installed daemon is still running. It is called
// from the existing weekly release check so telemetry adds no network cadence
// of its own. Failures are silent.
func Heartbeat(homeDir, version string) {
	if Disabled() {
		return
	}
	id, _, err := AnonymousID(homeDir)
	if err != nil {
		return
	}
	capture("handshake_heartbeat", id, version, nil)
}

func capture(event, distinctID, version string, extra map[string]any) {
	properties := map[string]any{
		"version": version,
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		// Anonymous events: no person profile is created in PostHog.
		"$process_person_profile": false,
	}
	for key, value := range extra {
		properties[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"event":       event,
		"distinct_id": distinctID,
		"properties":  properties,
	})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Post(captureURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	response.Body.Close()
}
