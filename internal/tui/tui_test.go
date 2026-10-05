package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

var testNow = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC) // Wednesday

func newTestApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvData, filepath.Join(dir, "data"))
	t.Setenv(config.EnvConfig, "")
	t.Setenv(config.EnvDB, "")
	a, err := app.Open(context.Background(), app.Options{
		ConfigPath: writeEmptyConfig(t, dir), DBPath: filepath.Join(dir, "h.db"), UTC: true,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	ctx := context.Background()
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	for _, n := range []journal.NewEntry{
		{Body: "Enabled IAM Access Analyzer in all AWS regions", OccurredAt: at(5, 9), Project: "AWS", Type: journal.TypeAccomplishment, Tags: []string{"security"}},
		{Body: "Investigated unexpected exporter restarts", OccurredAt: at(5, 11), Project: "Infra", Type: journal.TypeInvestigation, Tags: []string{"monitoring"}},
		{Body: "Fixed CloudTrail bucket logging policy", OccurredAt: at(6, 10), Project: "AWS", Type: journal.TypeProblem},
		{Body: "Reviewed repository moderation queue", OccurredAt: at(6, 14), Project: "CCR"},
		{Body: "Decision: retain the existing deployment model for now", OccurredAt: at(7, 9), Project: "Infra", Type: journal.TypeDecision},
		{Body: "Follow up with engineering about ARM build capacity\nThey mentioned Q4 budget.", OccurredAt: at(7, 10), Type: journal.TypeFollowUp},
	} {
		n.CreateProject = true
		if _, err := a.Store.AddEntry(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func writeEmptyConfig(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "config.toml")
	if err := writeFile(p, ""); err != nil {
		t.Fatal(err)
	}
	return p
}

// run executes a command and feeds resulting messages back into the model,
// following batches, until nothing is left. Commands that do not finish
// promptly (none are expected) are abandoned.
func run(t testing.TB, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(2 * time.Second):
			continue
		}
		switch msg := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
		default:
			nm, next := m.Update(msg)
			m = nm.(Model)
			queue = append(queue, next)
		}
	}
	return m
}

func send(t testing.TB, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		nm, cmd := m.Update(msg)
		m = run(t, nm.(Model), cmd)
	}
	return m
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	if r >= 'A' && r <= 'Z' {
		return tea.KeyPressMsg{Code: r + 32, ShiftedCode: r, Text: s, Mod: tea.ModShift}
	}
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			m = send(t, m, key("space"))
			continue
		}
		m = send(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func start(t *testing.T, w, h int) Model {
	t.Helper()
	m := New(context.Background(), newTestApp(t))
	m = send(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	return run(t, m, m.Init())
}

func titles(m Model) []string {
	var out []string
	for _, e := range m.entries {
		out = append(out, e.Title())
	}
	return out
}

func checkFits(t *testing.T, m Model) {
	t.Helper()
	view := plain(m)
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Errorf("%dx%d: view has %d lines", m.width, m.height, len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("%dx%d: line %d is %d cells wide: %q", m.width, m.height, i, w, l)
		}
	}
}

func TestStartupShowsRecentActivity(t *testing.T) {
	m := start(t, 100, 30)
	if len(m.entries) != 6 || m.total != 6 {
		t.Fatalf("loaded %d of %d entries", len(m.entries), m.total)
	}
	view := plain(m)
	for _, want := range []string{"HOLOCRON", "this week", "6 entries", "Wed 7 Oct 2026", "Enabled IAM Access Analyzer", "a add"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n%s", want, view)
		}
	}
	if m.current().Title() != "Follow up with engineering about ARM build capacity" {
		t.Errorf("newest entry should be selected, got %q", m.current().Title())
	}
}

func TestResizeKeepsLayoutInBounds(t *testing.T) {
	m := start(t, 120, 40)
	sizes := [][2]int{{120, 40}, {160, 50}, {100, 30}, {80, 24}, {60, 20}, {45, 15}, {40, 12}, {30, 8}, {25, 6}, {10, 3}}
	for _, sz := range sizes {
		m = send(t, m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		checkFits(t, m)
		for _, k := range []string{"?", "esc", "enter", "esc", "p", "esc", "e", "esc", "esc", "/", "esc"} {
			m = send(t, m, key(k))
			checkFits(t, m)
		}
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 20, Height: 5})
	if !strings.Contains(plain(m), "Holocron needs") {
		t.Error("tiny terminal should explain the minimum size")
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 130, Height: 30})
	if !m.wide() || !strings.Contains(plain(m), "│") {
		t.Error("wide terminal should show the detail pane")
	}
}

func TestNavigationAndScrolling(t *testing.T) {
	m := start(t, 80, 10) // short: forces scrolling
	if m.cursor != 0 {
		t.Fatal("cursor should start at the top")
	}
	m = send(t, m, key("k"))
	if m.cursor != 0 {
		t.Fatal("moving above the first entry should stay put")
	}
	for i := 0; i < 10; i++ {
		m = send(t, m, key("j"))
	}
	if m.cursor != 5 {
		t.Fatalf("cursor = %d, want last (5)", m.cursor)
	}
	if !strings.Contains(plain(m), "Enabled IAM Access Analyzer") {
		t.Error("selected entry scrolled out of view")
	}
	m = send(t, m, key("up"), key("g"))
	if m.cursor != 0 {
		t.Fatalf("g should jump to the first entry, got %d", m.cursor)
	}
	m = send(t, m, key("G"))
	if m.cursor != 5 {
		t.Fatalf("G should jump to the last entry, got %d", m.cursor)
	}
	checkFits(t, m)
}

func TestEscapeIsPredictable(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("enter"))
	if m.screen != screenDetail || !strings.Contains(plain(m), "They mentioned Q4 budget.") {
		t.Fatal("enter should open the full entry")
	}
	m = send(t, m, key("esc"))
	if m.screen != screenList {
		t.Fatal("esc should leave the detail screen")
	}
	m = send(t, m, key("/"))
	m = typeText(t, m, "cloudtrail")
	if len(m.entries) != 1 {
		t.Fatalf("live search: %v", titles(m))
	}
	m = send(t, m, key("esc"))
	if m.layer != layerNone || m.f.search != "" || len(m.entries) != 6 {
		t.Fatal("esc in the search box should cancel the search")
	}
	m = send(t, m, key("/"))
	m = typeText(t, m, "cloudtrail")
	m = send(t, m, key("enter"))
	if m.f.search != "cloudtrail" || len(m.entries) != 1 {
		t.Fatal("enter should keep the search")
	}
	_, cmd := m.Update(key("esc"))
	if cmd == nil {
		t.Fatal("esc on a filtered list should reload with filters cleared")
	}
	m = send(t, m, key("esc"))
	if m.f.active() || len(m.entries) != 6 {
		t.Fatal("esc on the list should clear filters")
	}
	_, cmd = m.Update(key("esc"))
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("esc must never quit")
		}
	}
	if _, cmd = m.Update(key("q")); cmd == nil {
		t.Fatal("q should quit from the list")
	} else if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("q should quit from the list")
	}
	m = send(t, m, key("/"))
	if _, cmd = m.Update(key("q")); cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("typing q in the search box must not quit")
		}
	}
}

func TestAddEditDeleteUndo(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("a"))
	m = typeText(t, m, "Rotated deploy keys +AWS #security")
	m = send(t, m, key("enter"))
	if !strings.Contains(m.status, "Added #7") {
		t.Fatalf("status = %q", m.status)
	}
	c := m.current()
	if c == nil || c.Title() != "Rotated deploy keys" || c.Project != "AWS" || !c.HasTag("security") {
		t.Fatalf("new entry not selected or not parsed: %+v", c)
	}

	// Edit in the form: replace the text and set a type.
	m = send(t, m, key("e"))
	if m.layer != layerForm {
		t.Fatal("e should open the edit form")
	}
	m.form.body.SetValue("Rotated deploy keys for every AWS account")
	m = send(t, m, key("tab"), key("tab"), key("tab")) // text → when → project → type
	if m.form.focus != fieldType {
		t.Fatalf("focus = %d", m.form.focus)
	}
	m = typeText(t, m, "accomplishment")
	m = send(t, m, key("ctrl+s"))
	if m.layer != layerNone || !strings.Contains(m.status, "Updated #7") {
		t.Fatalf("save: layer %d status %q", m.layer, m.status)
	}
	if c := m.current(); c.Body != "Rotated deploy keys for every AWS account" || c.Type != journal.TypeAccomplishment {
		t.Fatalf("form edit not applied: %+v", c)
	}

	// Escape with unsaved changes asks first.
	m = send(t, m, key("e"))
	m.form.body.SetValue("Something else")
	m = send(t, m, key("esc"))
	if m.layer != layerForm {
		t.Fatal("first esc with unsaved changes should keep the form open")
	}
	m = send(t, m, key("esc"))
	if m.layer != layerNone || m.current().Body == "Something else" {
		t.Fatal("second esc should discard the edit")
	}

	// Delete asks for confirmation; n cancels, y deletes; u undoes.
	m = send(t, m, key("d"))
	if m.layer != layerConfirm || !strings.Contains(plain(m), "Delete #7?") {
		t.Fatal("d should ask before deleting")
	}
	m = send(t, m, key("n"))
	if len(m.entries) != 7 {
		t.Fatal("n must not delete")
	}
	m = send(t, m, key("d"), key("y"))
	if len(m.entries) != 6 || !strings.Contains(m.status, "undo") {
		t.Fatalf("delete: %d entries, status %q", len(m.entries), m.status)
	}
	m = send(t, m, key("u"))
	if len(m.entries) != 7 || !strings.Contains(m.status, "Restored #7") {
		t.Fatalf("undo: %d entries, status %q", len(m.entries), m.status)
	}
	if got, err := m.app.Store.Get(context.Background(), "7"); err != nil || got.Type != journal.TypeAccomplishment {
		t.Fatalf("undo lost data: %+v %v", got, err)
	}
}

func TestNewEntryForm(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("A"))
	if m.layer != layerForm || m.form.entry != nil {
		t.Fatal("A should open an empty form")
	}
	m = typeText(t, m, "Paired on Terraform modules")
	m = send(t, m, key("tab"), key("tab"))
	m = typeText(t, m, "Infra")
	m = send(t, m, key("tab"), key("tab"))
	m = typeText(t, m, "terraform, pairing")
	m = send(t, m, key("tab"))
	m = typeText(t, m, "staff")
	m = send(t, m, key("enter")) // enter on the last field saves
	c := m.current()
	if c == nil || c.Title() != "Paired on Terraform modules" || c.Project != "Infra" || !c.HasTag("pairing") || !c.HasMark(journal.MarkStaff) {
		t.Fatalf("form entry = %+v (status %q)", c, m.status)
	}
}

func TestFiltersAndRanges(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("p"))
	if m.layer != layerPicker {
		t.Fatal("p should open the project picker")
	}
	m = typeText(t, m, "inf")
	m = send(t, m, key("enter"))
	if m.f.project != "Infra" || len(m.entries) != 2 {
		t.Fatalf("project filter: %q %v", m.f.project, titles(m))
	}
	if !strings.Contains(plain(m), "project: Infra") {
		t.Error("active filter should be labelled in the header")
	}
	m = send(t, m, key("y"))
	m = typeText(t, m, "decision")
	m = send(t, m, key("enter"))
	if len(m.entries) != 1 {
		t.Fatalf("type filter: %v", titles(m))
	}
	m = send(t, m, key("esc"))
	if m.f.active() {
		t.Fatal("esc should clear filters")
	}
	m = send(t, m, key("t"), key("down"), key("enter"))
	if m.f.tag == "" || len(m.entries) != 1 {
		t.Fatalf("tag filter: %q %v", m.f.tag, titles(m))
	}
	m = send(t, m, key("esc"), key("o"))
	if len(m.entries) != 2 { // the open problem and the open follow-up
		t.Fatalf("open filter: %v", titles(m))
	}
	m = send(t, m, key("esc"))

	m = send(t, m, key("r"))
	m = typeText(t, m, "today")
	m = send(t, m, key("enter"))
	if m.f.rangeKey != "today" || len(m.entries) != 2 {
		t.Fatalf("range today: %q %v", m.f.rangeKey, titles(m))
	}
	m = send(t, m, key("["))
	if len(m.entries) != 2 || !strings.Contains(plain(m), "Tue 6 Oct 2026") {
		t.Fatalf("[ should step to the previous day: %v", titles(m))
	}
	m = send(t, m, key("]"), key("]"))
	if len(m.entries) != 0 || !strings.Contains(plain(m), "No entries in this range") {
		t.Fatalf("] twice should reach tomorrow (empty): %v", titles(m))
	}
}

func TestMarksAndResolve(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("m"))
	if m.layer != layerPicker || !m.picker.multi {
		t.Fatal("m should open the marks checklist")
	}
	m = send(t, m, key("space"), key("down"), key("space"), key("enter"))
	if got := m.current().Marks; !reflect.DeepEqual(got, []journal.Mark{journal.MarkOneOnOne, journal.MarkStaff}) {
		t.Fatalf("marks = %v", got)
	}
	if !m.current().IsOpen() {
		t.Fatal("follow-up should start open")
	}
	m = send(t, m, key("x"))
	if m.current().IsOpen() || !strings.Contains(m.status, "Resolved") {
		t.Fatalf("x should resolve: %q", m.status)
	}
	m = send(t, m, key("x"))
	if !m.current().IsOpen() {
		t.Fatal("x again should reopen")
	}
}

func TestReports(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("R"))
	if m.layer != layerPicker {
		t.Fatal("R should open the report menu")
	}
	m = typeText(t, m, "staff")
	m = send(t, m, key("enter"))
	if m.screen != screenReport || m.rep.Kind != "staff" {
		t.Fatalf("screen %d kind %q status %q", m.screen, m.rep.Kind, m.status)
	}
	view := plain(m)
	for _, want := range []string{"Staff update", "COMPLETED", "Enabled IAM Access Analyzer", "DECISIONS"} {
		if !strings.Contains(view, want) {
			t.Errorf("report view missing %q\n%s", want, view)
		}
	}
	m = send(t, m, key("i"))
	if !strings.Contains(plain(m), "#1") {
		t.Error("i should show entry ids")
	}
	m = send(t, m, key("tab"))
	if m.rep.Kind != "one-on-one" {
		t.Fatalf("tab should move to the next report, got %q", m.rep.Kind)
	}
	m = send(t, m, key("["))
	if !strings.Contains(m.rep.Title, "2026-09") {
		t.Errorf("[ should move the report back a period: %q", m.rep.Title)
	}

	path := filepath.Join(t.TempDir(), "r.md")
	m = send(t, m, key("s"))
	if m.layer != layerSavePath {
		t.Fatal("s should ask where to save")
	}
	m.input.SetValue(path)
	m = send(t, m, key("enter"))
	if !strings.Contains(m.status, "Saved report") {
		t.Fatalf("save status %q", m.status)
	}
	if b, err := readFile(path); err != nil || !strings.Contains(b, "# One-on-one") {
		t.Fatalf("saved report: %v %q", err, b)
	}
	checkFits(t, m)
	m = send(t, m, key("esc"))
	if m.screen != screenList {
		t.Fatal("esc should return from the report to the list")
	}
}

func TestHelpOverlay(t *testing.T) {
	m := start(t, 100, 40)
	m = send(t, m, key("?"))
	view := plain(m)
	for _, want := range []string{"Keys", "quick add", "back one level", "ctrl+c"} {
		if !strings.Contains(view, want) {
			t.Errorf("help missing %q", want)
		}
	}
	m = send(t, m, key("esc"))
	if m.layer != layerNone {
		t.Fatal("esc should close help")
	}
}

func TestCtrlCQuitsFromAnywhere(t *testing.T) {
	m := start(t, 100, 30)
	for _, open := range []string{"a", "e", "?", "p"} {
		mm := send(t, m, key(open))
		_, cmd := mm.Update(key("ctrl+c"))
		if cmd == nil {
			t.Fatalf("ctrl+c ignored after %q", open)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("ctrl+c did not quit after %q", open)
		}
	}
}

func TestSelectChecklist(t *testing.T) {
	s := selectModel{items: []SelectItem{{Label: "a", Selected: true}, {Label: "b", Selected: true}, {Label: "c"}}, width: 40, height: 10}
	step := func(k string) {
		nm, _ := s.Update(key(k))
		s = nm.(selectModel)
	}
	step("space") // deselect a
	step("j")
	step("j")
	step("space") // select c
	if s.count() != 2 || s.items[0].Selected || !s.items[2].Selected {
		t.Fatalf("selection = %+v", s.items)
	}
	step("a")
	if s.count() != 3 {
		t.Fatal("a should select all when some are unselected")
	}
	step("a")
	if s.count() != 0 {
		t.Fatal("a should clear when all are selected")
	}
	step("esc")
	if !s.cancelled {
		t.Fatal("esc should cancel")
	}
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o600) }

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

// plain renders the model without ANSI styling, for text assertions.
func plain(m Model) string { return ansi.Strip(m.render()) }

func queryText(s string) journal.Query { return journal.Query{Text: s} }

func TestDetailShowsTextOnce(t *testing.T) {
	m := start(t, 130, 40) // wide: detail pane beside the list
	m = send(t, m, key("a"))
	m = typeText(t, m, "A single paragraph entry that should only appear once")
	m = send(t, m, key("enter"))
	if n := strings.Count(plain(m), "should only appear once"); n != 1 {
		t.Fatalf("wide detail pane shows the text %d times", n)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 30}, key("enter"))
	if n := strings.Count(plain(m), "should only appear once"); n != 1 {
		t.Fatalf("detail screen shows the text %d times", n)
	}
}

func TestDetailRendersMarkdown(t *testing.T) {
	m := start(t, 130, 40)
	m = send(t, m, key("a"))
	m = typeText(t, m, "Use _markdown_ and **bold** for Vec<T>")
	m = send(t, m, key("enter"))
	view := plain(m)
	if !strings.Contains(view, "Use markdown and bold for Vec<T>") {
		t.Fatalf("detail pane did not render Markdown:\n%s", view)
	}
	if !strings.Contains(view, "Use _markdown_ and **bold**") {
		t.Fatal("the list row should still show the text as typed")
	}
	// A light terminal switches palettes without breaking the layout.
	m = send(t, m, tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if m.dark || !strings.Contains(plain(m), "Use markdown and bold") {
		t.Fatal("light background not applied")
	}
	checkFits(t, m)
}

func TestPagingLoadsMoreAndEndReachesLastEntry(t *testing.T) {
	m := start(t, 100, 30)
	var items []journal.NewEntry
	for i := 0; i < 1194; i++ {
		items = append(items, journal.NewEntry{Body: fmt.Sprintf("Old entry %d", i), OccurredAt: testNow.AddDate(0, 0, -30).Add(-time.Duration(i) * time.Hour)})
	}
	if _, err := m.app.Store.AddEntries(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	m = send(t, m, key("r"))
	m = typeText(t, m, "all")
	m = send(t, m, key("enter"))
	if len(m.entries) != pageSize || m.total != 1200 {
		t.Fatalf("first page: %d loaded of %d", len(m.entries), m.total)
	}
	if !strings.Contains(plain(m), "1200 entries") {
		t.Error("header should show the true total")
	}

	// Moving near the end of the loaded page fetches the next one.
	for i := 0; i < pageSize-20; i++ {
		m = send(t, m, key("j"))
	}
	if len(m.entries) != 2*pageSize {
		t.Fatalf("after scrolling near the end: %d loaded", len(m.entries))
	}

	// End jumps to the true last entry, loading whatever remains.
	m = send(t, m, key("G"))
	if len(m.entries) != 1200 || m.cursor != 1199 || m.current().Body != "Old entry 1193" {
		t.Fatalf("End: %d loaded, cursor %d, current %q", len(m.entries), m.cursor, m.current().Body)
	}
	seen := map[int64]bool{}
	for _, e := range m.entries {
		if seen[e.ID] {
			t.Fatalf("entry #%d listed twice", e.ID)
		}
		seen[e.ID] = true
	}
	checkFits(t, m)
}

func TestFilterChangeDuringPageLoadKeepsPaging(t *testing.T) {
	m := start(t, 100, 30)
	var items []journal.NewEntry
	for i := 0; i < 1100; i++ {
		items = append(items, journal.NewEntry{Body: fmt.Sprintf("Old entry %d", i), OccurredAt: testNow.AddDate(0, 0, -30).Add(-time.Duration(i) * time.Hour)})
	}
	if _, err := m.app.Store.AddEntries(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	m.setRangeKey("all")
	m = run(t, m, m.reload())
	// Start fetching a page but change the range before it arrives.
	stale := m.loadMore(pageSize, false)
	m.setRangeKey("all")
	m = run(t, m, m.reload())
	m = run(t, m, stale) // the old page arrives late and must be ignored
	if m.loadingMore || len(m.entries) != pageSize {
		t.Fatalf("stale page handling: loadingMore=%v loaded=%d", m.loadingMore, len(m.entries))
	}
	m = send(t, m, key("G"))
	if len(m.entries) != m.total {
		t.Fatalf("paging stuck after a filter change: %d of %d", len(m.entries), m.total)
	}
}
