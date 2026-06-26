package main

import (
	"bytes"
	"testing"
)

func TestInjectBaseURL_ReplacesHardcodedPort(t *testing.T) {
	script := []byte(`BASE_URL = "http://localhost:8765"` + "\n" +
		`req = urllib.request.Request(f"{BASE_URL}/mcp")`)

	got := injectBaseURL(script, "http://localhost:8766")

	if bytes.Contains(got, []byte("http://localhost:8765")) {
		t.Fatalf("hardcoded URL still present after injection: %s", got)
	}
	if !bytes.Contains(got, []byte(`"http://localhost:8766"`)) {
		t.Fatalf("resolved URL not injected: %s", got)
	}
}

func TestInjectBaseURL_ReplacesAllOccurrences(t *testing.T) {
	script := []byte("http://localhost:8765/a\nhttp://localhost:8765/b")

	got := injectBaseURL(script, "http://localhost:8766")

	if want := []byte("http://localhost:8766/a\nhttp://localhost:8766/b"); !bytes.Equal(got, want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestInjectBaseURL_NoMatchLeavesScriptUntouched(t *testing.T) {
	script := []byte(`BASE_URL = "http://example.com:9000"`)

	got := injectBaseURL(script, "http://localhost:8766")

	if !bytes.Equal(got, script) {
		t.Fatalf("script unexpectedly modified: %s", got)
	}
}

func TestBaseURL_DefaultsToLocalhost8765(t *testing.T) {
	t.Setenv("HANDSHAKE_URL", "")
	t.Setenv("HANDSHAKE_ADDR", "")

	if got := baseURL(); got != "http://localhost:8765" {
		t.Fatalf("got %q, want http://localhost:8765", got)
	}
}

func TestBaseURL_StripsMCPPathFromEnv(t *testing.T) {
	t.Setenv("HANDSHAKE_URL", "http://localhost:8766/mcp")

	if got := baseURL(); got != "http://localhost:8766" {
		t.Fatalf("got %q, want http://localhost:8766", got)
	}
}

func TestBaseURL_KeepsEnvURLWithoutMCPPath(t *testing.T) {
	t.Setenv("HANDSHAKE_URL", "http://localhost:8766")

	if got := baseURL(); got != "http://localhost:8766" {
		t.Fatalf("got %q, want http://localhost:8766", got)
	}
}
