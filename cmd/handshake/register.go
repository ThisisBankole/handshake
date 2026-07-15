package main

import (
	"bufio"
	"bytes"
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
	registerClaudeCode(homeDir)
	registerOpenCode(homeDir)
	registerHermes(homeDir)
	registerCodexMCP(homeDir)
}

func deregisterAgents(homeDir string, deleteDB bool) {
	fmt.Println("Removing Handshake from each agent:")
	deregisterClaudeCode(homeDir)
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

// baseURL returns the daemon's root URL (without the /mcp path), matching the
// address agents are registered against. Used to template the resolved port
// into the hook scripts so they reach the daemon even when setup picked a
// non-default port.
func baseURL() string {
	if env := os.Getenv("HANDSHAKE_URL"); env != "" {
		return strings.TrimSuffix(env, "/mcp")
	}
	return "http://" + listenAddr
}

// injectBaseURL replaces the hardcoded localhost:8765 base URL in a hook
// script with the resolved daemon URL. Hook scripts are invoked by agents
// whose runtime environment does not carry HANDSHAKE_URL, so the URL must be
// baked in at install time.
func injectBaseURL(script []byte, baseURL string) []byte {
	return bytes.ReplaceAll(script, []byte("http://localhost:8765"), []byte(baseURL))
}

// resolvePython returns the absolute path to a Python 3 interpreter, preferring
// "python3" and falling back to "python" only when it is actually Python 3.
// Returns "" if no suitable interpreter is on PATH. Hook scripts use only the
// stdlib (json, urllib), so any Python 3 suffices. The absolute path is used so
// hooks still run when the agent invokes them with a minimal PATH.
func resolvePython() string {
	if path, err := exec.LookPath("python3"); err == nil {
		return path
	}
	if path, err := exec.LookPath("python"); err == nil && isPython3(path) {
		return path
	}
	return ""
}

// isPython3 reports whether the binary at path is Python 3, by running a tiny
// version probe. Prevents treating a Python 2 "python" as usable.
func isPython3(path string) bool {
	err := exec.Command(path, "-c", "import sys; sys.exit(0 if sys.version_info[0] >= 3 else 1)").Run()
	return err == nil
}

func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return writeFileAtomic(path+".handshake.bak", data, 0600)
}

// writeFileAtomic replaces path only after the complete contents have been
// written and synced to a temporary file in the same directory. Existing file
// permissions are preserved so agent config files keep their original access.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// --- Claude Code MCP registration ---

func registerClaudeCode(homeDir string) {
	if _, err := exec.LookPath("claude"); err == nil {
		// Remove any existing registration (ignore "not found") then re-add with the
		// current URL, so re-running setup corrects a stale port instead of leaving
		// the old URL in place via an "already registered" short-circuit.
		exec.Command("claude", "mcp", "remove", "-s", "user", "handshake").Run()
		out, err := exec.Command("claude", "mcp", "add", "-s", "user", "--transport", "http", "handshake", mcpURL()).CombinedOutput()
		if err != nil {
			fmt.Printf("✗ Claude Code: claude mcp add failed: %s\n", strings.TrimSpace(string(out)))
			return
		}
		fmt.Println("✓ Claude Code: registered (via claude mcp add)")
		return
	}

	if !hasClaudeCodeLocal(homeDir) {
		fmt.Println("- Claude Code: not installed, skipping")
		return
	}
	registerClaudeCodeMCPConfig(homeDir)
}

func hasClaudeCodeLocal(homeDir string) bool {
	for _, path := range []string{
		filepath.Join(homeDir, ".claude"),
		filepath.Join(homeDir, ".claude.json"),
		filepath.Join(homeDir, "Library", "Application Support", "Claude"),
		"/Applications/Claude.app",
		filepath.Join(homeDir, "Applications", "Claude.app"),
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// registerClaudeCodeMCPConfig supports Claude Desktop users, who may not have
// the separate claude CLI installed but still read ~/.claude.json.
func registerClaudeCodeMCPConfig(homeDir string) {
	configPath := filepath.Join(homeDir, ".claude.json")
	config := map[string]any{}
	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Println("- Claude Code: ~/.claude.json is not valid JSON; skipping MCP registration")
			return
		}
	} else if !os.IsNotExist(err) {
		fmt.Printf("✗ Claude Code: could not read ~/.claude.json: %v\n", err)
		return
	}

	mcpServers, _ := config["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = map[string]any{}
	}
	mcpServers["handshake"] = map[string]any{"type": "http", "url": mcpURL()}
	config["mcpServers"] = mcpServers

	if err == nil {
		if err := backup(configPath); err != nil {
			fmt.Printf("✗ Claude Code: could not back up ~/.claude.json: %v\n", err)
			return
		}
	}
	if err := writeJSON(configPath, config); err != nil {
		fmt.Printf("✗ Claude Code: could not write ~/.claude.json: %v\n", err)
		return
	}
	fmt.Println("✓ Claude Code: registered (desktop configuration)")
}

func deregisterClaudeCode(homeDir string) {
	if _, err := exec.LookPath("claude"); err == nil {
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
		return
	}
	deregisterClaudeCodeMCPConfig(homeDir)
}

func deregisterClaudeCodeMCPConfig(homeDir string) {
	configPath := filepath.Join(homeDir, ".claude.json")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("✓ Claude Code: no configuration found, nothing to remove")
		return
	}
	if err != nil {
		fmt.Printf("✗ Claude Code: could not read ~/.claude.json: %v\n", err)
		return
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Println("- Claude Code: ~/.claude.json is not valid JSON; remove Handshake manually")
		return
	}
	mcpServers, _ := config["mcpServers"].(map[string]any)
	if _, exists := mcpServers["handshake"]; !exists {
		fmt.Println("✓ Claude Code: Handshake not registered, nothing to remove")
		return
	}
	delete(mcpServers, "handshake")
	if len(mcpServers) == 0 {
		delete(config, "mcpServers")
	}
	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Claude Code: could not back up ~/.claude.json: %v\n", err)
		return
	}
	if err := writeJSON(configPath, config); err != nil {
		fmt.Printf("✗ Claude Code: could not write ~/.claude.json: %v\n", err)
		return
	}
	fmt.Println("✓ Claude Code: removed (desktop configuration)")
}

// --- Claude Code hooks (PreCompact + PostCompact) ---

func registerClaudeCodeHooks(homeDir string) {
	python := resolvePython()
	if python == "" {
		fmt.Println("- Claude Code hooks: Python 3 not found on PATH — skipping auto-checkpoint hooks")
		fmt.Println("    Install Python 3 to enable automatic syncing, or checkpoint manually with \"checkpoint this session\".")
		return
	}

	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not create hooks directory: %v\n", err)
		return
	}

	preCompactPath := filepath.Join(hooksDir, "handshake_pre_compact.py")
	postCompactPath := filepath.Join(hooksDir, "handshake_post_compact.py")
	stopPath := filepath.Join(hooksDir, "handshake_stop.py")

	if err := os.WriteFile(preCompactPath, injectBaseURL(claudecode.PreCompactHook, baseURL()), 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write pre_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(postCompactPath, injectBaseURL(claudecode.PostCompactHook, baseURL()), 0755); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write post_compact hook: %v\n", err)
		return
	}

	if err := os.WriteFile(stopPath, injectBaseURL(claudecode.StopHook, baseURL()), 0755); err != nil { // ← add
		fmt.Printf("✗ Claude Code hooks: could not write stop hook: %v\n", err)
		return
	}

	// Hooks must live in settings.json — Claude Code does not read ~/.claude/hooks.json.
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	registerClaudeCodeHooksConfig(settingsPath, python, preCompactPath, postCompactPath, stopPath)

	// Earlier versions wrote hook config to ~/.claude/hooks.json, which Claude
	// Code never reads. Strip our stale entries so they don't confuse anyone.
	if _, err := removeHandshakeHooksFromConfig(filepath.Join(homeDir, ".claude", "hooks.json")); err != nil {
		fmt.Printf("- Claude Code hooks: could not clean legacy hooks.json: %v\n", err)
	}
}

func registerClaudeCodeHooksConfig(settingsPath, python, preCompactPath, postCompactPath, stopPath string) {
	var config map[string]any
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Println("- Claude Code hooks: settings.json is not valid JSON; add manually:")
			printClaudeCodeHooksSnippet(python, preCompactPath, postCompactPath, stopPath)
			return
		}
	} else if os.IsNotExist(err) {
		config = map[string]any{}
	} else {
		fmt.Printf("✗ Claude Code hooks: could not read settings.json: %v\n", err)
		return
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
				"command":       fmt.Sprintf("%s %s", python, preCompactPath),
				"statusMessage": "Handshake: checkpointing session",
			},
		},
	}

	postCompactEntry := map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       fmt.Sprintf("%s %s", python, postCompactPath),
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
			"command":       fmt.Sprintf("%s %s", python, stopPath),
			"statusMessage": "Handshake: syncing session",
			"timeout":       30,
		}},
	})

	config["hooks"] = hooks

	if _, err := os.Stat(settingsPath); err == nil {
		if err := backup(settingsPath); err != nil {
			fmt.Printf("✗ Claude Code hooks: could not back up settings.json, not modifying it: %v\n", err)
			return
		}
	} else if !os.IsNotExist(err) {
		fmt.Printf("✗ Claude Code hooks: could not inspect settings.json: %v\n", err)
		return
	}

	if err := writeJSON(settingsPath, config); err != nil {
		fmt.Printf("✗ Claude Code hooks: could not write settings.json: %v\n", err)
		return
	}

	fmt.Println("✓ Claude Code hooks: registered (PreCompact + PostCompact + Stop)")
}

func printClaudeCodeHooksSnippet(python, preCompactPath, postCompactPath, stopPath string) {
	fmt.Printf(`  Add to ~/.claude/settings.json:
  {
    "hooks": {
      "PreCompact": [{"matcher":"auto|manual","hooks":[{"type":"command","command":"%s %s"}]}],
      "PostCompact": [{"matcher":"auto|manual","hooks":[{"type":"command","command":"%s %s"}]}],
      "Stop": [{"hooks":[{"type":"command","command":"%s %s","timeout":30}]}]
    }
  }
`, python, preCompactPath, python, postCompactPath, python, stopPath)
}

func deregisterClaudeCodeHooks(homeDir string) {
	removed, err := removeHandshakeHooksFromConfig(filepath.Join(homeDir, ".claude", "settings.json"))
	if err != nil {
		fmt.Printf("✗ Claude Code hooks: could not update settings.json: %v\n", err)
		return
	}
	// Legacy location from versions that wrote hooks.json (never read by Claude Code).
	legacyRemoved, err := removeHandshakeHooksFromConfig(filepath.Join(homeDir, ".claude", "hooks.json"))
	if err != nil {
		fmt.Printf("✗ Claude Code hooks: could not update legacy hooks.json: %v\n", err)
		return
	}
	removed = legacyRemoved || removed

	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	os.Remove(filepath.Join(hooksDir, "handshake_pre_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_post_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_stop.py"))

	if removed {
		fmt.Println("✓ Claude Code hooks: removed")
	} else {
		fmt.Println("✓ Claude Code hooks: nothing to remove")
	}
}

// removeHandshakeHooksFromConfig strips handshake hook entries from the given
// hooks config file. It returns whether anything was removed.
func removeHandshakeHooksFromConfig(configPath string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return false, fmt.Errorf("invalid JSON: %w", err)
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}

	removed := false

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
			} else {
				removed = true
			}
		}
		if len(filtered) == 0 {
			delete(hooks, key)
		} else {
			hooks[key] = filtered
		}
	}

	if !removed {
		return false, nil
	}

	config["hooks"] = hooks
	if err := backup(configPath); err != nil {
		return false, fmt.Errorf("back up config: %w", err)
	}
	if err := writeJSON(configPath, config); err != nil {
		return false, fmt.Errorf("write config: %w", err)
	}
	return true, nil
}

// --- OpenCode MCP registration ---

func registerOpenCode(homeDir string) {
	configPath := filepath.Join(homeDir, ".config", "opencode", "opencode.json")
	entry := map[string]any{"type": "remote", "url": mcpURL()}
	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		config := map[string]any{
			"$schema": "https://opencode.ai/config.json",
			"mcp":     map[string]any{"handshake": entry},
			"plugin":  []any{pluginPath},
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

	// Always set the handshake entry with the current URL so re-running setup
	// corrects a stale port instead of preserving the old one.
	mcp, _ := config["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	mcp["handshake"] = entry
	config["mcp"] = mcp

	// Ensure the plugin file is registered — directory auto-discovery is
	// unreliable in some OpenCode versions.
	existing, _ := config["plugin"].([]any)
	pluginRegistered := false
	for _, p := range existing {
		if p == pluginPath {
			pluginRegistered = true
			break
		}
	}
	if !pluginRegistered {
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

// indentOf returns the number of leading space characters in line. YAML
// forbids tabs for indentation, so only spaces are counted.
func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r == ' ' {
			n++
			continue
		}
		break
	}
	return n
}

// hermesChildIndent returns the indentation width of children under the
// mcp_servers: key at mcpLine, by inspecting the first non-blank line that
// follows it. Returns defaultIndent if mcp_servers has no children.
func hermesChildIndent(lines []string, mcpLine, defaultIndent int) int {
	for i := mcpLine + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) == 0 {
			break
		}
		return indentOf(lines[i])
	}
	return defaultIndent
}

// hermesGrandchildIndent returns the indentation of the first grandchild found
// under any child of mcp_servers (i.e. the indent used for keys like url:).
// Returns defaultIndent when mcp_servers has no children with children.
func hermesGrandchildIndent(lines []string, mcpLine, childIndent, defaultIndent int) int {
	for i := mcpLine + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ind := indentOf(lines[i])
		if ind == 0 {
			break
		}
		if ind == childIndent {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					continue
				}
				jind := indentOf(lines[j])
				if jind <= childIndent {
					break
				}
				return jind
			}
		}
	}
	return defaultIndent
}

// hermesMCPBlockIsEmpty reports whether the mcp_servers: key at mcpLine has no
// child entries (only blank/comment lines follow before the next top-level key).
func hermesMCPBlockIsEmpty(lines []string, mcpLine int) bool {
	for i := mcpLine + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return indentOf(lines[i]) == 0
	}
	return true
}

// hermesChildKey returns the YAML key of a child line (the text before the
// first colon, trimmed).
func hermesChildKey(line string) string {
	return strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
}

// hermesInjectMCP inserts the handshake MCP server under mcp_servers: in a
// Hermes config split into lines. It returns the updated lines, whether
// handshake was already present, and whether an mcp_servers: key at column 0
// existed. The entry is inserted with indentation matching the existing
// children of mcp_servers (defaulting to 2 spaces) so it works with configs
// that use non-standard indentation.
func hermesInjectMCP(lines []string, url string) (updated []string, already, mcpFound bool) {
	mcpLine := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "mcp_servers:" {
			mcpLine = i
			break
		}
	}
	if mcpLine == -1 {
		return lines, false, false
	}

	childIndent := hermesChildIndent(lines, mcpLine, 2)
	urlIndent := hermesGrandchildIndent(lines, mcpLine, childIndent, childIndent+2)

	for i := mcpLine + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ind := indentOf(lines[i])
		if ind == 0 {
			break
		}
		if ind == childIndent && hermesChildKey(lines[i]) == "handshake" {
			return lines, true, true
		}
	}

	entry := []string{
		strings.Repeat(" ", childIndent) + "handshake:",
		strings.Repeat(" ", urlIndent) + "url: " + url,
	}
	updated = append([]string{}, lines[:mcpLine+1]...)
	updated = append(updated, entry...)
	updated = append(updated, lines[mcpLine+1:]...)
	return updated, false, true
}

// hermesRemoveHandshake removes the handshake entry and all its nested children
// from a Hermes config split into lines. The mcp_servers: key is kept even if
// handshake was the only child. Returns the updated lines and whether handshake
// was present.
func hermesRemoveHandshake(lines []string) (updated []string, removed bool) {
	mcpLine := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "mcp_servers:" {
			mcpLine = i
			break
		}
	}
	if mcpLine == -1 {
		return lines, false
	}

	childIndent := hermesChildIndent(lines, mcpLine, 2)

	removeStart := -1
	for i := mcpLine + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ind := indentOf(lines[i])
		if ind == 0 {
			break
		}
		if ind == childIndent && hermesChildKey(lines[i]) == "handshake" {
			removeStart = i
			break
		}
	}
	if removeStart == -1 {
		return lines, false
	}

	handshakeIndent := indentOf(lines[removeStart])
	end := removeStart + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if indentOf(line) > handshakeIndent {
			end++
			continue
		}
		break
	}

	updated = append([]string{}, lines[:removeStart]...)
	updated = append(updated, lines[end:]...)
	return updated, true
}

// hermesRemoveMCP removes the handshake entry and all its nested children from
// a Hermes config split into lines. Returns the updated lines and whether
// handshake was present. If handshake was the only child, the mcp_servers: key
// is removed too.
func hermesRemoveMCP(lines []string) (updated []string, removed bool) {
	updated, removed = hermesRemoveHandshake(lines)
	if !removed {
		return updated, removed
	}
	mcpLine := -1
	for i, line := range updated {
		if strings.TrimRight(line, " \t") == "mcp_servers:" {
			mcpLine = i
			break
		}
	}
	if mcpLine != -1 && hermesMCPBlockIsEmpty(updated, mcpLine) {
		updated = append(updated[:mcpLine], updated[mcpLine+1:]...)
	}
	return updated, true
}

func registerHermes(homeDir string) {
	configPath := filepath.Join(homeDir, ".hermes", "config.yaml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		snippet := fmt.Sprintf("mcp_servers:\n  handshake:\n    url: %s\n", mcpURL())
		fmt.Println("- Hermes: no config found — if Hermes is installed, add to " + configPath + ":")
		fmt.Println(indent(snippet, "    "))
		return
	}
	if err != nil {
		fmt.Printf("✗ Hermes: could not read config: %v\n", err)
		return
	}

	lines := strings.Split(string(data), "\n")
	lines, _ = hermesRemoveHandshake(lines)
	updated, _, mcpFound := hermesInjectMCP(lines, mcpURL())

	var output string
	if mcpFound {
		output = strings.Join(updated, "\n")
	} else {
		snippet := fmt.Sprintf("mcp_servers:\n  handshake:\n    url: %s\n", mcpURL())
		output = strings.TrimRight(string(data), "\n") + "\n\n" + snippet
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Hermes: could not back up config, not modifying it: %v\n", err)
		return
	}
	if err := writeFileAtomic(configPath, []byte(output), 0600); err != nil {
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
	updated, removed := hermesRemoveMCP(lines)
	if !removed {
		fmt.Println("✓ Hermes: handshake not in config, nothing to do")
		return
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Hermes: could not back up config, not modifying it: %v\n", err)
		return
	}
	if err := writeFileAtomic(configPath, []byte(strings.Join(updated, "\n")), 0600); err != nil {
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
	if err := os.WriteFile(handlerPath, injectBaseURL(hermesplugin.HandlerPY, baseURL()), 0755); err != nil {
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
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0644)
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

// removeCodexMCPBlock strips the [mcp_servers.handshake] section (header +
// subsequent key-value lines) from a TOML file split into lines.
func removeCodexMCPBlock(lines []string) []string {
	var filtered []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[mcp_servers.handshake]" {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(trimmed, "[") {
			skip = false
		}
		if !skip {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func codexHome(homeDir string) string {
	if configured := os.Getenv("CODEX_HOME"); configured != "" {
		return configured
	}
	return filepath.Join(homeDir, ".codex")
}

func hasCodexLocal(homeDir string) bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	if os.Getenv("CODEX_HOME") != "" {
		return true
	}
	for _, path := range []string{
		codexHome(homeDir),
		"/Applications/ChatGPT.app",
		filepath.Join(homeDir, "Applications", "ChatGPT.app"),
		"/Applications/Codex.app",
		filepath.Join(homeDir, "Applications", "Codex.app"),
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// registerCodexMCP registers Handshake as an MCP server in ~/.codex/config.toml.
// Removes any stale [mcp_servers.handshake] block first, then appends fresh.
func registerCodexMCP(homeDir string) {
	if !hasCodexLocal(homeDir) {
		fmt.Println("- Codex: not installed, skipping")
		return
	}

	configPath := filepath.Join(codexHome(homeDir), "config.toml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		snippet := fmt.Sprintf("[mcp_servers.handshake]\nurl = %q\n", mcpURL())
		if err := writeFileAtomic(configPath, []byte(snippet), 0644); err != nil {
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

	lines := strings.Split(string(data), "\n")
	lines = removeCodexMCPBlock(lines)
	output := strings.Join(lines, "\n")
	snippet := fmt.Sprintf("\n[mcp_servers.handshake]\nurl = %q\n", mcpURL())

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Codex: could not back up config.toml: %v\n", err)
		return
	}
	if err := writeFileAtomic(configPath, []byte(output+snippet), 0644); err != nil {
		fmt.Printf("✗ Codex: could not write config.toml: %v\n", err)
		return
	}
	fmt.Println("✓ Codex: registered (config.toml, backup saved)")
}

// deregisterCodexMCP removes the Handshake MCP entry from ~/.codex/config.toml.
func deregisterCodexMCP(homeDir string) {
	configPath := filepath.Join(codexHome(homeDir), "config.toml")

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		fmt.Println("✓ Codex: no config found, nothing to do")
		return
	}
	if err != nil {
		fmt.Printf("✗ Codex: could not read config.toml: %v\n", err)
		return
	}

	lines := strings.Split(string(data), "\n")
	filtered := removeCodexMCPBlock(lines)
	if len(filtered) == len(lines) {
		fmt.Println("✓ Codex: handshake not in config, nothing to do")
		return
	}

	if err := backup(configPath); err != nil {
		fmt.Printf("✗ Codex: could not back up config.toml: %v\n", err)
		return
	}

	if err := writeFileAtomic(configPath, []byte(strings.Join(filtered, "\n")), 0644); err != nil {
		fmt.Printf("✗ Codex: could not write config.toml: %v\n", err)
		return
	}
	fmt.Println("✓ Codex: removed (backup saved)")
}

// --- Codex hooks (PreCompact + PostCompact + Stop) ---

// registerCodexHooks installs the three hook scripts and registers them
// in ~/.codex/hooks.json.
func registerCodexHooks(homeDir string) {
	if !hasCodexLocal(homeDir) {
		fmt.Println("- Codex hooks: Codex desktop or CLI not found, skipping")
		return
	}

	python := resolvePython()
	if python == "" {
		fmt.Println("- Codex hooks: Python 3 not found on PATH — skipping auto-checkpoint hooks")
		fmt.Println("    Install Python 3 to enable automatic syncing, or checkpoint manually with \"checkpoint this session\".")
		return
	}

	hooksDir := filepath.Join(codexHome(homeDir), "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not create hooks directory: %v\n", err)
		return
	}

	preCompactPath := filepath.Join(hooksDir, "handshake_pre_compact.py")
	postCompactPath := filepath.Join(hooksDir, "handshake_post_compact.py")
	stopPath := filepath.Join(hooksDir, "handshake_stop.py")

	if err := os.WriteFile(preCompactPath, injectBaseURL(codexplugin.PreCompactHook, baseURL()), 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write pre_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(postCompactPath, injectBaseURL(codexplugin.PostCompactHook, baseURL()), 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write post_compact hook: %v\n", err)
		return
	}
	if err := os.WriteFile(stopPath, injectBaseURL(codexplugin.StopHook, baseURL()), 0755); err != nil {
		fmt.Printf("✗ Codex hooks: could not write stop hook: %v\n", err)
		return
	}

	hooksConfigPath := filepath.Join(codexHome(homeDir), "hooks.json")
	registerCodexHooksConfig(hooksConfigPath, python, preCompactPath, postCompactPath, stopPath)
}

func registerCodexHooksConfig(hooksConfigPath, python, preCompactPath, postCompactPath, stopPath string) {
	var config map[string]any
	data, err := os.ReadFile(hooksConfigPath)
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Println("- Codex hooks: hooks.json is not valid JSON; add manually")
			return
		}
	} else if os.IsNotExist(err) {
		config = map[string]any{}
	} else {
		fmt.Printf("✗ Codex hooks: could not read hooks.json: %v\n", err)
		return
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
			"command":       fmt.Sprintf("%s %s", python, preCompactPath),
			"statusMessage": "Handshake: checkpointing session",
		}},
	})

	// PostCompact
	postCompacts, _ := hooks["PostCompact"].([]any)
	hooks["PostCompact"] = append(postCompacts, map[string]any{
		"matcher": "auto|manual",
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("%s %s", python, postCompactPath),
			"statusMessage": "Handshake: capturing compaction summary",
		}},
	})

	// Stop — no matcher (Stop doesn't support matchers)
	stops, _ := hooks["Stop"].([]any)
	hooks["Stop"] = append(stops, map[string]any{
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       fmt.Sprintf("%s %s", python, stopPath),
			"statusMessage": "Handshake: syncing session",
			"timeout":       30,
		}},
	})

	config["hooks"] = hooks

	if _, err := os.Stat(hooksConfigPath); err == nil {
		if err := backup(hooksConfigPath); err != nil {
			fmt.Printf("✗ Codex hooks: could not back up hooks.json, not modifying it: %v\n", err)
			return
		}
	} else if !os.IsNotExist(err) {
		fmt.Printf("✗ Codex hooks: could not inspect hooks.json: %v\n", err)
		return
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
	hooksConfigPath := filepath.Join(codexHome(homeDir), "hooks.json")
	removed, err := removeHandshakeHooksFromConfig(hooksConfigPath)
	if err != nil {
		fmt.Printf("✗ Codex hooks: could not update hooks.json: %v\n", err)
		return
	}

	hooksDir := filepath.Join(codexHome(homeDir), "hooks")
	os.Remove(filepath.Join(hooksDir, "handshake_pre_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_post_compact.py"))
	os.Remove(filepath.Join(hooksDir, "handshake_stop.py"))

	if removed {
		fmt.Println("✓ Codex hooks: removed")
	} else {
		fmt.Println("✓ Codex hooks: nothing to remove")
	}
	deregisterCodexMCP(homeDir)
}
