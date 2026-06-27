package main

import (
	"strings"
	"testing"
)

func splitTOML(s string) []string { return strings.Split(s, "\n") }

func TestRemoveCodexMCPBlock_RemovesBlock(t *testing.T) {
	in := "key = val\n[mcp_servers.handshake]\nurl = \"http://x\"\n[mcp_servers.other]\nurl = \"http://y\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	result := strings.Join(out, "\n")
	if strings.Contains(result, "[mcp_servers.handshake]") {
		t.Fatalf("handshake block still present:\n%s", result)
	}
	if !strings.Contains(result, "[mcp_servers.other]") {
		t.Fatalf("other section missing:\n%s", result)
	}
	if !strings.Contains(result, "key = val") {
		t.Fatalf("non-MCP key missing:\n%s", result)
	}
}

func TestRemoveCodexMCPBlock_NoopWhenNotPresent(t *testing.T) {
	in := "key = val\n[mcp_servers.other]\nurl = \"http://y\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	if len(out) != len(splitTOML(in)) {
		t.Fatalf("lines changed when not present: %d -> %d", len(splitTOML(in)), len(out))
	}
}

func TestRemoveCodexMCPBlock_EmptyLines(t *testing.T) {
	in := "[mcp_servers.handshake]\nurl = \"http://x\"\n\n[mcp_servers.other]\nurl = \"http://y\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	result := strings.Join(out, "\n")
	if strings.Contains(result, "[mcp_servers.handshake]") {
		t.Fatalf("handshake block still present:\n%s", result)
	}
}

func TestRemoveCodexMCPBlock_HandshakeAtEnd(t *testing.T) {
	in := "key = val\n[mcp_servers.handshake]\nurl = \"http://x\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	result := strings.Join(out, "\n")
	if strings.Contains(result, "handshake") {
		t.Fatalf("handshake still present:\n%s", result)
	}
	if !strings.Contains(result, "key = val") {
		t.Fatalf("non-MCP key missing:\n%s", result)
	}
}

func TestRemoveCodexMCPBlock_MultiLineValues(t *testing.T) {
	in := "[mcp_servers.handshake]\nurl = \"http://x\"\ntimeout = 30\n[mcp_servers.other]\nurl = \"http://y\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	result := strings.Join(out, "\n")
	if strings.Contains(result, "handshake") {
		t.Fatalf("handshake block still present:\n%s", result)
	}
}

func TestRemoveCodexMCPBlock_OnlyHandshakeBlock(t *testing.T) {
	in := "[mcp_servers.handshake]\nurl = \"http://x\"\n"
	out := removeCodexMCPBlock(splitTOML(in))
	if len(out) != 0 {
		t.Fatalf("expected empty result, got %d lines:\n%s", len(out), strings.Join(out, "\n"))
	}
}
