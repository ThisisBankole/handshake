package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"localhost:8765", "127.0.0.1:8765", "[::1]:8765"} {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("validateLoopbackAddr(%q): %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "192.0.2.1:8765", "bad-address"} {
		if err := validateLoopbackAddr(addr); err == nil {
			t.Errorf("validateLoopbackAddr(%q) accepted a non-loopback or invalid address", addr)
		}
	}
}

func TestProtectLocalHTTPRejectsCrossOriginRequests(t *testing.T) {
	handler := protectLocalHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	request = httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("{}"))
	request.Header.Set("Origin", "http://localhost:8765")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("loopback origin status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestProtectLocalHTTPLimitsRequestBodies(t *testing.T) {
	handler := protectLocalHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := make([]byte, maxRequestBytes+1)
		if _, err := r.Body.Read(buffer); err == nil {
			t.Fatal("oversized request body was not rejected")
		}
	}))
	request := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(strings.Repeat("x", maxRequestBytes+1)))
	handler.ServeHTTP(httptest.NewRecorder(), request)
}
