package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCodexTranscript(t *testing.T, codexDir, uuid string) string {
	t.Helper()
	dir := filepath.Join(codexDir, "sessions", "2026", "04", "14")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "rollout-2026-04-14T19-41-24-"+uuid+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestIDFromCodexFilename(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"rollout-2026-04-14T19-41-24-019cfd76-1234-5678-9abc-def012345678.jsonl", "019cfd76-1234-5678-9abc-def012345678"},
		{"notjson.txt", ""},
		{"rollout-x.jsonl", ""},
		{"rollout-2026-04-14T19-41-24-zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz.jsonl", ""},
	}
	for _, tc := range tests {
		if got := idFromCodexFilename(tc.name); got != tc.want {
			t.Errorf("idFromCodexFilename(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCodexFindSessionFile_SingleLookup(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	uuid := "019cfd76-1234-5678-9abc-def012345678"
	want := writeCodexTranscript(t, codexDir, uuid)

	a := NewCodexAdapter(home)
	got, err := a.findSessionFile(uuid)
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCodexBuildPathIndex_BulkLookup(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	uuid1 := "019cfd76-1234-5678-9abc-def012345678"
	uuid2 := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	p1 := writeCodexTranscript(t, codexDir, uuid1)
	p2 := writeCodexTranscript(t, codexDir, uuid2)

	a := NewCodexAdapter(home)
	a.buildPathIndex()

	for _, tc := range []struct{ id, want string }{{uuid1, p1}, {uuid2, p2}} {
		got, err := a.findSessionFile(tc.id)
		if err != nil {
			t.Fatalf("findSessionFile(%s): %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("findSessionFile(%s) = %q, want %q", tc.id, got, tc.want)
		}
	}

	if _, err := a.findSessionFile("nonexistent-id"); err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
}

func TestCodexFindSessionFile_MissingSessionsDir(t *testing.T) {
	home := t.TempDir()
	a := NewCodexAdapter(home)
	if _, err := a.findSessionFile("019cfd76-1234-5678-9abc-def012345678"); err == nil {
		t.Fatal("expected error for missing sessions dir, got nil")
	}
}

// The path index is built once and not re-walked on later lookups: a file
// added after buildPathIndex must not be found, proving the cache is reused.
func TestCodexBuildPathIndex_CachedNotRewalked(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	uuid1 := "019cfd76-1234-5678-9abc-def012345678"
	writeCodexTranscript(t, codexDir, uuid1)

	a := NewCodexAdapter(home)
	a.buildPathIndex()

	uuid2 := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeCodexTranscript(t, codexDir, uuid2)

	if _, err := a.findSessionFile(uuid2); err == nil {
		t.Fatal("expected uuid2 to be absent from cached index, but it was found")
	}
	if _, err := a.findSessionFile(uuid1); err != nil {
		t.Fatalf("uuid1 should still be in cache: %v", err)
	}
}
