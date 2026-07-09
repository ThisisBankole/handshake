package tui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"handshake/internal/db"
	"handshake/internal/git"
)

// newTestDB creates a database with two checkpointed sessions; the first has
// a transcript rich enough to exercise the brief and timeline views.
func newTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	gitState, _ := json.Marshal(git.State{
		Commit:  "24aace8badc0ffee24aace8badc0ffee24aace8b",
		Branch:  "main",
		Message: "Kill stale serve processes",
		Status:  " M cmd/handshake/main.go",
		Remote:  "https://github.com/ThisisBankole/handshake.git",
		Commits: []git.Commit{{
			Hash:    "5b8f0e3",
			Subject: "feat: public API docs at /docs",
			Author:  "Bankole <bjamgbadi@gmail.com>",
			Body:    "- Markdown-driven docs\n- Live coverage page",
			When:    time.Now().Unix() - 900,
		}},
	})
	now := time.Now().Unix()
	sessions := []*db.Session{
		{
			ID: "s1", Title: "Fix port migration", Agent: "claude-code",
			WorkingDir: "/tmp/handshake", Model: "fable-5",
			Summary:   "Fixed stale agent registrations after port change.",
			Decisions: "auto-detect busy port\nkeep 8765 as default",
			GitState:  string(gitState),
			CreatedAt: now - 1200, UpdatedAt: now,
		},
		{
			ID: "s2", Title: "Session search", Agent: "codex",
			CreatedAt: now - 200000, UpdatedAt: now - 24*3600,
		},
	}
	for _, s := range sessions {
		if err := database.StoreSession(s); err != nil {
			t.Fatalf("StoreSession: %v", err)
		}
	}
	msgs := []*db.Message{
		{ID: "m1", Role: "user", Content: "fix the stale registrations", CreatedAt: now - 1100},
		{ID: "m2", Role: "assistant", Content: "Scanning first. [tool: Bash]", CreatedAt: now - 1000},
		{ID: "m3", Role: "tool", Content: "[tool: Bash]", CreatedAt: now - 900},
		{ID: "m4", Role: "assistant", Content: "Registered handshake on the migrated port", CreatedAt: now - 800},
	}
	for _, m := range msgs {
		m.SessionID = "s1"
		if err := database.StoreMessage(m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}
	return database
}

// harness runs the UI on a tcell simulation screen.
type harness struct {
	u    *ui
	sim  tcell.SimulationScreen
	done chan error
	once sync.Once
}

func startUI(t *testing.T, database *db.Database) *harness {
	t.Helper()
	u, err := newUI(database, t.TempDir())
	if err != nil {
		t.Fatalf("newUI: %v", err)
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	u.app.SetScreen(sim)

	h := &harness{u: u, sim: sim, done: make(chan error, 1)}
	go func() { h.done <- u.app.Run() }()
	t.Cleanup(func() { h.stop(t) })

	// app.Run calls screen.Init, which resets the simulation screen to
	// 80x24 — so wait for the first draw, then grow it enough that full
	// views fit. SetSize does not deliver a resize event, so force a
	// re-layout with a queued draw.
	h.waitFor(t, "handshake")
	sim.SetSize(100, 60)
	u.app.QueueUpdateDraw(func() {})
	return h
}

// waitExit blocks until app.Run returns (at most once across the test).
func (h *harness) waitExit(t *testing.T) {
	t.Helper()
	h.once.Do(func() {
		select {
		case err := <-h.done:
			if err != nil {
				t.Errorf("app.Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("app did not stop")
		}
	})
}

func (h *harness) stop(t *testing.T) {
	h.u.app.Stop()
	h.waitExit(t)
}

// screenText flattens the simulation screen into a single string.
func (h *harness) screenText() string {
	cells, w, _ := h.sim.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		}
		if (i+1)%w == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// waitFor polls until every needle is on screen.
func (h *harness) waitFor(t *testing.T, needles ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		text = h.screenText()
		found := true
		for _, n := range needles {
			if !strings.Contains(text, n) {
				found = false
				break
			}
		}
		if found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; last screen:\n%s", needles, text)
}

// waitGone polls until needle is no longer on screen.
func (h *harness) waitGone(t *testing.T, needle string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(h.screenText(), needle) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen still shows %q:\n%s", needle, h.screenText())
}

func (h *harness) key(k tcell.Key, r rune) {
	h.sim.InjectKey(k, r, tcell.ModNone)
	time.Sleep(30 * time.Millisecond)
}

func TestBrowserRendersCards(t *testing.T) {
	h := startUI(t, newTestDB(t))

	// Both sessions render as cards: title, git line, footer with agent
	// pill, model, and relative age.
	h.waitFor(t,
		"s e s s i o n s",
		"Fix port migration", "claude-code", "fable-5",
		"main @ 24aace8b",
		"1 uncommitted change",
		"Session search", "codex",
	)

	// The selected card paints a surface-background bar.
	cells, _, _ := h.sim.GetContents()
	surface := 0
	for _, c := range cells {
		if _, bg, _ := c.Style.Decompose(); bg == colSurface {
			surface++
		}
	}
	if surface == 0 {
		t.Error("no cells use the surface background — selection bar missing")
	}

	// Selection moves without losing the cards.
	h.key(tcell.KeyDown, 0)
	h.waitFor(t, "Session search")
}

func TestBrowserAgentFilter(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "2 sessions · all agents")

	h.key(tcell.KeyRune, 'a')
	h.waitFor(t, "1 sessions · claude-code")

	h.key(tcell.KeyRune, 'a')
	h.waitFor(t, "1 sessions · codex")

	h.key(tcell.KeyRune, 'a')
	h.waitFor(t, "2 sessions · all agents")
}

func TestFullScreenDetailAndBrief(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	// Enter opens the full-screen detail tree: metadata, the remote, the
	// session-commits rail with the full commit block, the inline timeline,
	// the summary, and the brief button at the end.
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "detail · Fix port migration",
		"github.com/ThisisBankole/handshake.git",
		"── session commits ──",
		"1 uncommitted change", "commit 5b8f0e3",
		"Author: Bankole <bjamgbadi@gmail.com>",
		"feat: public API docs at /docs",
		"- Markdown-driven docs",
		"── timeline ──", "you  fix the stale registrations",
		"── decisions ──", "auto-detect busy port",
		"▶ view brief", "y restore")

	// The summary is not on the detail — it lives behind the brief button.
	if strings.Contains(h.screenText(), "── summary ──") {
		t.Error("detail still renders a summary section")
	}

	// End lands on the brief button; Enter presses it.
	h.key(tcell.KeyEnd, 0)
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "brief · Fix port migration")

	// Esc returns to the browser.
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "s e s s i o n s")
	h.waitGone(t, "brief · Fix port migration")
}

func TestTimelineView(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	// t from the browser opens the timeline: chapter prompt, collapsed
	// tool run with counts, agent conclusion, checkpoint.
	h.key(tcell.KeyRune, 't')
	h.waitFor(t,
		"timeline · Fix port migration",
		"you  fix the stale registrations",
		"2 tool calls — Bash ×2",
		"claude-code  Registered handshake on the migrated port",
		"checkpoint",
	)

	// The selected row must highlight with the dark surface background —
	// tview's default inverted highlight makes tag-colored text unreadable.
	cells, _, _ := h.sim.GetContents()
	surface := 0
	for _, c := range cells {
		if _, bg, _ := c.Style.Decompose(); bg == colSurface {
			surface++
		}
	}
	if surface == 0 {
		t.Error("no cells use the surface highlight — selected tree row is using tview's default inverted style")
	}

	// Enter on the first chapter folds it.
	h.key(tcell.KeyEnter, 0)
	h.waitGone(t, "2 tool calls — Bash ×2")
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "2 tool calls — Bash ×2")

	// Select the agent event and open it full-screen.
	h.key(tcell.KeyDown, 0)
	h.key(tcell.KeyDown, 0)
	h.key(tcell.KeyDown, 0)
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "Registered handshake on the migrated port", "esc back")

	// Esc backs out one layer at a time: event → timeline → browser.
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "timeline · Fix port migration")
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "s e s s i o n s")
}

func TestBrowserSearch(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	h.key(tcell.KeyRune, '/')
	for _, r := range "migrated" {
		h.key(tcell.KeyRune, r)
	}
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "results · migrated", "Registered handshake on the migrated port")

	// Enter opens the matched message full-screen.
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "match · Fix port migration", "y restore")

	// Esc goes back to the results, Esc again to the session list.
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "results · migrated")
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "s e s s i o n s")
}

func TestCommandBox(t *testing.T) {
	h := startUI(t, newTestDB(t))

	// The box sits at the top with its placeholder visible.
	h.waitFor(t, "type / for commands")
	lines := strings.Split(h.screenText(), "\n")
	row := -1
	for i, line := range lines {
		if strings.Contains(line, "type / for commands") {
			row = i
			break
		}
	}
	if row < 0 || row > 3 {
		t.Fatalf("command box on row %d, expected near the top", row)
	}

	// / focuses the box; typing / drops down the command list.
	h.key(tcell.KeyRune, '/')
	h.key(tcell.KeyRune, '/')
	h.waitFor(t, "/pull <agent>", "/help", "/search <query>")

	// Narrow to /pull, select it (fills the box, waits for the argument),
	// then run it against the empty test home.
	for _, r := range "pull" {
		h.key(tcell.KeyRune, r)
	}
	h.key(tcell.KeyEnter, 0) // select from dropdown → "/pull "
	h.key(tcell.KeyEnter, 0) // execute
	h.waitFor(t, "pull: nothing new")

	// A command without arguments runs straight from the dropdown.
	h.key(tcell.KeyRune, '/')
	for _, r := range "/help" {
		h.key(tcell.KeyRune, r)
	}
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "── command box ──")
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "s e s s i o n s")

	// Unknown commands report in the status bar instead of doing nothing.
	h.key(tcell.KeyRune, '/')
	for _, r := range "/nope" {
		h.key(tcell.KeyRune, r)
	}
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "unknown command /nope")
}

func TestHelpOverlay(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	// h opens the help overlay from the browser.
	h.key(tcell.KeyRune, 'h')
	h.waitFor(t, "pull sessions from agent storage", "restore & print the handoff brief", "handshake pull [agent]")

	// Esc closes it back to the browser.
	h.key(tcell.KeyEscape, 0)
	h.waitGone(t, "pull sessions from agent storage")
	h.waitFor(t, "s e s s i o n s")

	// ? opens it from a full-screen view too, and esc backs out one layer.
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "detail · Fix port migration")
	h.key(tcell.KeyRune, '?')
	h.waitFor(t, "pull sessions from agent storage")
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "detail · Fix port migration")
}

func TestPullKey(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	// The test homeDir has no agent storage, so a pull finds nothing new;
	// the status bar reports the outcome and the list survives the reload.
	h.key(tcell.KeyRune, 's')
	h.waitFor(t, "pull: nothing new", "Fix port migration")
}

func TestRestoreFlow(t *testing.T) {
	h := startUI(t, newTestDB(t))
	h.waitFor(t, "Fix port migration")

	// Open detail, then y for the full-screen restore confirmation.
	h.key(tcell.KeyEnter, 0)
	h.waitFor(t, "detail · Fix port migration")
	h.key(tcell.KeyRune, 'y')
	h.waitFor(t, "restore · Fix port migration", "restore & print brief")

	// Esc cancels back to the detail view…
	h.key(tcell.KeyEscape, 0)
	h.waitFor(t, "detail · Fix port migration")
	h.waitGone(t, "restore & print brief")
	if h.u.restore != nil {
		t.Fatal("restore should be nil after cancel")
	}

	// …and y y confirms, stopping the app with the session recorded.
	h.key(tcell.KeyRune, 'y')
	h.waitFor(t, "restore & print brief")
	h.key(tcell.KeyRune, 'y')

	h.waitExit(t)
	if h.u.restore == nil || h.u.restore.ID != "s1" {
		t.Fatalf("expected restore of s1, got %+v", h.u.restore)
	}
}
