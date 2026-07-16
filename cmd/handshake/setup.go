package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"handshake/plugins/opencode"
)

// setupCmd runs the guided onboarding flow. It is triggered automatically
// by the Homebrew cask post-install hook, and can also be run manually.
func setupCmd(homeDir, dbPath string) {
	clearScreen()

	fmt.Println("╔─────────────────────────────────────────────╗")
	fmt.Println("│  Welcome to Handshake                       │")
	fmt.Println("│  Session portability for AI agents          │")
	fmt.Println("╚─────────────────────────────────────────────╝")
	fmt.Println()
	fmt.Println("This will set up Handshake on your machine in three steps.")
	fmt.Println()

	// ── Port resolution ────────────────────────────────────────────────────
	// Respect an explicit HANDSHAKE_ADDR if the user pre-set it; otherwise
	// scan for the first free port starting at the default.
	resolvedAddr := listenAddr
	fmt.Println("  Checking port availability...")
	if explicit := os.Getenv("HANDSHAKE_ADDR"); explicit != "" {
		resolvedAddr = explicit
		fmt.Printf("  ✓ Using pre-configured address: %s\n", resolvedAddr)
		fmt.Println()
		if os.Getenv("HANDSHAKE_URL") == "" {
			os.Setenv("HANDSHAKE_URL", "http://"+resolvedAddr+"/mcp")
		}
	} else if handshakeAt(listenAddr) {
		// A Handshake daemon is already running on the default port — reuse it
		// so re-running setup keeps agents on their current port instead of
		// treating the daemon itself as a conflict and migrating.
		resolvedAddr = listenAddr
		fmt.Printf("  ✓ Handshake already running on %s — reusing\n", resolvedAddr)
		fmt.Println()
		os.Setenv("HANDSHAKE_URL", "http://"+resolvedAddr+"/mcp")
	} else {
		resolvedAddr = findFreePort(listenAddr)
		if resolvedAddr != listenAddr {
			warnBox([]string{
				"[!] Port " + listenAddr + " is already in use.",
				"    Handshake will use " + resolvedAddr + " instead.",
				"    All agents will be registered to the new address.",
			})
			os.Setenv("HANDSHAKE_ADDR", resolvedAddr)
			os.Setenv("HANDSHAKE_URL", "http://"+resolvedAddr+"/mcp")
		} else {
			fmt.Printf("  ✓ Port %s is free.\n", listenAddr)
			fmt.Println()
		}
	}

	// ── Step 1: database + agent registration ──────────────────────────────

	fmt.Println("Step 1 of 3 — Create the session database and register with your agents.")
	fmt.Println()

	if !confirmYN("Run setup now?", true) {
		fmt.Println()
		fmt.Println("Setup cancelled. Run it later with: handshake init")
		return
	}

	fmt.Println()

	// Create database
	database := openDB(dbPath)
	database.Close()
	fmt.Printf("  ✓ Database ready at %s\n", dbPath)

	// Install OpenCode plugin
	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		fmt.Printf("  ✗ Could not create OpenCode plugin directory: %v\n", err)
	} else if err := os.WriteFile(pluginPath, opencode.PluginJS, 0644); err != nil {
		fmt.Printf("  ✗ Could not install OpenCode plugin: %v\n", err)
	} else {
		fmt.Printf("  ✓ OpenCode plugin installed\n")
	}

	// Register with agents
	fmt.Println()
	fmt.Println("  Registering with your agents:")
	registerAgentsIndented(homeDir)
	registerClaudeCodeHooks(homeDir)
	registerHermesPlugin(homeDir)
	registerCodexHooks(homeDir)
	registerCursorHooks(homeDir)

	fmt.Println()
	authorSetupCmd(homeDir)

	fmt.Println()
	fmt.Println("  ✓ Step 1 complete.")
	fmt.Println()

	// ── Step 2: install as background service ──────────────────────────────

	fmt.Println("Step 2 of 3 — Start Handshake automatically on login.")

	fmt.Println()

	serviceInstalled := false
	if confirmYN("Install as a background service?", true) {
		fmt.Println()
		installServiceCmd(homeDir)
		serviceInstalled = true
		fmt.Println()
		fmt.Println("  ✓ Step 2 complete.")
	} else {
		fmt.Println()
		fmt.Println("  Skipped. Start manually later with: handshake install-service")
		fmt.Println("  ✓ Step 2 skipped.")
	}

	fmt.Println()

	// ── Step 3: start daemon now ───────────────────────────────────────────
	// When the login service was just installed, launchd (RunAtLoad/KeepAlive)
	// or systemd (--now) has already started the daemon on the resolved port.
	// Spawning a second `handshake serve` here would fail to bind the same port
	// and exit silently, so we just verify the service-managed daemon is up.
	// Only when no service was installed do we offer to start a one-off daemon.

	if serviceInstalled {
		fmt.Println("Step 3 of 3 — Verify the daemon is running.")
		fmt.Println()
		fmt.Printf("  Checking daemon on %s...\n", resolvedAddr)
		if daemonReady(resolvedAddr) {
			fmt.Printf("  ✓ Handshake is running on %s\n", resolvedAddr)
		} else {
			fmt.Printf("  ✗ Daemon did not respond on %s\n", resolvedAddr)
			fmt.Println("  Check logs or run: handshake serve")
		}
		fmt.Println()
		fmt.Println("  ✓ Step 3 complete.")
	} else {
		fmt.Println("Step 3 of 3 — Start Handshake now.")
		fmt.Println()

		if confirmYN("Start the daemon?", true) {
			fmt.Println()
			fmt.Printf("  Starting daemon on %s...\n", resolvedAddr)
			if err := startDaemonBackground(); err != nil {
				fmt.Printf("  ✗ Could not start daemon: %v\n", err)
				fmt.Println("  Start manually with: handshake serve")
			} else if daemonReady(resolvedAddr) {
				fmt.Printf("  ✓ Handshake is running on %s\n", resolvedAddr)
			} else {
				fmt.Printf("  ✗ Daemon did not respond on %s\n", resolvedAddr)
				fmt.Println("  Check logs or run: handshake serve")
			}
			fmt.Println()
			fmt.Println("  ✓ Step 3 complete.")
		} else {
			fmt.Println()
			fmt.Println("  Skipped. Start manually later with: handshake serve")
			fmt.Println("  ✓ Step 3 skipped.")
		}
	}

	fmt.Println()
	fmt.Println("╔─────────────────────────────────────────────╗")
	fmt.Println("│  All done. Handshake is ready.              │")
	fmt.Println("╚─────────────────────────────────────────────╝")
	fmt.Println()
	fmt.Println("In Claude Code, OpenCode, or Hermes just say:")
	fmt.Println()
	fmt.Println("  \"checkpoint this session\"")
	fmt.Println("  \"list my sessions\"")
	fmt.Println("  \"restore my <session_name> session\"")
	fmt.Println()
	fmt.Println("Your sessions are stored locally at ~/.handshake/sessions.db")
	fmt.Println("Session checkpoints stay local. Any enabled background writer uses its selected agent CLI and provider account.")
	fmt.Println()
}

// uninstallCmd removes everything Handshake wrote and optionally removes the binary.
func uninstallCmd(homeDir string) {
	fmt.Println("This will remove Handshake from your machine.")
	fmt.Println()

	// Stop and remove the service first
	fmt.Println("Stopping the daemon...")
	uninstallServiceCmd(homeDir)
	killRunningServe()
	fmt.Println()

	// Remove from all agents
	fmt.Println("Removing Handshake from each agent:")
	deregisterClaudeCode(homeDir)
	deregisterOpenCode(homeDir)
	deregisterHermes(homeDir)
	removeOpenCodePlugin(homeDir)
	deregisterClaudeCodeHooks(homeDir)
	deregisterHermesPlugin(homeDir)
	deregisterCodexHooks(homeDir)
	deregisterCursorHooks(homeDir)
	deregisterCursor(homeDir)
	removeKnowledgeAuthoringSkills(homeDir)
	removeHandshakeSkills(homeDir)
	//deregisterCodexMCP(homeDir)
	fmt.Println()

	// Ask about database
	deleteDB := confirmYN("Delete session database? Your session history will be lost.", false)
	fmt.Println()

	if deleteDB {
		if err := os.RemoveAll(filepath.Join(homeDir, ".handshake")); err != nil {
			fmt.Printf("✗ Could not remove ~/.handshake: %v\n", err)
		} else {
			fmt.Println("✓ Session database deleted")
		}
	} else {
		fmt.Printf("  Session database kept at %s\n", filepath.Join(homeDir, ".handshake", "sessions.db"))
	}

	fmt.Println()

	// Ask about removing the binary
	removeBinary := confirmYN("Remove the handshake binary from your machine?", false)
	fmt.Println()

	if removeBinary {
		if err := removeHandshakeBinary(); err != nil {
			fmt.Printf("✗ Could not remove binary: %v\n", err)
			fmt.Println("  Remove manually with: brew uninstall handshake")
			fmt.Println("  Or: rm $(which handshake)")
		}
	} else {
		fmt.Println("  Binary kept. Reinstall config later with: handshake setup")
	}

	fmt.Println()
	fmt.Println("✓ Handshake uninstalled.")
	fmt.Println()
}

// removeHandshakeBinary detects how Handshake was installed and removes it.
func removeHandshakeBinary() error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine binary path: %w", err)
	}

	// Detect Homebrew install by checking if the path contains Homebrew/Cellar
	// or if brew is available and knows about handshake.
	if strings.Contains(binPath, "Homebrew") || strings.Contains(binPath, "homebrew") || strings.Contains(binPath, "Cellar") {
		fmt.Println("  Detected Homebrew install — running brew uninstall handshake...")
		out, err := exec.Command("brew", "uninstall", "handshake").CombinedOutput()
		if err != nil {
			return fmt.Errorf("brew uninstall failed: %s", strings.TrimSpace(string(out)))
		}
		fmt.Println("  ✓ Binary removed via Homebrew")
		return nil
	}

	// Also try asking brew directly regardless of path
	if _, err := exec.LookPath("brew"); err == nil {
		out, err := exec.Command("brew", "list", "handshake").CombinedOutput()
		if err == nil && strings.Contains(string(out), "handshake") {
			fmt.Println("  Detected Homebrew install — running brew uninstall handshake...")
			out2, err2 := exec.Command("brew", "uninstall", "handshake").CombinedOutput()
			if err2 != nil {
				return fmt.Errorf("brew uninstall failed: %s", strings.TrimSpace(string(out2)))
			}
			fmt.Println("  ✓ Binary removed via Homebrew")
			return nil
		}
	}

	// Manual install — just delete the binary
	fmt.Printf("  Removing binary at %s...\n", binPath)
	if err := os.Remove(binPath); err != nil {
		return fmt.Errorf("could not remove %s: %w", binPath, err)
	}
	fmt.Println("  ✓ Binary removed")
	return nil
}

// killRunningServe kills all running "handshake serve" processes using pkill.
// This is called during uninstall so stale daemon processes don't hold the port
// after the binary is removed. The current process (which runs "handshake
// uninstall", not "handshake serve") is not affected.
func killRunningServe() {
	if err := exec.Command("pkill", "-f", "handshake serve").Run(); err != nil {
		// pkill exits 1 when no matching processes — that's fine
		return
	}
	fmt.Println("  ✓ Stopped running handshake serve processes")
}

// startDaemonBackground starts handshake serve as a detached background process.
func startDaemonBackground() error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine binary path: %w", err)
	}

	cmd := exec.Command(binPath, "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return err
	}

	// Detach from the parent process
	if err := cmd.Process.Release(); err != nil {
		return err
	}

	return nil
}

// warnBox prints a bordered warning box. Lines must be plain ASCII so that
// len() == display width and alignment is correct.
func warnBox(lines []string) {
	maxLen := 0
	for _, l := range lines {
		if len(l) > maxLen {
			maxLen = len(l)
		}
	}
	inner := maxLen + 4 // 2-space padding on each side
	border := strings.Repeat("─", inner)
	fmt.Println()
	fmt.Printf("  ╔%s╗\n", border)
	for _, l := range lines {
		pad := strings.Repeat(" ", maxLen-len(l))
		fmt.Printf("  │  %s%s  │\n", l, pad)
	}
	fmt.Printf("  ╚%s╝\n", border)
	fmt.Println()
}

// daemonReady probes addr with short retries to confirm the daemon came up.
func daemonReady(addr string) bool {
	for i := 0; i < 5; i++ {
		time.Sleep(300 * time.Millisecond)
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// handshakeAt reports whether a Handshake daemon is already listening on addr,
// by probing its /health endpoint. Used so re-running setup reuses the daemon's
// current port instead of treating it as a conflict and migrating to a new one.
func handshakeAt(addr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// registerAgentsIndented runs agent registration with indented output.
func registerAgentsIndented(homeDir string) {
	// Temporarily redirect stdout prefix — simplest is just call register
	// functions directly; they already print their own status lines.
	registerClaudeCode(homeDir)
	registerOpenCode(homeDir)
	registerHermes(homeDir)
	registerCursor(homeDir)
	printKnowledgeSkillInstallResults(installKnowledgeAuthoringSkills(homeDir))
	printKnowledgeSkillInstallResults(installHandshakeSkills(homeDir))
}

// confirmYN asks the user a yes/no question.
// defaultYes controls whether pressing Enter without input means yes or no.
func confirmYN(question string, defaultYes bool) bool {
	if defaultYes {
		fmt.Printf("%s [Y/n] ", question)
	} else {
		fmt.Printf("%s [y/N] ", question)
	}

	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if answer == "" {
			return defaultYes
		}
		return answer == "y" || answer == "yes"
	}
	return defaultYes
}

// clearScreen clears the terminal for a clean setup experience.
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}
