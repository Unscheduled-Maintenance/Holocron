package journal

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// device is an archive whose clock the test controls.
type device struct {
	*Store
	t   *testing.T
	now time.Time
}

func newDevice(t *testing.T, start time.Time) *device {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	d := &device{t: t, now: start}
	d.Store = NewStore(db, WithClock(func() time.Time { return d.now }), WithLocation(time.UTC))
	return d
}

// at moves the device's clock.
func (d *device) at(t time.Time) *device { d.now = t; return d }

func (d *device) records(changedOnly bool) Records {
	d.t.Helper()
	r, err := d.ReadRecords(context.Background(), changedOnly)
	if err != nil {
		d.t.Fatal(err)
	}
	return r
}

func (d *device) apply(r Records) ApplyResult {
	d.t.Helper()
	res, err := d.Apply(context.Background(), r, ApplyOptions{})
	if err != nil {
		d.t.Fatal(err)
	}
	return res
}

func (d *device) get(ref string) Entry {
	d.t.Helper()
	e, err := d.Get(context.Background(), ref)
	if err != nil {
		d.t.Fatalf("Get(%s): %v", ref, err)
	}
	return e
}

func (d *device) byUID(uid string) (Entry, bool) {
	e, err := d.Get(context.Background(), uid)
	return e, err == nil
}

// sync exchanges all records both ways.
func sync(a, b *device) {
	ra, rb := a.records(false), b.records(false)
	b.apply(ra)
	a.apply(rb)
}

type view struct {
	Ref, Body, Project string
	Type               Type
	Tags               []string
	Marks              []Mark
	Resolved           bool
	ResolvedBy         string
}

func snapshot(t *testing.T, d *device) map[string]view {
	t.Helper()
	es, err := d.Find(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]view{}
	for _, e := range es {
		out[e.UID] = view{e.Ref(), e.Body, e.Project, e.Type, e.Tags, e.Marks, e.ResolvedAt != nil, e.ResolvedByRef}
	}
	return out
}

var t0 = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func TestMergeRoundTripAndIdempotence(t *testing.T) {
	ctx := context.Background()
	a := newDevice(t, t0)
	if _, err := a.CreateProject(ctx, NewProject{Name: "Infra", Description: "Servers", Aliases: []string{"inf"}, URLs: []string{"https://example.com/infra"}}); err != nil {
		t.Fatal(err)
	}
	fu, _ := a.AddEntry(ctx, NewEntry{Body: "ask about ARM", Type: TypeFollowUp, Project: "inf", Tags: []string{"arm", "capacity"}, Marks: []Mark{MarkStaff}})
	a.at(t0.Add(time.Hour))
	if _, err := a.AddEntry(ctx, NewEntry{Body: "ARM approved", Resolves: []int64{fu.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddEntry(ctx, NewEntry{Body: "commit", Source: &Source{Type: "git", ID: "abc", ImportedAt: t0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecordReport(ctx, "staff", timerange.Range{Start: t0, End: t0.AddDate(0, 0, 7)}); err != nil {
		t.Fatal(err)
	}

	b := newDevice(t, t0.Add(2*time.Hour))
	res := b.apply(a.records(false))
	if res.EntriesAdded != 3 || res.ProjectsAdded != 1 || res.Reports != 1 || len(res.Renumbered) != 0 {
		t.Fatalf("first apply = %+v", res)
	}
	if got, want := snapshot(t, b), snapshot(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("archives differ:\n a: %+v\n b: %+v", want, got)
	}
	p, err := b.Project(ctx, "inf")
	if err != nil || p.Description != "Servers" || len(p.URLs) != 1 {
		t.Fatalf("project on b = %+v, %v", p, err)
	}
	if last, err := b.LastReport(ctx, "staff"); err != nil || !last.Range.End.Equal(t0.AddDate(0, 0, 7)) {
		t.Fatalf("report log on b = %+v, %v", last, err)
	}
	if hits, _ := b.Find(ctx, Query{Text: "approved"}); len(hits) != 1 {
		t.Fatal("merged entries must be searchable")
	}

	// Applying the same records again, either way, changes nothing.
	if res := b.apply(a.records(false)); res.Changed() {
		t.Fatalf("second apply changed things: %+v", res)
	}
	if res := a.apply(b.records(false)); res.Changed() {
		t.Fatalf("apply back changed things: %+v", res)
	}
	// Nothing merged in counts as changed here.
	if r := b.records(true); len(r.Entries)+len(r.Projects) != 0 {
		t.Fatalf("merged rows marked as changed here: %d entries, %d projects", len(r.Entries), len(r.Projects))
	}
}

func TestMergeFieldsIndependently(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0)
	e, _ := a.AddEntry(ctx, NewEntry{Body: "original", Tags: []string{"one"}})
	sync(a, b)
	eb := b.get(e.UID)

	body := "edited on a"
	a.at(t0.Add(time.Hour))
	if _, err := a.Update(ctx, e.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	b.at(t0.Add(2 * time.Hour))
	if _, err := b.Update(ctx, eb.ID, Patch{AddTags: []string{"two"}}); err != nil {
		t.Fatal(err)
	}
	sync(a, b)
	for name, d := range map[string]*device{"a": a, "b": b} {
		got := d.get(e.UID)
		if got.Body != "edited on a" || !reflect.DeepEqual(got.Tags, []string{"one", "two"}) {
			t.Errorf("%s: %q %v", name, got.Body, got.Tags)
		}
	}

	// Both edit the same field: the later change wins on both, whichever
	// order the records arrive in.
	first, second := "first", "second"
	b.at(t0.Add(3 * time.Hour))
	if _, err := b.Update(ctx, eb.ID, Patch{Body: &first}); err != nil {
		t.Fatal(err)
	}
	a.at(t0.Add(4 * time.Hour))
	if _, err := a.Update(ctx, e.ID, Patch{Body: &second}); err != nil {
		t.Fatal(err)
	}
	ra, rb := a.records(true), b.records(true)
	a.apply(rb)
	b.apply(ra)
	if a.get(e.UID).Body != "second" || b.get(e.UID).Body != "second" {
		t.Fatalf("later edit lost: a=%q b=%q", a.get(e.UID).Body, b.get(e.UID).Body)
	}
}

func TestMergeDeletes(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0)
	x, _ := a.AddEntry(ctx, NewEntry{Body: "x"})
	y, _ := a.AddEntry(ctx, NewEntry{Body: "y"})
	sync(a, b)

	// x: edited on b, then deleted on a later: the delete wins.
	b.at(t0.Add(time.Hour))
	body := "x edited"
	if _, err := b.Update(ctx, b.get(x.UID).ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	a.at(t0.Add(2 * time.Hour))
	if err := a.Delete(ctx, x.ID); err != nil {
		t.Fatal(err)
	}
	// y: deleted on a, then edited on b later: the edit brings it back.
	if err := a.Delete(ctx, y.ID); err != nil {
		t.Fatal(err)
	}
	b.at(t0.Add(3 * time.Hour))
	body = "y edited"
	if _, err := b.Update(ctx, b.get(y.UID).ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	sync(a, b)
	for name, d := range map[string]*device{"a": a, "b": b} {
		if _, ok := d.byUID(x.UID); ok {
			t.Errorf("%s: x should be deleted", name)
		}
		if got, ok := d.byUID(y.UID); !ok || got.Body != "y edited" {
			t.Errorf("%s: y = %+v %v", name, got.Body, ok)
		}
	}
	// An old copy of x arriving later does not bring it back.
	stale := Records{Entries: []EntryRecord{{UID: x.UID, Num: 1, Body: "x", OccurredAt: t0, CreatedAt: t0, UpdatedAt: t0,
		Clocks: Clocks{FieldBody: {Wall: t0.UnixMilli(), Device: "Z"}}}}}
	if res := a.apply(stale); res.EntriesAdded != 0 || res.EntriesSkipped != 1 {
		t.Fatalf("stale copy: %+v", res)
	}
}

func TestMergeProjects(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0.Add(time.Minute))
	ea, _ := a.AddEntry(ctx, NewEntry{Body: "on a", Project: "AWS", CreateProject: true})
	eb, _ := b.AddEntry(ctx, NewEntry{Body: "on b", Project: "aws", CreateProject: true})
	pa, _ := a.Project(ctx, "AWS")
	pb, _ := b.Project(ctx, "aws")
	sync(a, b)
	keep := min(pa.UID, pb.UID)
	for name, d := range map[string]*device{"a": a, "b": b} {
		ps, err := d.ListProjects(ctx, true)
		if err != nil || len(ps) != 1 || ps[0].UID != keep {
			t.Fatalf("%s: projects = %+v, %v", name, ps, err)
		}
		for _, uid := range []string{ea.UID, eb.UID} {
			if got := d.get(uid); got.ProjectID != ps[0].ID {
				t.Errorf("%s: entry %s not in the merged project", name, uid)
			}
		}
	}
	// Changes still reach the merged project through the other UID.
	desc := "Amazon"
	b.at(t0.Add(time.Hour))
	if _, err := b.UpdateProject(ctx, "aws", ProjectPatch{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	sync(a, b)
	if p, _ := a.Project(ctx, "AWS"); p.Description != "Amazon" {
		t.Fatalf("description on a = %q", p.Description)
	}

	// Deleting a project reaches the other device.
	a.at(t0.Add(2 * time.Hour))
	if _, err := a.DeleteProject(ctx, "AWS"); err != nil {
		t.Fatal(err)
	}
	b.apply(a.records(true))
	if ps, _ := b.ListProjects(ctx, true); len(ps) != 0 {
		t.Fatalf("project survived its deletion: %+v", ps)
	}
	if got := b.get(ea.UID); got.Project != "" {
		t.Fatalf("entry still has project %q", got.Project)
	}
}

func TestMergeNumbers(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0)
	a1, _ := a.AddEntry(ctx, NewEntry{Body: "a one"})
	b1, _ := b.AddEntry(ctx, NewEntry{Body: "b one"})
	b2, _ := b.AddEntry(ctx, NewEntry{Body: "b two"})

	// Unrelated archives both have #1 and #2: incoming entries that collide
	// take new numbers, and the counter moves past everything seen.
	res := a.apply(b.records(false))
	if len(res.Renumbered) != 1 || res.Renumbered[0].From != "#1" || res.Renumbered[0].To != "#3" {
		t.Fatalf("renumbered = %+v", res.Renumbered)
	}
	if a.get(a1.UID).Ref() != "#1" || a.get(b1.UID).Ref() != "#3" || a.get(b2.UID).Ref() != "#2" {
		t.Fatalf("numbers on a: %s %s %s", a.get(a1.UID).Ref(), a.get(b1.UID).Ref(), a.get(b2.UID).Ref())
	}
	if next, _ := a.AddEntry(ctx, NewEntry{Body: "next"}); next.Ref() != "#4" {
		t.Fatalf("next number = %s", next.Ref())
	}

	// Labelled devices keep their numbers on every device.
	c, d := newDevice(t, t0), newDevice(t, t0)
	if _, err := c.SetSelfLabel(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetSelfLabel(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	c1, _ := c.AddEntry(ctx, NewEntry{Body: "from c"})
	d1, _ := d.AddEntry(ctx, NewEntry{Body: "from d"})
	sync(c, d)
	for name, dev := range map[string]*device{"c": c, "d": d} {
		if dev.get(c1.UID).Ref() != "#1a" || dev.get(d1.UID).Ref() != "#1b" {
			t.Errorf("%s: %s %s", name, dev.get(c1.UID).Ref(), dev.get(d1.UID).Ref())
		}
	}
	if got := d.get("1a").Body; got != "from c" {
		t.Errorf("Get(1a) on d = %q", got)
	}
}

func TestMergeChangedOnly(t *testing.T) {
	ctx := context.Background()
	a := newDevice(t, t0)
	x, _ := a.AddEntry(ctx, NewEntry{Body: "x"})
	a.AddEntry(ctx, NewEntry{Body: "y"})
	all := a.records(true)
	if len(all.Entries) != 2 {
		t.Fatalf("changed entries = %d", len(all.Entries))
	}
	if err := a.MarkPublished(ctx, all); err != nil {
		t.Fatal(err)
	}
	if r := a.records(true); !r.IsEmpty() && (len(r.Entries)+len(r.Projects)+len(r.Tombstones)+len(r.Reports)) != 0 {
		t.Fatalf("after publishing: %+v", r)
	}
	body := "x2"
	if _, err := a.Update(ctx, x.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, x.ID); err != nil {
		t.Fatal(err)
	}
	r := a.records(true)
	if len(r.Entries) != 0 || len(r.Tombstones) != 1 || r.Tombstones[0].UID != x.UID {
		t.Fatalf("changes after delete: %+v", r)
	}
}

func TestMergeDuplicateImports(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0)
	src := func() *Source { return &Source{Type: "git", ID: "deadbeef", ImportedAt: t0} }
	ea, _ := a.AddEntry(ctx, NewEntry{Body: "commit on a", Source: src()})
	eb, _ := b.AddEntry(ctx, NewEntry{Body: "commit on b", Source: src()})
	sync(a, b)
	sync(a, b)
	keep := min(ea.UID, eb.UID)
	for name, d := range map[string]*device{"a": a, "b": b} {
		es, _ := d.Find(ctx, Query{})
		if len(es) != 1 || es[0].UID != keep {
			t.Errorf("%s: entries = %v", name, uidsOf(es))
		}
	}
}

func uidsOf(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.UID)
	}
	sort.Strings(out)
	return out
}

func TestMergeDryRun(t *testing.T) {
	ctx := context.Background()
	a, b := newDevice(t, t0), newDevice(t, t0)
	a.AddEntry(ctx, NewEntry{Body: "x", Project: "P", CreateProject: true})
	res, err := b.Apply(ctx, a.records(false), ApplyOptions{DryRun: true})
	if err != nil || res.EntriesAdded != 1 || res.ProjectsAdded != 1 {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if es, _ := b.Find(ctx, Query{}); len(es) != 0 {
		t.Fatal("dry run changed the archive")
	}
}
