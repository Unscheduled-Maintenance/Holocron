package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

// RenderOptions control how a report is presented.
type RenderOptions struct {
	// ShowIDs appends source entry references (#42) to each item.
	ShowIDs bool
	// Explain adds the selection reasons beneath each item.
	Explain bool
	// Loc is the display timezone.
	Loc *time.Location
	// TimeLayout formats times of day ("15:04" or "3:04pm").
	TimeLayout string
	Styler     style.Styler
}

func (o RenderOptions) loc() *time.Location {
	if o.Loc == nil {
		return time.Local
	}
	return o.Loc
}

func (o RenderOptions) timeLayout() string {
	if o.TimeLayout == "" {
		return "15:04"
	}
	return o.TimeLayout
}

func refs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	if len(parts) > 6 {
		return strings.Join(parts[:6], " ") + fmt.Sprintf(" +%d", len(parts)-6)
	}
	return strings.Join(parts, " ")
}

// sectionShowsProject reports whether items should name their project:
// sections that are themselves a project do not repeat it.
func sectionShowsProject(s Section) bool { return !strings.HasPrefix(s.Key, "project:") }

// sectionShowsType reports whether items should show their entry type:
// sections defined by type (Decisions, Open problems) do not repeat it.
func sectionShowsType(r Report) bool {
	return r.Kind == Day || r.Kind == Week || r.Kind == Quarter
}

// Text renders a report for the terminal.
func Text(w io.Writer, r Report, o RenderOptions) error {
	st := o.Styler
	var b strings.Builder
	b.WriteString(st.Accent(style.Mark) + " " + st.Title(r.Title) + "\n")
	meta := describeRange(r, o)
	b.WriteString("  " + st.Dim(meta) + "\n")
	for _, line := range r.Summary {
		b.WriteString("  " + line + "\n")
	}
	if r.IsEmpty() {
		b.WriteString("\n  " + st.Dim(emptyMessage(r)) + "\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	for _, s := range r.Sections {
		if len(s.Items) == 0 && s.Omitted == 0 {
			continue
		}
		b.WriteString("\n" + st.Heading(strings.ToUpper(s.Title)) + "\n")
		if s.Note != "" {
			b.WriteString("  " + st.Dim(s.Note) + "\n")
		}
		for _, it := range s.Items {
			b.WriteString("  " + st.Accent("•") + " ")
			if r.Kind == Day && !it.Time.IsZero() {
				b.WriteString(st.Dim(it.Time.In(o.loc()).Format(o.timeLayout())) + "  ")
			}
			if sectionShowsProject(s) && it.Project != "" {
				b.WriteString(st.Project(it.Project) + st.Dim(":") + " ")
			}
			b.WriteString(it.Text)
			if sectionShowsType(r) && it.Type != "" {
				b.WriteString(" " + st.Type("("+string(it.Type)+")"))
			}
			if it.Open && r.Kind != OneOnOne {
				b.WriteString(" " + st.Warn("[open]"))
			}
			if o.ShowIDs {
				b.WriteString("  " + st.Faint(refs(it.EntryIDs)))
			}
			b.WriteString("\n")
			if o.Explain && len(it.Reasons) > 0 {
				b.WriteString("      " + st.Faint("why: "+strings.Join(it.Reasons, "; ")) + "\n")
			}
		}
		if s.Omitted > 0 && !s.omittedInNote {
			line := fmt.Sprintf("  + %d more not shown", s.Omitted)
			if o.ShowIDs {
				line += "  " + refs(s.OmittedIDs)
			}
			b.WriteString(st.Dim(line) + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func describeRange(r Report, o RenderOptions) string {
	var parts []string
	if !r.Range.Start.IsZero() && !r.Range.End.IsZero() {
		first := r.Range.Start.In(o.loc()).Format("2006-01-02")
		last := r.Range.End.In(o.loc()).AddDate(0, 0, -1).Format("2006-01-02")
		if first == last {
			parts = append(parts, first)
		} else {
			parts = append(parts, first+" to "+last+" (inclusive)")
		}
	} else if !r.Range.Start.IsZero() {
		parts = append(parts, "since "+r.Range.Start.In(o.loc()).Format("2006-01-02"))
	}
	if !r.GeneratedAt.IsZero() {
		parts = append(parts, "generated "+r.GeneratedAt.In(o.loc()).Format("Mon 2 Jan 2006 "+o.timeLayout()))
	}
	return strings.Join(parts, " · ")
}

func emptyMessage(r Report) string {
	switch r.Kind {
	case Staff:
		return "Nothing stood out for a staff update. Record accomplishments and decisions with --type, or mark entries with `holocron mark <id> staff`."
	case OneOnOne:
		return "Nothing to raise yet. Mark entries with `holocron mark <id> one-on-one` to collect discussion items."
	}
	return "No entries recorded in this period."
}

// Markdown renders a report as Markdown.
func Markdown(w io.Writer, r Report, o RenderOptions) error {
	var b strings.Builder
	b.WriteString("# " + r.Title + "\n\n")
	b.WriteString("_" + describeRange(r, o) + "_\n\n")
	for _, line := range r.Summary {
		b.WriteString(line + "\n\n")
	}
	if r.IsEmpty() {
		b.WriteString(emptyMessage(r) + "\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	for _, s := range r.Sections {
		if len(s.Items) == 0 && s.Omitted == 0 {
			continue
		}
		b.WriteString("## " + mdEscape(s.Title) + "\n\n")
		if s.Note != "" {
			b.WriteString("_" + s.Note + "_\n\n")
		}
		for _, it := range s.Items {
			b.WriteString("- ")
			if r.Kind == Day && !it.Time.IsZero() {
				b.WriteString(it.Time.In(o.loc()).Format(o.timeLayout()) + " ")
			}
			if sectionShowsProject(s) && it.Project != "" {
				b.WriteString("**" + mdEscape(it.Project) + ":** ")
			}
			b.WriteString(mdEscape(it.Text))
			if sectionShowsType(r) && it.Type != "" {
				b.WriteString(" _(" + string(it.Type) + ")_")
			}
			if it.Open && r.Kind != OneOnOne {
				b.WriteString(" **[open]**")
			}
			if o.ShowIDs {
				b.WriteString(" (" + refs(it.EntryIDs) + ")")
			}
			b.WriteString("\n")
			if o.Explain && len(it.Reasons) > 0 {
				b.WriteString("  - _why: " + strings.Join(it.Reasons, "; ") + "_\n")
			}
		}
		if s.Omitted > 0 && !s.omittedInNote {
			fmt.Fprintf(&b, "- _…and %d more not shown_\n", s.Omitted)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, strings.TrimRight(b.String(), "\n")+"\n")
	return err
}

// mdEscape escapes characters that would otherwise start Markdown syntax
// at the beginning of a list item or inside emphasis.
func mdEscape(s string) string {
	r := strings.NewReplacer("*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", "&lt;")
	return r.Replace(s)
}

// JSONVersion identifies the report JSON structure. It changes only when a
// field is removed or its meaning changes.
const JSONVersion = "holocron.report/v1"

type jsonReport struct {
	Format      string        `json:"format"`
	Kind        Kind          `json:"kind"`
	Title       string        `json:"title"`
	Range       jsonRange     `json:"range"`
	GeneratedAt time.Time     `json:"generated_at"`
	Summary     []string      `json:"summary"`
	Sections    []jsonSection `json:"sections"`
	Sources     []jsonSource  `json:"sources"`
}

type jsonRange struct {
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
	Label string     `json:"label"`
}

type jsonSection struct {
	Key        string     `json:"key"`
	Title      string     `json:"title"`
	Note       string     `json:"note,omitempty"`
	Items      []jsonItem `json:"items"`
	Omitted    int        `json:"omitted"`
	OmittedIDs []int64    `json:"omitted_entry_ids,omitempty"`
}

type jsonItem struct {
	Text     string     `json:"text"`
	Project  string     `json:"project,omitempty"`
	Type     string     `json:"type,omitempty"`
	Time     *time.Time `json:"occurred_at,omitempty"`
	Open     bool       `json:"open,omitempty"`
	EntryIDs []int64    `json:"entry_ids"`
	Reasons  []string   `json:"reasons"`
}

type jsonSource struct {
	ID         int64     `json:"id"`
	UID        string    `json:"uid"`
	OccurredAt time.Time `json:"occurred_at"`
	Project    string    `json:"project,omitempty"`
	Type       string    `json:"type,omitempty"`
	Body       string    `json:"body"`
}

// JSON renders a report as JSON, including every source entry.
func JSON(w io.Writer, r Report) error {
	out := jsonReport{
		Format: JSONVersion, Kind: r.Kind, Title: r.Title, GeneratedAt: r.GeneratedAt.UTC(),
		Range:    jsonRange{Label: r.Range.Label},
		Summary:  append([]string{}, r.Summary...),
		Sections: []jsonSection{},
		Sources:  []jsonSource{},
	}
	if !r.Range.Start.IsZero() {
		t := r.Range.Start.UTC()
		out.Range.Start = &t
	}
	if !r.Range.End.IsZero() {
		t := r.Range.End.UTC()
		out.Range.End = &t
	}
	for _, s := range r.Sections {
		js := jsonSection{Key: s.Key, Title: s.Title, Note: s.Note, Omitted: s.Omitted, OmittedIDs: s.OmittedIDs, Items: []jsonItem{}}
		for _, it := range s.Items {
			ji := jsonItem{Text: it.Text, Project: it.Project, Type: string(it.Type), Open: it.Open, EntryIDs: it.EntryIDs, Reasons: it.Reasons}
			if !it.Time.IsZero() {
				t := it.Time.UTC()
				ji.Time = &t
			}
			if ji.Reasons == nil {
				ji.Reasons = []string{}
			}
			js.Items = append(js.Items, ji)
		}
		out.Sections = append(out.Sections, js)
	}
	for _, id := range r.SourceIDs() {
		e, ok := r.Entries[id]
		if !ok {
			continue
		}
		out.Sources = append(out.Sources, jsonSource{ID: e.ID, UID: e.UID, OccurredAt: e.OccurredAt.UTC(), Project: e.Project, Type: string(e.Type), Body: e.Body})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
