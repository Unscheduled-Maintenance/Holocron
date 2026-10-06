package journal

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHLC(t *testing.T) {
	a := HLC{Wall: 100, Count: 0, Device: "A"}
	cases := []struct {
		x, y HLC
		want bool
	}{
		{HLC{Wall: 101}, a, true},
		{a, HLC{Wall: 101}, false},
		{HLC{Wall: 100, Count: 1, Device: "Z"}, a, true},
		{HLC{Wall: 100, Device: "B"}, a, false}, // tie: lower device wins
		{a, HLC{Wall: 100, Device: "B"}, true},
		{a, a, false},
		{a, HLC{Wall: 100}, true}, // a known device beats an inferred clock
		{HLC{Wall: 100}, a, false},
	}
	for _, c := range cases {
		if got := c.x.After(c.y); got != c.want {
			t.Errorf("%v.After(%v) = %v", c.x, c.y, got)
		}
	}
	h := HLC{Wall: 1728000000123, Count: 7, Device: "01J5ABCDEFGHJKMNPQRSTVWXYZ"}
	if got, err := ParseHLC(h.String()); err != nil || got != h {
		t.Fatalf("ParseHLC(%q) = %v, %v", h.String(), got, err)
	}
	for _, bad := range []string{"", "1.2", "x.1.A", "1.y.A"} {
		if _, err := ParseHLC(bad); err == nil {
			t.Errorf("ParseHLC(%q) succeeded", bad)
		}
	}
	if (Clocks{"a": a, "b": {Wall: 300}}).Latest() != (HLC{Wall: 300}) {
		t.Error("Latest")
	}
}

func TestTickNeverGoesBackwards(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	wall := base
	s.now = func() time.Time { return wall }
	var last HLC
	for i, step := range []time.Duration{0, time.Second, -time.Hour, 0, time.Millisecond} {
		wall = wall.Add(step)
		var h HLC
		if err := s.db.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			h, err = s.tick(ctx, tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !h.After(last) {
			t.Fatalf("step %d: clock %v not after %v", i, h, last)
		}
		last = h
	}
}

func clocksOf(t *testing.T, s *Store, id int64) Clocks {
	t.Helper()
	c, err := s.EntryClocks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dirty(t *testing.T, s *Store, table string, id int64) bool {
	t.Helper()
	var d bool
	if err := s.db.QueryRow(`SELECT dirty FROM `+table+` WHERE id = ?`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFieldClocks(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	e := add(t, s, NewEntry{Body: "first", Type: TypeProblem, Tags: []string{"x"}})
	c0 := clocksOf(t, s, e.ID)
	for _, f := range EntryFields {
		if c0[f].IsZero() {
			t.Fatalf("new entry has no clock for %s: %v", f, c0)
		}
	}
	if !dirty(t, s, "entries", e.ID) {
		t.Fatal("a new entry must be marked as changed here")
	}
	if _, err := s.DB().Exec(`UPDATE entries SET dirty = 0`); err != nil {
		t.Fatal(err)
	}

	body, yes := "second", true
	if _, err := s.Update(ctx, e.ID, Patch{Body: &body, AddTags: []string{"y"}, Resolved: &yes}); err != nil {
		t.Fatal(err)
	}
	c1 := clocksOf(t, s, e.ID)
	for _, f := range []string{FieldBody, FieldTags, FieldResolved} {
		if !c1[f].After(c0[f]) {
			t.Errorf("%s clock did not advance: %v -> %v", f, c0[f], c1[f])
		}
	}
	for _, f := range []string{FieldTime, FieldType, FieldProject, FieldMarks} {
		if c1[f] != c0[f] {
			t.Errorf("%s clock changed without a change: %v -> %v", f, c0[f], c1[f])
		}
	}
	if !dirty(t, s, "entries", e.ID) {
		t.Fatal("an edited entry must be marked as changed")
	}

	// Renaming a tag changes the tags of the entries using it.
	if _, err := s.RenameTag(ctx, "x", "z"); err != nil {
		t.Fatal(err)
	}
	if c2 := clocksOf(t, s, e.ID); !c2[FieldTags].After(c1[FieldTags]) || c2[FieldBody] != c1[FieldBody] {
		t.Errorf("tag rename clocks: %v -> %v", c1, c2)
	}

	// Projects keep their own clocks.
	p, err := s.CreateProject(ctx, NewProject{Name: "Infra"})
	if err != nil {
		t.Fatal(err)
	}
	var pc string
	_ = s.db.QueryRow(`SELECT clocks FROM projects WHERE id = ?`, p.ID).Scan(&pc)
	before := decodeClocks(pc)
	if len(before) != len(ProjectFields) {
		t.Fatalf("new project clocks = %v", before)
	}
	desc := "Servers"
	if _, err := s.UpdateProject(ctx, "Infra", ProjectPatch{Description: &desc, AddAliases: []string{"inf"}, AddPaths: []string{t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRow(`SELECT clocks FROM projects WHERE id = ?`, p.ID).Scan(&pc)
	after := decodeClocks(pc)
	if !after[FieldDescription].After(before[FieldDescription]) || !after[FieldAliases].After(before[FieldAliases]) || after[FieldName] != before[FieldName] {
		t.Errorf("project clocks %v -> %v", before, after)
	}
}

func tombstone(t *testing.T, s *Store, uid string) (kind string, clock HLC, ok bool) {
	t.Helper()
	var c string
	err := s.db.QueryRow(`SELECT kind, clock FROM tombstones WHERE uid = ?`, uid).Scan(&kind, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", HLC{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	h, _ := ParseHLC(c)
	return kind, h, true
}

func TestTombstones(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	e := add(t, s, NewEntry{Body: "to delete", Project: "Infra", CreateProject: true})
	e, _ = s.GetByID(ctx, e.ID)
	before := clocksOf(t, s, e.ID).Latest()
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	kind, clock, ok := tombstone(t, s, e.UID)
	if !ok || kind != "entry" || !clock.After(before) {
		t.Fatalf("entry tombstone = %q %v %v", kind, clock, ok)
	}
	// Undo removes the tombstone, and the restored entry is newer than it.
	r, err := s.Restore(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tombstone(t, s, e.UID); ok {
		t.Fatal("undo must remove the tombstone")
	}
	if rc := clocksOf(t, s, r.ID); !rc[FieldBody].After(clock) || r.Ref() != e.Ref() {
		t.Fatalf("restored entry %s clocks %v, delete at %v", r.Ref(), rc, clock)
	}

	p, _ := s.Project(ctx, "Infra")
	if _, err := s.DeleteProject(ctx, "Infra"); err != nil {
		t.Fatal(err)
	}
	if kind, _, ok := tombstone(t, s, p.UID); !ok || kind != "project" {
		t.Fatalf("project tombstone = %q %v", kind, ok)
	}
}

func TestEntryNumbers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	one := add(t, s, NewEntry{Body: "one"})
	two := add(t, s, NewEntry{Body: "two"})
	if one.Ref() != "#1" || two.Ref() != "#2" {
		t.Fatalf("plain numbers = %s %s", one.Ref(), two.Ref())
	}
	if err := s.Delete(ctx, two.ID); err != nil {
		t.Fatal(err)
	}
	if three := add(t, s, NewEntry{Body: "three"}); three.Ref() != "#3" {
		t.Fatalf("numbers must never be reissued, got %s", three.Ref())
	}

	// Once labelled, new entries continue from the plain numbers with the
	// device's label.
	self, err := s.SetSelfLabel(ctx, "a")
	if err != nil || self.Label != "a" {
		t.Fatalf("SetSelfLabel = %+v, %v", self, err)
	}
	four := add(t, s, NewEntry{Body: "four", Resolves: nil})
	five := add(t, s, NewEntry{Body: "five", Type: TypeFollowUp})
	if four.Ref() != "#4a" || five.Ref() != "#5a" {
		t.Fatalf("labelled numbers = %s %s", four.Ref(), five.Ref())
	}
	for ref, want := range map[string]string{"1": "one", "#1": "one", "4a": "four", "#4A": "four", "4": "four", five.UID: "five"} {
		got, err := s.Get(ctx, ref)
		if err != nil || got.Body != want {
			t.Errorf("Get(%q) = %q, %v; want %q", ref, got.Body, err, want)
		}
	}
	for _, ref := range []string{"2", "4b", "99"} {
		if _, err := s.Get(ctx, ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q): %v", ref, err)
		}
	}
	if _, err := s.SetSelfLabel(ctx, "b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("relabelling: %v", err)
	}
	if _, err := s.SetSelfLabel(ctx, "B1"); err == nil {
		t.Fatal("invalid label accepted")
	}

	// Resolve links carry references.
	six := add(t, s, NewEntry{Body: "done", Resolves: []int64{five.ID}})
	five, _ = s.GetByID(ctx, five.ID)
	six, _ = s.GetByID(ctx, six.ID)
	if five.ResolvedByRef != "#6a" || !reflect.DeepEqual(six.ResolvesRefs, []string{"#5a"}) {
		t.Fatalf("resolve refs = %q %v", five.ResolvedByRef, six.ResolvesRefs)
	}

	// Undo keeps the labelled number.
	if err := s.Delete(ctx, four.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Restore(ctx, four); err != nil || r.Ref() != "#4a" {
		t.Fatalf("restore = %s, %v", r.Ref(), err)
	}
}
