// Package export writes the archive as Markdown or JSON so that a person's
// records are never trapped inside Holocron.
//
// The JSON structure is documented in docs/export-format.md and versioned by
// its "format" field.
package export

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// FormatVersion identifies the JSON export structure.
const FormatVersion = "holocron.export/v1"

// Archive is the material to export.
type Archive struct {
	ExportedAt time.Time
	// Description says what was selected, e.g. "all entries" or "project AWS, last 30 days".
	Description string
	Projects    []journal.Project
	Entries     []journal.Entry // oldest first
}

// Options control rendering.
type Options struct {
	Loc        *time.Location
	TimeLayout string
}

func (o Options) loc() *time.Location {
	if o.Loc == nil {
		return time.Local
	}
	return o.Loc
}

// JSON document types. Field names are part of the documented format.
type jsonDoc struct {
	Format      string        `json:"format"`
	ExportedAt  time.Time     `json:"exported_at"`
	Description string        `json:"description"`
	Projects    []jsonProject `json:"projects"`
	Entries     []EntryJSON   `json:"entries"`
}

type jsonProject struct {
	UID         string     `json:"uid"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Aliases     []string   `json:"aliases"`
	Paths       []string   `json:"repository_paths"`
	URLs        []string   `json:"urls"`
	ArchivedAt  *time.Time `json:"archived_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// EntryJSON is the documented JSON form of an entry, shared by export and
// every command that prints entries with --json.
type EntryJSON struct {
	ID         int64           `json:"id"`
	UID        string          `json:"uid"`
	OccurredAt time.Time       `json:"occurred_at"`
	LocalTime  string          `json:"recorded_local_time"`
	Body       string          `json:"body"`
	Type       *string         `json:"type"`
	Project    *string         `json:"project"`
	Tags       []string        `json:"tags"`
	Marks      []string        `json:"marks"`
	ResolvedAt *time.Time      `json:"resolved_at"`
	ResolvedBy *int64          `json:"resolved_by"`
	Resolves   []int64         `json:"resolves"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Source     *journal.Source `json:"source"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// JSON writes the archive as a single JSON document.
func JSON(w io.Writer, a Archive) error {
	doc := jsonDoc{
		Format:      FormatVersion,
		ExportedAt:  a.ExportedAt.UTC(),
		Description: a.Description,
		Projects:    []jsonProject{},
		Entries:     []EntryJSON{},
	}
	for _, p := range a.Projects {
		jp := jsonProject{UID: p.UID, Name: p.Name, Description: p.Description,
			Aliases: nonNil(p.Aliases), Paths: nonNil(p.Paths), URLs: nonNil(p.URLs),
			CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC()}
		if p.ArchivedAt != nil {
			t := p.ArchivedAt.UTC()
			jp.ArchivedAt = &t
		}
		doc.Projects = append(doc.Projects, jp)
	}
	for _, e := range a.Entries {
		doc.Entries = append(doc.Entries, NewEntryJSON(e))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Markdown writes the archive as a human-readable Markdown document,
// grouped by month and day. It is designed to be understandable without
// Holocron: every entry carries its ID, time, project, type, tags and marks.
func Markdown(w io.Writer, a Archive, o Options) error {
	layout := o.TimeLayout
	if layout == "" {
		layout = "15:04"
	}
	loc := o.loc()
	var b strings.Builder
	b.WriteString("# Holocron archive\n\n")
	fmt.Fprintf(&b, "Exported %s. %s.\n\n", a.ExportedAt.In(loc).Format("Monday 2 January 2006 15:04 MST"), describeCount(a, loc))
	if a.Description != "" {
		fmt.Fprintf(&b, "Selection: %s.\n\n", a.Description)
	}
	b.WriteString("Each entry shows its time, text, and then its ID (`#42`), project, type, `#tags` and report marks. ")
	b.WriteString("Times are shown in " + zoneName(a.ExportedAt.In(loc)) + ".\n")

	if len(a.Projects) > 0 {
		b.WriteString("\n## Projects\n\n")
		for _, p := range a.Projects {
			b.WriteString("- **" + escape(p.Name) + "**")
			var extra []string
			if p.Description != "" {
				extra = append(extra, escape(p.Description))
			}
			if len(p.Aliases) > 0 {
				extra = append(extra, "aliases: "+strings.Join(p.Aliases, ", "))
			}
			if p.Archived() {
				extra = append(extra, "archived")
			}
			if len(extra) > 0 {
				b.WriteString(" — " + strings.Join(extra, "; "))
			}
			b.WriteString("\n")
		}
	}

	var month, day string
	for _, e := range a.Entries {
		t := e.OccurredAt.In(loc)
		if m := t.Format("January 2006"); m != month {
			month = m
			b.WriteString("\n## " + m + "\n")
			day = ""
		}
		if d := t.Format("2006-01-02"); d != day {
			day = d
			b.WriteString("\n### " + t.Format("Monday 2 January 2006") + "\n\n")
		}
		lines := strings.Split(e.Body, "\n")
		fmt.Fprintf(&b, "- **%s** %s", t.Format(layout), escape(lines[0]))
		for _, l := range lines[1:] {
			b.WriteString("  \n  " + escape(l))
		}
		b.WriteString("  \n  " + metaLine(e) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func metaLine(e journal.Entry) string {
	parts := []string{"`" + e.Ref() + "`"}
	if e.Project != "" {
		parts = append(parts, "project: "+escape(e.Project))
	}
	if e.Type != "" {
		t := string(e.Type)
		if e.Type.Opens() {
			if e.ResolvedAt != nil {
				t += " (resolved"
				if e.ResolvedBy != 0 {
					t += " by #" + strconv.FormatInt(e.ResolvedBy, 10)
				}
				t += ")"
			} else {
				t += " (open)"
			}
		}
		parts = append(parts, "type: "+t)
	}
	if len(e.Tags) > 0 {
		tags := make([]string, len(e.Tags))
		for i, t := range e.Tags {
			tags[i] = "`#" + t + "`"
		}
		parts = append(parts, strings.Join(tags, " "))
	}
	if len(e.Marks) > 0 {
		ms := make([]string, len(e.Marks))
		for i, m := range e.Marks {
			ms[i] = string(m)
		}
		parts = append(parts, "marks: "+strings.Join(ms, ", "))
	}
	if len(e.Resolves) > 0 {
		refs := make([]string, len(e.Resolves))
		for i, id := range e.Resolves {
			refs[i] = "#" + strconv.FormatInt(id, 10)
		}
		parts = append(parts, "resolves: "+strings.Join(refs, ", "))
	}
	if e.Source != nil {
		src := "source: " + e.Source.Type + " " + e.Source.ID
		if e.Source.URL != "" {
			src = "source: [" + e.Source.Type + " " + shortID(e.Source.ID) + "](" + e.Source.URL + ")"
		}
		parts = append(parts, src)
	}
	return strings.Join(parts, " · ")
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func describeCount(a Archive, loc *time.Location) string {
	n := len(a.Entries)
	switch n {
	case 0:
		return "No entries"
	case 1:
		return "1 entry"
	}
	first := a.Entries[0].OccurredAt.In(loc).Format("2006-01-02")
	last := a.Entries[n-1].OccurredAt.In(loc).Format("2006-01-02")
	return fmt.Sprintf("%d entries from %s to %s", n, first, last)
}

func zoneName(t time.Time) string {
	name, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("%s (UTC%s%02d:%02d)", name, sign, off/3600, (off%3600)/60)
}

// escape neutralises Markdown syntax that would change an entry's meaning
// (emphasis, links, HTML) while keeping it readable as plain text.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;")
	out := r.Replace(s)
	// A line starting with '#', '-', '+' or a number and '.' would become a heading or list.
	if len(out) > 0 && strings.ContainsRune("#-+>", rune(out[0])) {
		out = `\` + out
	}
	return out
}

// NewEntryJSON converts an entry to its documented JSON form.
func NewEntryJSON(e journal.Entry) EntryJSON {
	je := EntryJSON{
		ID: e.ID, UID: e.UID, OccurredAt: e.OccurredAt.UTC(),
		LocalTime: e.OccurredAt.In(e.RecordedOffset()).Format(time.RFC3339),
		Body:      e.Body, Type: strPtr(string(e.Type)), Project: strPtr(e.Project),
		Tags: nonNil(e.Tags), Marks: []string{}, Resolves: nonNil(e.Resolves),
		CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC(), Source: e.Source,
	}
	for _, m := range e.Marks {
		je.Marks = append(je.Marks, string(m))
	}
	if e.ResolvedAt != nil {
		t := e.ResolvedAt.UTC()
		je.ResolvedAt = &t
	}
	if e.ResolvedBy != 0 {
		by := e.ResolvedBy
		je.ResolvedBy = &by
	}
	return je
}
