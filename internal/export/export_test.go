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
