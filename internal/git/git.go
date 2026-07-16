// Package git captures the state of a git repository at a point in time.
// Used by Handshake to detect drift between when a session was checkpointed
// and when it is restored in another agent.
package git

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// State holds a snapshot of a git repository at checkpoint time.
type State struct {
	Commit     string   `json:"commit"`            // full commit hash
	Branch     string   `json:"branch"`            // current branch name
	Message    string   `json:"message"`           // last commit message
	Status     string   `json:"status"`            // git status --short output
	Remote     string   `json:"remote,omitempty"`  // remote.origin.url
	Commits    []Commit `json:"commits,omitempty"` // commits made during the session
	CapturedAt int64    `json:"captured_at"`       // unix timestamp
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

	// Working tree status — lists modified, added, deleted files.
	// Use rawOutput to preserve per-line leading spaces (XY columns in the format).
	// Empty string means clean working tree.
	if out, err := rawOutput(workingDir, "status", "--short"); err == nil {
		state.Status = out
	}

	// Remote URL, so the session record points at the repo even when the
	// working directory no longer exists on the machine viewing it.
	if out, err := output(workingDir, "config", "--get", "remote.origin.url"); err == nil {
		state.Remote = out
	}

	return state, nil
}

// RepositoryRoot returns the canonical root of the repository containing
// workingDir. A non-Git directory is a normal absence, not an error.
func RepositoryRoot(workingDir string) (string, bool, error) {
	root, err := output(workingDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false, nil
	}
	return root, true, nil
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

// DriftReport summarises the difference between a checkpointed git state and
// the current state of the repository.
type DriftReport struct {
	CheckpointCommit string
	CheckpointBranch string
	CurrentCommit    string
	CurrentBranch    string
	CommitsAhead     int      // commits added since checkpoint; -1 if unknown
	ChangedFiles     []string // "X path" lines from git status diff
}

// CompareStates computes drift between a checkpointed state and the current state.
func CompareStates(workingDir string, checkpoint, current *State) DriftReport {
	return DriftReport{
		CheckpointCommit: shortHash(checkpoint.Commit),
		CheckpointBranch: checkpoint.Branch,
		CurrentCommit:    shortHash(current.Commit),
		CurrentBranch:    current.Branch,
		CommitsAhead:     CommitsBehind(workingDir, checkpoint.Commit, current.Commit),
		ChangedFiles:     ChangedFiles(checkpoint.Status, current.Status),
	}
}

// BuildRestorePacket formats a human-readable drift packet for display before
// a session is restored. Returns an empty string when checkpoint is nil or
// the working directory has no git repo.
func BuildRestorePacket(title, agent string, updatedAt int64, workingDir string, checkpoint *State, summary, decisions string) string {
	if checkpoint == nil {
		return ""
	}
	current, err := CaptureState(workingDir)
	if err != nil || current == nil {
		return ""
	}

	drift := CompareStates(workingDir, checkpoint, current)

	var b strings.Builder
	sep := strings.Repeat("─", 36)

	fmt.Fprintf(&b, "Restoring: %s\n", title)
	b.WriteString(sep + "\n")
	fmt.Fprintf(&b, "Source:       %s\n", agent)
	fmt.Fprintf(&b, "Checkpointed: %s\n", relativeAge(updatedAt))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Git at checkpoint:  %s (%s)\n", drift.CheckpointCommit, drift.CheckpointBranch)

	aheadNote := ""
	switch drift.CommitsAhead {
	case 0:
		// no note
	case -1:
		aheadNote = " (commit delta unknown)"
	default:
		s := "s"
		if drift.CommitsAhead == 1 {
			s = ""
		}
		aheadNote = fmt.Sprintf(" — %d commit%s ahead", drift.CommitsAhead, s)
	}
	fmt.Fprintf(&b, "Git now:            %s (%s)%s\n", drift.CurrentCommit, drift.CurrentBranch, aheadNote)

	if len(drift.ChangedFiles) > 0 {
		b.WriteString("\nFiles changed since checkpoint:\n")
		for _, f := range drift.ChangedFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}

	if decisions != "" {
		b.WriteString("\nDecisions carried forward:\n")
		for _, line := range strings.Split(strings.TrimSpace(decisions), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
		}
	}

	if summary != "" {
		b.WriteString("\nCheckpoint summary:\n")
		for _, line := range strings.Split(strings.TrimSpace(summary), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	b.WriteString(sep)
	return b.String()
}

func relativeAge(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func shortHash(h string) string {
	if len(h) >= 7 {
		return h[:7]
	}
	return h
}

// parseStatusLines turns git status --short output into a map of path → flag.
// git status --short format: XY<space>filename (X=staged, Y=unstaged, always 2 status
// chars then a space). TrimSpace must NOT be applied before slicing or it shifts columns.
func parseStatusLines(status string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r\n")
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

// Commit is a single commit that landed during a session's time window.
type Commit struct {
	Hash    string `json:"hash"` // abbreviated hash
	Subject string `json:"subject"`
	Author  string `json:"author,omitempty"` // "Name <email>"
	Body    string `json:"body,omitempty"`   // message body below the subject
	When    int64  `json:"when"`             // author time, unix
}

// CommitsBetween returns commits reachable from HEAD whose author time falls
// within [from, to]. Returns nil if workingDir is missing or not a git repo —
// like CaptureState, absence of history is not an error. Results are oldest
// first so they interleave naturally with a chronological timeline.
func CommitsBetween(workingDir string, from, to int64) []Commit {
	if workingDir == "" {
		return nil
	}
	if err := run(workingDir, "rev-parse", "--git-dir"); err != nil {
		return nil
	}

	// --since/--until filter server-side; timestamps are still checked in Go
	// because git's date parsing is fuzzy at the boundaries. Records are
	// separated by \x1e and fields by \x1f so multi-line bodies parse cleanly.
	out, err := rawOutput(workingDir, "log",
		"--since="+time.Unix(from, 0).Format(time.RFC3339),
		"--until="+time.Unix(to, 0).Format(time.RFC3339),
		"--format=%x1e%h%x1f%at%x1f%an <%ae>%x1f%s%x1f%b")
	if err != nil || out == "" {
		return nil
	}

	var commits []Commit
	for _, record := range strings.Split(out, "\x1e") {
		parts := strings.SplitN(record, "\x1f", 5)
		if len(parts) != 5 {
			continue
		}
		var at int64
		fmt.Sscanf(parts[1], "%d", &at)
		if at < from || at > to {
			continue
		}
		// git log is newest first; prepend to end up oldest first.
		commits = append([]Commit{{
			Hash:    strings.TrimSpace(parts[0]),
			When:    at,
			Author:  parts[2],
			Subject: parts[3],
			Body:    strings.TrimSpace(parts[4]),
		}}, commits...)
	}
	return commits
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

// rawOutput is like output but preserves leading whitespace on each line,
// only stripping the trailing newline. Used for multi-line outputs like
// git status --short where per-line column positions are meaningful.
func rawOutput(workingDir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", workingDir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
