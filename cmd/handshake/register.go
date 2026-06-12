package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// registerAgents wires Handshake into each installed agent's MCP config.
// Every step is idempotent and degrades to printing the manual snippet when
// the agent is missing or its config can't be edited safely.
func registerAgents(homeDir string) {
	registerClaudeCode()
	registerOpenCode(homeDir)
	registerHermes(homeDir)
}

// deregisterAgents removes everything Handshake wrote during init.
func deregisterAgents(homeDir string, deleteDB bool) {
	fmt.Println("Removing Handshake from each agent:")
	deregisterClaudeCode()
	deregisterOpenCode(homeDir)
	deregisterHermes(homeDir)
	removeOpenCodePlugin(homeDir)

	dbPath := filepath.Join(homeDir, ".handshake", "sessions.db")
	if deleteDB {
		if err := os.RemoveAll(filepath.Join(homeDir, ".handshake")); err != nil {
			fmt.Printf("✗ Could not remove ~/.handshake: %v\n", err)
		} else {
			fmt.Println("✓ Session database deleted")
		}
	} else {
		fmt.Printf("  Session database kept at %s\n", dbPath)
		fmt.Println("  Delete manually with: rm -rf ~/.handshake")
	}
}

func mcpURL() string {
	return "http://" + listenAddr + "/mcp"
}

// backup copies path to path.handshake.bak before the first modification.
func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".handshake.bak", data, 0600)
}

// --- Claude Code ---

func registerClaudeCode() {
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Println("- Claude Code: CLI not found — register manually with:")
		fmt.Printf("    claude mcp add -s user --transport http handshake %s\n", mcpURL())
		return
	}

	if err := exec.Command("claude", "mcp", "get", "handshake").Run(); err == nil {
		fmt.Println("✓ Claude Code: already registered")
		return
	}

	out, err := exec.Command("claude", "mcp", "add", "-s", "user", "--transport", "http", "handshake", mcpURL()).CombinedOutput()
	if err != nil {
		fmt.Printf("✗ Claude Code: claude mcp add failed: %s\n", strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("✓ Claude Code: registered (via claude mcp add)")
}

func deregisterClaudeCode() {
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Println("- Claude Code: CLI not found — remove manually with:")
		fmt.Println("    claude mcp remove -s user handshake")
		return
	}

	// Check if it's registered at all
	if err := exec.Command("claude", "mcp", "get", "handshake").Run(); err != nil {
		fmt.Println("✓ Claude Code: not registered, nothing to do")
		return
	}

	out, err := exec.Command("claude", "mcp", "remove", "-s", "user", "handshake").CombinedOutput()
	if err != nil {
		fmt.Printf("✗ Claude Code: claude mcp remove failed: %s\n", strings.TrimSpace(string(out)))
		fmt.Println("  Remove manually with: claude mcp remove -s user handshake")
		return
	}
	fmt.Println("✓ Claude Code: removed")
}

// --- OpenCode ---

func registerOpenCode(homeDir string) {
	configPath := filepath.Join(homeDir, ".config", "opencode", "opencode.json")
	entry := map[string]any{"type": "remote", "url": mcpURL()}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		config := map[string]any{
			"$schema": "https://opencode.ai/config.json",
			"mcp":     map[string]any{"handshake": entry},
		}
		if writeErr := writeJSON(configPath, config); writeErr != nil {
			fmt.Printf("✗ OpenCode: could not create config: %v\n", writeErr)
			return
		}
		fmt.Printf("✓ OpenCode: registered (created %s)\n", configPath)
		return
	}
	if err != nil {
		fmt.Printf("✗ OpenCode: could not read config: %v\n", err)
		return
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Println("- OpenCode: config is not plain JSON; add manually to " + configPath + ":")
		fmt.Printf("    \"mcp\": { \"handshake\": { \"type\": \"remote\", \"url\": %q } }\n", mcpURL())
		return
	}

	mcp, _ := config["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	if _, exists := mcp["handshake"]; exists {
		fmt.Println("✓ OpenCode: already registered")
		return
	}
	mcp["handshake"] = entry
	config["mcp"] = mcp

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ OpenCode: could not back up config, not modifying it: %v\n", err)
		return
	}
	if err := writeJSON(configPath, config); err != nil {
		fmt.Printf("✗ OpenCode: could not write config: %v\n", err)
		return
	}
	fmt.Printf("✓ OpenCode: registered (%s, backup saved)\n", configPath)
}

func deregisterOpenCode(homeDir string) {
	configPath := filepath.Join(homeDir, ".config", "opencode", "opencode.json")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("✓ OpenCode: no config found, nothing to do")
		return
	}
	if err != nil {
		fmt.Printf("✗ OpenCode: could not read config: %v\n", err)
		return
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Println("- OpenCode: config is not plain JSON; remove manually from " + configPath + ":")
		fmt.Println("    Remove the \"handshake\" key from the \"mcp\" object")
		return
	}

	mcp, _ := config["mcp"].(map[string]any)
	if mcp == nil {
		fmt.Println("✓ OpenCode: handshake not in config, nothing to do")
		return
	}
	if _, exists := mcp["handshake"]; !exists {
		fmt.Println("✓ OpenCode: handshake not in config, nothing to do")
		return
	}

	delete(mcp, "handshake")

	// If mcp map is now empty, remove the key entirely.
	if len(mcp) == 0 {
		delete(config, "mcp")
	} else {
		config["mcp"] = mcp
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ OpenCode: could not back up config, not modifying it: %v\n", err)
		return
	}
	if err := writeJSON(configPath, config); err != nil {
		fmt.Printf("✗ OpenCode: could not write config: %v\n", err)
		return
	}
	fmt.Println("✓ OpenCode: removed (backup saved)")
}

func removeOpenCodePlugin(homeDir string) {
	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
	if err := os.Remove(pluginPath); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("✓ OpenCode plugin: not installed, nothing to do")
			return
		}
		fmt.Printf("✗ OpenCode plugin: could not remove %s: %v\n", pluginPath, err)
		return
	}
	fmt.Printf("✓ OpenCode plugin: removed (%s)\n", pluginPath)
}

// --- Hermes ---

func registerHermes(homeDir string) {
	configPath := filepath.Join(homeDir, ".hermes", "config.yaml")
	snippet := fmt.Sprintf("mcp_servers:\n  handshake:\n    url: %s\n", mcpURL())

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("- Hermes: no config found — if Hermes is installed, add to " + configPath + ":")
		fmt.Println(indent(snippet, "    "))
		return
	}
	if err != nil {
		fmt.Printf("✗ Hermes: could not read config: %v\n", err)
		return
	}

	lines := strings.Split(string(data), "\n")
	mcpLine := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "mcp_servers:" {
			mcpLine = i
		}
		if mcpLine >= 0 && i > mcpLine && strings.TrimRight(line, " \t") == "  handshake:" {
			fmt.Println("✓ Hermes: already registered")
			return
		}
		if mcpLine >= 0 && i > mcpLine && len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			break
		}
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Hermes: could not back up config, not modifying it: %v\n", err)
		return
	}

	var updated string
	if mcpLine >= 0 {
		inserted := append([]string{}, lines[:mcpLine+1]...)
		inserted = append(inserted, "  handshake:", "    url: "+mcpURL())
		inserted = append(inserted, lines[mcpLine+1:]...)
		updated = strings.Join(inserted, "\n")
	} else if strings.Contains(string(data), "mcp_servers:") {
		fmt.Println("- Hermes: found an mcp_servers key in an unexpected form; add manually to " + configPath + ":")
		fmt.Println(indent(snippet, "    "))
		return
	} else {
		updated = strings.TrimRight(string(data), "\n") + "\n\n" + snippet
	}

	if err := os.WriteFile(configPath, []byte(updated), 0600); err != nil {
		fmt.Printf("✗ Hermes: could not write config: %v\n", err)
		return
	}
	fmt.Printf("✓ Hermes: registered (%s, backup saved)\n", configPath)
}

func deregisterHermes(homeDir string) {
	configPath := filepath.Join(homeDir, ".hermes", "config.yaml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("✓ Hermes: no config found, nothing to do")
		return
	}
	if err != nil {
		fmt.Printf("✗ Hermes: could not read config: %v\n", err)
		return
	}

	lines := strings.Split(string(data), "\n")

	// Find the handshake block under mcp_servers and remove it.
	// We look for:
	//   mcp_servers:        ← mcpLine
	//     handshake:        ← removeStart
	//       url: http://... ← removeEnd (inclusive)
	mcpLine := -1
	removeStart := -1
	removeEnd := -1

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "mcp_servers:" {
			mcpLine = i
			continue
		}
		if mcpLine >= 0 && trimmed == "  handshake:" {
			removeStart = i
			continue
		}
		if removeStart >= 0 && removeEnd < 0 {
			// Collect lines that belong to the handshake block (indented deeper).
			if strings.HasPrefix(line, "    ") {
				removeEnd = i
				continue
			}
			// Line no longer belongs to handshake block.
			break
		}
	}

	if removeStart < 0 {
		fmt.Println("✓ Hermes: handshake not in config, nothing to do")
		return
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Hermes: could not back up config, not modifying it: %v\n", err)
		return
	}

	// Remove lines from removeStart to removeEnd inclusive.
	end := removeEnd
	if end < removeStart {
		end = removeStart
	}
	updated := append(lines[:removeStart], lines[end+1:]...)

	// If mcp_servers block is now empty (next non-blank line is a new top-level
	// key or EOF), remove the mcp_servers line too.
	if mcpLine >= 0 {
		remaining := updated[mcpLine+1:]
		empty := true
		for _, l := range remaining {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if strings.HasPrefix(l, " ") {
				// Still has indented children.
				empty = false
			}
			break
		}
		if empty {
			updated = append(updated[:mcpLine], updated[mcpLine+1:]...)
		}
	}

	if err := os.WriteFile(configPath, []byte(strings.Join(updated, "\n")), 0600); err != nil {
		fmt.Printf("✗ Hermes: could not write config: %v\n", err)
		return
	}
	fmt.Println("✓ Hermes: removed (backup saved)")
}

// writeJSON marshals v and writes it to path, creating parent dirs as needed.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// promptYesNo asks the user a yes/no question and returns true if they confirm.
// The default is no unless the user types y or yes.
func promptYesNo(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}