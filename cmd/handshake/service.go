package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// install-service / uninstall-service: register `handshake serve` to start on
// login and stay running — launchd (LaunchAgent) on macOS, a systemd user
// unit on Linux.

const launchdLabel = "com.handshake.serve"

func installServiceCmd(homeDir string) {
	binPath, err := os.Executable()
	if err != nil {
		fmt.Printf("Could not determine binary path: %v\n", err)
		os.Exit(1)
	}
	binPath, _ = filepath.EvalSymlinks(binPath)

	logPath := filepath.Join(homeDir, ".handshake", "handshake.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		fmt.Printf("Could not create ~/.handshake: %v\n", err)
		os.Exit(1)
	}

	switch runtime.GOOS {
	case "darwin":
		installLaunchd(homeDir, binPath, logPath)
	case "linux":
		installSystemd(homeDir, binPath, logPath)
	default:
		fmt.Printf("install-service is not supported on %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func uninstallServiceCmd(homeDir string) {
	switch runtime.GOOS {
	case "darwin":
		plist := launchdPlistPath(homeDir)
		exec.Command("launchctl", "bootout", launchdDomain(), plist).Run()
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Could not remove %s: %v\n", plist, err)
			os.Exit(1)
		}
		fmt.Println("Service uninstalled")
	case "linux":
		exec.Command("systemctl", "--user", "disable", "--now", "handshake.service").Run()
		unit := systemdUnitPath(homeDir)
		if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Could not remove %s: %v\n", unit, err)
			os.Exit(1)
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("Service uninstalled")
	default:
		fmt.Printf("uninstall-service is not supported on %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

// --- macOS (launchd LaunchAgent)

func launchdPlistPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
}

func launchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func installLaunchd(homeDir, binPath, logPath string) {
	plist := launchdPlist(launchdLabel, binPath, logPath, resolvedAddr(), mcpURL())

	plistPath := launchdPlistPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		fmt.Printf("Could not create LaunchAgents directory: %v\n", err)
		os.Exit(1)
	}

	// Unload any previous version first so reinstalls pick up the new plist.
	exec.Command("launchctl", "bootout", launchdDomain(), plistPath).Run()

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		fmt.Printf("Could not write plist: %v\n", err)
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "bootstrap", launchdDomain(), plistPath).CombinedOutput(); err != nil {
		// Older macOS fallback.
		if out2, err2 := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err2 != nil {
			fmt.Printf("launchctl failed: %s / %s\n", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
			os.Exit(1)
		}
	}

	fmt.Println("✓ Service installed (launchd)")
	fmt.Printf("  Plist: %s\n", plistPath)
	fmt.Printf("  Logs:  %s\n", logPath)
	fmt.Println("  Handshake now starts on login. Manage it with:")
	fmt.Printf("    launchctl kickstart -k %s/%s   # restart\n", launchdDomain(), launchdLabel)
	fmt.Println("    handshake uninstall-service              # remove")
}

// --- Linux (systemd user unit)

func systemdUnitPath(homeDir string) string {
	return filepath.Join(homeDir, ".config", "systemd", "user", "handshake.service")
}

func installSystemd(homeDir, binPath, logPath string) {
	unit := systemdUnit(binPath, logPath, resolvedAddr(), mcpURL())

	unitPath := systemdUnitPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		fmt.Printf("Could not create systemd user directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		fmt.Printf("Could not write unit file: %v\n", err)
		os.Exit(1)
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "handshake.service").CombinedOutput(); err != nil {
		fmt.Printf("systemctl failed: %s\n", strings.TrimSpace(string(out)))
		os.Exit(1)
	}

	fmt.Println("✓ Service installed (systemd user unit)")
	fmt.Printf("  Unit: %s\n", unitPath)
	fmt.Printf("  Logs: %s\n", logPath)
	fmt.Println("  Handshake now starts on login. Manage it with:")
	fmt.Println("    systemctl --user restart handshake   # restart")
	fmt.Println("    handshake uninstall-service          # remove")
}

// resolvedAddr returns the address the daemon should listen on, honouring the
// HANDSHAKE_ADDR override (same resolution as `handshake serve`). Used to bake
// the resolved port into the service definition so the login service starts on
// the same port setup chose, not the default.
func resolvedAddr() string {
	if env := os.Getenv("HANDSHAKE_ADDR"); env != "" {
		return env
	}
	return listenAddr
}

// launchdPlist renders a LaunchAgent plist that runs `handshake serve` on
// login, with the resolved HANDSHAKE_ADDR and HANDSHAKE_URL baked in so the
// service matches the address agents are registered against.
func launchdPlist(label, binPath, logPath, addr, mcpURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HANDSHAKE_ADDR</key>
		<string>%s</string>
		<key>HANDSHAKE_URL</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, binPath, addr, mcpURL, logPath, logPath)
}

// systemdUnit renders a systemd user unit with the resolved
// HANDSHAKE_ADDR and HANDSHAKE_URL baked in.
func systemdUnit(binPath, logPath, addr, mcpURL string) string {
	return fmt.Sprintf(`[Unit]
Description=Handshake session portability daemon

[Service]
ExecStart=%s serve
Environment=HANDSHAKE_ADDR=%s
Environment=HANDSHAKE_URL=%s
Restart=always
RestartSec=2
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, binPath, addr, mcpURL, logPath, logPath)
}
