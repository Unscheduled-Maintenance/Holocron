// Package benchdata seeds archives with realistic, deterministic data for
// benchmarks: years of entries across projects, tags, types and marks.
package benchdata

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// Now is the fixed "present" for seeded archives.
var Now = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)

var (
	projects = []string{"AWS", "Infra", "CCR", "Website", "Docs", "Security", "Billing", "Mobile", "Data", "Support", "Hiring", "Platform"}
	tags     = []string{"security", "monitoring", "terraform", "deploy", "incident", "oncall", "postgres", "kubernetes", "ci", "release",
		"iam", "logging", "backups", "networking", "cost", "performance", "docs", "review", "planning", "meeting"}
	verbs   = []string{"Investigated", "Fixed", "Reviewed", "Enabled", "Rotated", "Migrated", "Documented", "Paired on", "Decided on", "Upgraded", "Debugged", "Planned"}
	objects = []string{"the exporter restart loop", "CloudTrail bucket logging", "IAM Access Analyzer findings", "the deploy pipeline", "Terraform module layout",
		"the backup restore runbook", "S3 bucket policies", "on-call handover notes", "the moderation queue", "ARM build capacity", "Postgres vacuum settings",
		"the Kubernetes ingress", "release notes", "cost anomaly alerts", "the staging environment", "flaky CI tests"}
	types = []journal.Type{journal.TypeWork, journal.TypeWork, journal.TypeWork, journal.TypeNone, journal.TypeNone, journal.TypeNote,
		journal.TypeInvestigation, journal.TypeAccomplishment, journal.TypeDecision, journal.TypeProblem, journal.TypeFollowUp}
)

// Entries generates n deterministic entries spread backwards from Now.
func Entries(n int) []journal.NewEntry {
	r := rand.New(rand.NewPCG(42, 7))
	out := make([]journal.NewEntry, 0, n)
	span := 5 * 365 * 24 * time.Hour
	step := span / time.Duration(n)
	for i := 0; i < n; i++ {
		at := Now.Add(-time.Duration(i) * step).Add(-time.Duration(r.IntN(int(step/time.Minute)+1)) * time.Minute)
		body := fmt.Sprintf("%s %s", verbs[r.IntN(len(verbs))], objects[r.IntN(len(objects))])
		if r.IntN(4) == 0 {
			body += "\n\n" + strings.Repeat("Notes about what happened and what to do next. ", 1+r.IntN(6))
		}
		e := journal.NewEntry{Body: body, OccurredAt: at, Type: types[r.IntN(len(types))], CreateProject: true}
		if r.IntN(5) != 0 {
			e.Project = projects[r.IntN(len(projects))]
		}
		for k := r.IntN(4); k > 0; k-- {
			e.Tags = append(e.Tags, tags[r.IntN(len(tags))])
		}
		if r.IntN(12) == 0 {
			e.Marks = []journal.Mark{journal.Marks[r.IntN(len(journal.Marks))]}
		}
		out = append(out, e)
	}
	return out
}

// Archive creates a temporary archive holding n entries and returns its store.
func Archive(tb testing.TB, n int) *journal.Store {
	tb.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(tb.TempDir(), "bench.db"), database.Options{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	s := journal.NewStore(db, journal.WithClock(func() time.Time { return Now }), journal.WithLocation(time.UTC))
	if _, err := s.AddEntries(ctx, Entries(n)); err != nil {
		tb.Fatal(err)
	}
	return s
}

// Shared creates an archive of n entries in its own temporary directory for
// use across many benchmarks. Call the returned function to remove it.
func Shared(n int) (*journal.Store, func(), error) {
	dir, err := os.MkdirTemp("", "holocron-bench-")
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(dir, "bench.db"), database.Options{})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	cleanup := func() { _ = db.Close(); _ = os.RemoveAll(dir) }
	s := journal.NewStore(db, journal.WithClock(func() time.Time { return Now }), journal.WithLocation(time.UTC))
	if _, err := s.AddEntries(ctx, Entries(n)); err != nil {
		cleanup()
		return nil, nil, err
	}
	return s, cleanup, nil
}
