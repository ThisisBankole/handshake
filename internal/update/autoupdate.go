package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Auto-update replaces the running daemon's binary with a newer release and
// lets the service manager relaunch it. The trust chain is: the release check
// tells us the tag, we download that tag's archive and its checksums.txt over
// HTTPS from the official GitHub release, and we refuse to install anything
// whose SHA-256 does not match the recorded checksum. This defends against
// corrupted, truncated, or MITM-swapped downloads. It does NOT defend against a
// compromised GitHub account publishing a malicious release — that needs signed
// checksums (cosign/GPG), which the project does not yet produce.

const (
	downloadBaseURL   = "https://github.com/ThisisBankole/handshake/releases/download"
	maxChecksumsBytes = 1 << 20   // checksums.txt is a few hundred bytes
	maxArchiveBytes   = 100 << 20 // the real archive is ~15 MB
	maxBinaryBytes    = 300 << 20 // the extracted binary, decompressed
)

// hex64 matches a well-formed SHA-256 digest so a malformed checksums.txt can
// never be mistaken for a valid expected hash.
var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Updater performs one download-verify-swap-restart cycle. Its network base
// URL, platform strings, target path, and restart action are injectable so the
// whole flow can be driven against a test server and a throwaway binary file.
type Updater struct {
	Client          *http.Client
	DownloadBaseURL string
	GOOS            string
	GOARCH          string
	// ExecutablePath resolves the binary to replace (the path the service
	// manager launched, so relaunch picks up the new bytes).
	ExecutablePath func() (string, error)
	// Restart hands the machine to the new binary. The default exits the
	// process; launchd KeepAlive and systemd Restart=always then relaunch it
	// from the replaced path, which is race-free (unlike self-signalling the
	// service manager from the dying process).
	Restart func() error
	Log     func(format string, args ...any)
}

func NewUpdater() *Updater {
	return &Updater{
		Client:          &http.Client{Timeout: 5 * time.Minute}, // room for the archive download
		DownloadBaseURL: downloadBaseURL,
		GOOS:            runtimeGOOS(),
		GOARCH:          runtimeGOARCH(),
		ExecutablePath:  resolvedExecutable,
		Restart:         gracefulExit,
		Log:             func(string, ...any) {},
	}
}

func (u *Updater) archiveName() string {
	return fmt.Sprintf("handshake_%s_%s.tar.gz", u.GOOS, u.GOARCH)
}

// Update downloads the release for tag, verifies its checksum, atomically
// replaces the current binary, and restarts. It returns whether the binary was
// swapped. Any verification failure aborts before the swap, leaving the running
// binary untouched.
func (u *Updater) Update(ctx context.Context, tag string) (bool, error) {
	target, err := u.ExecutablePath()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}

	expected, err := u.expectedChecksum(ctx, tag)
	if err != nil {
		return false, err
	}

	archivePath, err := u.downloadVerifiedArchive(ctx, tag, expected)
	if err != nil {
		return false, err
	}
	defer os.Remove(archivePath)

	if err := u.extractAndSwap(archivePath, target); err != nil {
		return false, err
	}

	u.Log("auto-update: installed %s over %s", tag, target)
	if u.Restart != nil {
		if err := u.Restart(); err != nil {
			return true, fmt.Errorf("restart after update: %w", err)
		}
	}
	return true, nil
}

// expectedChecksum fetches checksums.txt and returns the recorded SHA-256 for
// this platform's archive. A missing entry is a hard error: without a checksum
// to verify against we must never install the binary.
func (u *Updater) expectedChecksum(ctx context.Context, tag string) (string, error) {
	url := fmt.Sprintf("%s/%s/checksums.txt", u.DownloadBaseURL, tag)
	body, err := u.get(ctx, url, maxChecksumsBytes)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	want := u.archiveName()
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], strings.TrimPrefix(fields[1], "*")
		if name != want {
			continue
		}
		if !hex64.MatchString(sum) {
			return "", fmt.Errorf("malformed checksum for %s", want)
		}
		return strings.ToLower(sum), nil
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", want)
}

// downloadVerifiedArchive streams the archive to a temp file while hashing it,
// then compares the digest to expected. It returns the temp path only when the
// checksum matches; otherwise it deletes the file and errors.
func (u *Updater) downloadVerifiedArchive(ctx context.Context, tag, expected string) (string, error) {
	url := fmt.Sprintf("%s/%s/%s", u.DownloadBaseURL, tag, u.archiveName())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "handshake-auto-updater")
	response, err := u.Client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download archive: %s", response.Status)
	}

	tmp, err := os.CreateTemp("", "handshake-archive-*.tar.gz")
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, maxArchiveBytes+1))
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("read archive: %w", err)
	}
	if written > maxArchiveBytes {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("archive exceeds %d bytes", maxArchiveBytes)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expected {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", got, expected)
	}
	return tmp.Name(), nil
}

// extractAndSwap pulls the handshake binary out of the verified archive and
// atomically renames it over target. The new file is written into target's
// directory first so the rename stays on one filesystem and is atomic; the
// running process keeps its old inode until the service manager relaunches.
func (u *Updater) extractAndSwap(archivePath, target string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()

	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	dir := filepath.Dir(target)
	staged, err := os.CreateTemp(dir, ".handshake-update-*")
	if err != nil {
		return fmt.Errorf("stage in %s: %w", dir, err)
	}
	stagedPath := staged.Name()

	found := false
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			staged.Close()
			os.Remove(stagedPath)
			return fmt.Errorf("read archive: %w", err)
		}
		// Match only the binary by base name; never trust the archive's path.
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "handshake" {
			continue
		}
		if _, err := io.Copy(staged, io.LimitReader(reader, maxBinaryBytes)); err != nil {
			staged.Close()
			os.Remove(stagedPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		found = true
		break
	}
	if !found {
		staged.Close()
		os.Remove(stagedPath)
		return fmt.Errorf("archive has no handshake binary")
	}
	if err := staged.Chmod(0755); err != nil {
		staged.Close()
		os.Remove(stagedPath)
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		os.Remove(stagedPath)
		return err
	}
	if err := staged.Close(); err != nil {
		os.Remove(stagedPath)
		return err
	}
	if err := os.Rename(stagedPath, target); err != nil {
		os.Remove(stagedPath)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "handshake-auto-updater")
	response, err := u.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}
