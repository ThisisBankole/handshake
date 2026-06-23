package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	claudecode "handshake/plugins/claudecode"
	codexplugin "handshake/plugins/codex"
	hermesplugin "handshake/plugins/hermes"
)

func registerAgents(homeDir string) {
	registerClaudeCode()
	registerOpenCode(homeDir)
	registerHermes(homeDir)
	registerCodexMCP(homeDir)
}

func deregisterAgents(homeDir string, deleteDB bool) {
	fmt.Println("Removing Handshake from each agent:")
	deregisterClaudeCode()
	deregisterOpenCode(homeDir)
	deregisterHermes(homeDir)
	removeOpenCodePlugin(homeDir)
	deregisterClaudeCodeHooks(homeDir)
	deregisterHermesPlugin(homeDir)
	deregisterCodexHooks(homeDir)
	deregisterCodexMCP(homeDir)

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
	if env := os.Getenv("HANDSHAKE_URL"); env != "" {
		return env
	}
	return "http://" + listenAddr + "/mcp"
}

func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".handshake.bak", data, 0600)
}

// --- Claude Code MCP registration ---

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

// --- Claude Code hooks (PreCompact + PostCompact) ---

func registerClaudeCodeHooks(homeDir string) {
	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not create hooks directory: %v\n", err)
		return
	}

	preCompactPath := filepath.Join(hooksDir, "handshake_pre_compact.py")
	postCompactPath := filepath.Join(hooksDir, "handshake_post_compact.py")
	stopPath := filepath.Join(hooksDir, "handshake_stop.py")

	if err := os.WriteFile(preCompactPath, claudecode.PreCompactHook, 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write pre_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(postCompactPath, claudecode.PostCompactHook, 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write post_compact hook: %v\n", err)
		return
	}

	if err := os.WriteFile(stopPath, claudecode.StopHook, 0755); err != nil { // ← add
		fmt.Printf("✗ Claude Code hooks: could not write stop hook: %v\n", err)
		return
	}

	hooksConfigPath := filepath.Join(homeDir, ".claude", "hooks.json")
	registerClaudeCodeHooksConfig(hooksConfigPath, preCompactPath, postCompactPath, stopPath)
}

func registerClaudeCodeHooksConfig(hooksConfigPath, preCompactPath, postCompactPath, stopPath string) {
	var config map[string]any
	data, err := os.ReadFile(hooksConfigPath)
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Println("- Claude Code hooks: hooks.json is not valid JSON; add manually:")
			printClaudeCodeHooksSnippet(preCompactPath, postCompactPath, stopPath)
			return
		}
	} else {
		config = map[string]any{}
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	if preCompacts, ok := hooks["PreCompact"].([]any); ok {
		for _, h := range preCompacts {
			if hm, ok := h.(map[string]any); ok {
				if hooksArr, ok := hm["hooks"].([]any); ok {
					for _, hh := range hooksArr {
						if hhm, ok := hh.(map[string]any); ok {
							if cmd, ok := hhm["command"].(string); ok {
								if strings.Contains(cmd, "handshake") {
									fmt.Println("✓ Claude Code hooks: already registered")
									return
								}
							}
						}
					}
				}
			}
		}
	}

	preCompactEntry := map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       fmt.Sprintf("python3 %s", preCompactPath),
				"statusMessage": "Handshake: checkpointing session",
			},
		},
	}

	postCompactEntry := map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       fmt.Sprintf("python3 %s", postCompactPath),
				"statusMessage": "Handshake: capturing compaction summary",
			},
		},
	}

	preCompacts, _ := hooks["PreCompact"].([]any)
	hooks["PreCompact"] = append(preCompacts, preCompactEntry)
	postCompacts, _ := hooks["PostCompact"].([]any)
	hooks["PostCompact"] = append(postCompacts, postCompactEntry)
	stops, _ := hooks["Stop"].([]any)
	hooks["Stop"] = append(stops, map[string]any{
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("python3 %s", stopPath),
			"statusMessage": "Handshake: syncing session",
			"timeout":       30,
		}},
	})

	config["hooks"] = hooks

	if _, err := os.Stat(hooksConfigPath); err == nil {
		backup(hooksConfigPath)
	}

	if err := writeJSON(hooksConfigPath, config); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write hooks.json: %v\n", err)
		return
	}

	fmt.Println("✓ Claude Code hooks: registered (PreCompact + PostCompact + Stop)")
}

func printClaudeCodeHooksSnippet(preCompactPath, postCompactPath, stopPath string) {
	fmt.Printf(`  Add to ~/.claude/hooks.json:
  {
    "hooks": {
      "PreCompact": [{"matcher":"auto|manual","hooks":[{"type":"command","command":"python3 %s"}]}],
      "PostCompact": [{"matcher":"auto|manual","hooks":[{"type":"command","command":"python3 %s"}]}],
      "Stop": [{"hooks":[{"type":"command","command":"python3 %s","timeout":30}]}]
    }
  }
`, preCompactPath, postCompactPath, stopPath)
}

func deregisterClaudeCodeHooks(homeDir string) {
	// fmt.Println("DEBUG: running deregisterClaudeCodeHooks")
	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	os.Remove(filepath.Join(hooksDir, "handshake_pre_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_post_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_stop.py"))

	hooksConfigPath := filepath.Join(homeDir, ".claude", "hooks.json")
	data, err := os.ReadFile(hooksConfigPath)
	if err != nil {
		fmt.Println("✓ Claude Code hooks: nothing to remove")
		return
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		return
	}

	for _, key := range []string{"PreCompact", "PostCompact", "Stop"} {
		entries, _ := hooks[key].([]any)
		var filtered []any
		for _, entry := range entries {
			em, ok := entry.(map[string]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			hooksArr, _ := em["hooks"].([]any)
			isHandshake := false
			for _, h := range hooksArr {
				hm, ok := h.(map[string]any)
				if !ok {
					continue
				}
				if cmd, ok := hm["command"].(string); ok && strings.Contains(cmd, "handshake") {
					isHandshake = true
					break
				}
			}
			if !isHandshake {
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) == 0 {
			delete(hooks, key)
		} else {
			hooks[key] = filtered
		}
	}

	config["hooks"] = hooks
	backup(hooksConfigPath)
	writeJSON(hooksConfigPath, config)
	fmt.Println("✓ Claude Code hooks: removed")
}

// --- OpenCode MCP registration ---

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
		// MCP already registered — but still ensure plugin key is present
		pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
		existing, _ := config["plugin"].([]any)
		alreadyRegistered := false
		for _, p := range existing {
			if p == pluginPath {
				alreadyRegistered = true
				break
			}
		}
		if !alreadyRegistered {
			config["plugin"] = append(existing, pluginPath)
			backup(configPath)
			writeJSON(configPath, config)
			fmt.Println("✓ OpenCode: plugin path registered")
		} else {
			fmt.Println("✓ OpenCode: already registered")
		}
		return
	}
	mcp["handshake"] = entry
	config["mcp"] = mcp

	// Register the plugin file explicitly — directory auto-discovery
	// is unreliable in some OpenCode versions
	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
	existing, _ := config["plugin"].([]any)
	alreadyRegistered := false
	for _, p := range existing {
		if p == pluginPath {
			alreadyRegistered = true
			break
		}
	}
	if !alreadyRegistered {
		config["plugin"] = append(existing, pluginPath)
	}

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
	if len(mcp) == 0 {
		delete(config, "mcp")
	} else {
		config["mcp"] = mcp
	}

	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
	if plugins, ok := config["plugin"].([]any); ok {
		var filtered []any
		for _, p := range plugins {
			if p != pluginPath {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			delete(config, "plugin")
		} else {
			config["plugin"] = filtered
		}
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

// --- Hermes MCP registration ---

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
			if strings.HasPrefix(line, "    ") {
				removeEnd = i
				continue
			}
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

	end := removeEnd
	if end < removeStart {
		end = removeStart
	}
	updated := append(lines[:removeStart], lines[end+1:]...)

	if mcpLine >= 0 {
		remaining := updated[mcpLine+1:]
		empty := true
		for _, l := range remaining {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if strings.HasPrefix(l, " ") {
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

// registerHermesPlugin installs the Handshake plugin into ~/.hermes/hooks/handshake/
// Hermes auto-discovers plugins in that directory at startup.
func registerHermesPlugin(homeDir string) {
	pluginDir := filepath.Join(homeDir, ".hermes", "hooks", "handshake")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		fmt.Printf("✗ Hermes plugin: could not create plugin directory: %v\n", err)
		return
	}

	hookYAMLPath := filepath.Join(pluginDir, "HOOK.yaml")
	handlerPath := filepath.Join(pluginDir, "handler.py")

	if err := os.WriteFile(hookYAMLPath, hermesplugin.HookYAML, 0644); err != nil {
		fmt.Printf("✗ Hermes plugin: could not write HOOK.yaml: %v\n", err)
		return
	}
	if err := os.WriteFile(handlerPath, hermesplugin.HandlerPY, 0755); err != nil {
		fmt.Printf("✗ Hermes plugin: could not write handler.py: %v\n", err)
		return
	}

	fmt.Println("✓ Hermes plugin: installed (~/.hermes/hooks/handshake/)")
}

// deregisterHermesPlugin removes the Handshake plugin from ~/.hermes/hooks/
func deregisterHermesPlugin(homeDir string) {
	pluginDir := filepath.Join(homeDir, ".hermes", "hooks", "handshake")
	if err := os.RemoveAll(pluginDir); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("✗ Hermes plugin: could not remove %s: %v\n", pluginDir, err)
			return
		}
	}
	fmt.Println("✓ Hermes plugin: removed")
}

// --- Helpers ---

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

func promptYesNo(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

// --- Codex MCP registration ---

// registerCodexMCP adds Handshake as an MCP server in ~/.codex/config.toml.
// Codex uses TOML not JSON, so we append a snippet rather than parsing.
func registerCodexMCP(homeDir string) {
	// Check if codex is installed
	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Println("- Codex: not installed, skipping")
		return
	}

	configPath := filepath.Join(homeDir, ".codex", "config.toml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// Create minimal config with MCP entry
		snippet := fmt.Sprintf("[mcp_servers.handshake]\nurl = %q\n", mcpURL())
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			fmt.Printf("✗ Codex: could not create config directory: %v\n", err)
			return
		}
		if err := os.WriteFile(configPath, []byte(snippet), 0644); err != nil {
			fmt.Printf("✗ Codex: could not create config.toml: %v\n", err)
			return
		}
		fmt.Println("✓ Codex: registered (created config.toml)")
		return
	}
	if err != nil {
		fmt.Printf("✗ Codex: could not read config.toml: %v\n", err)
		return
	}

	// Already registered?
	if strings.Contains(string(data), "[mcp_servers.handshake]") {
		fmt.Println("✓ Codex: already registered")
		return
	}

	// Back up and append
	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Codex: could not back up config.toml: %v\n", err)
		return
	}

	snippet := fmt.Sprintf("\n[mcp_servers.handshake]\nurl = %q\n", mcpURL())
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("✗ Codex: could not open config.toml: %v\n", err)
		return
	}
	defer f.Close()
	f.WriteString(snippet)
	fmt.Println("✓ Codex: registered (config.toml, backup saved)")
}

// deregisterCodexMCP removes the Handshake MCP entry from ~/.codex/config.toml.
func deregisterCodexMCP(homeDir string) {
	//fmt.Println("DEBUG: running deregisterCodexMCP")
	configPath := filepath.Join(homeDir, ".codex", "config.toml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("✓ Codex: no config found, nothing to do")
		return
	}
	if err != nil {
		fmt.Printf("✗ Codex: could not read config.toml: %v\n", err)
		return
	}

	if !strings.Contains(string(data), "[mcp_servers.handshake]") {
		fmt.Println("✓ Codex: handshake not in config, nothing to do")
		return
	}

	// Remove the [mcp_servers.handshake] block — find it and strip it
	lines := strings.Split(string(data), "\n")
	var filtered []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[mcp_servers.handshake]" {
			skip = true
			continue
		}
		// Stop skipping when we hit the next section header
		if skip && strings.HasPrefix(trimmed, "[") {
			skip = false
		}
		if !skip {
			filtered = append(filtered, line)
		}
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Codex: could not back up config.toml: %v\n", err)
		return
	}

	if err := os.WriteFile(configPath, []byte(strings.Join(filtered, "\n")), 0644); err != nil {
		fmt.Printf("✗ Codex: could not write config.toml: %v\n", err)
		return
	}
	fmt.Println("✓ Codex: removed (backup saved)")
}

// --- Codex hooks (PreCompact + PostCompact + Stop) ---

// registerCodexHooks installs the three hook scripts and registers them
// in ~/.codex/hooks.json.
func registerCodexHooks(homeDir string) {
	// Check if codex is installed
	if _, err := exec.LookPath("codex"); err != nil {
		return // silently skip — codex not installed
	}

	hooksDir := filepath.Join(homeDir, ".codex", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not create hooks directory: %v\n", err)
		return
	}

	preCompactPath := filepath.Join(hooksDir, "handshake_pre_compact.py")
	postCompactPath := filepath.Join(hooksDir, "handshake_post_compact.py")
	stopPath := filepath.Join(hooksDir, "handshake_stop.py")

	if err := os.WriteFile(preCompactPath, codexplugin.PreCompactHook, 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write pre_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(postCompactPath, codexplugin.PostCompactHook, 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write post_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(stopPath, codexplugin.StopHook, 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write stop hook: %v\n", err)
		return
	}

	hooksConfigPath := filepath.Join(homeDir, ".codex", "hooks.json")
	registerCodexHooksConfig(hooksConfigPath, preCompactPath, postCompactPath, stopPath)
}

func registerCodexHooksConfig(hooksConfigPath, preCompactPath, postCompactPath, stopPath string) {
	var config map[string]any
	data, err := os.ReadFile(hooksConfigPath)
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Println("- Codex hooks: hooks.json is not valid JSON; add manually")
			return
		}
	} else {
		config = map[string]any{}
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	// Check if already registered
	if preCompacts, ok := hooks["PreCompact"].([]any); ok {
		for _, h := range preCompacts {
			if hm, ok := h.(map[string]any); ok {
				if hooksArr, ok := hm["hooks"].([]any); ok {
					for _, hh := range hooksArr {
						if hhm, ok := hh.(map[string]any); ok {
							if cmd, ok := hhm["command"].(string); ok {
								if strings.Contains(cmd, "handshake") {
									fmt.Println("✓ Codex hooks: already registered")
									return
								}
							}
						}
					}
				}
			}
		}
	}

	// PreCompact
	preCompacts, _ := hooks["PreCompact"].([]any)
	hooks["PreCompact"] = append(preCompacts, map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("python3 %s", preCompactPath),
			"statusMessage": "Handshake: checkpointing session",
		}},
	})

	// PostCompact
	postCompacts, _ := hooks["PostCompact"].([]any)
	hooks["PostCompact"] = append(postCompacts, map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("python3 %s", postCompactPath),
			"statusMessage": "Handshake: capturing compaction summary",
		}},
	})

	// Stop — no matcher (Stop doesn't support matchers)
	stops, _ := hooks["Stop"].([]any)
	hooks["Stop"] = append(stops, map[string]any{
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("python3 %s", stopPath),
			"statusMessage": "Handshake: syncing session",
			"timeout":       30,
		}},
	})

	config["hooks"] = hooks

	if _, err := os.Stat(hooksConfigPath); err == nil {
		backup(hooksConfigPath)
	}

	if err := writeJSON(hooksConfigPath, config); err != nil {
		fmt.Printf("✗ Codex hooks: could not write hooks.json: %v\n", err)
		return
	}

	fmt.Println("✓ Codex hooks: registered (PreCompact + PostCompact + Stop)")
	fmt.Println("  ⚠ Open Codex and run /hooks to trust the hook scripts")
}

// deregisterCodexHooks removes hook scripts and entries from hooks.json.
func deregisterCodexHooks(homeDir string) {
	// fmt.Println("DEBUG: running deregisterCodexHooks")
	hooksDir := filepath.Join(homeDir, ".codex", "hooks")
	os.Remove(filepath.Join(hooksDir, "handshake_pre_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_post_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_stop.py"))

	hooksConfigPath := filepath.Join(homeDir, ".codex", "hooks.json")
	data, err := os.ReadFile(hooksConfigPath)
	if err != nil {
		fmt.Println("✓ Codex hooks: nothing to remove")
		return
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		return
	}

	for _, key := range []string{"PreCompact", "PostCompact", "Stop"} {
		entries, _ := hooks[key].([]any)
		var filtered []any
		for _, entry := range entries {
			em, ok := entry.(map[string]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			hooksArr, _ := em["hooks"].([]any)
			isHandshake := false
			for _, h := range hooksArr {
				hm, ok := h.(map[string]any)
				if !ok {
					continue
				}
				if cmd, ok := hm["command"].(string); ok && strings.Contains(cmd, "handshake") {
					isHandshake = true
					break
				}
			}
			if !isHandshake {
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) == 0 {
			delete(hooks, key)
		} else {
			hooks[key] = filtered
		}
	}

	config["hooks"] = hooks
	backup(hooksConfigPath)
	writeJSON(hooksConfigPath, config)
	fmt.Println("✓ Codex hooks: removed")
	deregisterCodexMCP(homeDir)
}
