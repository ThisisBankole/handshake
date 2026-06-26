package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFakePython creates an executable named name in dir that exits with the
// given code when run. Used to simulate python3/python interpreters on PATH.
func writeFakePython(t *testing.T, dir, name string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, name)
	// #!/bin/sh — the body ignores args and exits with the chosen code, so
	// isPython3's version probe sees exit 0 (Python 3) or exit 1 (Python 2).
	script := "#!/bin/sh\nexit 0\n"
	if exitCode != 0 {
		script = "#!/bin/sh\nexit 1\n"
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolvePython_PrefersPython3(t *testing.T) {
	dir := t.TempDir()
	writeFakePython(t, dir, "python3", 0)
	writeFakePython(t, dir, "python", 0)
	t.Setenv("PATH", dir)

	got := resolvePython()
	want := filepath.Join(dir, "python3")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePython_FallsBackToPythonWhenPython3Absent(t *testing.T) {
	dir := t.TempDir()
	writeFakePython(t, dir, "python", 0) // exits 0 → treated as Python 3
	t.Setenv("PATH", dir)

	got := resolvePython()
	want := filepath.Join(dir, "python")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePython_RejectsPython2Fallback(t *testing.T) {
	dir := t.TempDir()
	writeFakePython(t, dir, "python", 1) // exits 1 → treated as Python 2
	t.Setenv("PATH", dir)

	if got := resolvePython(); got != "" {
		t.Fatalf("got %q, want empty (python2 should be rejected)", got)
	}
}

func TestResolvePython_NoneFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if got := resolvePython(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
