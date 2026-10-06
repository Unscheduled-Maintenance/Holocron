package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

var base = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := base
	return NewStore(db, WithClock(func() time.Time { clock = clock.Add(time.Second); return clock }), WithLocation(time.UTC))
}

func add(t *testing.T, s *Store, n NewEntry) Entry {
	t.Helper()
	e, err := s.AddEntry(context.Background(), n)
	if err != nil {
		t.Fatalf("AddEntry(%q): %v", n.Body, err)
	}
	return e
}

func ids(es []Entry) []int64 {
	out := []int64{}
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestEntryCRUD(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	at := base.Add(-2 * time.Hour)
	e := add(t, s, NewEntry{
		Body: "  Investigated an S3 policy issue \r\n", OccurredAt: at, Type: TypeInvestigation,
		Project: "aws", CreateProject: true, Tags: []string{"Security", "s3,security"}, Marks: []Mark{MarkStaff},
	})
	if e.ID != 1 || len(e.UID) != 26 {
		t.Fatalf("unexpected identity %d %q", e.ID, e.UID)
	}
	if e.Body != "Investigated an S3 policy issue" {
		t.Fatalf("body not normalised: %q", e.Body)
	}
	if !e.OccurredAt.Equal(at) || e.Project != "aws" || e.Type != TypeInvestigation {
		t.Fatalf("fields not stored: %+v", e)
	}
	if !reflect.DeepEqual(e.Tags, []string{"s3", "security"}) {
		t.Fatalf("tags = %v", e.Tags)
	}
	if !e.HasMark(MarkStaff) || e.Ref() != "#1" {
		t.Fatalf("marks/ref wrong: %+v", e)
	}

	got, err := s.Get(ctx, "#1")
	if err != nil || got.UID != e.UID {
		t.Fatalf("Get(#1) = %v, %v", got.UID, err)
	}
	got, err = s.Get(ctx, strings.ToLower(e.UID))
	if err != nil || got.ID != 1 {
		t.Fatalf("Get(uid) = %v, %v", got.ID, err)
	}

	body := "Investigated and fixed an S3 policy issue"
	none := ""
	typ := TypeAccomplishment
	upd, err := s.Update(ctx, e.ID, Patch{Body: &body, Type: &typ, Project: &none, AddTags: []string{"iam"}, RemoveTags: []string{"S3"}, RemoveMarks: []Mark{MarkStaff}, AddMarks: []Mark{MarkQuarterly}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Body != body || upd.Type != TypeAccomplishment || upd.Project != "" {
		t.Fatalf("update not applied: %+v", upd)
	}
	if !reflect.DeepEqual(upd.Tags, []string{"iam", "security"}) || !reflect.DeepEqual(upd.Marks, []Mark{MarkQuarterly}) {
		t.Fatalf("tags/marks after update: %v %v", upd.Tags, upd.Marks)
	}
	if !upd.UpdatedAt.After(e.UpdatedAt) || !upd.CreatedAt.Equal(e.CreatedAt) {
		t.Fatalf("timestamps: created %v→%v updated %v→%v", e.CreatedAt, upd.CreatedAt, e.UpdatedAt, upd.UpdatedAt)
	}

	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry still found: %v", err)
	}
	if err := s.Delete(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	tags, _ := s.ListTags(ctx)
	if len(tags) != 0 {
		t.Fatalf("orphan tags remain: %v", tags)
	}

	// IDs are never reused, so an old reference cannot point at a new record.
	next := add(t, s, NewEntry{Body: "Something else"})
	if next.ID != 2 {
		t.Fatalf("new entry reused ID: got #%d", next.ID)
	}
	if _, err := s.Get(ctx, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("#1 should stay missing, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	cases := []NewEntry{
		{Body: "   "},
		{Body: "x", Type: "chore"},
		{Body: "x", Tags: []string{"has space"}},
		{Body: "x", Marks: []Mark{"lunch"}},
		{Body: "x", Project: "unknown-project"},
	}
	for _, n := range cases {
		if _, err := s.AddEntry(ctx, n); err == nil {
			t.Errorf("AddEntry(%+v) succeeded", n)
		}
	}
	if n, _ := s.Count(ctx, Query{}); n != 0 {
		t.Fatalf("failed adds left %d entries", n)
	}
	if _, err := s.Update(ctx, 99, Patch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	for _, ref := range []string{"", "abc", "-3", "#0"} {
		if _, err := s.Get(ctx, ref); err == nil {
			t.Errorf("Get(%q) succeeded", ref)
		}
	}
}

func TestRestoreKeepsIdentity(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	e := add(t, s, NewEntry{Body: "Fixed CloudTrail bucket logging policy", Project: "aws", CreateProject: true, Tags: []string{"logging"}, Type: TypeProblem})
	resolved := true
	e, _ = s.Update(ctx, e.ID, Patch{Resolved: &resolved})
	add(t, s, NewEntry{Body: "Later entry"})
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.Restore(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != e.ID || r.UID != e.UID || !r.CreatedAt.Equal(e.CreatedAt) || r.ResolvedAt == nil || r.Project != "aws" {
		t.Fatalf("restore lost identity: %+v vs %+v", r, e)
	}
	found, _ := s.Find(ctx, Query{Text: "cloudtrail"})
	if len(found) != 1 || found[0].ID != e.ID {
		t.Fatalf("restored entry not searchable: %v", ids(found))
	}
}

func TestSearchIndexFollowsEdits(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := add(t, s, NewEntry{Body: "Enabled IAM Access Analyzer in all AWS regions", Project: "aws", CreateProject: true, Tags: []string{"security"}})
	b := add(t, s, NewEntry{Body: "Investigating unexpected exporter restarts", Tags: []string{"monitoring"}})
	add(t, s, NewEntry{Body: "Reviewed repository moderation queue"})

	search := func(text string) []int64 {
		t.Helper()
		res, err := s.Find(ctx, Query{Text: text})
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		return ids(res)
	}
	check := func(text string, want ...int64) {
		t.Helper()
		got := search(text)
		if want == nil {
			want = []int64{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("search %q = %v, want %v", text, got, want)
		}
	}

	check("analyzer", a.ID)
	check("ANALY", a.ID)             // prefix, case-insensitive
	check("investigated", b.ID)      // suffix relaxation
	check("restart", b.ID)           // prefix of "restarts"
	check("monitoring", b.ID)        // tag text is indexed
	check("aws", a.ID)               // project name and body
	check(`"access analyzer"`, a.ID) // phrase
	check(`"analyzer access"`)       // wrong phrase order
	check("regions -iam")            // exclusion
	check("exporter OR moderation", 3, b.ID)
	check(`weird "quotes and (parens) *`) // must not error

	if _, err := s.Find(ctx, Query{Text: "-iam"}); err == nil {
		t.Error("exclusion-only query should be rejected")
	}

	// Editing the body must update the index.
	body := "Disabled the legacy exporter"
	if _, err := s.Update(ctx, b.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	check("restarts")
	check("legacy", b.ID)

	// Retagging must update the index.
	if _, err := s.Update(ctx, b.ID, Patch{Tags: &[]string{"observability"}}); err != nil {
		t.Fatal(err)
	}
	check("monitoring")
	check("observability", b.ID)

	// Renaming a project or adding an alias must update the index.
	if _, err := s.UpdateProject(ctx, "aws", ProjectPatch{Name: ptr("Amazon Web Services"), AddAliases: []string{"cloud"}}); err != nil {
		t.Fatal(err)
	}
	check("amazon", a.ID)
	check("cloud", a.ID)

	// Renaming a tag must update the index.
	if _, err := s.RenameTag(ctx, "security", "secops"); err != nil {
		t.Fatal(err)
	}
	check("secops", a.ID)

	// Deleting removes the row from the index.
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	check("analyzer")
	if err := s.CheckIndex(ctx); err != nil {
		t.Fatalf("index check after edits: %v", err)
	}

	// Rebuilding the index is lossless.
	n, err := s.RebuildIndex(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RebuildIndex = %d, %v", n, err)
	}
	check("legacy", b.ID)
}

func TestFindFilters(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	day := func(d int, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC) }
	e1 := add(t, s, NewEntry{Body: "Enabled analyzer", OccurredAt: day(1, 9), Project: "aws", CreateProject: true, Tags: []string{"security", "iam"}, Type: TypeAccomplishment})
	e2 := add(t, s, NewEntry{Body: "Bucket policy decision", OccurredAt: day(15, 10), Project: "aws", Tags: []string{"security"}, Type: TypeDecision, Marks: []Mark{MarkStaff}})
	e3 := add(t, s, NewEntry{Body: "Exporter restarts", OccurredAt: day(20, 11), Project: "infra", CreateProject: true, Type: TypeProblem})
	e4 := add(t, s, NewEntry{Body: "Lunch and learn", OccurredAt: day(30, 23)})
	if _, err := s.UpdateProject(ctx, "infra", ProjectPatch{AddAliases: []string{"ops"}}); err != nil {
		t.Fatal(err)
	}

	rng := timerange.Range{Start: day(15, 0), End: day(30, 0).AddDate(0, 0, 1)}
	cases := []struct {
		name string
		q    Query
		want []int64
	}{
		{"all newest first", Query{}, []int64{e4.ID, e3.ID, e2.ID, e1.ID}},
		{"oldest first", Query{Order: OldestFirst}, []int64{e1.ID, e2.ID, e3.ID, e4.ID}},
		{"range inclusive of last day", Query{Range: rng}, []int64{e4.ID, e3.ID, e2.ID}},
		{"project", Query{Projects: []string{"aws"}}, []int64{e2.ID, e1.ID}},
		{"project alias", Query{Projects: []string{"OPS"}}, []int64{e3.ID}},
		{"no project", Query{Projects: []string{"-"}}, []int64{e4.ID}},
		{"tags are ANDed", Query{Tags: []string{"security", "iam"}}, []int64{e1.ID}},
		{"types are ORed", Query{Types: []Type{TypeDecision, TypeProblem}}, []int64{e3.ID, e2.ID}},
		{"marks", Query{Marks: []Mark{MarkStaff}}, []int64{e2.ID}},
		{"open", Query{OpenOnly: true}, []int64{e3.ID}},
		{"text plus project", Query{Text: "policy", Projects: []string{"aws"}}, []int64{e2.ID}},
		{"limit", Query{Limit: 2}, []int64{e4.ID, e3.ID}},
		{"offset", Query{Limit: 2, Offset: 2}, []int64{e2.ID, e1.ID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Find(ctx, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(got), c.want) {
				t.Fatalf("got %v, want %v", ids(got), c.want)
			}
			n, err := s.Count(ctx, Query{Text: c.q.Text, Range: c.q.Range, Projects: c.q.Projects, Tags: c.q.Tags, Types: c.q.Types, Marks: c.q.Marks, OpenOnly: c.q.OpenOnly})
			if err != nil {
				t.Fatal(err)
			}
			if c.q.Limit == 0 && n != len(c.want) {
				t.Fatalf("Count = %d, want %d", n, len(c.want))
			}
		})
	}
	if _, err := s.Find(ctx, Query{Projects: []string{"nope"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project filter should fail clearly, got %v", err)
	}

	// Resolving closes an open item; reopening restores it.
	yes, no := true, false
	if _, err := s.Update(ctx, e3.ID, Patch{Resolved: &yes}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Find(ctx, Query{OpenOnly: true}); len(got) != 0 {
		t.Fatalf("resolved item still open: %v", ids(got))
	}
	if _, err := s.Update(ctx, e3.ID, Patch{Resolved: &no}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Find(ctx, Query{OpenOnly: true}); len(got) != 1 {
		t.Fatalf("reopened item not open: %v", ids(got))
	}
}

func TestSearchSnippetHighlights(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	add(t, s, NewEntry{Body: "Found unexpected CloudTrail configuration"})
	res, err := s.Find(ctx, Query{Text: "cloudtrail"})
	if err != nil || len(res) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	if !strings.Contains(res[0].Snippet, HighlightStart+"CloudTrail"+HighlightEnd) {
		t.Fatalf("snippet not highlighted: %q", res[0].Snippet)
	}
}

func TestProjects(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, err := s.CreateProject(ctx, NewProject{Name: " Chocolatey   Infrastructure ", Aliases: []string{"infra", "ops"}, Description: "Servers", Paths: []string{"repo"}, URLs: []string{"https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Chocolatey Infrastructure" || !reflect.DeepEqual(p.Aliases, []string{"infra", "ops"}) || len(p.Paths) != 1 || !filepath.IsAbs(p.Paths[0]) {
		t.Fatalf("project = %+v", p)
	}
	for _, n := range []string{"chocolatey infrastructure", "INFRA", "ops"} {
		if _, err := s.CreateProject(ctx, NewProject{Name: n}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate project %q: %v", n, err)
		}
	}
	other, _ := s.CreateProject(ctx, NewProject{Name: "aws"})
	if _, err := s.UpdateProject(ctx, "aws", ProjectPatch{AddAliases: []string{"ops"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stealing an alias: %v", err)
	}
	if _, err := s.UpdateProject(ctx, "aws", ProjectPatch{Name: ptr("infra")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("renaming onto an alias: %v", err)
	}
	e := add(t, s, NewEntry{Body: "Patched servers", Project: "ops"})
	if e.Project != "Chocolatey Infrastructure" {
		t.Fatalf("alias not resolved: %q", e.Project)
	}

	inside := filepath.Join(p.Paths[0], "sub", "dir")
	got, ok, err := s.ProjectForPath(ctx, inside)
	if err != nil || !ok || got.ID != p.ID {
		t.Fatalf("ProjectForPath = %v %v %v", got.Name, ok, err)
	}
	if _, ok, _ := s.ProjectForPath(ctx, t.TempDir()); ok {
		t.Fatal("unrelated path matched a project")
	}

	if _, err := s.UpdateProject(ctx, "infra", ProjectPatch{Archived: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	active, _ := s.ListProjects(ctx, false)
	if len(active) != 1 || active[0].ID != other.ID {
		t.Fatalf("archived project listed: %+v", active)
	}
	all, _ := s.ListProjects(ctx, true)
	if len(all) != 2 || all[1].EntryCount != 1 {
		t.Fatalf("ListProjects(all) = %+v", all)
	}

	n, err := s.DeleteProject(ctx, "infra")
	if err != nil || n != 1 {
		t.Fatalf("DeleteProject = %d, %v", n, err)
	}
	kept, err := s.GetByID(ctx, e.ID)
	if err != nil || kept.Project != "" {
		t.Fatalf("entry after project delete: %+v %v", kept, err)
	}
	if res, _ := s.Find(ctx, Query{Text: "infrastructure"}); len(res) != 0 {
		t.Fatal("deleted project name still indexed")
	}
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	add(t, s, NewEntry{Body: "a", Tags: []string{"sec"}})
	add(t, s, NewEntry{Body: "b", Tags: []string{"sec", "security"}})
	add(t, s, NewEntry{Body: "c", Tags: []string{"security"}})
	n, err := s.RenameTag(ctx, "sec", "security")
	if err != nil || n != 2 {
		t.Fatalf("RenameTag = %d, %v", n, err)
	}
	tags, _ := s.ListTags(ctx)
	if len(tags) != 1 || tags[0].Name != "security" || tags[0].Count != 3 {
		t.Fatalf("tags after merge: %+v", tags)
	}
	if _, err := s.RenameTag(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing: %v", err)
	}
}

func TestImportDeduplication(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	items := []NewEntry{
		{Body: "Fix build", Source: &Source{Type: "git", ID: "abc123"}},
		{Body: "Add tests", Source: &Source{Type: "git", ID: "def456", URL: "https://example.com/c/def456"}},
	}
	created, skipped, err := s.ImportEntries(ctx, items)
	if err != nil || len(created) != 2 || skipped != 0 {
		t.Fatalf("first import = %d created, %d skipped, %v", len(created), skipped, err)
	}
	if created[1].Source == nil || created[1].Source.URL == "" || created[1].Source.ImportedAt.IsZero() {
		t.Fatalf("provenance not stored: %+v", created[1].Source)
	}
	items = append(items, NewEntry{Body: "New commit", Source: &Source{Type: "git", ID: "fff999"}})
	created, skipped, err = s.ImportEntries(ctx, items)
	if err != nil || len(created) != 1 || skipped != 2 {
		t.Fatalf("second import = %d created, %d skipped, %v", len(created), skipped, err)
	}
	seen, err := s.ImportedIDs(ctx, "git", []string{"abc123", "zzz", "fff999"})
	if err != nil || len(seen) != 2 {
		t.Fatalf("ImportedIDs = %v, %v", seen, err)
	}
	if _, err := s.AddEntry(ctx, items[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct duplicate insert: %v", err)
	}
	if n, _ := s.Count(ctx, Query{}); n != 3 {
		t.Fatalf("archive has %d entries, want 3", n)
	}
	imported := true
	if n, _ := s.Count(ctx, Query{Imported: &imported, SourceType: "git"}); n != 3 {
		t.Fatalf("imported count %d", n)
	}
}

func TestOffsetPreserved(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "h.db"), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loc := time.FixedZone("NZDT", 13*3600)
	s := NewStore(db, WithLocation(loc))
	at := time.Date(2026, 10, 5, 9, 14, 0, 0, loc)
	e, err := s.AddEntry(ctx, NewEntry{Body: "Morning work", OccurredAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if e.UTCOffset != 13*3600 || e.OccurredAt.Location() != time.UTC || !e.OccurredAt.Equal(at) {
		t.Fatalf("offset/instant: %d %v", e.UTCOffset, e.OccurredAt)
	}
	if got := e.OccurredAt.In(e.RecordedOffset()).Format("15:04"); got != "09:14" {
		t.Fatalf("recorded wall clock = %s", got)
	}
}

func TestShorthand(t *testing.T) {
	cases := []struct {
		in      string
		body    string
		project string
		tags    []string
		typ     Type
	}{
		{"Fixed S3 bucket policy +aws #security", "Fixed S3 bucket policy", "aws", []string{"security"}, ""},
		{"+aws #security Fixed S3 bucket policy", "Fixed S3 bucket policy", "aws", []string{"security"}, ""},
		{"Patched #security hole in +aws today.", "Patched security hole in aws today.", "aws", []string{"security"}, ""},
		{"Fixed bug #123 and C# parser, +1 from team", "Fixed bug #123 and C# parser, +1 from team", "", nil, ""},
		{"Decision: retain the existing deployment model #arch", "retain the existing deployment model", "", []string{"arch"}, TypeDecision},
		{"decision:no space after the colon", "no space after the colon", "", nil, TypeDecision},
		{"PROBLEM :   exporter restarts", "exporter restarts", "", nil, TypeProblem},
		{"+aws Accomplishment: rotated keys", "rotated keys", "aws", nil, TypeAccomplishment},
		{"Note: first line\nsecond line", "first line\nsecond line", "", nil, TypeNote},
		{"Decisions: not a type name", "Decisions: not a type name", "", nil, ""},
		{"Decision:", "Decision:", "", nil, ""},
		{"Mid-sentence decision: stays as text", "Mid-sentence decision: stays as text", "", nil, ""},
		{"Follow-up: ask about ARM capacity", "ask about ARM capacity", "", nil, TypeFollowUp},
		{"followup: ask again", "ask again", "", nil, TypeFollowUp},
		{"Shipped it #release.", "Shipped it", "", []string{"release"}, ""},
		{"line one\nline two #tag", "line one\nline two", "", []string{"tag"}, ""},
		{"#only #tags", "", "", []string{"only", "tags"}, ""},
	}
	for _, c := range cases {
		got, err := ParseShorthand(c.in, nil)
		if err != nil {
			t.Errorf("ParseShorthand(%q): %v", c.in, err)
			continue
		}
		if got.Body != c.body || got.Project != c.project || !reflect.DeepEqual(got.Tags, c.tags) || got.Type != c.typ {
			t.Errorf("ParseShorthand(%q) = %+v, want body=%q project=%q tags=%v type=%q", c.in, got, c.body, c.project, c.tags, c.typ)
		}
	}
	if _, err := ParseShorthand("+aws +gcp moved things", nil); err == nil {
		t.Error("two projects should be an error")
	}
}

func TestTypeAliases(t *testing.T) {
	al, err := NewTypeAliases(DefaultTypeAliases())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(al, TypeAliases{"win": TypeAccomplishment, "look": TypeInvestigation}) {
		t.Fatalf("default aliases = %v", al)
	}
	for in, want := range map[string]Type{"win": TypeAccomplishment, "WIN": TypeAccomplishment, "Look": TypeInvestigation, "dec": TypeDecision, "work": TypeWork, "": TypeNone} {
		if got, err := al.Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"wi", "looking", "chore"} {
		if _, err := al.Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded: aliases match exactly", bad)
		}
	}
	if _, err := ParseType("win"); err == nil {
		t.Error("ParseType must not know about aliases")
	}

	// An exact alias beats a type prefix ("w" would otherwise mean work).
	custom, err := NewTypeAliases(map[string]string{"w": "acc", "Ship": "accomplishment", "look": ""})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(custom, TypeAliases{"w": TypeAccomplishment, "ship": TypeAccomplishment}) {
		t.Fatalf("custom aliases = %v", custom)
	}

	for name, in := range map[string]map[string]string{
		"type name":      {"decision": "accomplishment"},
		"type variant":   {"followup": "note"},
		"unknown target": {"win": "victory"},
		"none target":    {"win": "none"},
		"bad characters": {"big win": "accomplishment"},
		"leading digit":  {"1on1": "note"},
	} {
		if _, err := NewTypeAliases(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: NewTypeAliases(%v) = %v, want ErrInvalid", name, in, err)
		}
	}
}

func TestShorthandTypeAliases(t *testing.T) {
	al, err := NewTypeAliases(DefaultTypeAliases())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in, body string
		typ      Type
	}{
		{"Win: rotated the keys +aws", "rotated the keys", TypeAccomplishment},
		{"win:no space", "no space", TypeAccomplishment},
		{"LOOK : why the cache misses", "why the cache misses", TypeInvestigation},
		{"Decision: still works", "still works", TypeDecision},
		{"Wins: plural is text", "Wins: plural is text", TypeNone},
		{"Dec: prefixes are text", "Dec: prefixes are text", TypeNone},
		{"Big win: mid-sentence", "Big win: mid-sentence", TypeNone},
	}
	for _, c := range cases {
		got, err := ParseShorthand(c.in, al)
		if err != nil {
			t.Errorf("ParseShorthand(%q): %v", c.in, err)
			continue
		}
		if got.Body != c.body || got.Type != c.typ {
			t.Errorf("ParseShorthand(%q) = body %q type %q, want %q %q", c.in, got.Body, got.Type, c.body, c.typ)
		}
	}
	if got, _ := ParseShorthand("Win: no aliases configured", nil); got.Type != TypeNone || got.Body != "Win: no aliases configured" {
		t.Errorf("without aliases, Win: is text: %+v", got)
	}
}

func TestBuildMatch(t *testing.T) {
	cases := map[string]string{
		"cloudtrail":         `("cloudtrail"*)`,
		"investigating logs": `(("investigating"* OR "investigat"*) AND "logs"*)`,
		`"exact phrase"`:     `("exact phrase")`,
		"a OR b":             `("a"* OR "b"*)`,
		"alpha -beta":        `("alpha"*) NOT "beta"*`,
		`say "hi`:            `("say"* AND "hi")`,
		"***":                ``,
		`s3://bucket`:        `("s3://bucket"*)`,
	}
	for in, want := range cases {
		got, err := BuildMatch(in)
		if err != nil {
			t.Errorf("BuildMatch(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("BuildMatch(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParsers(t *testing.T) {
	for in, want := range map[string]Type{"dec": TypeDecision, "followup": TypeFollowUp, "Investigation": TypeInvestigation, "acc": TypeAccomplishment, "": TypeNone} {
		if got, err := ParseType(in); err != nil || got != want {
			t.Errorf("ParseType(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"chore", "n0"} {
		if _, err := ParseType(bad); err == nil {
			t.Errorf("ParseType(%q) succeeded", bad)
		}
	}
	marks, err := ParseMarks([]string{"staff,1on1", "imp"})
	if err != nil || !reflect.DeepEqual(marks, []Mark{MarkImportant, MarkOneOnOne, MarkStaff}) {
		t.Fatalf("ParseMarks = %v, %v", marks, err)
	}
	if id, uid, err := ParseRef(" #42 "); err != nil || id != 42 || uid != "" {
		t.Fatalf("ParseRef = %d %q %v", id, uid, err)
	}
	u := NewUID(time.Now())
	if !IsUID(u) {
		t.Fatalf("NewUID produced %q", u)
	}
	if a, b := NewUID(time.Now()), NewUID(time.Now()); a == b {
		t.Fatal("UIDs collide")
	}
	early, late := NewUID(time.Unix(1000, 0)), NewUID(time.Unix(2000, 0))
	if early >= late {
		t.Fatalf("UIDs not time ordered: %s %s", early, late)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSplitSearch(t *testing.T) {
	text, projects, tags := SplitSearch(`cloudtrail +aws "bucket policy" #security -noise`)
	if text != `cloudtrail "bucket policy" -noise` || !reflect.DeepEqual(projects, []string{"aws"}) || !reflect.DeepEqual(tags, []string{"security"}) {
		t.Fatalf("SplitSearch = %q %v %v", text, projects, tags)
	}
}

func TestParseStoredTimeMatchesTimeParse(t *testing.T) {
	for _, s := range []string{
		"2026-10-05T09:14:00.000Z", "2026-02-28T23:59:59.999Z", "2024-02-29T00:00:00.001Z", "1999-12-31T12:30:45.120Z",
	} {
		fast, ok := parseStoredTime(s)
		slow, err := time.Parse(dbTimeLayout, s)
		if !ok || err != nil || !fast.Equal(slow) || fast.Location() != time.UTC {
			t.Errorf("%s: fast=%v ok=%v slow=%v err=%v", s, fast, ok, slow, err)
		}
	}
	for _, s := range []string{"", "2026-10-05T09:14:00Z", "2026-02-30T00:00:00.000Z", "2026-13-01T00:00:00.000Z",
		"2026-10-05 09:14:00.000Z", "2026-10-05T25:00:00.000Z", "2026-1x-05T09:14:00.000Z"} {
		if _, ok := parseStoredTime(s); ok {
			t.Errorf("%q should not take the fast path", s)
		}
	}
	// Lenient fallback still works.
	if got, err := parseTime("2026-10-05T09:14:00+13:00"); err != nil || got.Hour() != 20 || got.Day() != 4 {
		t.Errorf("RFC 3339 fallback = %v, %v", got, err)
	}
}

// Repository paths must match however the directory is spelled: through a
// symlink (macOS /var -> /private/var), an 8.3 short name or other letter
// case on Windows. Git reports the resolved form.
func TestProjectForPathCanonicalises(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	real := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Logf("symlinks unavailable (%v); skipping the symlink case", err)
	} else {
		p, err := s.CreateProject(ctx, NewProject{Name: "via-link", Paths: []string{link}})
		if err != nil {
			t.Fatal(err)
		}
		resolved, _ := filepath.EvalSymlinks(filepath.Join(real, "sub"))
		got, ok, err := s.ProjectForPath(ctx, resolved)
		if err != nil || !ok || got.ID != p.ID {
			t.Fatalf("project registered through a symlink not found for %s: %v %v", resolved, ok, err)
		}
		if _, err := s.DeleteProject(ctx, "via-link"); err != nil {
			t.Fatal(err)
		}
	}

	if runtime.GOOS == "windows" {
		p, err := s.CreateProject(ctx, NewProject{Name: "case", Paths: []string{strings.ToUpper(real)}})
		if err != nil {
			t.Fatal(err)
		}
		if got, ok, _ := s.ProjectForPath(ctx, strings.ToLower(filepath.Join(real, "sub"))); !ok || got.ID != p.ID {
			t.Fatal("Windows paths should match regardless of letter case")
		}
		short := `C:\PROGRA~1`
		if long, err := filepath.EvalSymlinks(short); err == nil && !strings.EqualFold(long, short) {
			p, err := s.CreateProject(ctx, NewProject{Name: "short", Paths: []string{short}})
			if err != nil {
				t.Fatal(err)
			}
			if got, ok, _ := s.ProjectForPath(ctx, filepath.Join(long, "Some Tool")); !ok || got.ID != p.ID {
				t.Fatalf("project registered by 8.3 name %s not found under %s", short, long)
			}
		}
	}
}

func TestResolvesLink(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fu := add(t, s, NewEntry{Body: "ask about ARM capacity", Type: TypeFollowUp, OccurredAt: base})
	pr := add(t, s, NewEntry{Body: "exporter restarts", Type: TypeProblem, OccurredAt: base})
	at := base.Add(48 * time.Hour)
	done := add(t, s, NewEntry{Body: "ARM capacity approved", OccurredAt: at, Resolves: []int64{fu.ID, pr.ID}})
	if !reflect.DeepEqual(done.Resolves, []int64{fu.ID, pr.ID}) {
		t.Fatalf("Resolves = %v", done.Resolves)
	}
	for _, id := range []int64{fu.ID, pr.ID} {
		got, err := s.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ResolvedAt == nil || !got.ResolvedAt.Equal(at) || got.ResolvedBy != done.ID || got.IsOpen() {
			t.Fatalf("#%d resolved = %v by %d", id, got.ResolvedAt, got.ResolvedBy)
		}
	}
	if open, err := s.Find(ctx, Query{OpenOnly: true}); err != nil || len(open) != 0 {
		t.Fatalf("open items = %v, %v", ids(open), err)
	}

	// Resolving an already-resolved or unknown entry saves nothing.
	before, _ := s.Find(ctx, Query{})
	if _, err := s.AddEntry(ctx, NewEntry{Body: "again", Resolves: []int64{fu.ID}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("resolving twice: %v", err)
	}
	if _, err := s.AddEntry(ctx, NewEntry{Body: "nothing", Resolves: []int64{999}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown entry: %v", err)
	}
	if after, _ := s.Find(ctx, Query{}); len(after) != len(before) {
		t.Fatalf("a failed resolve saved an entry: %d → %d", len(before), len(after))
	}

	// Reopening drops the link; resolving by hand leaves it unset.
	reopen, resolve := false, true
	got, err := s.Update(ctx, pr.ID, Patch{Resolved: &reopen})
	if err != nil || got.ResolvedAt != nil || got.ResolvedBy != 0 {
		t.Fatalf("reopen = %v by %d, %v", got.ResolvedAt, got.ResolvedBy, err)
	}
	if got, _ = s.GetByID(ctx, done.ID); !reflect.DeepEqual(got.Resolves, []int64{fu.ID}) {
		t.Fatalf("after reopen Resolves = %v", got.Resolves)
	}
	if got, _ = s.Update(ctx, pr.ID, Patch{Resolved: &resolve}); got.ResolvedAt == nil || got.ResolvedBy != 0 {
		t.Fatalf("manual resolve = %v by %d", got.ResolvedAt, got.ResolvedBy)
	}

	// Deleting the resolver keeps the resolution but drops the link; undo
	// restores the link.
	done, _ = s.GetByID(ctx, done.ID)
	if err := s.Delete(ctx, done.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetByID(ctx, fu.ID); got.ResolvedAt == nil || got.ResolvedBy != 0 {
		t.Fatalf("after deleting the resolver: %v by %d", got.ResolvedAt, got.ResolvedBy)
	}
	if _, err := s.Restore(ctx, done); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetByID(ctx, fu.ID); got.ResolvedBy != done.ID {
		t.Fatalf("restore did not relink: by %d", got.ResolvedBy)
	}

	// Undo of a deleted resolved entry keeps its link to the resolver.
	fu, _ = s.GetByID(ctx, fu.ID)
	if err := s.Delete(ctx, fu.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Restore(ctx, fu); got.ResolvedBy != done.ID || got.ResolvedAt == nil {
		t.Fatalf("restored resolved entry: %v by %d", got.ResolvedAt, got.ResolvedBy)
	}
}

func TestTypeAliasesDescribe(t *testing.T) {
	al := TypeAliases{"win": TypeAccomplishment, "look": TypeInvestigation}
	if got := al.Describe(); got != "look → investigation, win → accomplishment" {
		t.Errorf("Describe = %q", got)
	}
	if got := (TypeAliases{}).Describe(); got != "" {
		t.Errorf("empty Describe = %q", got)
	}
	if got := al.Strings(); !reflect.DeepEqual(got, map[string]string{"win": "accomplishment", "look": "investigation"}) {
		t.Errorf("Strings = %v", got)
	}
}
