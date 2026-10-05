package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/export"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/markdown"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func entriesJSON(es []journal.Entry) []export.EntryJSON {
	out := make([]export.EntryJSON, len(es))
	for i, e := range es {
		out[i] = export.NewEntryJSON(e)
	}
	return out
}

// listOptions control the compact entry listing.
type listOptions struct {
	width    int
	snippets bool
}

// renderList prints entries grouped under day headings, one line each:
//
//	Mon 5 Oct 2026
//	  09:14  #12  AWS  Enabled IAM Access Analyzer   accomplishment #security
func renderList(w io.Writer, a *app.App, st style.Styler, es []journal.Entry, o listOptions) {
	idWidth := 2
	for _, e := range es {
		if n := len(e.Ref()); n > idWidth {
			idWidth = n
		}
	}
	projWidth := 0
	for _, e := range es {
		if n := lipgloss.Width(e.Project); n > projWidth {
			projWidth = n
		}
	}
	if projWidth > 16 {
		projWidth = 16
	}
	var day string
	for _, e := range es {
		t := e.OccurredAt.In(a.Loc)
		if d := t.Format("2006-01-02"); d != day {
			if day != "" {
				fmt.Fprintln(w)
			}
			day = d
			fmt.Fprintln(w, st.Heading(t.Format("Mon 2 Jan 2006")))
		}
		timeStr := t.Format(a.TimeLayout())
		prefix := "  " + st.Dim(padRight(timeStr, len(a.TimeLayout()))) + "  " + st.Faint(padLeft(e.Ref(), idWidth)) + "  "
		prefixWidth := 2 + len(a.TimeLayout()) + 2 + idWidth + 2
		var proj string
		if projWidth > 0 {
			name := truncate(e.Project, projWidth)
			proj = st.Project(padRight(name, projWidth)) + "  "
			prefixWidth += projWidth + 2
		}

		text := e.Title()
		highlighted := false
		if o.snippets && e.Snippet != "" {
			text = strings.Join(strings.Fields(e.Snippet), " ")
			highlighted = true
		} else if strings.Contains(e.Body, "\n") {
			text += " …"
		}
		meta := metaSuffix(st, e)
		metaWidth := lipgloss.Width(meta)

		if o.width > 0 {
			// Leave the last column free (writing into it makes some
			// terminals wrap) and count the two spaces before the metadata.
			usable := o.width - 1
			avail := usable - prefixWidth - metaWidth - 2
			if avail < 20 {
				meta = ""
				avail = usable - prefixWidth
			}
			if avail < 10 {
				avail = 10
			}
			text = truncate(text, avail)
		}
		if highlighted {
			text = renderHighlights(st, text)
		} else {
			text = stripMarkers(text)
		}
		line := prefix + proj + text
		if meta != "" {
			line += "  " + meta
		}
		fmt.Fprintln(w, line)
	}
}

func metaSuffix(st style.Styler, e journal.Entry) string {
	var parts []string
	if e.Type != "" {
		label := string(e.Type)
		parts = append(parts, st.Type(label))
	}
	if e.IsOpen() {
		parts = append(parts, st.Warn("open"))
	} else if e.Type.Opens() && e.ResolvedAt != nil {
		parts = append(parts, st.Dim("resolved"))
	}
	for _, t := range e.Tags {
		parts = append(parts, st.Tag("#"+t))
	}
	for _, m := range e.Marks {
		parts = append(parts, st.Accent("◇"+string(m)))
	}
	return strings.Join(parts, " ")
}

// renderHighlights turns FTS highlight markers into styling (or plain text).
func renderHighlights(st style.Styler, s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, journal.HighlightStart)
		if i < 0 {
			b.WriteString(strings.ReplaceAll(s, journal.HighlightEnd, ""))
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+len(journal.HighlightStart):]
		j := strings.Index(s, journal.HighlightEnd)
		if j < 0 {
			b.WriteString(st.Highlight(s))
			return b.String()
		}
		b.WriteString(st.Highlight(s[:j]))
		s = s[j+len(journal.HighlightEnd):]
	}
}

func stripMarkers(s string) string {
	return strings.NewReplacer(journal.HighlightStart, "", journal.HighlightEnd, "").Replace(s)
}

// truncate shortens s to at most n display cells, ending with an ellipsis.
// Highlight markers are zero-width and never split.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(stripMarkers(s)) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	open := false
	for _, r := range s {
		switch string(r) {
		case journal.HighlightStart:
			open = true
			b.WriteRune(r)
			continue
		case journal.HighlightEnd:
			open = false
			b.WriteRune(r)
			continue
		}
		rw := lipgloss.Width(string(r))
		if w+rw > n-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	if open {
		b.WriteString(journal.HighlightEnd)
	}
	return b.String() + "…"
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func padLeft(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

// renderDetail prints one entry in full.
// md, when non-nil, renders the body as Markdown for a terminal; otherwise
// the body is printed exactly as stored.
func renderDetail(w io.Writer, a *app.App, st style.Styler, e journal.Entry, md func(string) string) {
	// The full text appears once, first; the metadata follows it.
	fmt.Fprintf(w, "%s %s\n", st.Accent(style.Mark), st.Faint(e.Ref()))
	body := e.Body
	if md != nil {
		body = md(body)
	}
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintln(w, "  "+line)
	}
	fmt.Fprintln(w)
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(w, "  %s %s\n", st.Dim(padRight(label, 9)), value)
		}
	}
	t := e.OccurredAt.In(a.Loc)
	when := t.Format("Monday 2 January 2006, " + a.TimeLayout())
	if _, off := t.Zone(); off != e.UTCOffset {
		when += st.Dim(fmt.Sprintf("  (recorded as %s %s)", e.OccurredAt.In(e.RecordedOffset()).Format(a.TimeLayout()), offsetLabel(e.UTCOffset)))
	}
	row("When", when)
	row("Project", st.Project(e.Project))
	row("Type", st.Type(string(e.Type)))
	if len(e.Tags) > 0 {
		tags := make([]string, len(e.Tags))
		for i, tg := range e.Tags {
			tags[i] = st.Tag("#" + tg)
		}
		row("Tags", strings.Join(tags, " "))
	}
	if len(e.Marks) > 0 {
		ms := make([]string, len(e.Marks))
		for i, m := range e.Marks {
			ms[i] = string(m)
		}
		row("Marks", strings.Join(ms, ", "))
	}
	if e.Type.Opens() {
		if e.ResolvedAt != nil {
			row("Status", "resolved "+e.ResolvedAt.In(a.Loc).Format("Mon 2 Jan 2006 "+a.TimeLayout()))
		} else {
			row("Status", st.Warn("open"))
		}
	}
	if e.Source != nil {
		src := e.Source.Type + " " + e.Source.ID
		if e.Source.URL != "" {
			src += "  " + e.Source.URL
		}
		row("Source", src+st.Dim("  (imported "+e.Source.ImportedAt.In(a.Loc).Format("2006-01-02")+")"))
	}
	row("Recorded", st.Dim(fmt.Sprintf("created %s · modified %s · uid %s",
		e.CreatedAt.In(a.Loc).Format("2006-01-02 "+a.TimeLayout()), e.UpdatedAt.In(a.Loc).Format("2006-01-02 "+a.TimeLayout()), e.UID)))
}

func offsetLabel(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, sec/3600, (sec%3600)/60)
}

// summaryLine is the one-line confirmation printed after a change.
func summaryLine(a *app.App, st style.Styler, e journal.Entry) string {
	parts := []string{st.Faint(e.Ref()), st.Dim(e.OccurredAt.In(a.Loc).Format("Mon 2 Jan " + a.TimeLayout()))}
	if e.Project != "" {
		parts = append(parts, st.Project(e.Project))
	}
	parts = append(parts, truncate(e.Title(), 60))
	if m := metaSuffix(st, e); m != "" {
		parts = append(parts, m)
	}
	return strings.Join(parts, "  ")
}

func relativeDay(a *app.App, t time.Time) string {
	clock := a.Clock()
	d := clock.Midnight(t)
	switch days := int(clock.Today().Sub(d).Hours() / 24); {
	case days == 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days > 1 && days < 7:
		return t.In(a.Loc).Format("Monday")
	}
	return t.In(a.Loc).Format("2 Jan 2006")
}

// markdownRenderer returns a Markdown renderer for stdout when it is a
// colour-capable terminal, or nil so that piped output stays exactly as
// stored.
func (e *env) markdownRenderer() func(string) string {
	if !e.out().Enabled() || !e.io.OutTTY {
		return nil
	}
	width := e.io.Width
	if width <= 0 || width > 100 {
		width = min(max(width, 80), 100)
	}
	dark := true
	if e.io.InTTY {
		dark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	}
	return func(s string) string { return markdown.Render(s, width-2, dark) }
}
