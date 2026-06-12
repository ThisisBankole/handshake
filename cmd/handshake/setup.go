package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"handshake/plugins/opencode"
)

// setupCmd runs the guided onboarding flow. It is triggered automatically
// by the Homebrew cask post-install hook, and can also be run manually.
func setupCmd(homeDir, dbPath string) {
	clearScreen()

	fmt.Println("╔─────────────────────────────────────────────╗")
	fmt.Println("│  Welcome to Handshake                       │")
	fmt.Println("│  Session portability for AI agents   │")
	fmt.Println("╚─────────────────────────────────────────────╝")
	fmt.Println()
	fmt.Println("This will set up Handshake on your machine in three steps.")
	fmt.Println()

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

	fmt.Println()
	fmt.Println("  ✓ Step 1 complete.")
	fmt.Println()

	// ── Step 2: install as background service ──────────────────────────────

	fmt.Println("Step 2 of 3 — Start Handshake automatically on login.")
	
	fmt.Println()

	if confirmYN("Install as a background service?", true) {
		fmt.Println()
		installServiceCmd(homeDir)
		fmt.Println()
		fmt.Println("  ✓ Step 2 complete.")
	} else {
		fmt.Println()
		fmt.Println("  Skipped. Start manually later with: handshake install-service")
		fmt.Println("  ✓ Step 2 skipped.")
	}

	fmt.Println()

	// ── Step 3: start daemon now ───────────────────────────────────────────

	fmt.Println("Step 3 of 3 — Start Handshake now.")
	fmt.Println()

	if confirmYN("Start the daemon?", true) {
		fmt.Println()
		if err := startDaemonBackground(); err != nil {
			fmt.Printf("  ✗ Could not start daemon: %v\n", err)
			fmt.Println("  Start manually with: handshake serve")
		} else {
			fmt.Println("  ✓ Handshake is running on localhost:8765")
		}
		fmt.Println()
		fmt.Println("  ✓ Step 3 complete.")
	} else {
		fmt.Println()
		fmt.Println("  Skipped. Start manually later with: handshake serve")
		fmt.Println("  ✓ Step 3 skipped.")
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
	fmt.Println("No cloud. No accounts. No data leaving your machine.")
	fmt.Println()
}

// uninstallCmd removes everything Handshake wrote and optionally removes the binary.
func uninstallCmd(homeDir string) {
	fmt.Println("This will remove Handshake from your machine.")
	fmt.Println()

	// Stop and remove the service first
	fmt.Println("Stopping the daemon...")
	uninstallServiceCmd(homeDir)
	fmt.Println()

	// Remove from all agents
	fmt.Println("Removing Handshake from each agent:")
	deregisterClaudeCode()
	deregisterOpenCode(homeDir)
	deregisterHermes(homeDir)
	removeOpenCodePlugin(homeDir)
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

// registerAgentsIndented runs agent registration with indented output.
func registerAgentsIndented(homeDir string) {
	// Temporarily redirect stdout prefix — simplest is just call register
	// functions directly; they already print their own status lines.
	registerClaudeCode()
	registerOpenCode(homeDir)
	registerHermes(homeDir)
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