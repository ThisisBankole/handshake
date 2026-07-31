package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testTag = "v9.9.9"

// buildArchive returns a gzip-compressed tar containing the named files, and
// the SHA-256 of the whole archive — mirroring GoReleaser, whose checksums.txt
// records the digest of the archive, not the binary inside it.
func buildArchive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0755,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// releaseServer serves a release for testTag: the archive at its download path
// and a checksums.txt whose body the caller supplies, so tests can inject a
// correct, wrong, or incomplete checksum listing.
func releaseServer(t *testing.T, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			w.Write([]byte(checksums))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newTestUpdater wires an Updater at the test server with a throwaway target
// binary and a Restart that records invocation instead of exiting.
func newTestUpdater(t *testing.T, server *httptest.Server, restarted *bool) (*Updater, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "handshake")
	if err := os.WriteFile(target, []byte("OLD BINARY"), 0755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	updater := &Updater{
		Client:          &http.Client{Timeout: 5 * time.Second},
		DownloadBaseURL: server.URL,
		GOOS:            "testos",
		GOARCH:          "testarch",
		ExecutablePath:  func() (string, error) { return target, nil },
		Restart:         func() error { *restarted = true; return nil },
		Log:             func(string, ...any) {},
	}
	return updater, target
}

func checksumsLine(sum, name string) string {
	return fmt.Sprintf("%s  %s\n", sum, name)
}

func TestUpdate_VerifiesAndSwapsBinary(t *testing.T) {
	archive, sum := buildArchive(t, map[string]string{
		"handshake": "NEW BINARY",
		"README.md": "docs",
	})
	server := releaseServer(t, archive, checksumsLine(sum, "handshake_testos_testarch.tar.gz"))

	restarted := false
	updater, target := newTestUpdater(t, server, &restarted)

	swapped, err := updater.Update(context.Background(), testTag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !swapped || !restarted {
		t.Fatalf("swapped=%v restarted=%v, want both true", swapped, restarted)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "NEW BINARY" {
		t.Fatalf("target = %q, want NEW BINARY", got)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm()&0100 == 0 {
		t.Fatalf("target not executable: %v", info.Mode())
	}
}

func TestUpdate_RejectsChecksumMismatch(t *testing.T) {
	archive, _ := buildArchive(t, map[string]string{"handshake": "NEW BINARY"})
	wrong := strings.Repeat("a", 64)
	server := releaseServer(t, archive, checksumsLine(wrong, "handshake_testos_testarch.tar.gz"))

	restarted := false
	updater, target := newTestUpdater(t, server, &restarted)

	swapped, err := updater.Update(context.Background(), testTag)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if swapped || restarted {
		t.Fatalf("swapped=%v restarted=%v, want both false", swapped, restarted)
	}
	assertUntouched(t, target)
	assertNoTempLeftovers(t, target)
}

func TestUpdate_RejectsMissingChecksumEntry(t *testing.T) {
	archive, sum := buildArchive(t, map[string]string{"handshake": "NEW BINARY"})
	// Checksums list a different artifact; ours is absent.
	server := releaseServer(t, archive, checksumsLine(sum, "some_other_file.tar.gz"))

	restarted := false
	updater, target := newTestUpdater(t, server, &restarted)

	_, err := updater.Update(context.Background(), testTag)
	if err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("err = %v, want no-entry error", err)
	}
	if restarted {
		t.Fatal("restarted despite missing checksum entry")
	}
	assertUntouched(t, target)
}

func TestUpdate_RejectsMalformedChecksum(t *testing.T) {
	archive, _ := buildArchive(t, map[string]string{"handshake": "NEW BINARY"})
	server := releaseServer(t, archive, checksumsLine("not-a-real-hash", "handshake_testos_testarch.tar.gz"))

	restarted := false
	updater, target := newTestUpdater(t, server, &restarted)

	_, err := updater.Update(context.Background(), testTag)
	if err == nil || !strings.Contains(err.Error(), "malformed checksum") {
		t.Fatalf("err = %v, want malformed-checksum error", err)
	}
	assertUntouched(t, target)
}

func TestUpdate_RejectsArchiveWithoutBinary(t *testing.T) {
	// Checksum matches (so verification passes) but the archive lacks the
	// handshake binary — the swap must still be refused, leaving the original.
	archive, sum := buildArchive(t, map[string]string{"README.md": "docs only"})
	server := releaseServer(t, archive, checksumsLine(sum, "handshake_testos_testarch.tar.gz"))

	restarted := false
	updater, target := newTestUpdater(t, server, &restarted)

	_, err := updater.Update(context.Background(), testTag)
	if err == nil || !strings.Contains(err.Error(), "no handshake binary") {
		t.Fatalf("err = %v, want missing-binary error", err)
	}
	if restarted {
		t.Fatal("restarted despite missing binary")
	}
	assertUntouched(t, target)
	assertNoTempLeftovers(t, target)
}

func TestAutoUpdateDisabled(t *testing.T) {
	if AutoUpdateDisabled("0.23.0") {
		t.Fatal("release build should be eligible")
	}
	if !AutoUpdateDisabled("0.1.0-dev") {
		t.Fatal("dev build must be disabled")
	}
	t.Setenv("HANDSHAKE_NO_AUTO_UPDATE", "1")
	if !AutoUpdateDisabled("0.23.0") {
		t.Fatal("opt-out env must disable auto-update")
	}
}

func TestIsHomebrewPath(t *testing.T) {
	cases := map[string]bool{
		"/opt/homebrew/Cellar/handshake/0.23.0/bin/handshake": true,
		"/usr/local/Caskroom/handshake/0.23.0/handshake":      true,
		"/home/user/.local/bin/handshake":                     false,
		"/usr/local/bin/handshake":                            false,
	}
	for path, want := range cases {
		if got := isHomebrewPath(path); got != want {
			t.Errorf("isHomebrewPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func assertUntouched(t *testing.T, target string) {
	t.Helper()
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "OLD BINARY" {
		t.Fatalf("target was modified to %q; must stay OLD BINARY on failure", got)
	}
}

// assertNoTempLeftovers checks the staged update file was cleaned from the
// target directory so failed attempts do not accumulate.
func assertNoTempLeftovers(t *testing.T, target string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".handshake-update-") {
			t.Fatalf("leftover staged file: %s", entry.Name())
		}
	}
}
