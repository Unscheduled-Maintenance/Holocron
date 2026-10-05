package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// --- pickers ----------------------------------------------------------------

type pickerKind int

const (
	pickProject pickerKind = iota
	pickTag
	pickType
	pickRange
	pickReport
	pickMarks
)

type pickItem struct {
	label   string
	value   string
	detail  string
	checked bool
}

// picker is a list chooser with type-to-filter (single choice) or
// space-to-toggle (multiple choice, used for marks).
type picker struct {
	kind   pickerKind
	title  string
	items  []pickItem
	cursor int
	multi  bool
	filter string
	target int64 // entry being marked
}

func (p picker) visible() []int {
	var out []int
	f := strings.ToLower(p.filter)
	for i, it := range p.items {
		if f == "" || strings.Contains(strings.ToLower(it.label), f) || strings.Contains(strings.ToLower(it.detail), f) {
			out = append(out, i)
		}
	}
	return out
}

func newPicker(kind pickerKind, items []pickItem, current string) picker {
	p := picker{kind: kind, items: items}
	switch kind {
	case pickProject:
		p.title = "Filter by project"
	case pickTag:
		p.title = "Filter by tag"
	case pickType:
		p.title = "Filter by type"
	case pickRange:
		p.title = "Date range"
	case pickReport:
		p.title = "Generate a report"
	case pickMarks:
		p.title = "Report marks"
		p.multi = true
	}
	for i, it := range items {
		if it.value == current && current != "" {
			p.cursor = i
		}
	}
	return p
}

func (m Model) pickerCurrent(kind pickerKind) string {
	switch kind {
	case pickProject:
		return m.f.project
	case pickTag:
		return m.f.tag
	case pickType:
		return string(m.f.typ)
	case pickRange:
		return m.f.rangeKey
	case pickReport:
		return string(m.repKind)
	}
	return ""
}

// loadPicker gathers choices (projects and tags come from the archive).
func (m Model) loadPicker(kind pickerKind) tea.Cmd {
	ctx, a := m.ctx, m.app
	clock := a.Clock()
	return func() tea.Msg {
		var items []pickItem
		switch kind {
		case pickProject:
			items = append(items, pickItem{label: "Any project", value: ""}, pickItem{label: "No project", value: "-"})
			ps, err := a.Store.ListProjects(ctx, true)
			if err != nil {
				return pickerDataMsg{err: err}
			}
			for _, p := range ps {
				detail := fmt.Sprintf("%d entries", p.EntryCount)
				if len(p.Aliases) > 0 {
					detail += " · " + strings.Join(p.Aliases, ", ")
				}
				if p.Archived() {
					detail += " · archived"
				}
				items = append(items, pickItem{label: p.Name, value: p.Name, detail: detail})
			}
		case pickTag:
			items = append(items, pickItem{label: "Any tag", value: ""})
			tags, err := a.Store.ListTags(ctx)
			if err != nil {
				return pickerDataMsg{err: err}
			}
			for _, t := range tags {
				items = append(items, pickItem{label: "#" + t.Name, value: t.Name, detail: fmt.Sprintf("%d entries", t.Count)})
			}
		case pickType:
			items = append(items, pickItem{label: "Any type", value: ""})
			for _, t := range journal.Types {
				items = append(items, pickItem{label: string(t), value: string(t)})
			}
		case pickRange:
			for _, key := range rangePresets {
				r, _ := clock.Parse(key)
				items = append(items, pickItem{label: strings.ReplaceAll(key, "-", " "), value: key, detail: clock.Describe(r)})
			}
		case pickReport:
			for _, k := range report.Kinds {
				items = append(items, pickItem{label: string(k), value: string(k), detail: k.Describe()})
			}
		}
		return pickerDataMsg{kind: kind, items: items}
	}
}

func (m *Model) openMarks(e journal.Entry) {
	var items []pickItem
	for _, mk := range journal.Marks {
		items = append(items, pickItem{label: string(mk), value: string(mk), checked: e.HasMark(mk), detail: markHelp(mk)})
	}
	m.picker = newPicker(pickMarks, items, "")
	m.picker.title = "Report marks for " + e.Ref()
	m.picker.target = e.ID
	m.layer = layerPicker
}

func markHelp(mk journal.Mark) string {
	switch mk {
	case journal.MarkStaff:
		return "include in the staff update"
	case journal.MarkOneOnOne:
		return "raise in the next one-on-one"
	case journal.MarkQuarterly:
		return "feature in the quarterly report"
	case journal.MarkImportant:
		return "rank above everything else"
	case journal.MarkCrossTeam:
		return "relevant to other teams"
	}
	return ""
}

func (m Model) updatePicker(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.picker
	vis := p.visible()
	switch k.String() {
	case "esc":
		if p.filter != "" && !p.multi {
			p.filter = ""
			p.cursor = 0
			return m, nil
		}
		m.layer = layerNone
		return m, nil
	case "up", "ctrl+p", "shift+tab":
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case "down", "ctrl+n", "tab":
		if p.cursor < len(vis)-1 {
			p.cursor++
		}
		return m, nil
	case "home":
		p.cursor = 0
		return m, nil
	case "end":
		p.cursor = max(0, len(vis)-1)
		return m, nil
	case "enter":
		if p.multi {
			return m.applyMarks()
		}
		if len(vis) == 0 {
			return m, nil
		}
		return m.choose(p.items[vis[p.cursor]])
	case "backspace":
		if !p.multi && p.filter != "" {
			r := []rune(p.filter)
			p.filter = string(r[:len(r)-1])
			p.cursor = 0
		}
		return m, nil
	}
	if p.multi {
		switch k.String() {
		case "space", "x":
			if len(vis) > 0 {
				it := &p.items[vis[p.cursor]]
				it.checked = !it.checked
			}
		case "j":
			if p.cursor < len(vis)-1 {
				p.cursor++
			}
		case "k":
			if p.cursor > 0 {
				p.cursor--
			}
		}
		return m, nil
	}
	if k.Text != "" && k.Mod == 0 || (k.Text != "" && k.Mod == tea.ModShift) {
		p.filter += k.Text
		p.cursor = 0
	}
	return m, nil
}

func (m Model) choose(it pickItem) (tea.Model, tea.Cmd) {
	m.layer = layerNone
	switch m.picker.kind {
	case pickProject:
		m.f.project = it.value
	case pickTag:
		m.f.tag = it.value
	case pickType:
		m.f.typ = journal.Type(it.value)
	case pickRange:
		m.setRangeKey(it.value)
	case pickReport:
		k, err := report.ParseKind(it.value)
		if err != nil {
			return m, nil
		}
		m.repKind = k
		return m, m.buildReport(nil)
	}
	m.cursor, m.offset = 0, 0
	return m, m.reload()
}

func (m Model) applyMarks() (tea.Model, tea.Cmd) {
	m.layer = layerNone
	var marks []journal.Mark
	for _, it := range m.picker.items {
		if it.checked {
			marks = append(marks, journal.Mark(it.value))
		}
	}
	id, ctx, store := m.picker.target, m.ctx, m.app.Store
	return m, func() tea.Msg {
		e, err := store.Update(ctx, id, journal.Patch{Marks: &marks})
		return savedMsg{entry: e, verb: "Marked", err: err}
	}
}

// --- confirmation -----------------------------------------------------------

type confirmDialog struct {
	title  string
	body   string
	action string // label of the confirming key
	onYes  tea.Cmd
}

func (m *Model) openDeleteConfirm(e journal.Entry) {
	ctx, store := m.ctx, m.app.Store
	m.confirm = confirmDialog{
		title:  "Delete " + e.Ref() + "?",
		body:   e.Title(),
		action: "delete",
		onYes: func() tea.Msg {
			err := store.Delete(ctx, e.ID)
			return deletedMsg{entry: e, err: err}
		},
	}
	m.layer = layerConfirm
}

func (m Model) updateConfirm(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		m.layer = layerNone
		return m, m.confirm.onYes
	case "n", "N", "esc", "enter", "q":
		m.layer = layerNone
		m.setStatus("Cancelled", false)
	}
	return m, nil
}

// --- entry form -------------------------------------------------------------

const (
	fieldBody = iota
	fieldTime
	fieldProject
	fieldType
	fieldTags
	fieldMarks
	fieldCount
)

var fieldLabels = [fieldCount]string{"Text", "When", "Project", "Type", "Tags", "Marks"}

type entryForm struct {
	entry        *journal.Entry // nil when creating
	body         textarea.Model
	fields       [fieldCount]textinput.Model // index 0 unused
	focus        int
	original     app.EntryDoc
	confirmLeave bool
}

func (m Model) openForm(e *journal.Entry) (tea.Model, tea.Cmd) {
	doc := m.app.DocFor(e)
	f := entryForm{original: doc, body: newTextArea()}
	if e != nil {
		c := *e
		f.entry = &c
	}
	f.body.SetValue(doc.Body)
	values := [fieldCount]string{"", doc.Time, doc.Project, doc.Type, doc.Tags, doc.Marks}
	hints := [fieldCount]string{"", "now, 14:30, yesterday 16:00, 2026-10-02 09:15", "project name or alias", strings.Join(journal.TypeStrings(), ", "),
		"comma separated", strings.Join(journal.MarkStrings(), ", ")}
	for i := fieldTime; i < fieldCount; i++ {
		f.fields[i] = newInput("", hints[i])
		f.fields[i].SetValue(values[i])
	}
	m.form = f
	m.layer = layerForm
	m.layout()
	return m, m.form.focusField(fieldBody)
}

func (f *entryForm) focusField(i int) tea.Cmd {
	f.focus = i
	f.body.Blur()
	for j := fieldTime; j < fieldCount; j++ {
		f.fields[j].Blur()
	}
	if i == fieldBody {
		return f.body.Focus()
	}
	f.fields[i].CursorEnd()
	return f.fields[i].Focus()
}

func (f entryForm) doc() app.EntryDoc {
	d := app.EntryDoc{
		Body: strings.TrimSpace(f.body.Value()), Time: strings.TrimSpace(f.fields[fieldTime].Value()),
		Project: strings.TrimSpace(f.fields[fieldProject].Value()), Type: strings.TrimSpace(f.fields[fieldType].Value()),
		Tags: strings.TrimSpace(f.fields[fieldTags].Value()), Marks: strings.TrimSpace(f.fields[fieldMarks].Value()),
		Resolved: f.original.Resolved,
	}
	return d
}

func (f entryForm) dirty() bool {
	d, o := f.doc(), f.original
	o.Body = strings.TrimSpace(o.Body)
	return d.Body != o.Body || d.Time != o.Time || d.Project != o.Project || d.Type != o.Type || d.Tags != o.Tags || d.Marks != o.Marks
}

func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := &m.form
	if k, ok := msg.(tea.KeyPressMsg); ok {
		key := k.String()
		if key != "esc" {
			f.confirmLeave = false
		}
		switch key {
		case "esc":
			if f.dirty() && !f.confirmLeave {
				f.confirmLeave = true
				m.setStatus("Unsaved changes · esc again to discard · ctrl+s to save", true)
				return m, nil
			}
			m.layer = layerNone
			m.setStatus("Edit cancelled", false)
			return m, nil
		case "ctrl+s":
			return m.saveForm()
		case "tab":
			return m, f.focusField((f.focus + 1) % fieldCount)
		case "shift+tab":
			return m, f.focusField((f.focus + fieldCount - 1) % fieldCount)
		case "up":
			if f.focus != fieldBody {
				return m, f.focusField(f.focus - 1)
			}
			if f.body.Line() == 0 {
				return m, nil
			}
		case "down":
			if f.focus != fieldBody {
				if f.focus < fieldCount-1 {
					return m, f.focusField(f.focus + 1)
				}
				return m, nil
			}
			if f.body.Line() >= f.body.LineCount()-1 {
				return m, f.focusField(fieldTime)
			}
		case "enter":
			if f.focus != fieldBody {
				if f.focus == fieldCount-1 {
					return m.saveForm()
				}
				return m, f.focusField(f.focus + 1)
			}
		}
	}
	var cmd tea.Cmd
	if f.focus == fieldBody {
		f.body, cmd = f.body.Update(msg)
	} else {
		f.fields[f.focus], cmd = f.fields[f.focus].Update(msg)
	}
	return m, cmd
}

func (m Model) saveForm() (tea.Model, tea.Cmd) {
	doc := m.form.doc()
	if doc.Body == "" {
		m.setStatus("The entry text is empty", true)
		return m, m.form.focusField(fieldBody)
	}
	ctx, a := m.ctx, m.app
	if m.form.entry == nil {
		m.layer = layerNone
		return m, func() tea.Msg {
			res, err := a.CreateFromDoc(ctx, doc)
			return savedMsg{entry: res.Entry, verb: "Added", created: res.CreatedProject, err: err}
		}
	}
	e := *m.form.entry
	p, err := a.PatchFromDoc(e, doc)
	if errors.Is(err, app.ErrNoChanges) {
		m.layer = layerNone
		m.setStatus("No changes", false)
		return m, nil
	}
	if err != nil {
		// Keep the form open so nothing typed is lost.
		m.setStatus(errText(err), true)
		return m, nil
	}
	m.layer = layerNone
	return m, func() tea.Msg {
		updated, err := a.Store.Update(ctx, e.ID, p)
		return savedMsg{entry: updated, verb: "Updated", err: err}
	}
}

// --- reports ----------------------------------------------------------------

// buildReport generates the current report kind for rng, or for the kind's
// default range when rng is nil.
func (m Model) buildReport(rng *timerange.Range) tea.Cmd {
	ctx, a, kind := m.ctx, m.app, m.repKind
	var projects []string
	if m.f.project != "" && m.f.project != "-" {
		projects = []string{m.f.project}
	}
	r := a.DefaultReportRange(kind)
	if rng != nil {
		r = *rng
	}
	return func() tea.Msg {
		rep, err := a.ReportBuilder().Build(ctx, kind, report.Options{Range: r, Projects: projects})
		return reportMsg{rep: rep, err: err}
	}
}

func (m Model) updateReport(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch k.String() {
	case "esc", "q", "backspace":
		m.screen = screenList
	case "up", "k":
		m.repVP.ScrollUp(1)
	case "down", "j":
		m.repVP.ScrollDown(1)
	case "pgup", "ctrl+u":
		m.repVP.PageUp()
	case "pgdown", "space", "ctrl+d":
		m.repVP.PageDown()
	case "home", "g":
		m.repVP.GotoTop()
	case "end", "G":
		m.repVP.GotoBottom()
	case "i":
		m.repIDs = !m.repIDs
		m.refreshReport()
	case "w":
		m.repExplain = !m.repExplain
		m.refreshReport()
	case "[", "]":
		step := -1
		if k.String() == "]" {
			step = 1
		}
		if m.rep.Range.IsZero() {
			return m, nil
		}
		r := m.app.Clock().Shift(m.rep.Range, step)
		return m, m.buildReport(&r)
	case "tab", "shift+tab":
		idx := 0
		for i, kd := range report.Kinds {
			if kd == m.repKind {
				idx = i
			}
		}
		if k.String() == "tab" {
			idx = (idx + 1) % len(report.Kinds)
		} else {
			idx = (idx + len(report.Kinds) - 1) % len(report.Kinds)
		}
		m.repKind = report.Kinds[idx]
		return m, m.buildReport(nil)
	case "R":
		return m, m.loadPicker(pickReport)
	case "s":
		m.layer = layerSavePath
		m.input = newInput("save as › ", "path for the Markdown file")
		m.input.SetValue(defaultReportPath(m.rep))
		m.input.CursorEnd()
		m.input.SetWidth(max(10, m.width-4))
		return m, m.input.Focus()
	case "?", "f1":
		m.layer = layerHelp
	}
	return m, nil
}

func defaultReportPath(r report.Report) string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	name := fmt.Sprintf("holocron-%s-%s.md", r.Kind, r.GeneratedAt.Format("2006-01-02"))
	return filepath.Join(wd, name)
}

func (m Model) saveReport(path string) tea.Cmd {
	rep, ids, explain, a := m.rep, m.repIDs, m.repExplain, m.app
	return func() tea.Msg {
		path = strings.TrimSpace(path)
		if path == "" {
			return statusMsg{text: "Not saved: no path given", isErr: true}
		}
		var b strings.Builder
		if err := report.Markdown(&b, rep, report.RenderOptions{ShowIDs: ids, Explain: explain, Loc: a.Loc, TimeLayout: a.TimeLayout()}); err != nil {
			return statusMsg{text: errText(err), isErr: true}
		}
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return statusMsg{text: errText(err), isErr: true}
			}
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			return statusMsg{text: "Not saved: " + errText(err), isErr: true}
		}
		return statusMsg{text: "Saved report to " + path}
	}
}

// --- editor helpers -----------------------------------------------------------

func resolveEditor(a *app.App) ([]string, error) {
	argv, _, err := editor.Resolve(a.Config.Editor)
	return argv, err
}

func tempDoc(text string) (string, error) { return editor.TempFile(text) }

func readDoc(path string) (string, error) {
	defer os.Remove(path)
	return editor.ReadBack(path)
}

func editorCommand(ctx context.Context, argv []string, path string) *exec.Cmd {
	return editor.Command(ctx, argv, path)
}
