// Package timeline folds a session's raw transcript into a readable
// chronology: the user's prompts form chapters, consecutive tool activity
// collapses into counted runs, and commits from the session's working
// directory are interleaved by timestamp.
package timeline

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"handshake/internal/db"
	"handshake/internal/git"
)

type Kind int

const (
	Prompt Kind = iota // a real user message — opens a chapter
	Agent              // substantive assistant text
	Tools              // a collapsed run of tool activity
	CommitEvent
	Checkpoint
)

// Event is one row on the timeline.
type Event struct {
	Kind   Kind
	At     int64
	Text   string         // one-line summary shown on the timeline
	Body   string         // full content, shown when the event is opened
	Counts map[string]int // Tools only: tool name → call count
	Total  int            // Tools only: total calls in the run
}

// Chapter is a user prompt and everything that happened until the next one.
type Chapter struct {
	Prompt Event
	Events []Event
}

// toolMarker matches the "[tool: Name]" markers the adapters write into
// flattened message content.
var toolMarker = regexp.MustCompile(`\[tool: ([^\]]+)\]`)

// toolResults matches the "[N tool result(s)]" content of pure tool-result
// messages.
var toolResults = regexp.MustCompile(`^\[\d+ tool result\(s\)\]$`)

// scaffolding lists content prefixes that mark a user-role message as
// harness injection rather than something the human typed. Checked after
// TrimSpace. Tune against real sessions — each agent injects differently.
var scaffolding = []string{
	"# AGENTS.md",
	"<INSTRUCTIONS>",
	"<system-reminder>",
	"# claudeMd",
	"<command-name>",
	"<local-command",
	"Caveat: the messages below",
}

func isScaffolding(content string) bool {
	c := strings.TrimSpace(content)
	for _, prefix := range scaffolding {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Build assembles the timeline for a session from stored messages and, when
// the working directory still exists, commits that landed during the session.
func Build(database *db.Database, session *db.Session) ([]Chapter, error) {
	messages, err := database.GetMessages(session.ID)
	if err != nil {
		return nil, err
	}

	// Chapter zero collects anything that precedes the first real prompt.
	chapters := []Chapter{{Prompt: Event{
		Kind: Prompt,
		At:   session.CreatedAt,
		Text: "session start",
	}}}
	cur := func() *Chapter { return &chapters[len(chapters)-1] }

	// A run accumulates consecutive tool activity until flushed by
	// substantive text or a new prompt.
	var run *Event
	flushRun := func() {
		if run != nil && run.Total > 0 {
			cur().Events = append(cur().Events, *run)
		}
		run = nil
	}
	addToRun := func(at int64, name string, n int) {
		if run == nil {
			run = &Event{Kind: Tools, At: at, Counts: map[string]int{}}
		}
		run.Counts[name] += n
		run.Total += n
	}

	for _, m := range messages {
		switch m.Role {
		case "user":
			if isScaffolding(m.Content) {
				continue
			}
			flushRun()
			chapters = append(chapters, Chapter{Prompt: Event{
				Kind: Prompt,
				At:   m.CreatedAt,
				Text: firstLine(m.Content, 72),
				Body: m.Content,
			}})

		case "tool":
			// Pure tool messages either carry markers themselves or are
			// "[N tool result(s)]" rows; results confirm calls already
			// counted from assistant markers, so they don't add counts.
			if names := toolMarker.FindAllStringSubmatch(m.Content, -1); len(names) > 0 {
				for _, n := range names {
					addToRun(m.CreatedAt, n[1], 1)
				}
			} else if !toolResults.MatchString(strings.TrimSpace(m.Content)) {
				addToRun(m.CreatedAt, "tool", 1)
			}

		case "assistant":
			names := toolMarker.FindAllStringSubmatch(m.Content, -1)
			text := strings.TrimSpace(toolMarker.ReplaceAllString(m.Content, ""))
			// Text is narration that precedes the calls it announces, so it
			// flushes any prior run first; this message's own markers then
			// open a fresh run that merges with the tool messages after it.
			if text != "" {
				flushRun()
				cur().Events = append(cur().Events, Event{
					Kind: Agent,
					At:   m.CreatedAt,
					Text: firstLine(text, 72),
					Body: text,
				})
			}
			for _, n := range names {
				addToRun(m.CreatedAt, n[1], 1)
			}
		}
	}
	flushRun()

	// Interleave commits from the session's window (padded — checkpoint
	// time trails the last commit slightly).
	for _, c := range git.CommitsBetween(session.WorkingDir, session.CreatedAt-300, session.UpdatedAt+300) {
		ev := Event{
			Kind: CommitEvent,
			At:   c.When,
			Text: fmt.Sprintf("%s  %s", c.Hash, c.Subject),
		}
		ch := &chapters[chapterFor(chapters, c.When)]
		ch.Events = append(ch.Events, ev)
		sort.SliceStable(ch.Events, func(i, j int) bool { return ch.Events[i].At < ch.Events[j].At })
	}

	last := cur()
	last.Events = append(last.Events, Event{
		Kind: Checkpoint,
		At:   session.UpdatedAt,
		Text: "checkpoint",
	})

	// Drop the synthetic opening chapter if nothing landed in it.
	if len(chapters) > 1 && len(chapters[0].Events) == 0 {
		chapters = chapters[1:]
	}
	return chapters, nil
}

// chapterFor returns the index of the chapter whose window contains t.
func chapterFor(chapters []Chapter, t int64) int {
	idx := 0
	for i, ch := range chapters {
		if ch.Prompt.At <= t {
			idx = i
		}
	}
	return idx
}

// Line renders the event's one-line timeline text (without glyph or time),
// e.g. "12 tool calls — Bash ×6 · Edit ×4" or "codex  Found the bug…".
func (e Event) Line(agent string) string {
	switch e.Kind {
	case Tools:
		return summarizeCounts(e)
	case Agent:
		return agent + "  " + e.Text
	case CommitEvent:
		return "commit " + e.Text
	case Checkpoint:
		return "checkpoint"
	default:
		return "you  " + e.Text
	}
}

// Render formats chapters as plain text for `handshake timeline` output.
func Render(session *db.Session, chapters []Chapter) string {
	var b strings.Builder
	for _, ch := range chapters {
		fmt.Fprintf(&b, "◉ %s  %s\n", Clock(ch.Prompt.At), ch.Prompt.Line(session.Agent))
		for _, ev := range ch.Events {
			switch ev.Kind {
			case Tools:
				fmt.Fprintf(&b, "  ⚙ %s  %s\n", Clock(ev.At), ev.Line(session.Agent))
			case Agent:
				fmt.Fprintf(&b, "  ◆ %s  %s\n", Clock(ev.At), ev.Line(session.Agent))
			case CommitEvent:
				fmt.Fprintf(&b, "  ● %s  %s\n", Clock(ev.At), ev.Line(session.Agent))
			case Checkpoint:
				fmt.Fprintf(&b, "✔ %s  checkpoint\n", Clock(ev.At))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// summarizeCounts renders a tool run as "12 tool calls — Bash ×6 · Edit ×4",
// most-used first.
func summarizeCounts(ev Event) string {
	names := make([]string, 0, len(ev.Counts))
	for n := range ev.Counts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if ev.Counts[names[i]] != ev.Counts[names[j]] {
			return ev.Counts[names[i]] > ev.Counts[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s ×%d", n, ev.Counts[n])
	}
	call := "calls"
	if ev.Total == 1 {
		call = "call"
	}
	return fmt.Sprintf("%d tool %s — %s", ev.Total, call, strings.Join(parts, " · "))
}

// firstLine collapses content to its first line, truncated to max runes.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// Clock renders a timestamp as HH:MM for today's events, and adds the date
// for anything older so multi-day gaps stay legible.
func Clock(unix int64) string {
	t := time.Unix(unix, 0)
	if t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
