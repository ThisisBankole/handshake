package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This file holds the policy that decides whether an auto-update should even be
// attempted, plus the platform helpers the Updater defaults to. Keeping them
// out of autoupdate.go lets the download-verify-swap core stay pure and
// test-driven with injected dependencies.

// AutoUpdateDisabled reports whether auto-update must be skipped regardless of
// available releases. Development builds never self-update (their version
// carries a pre-release suffix), and HANDSHAKE_NO_AUTO_UPDATE is the opt-out.
func AutoUpdateDisabled(installedVersion string) bool {
	if os.Getenv("HANDSHAKE_NO_AUTO_UPDATE") != "" {
		return true
	}
	return strings.Contains(installedVersion, "-")
}

// MaybeAutoUpdate runs one best-effort update to latest. It is deliberately
// conservative: it only proceeds for a managed-service install (where exiting
// triggers a clean relaunch) and never touches a Homebrew-managed binary
// (brew owns those upgrades). Every skip and failure is logged, never fatal —
// a failed update must not take the daemon down.
func MaybeAutoUpdate(ctx context.Context, homeDir, installedVersion, latest string, log func(string, ...any)) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if AutoUpdateDisabled(installedVersion) {
		return
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	if !serviceInstalled(homeDir) {
		log("auto-update skipped: no managed service to relaunch the daemon")
		return
	}
	target, err := resolvedExecutable()
	if err != nil {
		log("auto-update skipped: cannot resolve binary path: %v", err)
		return
	}
	if isHomebrewPath(target) {
		log("auto-update skipped: Homebrew manages this binary (brew upgrade handshake)")
		return
	}

	updater := NewUpdater()
	updater.Log = log
	if _, err := updater.Update(ctx, latest); err != nil {
		log("auto-update failed: %v", err)
	}
}

// serviceInstalled reports whether a login service exists for the current OS.
// Its exit is what relaunches the daemon after the binary swap, so without one
// we do not attempt an update. Paths mirror cmd/handshake/service.go.
func serviceInstalled(homeDir string) bool {
	switch runtime.GOOS {
	case "darwin":
		return fileExists(filepath.Join(homeDir, "Library", "LaunchAgents", "com.handshake.serve.plist"))
	case "linux":
		return fileExists(filepath.Join(homeDir, ".config", "systemd", "user", "handshake.service"))
	default:
		return false
	}
}

// isHomebrewPath reports whether the resolved binary lives inside a Homebrew
// prefix. Swapping it would desync brew's records and be reverted by the next
// brew upgrade, so those installs are left to brew.
func isHomebrewPath(path string) bool {
	return strings.Contains(path, "/Cellar/") || strings.Contains(path, "/Caskroom/")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// resolvedExecutable returns the fully resolved path of the running binary,
// matching the path the service manager baked into its unit at install time.
func resolvedExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}

// gracefulExit ends the process so the service manager (launchd KeepAlive /
// systemd Restart=always) relaunches it from the replaced binary.
func gracefulExit() error {
	os.Exit(0)
	return nil
}
