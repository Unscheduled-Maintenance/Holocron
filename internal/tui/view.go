package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/markdown"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

const (
	minWidth  = 30
	minHeight = 8
	wideWidth = 110 // at or above this width the list shows a detail pane
)

// compact reports whether the terminal is too short for the full chrome.
func (m Model) compact() bool { return m.height < 14 }

func (m Model) headerHeight() int {
	if m.compact() {
		return 1
	}
	return 3 // title, filter bar, rule
}

func (m Model) footerHeight() int {
	h := 1 // status / key hints
	if !m.compact() {
		h++ // rule
	}
	if m.layer == layerAdd || m.layer == layerSearch || m.layer == layerSavePath {
		h++
	}
	return h
}

func (m Model) bodyHeight() int { return max(1, m.height-m.headerHeight()-m.footerHeight()) }

func (m Model) wide() bool { return m.width >= wideWidth && m.screen == screenList }

func (m Model) listWidth() int {
	if m.wide() {
		return m.width * 55 / 100
	}
	return m.width
}

// layout resizes the viewports and form after a size change.
func (m *Model) layout() {
	w := m.width
	if m.wide() {
		w = m.width - m.listWidth() - 3
	}
	m.detailVP.SetWidth(max(10, w))
	m.detailVP.SetHeight(m.bodyHeight())
	m.repVP.SetWidth(max(10, m.width))
	m.repVP.SetHeight(m.bodyHeight())
	m.input.SetWidth(max(10, m.width-6))
	formW := min(m.width-4, 100)
	m.form.body.SetWidth(max(10, formW-12))
	m.form.body.SetHeight(max(2, min(10, m.bodyHeight()-10)))
	for i := fieldTime; i < fieldCount; i++ {
		m.form.fields[i].SetWidth(max(10, formW-14))
	}
	m.refreshDetail()
	m.refreshReport()
}

// --- list rows -------------------------------------------------------------

type row struct {
	day   string // non-empty for a day heading
	entry int    // index into m.entries for entry rows
}

// rows returns the list rows (day headings and entries) for the loaded
// entries. They are built once per load by buildRows rather than on every
// frame and cursor move.
func (m Model) rows() []row { return m.rowCache }

// buildRows groups the loaded entries under day headings and records which
// row each entry occupies.
func (m *Model) buildRows() {
	m.rowCache = make([]row, 0, len(m.entries)+len(m.entries)/4+1) // fresh: models are copied by value
	m.entryRow = make([]int, len(m.entries))
	var day string
	for i, e := range m.entries {
		d := e.OccurredAt.In(m.app.Loc).Format("Mon 2 Jan 2006")
		if d != day {
			day = d
			m.rowCache = append(m.rowCache, row{day: d, entry: -1})
		}
		m.entryRow[i] = len(m.rowCache)
		m.rowCache = append(m.rowCache, row{entry: i})
	}
}

func (m Model) listHeight() int { return m.bodyHeight() }

// cursorRow returns the row index of the selected entry.
func (m Model) cursorRow() int {
	if m.cursor >= 0 && m.cursor < len(m.entryRow) {
		return m.entryRow[m.cursor]
	}
	return 0
}

// clampScroll keeps the selected entry (and its day heading) visible.
func (m *Model) clampScroll() {
	rows := m.rows()
	h := m.listHeight()
	cr := m.cursorRow()
	if cr-1 < m.offset { // keep the heading above the first entry of a day visible
		m.offset = max(0, cr-1)
		if cr > 0 && rows[cr-1].day == "" {
			m.offset = cr
		}
	}
	if cr >= m.offset+h {
		m.offset = cr - h + 1
	}
	if maxOff := max(0, len(rows)-h); m.offset > maxOff {
		m.offset = maxOff
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// --- rendering ----------------------------------------------------------------

// View renders the interface.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "Holocron"
	return v
}

func (m Model) render() string {
	if m.width < minWidth || m.height < minHeight {
		msg := fmt.Sprintf("Holocron needs at least %d×%d (now %d×%d). q quits.", minWidth, minHeight, m.width, m.height)
		w, h := max(1, m.width), max(1, m.height)
		return fitBlock(lipgloss.NewStyle().Width(w).Render(msg), w, h)
	}
	var b strings.Builder
	b.WriteString(m.renderHeader())
	body := m.renderBody()
	b.WriteString(fitBlock(body, m.width, m.bodyHeight()))
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderHeader() string {
	title := sMark.Render(style.Mark) + " " + sTitle.Render("HOLOCRON")
	if m.width >= 60 {
		title += sDim.Render(" // personal work archive")
	}
	date := sDim.Render(m.app.Now().Format("Mon 2 Jan 2006"))
	line1 := spread(title, date, m.width)
	if m.compact() {
		return line1 + "\n"
	}
	return line1 + "\n" + m.filterBar() + "\n" + sRule.Render(strings.Repeat("─", m.width)) + "\n"
}

// filterBar shows the active range and filters as labelled chips, so state
// never depends on colour alone.
func (m Model) filterBar() string {
	var chips []string
	switch m.screen {
	case screenReport:
		chips = append(chips, sChip.Render("["+m.rep.Title+"]"))
		if m.repIDs {
			chips = append(chips, sDim.Render("ids on"))
		}
		if m.repExplain {
			chips = append(chips, sDim.Render("reasons on"))
		}
	default:
		label := m.f.rng.Label
		if label == "" {
			label = m.app.Clock().Describe(m.f.rng)
		}
		chips = append(chips, sChip.Render("["+label+"]"))
		if m.f.search != "" {
			chips = append(chips, "search: "+sHighlight.Render(m.f.search))
		}
		if m.f.project != "" {
			name := m.f.project
			if name == "-" {
				name = "none"
			}
			chips = append(chips, "project: "+sProject.Render(name))
		}
		if m.f.tag != "" {
			chips = append(chips, "tag: "+sTag.Render("#"+m.f.tag))
		}
		if m.f.typ != "" {
			chips = append(chips, "type: "+typeStyle(string(m.f.typ)).Render(string(m.f.typ)))
		}
		if m.f.openOnly {
			chips = append(chips, sWarn.Render("open only"))
		}
	}
	left := " " + strings.Join(chips, sFaint.Render(" · "))
	right := ""
	if m.screen != screenReport {
		switch {
		case m.loading && len(m.entries) == 0:
			right = sDim.Render("loading…")
		default:
			right = sDim.Render(fmt.Sprintf("%d %s ", m.total, plural(m.total, "entry", "entries")))
		}
	}
	return spread(truncateANSI(left, m.width-lipgloss.Width(right)-1), right, m.width)
}

func (m Model) renderBody() string {
	switch m.layer {
	case layerHelp:
		return m.renderHelp()
	case layerPicker:
		return m.renderPicker()
	case layerConfirm:
		return m.renderConfirm()
	case layerForm:
		return m.renderForm()
	}
	switch m.screen {
	case screenDetail:
		return m.detailVP.View()
	case screenReport:
		return m.repVP.View()
	}
	list := m.renderList(m.listWidth())
	if !m.wide() {
		return list
	}
	sep := sRule.Render(strings.Repeat("│\n", m.bodyHeight()-1) + "│")
	return lipgloss.JoinHorizontal(lipgloss.Top, fitBlock(list, m.listWidth(), m.bodyHeight()), " ", sep, " ", m.detailVP.View())
}

func (m Model) renderList(width int) string {
	if len(m.entries) == 0 {
		if m.loading {
			return ""
		}
		msg := "No entries in this range."
		if m.f.active() {
			msg = "Nothing matches these filters. Esc clears them."
		}
		hint := "Press a to add an entry, r to change the range, ? for help."
		return "\n  " + sDim.Render(msg) + "\n  " + sFaint.Render(hint)
	}
	rows := m.rows()
	h := m.listHeight()
	end := min(len(rows), m.offset+h)
	var lines []string
	for _, r := range rows[m.offset:end] {
		if r.day != "" {
			lines = append(lines, " "+sDay.Render(r.day))
			continue
		}
		lines = append(lines, m.renderRow(m.entries[r.entry], r.entry == m.cursor, width))
	}
	// Scroll indicators.
	if m.offset > 0 && len(lines) > 0 {
		lines[0] = spread(lines[0], sFaint.Render("↑ "), width)
	}
	if end < len(rows) && len(lines) > 0 {
		lines[len(lines)-1] = spread(lines[len(lines)-1], sFaint.Render("↓ "), width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderRow(e journal.Entry, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = sCursor.Render("▌ ")
	}
	timeStr := e.OccurredAt.In(m.app.Loc).Format(m.app.TimeLayout())
	idStr := e.Ref()
	left := marker + sDim.Render(timeStr) + " " + sFaint.Render(fmt.Sprintf("%5s", idStr)) + " "
	used := 2 + len(timeStr) + 1 + max(5, len(idStr)) + 1

	if width >= 60 && e.Project != "" {
		pw := min(14, max(6, width/7))
		left += sProject.Render(pad(truncate(e.Project, pw), pw)) + " "
		used += pw + 1
	} else if width >= 60 {
		pw := min(14, max(6, width/7))
		left += strings.Repeat(" ", pw+1)
		used += pw + 1
	}

	var meta []string
	if width >= 70 {
		if e.Type != "" {
			meta = append(meta, typeStyle(string(e.Type)).Render(string(e.Type)))
		}
		if e.IsOpen() {
			meta = append(meta, sWarn.Render("open"))
		}
		if len(e.Marks) > 0 {
			meta = append(meta, sMark.Render("◇"+fmt.Sprint(len(e.Marks))))
		}
	} else if e.IsOpen() {
		meta = append(meta, sWarn.Render("○"))
	}
	metaStr := strings.Join(meta, " ")
	metaW := lipgloss.Width(metaStr)

	avail := width - used - metaW - 2
	if avail < 8 {
		metaStr = ""
		avail = width - used - 1
	}
	text := e.Title()
	if m.f.search != "" && e.Snippet != "" {
		text = strings.Join(strings.Fields(e.Snippet), " ")
	} else if strings.Contains(e.Body, "\n") {
		text += " …"
	}
	text = truncate(text, max(1, avail))
	rendered := renderMarkers(text)
	if selected {
		rendered = sSelected.Render(stripMarkers(text))
		if strings.Contains(text, journal.HighlightStart) {
			rendered = renderMarkers(text)
		}
	}
	line := left + rendered
	if metaStr != "" {
		line = spread(line, metaStr+" ", width)
	}
	return line
}

func (m *Model) refreshDetail() {
	c := m.current()
	if c == nil || m.app == nil {
		m.detailVP.SetContent("")
		return
	}
	m.detailVP.SetContent(m.detailText(*c, max(10, m.detailVP.Width())))
}

func (m Model) detailText(e journal.Entry, width int) string {
	var b strings.Builder
	b.WriteString(sMark.Render(style.Mark) + " " + sFaint.Render(e.Ref()) + "\n")
	wrap := lipgloss.NewStyle().Width(width)
	// The full text appears once, first; the metadata follows it.
	b.WriteString(markdown.Render(e.Body, width, m.dark) + "\n\n")
	row := func(label, value string) {
		if value != "" {
			b.WriteString(sDim.Render(pad(label, 9)) + " " + value + "\n")
		}
	}
	t := e.OccurredAt.In(m.app.Loc)
	row("When", t.Format("Mon 2 Jan 2006 "+m.app.TimeLayout()))
	if e.Project != "" {
		row("Project", sProject.Render(e.Project))
	}
	if e.Type != "" {
		row("Type", typeStyle(string(e.Type)).Render(string(e.Type)))
	}
	if e.Type.Opens() {
		if e.ResolvedAt != nil {
			row("Status", "resolved "+e.ResolvedAt.In(m.app.Loc).Format("2 Jan 2006"))
		} else {
			row("Status", sWarn.Render("open")+sFaint.Render("  (x resolves)"))
		}
	}
	if len(e.Tags) > 0 {
		tags := make([]string, len(e.Tags))
		for i, tg := range e.Tags {
			tags[i] = sTag.Render("#" + tg)
		}
		row("Tags", wrap.Width(max(10, width-10)).Render(strings.Join(tags, " ")))
	}
	if len(e.Marks) > 0 {
		ms := make([]string, len(e.Marks))
		for i, mk := range e.Marks {
			ms[i] = string(mk)
		}
		row("Marks", strings.Join(ms, ", "))
	}
	if e.Source != nil {
		row("Source", e.Source.Type+" "+truncate(e.Source.ID, 12))
		if e.Source.URL != "" {
			row("", sFaint.Render(truncate(e.Source.URL, max(10, width-10))))
		}
	}
	b.WriteString("\n" + sFaint.Render(wrap.Render(fmt.Sprintf("created %s · modified %s",
		e.CreatedAt.In(m.app.Loc).Format("2006-01-02 "+m.app.TimeLayout()),
		e.UpdatedAt.In(m.app.Loc).Format("2006-01-02 "+m.app.TimeLayout())))))
	return b.String()
}

func (m *Model) refreshReport() {
	if m.rep.Kind == "" || m.app == nil {
		return
	}
	var b strings.Builder
	_ = report.Text(&b, m.rep, report.RenderOptions{ShowIDs: m.repIDs, Explain: m.repExplain, Loc: m.app.Loc,
		TimeLayout: m.app.TimeLayout(), Styler: style.New(true)})
	m.repVP.SetContent(lipgloss.NewStyle().Width(max(10, m.repVP.Width()-1)).Render(b.String()))
}

func (m Model) renderFooter() string {
	var b strings.Builder
	if !m.compact() {
		b.WriteString(sRule.Render(strings.Repeat("─", m.width)) + "\n")
	}
	if m.layer == layerAdd || m.layer == layerSearch || m.layer == layerSavePath {
		b.WriteString(" " + m.input.View() + "\n")
	}
	if m.status != "" {
		st := sSuccess
		if m.statusErr {
			st = sError
		}
		b.WriteString(" " + st.Render(truncate(m.status, m.width-2)))
		return b.String()
	}
	b.WriteString(" " + fitHints(m.keyHints(), m.width-2))
	return b.String()
}

func hint(key, label string) string { return sKey.Render(key) + " " + sDim.Render(label) }

func (m Model) keyHints() []string {
	var hs []string
	switch m.layer {
	case layerAdd:
		hs = []string{hint("enter", "save"), hint("esc", "cancel"), hint("+project #tag", "shorthand")}
	case layerSearch:
		hs = []string{hint("enter", "keep results"), hint("esc", "cancel search")}
	case layerSavePath:
		hs = []string{hint("enter", "save Markdown"), hint("esc", "cancel")}
	case layerPicker:
		if m.picker.multi {
			hs = []string{hint("space", "toggle"), hint("enter", "apply"), hint("esc", "cancel")}
		} else {
			hs = []string{hint("↑↓", "move"), hint("type", "filter"), hint("enter", "choose"), hint("esc", "cancel")}
		}
	case layerConfirm:
		hs = []string{hint("y", m.confirm.action), hint("n/esc", "cancel")}
	case layerForm:
		hs = []string{hint("ctrl+s", "save"), hint("tab", "next field"), hint("esc", "cancel")}
	case layerHelp:
		hs = []string{hint("esc", "close help")}
	default:
		switch m.screen {
		case screenDetail:
			hs = []string{hint("esc", "back"), hint("e", "edit"), hint("m", "marks"), hint("d", "delete"), hint("n/N", "next/prev"), hint("?", "help")}
		case screenReport:
			hs = []string{hint("esc", "back"), hint("tab", "next report"), hint("[ ]", "period"), hint("i", "ids"), hint("w", "why"), hint("s", "save"), hint("?", "help")}
		default:
			hs = []string{hint("a", "add"), hint("/", "search"), hint("enter", "open"), hint("e", "edit"), hint("p t y", "filter"),
				hint("r [ ]", "range"), hint("R", "reports")}
			if m.f.active() {
				hs = append(hs, hint("esc", "clear filters"))
			}
			hs = append(hs, hint("q", "quit"), hint("?", "help"))
		}
	}
	return hs
}

func (m Model) renderHelp() string {
	sections := []struct {
		title string
		keys  [][2]string
	}{
		{"Move", [][2]string{{"↑ ↓  j k", "select entry"}, {"pgup pgdn", "page"}, {"home end  g G", "first / last"}, {"enter", "open entry"}}},
		{"Entries", [][2]string{{"a", "quick add (+project #tag)"}, {"A", "add with all fields"}, {"e", "edit in a form"}, {"E", "edit in $EDITOR"},
			{"d", "delete (asks first)"}, {"u", "undo last delete"}, {"m", "report marks"}, {"x", "resolve / reopen"}}},
		{"Find", [][2]string{{"/", "search as you type"}, {"p", "filter by project"}, {"t", "filter by tag"}, {"y", "filter by type"},
			{"o", "open items only"}, {"r", "choose date range"}, {"[ ]", "previous / next period"}, {"ctrl+r", "reload"}}},
		{"Reports", [][2]string{{"R", "generate a report"}, {"tab", "next report kind"}, {"i", "show entry ids"}, {"w", "show why items appear"}, {"s", "save as Markdown"}}},
		{"Leave", [][2]string{{"esc", "back one level: close dialog, leave screen, clear filters"}, {"q", "quit (from the list)"}, {"ctrl+c", "quit from anywhere"}}},
	}
	var cols []string
	for _, s := range sections {
		var b strings.Builder
		b.WriteString(sHeading.Render(s.title) + "\n")
		for _, k := range s.keys {
			b.WriteString(sKey.Render(pad(k[0], 14)) + " " + sDim.Render(k[1]) + "\n")
		}
		cols = append(cols, b.String())
	}
	var body string
	if m.width >= 100 {
		left := strings.Join(cols[:2], "\n")
		right := strings.Join(cols[2:], "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right)
	} else {
		body = strings.Join(cols, "\n")
	}
	return sBox.Render(sTitle.Render("Keys") + "\n\n" + body)
}

func (m Model) renderPicker() string {
	p := m.picker
	vis := p.visible()
	maxRows := max(3, m.bodyHeight()-6)
	start := 0
	if p.cursor >= maxRows {
		start = p.cursor - maxRows + 1
	}
	var b strings.Builder
	b.WriteString(sTitle.Render(p.title) + "\n")
	if !p.multi {
		filter := p.filter
		if filter == "" {
			filter = sFaint.Render("type to filter")
		}
		b.WriteString(sDim.Render("filter: ") + filter + "\n")
	}
	b.WriteString("\n")
	if len(vis) == 0 {
		b.WriteString(sDim.Render("  no matches") + "\n")
	}
	innerW := min(m.width-6, 70)
	for i := start; i < len(vis) && i < start+maxRows; i++ {
		it := p.items[vis[i]]
		marker := "  "
		if i == p.cursor {
			marker = sCursor.Render("▌ ")
		}
		label := it.label
		if p.multi {
			box := "[ ] "
			if it.checked {
				box = "[x] "
			}
			label = box + label
		}
		if i == p.cursor {
			label = sSelected.Render(label)
		}
		line := marker + label
		if it.detail != "" {
			line += "  " + sFaint.Render(it.detail)
		}
		b.WriteString(truncateANSI(line, innerW) + "\n")
	}
	return sFocusBox.Render(strings.TrimRight(b.String(), "\n"))
}

func (m Model) renderConfirm() string {
	body := sTitle.Render(m.confirm.title) + "\n\n" + lipgloss.NewStyle().Width(min(60, m.width-6)).Render(m.confirm.body) +
		"\n\n" + hint("y", m.confirm.action) + "   " + hint("n / esc", "cancel")
	return sFocusBox.Render(body)
}

func (m Model) renderForm() string {
	f := m.form
	title := "New entry"
	if f.entry != nil {
		title = "Edit " + f.entry.Ref()
	}
	var b strings.Builder
	b.WriteString(sTitle.Render(title) + "\n\n")
	label := func(i int) string {
		l := pad(fieldLabels[i], 8)
		if f.focus == i {
			return sCursor.Render("▸ " + l)
		}
		return sDim.Render("  " + l)
	}
	b.WriteString(label(fieldBody) + "\n" + f.body.View() + "\n\n")
	for i := fieldTime; i < fieldCount; i++ {
		b.WriteString(label(i) + " " + f.fields[i].View() + "\n")
	}
	return sFocusBox.Render(strings.TrimRight(b.String(), "\n"))
}

// --- text helpers -------------------------------------------------------------

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// spread places left and right on one line of the given width.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncateANSI(left, max(0, width-lipgloss.Width(right)-1)) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// truncate shortens plain text (which may contain highlight markers) to n cells.
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

// truncateANSI shortens already-styled text to n cells.
func truncateANSI(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(n).Render(s)
}

func stripMarkers(s string) string {
	return strings.NewReplacer(journal.HighlightStart, "", journal.HighlightEnd, "").Replace(s)
}

func renderMarkers(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, journal.HighlightStart)
		if i < 0 {
			b.WriteString(strings.ReplaceAll(s, journal.HighlightEnd, ""))
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+1:]
		j := strings.Index(s, journal.HighlightEnd)
		if j < 0 {
			b.WriteString(sHighlight.Render(s))
			return b.String()
		}
		b.WriteString(sHighlight.Render(s[:j]))
		s = s[j+1:]
	}
}

// fitBlock pads or crops a block to exactly width×height cells so the
// footer never moves and nothing wraps past the terminal edge.
func fitBlock(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			l = truncateANSI(l, width)
		}
		lines[i] = l
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// fitHints keeps as many whole key hints as fit in width, always keeping
// the last one (help or quit) so the way to discover more is never lost.
func fitHints(hs []string, width int) string {
	if len(hs) == 0 {
		return ""
	}
	sep := "  "
	last := hs[len(hs)-1]
	out := ""
	for _, h := range hs[:len(hs)-1] {
		cand := out
		if cand != "" {
			cand += sep
		}
		cand += h
		if lipgloss.Width(cand+sep+last) > width {
			break
		}
		out = cand
	}
	if out != "" {
		out += sep
	}
	return truncateANSI(out+last, width)
}
