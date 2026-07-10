package main

import (
	"os"
	"path/filepath"
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
