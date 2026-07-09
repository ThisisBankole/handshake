package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"handshake/internal/db"
	"handshake/internal/git"
	"handshake/internal/timeline"
)

// detailTree builds the full-screen session detail as one navigable tree:
// metadata, the session-commits rail, a collapsible timeline, the summary
// and decisions, and a "view brief" button at the end that the arrow keys
// reach like any other row.
func (u *ui) detailTree(s *db.Session) tview.Primitive {
	selected := tcell.StyleDefault.Background(colSurface).Bold(true)
	root := tview.NewTreeNode("")
	add := func(line string) {
		root.AddChild(tview.NewTreeNode(line).SetSelectedTextStyle(selected))
	}
	section := func(name string) {
		add("")
		add(tag(colFaint) + "── " + name + " ──[-]")
	}

	// ── metadata ──
	agentLine := fmt.Sprintf("%s● [-]%s%s[-]", tag(agentColor(s.Agent)), tag(colText), tview.Escape(s.Agent))
	if s.Model != "" {
		agentLine += "  " + tag(colFaint) + tview.Escape(s.Model) + "[-]"
	}
	add(agentLine)
	if s.WorkingDir != "" {
		add(tag(colFaint) + "dir[-]      " + tag(colDim) + tview.Escape(s.WorkingDir) + "[-]")
	}
	add(tag(colFaint) + "updated[-]  " + tag(colDim) + relTime(s.UpdatedAt) + "[-]")

	var st git.State
	hasGit := s.GitState != "" && json.Unmarshal([]byte(s.GitState), &st) == nil && st.Commit != ""
	if hasGit && st.Remote != "" {
		add(tag(colFaint) + "remote[-]   " + tag(colDim) + tview.Escape(st.Remote) + "[-]")
	}

	// ── session commits ── every session gets this section so the layout is
	// stable across agents. Stored commits win; sessions imported by pull
	// have no stored git state, so reconstruct the window from the repo when
	// it still exists.
	if !hasGit || len(st.Commits) == 0 {
		if s.WorkingDir != "" && s.CreatedAt > 0 {
			st.Commits = git.CommitsBetween(s.WorkingDir, s.CreatedAt-300, s.UpdatedAt+300)
		}
	}
	section("session commits")
	if hasGit || len(st.Commits) > 0 {
		for _, l := range commitRailLines(&st) {
			add(l)
		}
	} else {
		add(tag(colFaint) + "no git activity recorded for this session[-]")
	}

	// ── timeline ── chapters fold on enter, events open in the sub reader.
	if chapters, err := timeline.Build(u.db, s); err == nil && len(chapters) > 0 {
		section("timeline")
		for i := range chapters {
			ch := &chapters[i]
			chNode := tview.NewTreeNode(eventLine(s.Agent, &ch.Prompt)).
				SetReference(&ch.Prompt).
				SetSelectedTextStyle(selected).
				SetExpanded(i == 0)
			for j := range ch.Events {
				chNode.AddChild(tview.NewTreeNode(eventLine(s.Agent, &ch.Events[j])).
					SetReference(&ch.Events[j]).
					SetSelectedTextStyle(selected))
			}
			root.AddChild(chNode)
		}
	}

	// ── decisions ── (the summary lives in the brief, behind the button)
	if strings.TrimSpace(s.Decisions) != "" {
		section("decisions")
		for _, line := range strings.Split(strings.TrimSpace(s.Decisions), "\n") {
			add(tag(colAccent) + "·[-] " + tag(colText) + tview.Escape(strings.TrimSpace(line)) + "[-]")
		}
	}

	// ── brief button ──
	add("")
	root.AddChild(tview.NewTreeNode(tag(colAccent) + "[::b]▶ view brief[::-]").
		SetReference("brief-button").
		SetSelectedTextStyle(tcell.StyleDefault.Foreground(colAccent).Background(colSurface).Bold(true)))

	u.tree = tview.NewTreeView()
	u.tree.SetRoot(root).
		SetTopLevel(1).
		SetGraphicsColor(colBorder)
	u.tree.SetBorderPadding(1, 1, 0, 0)
	if kids := root.GetChildren(); len(kids) > 0 {
		u.tree.SetCurrentNode(kids[0])
	}
	u.tree.SetSelectedFunc(func(node *tview.TreeNode) {
		switch ref := node.GetReference().(type) {
		case string:
			if ref == "brief-button" {
				u.openView("brief", s)
			}
		case *timeline.Event:
			if ref.Kind == timeline.Prompt && len(node.GetChildren()) > 0 {
				node.SetExpanded(!node.IsExpanded())
				return
			}
			if ref.Body != "" {
				u.openSub("read",
					fmt.Sprintf("%s · %s", timeline.Clock(ref.At), tview.Escape(oneLine(s.Title, 48))),
					tag(colText)+tview.Escape(ref.Body),
					"esc back · ↑↓ scroll")
			}
		}
	})

	title := tview.NewTextView()
	title.SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText("\n" + tag(colAccent) + "detail · " + tview.Escape(oneLine(s.Title, 60)) + "[-]")
	hints := tview.NewTextView()
	hints.SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText("[::b]" + tag(colDim) + "esc back · ↑↓ move · enter fold / open · y restore · h help")

	column := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(title, 2, 0, false).
		AddItem(u.tree, 0, 1, true).
		AddItem(hints, 1, 0, false)

	return tview.NewFlex().
		AddItem(tview.NewBox(), 0, 1, false).
		AddItem(column, readerWidth, 0, true).
		AddItem(tview.NewBox(), 0, 1, false)
}
