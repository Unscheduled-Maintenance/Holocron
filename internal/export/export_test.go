package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

func sample() Archive {
	loc := time.FixedZone("NZDT", 13*3600)
	at := time.Date(2026, 10, 5, 9, 14, 0, 0, loc)
	resolved := at.Add(time.Hour)
	return Archive{
		ExportedAt:  time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC),
		Description: "all entries",
		Projects:    []journal.Project{{UID: "P1", Name: "AWS", Aliases: []string{"amazon"}}},
		Entries: []journal.Entry{
			{ID: 1, UID: "01J0000000000000000000000A", OccurredAt: at.UTC(), UTCOffset: 13 * 3600,
				Body: "Enabled *IAM* Access Analyzer\nin all regions", Project: "AWS", Type: journal.TypeAccomplishment,
				Tags: []string{"security"}, Marks: []journal.Mark{journal.MarkStaff}, CreatedAt: at, UpdatedAt: at},
			{ID: 2, UID: "01J0000000000000000000000B", OccurredAt: at.Add(26 * time.Hour).UTC(), UTCOffset: 13 * 3600,
				Body: "# not a heading", Type: journal.TypeProblem, ResolvedAt: &resolved,
				Source: &journal.Source{Type: "git", ID: "abcdef1234567890", URL: "https://example.com/c/abcdef"}, CreatedAt: at, UpdatedAt: at},
		},
	}
}

func TestJSONExport(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["format"] != FormatVersion {
		t.Fatalf("format = %v", doc["format"])
	}
	entries := doc["entries"].([]any)
	first := entries[0].(map[string]any)
	if first["recorded_local_time"] != "2026-10-05T09:14:00+13:00" || first["project"] != "AWS" {
		t.Fatalf("entry = %v", first)
	}
	second := entries[1].(map[string]any)
	if second["project"] != nil || second["source"].(map[string]any)["type"] != "git" {
		t.Fatalf("null project / source wrong: %v", second)
	}
	for _, key := range []string{"id", "uid", "occurred_at", "body", "type", "tags", "marks", "resolved_at", "created_at", "updated_at"} {
		if _, ok := first[key]; !ok {
			t.Errorf("entry JSON missing %q", key)
		}
	}
}

func TestMarkdownExport(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, sample(), Options{Loc: time.FixedZone("NZDT", 13*3600)}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# Holocron archive",
		"2 entries from",
		"## October 2026",
		"### Monday 5 October 2026",
		`- **09:14** Enabled \*IAM\* Access Analyzer  ` + "\n  in all regions",
		"`#1` · project: AWS · type: accomplishment · `#security` · marks: staff",
		`\# not a heading`,
		"type: problem (resolved)",
		"[git abcdef123456](https://example.com/c/abcdef)",
		"**AWS** — aliases: amazon",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}
}

func TestReadJSONRoundTrip(t *testing.T) {
	a := sample()
	a.Entries[1].Num, a.Entries[1].Label = 7, "b"
	a.Entries[1].ResolvedBy = 1
	var buf bytes.Buffer
	if err := JSON(&buf, a); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs.Projects) != 1 || recs.Projects[0].UID != "P1" || recs.Projects[0].Aliases[0] != "amazon" {
		t.Fatalf("projects = %+v", recs.Projects)
	}
	if len(recs.Entries) != 2 {
		t.Fatalf("entries = %+v", recs.Entries)
	}
	first, second := recs.Entries[0], recs.Entries[1]
	if first.Ref() != "#1" || first.UTCOffset != 13*3600 || first.Project != "P1" || first.Marks[0] != journal.MarkStaff ||
		first.Clocks[journal.FieldBody] != journal.ClockFromTime(a.Entries[0].UpdatedAt) {
		t.Errorf("first = %+v", first)
	}
	if second.Ref() != "#7b" || second.ResolvedBy != first.UID || second.Source == nil || second.Source.ID != "abcdef1234567890" {
		t.Errorf("second = %+v", second)
	}

	for in, want := range map[string]string{
		`not json`:                         "not a Holocron JSON export",
		`{"entries": []}`:                  "no format field",
		`{"format": "holocron.export/v9"}`: "unsupported export format",
		`{"format": "holocron.export/v1", "entries": [{"id": 1, "uid": "nope"}]}`: "no valid UID",
	} {
		if _, err := ReadJSON(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadJSON(%s) = %v, want %q", in, err, want)
		}
	}
}
