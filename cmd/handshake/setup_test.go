package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandshakeAt_ReturnsTrueWhenHealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	if !handshakeAt(addr) {
		t.Fatalf("handshakeAt(%q) = false, want true", addr)
	}
}

func TestHandshakeAt_ReturnsFalseWhenHealthNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

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
