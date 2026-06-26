package main

import (
	"strings"
	"testing"
)

func TestResolvedAddr_DefaultsToListenAddr(t *testing.T) {
	t.Setenv("HANDSHAKE_ADDR", "")

	if got := resolvedAddr(); got != listenAddr {
		t.Fatalf("got %q, want %q", got, listenAddr)
	}
}

func TestResolvedAddr_UsesEnv(t *testing.T) {
	t.Setenv("HANDSHAKE_ADDR", "localhost:8766")

	if got := resolvedAddr(); got != "localhost:8766" {
		t.Fatalf("got %q, want localhost:8766", got)
	}
}

func TestLaunchdPlist_ContainsEnvBlock(t *testing.T) {
	got := launchdPlist("com.handshake.serve", "/usr/local/bin/handshake",
		"/tmp/handshake.log", "localhost:8766", "http://localhost:8766/mcp")

	if !strings.Contains(got, "<key>EnvironmentVariables</key>") {
		t.Fatal("plist missing EnvironmentVariables key")
	}
	if !strings.Contains(got, "<key>HANDSHAKE_ADDR</key>") {
		t.Fatal("plist missing HANDSHAKE_ADDR key")
	}
	if !strings.Contains(got, "<string>localhost:8766</string>") {
		t.Fatalf("plist missing resolved HANDSHAKE_ADDR value: %s", got)
	}
	if !strings.Contains(got, "<key>HANDSHAKE_URL</key>") {
		t.Fatal("plist missing HANDSHAKE_URL key")
	}
	if !strings.Contains(got, "<string>http://localhost:8766/mcp</string>") {
		t.Fatalf("plist missing resolved HANDSHAKE_URL value: %s", got)
	}
	// The default port must not leak in when a custom port is used.
	if strings.Contains(got, "8765") {
		t.Fatalf("default port leaked into plist: %s", got)
	}
}

func TestLaunchdPlist_DefaultPortWhenNoOverride(t *testing.T) {
	got := launchdPlist("com.handshake.serve", "/usr/local/bin/handshake",
		"/tmp/handshake.log", "localhost:8765", "http://localhost:8765/mcp")

	if !strings.Contains(got, "<string>localhost:8765</string>") {
		t.Fatalf("default plist missing default addr: %s", got)
	}
}

func TestSystemdUnit_ContainsEnvLines(t *testing.T) {
	got := systemdUnit("/usr/local/bin/handshake", "/tmp/handshake.log",
		"localhost:8766", "http://localhost:8766/mcp")

	if !strings.Contains(got, "Environment=HANDSHAKE_ADDR=localhost:8766") {
		t.Fatalf("unit missing HANDSHAKE_ADDR env line: %s", got)
	}
	if !strings.Contains(got, "Environment=HANDSHAKE_URL=http://localhost:8766/mcp") {
		t.Fatalf("unit missing HANDSHAKE_URL env line: %s", got)
	}
	if strings.Contains(got, "8765") {
		t.Fatalf("default port leaked into unit: %s", got)
	}
}
