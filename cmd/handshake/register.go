package main

import (
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

// --- Claude Code: delegate to its own CLI so we never touch its config file.

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

	// User scope: the daemon serves every project, not just the current one.
	out, err := exec.Command("claude", "mcp", "add", "-s", "user", "--transport", "http", "handshake", mcpURL()).CombinedOutput()
	if err != nil {
		fmt.Printf("✗ Claude Code: claude mcp add failed: %s\n", strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("✓ Claude Code: registered (via claude mcp add)")
}

// --- OpenCode: parse ~/.config/opencode/opencode.json and merge the mcp key.

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
		// Likely JSONC (comments) — rewriting would destroy them.
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

// --- Hermes: ~/.hermes/config.yaml is large, hand-maintained YAML. A library
// round-trip would strip comments and reorder keys, so instead splice the
// handshake entry in as text directly under the top-level mcp_servers key,
// leaving every other byte of the file untouched.

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
		// Already registered? Look for a "handshake:" map entry at the
		// first indent level under mcp_servers.
		if mcpLine >= 0 && i > mcpLine && strings.TrimRight(line, " \t") == "  handshake:" {
			fmt.Println("✓ Hermes: already registered")
			return
		}
		// Stop scanning for the existing entry once the block ends.
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
		// mcp_servers exists but in a form we don't understand (flow style,
		// nested, commented) — don't guess.
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

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
