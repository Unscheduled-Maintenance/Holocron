package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// ErrNoChanges is returned when an edited document matches the original.
var ErrNoChanges = errors.New("no changes")

// ErrEmptyEntry is returned when an editor session leaves no entry text.
var ErrEmptyEntry = errors.New("entry text is empty; nothing saved")

// EntryDoc is the editable plain-text form of an entry: a few header fields,
// a "---" separator, then the body.
type EntryDoc struct {
	Time     string
	Project  string
	Type     string
	Tags     string
	Marks    string
	Resolved string // "yes"/"no"; only meaningful for problems and follow-ups
	Body     string
}

const docHelp = `# Edit the fields and the text below the --- line, then save and close.
# Lines starting with # above the --- are ignored. Leave a field empty to clear it.
# time:  2026-10-05 14:30, yesterday 16:00, now
# type:  ` + "work, accomplishment, decision, investigation, problem, follow-up, note" + `
# marks: staff, one-on-one, quarterly, important, cross-team (report signals)
`

// DocFor renders an entry (or a blank new entry) as an EntryDoc.
func (a *App) DocFor(e *journal.Entry) EntryDoc {
	if e == nil {
		return EntryDoc{Time: "now"}
	}
	d := EntryDoc{
		Time:    e.OccurredAt.In(a.Loc).Format("2006-01-02 15:04"),
		Project: e.Project,
		Type:    string(e.Type),
		Tags:    strings.Join(e.Tags, ", "),
		Body:    e.Body,
	}
	var ms []string
	for _, m := range e.Marks {
		ms = append(ms, string(m))
	}
	d.Marks = strings.Join(ms, ", ")
	if e.Type.Opens() {
		d.Resolved = "no"
		if e.ResolvedAt != nil {
			d.Resolved = "yes"
		}
	}
	return d
}

// Format renders the document as text for an editor.
func (d EntryDoc) Format(title string) string {
	var b strings.Builder
	b.WriteString("# " + title + "\n")
	b.WriteString(docHelp)
	fmt.Fprintf(&b, "time: %s\nproject: %s\ntype: %s\ntags: %s\nmarks: %s\n", d.Time, d.Project, d.Type, d.Tags, d.Marks)
	if d.Resolved != "" {
		fmt.Fprintf(&b, "resolved: %s\n", d.Resolved)
	}
	b.WriteString("---\n")
	b.WriteString(d.Body)
	if !strings.HasSuffix(d.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// ParseEntryDoc parses edited text back into an EntryDoc.
func ParseEntryDoc(text string) (EntryDoc, error) {
	var d EntryDoc
	text = strings.ReplaceAll(text, "\r\n", "\n")
	head, body, found := strings.Cut(text, "\n---\n")
	if !found {
		if strings.HasPrefix(text, "---\n") {
			head, body, found = "", strings.TrimPrefix(text, "---\n"), true
		} else if h, ok := strings.CutSuffix(text, "\n---"); ok {
			head, body, found = h, "", true
		}
	}
	if !found {
		return d, fmt.Errorf("%w: the --- line separating fields from text is missing", journal.ErrInvalid)
	}
	for i, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return d, fmt.Errorf("%w: line %d (%q) should look like \"field: value\"", journal.ErrInvalid, i+1, line)
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "time", "date", "when":
			d.Time = val
		case "project":
			d.Project = val
		case "type":
			d.Type = val
		case "tags", "tag":
			d.Tags = val
		case "marks", "mark":
			d.Marks = val
		case "resolved":
			d.Resolved = strings.ToLower(val)
		default:
			return d, fmt.Errorf("%w: unknown field %q on line %d", journal.ErrInvalid, key, i+1)
		}
	}
	d.Body = strings.TrimSpace(body)
	return d, nil
}

func splitField(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseYesNo(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "1", "resolved", "done":
		return true, nil
	case "no", "n", "false", "0", "open", "":
		return false, nil
	}
	return false, fmt.Errorf("%w: resolved should be yes or no, not %q", journal.ErrInvalid, s)
}

// CreateFromDoc saves a new entry from an edited document.
// resolves names open items the new entry resolves, as in CaptureInput.
func (a *App) CreateFromDoc(ctx context.Context, d EntryDoc, resolves ...string) (CaptureResult, error) {
	if d.Body == "" {
		return CaptureResult{}, ErrEmptyEntry
	}
	at := d.Time
	if strings.EqualFold(at, "now") {
		at = ""
	}
	res, err := a.Capture(ctx, CaptureInput{Text: d.Body, Project: d.Project, Type: d.Type,
		Tags: splitField(d.Tags), Marks: splitField(d.Marks), At: at, Raw: true, Resolves: resolves})
	if err != nil {
		return res, err
	}
	if done, _ := parseYesNo(d.Resolved); done && res.Entry.Type.Opens() {
		yes := true
		res.Entry, err = a.Store.Update(ctx, res.Entry.ID, journal.Patch{Resolved: &yes})
	}
	return res, err
}

// PatchFromDoc computes the changes between an entry and its edited document.
func (a *App) PatchFromDoc(e journal.Entry, d EntryDoc) (journal.Patch, error) {
	var p journal.Patch
	orig := a.DocFor(&e)
	if d.Body == "" {
		return p, ErrEmptyEntry
	}
	if d.Body != orig.Body {
		p.Body = &d.Body
	}
	if d.Time != orig.Time {
		t, err := a.Clock().ParseMoment(d.Time)
		if err != nil {
			return p, fmt.Errorf("%w: time: %w", journal.ErrInvalid, err)
		}
		if !t.Equal(e.OccurredAt) {
			p.OccurredAt = &t
		}
	}
	if !strings.EqualFold(d.Project, orig.Project) {
		p.Project = &d.Project
		p.CreateProject = a.Config.CreateProjectsEnabled()
	}
	if d.Type != orig.Type {
		t, err := a.ParseType(d.Type)
		if err != nil {
			return p, err
		}
		p.Type = &t
	}
	if d.Tags != orig.Tags {
		tags := splitField(d.Tags)
		p.Tags = &tags
	}
	if d.Marks != orig.Marks {
		marks, err := journal.ParseMarks(splitField(d.Marks))
		if err != nil {
			return p, err
		}
		p.Marks = &marks
	}
	if d.Resolved != orig.Resolved && d.Resolved != "" {
		v, err := parseYesNo(d.Resolved)
		if err != nil {
			return p, err
		}
		if v != (e.ResolvedAt != nil) {
			p.Resolved = &v
		}
	}
	if p.IsEmpty() {
		return p, ErrNoChanges
	}
	return p, nil
}

// FormatTime renders an instant for display in the configured style.
func (a *App) FormatTime(t time.Time) string { return t.In(a.Loc).Format(a.TimeLayout()) }
