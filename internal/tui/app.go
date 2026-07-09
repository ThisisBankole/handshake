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
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"handshake/internal/adapters"
	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/git"
	"handshake/internal/timeline"
)

type ui struct {
	app     *tview.Application
	db      *db.Database
	homeDir string

	sessions  []*db.Session
	allAgents []string // distinct agents across all sessions, for the filter
	agentIdx  int      // 0 = all agents, otherwise allAgents[agentIdx-1]
	syncing   bool     // a pull is running in the background

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

	pages  *tview.Pages
	cards  *tview.TextView // single centered column of session (or result) cards
	status *tview.TextView
	input  *tview.InputField // command box under the header: / commands or search

	selIdx     int   // card the selection bar is on
	cardW      int   // inner width of the cards panel at last render
	cardStarts []int // first text row of each card, for scroll-into-view
	cardEnds   []int // last text row of each card

	restore *db.Session // set when the user confirms a restore
}

// Run starts the browser. If the user confirms restoring a session it is
// returned so the caller can print the handoff brief once the terminal is
// back to normal; nil means the user just quit.
func Run(database *db.Database, homeDir string) (*db.Session, error) {
	// Best-effort Codex sync, same as `handshake list`.
	adapters.IngestCodexSessions(database, homeDir)

	u, err := newUI(database, homeDir)
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
func newUI(database *db.Database, homeDir string) (*ui, error) {
	applyTheme()
	u := &ui{app: tview.NewApplication(), db: database, homeDir: homeDir}
	if err := u.loadSessions(); err != nil {
		return nil, err
	}
	u.refreshAgents()
	u.build()
	u.renderCards()
	return u, nil
}

// refreshAgents recomputes the distinct-agent list for the filter, keeping
// the current filter selection when its agent still exists.
func (u *ui) refreshAgents() {
	current := u.agentFilter()
	all, err := u.db.ListSessions("")
	if err != nil {
		return
	}
	u.allAgents = nil
	for _, s := range all {
		if !contains(u.allAgents, s.Agent) {
			u.allAgents = append(u.allAgents, s.Agent)
		}
	}
	u.agentIdx = 0
	for i, a := range u.allAgents {
		if a == current {
			u.agentIdx = i + 1
			break
		}
	}
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
	u.cardW = 76
	u.cards = tview.NewTextView()
	u.cards.SetDynamicColors(true).SetWrap(false)
	u.cards.SetBorder(true).
		SetTitle(" s e s s i o n s ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(colBorder).
		SetBorderPadding(1, 1, 2, 2)
	focusColors(u.cards.Box)

	// Cards pad the selection bar to the panel's inner width, which is only
	// known after layout — re-render when it settles or the terminal resizes.
	u.app.SetAfterDrawFunc(func(_ tcell.Screen) {
		_, _, w, _ := u.cards.GetInnerRect()
		if w > 0 && w != u.cardW {
			u.cardW = w
			go u.app.QueueUpdateDraw(func() {
				u.renderCards()
				u.scrollToSelected()
			})
		}
	})

	title := tview.NewTextView()
	title.SetDynamicColors(true).
		SetText(" " + tag(colAccent) + "✦ [-]" + gradient("handshake", 0xB4BEFE, 0xCBA6F7))
	u.status = tview.NewTextView()
	u.status.SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	header := tview.NewFlex().
		AddItem(title, 0, 1, false).
		AddItem(u.status, 0, 1, false)

	u.input = tview.NewInputField()
	u.input.SetLabel("› ").
		SetLabelColor(colAccent).
		SetFieldBackgroundColor(tcell.ColorDefault).
		SetFieldTextColor(colText).
		SetPlaceholder("type / for commands · anything else searches every session…").
		SetPlaceholderTextColor(colFaint).
		SetDoneFunc(func(key tcell.Key) {
			switch key {
			case tcell.KeyEnter:
				text := strings.TrimSpace(u.input.GetText())
				switch {
				case text == "":
					u.closeInput()
				case strings.HasPrefix(text, "/"):
					u.runCommand(text)
				default:
					u.runSearch(text)
				}
			case tcell.KeyEscape:
				u.closeInput()
			}
		})
	u.input.SetAutocompleteFunc(u.completeCommand)
	u.input.SetAutocompletedFunc(u.commandSelected)
	u.input.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetBorderColor(colBorder)
	focusColors(u.input.Box)

	// The card column sits centered with breathing room on both sides,
	// like the reader views.
	body := tview.NewFlex().
		AddItem(tview.NewBox(), 0, 1, false).
		AddItem(u.cards, 0, 5, true).
		AddItem(tview.NewBox(), 0, 1, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(u.input, 3, 0, false).
		AddItem(body, 0, 1, true)

	u.viewReader = newReader()
	u.subReader = newReader()

	u.pages = tview.NewPages().AddPage("main", root, true, true)
	u.app.SetRoot(u.pages, true).SetInputCapture(u.onKey)
	u.app.EnableMouse(true)
	u.updateStatus()
}

const viewHints = "esc back · ↑↓ scroll · d detail · r brief · t timeline · y restore · h help"

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
	if u.app.GetFocus() == u.input {
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
		case ev.Rune() == 'h', ev.Rune() == '?':
			u.openHelp()
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
			u.selIdx = 0
			u.renderCards()
			u.cards.ScrollToBeginning()
		}
	case ev.Key() == tcell.KeyUp, ev.Rune() == 'k':
		u.moveSel(-1)
	case ev.Key() == tcell.KeyDown, ev.Rune() == 'j':
		u.moveSel(1)
	case ev.Key() == tcell.KeyPgUp:
		u.moveSel(-3)
	case ev.Key() == tcell.KeyPgDn:
		u.moveSel(3)
	case ev.Key() == tcell.KeyHome:
		u.setSel(0)
	case ev.Key() == tcell.KeyEnd:
		u.setSel(u.cardCount() - 1)
	case ev.Key() == tcell.KeyEnter:
		u.onEnter(u.selIdx)
	case ev.Rune() == '/':
		u.openInput()
	case ev.Rune() == 'a':
		u.cycleAgent()
	case ev.Rune() == 's':
		u.startSync("")
	case ev.Rune() == 'h', ev.Rune() == '?':
		u.openHelp()
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

// ── browser: card column ───────────────────────────────────────────────────

func (u *ui) cardCount() int {
	if u.inResults {
		return len(u.results)
	}
	return len(u.sessions)
}

func (u *ui) setSel(i int) {
	n := u.cardCount()
	if n == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	if i == u.selIdx {
		return
	}
	u.selIdx = i
	u.renderCards()
	u.scrollToSelected()
}

func (u *ui) moveSel(delta int) { u.setSel(u.selIdx + delta) }

// scrollToSelected nudges the panel so the selected card is fully visible.
func (u *ui) scrollToSelected() {
	if u.selIdx < 0 || u.selIdx >= len(u.cardStarts) {
		return
	}
	_, _, _, h := u.cards.GetInnerRect()
	if h <= 0 {
		return
	}
	top, _ := u.cards.GetScrollOffset()
	start, end := u.cardStarts[u.selIdx], u.cardEnds[u.selIdx]
	switch {
	case end-start+1 >= h, start < top:
		u.cards.ScrollTo(start, 0)
	case end >= top+h:
		u.cards.ScrollTo(end-h+1, 0)
	}
}

// renderCards redraws the whole card column, painting the selected card on
// the surface background.
func (u *ui) renderCards() {
	if u.selIdx >= u.cardCount() {
		u.selIdx = 0
	}
	if u.inResults {
		u.cards.SetTitle(fmt.Sprintf(" results · %s ", tview.Escape(u.lastQuery)))
	} else {
		u.cards.SetTitle(" s e s s i o n s ")
	}

	var b strings.Builder
	u.cardStarts = u.cardStarts[:0]
	u.cardEnds = u.cardEnds[:0]
	row := 0
	add := func(lines []string) {
		u.cardStarts = append(u.cardStarts, row)
		for _, l := range lines {
			b.WriteString(l + "\n")
			row++
		}
		u.cardEnds = append(u.cardEnds, row-1)
	}

	if u.inResults {
		for i, r := range u.results {
			add(u.resultCard(r, i == u.selIdx))
		}
		if len(u.results) == 0 {
			fmt.Fprintf(&b, "\n%s No matches for %s%s[-]",
				tag(colDim), tag(colYellow), tview.Escape(u.lastQuery))
		}
	} else {
		for i, s := range u.sessions {
			add(u.sessionCard(s, i == u.selIdx))
		}
		if len(u.sessions) == 0 {
			fmt.Fprintf(&b, "\n%s No sessions yet. Run a coding agent, then checkpoint or /pull.[-]", tag(colDim))
		}
	}
	u.cards.SetText(strings.TrimRight(b.String(), "\n"))
}

// selectCard paints a block of card lines onto the surface background and
// pads each line so the bar spans the panel.
func (u *ui) selectCard(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if pad := u.cardW - tview.TaggedStringWidth(l); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		out[i] = "[:#313244]" + l + "[:-]"
	}
	return out
}

// sessionCard renders one session as a multi-line card.
func (u *ui) sessionCard(s *db.Session, sel bool) []string {
	w := u.cardW
	if w < 40 {
		w = 40
	}
	var lines []string
	push := func(l string) { lines = append(lines, l) }

	push("")
	for _, t := range wrapText(s.Title, w-4) {
		push(" " + tag(colText) + "[::b]" + tview.Escape(t) + "[::-]")
	}
	if s.WorkingDir != "" {
		push("")
		push(" " + tag(colFaint) + "dir[-]      " + tag(colDim) + tview.Escape(s.WorkingDir) + "[-]")
	}

	if s.GitState != "" {
		var st git.State
		if json.Unmarshal([]byte(s.GitState), &st) == nil && st.Commit != "" {
			push("")
			push(" " + tag(colFaint) + "── git ──[-]")
			short := st.Commit
			if len(short) > 8 {
				short = short[:8]
			}
			push(fmt.Sprintf(" %s%s[-] @ %s%s[-] %s%s[-]",
				tag(colAccent2), tview.Escape(st.Branch), tag(colDim), short,
				tag(colText), tview.Escape(oneLine(st.Message, w-20))))
			if strings.TrimSpace(st.Status) == "" {
				push(" " + tag(colGreen) + "✓ clean[-]")
			} else {
				n := len(strings.Split(strings.TrimSpace(st.Status), "\n"))
				push(fmt.Sprintf(" %s● %d uncommitted change(s)[-]", tag(colYellow), n))
			}
		}
	}

	// Footer: agent pill + model on the left, relative age on the right.
	// The pill's background reset must restore the selection bar when active.
	bgReset := "[-:-]"
	if sel {
		bgReset = "[-:#313244]"
	}
	left := fmt.Sprintf(" [%s:#45475a] %s %s", "#cdd6f4", tview.Escape(s.Agent), bgReset)
	if s.Model != "" {
		left += "  " + tag(colDim) + tview.Escape(s.Model) + "[-]"
	}
	right := tag(colDim) + relTime(s.UpdatedAt) + "[-] "
	pad := w - tview.TaggedStringWidth(left) - tview.TaggedStringWidth(right)
	if pad < 1 {
		pad = 1
	}
	push("")
	push(left + strings.Repeat(" ", pad) + right)

	if sel {
		lines = u.selectCard(lines)
	}
	// Separator rule between cards, outside the selection bar.
	lines = append(lines, "", " "+tag(colBorder)+strings.Repeat("─", max(w-2, 10))+"[-]")
	return lines
}

// resultCard renders one search hit as a compact card.
func (u *ui) resultCard(r *db.SearchResult, sel bool) []string {
	w := u.cardW
	if w < 40 {
		w = 40
	}
	lines := []string{
		"",
		" " + tag(colText) + tview.Escape(oneLine(r.Content, w-4)) + "[-]",
		fmt.Sprintf(" %s%s[-] %s· %s · %s · %s[-]",
			tag(agentColor(r.SessionAgent)), tview.Escape(r.SessionAgent), tag(colFaint),
			tview.Escape(oneLine(r.SessionTitle, 36)), r.Role, relTime(r.CreatedAt)),
	}
	if sel {
		lines = u.selectCard(lines)
	}
	return append(lines, "", " "+tag(colBorder)+strings.Repeat("─", max(w-2, 10))+"[-]")
}

// wrapText word-wraps s to lines at most w runes wide.
func wrapText(s string, w int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{"untitled"}
	}
	var lines []string
	cur := words[0]
	for _, word := range words[1:] {
		if len([]rune(cur))+1+len([]rune(word)) <= w {
			cur += " " + word
		} else {
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (u *ui) currentSession() *db.Session {
	i := u.selIdx
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
			if st.Remote != "" {
				fmt.Fprintf(&b, "%sremote[-]   %s%s[-]\n", tag(colFaint), tag(colDim), tview.Escape(st.Remote))
			}
			if len(st.Commits) > 0 {
				section("session commits")
				b.WriteString(commitRail(&st))
			} else {
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

// commitRail renders the stored session commits as a vertical rail: the
// branch head first, then one node per commit (newest first) with the full
// git-show-style block hanging off it.
func commitRail(st *git.State) string {
	var b strings.Builder
	dot := tag(colGreen) + "●[-] "
	pipe := tag(colBorder) + "│[-]  "

	short := st.Commit
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Fprintf(&b, "%s%s%s[-] @ %s%s[-]  %s\n",
		dot, tag(colAccent2), tview.Escape(st.Branch), tag(colDim), short,
		tview.Escape(oneLine(st.Message, 44)))
	if strings.TrimSpace(st.Status) == "" {
		fmt.Fprintf(&b, "%s%s✓ clean[-]\n", pipe, tag(colGreen))
	} else {
		n := len(strings.Split(strings.TrimSpace(st.Status), "\n"))
		fmt.Fprintf(&b, "%s%s● %d uncommitted change(s)[-]\n", pipe, tag(colYellow), n)
	}

	// Stored oldest-first; show newest-first under the branch head.
	for i := len(st.Commits) - 1; i >= 0; i-- {
		c := st.Commits[i]
		b.WriteString(pipe + "\n")
		fmt.Fprintf(&b, "%s%scommit %s[-]\n", dot, tag(colAccent), tview.Escape(c.Hash))
		if c.Author != "" {
			fmt.Fprintf(&b, "%s%sAuthor: %s[-]\n", pipe, tag(colDim), tview.Escape(c.Author))
		}
		fmt.Fprintf(&b, "%s%sDate:   %s[-]\n", pipe, tag(colDim),
			time.Unix(c.When, 0).Format("Mon Jan 2 15:04:05 2006 -0700"))
		b.WriteString(pipe + "\n")
		fmt.Fprintf(&b, "%s    %s%s[-]\n", pipe, tag(colText), tview.Escape(c.Subject))
		if c.Body != "" {
			b.WriteString(pipe + "\n")
			for _, line := range strings.Split(c.Body, "\n") {
				fmt.Fprintf(&b, "%s    %s%s[-]\n", pipe, tag(colDim), tview.Escape(line))
			}
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
	u.app.SetFocus(u.cards)
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
		u.app.SetFocus(u.cards)
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

// ── command box: / commands or full-text search ────────────────────────────

// slashCommand is one entry in the / dropdown. args is rendered in the
// dropdown and, when non-empty, selecting the command waits for an argument
// instead of running immediately.
type slashCommand struct {
	name string
	args string
	desc string
	run  func(u *ui, arg string)
}

// Assigned in init: a package-level literal would form an initialization
// cycle (slashCommands → openHelp → helpText → slashCommands).
var slashCommands []slashCommand

func init() {
	slashCommands = []slashCommand{
		{"/open", "", "open the selected session", func(u *ui, _ string) { u.openView("detail", u.currentSession()) }},
		{"/brief", "", "handoff brief for the selected session", func(u *ui, _ string) { u.openView("brief", u.currentSession()) }},
		{"/timeline", "", "timeline for the selected session", func(u *ui, _ string) { u.openView("timeline", u.currentSession()) }},
		{"/search", "<query>", "search across all sessions", func(u *ui, arg string) { u.runSearch(arg) }},
		{"/pull", "<agent>", "pull sessions from agent storage", func(u *ui, arg string) { u.startSync(arg) }},
		{"/agent", "<name>", "filter by agent · no name cycles", func(u *ui, arg string) { u.setAgentFilter(arg) }},
		{"/help", "", "all commands and keys", func(u *ui, _ string) { u.openHelp() }},
		{"/quit", "", "leave the browser", func(u *ui, _ string) { u.app.Stop() }},
	}
}

func findCommand(name string) *slashCommand {
	for i := range slashCommands {
		if slashCommands[i].name == name {
			return &slashCommands[i]
		}
	}
	return nil
}

func (u *ui) openInput() {
	u.input.SetText("")
	u.app.SetFocus(u.input)
}

func (u *ui) closeInput() {
	u.input.SetText("")
	u.app.SetFocus(u.cards)
}

// completeCommand feeds the dropdown while the box holds a bare "/…" word.
func (u *ui) completeCommand(current string) []string {
	if !strings.HasPrefix(current, "/") || strings.Contains(current, " ") {
		return nil
	}
	var entries []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c.name, strings.ToLower(current)) {
			label := c.name
			if c.args != "" {
				label += " " + c.args
			}
			entries = append(entries, fmt.Sprintf("%-17s %s", label, c.desc))
		}
	}
	return entries
}

// commandSelected fires when the user picks a dropdown entry. Commands
// without an argument run immediately; the rest wait in the box for one.
func (u *ui) commandSelected(text string, index, source int) bool {
	if source == tview.AutocompletedNavigate {
		return false
	}
	name := strings.Fields(text)[0]
	if cmd := findCommand(name); cmd != nil && cmd.args == "" &&
		(source == tview.AutocompletedEnter || source == tview.AutocompletedClick) {
		u.runCommand(name)
		return true
	}
	u.input.SetText(name + " ")
	return true
}

func (u *ui) runCommand(text string) {
	fields := strings.SplitN(strings.TrimSpace(text), " ", 2)
	name := strings.ToLower(fields[0])
	arg := ""
	if len(fields) > 1 {
		arg = strings.TrimSpace(fields[1])
	}
	cmd := findCommand(name)
	u.closeInput()
	if cmd == nil {
		u.status.SetText(tag(colRed) + "unknown command " + tview.Escape(name) + " — type / to list commands ")
		return
	}
	cmd.run(u, arg)
}

func (u *ui) runSearch(query string) {
	u.closeInput()
	if query == "" {
		return
	}
	results, err := u.db.SearchAllMessages(query, 50, u.agentFilter())
	if err != nil {
		u.status.SetText(tag(colRed) + "search failed: " + tview.Escape(err.Error()) + " ")
		return
	}
	u.results, u.lastQuery, u.inResults = results, query, true
	u.selIdx = 0
	u.renderCards()
	u.cards.ScrollToBeginning()
}

// ── agent filter ───────────────────────────────────────────────────────────

func (u *ui) cycleAgent() {
	if len(u.allAgents) == 0 {
		return
	}
	u.agentIdx = (u.agentIdx + 1) % (len(u.allAgents) + 1)
	u.reloadList()
}

// setAgentFilter filters by agent name ("all" or empty cycles instead).
func (u *ui) setAgentFilter(name string) {
	switch name {
	case "":
		u.cycleAgent()
		return
	case "all":
		u.agentIdx = 0
	default:
		idx := -1
		for i, a := range u.allAgents {
			if a == name {
				idx = i + 1
				break
			}
		}
		if idx < 0 {
			u.status.SetText(tag(colRed) + "no sessions from " + tview.Escape(name) + " ")
			return
		}
		u.agentIdx = idx
	}
	u.reloadList()
}

// reloadList re-queries sessions for the current filter and redraws the
// browser back in session mode.
func (u *ui) reloadList() {
	if err := u.loadSessions(); err != nil {
		u.status.SetText(tag(colRed) + "failed to load sessions: " + tview.Escape(err.Error()) + " ")
		return
	}
	u.inResults = false
	u.selIdx = 0
	u.updateStatus()
	u.renderCards()
	u.cards.ScrollToBeginning()
}

// ── pull ───────────────────────────────────────────────────────────────────

// startSync pulls sessions from agents' native storage in the background,
// then reloads the list and reports the outcome in the status bar. An empty
// agent pulls everything.
func (u *ui) startSync(agent string) {
	if u.syncing {
		return
	}
	if agent != "" && !contains(adapters.PullableAgents, agent) {
		u.status.SetText(tag(colRed) + "unknown agent " + tview.Escape(agent) +
			" — " + strings.Join(adapters.PullableAgents, ", ") + " ")
		return
	}
	u.syncing = true
	u.status.SetText(tag(colYellow) + "pulling sessions… ")

	go func() {
		var results []adapters.SyncResult
		if agent != "" {
			results = []adapters.SyncResult{adapters.SyncAgent(u.db, u.homeDir, agent)}
		} else {
			results = adapters.SyncAll(u.db, u.homeDir)
		}
		u.app.QueueUpdateDraw(func() {
			u.syncing = false
			u.refreshAgents()
			if err := u.loadSessions(); err != nil {
				u.status.SetText(tag(colRed) + "pull failed: " + tview.Escape(err.Error()) + " ")
				return
			}
			u.inResults = false
			u.renderCards()
			u.scrollToSelected()
			u.status.SetText(syncSummary(results))
		})
	}()
}

// syncSummary condenses per-agent pull results into one status-bar line.
func syncSummary(results []adapters.SyncResult) string {
	imported, updated, failed := 0, 0, 0
	for _, r := range results {
		imported += r.Imported
		updated += r.Updated
		failed += r.Failed
		if r.Err != nil {
			failed++
		}
	}
	var parts []string
	if imported > 0 {
		parts = append(parts, fmt.Sprintf("%d new", imported))
	}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", updated))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if len(parts) == 0 {
		return tag(colFaint) + "pull: nothing new "
	}
	col := colGreen
	if imported+updated == 0 {
		col = colYellow
	}
	return tag(col) + "pull: " + strings.Join(parts, " · ") + " "
}

// ── help ───────────────────────────────────────────────────────────────────

// openHelp shows every keybinding on the sub layer, so it works from the
// browser and from any full-screen view.
func (u *ui) openHelp() {
	u.openSub("help", "help", helpText(), "esc close · ↑↓ scroll")
}

func helpText() string {
	var b strings.Builder
	section := func(name string) {
		fmt.Fprintf(&b, "\n%s── %s ──[-]\n\n", tag(colFaint), name)
	}
	key := func(k, desc string) {
		fmt.Fprintf(&b, "  %s%-9s[-] %s%s[-]\n", tag(colAccent), k, tag(colText), desc)
	}

	section("browser")
	key("enter", "open the selected session")
	key("r", "handoff brief")
	key("t", "session timeline")
	key("/", "focus the command box")
	key("a", "cycle the agent filter")
	key("s", "pull sessions from agent storage ("+strings.Join(adapters.PullableAgents, ", ")+")")
	key("h  ?", "this help")
	key("esc", "leave search results")
	key("q", "quit")

	section("command box")
	fmt.Fprintf(&b, "  %stype / for the command list · anything else searches every session[-]\n\n", tag(colDim))
	for _, c := range slashCommands {
		label := c.name
		if c.args != "" {
			label += " " + c.args
		}
		fmt.Fprintf(&b, "  %s%-17s[-] %s%s[-]\n", tag(colAccent2), label, tag(colText), c.desc)
	}

	section("full-screen view")
	key("↑ ↓", "scroll")
	key("d", "session detail")
	key("r", "handoff brief")
	key("t", "session timeline")
	key("y", "restore this session (asks to confirm)")
	key("esc", "back one layer")

	section("restore confirm")
	key("y  enter", "restore & print the handoff brief")
	key("n  esc", "cancel")

	section("command line")
	fmt.Fprintf(&b, "  %s%s[-]%s   import sessions without opening the TUI\n",
		tag(colAccent2), tview.Escape("handshake pull [agent]"), tag(colText))
	fmt.Fprintf(&b, "  %shandshake restore <title>[-]%s print a session's handoff brief\n",
		tag(colAccent2), tag(colText))
	return b.String()
}
