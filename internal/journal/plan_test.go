package journal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func plan(t *testing.T, s *Store, day, body string) Entry {
	t.Helper()
	return add(t, s, NewEntry{Body: body, Type: TypeFollowUp, PlannedFor: day})
}

func bodies(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Body)
	}
	return out
}

func TestPlannedForValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.AddEntry(ctx, NewEntry{Body: "x", Type: TypeNote, PlannedFor: "2026-10-13"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("planning a note: %v", err)
	}
	if _, err := s.AddEntry(ctx, NewEntry{Body: "x", Type: TypeFollowUp, PlannedFor: "13/10/2026"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad day: %v", err)
	}
	e := plan(t, s, " 2026-10-13 ", "Vagrant Registry go bye bye")
	if e.PlannedFor != "2026-10-13" || !e.IsPlanned() {
		t.Fatalf("PlannedFor = %q", e.PlannedFor)
	}
	note := add(t, s, NewEntry{Body: "a note"})
	day := "2026-10-14"
	if _, err := s.Update(ctx, note.ID, Patch{PlannedFor: &day}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("planning a note by update: %v", err)
	}
	fu := TypeFollowUp
	if got, err := s.Update(ctx, note.ID, Patch{Type: &fu, PlannedFor: &day}); err != nil || got.PlannedFor != day {
		t.Fatalf("type and plan together: %v %q", err, got.PlannedFor)
	}
	none := ""
	if got, err := s.Update(ctx, e.ID, Patch{PlannedFor: &none}); err != nil || got.PlannedFor != "" {
		t.Fatalf("unplan: %v %q", err, got.PlannedFor)
	}
}

func TestPlansCurrentAndNext(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan(t, s, "2026-10-12", "Monday item")
	tue1 := plan(t, s, "2026-10-13", "Vagrant Registry")
	plan(t, s, "2026-10-13", "Pen test remediation")
	plan(t, s, "2026-10-19", "After leave")
	if _, err := s.Update(ctx, tue1.ID, Patch{Resolved: new(true)}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		today, cur, next string
		items            []string
	}{
		{"2026-10-11", "", "2026-10-12", nil},
		{"2026-10-12", "2026-10-12", "2026-10-13", []string{"Monday item"}},
		{"2026-10-13", "2026-10-13", "2026-10-19", []string{"Vagrant Registry", "Pen test remediation"}},
		// Off sick on the 14th and 15th: Tuesday's plan is still current.
		{"2026-10-16", "2026-10-13", "2026-10-19", []string{"Vagrant Registry", "Pen test remediation"}},
		{"2026-10-20", "2026-10-19", "", []string{"After leave"}},
	}
	for _, c := range cases {
		cur, next, err := s.Plans(ctx, c.today)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Day != c.cur || next.Day != c.next {
			t.Errorf("%s: current %q next %q, want %q %q", c.today, cur.Day, next.Day, c.cur, c.next)
		}
		if got := bodies(cur.Items); len(got) != len(c.items) || (len(got) > 0 && got[0] != c.items[0]) {
			t.Errorf("%s: items %v, want %v", c.today, got, c.items)
		}
	}
	cur, _, _ := s.Plans(ctx, "2026-10-13")
	if cur.Done() != 1 {
		t.Fatalf("Done = %d", cur.Done())
	}
	// A plan item that changed type is no longer on the plan.
	note := TypeNote
	if _, err := s.Update(ctx, tue1.ID, Patch{Type: &note}); err != nil {
		t.Fatal(err)
	}
	if cur, _, _ = s.Plans(ctx, "2026-10-13"); len(cur.Items) != 1 {
		t.Fatalf("items after retype: %v", bodies(cur.Items))
	}
	if _, _, err := s.Plans(ctx, "tomorrow"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad today: %v", err)
	}
}

func TestPlanSurvivesUndoDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := plan(t, s, "2026-10-13", "keep me planned")
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Restore(ctx, e)
	if err != nil || got.PlannedFor != "2026-10-13" {
		t.Fatalf("Restore: %v %q", err, got.PlannedFor)
	}
}

func TestPlanMerges(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)
	a, b := newDevice(t, t0), newDevice(t, t0)
	e := add(t, a.Store, NewEntry{Body: "Pen test remediation", Type: TypeFollowUp, PlannedFor: "2026-10-13"})
	sync(a, b)
	if got := b.get(e.UID); got.PlannedFor != "2026-10-13" {
		t.Fatalf("plan not synced: %q", got.PlannedFor)
	}

	// Carried over on b; the later change wins on a.
	day := "2026-10-14"
	if _, err := b.at(t0.Add(time.Hour)).Update(context.Background(), b.get(e.UID).ID, Patch{PlannedFor: &day}); err != nil {
		t.Fatal(err)
	}
	sync(a, b)
	if got := a.get(e.UID); got.PlannedFor != day {
		t.Fatalf("carry-over not synced: %q", got.PlannedFor)
	}
}

func TestPlanIgnoresRecordsWithoutPlanClock(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)
	a := newDevice(t, t0)
	e := add(t, a.Store, NewEntry{Body: "Pen test remediation", Type: TypeFollowUp, PlannedFor: "2026-10-13"})

	// An older Holocron edits the body and publishes a record that knows
	// nothing about plans.
	recs := a.records(false)
	r := recs.Entries[0]
	r.Body = "Pen test remediation (phase 1)"
	r.PlannedFor = ""
	r.UpdatedAt = t0.Add(time.Hour)
	r.Clocks = Clocks{}
	for _, f := range EntryFields {
		if f != FieldPlan {
			r.Clocks[f] = HLC{Wall: t0.Add(time.Hour).UnixMilli(), Device: "OLD"}
		}
	}
	a.apply(Records{Entries: []EntryRecord{r}})
	got := a.get(e.UID)
	if got.Body != "Pen test remediation (phase 1)" || got.PlannedFor != "2026-10-13" {
		t.Fatalf("after old record: body %q plan %q", got.Body, got.PlannedFor)
	}

	// And the same record inserted fresh keeps no plan and no plan clock.
	b := newDevice(t, t0)
	b.apply(Records{Entries: []EntryRecord{r}})
	if got := b.get(e.UID); got.PlannedFor != "" {
		t.Fatalf("plan invented: %q", got.PlannedFor)
	}
}
