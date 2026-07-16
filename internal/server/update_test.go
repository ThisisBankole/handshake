package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"handshake/internal/db"
)

func TestUpdateStatusTool_ReturnsCachedAvailableRelease(t *testing.T) {
	homeDir := t.TempDir()
	database, err := db.New(filepath.Join(homeDir, ".handshake", "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()
	if err := database.SaveUpdateStatus(&db.UpdateStatus{
		LastCheckedAt: time.Now().Unix(), InstalledVersion: "v0.18.1",
		LatestVersion: "v0.19.0", ReleaseURL: "https://example.test/release",
	}); err != nil {
		t.Fatalf("SaveUpdateStatus: %v", err)
	}

	server := New("handshake", "v0.18.1", homeDir, database)
	result, err := server.handleUpdateStatus(mcp.CallToolRequest{})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("handleUpdateStatus = %+v, %v", result, err)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "Update available: yes") || !strings.Contains(text.Text, "Tell the user") {
		t.Fatalf("unexpected update status result: %+v", result.Content)
	}
}
