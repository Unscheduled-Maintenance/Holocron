package journal_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/benchdata"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// benchEntries is about five years of heavy daily use.
const benchEntries = 15000

var (
	benchOnce    sync.Once
	benchStore   *journal.Store
	benchCleanup = func() {}
)

func TestMain(m *testing.M) {
	code := m.Run()
	benchCleanup()
	os.Exit(code)
}

func store(b *testing.B) *journal.Store {
	benchOnce.Do(func() {
		s, cleanup, err := benchdata.Shared(benchEntries)
		if err != nil {
			b.Fatal(err)
		}
		benchStore, benchCleanup = s, cleanup
	})
	return benchStore
}

func clock() timerange.Clock {
	return timerange.NewClock(benchdata.Now, time.UTC, time.Monday)
}

func rng(b *testing.B, expr string) timerange.Range {
	r, err := clock().Parse(expr)
	if err != nil {
		b.Fatal(err)
	}
	return r
}

func benchFind(b *testing.B, q journal.Query) {
	s := store(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Find(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFindThisWeek(b *testing.B) { benchFind(b, journal.Query{Range: rng(b, "this-week")}) }
func BenchmarkFind30Days(b *testing.B)   { benchFind(b, journal.Query{Range: rng(b, "30d")}) }
func BenchmarkFindLatest50(b *testing.B) { benchFind(b, journal.Query{Limit: 50}) }
func BenchmarkFindTUICap(b *testing.B)   { benchFind(b, journal.Query{Limit: 2000}) }
func BenchmarkFindQuarter(b *testing.B) {
	benchFind(b, journal.Query{Range: rng(b, "last-quarter"), Order: journal.OldestFirst})
}
func BenchmarkFindProject(b *testing.B) {
	benchFind(b, journal.Query{Projects: []string{"aws"}, Limit: 50})
}
func BenchmarkFindTag(b *testing.B) {
	benchFind(b, journal.Query{Tags: []string{"security"}, Limit: 50})
}
func BenchmarkFindTagAll(b *testing.B) { benchFind(b, journal.Query{Tags: []string{"security"}}) }
func BenchmarkFindOpen(b *testing.B)   { benchFind(b, journal.Query{OpenOnly: true}) }
func BenchmarkFindMarked(b *testing.B) {
	benchFind(b, journal.Query{Marks: []journal.Mark{journal.MarkStaff}, Range: rng(b, "this-quarter")})
}
func BenchmarkSearchCommon(b *testing.B) { benchFind(b, journal.Query{Text: "exporter", Limit: 50}) }
func BenchmarkSearchCommonCap(b *testing.B) {
	benchFind(b, journal.Query{Text: "exporter", Limit: 2000})
}
func BenchmarkSearchRare(b *testing.B) {
	benchFind(b, journal.Query{Text: "vacuum ingress", Limit: 50})
}
func BenchmarkSearchPrefix(b *testing.B) {
	benchFind(b, journal.Query{Text: "inv", Limit: 50})
}
func BenchmarkSearchRelevance(b *testing.B) {
	benchFind(b, journal.Query{Text: "exporter", Limit: 50, Order: journal.Relevance})
}

func BenchmarkCountSearch(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.Count(ctx, journal.Query{Text: "exporter"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.GetByID(ctx, 7500); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListProjects(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.ListProjects(ctx, true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListTags(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.ListTags(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAddEntry(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.AddEntry(ctx, journal.NewEntry{Body: "Benchmark entry", Project: "AWS", Tags: []string{"security", "bench"}}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateTags(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	i := 0
	for b.Loop() {
		i++
		tags := []string{"security"}
		if i%2 == 0 {
			tags = []string{"monitoring"}
		}
		if _, err := s.Update(ctx, 100, journal.Patch{Tags: &tags}); err != nil {
			b.Fatal(err)
		}
	}
}

// A tag used on few entries: the worst case for scanning in date order.
func BenchmarkFindRareTag(b *testing.B) {
	s := store(b)
	ctx := context.Background()
	ids, err := s.AddEntries(ctx, []journal.NewEntry{
		{Body: "rare one", OccurredAt: benchdata.Now.AddDate(-4, 0, 0), Tags: []string{"rare-bench-tag"}},
	})
	if err != nil || len(ids) != 1 {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Find(ctx, journal.Query{Tags: []string{"rare-bench-tag"}, Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}

// The first keystroke of a search matches most of the archive.
func BenchmarkSearchFirstKeystroke(b *testing.B) {
	benchFind(b, journal.Query{Text: "e", Limit: 500})
}

func BenchmarkSearchTUIPage(b *testing.B) {
	benchFind(b, journal.Query{Text: "exporter", Limit: 500})
}
