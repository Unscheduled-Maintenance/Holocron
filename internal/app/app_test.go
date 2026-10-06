package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

var now = time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)

func openApp(t *testing.T, cfg string) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvData, filepath.Join(dir, "data"))
	t.Setenv(config.EnvDB, "")
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), Options{ConfigPath: path, UTC: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestCaptureRules(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, "")
	res, err := a.Capture(ctx, CaptureInput{Text: "Fixed S3 bucket policy +aws #security", Tags: []string{"iam"}, At: "09:30"})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entry
	if e.Body != "Fixed S3 bucket policy" || e.Project != "aws" || res.CreatedProject != "aws" || len(e.Tags) != 2 {
		t.Fatalf("capture = %+v (%+v)", e, res)
	}
	if e.OccurredAt.Hour() != 9 || e.OccurredAt.Minute() != 30 {
		t.Fatalf("--at not applied: %v", e.OccurredAt)
	}
	res, _ = a.Capture(ctx, CaptureInput{Text: "Again +AWS"})
	if res.CreatedProject != "" || res.Entry.Project != "aws" {
		t.Fatalf("existing project should be reused case-insensitively: %+v", res)
	}
	if _, err := a.Capture(ctx, CaptureInput{Text: "x +aws", Project: "gcp"}); !errors.Is(err, journal.ErrInvalid) {
		t.Fatalf("conflicting project: %v", err)
	}
	if _, err := a.Capture(ctx, CaptureInput{Text: "#onlytags"}); !errors.Is(err, journal.ErrInvalid) {
		t.Fatalf("tag-only text: %v", err)
	}
	res, _ = a.Capture(ctx, CaptureInput{Text: "Decision: keep it", Type: "work"})
	if res.Entry.Type != journal.TypeWork {
		t.Fatal("explicit --type must beat inferred type")
	}
	if res.Entry.Body != "Decision: keep it" {
		t.Fatalf("a conflicting --type must keep the text as typed, got %q", res.Entry.Body)
	}

	// The type prefix sets the type and is removed from the stored text (#1).
	res, _ = a.Capture(ctx, CaptureInput{Text: "Decision: keep weekly deploy windows +infra"})
	if res.Entry.Type != journal.TypeDecision || res.Entry.Body != "keep weekly deploy windows" || res.Entry.Project != "infra" {
		t.Fatalf("type prefix capture = %q %q %q", res.Entry.Type, res.Entry.Body, res.Entry.Project)
	}
	res, _ = a.Capture(ctx, CaptureInput{Text: "decision: agreed with --type", Type: "dec"})
	if res.Entry.Type != journal.TypeDecision || res.Entry.Body != "agreed with --type" {
		t.Fatalf("matching --type should still strip the prefix: %q %q", res.Entry.Type, res.Entry.Body)
	}
	res, _ = a.Capture(ctx, CaptureInput{Text: "Decision: kept as typed", Raw: true})
	if res.Entry.Type != journal.TypeNone || res.Entry.Body != "Decision: kept as typed" {
		t.Fatalf("--raw must keep the prefix: %q %q", res.Entry.Type, res.Entry.Body)
	}

	strict := openApp(t, "[capture]\ncreate_projects = false\nshorthand = false\n")
	if _, err := strict.Capture(ctx, CaptureInput{Text: "x", Project: "nope"}); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("create_projects=false: %v", err)
	}
	res, err = strict.Capture(ctx, CaptureInput{Text: "Keep +literal #text"})
	if err != nil || res.Entry.Body != "Keep +literal #text" {
		t.Fatalf("shorthand=false: %+v %v", res.Entry, err)
	}
}

func TestCaptureTypeAliases(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, "[type_aliases]\nship = \"accomplishment\"\n")
	cases := []struct {
		in   CaptureInput
		body string
		typ  journal.Type
	}{
		{CaptureInput{Text: "Win: rotated the keys +aws"}, "rotated the keys", journal.TypeAccomplishment},
		{CaptureInput{Text: "look: why the cache misses"}, "why the cache misses", journal.TypeInvestigation},
		{CaptureInput{Text: "Ship: exporter v2"}, "exporter v2", journal.TypeAccomplishment},
		{CaptureInput{Text: "Closed the incident", Type: "win"}, "Closed the incident", journal.TypeAccomplishment},
		{CaptureInput{Text: "Win: same type either way", Type: "accomplishment"}, "same type either way", journal.TypeAccomplishment},
		{CaptureInput{Text: "Win: kept as typed", Type: "look"}, "Win: kept as typed", journal.TypeInvestigation},
		{CaptureInput{Text: "Win: raw", Raw: true}, "Win: raw", journal.TypeNone},
	}
	for _, c := range cases {
		res, err := a.Capture(ctx, c.in)
		if err != nil {
			t.Errorf("Capture(%+v): %v", c.in, err)
			continue
		}
		if res.Entry.Body != c.body || res.Entry.Type != c.typ {
			t.Errorf("Capture(%+v) = %q %q, want %q %q", c.in, res.Entry.Body, res.Entry.Type, c.body, c.typ)
		}
		// The real type is stored, never the alias.
		stored, err := a.Store.GetByID(ctx, res.Entry.ID)
		if err != nil || stored.Type != c.typ {
			t.Errorf("stored type = %q, %v; want %q", stored.Type, err, c.typ)
		}
	}

	// Aliases also work in the editor document's type field.
	res, err := a.Capture(ctx, CaptureInput{Text: "Exporter restarts", Type: "problem"})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entry
	parsed, err := ParseEntryDoc(strings.Replace(a.DocFor(&e).Format("x"), "type: problem", "type: win", 1))
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.PatchFromDoc(e, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type == nil || *p.Type != journal.TypeAccomplishment {
		t.Fatalf("doc type alias = %v", p.Type)
	}
	u, err := a.Store.Update(ctx, e.ID, p)
	if err != nil || u.Type != journal.TypeAccomplishment {
		t.Fatalf("Update = %q, %v", u.Type, err)
	}
	if got := a.DocFor(&u).Type; got != "accomplishment" {
		t.Fatalf("documents must show the real type, got %q", got)
	}

	// A removed default is no longer an alias.
	plain := openApp(t, "[type_aliases]\nwin = \"\"\n")
	res, err = plain.Capture(ctx, CaptureInput{Text: "Win: just text"})
	if err != nil || res.Entry.Type != journal.TypeNone || res.Entry.Body != "Win: just text" {
		t.Fatalf("removed alias: %q %q %v", res.Entry.Type, res.Entry.Body, err)
	}
	if _, err := plain.ParseType("win"); !errors.Is(err, journal.ErrInvalid) {
		t.Fatalf("removed alias parsed: %v", err)
	}
}

func TestEntryDocRoundTrip(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, "")
	res, err := a.Capture(ctx, CaptureInput{Text: "Exporter restarts\nsecond line", Project: "infra", Type: "problem", Tags: []string{"monitoring"}, Marks: []string{"staff"}, At: "08:00"})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entry
	text := a.DocFor(&e).Format("Holocron entry #1")
	parsed, err := ParseEntryDoc(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.PatchFromDoc(e, parsed); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("unchanged doc should produce no changes, got %v", err)
	}

	edited := strings.Replace(text, "type: problem", "type: accomplishment", 1)
	edited = strings.Replace(edited, "tags: monitoring", "tags: monitoring, alerts", 1)
	edited = strings.Replace(edited, "time: 2026-10-05 08:00", "time: 2026-10-04 17:15", 1)
	edited = strings.Replace(edited, "\nmarks: staff\n", "\nmarks:\n", 1)
	edited = strings.Replace(edited, "second line", "second line, edited", 1)
	parsed, err = ParseEntryDoc(edited)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.PatchFromDoc(e, parsed)
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.Store.Update(ctx, e.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Type != journal.TypeAccomplishment || len(u.Tags) != 2 || len(u.Marks) != 0 || u.OccurredAt.Day() != 4 || !strings.HasSuffix(u.Body, "edited") {
		t.Fatalf("doc edit not applied: %+v", u)
	}

	for _, bad := range []string{"no separator here", "colour: red\n---\nbody", "type: chore\n---\nbody"} {
		d, err := ParseEntryDoc(bad)
		if err == nil {
			_, err = a.PatchFromDoc(u, d)
		}
		if err == nil {
			t.Errorf("accepted bad doc %q", bad)
		}
	}
	if _, err := a.CreateFromDoc(ctx, EntryDoc{Time: "now"}); !errors.Is(err, ErrEmptyEntry) {
		t.Fatalf("empty new doc: %v", err)
	}
	created, err := a.CreateFromDoc(ctx, EntryDoc{Time: "now", Body: "From the editor +notparsed", Type: "follow-up", Resolved: "yes"})
	if err != nil || created.Entry.Body != "From the editor +notparsed" || created.Entry.ResolvedAt == nil {
		t.Fatalf("CreateFromDoc = %+v %v", created.Entry, err)
	}
}

func TestBackupDestinations(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, "")
	if _, err := a.Capture(ctx, CaptureInput{Text: "something"}); err != nil {
		t.Fatal(err)
	}
	p, info, err := a.Backup(ctx, "")
	if err != nil || info.Entries != 1 || filepath.Dir(p) != a.Paths.BackupDir {
		t.Fatalf("default backup = %s %+v %v", p, info, err)
	}
	dir := t.TempDir()
	p, _, err = a.Backup(ctx, dir)
	if err != nil || filepath.Dir(p) != dir {
		t.Fatalf("directory backup = %s %v", p, err)
	}
	if _, _, err := a.Backup(ctx, a.Paths.Database); err == nil {
		t.Fatal("backing up onto the live archive must fail")
	}
}
