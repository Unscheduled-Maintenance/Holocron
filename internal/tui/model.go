// Package tui is Holocron's interactive interface, built with Bubble Tea.
//
// It is a presentation layer only: every read and write goes through the
// same application services as the CLI (internal/app, internal/journal,
// internal/report). State transitions are plain Update calls, so they are
// tested without a terminal.
//
// Escape always means "back one level": it closes the open dialog or input;
// with none open it leaves the detail or report screen; on the main list it
// clears active filters. It never quits. q quits from the main list and
// ctrl+c quits from anywhere.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// screen is the full-screen view currently shown.
type screen int

const (
	screenList screen = iota
	screenDetail
	screenReport
)

// layer is a dialog or input shown on top of the current screen. Only one
// layer is open at a time, and Esc always closes it.
type layer int

const (
	layerNone layer = iota
	layerAdd
	layerSearch
	layerPicker
	layerConfirm
	layerForm
	layerHelp
	layerSavePath
)

// rangePresets are offered by the range picker and cycled with [ and ].
var rangePresets = []string{"today", "yesterday", "this-week", "last-week", "this-month", "last-month", "this-quarter", "last-quarter", "30d", "90d", "all"}

// filters narrow the main list.
type filters struct {
	rng      timerange.Range
	rangeKey string // preset expression, or "" for a stepped/custom range
	search   string
	project  string
	tag      string
	typ      journal.Type
	openOnly bool
}

func (f filters) active() bool {
	return f.search != "" || f.project != "" || f.tag != "" || f.typ != "" || f.openOnly
}

// Model is the root Bubble Tea model.
type Model struct {
	ctx context.Context
	app *app.App

	width, height int

	screen screen
	layer  layer

	f       filters
	entries []journal.Entry
	total   int // entries matching filters (may exceed len(entries) when capped)
	cursor  int
	offset  int // first visible list row
	loading bool
	seq     int // load sequence; stale results are dropped
	loadErr error

	input      textinput.Model // add, search and save-path inputs
	prevSearch string          // restored when a search is cancelled

	picker  picker
	confirm confirmDialog
	form    entryForm

	detailVP viewport.Model

	rep        report.Report
	repKind    report.Kind
	repIDs     bool
	repExplain bool
	repVP      viewport.Model

	status    string
	statusErr bool

	undo *journal.Entry // last deleted entry, restorable with u

	pendingSelect int64 // entry to select after the next load
	loadingMore   bool  // a further page is being fetched

	rowCache []row // day headings and entries for the list, built per load
	entryRow []int // row index of each entry in rowCache

	// dark is the terminal background, used to pick the Markdown palette.
	dark bool
}

// New creates the TUI model for an open application.
func New(ctx context.Context, a *app.App) Model {
	m := Model{ctx: ctx, app: a, width: 80, height: 24}
	key := a.Config.TUI.DefaultRange
	if key == "" {
		key = "this-week"
	}
	m.setRangeKey(key)
	m.input = newInput("", "")
	m.form.body = newTextArea()
	for i := fieldTime; i < fieldCount; i++ {
		m.form.fields[i] = newInput("", "")
	}
	m.detailVP = viewport.New()
	m.repVP = viewport.New()
	m.repKind = report.Staff
	m.seq, m.loading = 1, true
	m.dark = true // until the terminal reports its background
	return m
}

func (m *Model) setRangeKey(key string) {
	r, err := m.app.Clock().Parse(key)
	if err != nil {
		r, _ = m.app.Clock().Parse("this-week")
		key = "this-week"
	}
	m.f.rng, m.f.rangeKey = r, key
}

// Init loads the first page of entries. New has already marked the model as
// loading, because changes Init makes to its value receiver are discarded.
func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tea.RequestBackgroundColor) }

// Messages.
type (
	entriesMsg struct {
		seq     int
		entries []journal.Entry
		total   int
		err     error
		more    bool // a further page to append
		toEnd   bool // move the cursor to the last entry once appended
	}
	savedMsg struct {
		entry   journal.Entry
		verb    string
		created string
		err     error
	}
	deletedMsg struct {
		entry journal.Entry
		err   error
	}
	reportMsg struct {
		rep report.Report
		err error
	}
	pickerDataMsg struct {
		kind  pickerKind
		items []pickItem
		err   error
	}
	editorDoneMsg struct {
		entry *journal.Entry // nil for a new entry
		path  string
		err   error
	}
	statusMsg struct {
		text  string
		isErr bool
	}
)

// pageSize is how many entries are loaded at a time. More are fetched as the
// cursor nears the end of the list (and End/G fetches the rest), so opening
// the archive or typing a search never waits for thousands of rows. The
// header always shows the true total.
const pageSize = 500

// reload queries entries for the current filters.
func (m *Model) reload() tea.Cmd {
	m.seq++
	m.loading = true
	m.loadingMore = false // any page still in flight belongs to the old sequence
	return m.load()
}

// load returns a command that fetches entries for the current sequence.
func (m Model) load() tea.Cmd {
	seq, ctx, store, q := m.seq, m.ctx, m.app.Store, m.query()
	return func() tea.Msg {
		q.Limit = pageSize
		es, err := store.Find(ctx, q)
		if err != nil {
			return entriesMsg{seq: seq, err: err}
		}
		total := len(es)
		if total == pageSize {
			q.Limit = 0
			total, _ = store.Count(ctx, q)
		}
		return entriesMsg{seq: seq, entries: es, total: total}
	}
}

func (m Model) query() journal.Query {
	text, projects, tags := journal.SplitSearch(m.f.search)
	q := journal.Query{Text: text, Range: m.f.rng, OpenOnly: m.f.openOnly, Order: journal.NewestFirst}
	if m.f.project != "" {
		projects = append(projects, m.f.project)
	}
	if m.f.tag != "" {
		tags = append(tags, m.f.tag)
	}
	q.Projects, q.Tags = projects, tags
	if m.f.typ != "" {
		q.Types = []journal.Type{m.f.typ}
	}
	return q
}

// Update handles a message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.refreshDetail()
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.clampScroll()
		return m, nil

	case entriesMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		if msg.more {
			return m.appendPage(msg), nil
		}
		m.loading = false
		m.loadErr = msg.err
		if msg.err != nil {
			m.setStatus(errText(msg.err), true)
			return m, nil
		}
		selected := m.pendingSelect
		m.pendingSelect = 0
		if c := m.current(); c != nil && selected == 0 {
			selected = c.ID
		}
		m.entries, m.total = msg.entries, msg.total
		m.buildRows()
		m.cursor = 0
		for i, e := range m.entries {
			if e.ID == selected {
				m.cursor = i
			}
		}
		m.clampScroll()
		m.refreshDetail()
		return m, nil

	case savedMsg:
		if msg.err != nil {
			m.setStatus(errText(msg.err), true)
			return m, nil
		}
		text := fmt.Sprintf("%s %s", msg.verb, msg.entry.Ref())
		if msg.created != "" {
			text += fmt.Sprintf(" · created project %s", msg.created)
		}
		if !m.f.rng.Contains(msg.entry.OccurredAt) {
			text += " (outside the current range)"
		}
		m.setStatus(text, false)
		cmd := m.reload()
		m.selectID(msg.entry.ID)
		return m, cmd

	case deletedMsg:
		if msg.err != nil {
			m.setStatus(errText(msg.err), true)
			return m, nil
		}
		e := msg.entry
		m.undo = &e
		m.setStatus(fmt.Sprintf("Deleted %s · press u to undo", e.Ref()), false)
		if m.screen == screenDetail {
			m.screen = screenList
		}
		return m, m.reload()

	case reportMsg:
		if msg.err != nil {
			m.setStatus(errText(msg.err), true)
			return m, nil
		}
		m.rep = msg.rep
		m.screen = screenReport
		m.refreshReport()
		return m, nil

	case pickerDataMsg:
		if msg.err != nil {
			m.setStatus(errText(msg.err), true)
			return m, nil
		}
		m.picker = newPicker(msg.kind, msg.items, m.pickerCurrent(msg.kind))
		m.layer = layerPicker
		return m, nil

	case editorDoneMsg:
		return m.finishExternalEdit(msg)

	case statusMsg:
		m.setStatus(msg.text, msg.isErr)
		return m, nil

	case tea.PasteMsg:
		return m.updateLayerInput(msg)

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

// errText makes an error suitable for the one-line status bar.
func errText(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, journal.ErrInvalid.Error()+": ")
	s = strings.TrimPrefix(s, journal.ErrNotFound.Error()+": ")
	s = strings.TrimPrefix(s, journal.ErrConflict.Error()+": ")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (m Model) current() *journal.Entry {
	if m.cursor >= 0 && m.cursor < len(m.entries) {
		e := m.entries[m.cursor]
		return &e
	}
	return nil
}

// selectID moves the cursor to an entry, now or when the next load arrives.
func (m *Model) selectID(id int64) {
	m.pendingSelect = id
	for i, e := range m.entries {
		if e.ID == id {
			m.cursor = i
		}
	}
}

// handleKey routes a key press to the open layer, then the screen.
func (m Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.layer {
	case layerAdd, layerSearch, layerSavePath:
		return m.updateLayerInput(k)
	case layerPicker:
		return m.updatePicker(k)
	case layerConfirm:
		return m.updateConfirm(k)
	case layerForm:
		return m.updateForm(k)
	case layerHelp:
		switch k.String() {
		case "esc", "?", "q", "enter":
			m.layer = layerNone
		}
		return m, nil
	}
	switch m.screen {
	case screenDetail:
		return m.updateDetail(k)
	case screenReport:
		return m.updateReport(k)
	}
	return m.updateList(k)
}

func (m Model) updateList(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.f.active() {
			m.f.search, m.f.project, m.f.tag, m.f.typ, m.f.openOnly = "", "", "", "", false
			m.setStatus("Filters cleared", false)
			return m, m.reload()
		}
		m.setStatus("Nothing to clear · q quits", false)
		return m, nil
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
		return m, m.maybeLoadMore()
	case "pgup", "ctrl+u":
		m.move(-m.listHeight())
	case "pgdown", "ctrl+d", "space":
		m.move(m.listHeight())
		return m, m.maybeLoadMore()
	case "home", "g":
		m.move(-len(m.entries))
	case "end", "G":
		m.move(len(m.entries))
		if len(m.entries) < m.total {
			return m, m.loadMore(m.total-len(m.entries), true)
		}
	case "enter", "l", "right":
		if m.current() != nil {
			m.screen = screenDetail
			m.refreshDetail()
			m.detailVP.GotoTop()
		}
	case "a":
		return m.openAdd()
	case "A":
		return m.openForm(nil)
	case "e":
		if c := m.current(); c != nil {
			return m.openForm(c)
		}
	case "E":
		if c := m.current(); c != nil {
			return m.openExternalEditor(c)
		}
	case "d", "delete":
		if c := m.current(); c != nil {
			m.openDeleteConfirm(*c)
		}
	case "u":
		return m.undoDelete()
	case "m":
		if c := m.current(); c != nil {
			m.openMarks(*c)
		}
	case "x":
		if c := m.current(); c != nil {
			return m, m.toggleResolved(*c)
		}
	case "/":
		return m.openSearch()
	case "p":
		return m, m.loadPicker(pickProject)
	case "t":
		return m, m.loadPicker(pickTag)
	case "y":
		return m, m.loadPicker(pickType)
	case "r":
		return m, m.loadPicker(pickRange)
	case "o":
		m.f.openOnly = !m.f.openOnly
		if m.f.openOnly {
			m.setStatus("Showing only open problems and follow-ups", false)
		}
		return m, m.reload()
	case "[", "]":
		step := -1
		if k.String() == "]" {
			step = 1
		}
		if m.f.rng.IsZero() {
			m.setStatus("All time has no previous or next period", false)
			return m, nil
		}
		m.f.rng = m.app.Clock().Shift(m.f.rng, step)
		m.f.rangeKey = ""
		return m, m.reload()
	case "R":
		return m, m.loadPicker(pickReport)
	case "ctrl+r", "f5":
		m.setStatus("Reloaded", false)
		return m, m.reload()
	case "?", "f1":
		m.layer = layerHelp
	}
	return m, nil
}

func (m *Model) move(delta int) {
	if len(m.entries) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	m.clampScroll()
	m.refreshDetail()
}

func (m Model) updateDetail(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch k.String() {
	case "esc", "q", "h", "left", "backspace":
		m.screen = screenList
		return m, nil
	case "up", "k":
		m.detailVP.ScrollUp(1)
	case "down", "j":
		m.detailVP.ScrollDown(1)
	case "pgup":
		m.detailVP.PageUp()
	case "pgdown", "space":
		m.detailVP.PageDown()
	case "n", "J":
		m.move(1)
		m.detailVP.GotoTop()
		return m, m.maybeLoadMore()
	case "N", "K":
		m.move(-1)
		m.detailVP.GotoTop()
	case "?", "f1":
		m.layer = layerHelp
	default:
		// Entry actions work the same on the detail screen as on the list.
		switch k.String() {
		case "e", "E", "d", "delete", "m", "x", "a", "A":
			return m.updateList(k)
		}
	}
	return m, nil
}

func (m Model) undoDelete() (tea.Model, tea.Cmd) {
	if m.undo == nil {
		m.setStatus("Nothing to undo", false)
		return m, nil
	}
	e := *m.undo
	m.undo = nil
	ctx, store := m.ctx, m.app.Store
	return m, func() tea.Msg {
		restored, err := store.Restore(ctx, e)
		return savedMsg{entry: restored, verb: "Restored", err: err}
	}
}

func (m Model) toggleResolved(e journal.Entry) tea.Cmd {
	want := e.ResolvedAt == nil
	ctx, store := m.ctx, m.app.Store
	return func() tea.Msg {
		updated, err := store.Update(ctx, e.ID, journal.Patch{Resolved: &want})
		verb := "Resolved"
		if !want {
			verb = "Reopened"
		}
		return savedMsg{entry: updated, verb: verb, err: err}
	}
}

func (m Model) openAdd() (tea.Model, tea.Cmd) {
	m.layer = layerAdd
	m.input = newInput("› ", "What did you do? +project #tag shorthand works")
	m.input.SetWidth(max(10, m.width-4))
	return m, m.input.Focus()
}

func (m Model) openSearch() (tea.Model, tea.Cmd) {
	m.layer = layerSearch
	m.prevSearch = m.f.search
	m.input = newInput("/ ", "search words, \"phrase\", -exclude, +project, #tag")
	m.input.SetValue(m.f.search)
	m.input.CursorEnd()
	m.input.SetWidth(max(10, m.width-4))
	return m, m.input.Focus()
}

// updateLayerInput handles the single-line inputs (add, search, save path).
func (m Model) updateLayerInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.layer == layerForm {
		return m.updateForm(msg)
	}
	if m.layer != layerAdd && m.layer != layerSearch && m.layer != layerSavePath {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			if m.layer == layerSearch && m.f.search != m.prevSearch {
				m.f.search = m.prevSearch
				m.layer = layerNone
				return m, m.reload()
			}
			m.layer = layerNone
			return m, nil
		case "enter":
			return m.submitInput()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.layer == layerSearch && m.input.Value() != m.f.search {
		// Search as you type: queries are local and fast.
		m.f.search = m.input.Value()
		return m, tea.Batch(cmd, m.reload())
	}
	return m, cmd
}

func (m Model) submitInput() (tea.Model, tea.Cmd) {
	value := m.input.Value()
	switch m.layer {
	case layerAdd:
		if strings.TrimSpace(value) == "" {
			m.layer = layerNone
			return m, nil
		}
		m.layer = layerNone
		ctx, a := m.ctx, m.app
		return m, func() tea.Msg {
			res, err := a.Capture(ctx, app.CaptureInput{Text: value})
			return savedMsg{entry: res.Entry, verb: "Added", created: res.CreatedProject, err: err}
		}
	case layerSearch:
		m.layer = layerNone
		m.f.search = strings.TrimSpace(value)
		if m.f.search != "" {
			m.setStatus(fmt.Sprintf("%d matching · esc clears", m.total), false)
		}
		return m, m.reload()
	case layerSavePath:
		m.layer = layerNone
		return m, m.saveReport(value)
	}
	return m, nil
}

// --- external editor --------------------------------------------------------

func (m Model) openExternalEditor(e *journal.Entry) (tea.Model, tea.Cmd) {
	argv, err := resolveEditor(m.app)
	if err != nil {
		m.setStatus(errText(err), true)
		return m, nil
	}
	title := "New Holocron entry"
	if e != nil {
		title = fmt.Sprintf("Holocron entry %s (uid %s)", e.Ref(), e.UID)
	}
	path, err := tempDoc(m.app.DocFor(e).Format(title))
	if err != nil {
		m.setStatus(errText(err), true)
		return m, nil
	}
	cmd := editorCommand(m.ctx, argv, path)
	var target *journal.Entry
	if e != nil {
		c := *e
		target = &c
	}
	return m, execEditor(cmd, func(err error) tea.Msg {
		return editorDoneMsg{entry: target, path: path, err: err}
	})
}

func (m Model) finishExternalEdit(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	text, readErr := readDoc(msg.path)
	if msg.err != nil {
		m.setStatus("Editor failed: "+errText(msg.err), true)
		return m, nil
	}
	if readErr != nil {
		m.setStatus(errText(readErr), true)
		return m, nil
	}
	doc, err := app.ParseEntryDoc(text)
	if err != nil {
		m.setStatus(errText(err)+"; nothing saved", true)
		return m, nil
	}
	ctx, a := m.ctx, m.app
	if msg.entry == nil {
		return m, func() tea.Msg {
			res, err := a.CreateFromDoc(ctx, doc)
			if errors.Is(err, app.ErrEmptyEntry) {
				return statusMsg{text: "Empty entry; nothing saved"}
			}
			return savedMsg{entry: res.Entry, verb: "Added", created: res.CreatedProject, err: err}
		}
	}
	e := *msg.entry
	return m, func() tea.Msg {
		p, err := a.PatchFromDoc(e, doc)
		if errors.Is(err, app.ErrNoChanges) {
			return statusMsg{text: "No changes"}
		}
		if err != nil {
			return savedMsg{err: err}
		}
		updated, err := a.Store.Update(ctx, e.ID, p)
		return savedMsg{entry: updated, verb: "Updated", err: err}
	}
}

// Run starts the full-screen TUI.
func Run(ctx context.Context, a *app.App) error {
	p := tea.NewProgram(New(ctx, a), tea.WithContext(ctx))
	err := withUnambiguousKeys(func() error {
		_, err := p.Run()
		return err
	})
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// maybeLoadMore fetches the next page when the cursor nears the end of the
// loaded entries and more exist.
func (m *Model) maybeLoadMore() tea.Cmd {
	if m.loadingMore || len(m.entries) >= m.total || m.cursor < len(m.entries)-m.listHeight()*2 {
		return nil
	}
	return m.loadMore(pageSize, false)
}

// loadMore fetches up to limit further entries after those already loaded.
func (m *Model) loadMore(limit int, toEnd bool) tea.Cmd {
	if m.loadingMore && !toEnd {
		return nil
	}
	m.loadingMore = true
	seq, ctx, store, q := m.seq, m.ctx, m.app.Store, m.query()
	q.Limit, q.Offset = limit, len(m.entries)
	return func() tea.Msg {
		es, err := store.Find(ctx, q)
		return entriesMsg{seq: seq, entries: es, err: err, more: true, toEnd: toEnd}
	}
}

// appendPage adds a further page to the list, skipping any entry already
// shown (an entry added meanwhile can shift later pages by one).
func (m Model) appendPage(msg entriesMsg) Model {
	if msg.seq != m.seq {
		return m
	}
	m.loadingMore = false
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		return m
	}
	seen := make(map[int64]bool, len(m.entries))
	for _, e := range m.entries {
		seen[e.ID] = true
	}
	merged := make([]journal.Entry, len(m.entries), len(m.entries)+len(msg.entries))
	copy(merged, m.entries) // a new slice: models are copied by value
	for _, e := range msg.entries {
		if !seen[e.ID] {
			merged = append(merged, e)
		}
	}
	m.entries = merged
	if len(m.entries) > m.total {
		m.total = len(m.entries)
	}
	m.buildRows()
	if msg.toEnd {
		m.cursor = len(m.entries) - 1
	}
	m.clampScroll()
	m.refreshDetail()
	return m
}
