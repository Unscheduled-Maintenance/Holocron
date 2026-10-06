package export

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// ReadJSON reads a JSON export (holocron.export/v1) as records ready to be
// merged into an archive. Exports carry no per-field clocks, so every field
// of a record is treated as last changed at the record's updated_at.
// Repository paths are local to the computer that made the export and are
// not imported.
func ReadJSON(r io.Reader) (journal.Records, error) {
	var doc jsonDoc
	dec := json.NewDecoder(r)
	if err := dec.Decode(&doc); err != nil {
		return journal.Records{}, fmt.Errorf("%w: not a Holocron JSON export: %w", journal.ErrInvalid, err)
	}
	if doc.Format != FormatVersion {
		if doc.Format == "" {
			return journal.Records{}, fmt.Errorf("%w: not a Holocron JSON export (no format field); create one with `holocron export --format json`", journal.ErrInvalid)
		}
		return journal.Records{}, fmt.Errorf("%w: unsupported export format %q (this Holocron reads %s)", journal.ErrInvalid, doc.Format, FormatVersion)
	}

	var out journal.Records
	projectUID := map[string]string{} // lower-case name -> UID
	for _, p := range doc.Projects {
		if p.UID == "" || strings.TrimSpace(p.Name) == "" {
			return journal.Records{}, fmt.Errorf("%w: a project in the export has no UID or name", journal.ErrInvalid)
		}
		clock := journal.ClockFromTime(p.UpdatedAt)
		out.Projects = append(out.Projects, journal.ProjectRecord{
			UID: p.UID, Name: p.Name, Description: p.Description, Aliases: p.Aliases, URLs: p.URLs,
			ArchivedAt: p.ArchivedAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
			Clocks: clocksAt(clock, journal.ProjectFields),
		})
		projectUID[strings.ToLower(p.Name)] = p.UID
	}

	uidByID := map[int64]string{}
	for _, e := range doc.Entries {
		uidByID[e.ID] = e.UID
	}
	for _, e := range doc.Entries {
		if !journal.IsUID(e.UID) {
			return journal.Records{}, fmt.Errorf("%w: entry %d in the export has no valid UID", journal.ErrInvalid, e.ID)
		}
		rec := journal.EntryRecord{
			UID: strings.ToUpper(e.UID), Num: e.ID, OccurredAt: e.OccurredAt, Body: e.Body, Tags: e.Tags,
			ResolvedAt: e.ResolvedAt, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Source: e.Source,
			Clocks: clocksAt(journal.ClockFromTime(e.UpdatedAt), journal.EntryFields),
		}
		if e.Ref != "" {
			ref, err := journal.ParseRef(e.Ref)
			if err != nil {
				return journal.Records{}, fmt.Errorf("entry %s in the export: %w", e.UID, err)
			}
			rec.Num, rec.Label = ref.Num, ref.Label
		}
		if t, err := time.Parse(time.RFC3339, e.LocalTime); err == nil {
			_, rec.UTCOffset = t.Zone()
		}
		if e.Type != nil {
			rec.Type = journal.Type(*e.Type)
		}
		if e.Project != nil && *e.Project != "" {
			uid, ok := projectUID[strings.ToLower(*e.Project)]
			if !ok {
				// A filtered export may leave a project out; match it by name.
				uid = journal.NewUID(e.CreatedAt)
				clock := journal.ClockFromTime(e.CreatedAt)
				out.Projects = append(out.Projects, journal.ProjectRecord{UID: uid, Name: *e.Project,
					CreatedAt: e.CreatedAt, UpdatedAt: e.CreatedAt, Clocks: clocksAt(clock, journal.ProjectFields)})
				projectUID[strings.ToLower(*e.Project)] = uid
			}
			rec.Project = uid
		}
		for _, m := range e.Marks {
			rec.Marks = append(rec.Marks, journal.Mark(m))
		}
		if e.ResolvedBy != nil {
			rec.ResolvedBy = uidByID[*e.ResolvedBy]
		}
		out.Entries = append(out.Entries, rec)
	}
	return out, nil
}

func clocksAt(h journal.HLC, fields []string) journal.Clocks {
	c := make(journal.Clocks, len(fields))
	for _, f := range fields {
		c[f] = h
	}
	return c
}
