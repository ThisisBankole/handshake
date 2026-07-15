package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic_ReplacesContentsAndPreservesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := writeFileAtomic(path, []byte("new"), 0644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("contents = %q, want %q", data, "new")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("permissions = %o, want 600", got)
	}

	tempFiles, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings.json.tmp-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(tempFiles) != 0 {
		t.Errorf("temporary files remain: %v", tempFiles)
	}
}

func TestBackup_CopiesOriginalContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers]\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := backup(path); err != nil {
		t.Fatalf("backup: %v", err)
	}

	data, err := os.ReadFile(path + ".handshake.bak")
	if err != nil {
		t.Fatalf("ReadFile backup: %v", err)
	}
	if string(data) != "[mcp_servers]\n" {
		t.Errorf("backup contents = %q", data)
	}

	info, err := os.Stat(path + ".handshake.bak")
	if err != nil {
		t.Fatalf("Stat backup: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("backup permissions = %o, want 600", got)
	}
}

func TestRegisterClaudeCodeMCPConfig(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(homeDir, ".claude.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"other":{"type":"http","url":"http://example.test/mcp"}}}`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	registerClaudeCodeMCPConfig(homeDir)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	mcpServers := config["mcpServers"].(map[string]any)
	if _, ok := mcpServers["other"]; !ok {
		t.Fatal("existing MCP server was removed")
	}
	handshake := mcpServers["handshake"].(map[string]any)
	if got := handshake["url"]; got != mcpURL() {
		t.Errorf("Handshake MCP URL = %q, want %q", got, mcpURL())
	}
}

func TestCodexHomeOverrideAndMCPRegistration(t *testing.T) {
	homeDir := t.TempDir()
	configuredHome := filepath.Join(homeDir, "custom-codex")
	t.Setenv("CODEX_HOME", configuredHome)

	if got := codexHome(homeDir); got != configuredHome {
		t.Fatalf("codexHome = %q, want %q", got, configuredHome)
	}
	if !hasCodexLocal(homeDir) {
		t.Fatal("CODEX_HOME should make Codex a configured local target")
	}

	registerCodexMCP(homeDir)
	data, err := os.ReadFile(filepath.Join(configuredHome, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "[mcp_servers.handshake]") {
		t.Fatalf("Handshake MCP entry was not written: %q", data)
	}
}
