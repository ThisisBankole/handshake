package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Palette — soft accents drawn over the terminal's own background color so
// the browser blends with the user's light or dark theme (Catppuccin-inspired).
var (
	colText    = tcell.NewHexColor(0xCDD6F4)
	colDim     = tcell.NewHexColor(0x9399B2)
	colFaint   = tcell.NewHexColor(0x6C7086)
	colAccent  = tcell.NewHexColor(0xB4BEFE) // lavender
	colAccent2 = tcell.NewHexColor(0xCBA6F7) // mauve
	colSurface = tcell.NewHexColor(0x313244)
	colBorder  = tcell.NewHexColor(0x45475A)
	colGreen   = tcell.NewHexColor(0xA6E3A1)
	colYellow  = tcell.NewHexColor(0xF9E2AF)
	colTeal    = tcell.NewHexColor(0x94E2D5)
	colRed     = tcell.NewHexColor(0xF38BA8)
)

// agentColors gives each agent a stable accent so sessions are scannable at
// a glance; unknown agents fall back to lavender.
var agentColors = map[string]tcell.Color{
	"claude-code": colAccent2,
	"codex":       colTeal,
	"opencode":    colGreen,
	"hermes":      colYellow,
	"cursor":      colRed,
}

func agentColor(agent string) tcell.Color {
	if c, ok := agentColors[agent]; ok {
		return c
	}
	return colAccent
}

// tag returns a tview dynamic-color open tag for c, e.g. "[#b4befe]".
func tag(c tcell.Color) string {
	return fmt.Sprintf("[#%06x]", c.Hex())
}

// applyTheme must run before any primitive is created: tview copies Styles
// values at construction time.
func applyTheme() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.ContrastBackgroundColor = colSurface
	tview.Styles.MoreContrastBackgroundColor = colSurface
	tview.Styles.PrimaryTextColor = colText
	tview.Styles.SecondaryTextColor = colDim
	tview.Styles.TertiaryTextColor = colFaint
	tview.Styles.BorderColor = colBorder
	tview.Styles.TitleColor = colAccent
	tview.Styles.GraphicsColor = colBorder

	// Rounded corners everywhere. Focus runes match the unfocused set —
	// focus is signalled by border color (see focusColors) rather than
	// tview's default double-line border, which reads as harsh.
	tview.Borders.TopLeft = '╭'
	tview.Borders.TopRight = '╮'
	tview.Borders.BottomLeft = '╰'
	tview.Borders.BottomRight = '╯'
	tview.Borders.TopLeftFocus = '╭'
	tview.Borders.TopRightFocus = '╮'
	tview.Borders.BottomLeftFocus = '╰'
	tview.Borders.BottomRightFocus = '╯'
	tview.Borders.HorizontalFocus = '─'
	tview.Borders.VerticalFocus = '│'
}

// focusColors makes a pane's border glow accent while it has focus.
func focusColors(b *tview.Box) {
	b.SetFocusFunc(func() { b.SetBorderColor(colAccent) })
	b.SetBlurFunc(func() { b.SetBorderColor(colBorder) })
}

// gradient renders s with a per-rune color sweep from one hex color to
// another, e.g. gradient("handshake", 0xB4BEFE, 0xCBA6F7).
func gradient(s string, from, to int32) string {
	runes := []rune(s)
	steps := len(runes) - 1
	if steps < 1 {
		steps = 1
	}
	var b strings.Builder
	for i, r := range runes {
		t := float64(i) / float64(steps)
		lerp := func(shift int32) int32 {
			a, z := (from>>shift)&0xFF, (to>>shift)&0xFF
			return a + int32(t*float64(z-a))
		}
		fmt.Fprintf(&b, "[#%02x%02x%02x]%c", lerp(16), lerp(8), lerp(0), r)
	}
	b.WriteString("[-]")
	return b.String()
}

// relTime renders a unix timestamp as a compact relative age ("2h ago").
func relTime(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.Unix(unix, 0).Format("Jan 2 2006")
	}
}

// oneLine collapses whitespace and truncates s to max runes for list rows.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
