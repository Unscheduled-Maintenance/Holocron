package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// now is Wednesday 7 October 2026, 17:00 UTC.
var now = time.Date(2026, 10, 7, 17, 0, 0, 0, time.UTC)

type fixture struct {
	store *journal.Store
	b     Builder
	ids   map[string]int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "h.db"), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := journal.NewStore(db, journal.WithClock(func() time.Time { return now }), journal.WithLocation(time.UTC))
	f := fixture{store: s, ids: map[string]int64{},
		b: Builder{Store: s, Clock: timerange.NewClock(now, time.UTC, time.Monday), MaxItems: 8, OpenLookback: "90d"}}

	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	add := func(key string, n journal.NewEntry) {
		n.CreateProject = true
		e, err := s.AddEntry(ctx, n)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		f.ids[key] = e.ID
	}
	// This week (Mon 5 – Sun 11 October).
	add("analyzer", journal.NewEntry{Body: "Enabled IAM Access Analyzer in all AWS regions", OccurredAt: at(5, 9), Project: "AWS", Type: journal.TypeAccomplishment, Tags: []string{"security"}})
	add("exporter", journal.NewEntry{Body: "Investigated unexpected exporter restarts", OccurredAt: at(5, 11), Project: "Infra", Type: journal.TypeInvestigation, Tags: []string{"monitoring"}})
	add("cloudtrail", journal.NewEntry{Body: "Fixed CloudTrail bucket logging policy", OccurredAt: at(6, 10), Project: "AWS", Type: journal.TypeProblem, Tags: []string{"security", "logging"}})
	add("queue", journal.NewEntry{Body: "Reviewed repository moderation queue", OccurredAt: at(6, 14), Project: "CCR", Type: journal.TypeWork})
	add("deploy", journal.NewEntry{Body: "Decision: retain the existing deployment model for now", OccurredAt: at(6, 15), Project: "Infra", Type: journal.TypeDecision})
	add("arm", journal.NewEntry{Body: "Follow up with engineering about ARM build capacity", OccurredAt: at(7, 9), Project: "Infra", Type: journal.TypeFollowUp, Marks: []journal.Mark{journal.MarkCrossTeam}})
	add("restarts2", journal.NewEntry{Body: "Exporter restarted again overnight", OccurredAt: at(7, 10), Project: "Infra", Type: journal.TypeProblem, Tags: []string{"monitoring"}})
	add("lunch", journal.NewEntry{Body: "Tidied my desk", OccurredAt: at(7, 12), Type: journal.TypeNote})
	add("talk", journal.NewEntry{Body: "Want to talk about on-call load", OccurredAt: at(7, 13), Marks: []journal.Mark{journal.MarkOneOnOne}})
	add("pinned", journal.NewEntry{Body: "Paired with the new hire on Terraform", OccurredAt: at(7, 14), Project: "AWS", Type: journal.TypeWork, Marks: []journal.Mark{journal.MarkStaff}})
	for i := 0; i < 4; i++ {
		add(fmt.Sprintf("commit%d", i), journal.NewEntry{Body: fmt.Sprintf("Commit %d", i), OccurredAt: at(6, 16+i), Project: "Holocron",
			Source: &journal.Source{Type: "git", ID: fmt.Sprintf("sha%d", i)}})
	}
	// Older: an unresolved problem from last month, and a resolved one.
	add("oldproblem", journal.NewEntry{Body: "Backups occasionally time out", OccurredAt: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), Project: "Infra", Type: journal.TypeProblem, Tags: []string{"backups"}})
	add("oldfixed", journal.NewEntry{Body: "Disk alert noise", OccurredAt: time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC), Project: "Infra", Type: journal.TypeProblem})
	yes := true
	if _, err := s.Update(ctx, f.ids["oldfixed"], journal.Patch{Resolved: &yes}); err != nil {
		t.Fatal(err)
	}
	add("q3win", journal.NewEntry{Body: "Migrated build agents to ARM", OccurredAt: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), Project: "Infra", Type: journal.TypeAccomplishment, Tags: []string{"arm"}})
	return f
}

func (f fixture) build(t *testing.T, k Kind, expr string) Report {
	t.Helper()
	r, err := f.b.Clock.Parse(expr)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := f.b.Build(context.Background(), k, Options{Range: r})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func sectionIDs(r Report, key string) []int64 {
	for _, s := range r.Sections {
		if s.Key == key {
			var out []int64
			for _, it := range s.Items {
				out = append(out, it.EntryIDs...)
			}
			return out
		}
	}
	return nil
}

func has(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestStaffReport(t *testing.T) {
	f := setup(t)
	r := f.build(t, Staff, "this-week")
	want := map[string][]string{
		"completed":  {"analyzer", "pinned"},
		"decisions":  {"deploy"},
		"problems":   {"cloudtrail", "restarts2"},
		"cross-team": {"arm"},
	}
	for key, names := range want {
		got := sectionIDs(r, key)
		if len(got) != len(names) {
			t.Errorf("section %s = %v, want %v", key, got, names)
		}
		for _, n := range names {
			if !has(got, f.ids[n]) {
				t.Errorf("section %s missing %s (#%d); got %v", key, n, f.ids[n], got)
			}
		}
	}
	// Marked entries rank first in their section.
	if got := sectionIDs(r, "completed"); got[0] != f.ids["pinned"] {
		t.Errorf("staff-marked entry should lead Completed, got %v", got)
	}
	// Routine work and notes stay out; the cross-team follow-up is not repeated.
	all := r.SourceIDs()
	for _, n := range []string{"queue", "lunch", "exporter", "commit0", "oldproblem"} {
		if has(all, f.ids[n]) {
			t.Errorf("staff report should not include %s", n)
		}
	}
	if has(sectionIDs(r, "upcoming"), f.ids["arm"]) {
		t.Error("an entry must appear in only one section")
	}
	if len(r.Summary) == 0 || !strings.Contains(r.Summary[0], "holocron mark") {
		t.Errorf("summary should explain left-out entries: %v", r.Summary)
	}
	// Provenance: every referenced entry is available with its reasons.
	for _, s := range r.Sections {
		for _, it := range s.Items {
			if len(it.EntryIDs) == 0 || len(it.Reasons) == 0 {
				t.Errorf("item %q lacks provenance", it.Text)
			}
			for _, id := range it.EntryIDs {
				if r.Entries[id].ID != id {
					t.Errorf("source #%d missing from report", id)
				}
			}
		}
	}
}

func TestOneOnOneReport(t *testing.T) {
	f := setup(t)
	r := f.build(t, OneOnOne, "this-week")
	if !has(sectionIDs(r, "discuss"), f.ids["talk"]) {
		t.Error("one-on-one mark not in To discuss")
	}
	if !has(sectionIDs(r, "wins"), f.ids["analyzer"]) {
		t.Error("accomplishment not in Wins")
	}
	problems := sectionIDs(r, "problems")
	if !has(problems, f.ids["oldproblem"]) {
		t.Error("open problem from last month (inside lookback) missing")
	}
	if has(problems, f.ids["oldfixed"]) {
		t.Error("resolved problem listed as open")
	}
	if !has(sectionIDs(r, "follow-ups"), f.ids["arm"]) {
		t.Error("open follow-up missing")
	}
	friction := sectionIDs(r, "friction")
	if !has(friction, f.ids["exporter"]) || !has(friction, f.ids["restarts2"]) {
		t.Errorf("recurring monitoring friction not detected: %v", friction)
	}
}

func TestWeekReport(t *testing.T) {
	f := setup(t)
	r := f.build(t, Week, "this-week")
	if len(r.Sections) == 0 || r.Sections[0].Title == noProject {
		t.Fatalf("project sections should come first: %+v", r.Sections)
	}
	if last := r.Sections[len(r.Sections)-1]; last.Key != "open" {
		t.Errorf("open items should close the week report, got %q", last.Key)
	}
	var holo *Section
	for i := range r.Sections {
		if r.Sections[i].Title == "Holocron" {
			holo = &r.Sections[i]
		}
	}
	if holo == nil || len(holo.Items) != 1 || len(holo.Items[0].EntryIDs) != 4 || !strings.Contains(holo.Items[0].Text, "4 commits") {
		t.Fatalf("imported commits should collapse into one item: %+v", holo)
	}
	if !strings.Contains(strings.Join(r.Summary, " "), "Active on 3 days") {
		t.Errorf("summary = %v", r.Summary)
	}

	// Caps leave a note rather than silently dropping entries.
	small, err := f.b.Build(context.Background(), Week, Options{Range: r.Range, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range small.Sections {
		if s.Key == "project:infra" && (s.Omitted == 0 || !strings.Contains(s.Note, "lower-priority")) {
			t.Errorf("capped section should note omissions: %+v", s)
		}
	}
}

func TestDayAndQuarter(t *testing.T) {
	f := setup(t)
	day := f.build(t, Day, "2026-10-06")
	tl := sectionIDs(day, "timeline")
	if len(tl) != 7 { // 3 hand-written + 4 commits collapsed into one item
		t.Errorf("day timeline ids = %v", tl)
	}
	for i := 1; i < len(day.Sections[0].Items); i++ {
		if day.Sections[0].Items[i].Time.Before(day.Sections[0].Items[i-1].Time) {
			t.Error("day timeline is not chronological")
		}
	}

	q := f.build(t, Quarter, "2026-Q3")
	if !has(q.SourceIDs(), f.ids["q3win"]) {
		t.Error("quarter report missing accomplishment")
	}
	if has(q.SourceIDs(), f.ids["oldproblem"]) {
		t.Error("routine problem should be counted, not listed")
	}
	found := false
	for _, s := range q.Sections {
		if s.Title == "Infra" && strings.Contains(s.Note, "Active in") && strings.Contains(s.Note, "routine") {
			found = true
		}
	}
	if !found {
		t.Errorf("quarter project note missing: %+v", q.Sections)
	}

	q4 := f.build(t, Quarter, "this-quarter")
	themes := sectionIDs(q4, "themes")
	if len(themes) != 0 {
		t.Errorf("no tag reaches three uses this quarter, got themes %v", themes)
	}
}

func TestEmptyReports(t *testing.T) {
	f := setup(t)
	for _, k := range Kinds {
		r := f.build(t, k, "2025-01-01")
		if !r.IsEmpty() {
			t.Errorf("%s report for an empty day has items", k)
		}
		var buf bytes.Buffer
		if err := Text(&buf, r, RenderOptions{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), r.Title) || len(buf.String()) < 40 {
			t.Errorf("%s empty render: %q", k, buf.String())
		}
	}
}

func TestRenderers(t *testing.T) {
	f := setup(t)
	r := f.build(t, Staff, "this-week")
	opts := RenderOptions{ShowIDs: true, Explain: true, Loc: time.UTC, Styler: style.New(false)}

	var text bytes.Buffer
	if err := Text(&text, r, opts); err != nil {
		t.Fatal(err)
	}
	out := text.String()
	for _, want := range []string{"Staff update", "COMPLETED", "DECISIONS", "Enabled IAM Access Analyzer", fmt.Sprintf("#%d", f.ids["analyzer"]), "why: type accomplishment", "AWS:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain text output contains ANSI escapes")
	}

	var md bytes.Buffer
	if err := Markdown(&md, r, opts); err != nil {
		t.Fatal(err)
	}
	mdOut := md.String()
	for _, want := range []string{"# Staff update", "## Completed", "- **AWS:** Enabled IAM Access Analyzer", "_why:"} {
		if !strings.Contains(mdOut, want) {
			t.Errorf("markdown missing %q:\n%s", want, mdOut)
		}
	}

	var js bytes.Buffer
	if err := JSON(&js, r); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Format   string `json:"format"`
		Sections []struct {
			Items []struct {
				EntryIDs []int64  `json:"entry_ids"`
				Reasons  []string `json:"reasons"`
			} `json:"items"`
		} `json:"sections"`
		Sources []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, js.String())
	}
	if decoded.Format != JSONVersion || len(decoded.Sources) != len(r.SourceIDs()) {
		t.Fatalf("JSON format/sources wrong: %s, %d sources", decoded.Format, len(decoded.Sources))
	}
}

func TestParseKind(t *testing.T) {
	for in, want := range map[string]Kind{"1on1": OneOnOne, "Weekly": Week, "staff": Staff, "quarterly": Quarter, "day": Day} {
		if got, err := ParseKind(in); err != nil || got != want {
			t.Errorf("ParseKind(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseKind("annual"); err == nil {
		t.Error("expected error")
	}
}

func TestEachEntryAppearsOnce(t *testing.T) {
	f := setup(t)
	for _, k := range Kinds {
		for _, expr := range []string{"this-week", "this-quarter", "2026-10-06"} {
			r := f.build(t, k, expr)
			seen := map[int64]string{}
			for _, s := range r.Sections {
				if s.Key == "friction" || s.Key == "themes" {
					continue // summaries that deliberately refer back to listed entries
				}
				for _, it := range s.Items {
					for _, id := range it.EntryIDs {
						if prev, dup := seen[id]; dup {
							t.Errorf("%s/%s: entry #%d appears in %q and %q", k, expr, id, prev, s.Title)
						}
						seen[id] = s.Title
					}
				}
			}
		}
	}
}
