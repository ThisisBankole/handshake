package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupAgentRegistrationsIncludeCodex(t *testing.T) {
	for _, agent := range setupAgentRegistrations {
		if agent.name == "Codex" {
			return
		}
	}
	t.Fatal("setup agent registrations do not include Codex")
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on IPv4 loopback: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestHandshakeAt_ReturnsTrueWhenHealthOK(t *testing.T) {
	srv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	addr := srv.Listener.Addr().String()
	if !handshakeAt(addr) {
		t.Fatalf("handshakeAt(%q) = false, want true", addr)
	}
}

func TestHandshakeAt_ReturnsFalseWhenHealthNotOK(t *testing.T) {
	srv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	addr := srv.Listener.Addr().String()
	if handshakeAt(addr) {
		t.Fatalf("handshakeAt(%q) = true, want false", addr)
	}
}

func TestHandshakeAt_ReturnsFalseWhenNoServer(t *testing.T) {
	// An address nothing is listening on — expect false.
	if handshakeAt("localhost:19999") {
		t.Fatal("handshakeAt(localhost:19999) = true, want false")
	}
}
