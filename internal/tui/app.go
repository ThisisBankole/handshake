// Package tui implements the interactive session browser behind
// `handshake browse` (and `handshake list` on a terminal).
//
// The UI has three layers: the browser (session list + preview pane, for
// scanning), a full-screen "view" layer (detail / brief / timeline / search
// match, for reading), and a full-screen "sub" layer above it (a timeline
// event, or the restore confirmation). Esc always backs out one layer.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"handshake/internal/adapters"
	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/git"
	"handshake/internal/timeline"
)

type ui struct {
	app *tview.Application
	db  *db.Database

	sessions  []*db.Session
	allAgents []string // distinct agents across all sessions, for the filter
	agentIdx  int      // 0 = all agents, otherwise allAgents[agentIdx-1]

	results   []*db.SearchResult
	lastQuery string
	inResults bool // list is showing search results instead of sessions

	// view/sub layer state
	view       string // "" | "detail" | "brief" | "timeline" | "match"
	sub        string // "" | "read" | "confirm"
	viewSess   *db.Session
	viewReader *reader
	subReader  *reader
	tree       *tview.TreeView

	pages   *tview.Pages
	list    *tview.List
	preview *tview.TextView
	status  *tview.TextView
	footer  *tview.Flex
	hints   *tview.TextView
	search  *tview.InputField

	restore *db.Session // set when the user confirms a restore
}

// Run starts the browser. If the user confirms restoring a session it is
// returned so the caller can print the handoff brief once the terminal is
// back to normal; nil means the user just quit.
func Run(database *db.Database, homeDir string) (*db.Session, error) {
	// Best-effort Codex sync, same as `handshake list`.
	adapters.IngestCodexSessions(database, homeDir)

	u, err := newUI(database)
	if err != nil {
		return nil, err
	}
	if err := u.app.Run(); err != nil {
		return nil, err
	}
	return u.restore, nil
}

// newUI constructs the fully wired application without running it, so tests
// can drive it on a simulation screen.
func newUI(database *db.Database) (*ui, error) {
	applyTheme()
	u := &ui{app: tview.NewApplication(), db: database}
	if err := u.loadSessions(); err != nil {
		return nil, err
	}
	for _, s := range u.sessions {
		if !contains(u.allAgents, s.Agent) {
			u.allAgents = append(u.allAgents, s.Agent)
		}
	}
	u.build()
	u.renderList()
	return u, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (u *ui) agentFilter() string {
	if u.agentIdx == 0 {
		return ""
	}
	return u.allAgents[u.agentIdx-1]
}

func (u *ui) loadSessions() error {
	sessions, err := u.db.ListSessions(u.agentFilter())
	if err != nil {
		return err
	}
	u.sessions = sessions
	return nil
}

func (u *ui) build() {
	u.list = tview.NewList()
	u.list.ShowSecondaryText(true).
		SetSecondaryTextColor(colFaint).
		SetSelectedStyle(tcell.StyleDefault.Foreground(colAccent).Background(colSurface).Bold(true)).
		SetHighlightFullLine(true).
		SetChangedFunc(func(i int, _, _ string, _ rune) { u.updatePreview(i) }).
		SetSelectedFunc(func(i int, _, _ string, _ rune) { u.onEnter(i) })
	u.list.SetBorder(true).
		SetTitle(" sessions ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(colAccent).
		SetBorderPadding(1, 1, 1, 1)

	u.preview = tview.NewTextView()
	u.preview.SetDynamicColors(true).SetWordWrap(true)
	u.preview.SetBorder(true).
		SetTitle(" preview ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderPadding(1, 1, 2, 2)

	title := tview.NewTextView()
	title.SetDynamicColors(true).
		SetText(" " + tag(colAccent) + "✦ [-]" + gradient("handshake", 0xB4BEFE, 0xCBA6F7))
	u.status = tview.NewTextView()
	u.status.SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	header := tview.NewFlex().
		AddItem(title, 0, 1, false).
		AddItem(u.status, 0, 1, false)

	u.hints = tview.NewTextView()
	u.hints.SetDynamicColors(true).SetText(hintBar())

	u.search = tview.NewInputField()
	u.search.SetLabel(" / ").
		SetLabelColor(colAccent).
		SetFieldBackgroundColor(tcell.ColorDefault).
		SetFieldTextColor(colText).
		SetPlaceholder("search every session…").
		SetPlaceholderTextColor(colFaint).
		SetDoneFunc(func(key tcell.Key) {
			switch key {
			case tcell.KeyEnter:
				u.runSearch(strings.TrimSpace(u.search.GetText()))
			case tcell.KeyEscape:
				u.closeSearch()
			}
		})

	u.footer = tview.NewFlex()
	u.footer.AddItem(u.hints, 0, 1, false)

	body := tview.NewFlex().
		AddItem(u.list, 0, 2, true).
		AddItem(u.preview, 0, 3, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(u.footer, 1, 0, false)

	u.viewReader = newReader()
	u.subReader = newReader()

	u.pages = tview.NewPages().AddPage("main", root, true, true)
	u.app.SetRoot(u.pages, true).SetInputCapture(u.onKey)
	u.app.EnableMouse(true)
	u.updateStatus()
}

func hintBar() string {
	key := func(k, label string) string {
		return tag(colAccent) + k + tag(colFaint) + " " + label
	}
	return " " + tag(colFaint) +
		key("enter", "open") + "   " +
		key("r", "brief") + "   " +
		key("t", "timeline") + "   " +
		key("/", "search") + "   " +
		key("a", "agent") + "   " +
		key("q", "quit")
}

const viewHints = "esc back · ↑↓ scroll · d detail · r brief · t timeline · y restore"

func (u *ui) updateStatus() {
	filter := "all agents"
	col := colFaint
	if agent := u.agentFilter(); agent != "" {
		filter = agent
		col = agentColor(agent)
	}
	u.status.SetText(fmt.Sprintf("%s%d sessions · %s%s[-] ",
		tag(colFaint), len(u.sessions), tag(col), filter))
}

// ── key routing ────────────────────────────────────────────────────────────

// onKey routes keys by layer: sub above view above browser. Esc always backs
// out one layer; q always quits (except while typing a search).
func (u *ui) onKey(ev *tcell.EventKey) *tcell.EventKey {
	if u.app.GetFocus() == u.search {
		return ev
	}
	if ev.Rune() == 'q' {
		u.app.Stop()
		return nil
	}

	if u.sub != "" {
		switch {
		case ev.Key() == tcell.KeyEscape, u.sub == "confirm" && ev.Rune() == 'n':
			u.closeSub()
		case u.sub == "confirm" && (ev.Rune() == 'y' || ev.Key() == tcell.KeyEnter):
			u.restore = u.viewSess
			u.app.Stop()
		default:
			return ev // arrows etc. scroll the sub reader
		}
		return nil
	}

	if u.view != "" {
		switch {
		case ev.Key() == tcell.KeyEscape:
			u.closeView()
		case ev.Rune() == 'd':
			u.openView("detail", u.viewSess)
		case ev.Rune() == 'r':
			u.openView("brief", u.viewSess)
		case ev.Rune() == 't':
			u.openView("timeline", u.viewSess)
		case ev.Rune() == 'y':
			u.openConfirm(u.viewSess)
		default:
			return ev // arrows scroll the reader / move the tree
		}
		return nil
	}

	// Browser layer.
	switch {
	case ev.Key() == tcell.KeyEscape:
		if u.inResults {
			u.inResults = false
			u.renderList()
			u.updatePreview(u.list.GetCurrentItem())
		}
	case ev.Rune() == '/':
		u.openSearch()
	case ev.Rune() == 'a':
		u.cycleAgent()
	case ev.Rune() == 'r':
		if s := u.currentSession(); s != nil {
			u.openView("brief", s)
		}
	case ev.Rune() == 't':
		if s := u.currentSession(); s != nil {
			u.openView("timeline", s)
		}
	default:
		return ev
	}
	return nil
}

// ── browser: list + preview ────────────────────────────────────────────────

func (u *ui) renderList() {
	u.list.Clear()
	if u.inResults {
		u.list.SetTitle(fmt.Sprintf(" results · %s ", u.lastQuery))
		for _, r := range u.results {
			meta := fmt.Sprintf("%s%s[-] · %s · %s",
				tag(agentColor(r.SessionAgent)), r.SessionAgent, oneLine(r.SessionTitle, 30), relTime(r.CreatedAt))
			u.list.AddItem(oneLine(r.Content, 64), meta, 0, nil)
		}
		if len(u.results) == 0 {
			u.preview.SetText(fmt.Sprintf("\n%sNo matches for %s%q[-]",
				tag(colDim), tag(colYellow), u.lastQuery))
		}
		return
	}
	u.list.SetTitle(" sessions ")
	for _, s := range u.sessions {
		meta := fmt.Sprintf("%s%s[-] · %s", tag(agentColor(s.Agent)), s.Agent, relTime(s.UpdatedAt))
		u.list.AddItem(s.Title, meta, 0, nil)
	}
	if len(u.sessions) == 0 {
		u.preview.SetText(fmt.Sprintf("\n%sNo sessions yet.\n\nRun a coding agent and checkpoint a session,\nthen come back here to browse and restore it.", tag(colDim)))
	}
}

func (u *ui) updatePreview(i int) {
	if u.inResults {
		if i < 0 || i >= len(u.results) {
			return
		}
		r := u.results[i]
		var b strings.Builder
		fmt.Fprintf(&b, "[::b]%s[::-]\n\n", tview.Escape(r.SessionTitle))
		fmt.Fprintf(&b, "%s● [-]%s%s[-]  %s%s · %s[-]\n\n",
			tag(agentColor(r.SessionAgent)), tag(colText), r.SessionAgent, tag(colFaint), r.Role, relTime(r.CreatedAt))
		fmt.Fprintf(&b, "%s%s", tag(colText), tview.Escape(r.Content))
		u.preview.SetTitle(" match ")
		u.preview.SetText(b.String()).ScrollToBeginning()
		return
	}
	if i < 0 || i >= len(u.sessions) {
		return
	}
	u.preview.SetTitle(" preview ")
	u.preview.SetText(sessionDetail(u.sessions[i])).ScrollToBeginning()
}

func (u *ui) currentSession() *db.Session {
	i := u.list.GetCurrentItem()
	if u.inResults {
		if i < 0 || i >= len(u.results) {
			return nil
		}
		s, err := u.db.GetSession(u.results[i].SessionID)
		if err != nil {
			return nil
		}
		return s
	}
	if i < 0 || i >= len(u.sessions) {
		return nil
	}
	return u.sessions[i]
}

// onEnter opens the selected thing full-screen: a session's detail, or a
// search match's full message.
func (u *ui) onEnter(i int) {
	if u.inResults {
		if i >= 0 && i < len(u.results) {
			u.openMatch(u.results[i])
		}
		return
	}
	if s := u.currentSession(); s != nil {
		u.openView("detail", s)
	}
}

func sessionDetail(s *db.Session) string {
	var b strings.Builder
	section := func(name string) {
		fmt.Fprintf(&b, "\n%s── %s ──[-]\n\n", tag(colFaint), name)
	}

	fmt.Fprintf(&b, "[::b]%s%s[-::-]\n\n", tag(colAccent), tview.Escape(s.Title))
	fmt.Fprintf(&b, "%s● [-]%s", tag(agentColor(s.Agent)), s.Agent)
	if s.Model != "" {
		fmt.Fprintf(&b, "  %s%s[-]", tag(colFaint), s.Model)
	}
	b.WriteString("\n")
	if s.WorkingDir != "" {
		fmt.Fprintf(&b, "%sdir[-]      %s\n", tag(colFaint), s.WorkingDir)
	}
	fmt.Fprintf(&b, "%supdated[-]  %s\n", tag(colFaint), relTime(s.UpdatedAt))

	if s.GitState != "" {
		var st git.State
		if err := json.Unmarshal([]byte(s.GitState), &st); err == nil {
			section("git")
			short := st.Commit
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(&b, "%s%s[-] @ %s%s[-]  %s\n",
				tag(colAccent2), st.Branch, tag(colDim), short, oneLine(st.Message, 48))
			if strings.TrimSpace(st.Status) == "" {
				fmt.Fprintf(&b, "%s✓ clean[-]\n", tag(colGreen))
			} else {
				n := len(strings.Split(strings.TrimSpace(st.Status), "\n"))
				fmt.Fprintf(&b, "%s● %d uncommitted change(s)[-]\n", tag(colYellow), n)
			}
		}
	}

	section("summary")
	if strings.TrimSpace(s.Summary) != "" {
		b.WriteString(tview.Escape(s.Summary) + "\n")
	} else {
		fmt.Fprintf(&b, "%sno summary recorded[-]\n", tag(colFaint))
	}

	if strings.TrimSpace(s.Decisions) != "" {
		section("decisions")
		for _, line := range strings.Split(strings.TrimSpace(s.Decisions), "\n") {
			fmt.Fprintf(&b, "%s·[-] %s\n", tag(colAccent), tview.Escape(strings.TrimSpace(line)))
		}
	}
	return b.String()
}

// ── view layer (full-screen) ───────────────────────────────────────────────

// openView shows a full-screen view of a session, replacing any current one.
func (u *ui) openView(kind string, s *db.Session) {
	if s == nil {
		return
	}
	u.viewSess = s
	u.view = kind

	var prim tview.Primitive
	focus := tview.Primitive(u.viewReader.text)
	switch kind {
	case "detail":
		u.viewReader.show("detail · "+tview.Escape(oneLine(s.Title, 60)), sessionDetail(s), viewHints)
		prim = u.viewReader.root
	case "brief":
		brief, err := engine.NewBriefGenerator(u.db).GenerateBrief(s.ID)
		body := ""
		if err != nil {
			body = fmt.Sprintf("%sFailed to generate brief:[-] %s", tag(colRed), tview.Escape(err.Error()))
		} else {
			body = tag(colText) + tview.Escape(brief.Brief)
		}
		u.viewReader.show("brief · "+tview.Escape(oneLine(s.Title, 60)), body, viewHints)
		prim = u.viewReader.root
	case "timeline":
		chapters, err := timeline.Build(u.db, s)
		if err != nil {
			u.viewReader.show("timeline · "+tview.Escape(oneLine(s.Title, 60)),
				fmt.Sprintf("%sFailed to build timeline:[-] %s", tag(colRed), tview.Escape(err.Error())), viewHints)
			prim = u.viewReader.root
		} else {
			prim = u.timelineView(s, chapters)
			focus = u.tree
		}
	}

	u.pages.RemovePage("view")
	u.pages.AddPage("view", prim, true, true)
	u.app.SetFocus(focus)
}

// openMatch shows a search hit's full message; d/r/t then act on its session.
func (u *ui) openMatch(r *db.SearchResult) {
	s, err := u.db.GetSession(r.SessionID)
	if err != nil {
		return
	}
	u.viewSess = s
	u.view = "match"

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s · %s[-]\n\n", tag(colFaint), r.Role, relTime(r.CreatedAt))
	fmt.Fprintf(&b, "%s%s", tag(colText), tview.Escape(r.Content))
	u.viewReader.show("match · "+tview.Escape(oneLine(r.SessionTitle, 60)), b.String(), viewHints)

	u.pages.RemovePage("view")
	u.pages.AddPage("view", u.viewReader.root, true, true)
	u.app.SetFocus(u.viewReader.text)
}

func (u *ui) closeView() {
	u.pages.RemovePage("view")
	u.view = ""
	u.viewSess = nil
	u.app.SetFocus(u.list)
}

// ── sub layer (full-screen, above a view) ──────────────────────────────────

func (u *ui) openSub(kind, title, body, hints string) {
	u.sub = kind
	u.subReader.show(title, body, hints)
	u.pages.RemovePage("sub")
	u.pages.AddPage("sub", u.subReader.root, true, true)
	u.app.SetFocus(u.subReader.text)
}

func (u *ui) closeSub() {
	u.pages.RemovePage("sub")
	u.sub = ""
	if u.view == "timeline" && u.tree != nil {
		u.app.SetFocus(u.tree)
	} else if u.view != "" {
		u.app.SetFocus(u.viewReader.text)
	} else {
		u.app.SetFocus(u.list)
	}
}

// openConfirm shows the restore packet full-screen; y confirms, which stops
// the app so the caller can print the brief to stdout.
func (u *ui) openConfirm(s *db.Session) {
	if s == nil {
		return
	}
	body := restorePacket(s)
	if body == "" {
		body = sessionDetail(s)
	} else {
		body = tag(colText) + tview.Escape(body)
	}
	u.openSub("confirm",
		"restore · "+tview.Escape(oneLine(s.Title, 60)),
		body,
		tag(colGreen)+"y"+tag(colFaint)+" restore & print brief · "+tag(colRed)+"esc"+tag(colFaint)+" cancel")
}

// restorePacket rebuilds the git restore packet shown by `handshake restore`.
func restorePacket(s *db.Session) string {
	if s.GitState == "" {
		return ""
	}
	var checkpoint git.State
	if err := json.Unmarshal([]byte(s.GitState), &checkpoint); err != nil {
		return ""
	}
	return git.BuildRestorePacket(s.Title, s.Agent, s.UpdatedAt, s.WorkingDir, &checkpoint, s.Summary, s.Decisions)
}

// ── search ─────────────────────────────────────────────────────────────────

func (u *ui) openSearch() {
	u.search.SetText("")
	u.footer.Clear()
	u.footer.AddItem(u.search, 0, 1, true)
	u.app.SetFocus(u.search)
}

func (u *ui) closeSearch() {
	u.footer.Clear()
	u.footer.AddItem(u.hints, 0, 1, false)
	u.app.SetFocus(u.list)
}

func (u *ui) runSearch(query string) {
	u.closeSearch()
	if query == "" {
		return
	}
	results, err := u.db.SearchAllMessages(query, 50, u.agentFilter())
	if err != nil {
		u.preview.SetText(fmt.Sprintf("\n%sSearch failed:[-] %s", tag(colRed), tview.Escape(err.Error())))
		return
	}
	u.results, u.lastQuery, u.inResults = results, query, true
	u.renderList()
	if len(results) > 0 {
		u.updatePreview(0)
	}
}

// ── agent filter ───────────────────────────────────────────────────────────

func (u *ui) cycleAgent() {
	if len(u.allAgents) == 0 {
		return
	}
	u.agentIdx = (u.agentIdx + 1) % (len(u.allAgents) + 1)
	if err := u.loadSessions(); err != nil {
		u.preview.SetText(fmt.Sprintf("\n%sFailed to load sessions:[-] %s", tag(colRed), tview.Escape(err.Error())))
		return
	}
	u.inResults = false
	u.updateStatus()
	u.renderList()
	u.updatePreview(u.list.GetCurrentItem())
}
