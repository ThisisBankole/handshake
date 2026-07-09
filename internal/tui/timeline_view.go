package tui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"handshake/internal/db"
	"handshake/internal/timeline"
)

// timelineView builds the full-screen timeline for a session: chapters as
// collapsible tree nodes, events as leaves. Enter on a chapter folds it;
// Enter on an event with content opens it in the sub reader.
func (u *ui) timelineView(s *db.Session, chapters []timeline.Chapter) tview.Primitive {
	// tview's default selected-node style inverts colors, which fights the
	// color tags in the row text (pale text on a pale bar). Instead keep
	// the tag colors and highlight with the dark surface background, same
	// as the session list.
	selected := tcell.StyleDefault.Background(colSurface).Bold(true)
	newNode := func(ev *timeline.Event, agent string) *tview.TreeNode {
		return tview.NewTreeNode(eventLine(agent, ev)).
			SetReference(ev).
			SetSelectedTextStyle(selected)
	}

	root := tview.NewTreeNode("")
	for i := range chapters {
		ch := &chapters[i]
		node := newNode(&ch.Prompt, s.Agent).SetExpanded(i == 0)
		for j := range ch.Events {
			node.AddChild(newNode(&ch.Events[j], s.Agent))
		}
		root.AddChild(node)
	}

	u.tree = tview.NewTreeView()
	u.tree.SetRoot(root).
		SetTopLevel(1).
		SetGraphicsColor(colBorder)
	u.tree.SetBorderPadding(1, 1, 0, 0)
	if len(root.GetChildren()) > 0 {
		u.tree.SetCurrentNode(root.GetChildren()[0])
	}
	u.tree.SetSelectedFunc(func(node *tview.TreeNode) {
		ev, ok := node.GetReference().(*timeline.Event)
		if !ok {
			return
		}
		if ev.Kind == timeline.Prompt && len(node.GetChildren()) > 0 {
			node.SetExpanded(!node.IsExpanded())
			return
		}
		if ev.Body != "" {
			u.openSub("read",
				fmt.Sprintf("%s · %s", timeline.Clock(ev.At), oneLine(s.Title, 48)),
				tag(colText)+tview.Escape(ev.Body),
				"esc back · ↑↓ scroll")
		}
	})

	title := tview.NewTextView()
	title.SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText("\n" + tag(colAccent) + "timeline · " + tview.Escape(oneLine(s.Title, 60)) + "[-]")
	hints := tview.NewTextView()
	hints.SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText("[::b]" + tag(colDim) + "esc back · ↑↓ move · enter open/fold · d detail · y restore")

	column := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(title, 2, 0, false).
		AddItem(u.tree, 0, 1, true).
		AddItem(hints, 1, 0, false)

	return tview.NewFlex().
		AddItem(tview.NewBox(), 0, 1, false).
		AddItem(column, readerWidth, 0, true).
		AddItem(tview.NewBox(), 0, 1, false)
}

// eventLine renders a timeline event as a colored tree row.
func eventLine(agent string, ev *timeline.Event) string {
	glyph, col := "◉", colGreen
	switch ev.Kind {
	case timeline.Agent:
		glyph, col = "◆", agentColor(agent)
	case timeline.Tools:
		glyph, col = "⚙", colTeal
	case timeline.CommitEvent:
		glyph, col = "●", colYellow
	case timeline.Checkpoint:
		glyph, col = "✔", colAccent
	}
	return fmt.Sprintf("%s%s %s[-]  %s%s[-]",
		tag(col), glyph, timeline.Clock(ev.At), tag(colText), tview.Escape(ev.Line(agent)))
}
