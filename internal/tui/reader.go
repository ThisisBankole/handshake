package tui

import (
	"github.com/rivo/tview"
)

// readerWidth caps the reading column — full-bleed text on a wide terminal
// is hard to read; ~90 columns keeps line lengths comfortable.
const readerWidth = 92

// reader is a full-screen reading surface: a quiet title on top, a centered
// scrollable text column, and a one-line hint bar at the bottom. Both the
// view layer (detail/brief/match) and the sub layer (timeline events,
// restore confirm) reuse it.
type reader struct {
	root  tview.Primitive
	title *tview.TextView
	text  *tview.TextView
	hints *tview.TextView
}

func newReader() *reader {
	r := &reader{
		title: tview.NewTextView(),
		text:  tview.NewTextView(),
		hints: tview.NewTextView(),
	}
	r.title.SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	r.text.SetDynamicColors(true).SetWordWrap(true)
	r.text.SetBorderPadding(1, 1, 0, 0)
	r.hints.SetDynamicColors(true).SetTextAlign(tview.AlignCenter)

	column := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(r.title, 2, 0, false).
		AddItem(r.text, 0, 1, true).
		AddItem(r.hints, 1, 0, false)

	// Boxes (not nil spacers) in the gutters: they paint their background,
	// so the browser underneath doesn't bleed through the margins.
	r.root = tview.NewFlex().
		AddItem(tview.NewBox(), 0, 1, false).
		AddItem(column, readerWidth, 0, true).
		AddItem(tview.NewBox(), 0, 1, false)
	return r
}

// show fills the reader and scrolls to the top. title and hints get their
// standard styling here so callers pass plain-ish strings.
func (r *reader) show(title, body, hints string) {
	r.title.SetText("\n" + tag(colAccent) + title + "[-]")
	r.text.SetText(body)
	r.text.ScrollToBeginning()
	r.hints.SetText(tag(colFaint) + hints)
}
