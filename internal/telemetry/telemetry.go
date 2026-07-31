// Package telemetry sends anonymous usage pings to PostHog. Every event
// carries only the Handshake version, operating system, architecture, a
// random per-machine ID, agent names from the fixed adapter set, and
// feature-usage counts — never project, session, or repository data. Setting
// HANDSHAKE_NO_TELEMETRY to any non-empty value disables all sends.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	timeout = 3 * time.Second
	// heartbeatInterval is the daily cadence; heartbeatPoll is how often the
	// daemon loop re-checks whether a heartbeat is due, so laptops that sleep
	// through the exact deadline still send within the hour after waking.
	heartbeatInterval = 24 * time.Hour
	heartbeatPoll     = time.Hour
)

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
// on first use. Any sender may create it; whether the install event was sent
// is tracked separately by the marker file in Installed, so an early daemon
// heartbeat cannot swallow the install ping.
func AnonymousID(homeDir string) (string, error) {
	path := filepath.Join(homeDir, ".handshake", "telemetry_id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0600); err != nil {
		return "", err
	}
	return id, nil
}

// Installed reports a completed first-time setup. A marker file records that
// the install event was sent, so re-running setup does not inflate install
// counts even though the daemon's heartbeat may have created the anonymous ID
// first. serviceInstalled and daemonStarted record where the setup funnel
// ended, as booleans only. Failures are silent: telemetry must never affect
// setup.
func Installed(homeDir, version string, agents []string, serviceInstalled, daemonStarted bool) {
	if Disabled() {
		return
	}
	marker := filepath.Join(homeDir, ".handshake", "telemetry_install_sent")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	id, err := AnonymousID(homeDir)
	if err != nil {
		return
	}
	capture("handshake_installed", id, version, map[string]any{
		"agents":            agents,
		"service_installed": serviceInstalled,
		"daemon_started":    daemonStarted,
	})
	_ = os.WriteFile(marker, []byte("1\n"), 0600)
}

// Heartbeat reports that an installed daemon is still running, with optional
// extra properties (feature-usage counts, active agent names). Failures are
// silent.
func Heartbeat(homeDir, version string, extra map[string]any) {
	if Disabled() {
		return
	}
	id, err := AnonymousID(homeDir)
	if err != nil {
		return
	}
	capture("handshake_heartbeat", id, version, extra)
}

// Uninstalled reports that the user ran the uninstall command. It only reads
// the existing machine ID — uninstalling must never create telemetry state —
// and stays silent when this machine was never set up with telemetry.
func Uninstalled(homeDir, version string) {
	if Disabled() {
		return
	}
	id := existingID(homeDir)
	if id == "" {
		return
	}
	capture("handshake_uninstalled", id, version, nil)
}

// existingID returns the persisted machine ID without ever creating it.
func existingID(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".handshake", "telemetry_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// StartDaily runs the daily heartbeat loop. gather is called once per sent
// heartbeat with the previous heartbeat's Unix time and returns the extra
// properties to attach (usage counters, active agents); it may be nil. The
// last-sent time is persisted so daemon restarts do not resend early.
func StartDaily(ctx context.Context, homeDir, version string, gather func(sinceUnix int64) map[string]any) {
	if Disabled() {
		return
	}
	go func() {
		for {
			maybeSendHeartbeat(homeDir, version, time.Now(), gather)
			timer := time.NewTimer(heartbeatPoll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// maybeSendHeartbeat sends one heartbeat when the daily interval has elapsed
// and reports whether it sent. The timestamp advances even if the send fails,
// so a broken network undercounts rather than building a backlog.
func maybeSendHeartbeat(homeDir, version string, now time.Time, gather func(sinceUnix int64) map[string]any) bool {
	last := lastHeartbeatAt(homeDir)
	if now.Sub(last) < heartbeatInterval {
		return false
	}
	var extra map[string]any
	if gather != nil {
		extra = gather(last.Unix())
	}
	Heartbeat(homeDir, version, extra)
	path := filepath.Join(homeDir, ".handshake", "telemetry_last_heartbeat")
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.WriteFile(path, []byte(strconv.FormatInt(now.Unix(), 10)+"\n"), 0600)
	return true
}

func lastHeartbeatAt(homeDir string) time.Time {
	data, err := os.ReadFile(filepath.Join(homeDir, ".handshake", "telemetry_last_heartbeat"))
	if err != nil {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
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
