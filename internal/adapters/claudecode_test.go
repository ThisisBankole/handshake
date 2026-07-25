package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeCodeFindSessionFileRejectsTraversal(t *testing.T) {
	homeDir := t.TempDir()
	projectDir := filepath.Join(homeDir, ".claude", "projects", "project-one")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	adapter := NewClaudeCodeAdapter(homeDir)

	for _, sessionID := range []string{"../outside", "../../outside", "/tmp/outside", `..\outside`} {
		if _, err := adapter.findSessionFile("", sessionID); err == nil {
			t.Errorf("findSessionFile accepted unsafe session ID %q", sessionID)
		}
	}
	if _, err := adapter.findSessionFile("../project-two", "safe-session"); err == nil {
		t.Error("findSessionFile accepted unsafe project ID")
	}
}
