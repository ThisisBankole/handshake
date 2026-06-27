package main

import (
	"reflect"
	"strings"
	"testing"
)

func splitYAML(s string) []string { return strings.Split(s, "\n") }

func TestHermesInjectMCP_InsertsUnderExistingMcpServers(t *testing.T) {
	in := "model: gpt\nmcp_servers:\n  other:\n    url: http://x\n"
	updated, already, found := hermesInjectMCP(splitYAML(in), "http://localhost:8766/mcp")
	if !found || already {
		t.Fatalf("found=%v already=%v, want found=true already=false", found, already)
	}
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "  handshake:\n    url: http://localhost:8766/mcp") {
		t.Fatalf("handshake entry not inserted:\n%s", out)
	}
	if !strings.Contains(out, "  other:\n    url: http://x") {
		t.Fatalf("existing entry corrupted:\n%s", out)
	}
}

func TestHermesInjectMCP_AlreadyRegistered(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://old\n"
	_, already, found := hermesInjectMCP(splitYAML(in), "http://localhost:8766/mcp")
	if !found || !already {
		t.Fatalf("found=%v already=%v, want both true", found, already)
	}
}

func TestHermesInjectMCP_DetectsFourSpaceIndent(t *testing.T) {
	in := "mcp_servers:\n    other:\n        url: http://x\n"
	updated, _, _ := hermesInjectMCP(splitYAML(in), "http://localhost:8766/mcp")
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "\n    handshake:\n        url: http://localhost:8766/mcp") {
		t.Fatalf("handshake not inserted with 4-space indent:\n%s", out)
	}
}

func TestHermesInjectMCP_EmptyMcpServers(t *testing.T) {
	in := "mcp_servers:\n"
	updated, already, found := hermesInjectMCP(splitYAML(in), "http://localhost:8766/mcp")
	if !found || already {
		t.Fatalf("found=%v already=%v", found, already)
	}
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "mcp_servers:\n  handshake:\n    url: http://localhost:8766/mcp") {
		t.Fatalf("handshake not inserted under empty mcp_servers:\n%s", out)
	}
}

func TestHermesInjectMCP_NoMcpServers(t *testing.T) {
	in := "model: gpt\n"
	_, _, found := hermesInjectMCP(splitYAML(in), "http://localhost:8766/mcp")
	if found {
		t.Fatal("found=true, want false (no mcp_servers key)")
	}
}

func TestHermesRemoveMCP_RemovesHandshakeAndNestedChildren(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://x\n    headers:\n      auth: token\n  other:\n    url: http://y\n"
	updated, removed := hermesRemoveMCP(splitYAML(in))
	if !removed {
		t.Fatal("removed=false, want true")
	}
	out := strings.Join(updated, "\n")
	if strings.Contains(out, "handshake") {
		t.Fatalf("handshake still present:\n%s", out)
	}
	if !strings.Contains(out, "  other:\n    url: http://y") {
		t.Fatalf("sibling 'other' corrupted:\n%s", out)
	}
	if strings.Contains(out, "auth: token") {
		t.Fatalf("nested child not removed:\n%s", out)
	}
}

func TestHermesRemoveMCP_RemovesEmptyMcpServersKey(t *testing.T) {
	in := "model: gpt\nmcp_servers:\n  handshake:\n    url: http://x\nother: val\n"
	updated, _ := hermesRemoveMCP(splitYAML(in))
	out := strings.Join(updated, "\n")
	if strings.Contains(out, "mcp_servers:") {
		t.Fatalf("empty mcp_servers key should be removed:\n%s", out)
	}
	if !strings.Contains(out, "model: gpt") || !strings.Contains(out, "other: val") {
		t.Fatalf("unrelated keys corrupted:\n%s", out)
	}
}

func TestHermesRemoveMCP_KeepsMcpServersWithOtherChildren(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://x\n  other:\n    url: http://y\n"
	updated, _ := hermesRemoveMCP(splitYAML(in))
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "mcp_servers:") {
		t.Fatalf("mcp_servers should be kept (has other children):\n%s", out)
	}
	if strings.Contains(out, "handshake") {
		t.Fatalf("handshake should be removed:\n%s", out)
	}
}

func TestHermesRemoveMCP_NotPresent(t *testing.T) {
	in := "mcp_servers:\n  other:\n    url: http://x\n"
	updated, removed := hermesRemoveMCP(splitYAML(in))
	if removed {
		t.Fatal("removed=true, want false")
	}
	if !reflect.DeepEqual(updated, splitYAML(in)) {
		t.Fatalf("lines changed when handshake not present:\n%s", strings.Join(updated, "\n"))
	}
}

func TestHermesRemoveMCP_NoMcpServers(t *testing.T) {
	in := "model: gpt\n"
	_, removed := hermesRemoveMCP(splitYAML(in))
	if removed {
		t.Fatal("removed=true, want false")
	}
}

// Round-trip: inject then remove should restore the original mcp_servers block
// (for a config that had other children).
func TestHermesRemoveHandshake_RemovesHandshakeKeepsMcpServersWhenEmpty(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://x\n"
	updated, removed := hermesRemoveHandshake(splitYAML(in))
	if !removed {
		t.Fatal("removed=false, want true")
	}
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "mcp_servers:") {
		t.Fatalf("mcp_servers: should be kept even when empty:\n%s", out)
	}
	if strings.Contains(out, "handshake") {
		t.Fatalf("handshake should be removed:\n%s", out)
	}
}

func TestHermesRemoveHandshake_KeepsSiblings(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://x\n  other:\n    url: http://y\n"
	updated, removed := hermesRemoveHandshake(splitYAML(in))
	if !removed {
		t.Fatal("removed=false, want true")
	}
	out := strings.Join(updated, "\n")
	if strings.Contains(out, "handshake") {
		t.Fatalf("handshake still present:\n%s", out)
	}
	if !strings.Contains(out, "other:\n    url: http://y") {
		t.Fatalf("sibling corrupted:\n%s", out)
	}
}

func TestHermesRemoveHandshake_NotPresent(t *testing.T) {
	in := "mcp_servers:\n  other:\n    url: http://x\n"
	updated, removed := hermesRemoveHandshake(splitYAML(in))
	if removed {
		t.Fatal("removed=true, want false")
	}
	if strings.Join(updated, "\n") != strings.Join(splitYAML(in), "\n") {
		t.Fatalf("lines changed when not present:\n%s", strings.Join(updated, "\n"))
	}
}

func TestHermesRemoveHandshake_NoMcpServers(t *testing.T) {
	in := "model: gpt\n"
	_, removed := hermesRemoveHandshake(splitYAML(in))
	if removed {
		t.Fatal("removed=true, want false")
	}
}

func TestHermesRemoveThenInject_ReplacesStaleURL(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://old:8765/mcp\n  other:\n    url: http://other\n"
	lines := splitYAML(in)
	lines, _ = hermesRemoveHandshake(lines)
	updated, _, _ := hermesInjectMCP(lines, "http://new:9999/mcp")
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "url: http://new:9999/mcp") {
		t.Fatalf("new URL not injected:\n%s", out)
	}
	if strings.Contains(out, "http://old:8765/mcp") {
		t.Fatalf("old URL still present:\n%s", out)
	}
	if !strings.Contains(out, "other:\n    url: http://other") {
		t.Fatalf("sibling other corrupted:\n%s", out)
	}
}

func TestHermesRemoveThenInject_HandshakeOnlyChild(t *testing.T) {
	in := "mcp_servers:\n  handshake:\n    url: http://old/mcp\n"
	lines := splitYAML(in)
	lines, _ = hermesRemoveHandshake(lines)
	updated, _, _ := hermesInjectMCP(lines, "http://new/mcp")
	out := strings.Join(updated, "\n")
	if !strings.Contains(out, "mcp_servers:\n  handshake:\n    url: http://new/mcp") {
		t.Fatalf("stale URL not replaced:\n%s", out)
	}
}

func TestHermesInjectThenRemove_RoundTrip(t *testing.T) {
	original := "mcp_servers:\n  other:\n    url: http://x\n"
	injected, _, _ := hermesInjectMCP(splitYAML(original), "http://localhost:8766/mcp")
	restored, _ := hermesRemoveMCP(injected)
	out := strings.Join(restored, "\n")
	if !strings.Contains(out, "  other:\n    url: http://x") {
		t.Fatalf("round-trip lost other entry:\n%s", out)
	}
	if strings.Contains(out, "handshake") {
		t.Fatalf("round-trip left handshake:\n%s", out)
	}
}
