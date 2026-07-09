package timeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handshake/internal/db"
)

// base is a fixed session start time; events are offsets from it.
var base = time.Now().Add(-2 * time.Hour).Unix()

func newTestDB(t *testing.T, workingDir string) (*db.Database, *db.Session) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	session := &db.Session{
		ID: "s1", Title: "Fix registrations", Agent: "codex",
		WorkingDir: workingDir,
		CreatedAt:  base, UpdatedAt: base + 1200,
	}
	if err := database.StoreSession(session); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	msgs := []*db.Message{
		// Harness scaffolding masquerading as the user — must be dropped.
		{ID: "m0", Role: "user", Content: "# AGENTS.md instructions for /tmp\n<INSTRUCTIONS>stuff", CreatedAt: base},
		{ID: "m1", Role: "user", Content: "fix the stale registrations\nplease", CreatedAt: base + 60},
		{ID: "m2", Role: "assistant", Content: "I'll scan the codebase first. [tool: exec_command]", CreatedAt: base + 120},
		{ID: "m3", Role: "tool", Content: "[tool: exec_command]", CreatedAt: base + 130},
		{ID: "m4", Role: "tool", Content: "[tool: exec_command]", CreatedAt: base + 140},
		{ID: "m5", Role: "tool", Content: "[2 tool result(s)]", CreatedAt: base + 150},
		{ID: "m6", Role: "assistant", Content: "Found the bug in register.go.", CreatedAt: base + 300},
		{ID: "m7", Role: "user", Content: "great, also add tests", CreatedAt: base + 600},
		{ID: "m8", Role: "assistant", Content: "[tool: apply_patch][tool: exec_command]", CreatedAt: base + 700},
		{ID: "m9", Role: "assistant", Content: "Done — tests pass.", CreatedAt: base + 900},
	}
	for _, m := range msgs {
		m.SessionID = "s1"
		if err := database.StoreMessage(m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}
	return database, session
}

func kinds(events []Event) []Kind {
	out := make([]Kind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func TestBuildChaptersAndRuns(t *testing.T) {
	database, session := newTestDB(t, "")

	chapters, err := Build(database, session)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Scaffolding dropped, empty opening chapter pruned → 2 chapters.
	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d: %+v", len(chapters), chapters)
	}
	if chapters[0].Prompt.Text != "fix the stale registrations" {
		t.Errorf("chapter 1 prompt = %q", chapters[0].Prompt.Text)
	}

	// Chapter 1: agent intro, tool run, agent conclusion.
	got := kinds(chapters[0].Events)
	want := []Kind{Agent, Tools, Agent}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("chapter 1 kinds = %v, want %v", got, want)
	}
	run := chapters[0].Events[1]
	// 1 marker in m2 + 2 markers in m3/m4; the "[2 tool result(s)]" row
	// confirms calls already counted and must not inflate the total.
	if run.Total != 3 || run.Counts["exec_command"] != 3 {
		t.Errorf("run = total %d counts %v, want 3 exec_command", run.Total, run.Counts)
	}

	// Chapter 2: tool run, agent, checkpoint.
	got = kinds(chapters[1].Events)
	want = []Kind{Tools, Agent, Checkpoint}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("chapter 2 kinds = %v, want %v", got, want)
	}
	run = chapters[1].Events[0]
	if run.Total != 2 || run.Counts["apply_patch"] != 1 || run.Counts["exec_command"] != 1 {
		t.Errorf("chapter 2 run = %+v", run)
	}
}

func TestBuildInterleavesCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			fmt.Sprintf("GIT_AUTHOR_DATE=@%d +0000", base+400),
			fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", base+400),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitIn("init", "-q")
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0644)
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "Fix stale registrations")

	database, session := newTestDB(t, repo)
	chapters, err := Build(database, session)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The commit (base+400) lands in chapter 1 (base+60 … base+600),
	// after the concluding agent message at base+300.
	got := kinds(chapters[0].Events)
	want := []Kind{Agent, Tools, Agent, CommitEvent}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("chapter 1 kinds = %v, want %v", got, want)
	}
	if !strings.Contains(chapters[0].Events[3].Text, "Fix stale registrations") {
		t.Errorf("commit text = %q", chapters[0].Events[3].Text)
	}
}

func TestRender(t *testing.T) {
	database, session := newTestDB(t, "")
	chapters, _ := Build(database, session)
	out := Render(session, chapters)

	for _, needle := range []string{
		"◉", "you  fix the stale registrations",
		"⚙", "3 tool calls — exec_command ×3",
		"◆", "codex  Found the bug in register.go.",
		"✔", "checkpoint",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("Render missing %q in:\n%s", needle, out)
		}
	}
	if strings.Contains(out, "AGENTS.md") {
		t.Error("scaffolding leaked into rendered timeline")
	}
}
