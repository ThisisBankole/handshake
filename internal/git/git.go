// Package git captures the state of a git repository at a point in time.
// Used by Handshake to detect drift between when a session was checkpointed
// and when it is restored in another agent.
package git

import (
	"os/exec"
	"strings"
	"time"
)

// State holds a snapshot of a git repository at checkpoint time.
type State struct {
	Commit     string `json:"commit"`      // full commit hash
	Branch     string `json:"branch"`      // current branch name
	Message    string `json:"message"`     // last commit message
	Status     string `json:"status"`      // git status --short output
	CapturedAt int64  `json:"captured_at"` // unix timestamp
}

// CaptureState runs git commands in workingDir and returns the current
// repository state. Returns nil, nil if workingDir is not a git repo —
// not an error, just no state to capture.
func CaptureState(workingDir string) (*State, error) {
	// Verify this is actually a git repo before running anything else.
	// git rev-parse --git-dir exits non-zero if not in a repo.
	if err := run(workingDir, "rev-parse", "--git-dir"); err != nil {
		return nil, nil // not a git repo — silently skip
	}

	state := &State{
		CapturedAt: time.Now().Unix(),
	}

	// Full commit hash
	if out, err := output(workingDir, "rev-parse", "HEAD"); err == nil {
		state.Commit = out
	}

	// Branch name — detached HEAD returns the commit hash again, handle gracefully
	if out, err := output(workingDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		state.Branch = out
	}

	// Last commit message
	if out, err := output(workingDir, "log", "-1", "--format=%s"); err == nil {
		state.Message = out
	}

	// Working tree status — lists modified, added, deleted files
	// Empty string means clean working tree
	if out, err := output(workingDir, "status", "--short"); err == nil {
		state.Status = out
	}

	return state, nil
}

// CommitsBehind returns how many commits currentCommit is behind headCommit.
// Used at restore time to show drift since checkpoint.
// Returns -1 if the comparison cannot be made (different branches, no shared history).
func CommitsBehind(workingDir, checkpointCommit, currentCommit string) int {
	if checkpointCommit == "" || currentCommit == "" {
		return -1
	}
	if checkpointCommit == currentCommit {
		return 0
	}

	// git rev-list <checkpoint>..<current> counts commits added since checkpoint
	out, err := output(workingDir, "rev-list", "--count", checkpointCommit+".."+currentCommit)
	if err != nil {
		return -1
	}

	count := 0
	for _, c := range out {
		if c >= '0' && c <= '9' {
			count = count*10 + int(c-'0')
		}
	}
	return count
}

// ChangedFiles returns files that differ between the checkpoint status
// and the current working tree status. Simple line-by-line diff of
// git status --short output.
func ChangedFiles(checkpointStatus, currentStatus string) []string {
	checkpointFiles := parseStatusLines(checkpointStatus)
	currentFiles := parseStatusLines(currentStatus)

	var changed []string
	for path, flag := range currentFiles {
		if checkpointFiles[path] != flag {
			changed = append(changed, flag+" "+path)
		}
	}
	// Files that existed at checkpoint but are now gone
	for path, flag := range checkpointFiles {
		if _, stillExists := currentFiles[path]; !stillExists {
			changed = append(changed, "D "+path+" (removed since checkpoint)")
			_ = flag
		}
	}
	return changed
}

// parseStatusLines turns git status --short output into a map of path → flag.
// Each line looks like "M  src/auth/middleware.go" or "?? newfile.go"
func parseStatusLines(status string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 4 {
			continue
		}
		flag := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		if path != "" && flag != "" {
			result[path] = flag
		}
	}
	return result
}

// run executes a git command in workingDir and returns only the error.
func run(workingDir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", workingDir}, args...)...)
	return cmd.Run()
}

// output executes a git command in workingDir and returns trimmed stdout.
func output(workingDir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", workingDir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}