// Package update checks Handshake releases without sending project or session
// data, and (unless opted out) auto-updates a managed-service install to the
// newest release after verifying its SHA-256 checksum. The anonymous telemetry
// heartbeat runs on its own daily schedule in internal/telemetry, not here.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"handshake/internal/db"
)

const (
	defaultReleaseURL = "https://api.github.com/repos/ThisisBankole/handshake/releases/latest"
	checkInterval     = 7 * 24 * time.Hour
	failureRetry      = time.Hour
)

type Checker struct {
	Client     *http.Client
	ReleaseURL string
	Now        func() time.Time
}

func NewChecker() *Checker {
	return &Checker{
		Client:     &http.Client{Timeout: 5 * time.Second},
		ReleaseURL: defaultReleaseURL,
		Now:        time.Now,
	}
}

func (c *Checker) Check(ctx context.Context, database *db.Database, installedVersion string, force bool) (*db.UpdateStatus, bool, error) {
	status, err := database.GetUpdateStatus()
	if err != nil {
		return nil, false, err
	}
	now := c.Now()
	if !force && !Due(status, now) {
		return status, false, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ReleaseURL, nil)
	if err != nil {
		return status, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "handshake-update-checker")
	if status.ETag != "" {
		request.Header.Set("If-None-Match", status.ETag)
	}

	response, err := c.Client.Do(request)
	status.LastCheckedAt = now.Unix()
	status.InstalledVersion = installedVersion
	if err != nil {
		status.LastError = err.Error()
		_ = database.SaveUpdateStatus(status)
		return status, true, fmt.Errorf("check latest release: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		status.LastError = ""
		if err := database.SaveUpdateStatus(status); err != nil {
			return status, true, err
		}
		return status, true, nil
	}
	if response.StatusCode != http.StatusOK {
		status.LastError = "release check returned " + response.Status
		_ = database.SaveUpdateStatus(status)
		return status, true, fmt.Errorf("%s", status.LastError)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		status.LastError = "decode latest release: " + err.Error()
		_ = database.SaveUpdateStatus(status)
		return status, true, fmt.Errorf("%s", status.LastError)
	}
	if release.TagName == "" || release.HTMLURL == "" {
		status.LastError = "latest release response omitted tag_name or html_url"
		_ = database.SaveUpdateStatus(status)
		return status, true, fmt.Errorf("%s", status.LastError)
	}
	status.ETag = response.Header.Get("ETag")
	status.LatestVersion = release.TagName
	status.ReleaseURL = release.HTMLURL
	status.LastError = ""
	if err := database.SaveUpdateStatus(status); err != nil {
		return status, true, err
	}
	return status, true, nil
}

// Due applies a seven-day cadence after successful checks and a shorter retry
// after offline or server errors. It avoids network calls during normal MCP use.
func Due(status *db.UpdateStatus, now time.Time) bool {
	if status == nil || status.LastCheckedAt == 0 {
		return true
	}
	interval := checkInterval
	if status.LastError != "" {
		interval = failureRetry
	}
	return now.Sub(time.Unix(status.LastCheckedAt, 0)) >= interval
}

func Start(ctx context.Context, database *db.Database, homeDir, installedVersion string) {
	checker := NewChecker()
	go func() {
		for {
			status, performed, _ := checker.Check(ctx, database, installedVersion, false)
			if performed && status.LastError == "" && IsNewer(installedVersion, status.LatestVersion) {
				// Best-effort: on success this replaces the binary and exits so
				// the service manager relaunches on the new version; it never
				// returns. On any skip or failure the daemon keeps running.
				MaybeAutoUpdate(ctx, homeDir, installedVersion, status.LatestVersion, log.Printf)
			}
			delay := NextCheckDelay(status, checker.Now())
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func NextCheckDelay(status *db.UpdateStatus, now time.Time) time.Duration {
	if status == nil || status.LastCheckedAt == 0 {
		return 0
	}
	interval := checkInterval
	if status.LastError != "" {
		interval = failureRetry
	}
	delay := time.Unix(status.LastCheckedAt, 0).Add(interval).Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

// IsNewer compares release versions without accepting arbitrary version syntax.
// Development builds deliberately do not produce update notices.
func IsNewer(installed, latest string) bool {
	if strings.Contains(installed, "-") {
		return false
	}
	installedParts, ok := versionParts(installed)
	if !ok {
		return false
	}
	latestParts, ok := versionParts(latest)
	if !ok {
		return false
	}
	for index := range installedParts {
		if latestParts[index] != installedParts[index] {
			return latestParts[index] > installedParts[index]
		}
	}
	return false
}

func versionParts(version string) ([3]int, bool) {
	var result [3]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return result, false
	}
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return result, false
		}
		result[index] = value
	}
	return result, true
}
