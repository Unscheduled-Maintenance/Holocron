// Package report builds deterministic reports from journal entries.
//
// Reports never guess at meaning they cannot infer. Selection and ordering
// use explicit metadata — entry type, report marks, resolution state,
// project and tags — plus a few documented heuristics. Every report item
// records the entries it came from and why it was included, so a reader can
// always answer "why is this in the report?".
package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// Kind identifies a report.
type Kind string

// Report kinds.
const (
	Day      Kind = "day"
	Week     Kind = "week"
	Staff    Kind = "staff"
	OneOnOne Kind = "one-on-one"
	Quarter  Kind = "quarter"
)

// Kinds lists every report kind.
var Kinds = []Kind{Day, Week, Staff, OneOnOne, Quarter}

// Describe returns a one-line description of a report kind.
func (k Kind) Describe() string {
	switch k {
	case Day:
		return "Chronological record of one day"
	case Week:
		return "The week's meaningful work, grouped by project"
	case Staff:
		return "A concise team update: completed work, decisions, problems, what's next"
	case OneOnOne:
		return "Material for a one-on-one: discussion items, wins, open problems, friction"
	case Quarter:
		return "Evidence of work over a quarter, grouped by project and theme"
	}
	return ""
}

// ParseKind accepts a report kind or a common variant.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "day", "daily", "today":
		return Day, nil
	case "week", "weekly":
		return Week, nil
	case "staff", "team":
		return Staff, nil
	case "one-on-one", "oneonone", "1on1", "1:1", "one_on_one":
		return OneOnOne, nil
	case "quarter", "quarterly":
		return Quarter, nil
	}
	return "", fmt.Errorf("unknown report %q (choose from day, week, staff, one-on-one, quarter)", s)
}

// Report is a generated report.
type Report struct {
	Kind        Kind
	Title       string
	Range       timerange.Range
	GeneratedAt time.Time
	// Summary holds short context lines shown under the title.
	Summary  []string
	Sections []Section
	// Entries holds every source entry referenced by the report, by ID.
	Entries map[int64]journal.Entry
}

// Section is a titled group of items.
type Section struct {
	Key   string
	Title string
	Note  string
	Items []Item
	// Omitted counts lower-priority items left out to keep the section short.
	Omitted    int
	OmittedIDs []int64
	// omittedInNote is set when Note already describes the omission.
	omittedInNote bool
}

// Item is one bullet in a report.
type Item struct {
	Text     string
	Project  string
	Type     journal.Type
	Time     time.Time
	EntryIDs []int64
	// Reasons explains why the item was selected.
	Reasons []string
	// Open is true for unresolved problems and follow-ups.
	Open bool
}

// IsEmpty reports whether the report has no items.
func (r Report) IsEmpty() bool {
	for _, s := range r.Sections {
		if len(s.Items) > 0 {
			return false
		}
	}
	return true
}

// SourceIDs returns every entry ID referenced by the report, ascending.
func (r Report) SourceIDs() []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, s := range r.Sections {
		for _, it := range s.Items {
			for _, id := range it.EntryIDs {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Options control report generation.
type Options struct {
	Range timerange.Range
	// Projects restricts the report to these projects (names or aliases).
	Projects []string
	// MaxItems caps each section; 0 means the builder default.
	MaxItems int
	// Note is an extra summary line, such as why the range was chosen.
	Note string
}

// Builder produces reports from a store.
type Builder struct {
	Store *journal.Store
	Clock timerange.Clock
	// MaxItems is the default section cap.
	MaxItems int
	// OpenLookback is how far back unresolved problems and follow-ups are
	// collected for one-on-one and staff reports.
	OpenLookback string
	// StaffEarlyDays makes a this-week staff report cover last week when it is
	// run within this many days of the start of the week.
	StaffEarlyDays int
}

// DefaultRange returns the natural range for a report kind, and a note for the
// report when the choice needs explaining.
//
// People often write the staff update at the start of the week, about the week
// that just finished, so early in the week a this-week staff report covers last
// week instead (see StaffEarlyDays).
func (b Builder) DefaultRange(k Kind, staffRange, oneOnOneRange string) (timerange.Range, string) {
	expr := map[Kind]string{Day: "today", Week: "this-week", Staff: staffRange, OneOnOne: oneOnOneRange, Quarter: "this-quarter"}[k]
	if expr == "" {
		expr = "this-week"
	}
	r, err := b.Clock.Parse(expr)
	if err != nil {
		r, _ = b.Clock.Parse("this-week")
	}
	if k == Staff && strings.EqualFold(strings.TrimSpace(expr), "this-week") && b.StaffEarlyDays > 0 &&
		b.Clock.Now.Before(b.Clock.WeekStartOf(b.Clock.Now).AddDate(0, 0, b.StaffEarlyDays)) {
		if last, err := b.Clock.Parse("last-week"); err == nil {
			return last, "Covering last week because it is early in the week (reports.staff_early_days)."
		}
	}
	return r, ""
}

// Build generates a report.
func (b Builder) Build(ctx context.Context, kind Kind, opts Options) (Report, error) {
	if opts.MaxItems <= 0 {
		opts.MaxItems = b.MaxItems
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = 8
	}
	entries, err := b.Store.Find(ctx, journal.Query{Range: opts.Range, Projects: opts.Projects, Order: journal.OldestFirst})
	if err != nil {
		return Report{}, err
	}
	r := Report{
		Kind:        kind,
		Range:       opts.Range,
		GeneratedAt: b.Clock.Now,
		Entries:     map[int64]journal.Entry{},
	}
	if opts.Note != "" {
		r.Summary = append(r.Summary, opts.Note)
	}
	var open []journal.Entry
	if kind == Staff || kind == OneOnOne {
		if open, err = b.openItems(ctx, opts); err != nil {
			return Report{}, err
		}
	}
	switch kind {
	case Day:
		b.buildDay(&r, entries)
	case Week:
		b.buildWeek(&r, entries, opts)
	case Staff:
		b.buildStaff(&r, entries, open, opts)
	case OneOnOne:
		b.buildOneOnOne(&r, entries, open, opts)
	case Quarter:
		b.buildQuarter(&r, entries, opts)
	default:
		return Report{}, fmt.Errorf("unknown report kind %q", kind)
	}
	pool := map[int64]journal.Entry{}
	for _, e := range append(entries, open...) {
		pool[e.ID] = e
	}
	if err := b.linkResolutions(ctx, &r, pool); err != nil {
		return Report{}, err
	}
	for _, s := range r.Sections {
		for _, it := range s.Items {
			for _, id := range it.EntryIDs {
				r.Entries[id] = pool[id]
			}
		}
		// Omitted entries are only referenced, so their numbers can be shown.
		for _, id := range s.OmittedIDs {
			if e, ok := pool[id]; ok {
				r.Entries[id] = e
			}
		}
	}
	return r, nil
}

// openItems returns unresolved problems and follow-ups from the lookback
// window, which usually extends before the report range.
func (b Builder) openItems(ctx context.Context, opts Options) ([]journal.Entry, error) {
	lookback := b.OpenLookback
	if lookback == "" {
		lookback = "90d"
	}
	lr, err := b.Clock.Parse(lookback)
	if err != nil {
		return nil, fmt.Errorf("open_lookback: %w", err)
	}
	rng := timerange.Range{Start: lr.Start}
	if !opts.Range.End.IsZero() {
		rng.End = opts.Range.End
	}
	if !opts.Range.Start.IsZero() && opts.Range.Start.Before(rng.Start) {
		rng.Start = opts.Range.Start
	}
	return b.Store.Find(ctx, journal.Query{Range: rng, Projects: opts.Projects, OpenOnly: true, Order: journal.OldestFirst})
}

func (b Builder) rangeTitle(r timerange.Range) string {
	if r.Label != "" {
		return r.Label
	}
	return b.Clock.Describe(r)
}

// --- selection heuristics -------------------------------------------------

// typeWeight ranks entry types by how likely they are to matter to a reader.
var typeWeight = map[journal.Type]int{
	journal.TypeAccomplishment: 5,
	journal.TypeDecision:       4,
	journal.TypeProblem:        3,
	journal.TypeFollowUp:       2,
	journal.TypeInvestigation:  2,
	journal.TypeWork:           2,
	journal.TypeNone:           1,
	journal.TypeNote:           0,
}

// weight scores an entry for ordering within a section. Explicit marks
// always outrank inferred importance.
func weight(e journal.Entry, audience journal.Mark) int {
	w := typeWeight[e.Type]
	if e.HasMark(journal.MarkImportant) {
		w += 6
	}
	if audience != "" && e.HasMark(audience) {
		w += 8
	}
	if e.Source != nil {
		w-- // imported records (e.g. individual commits) are usually granular
	}
	return w
}

func reasonsFor(e journal.Entry, audience journal.Mark) []string {
	var rs []string
	if audience != "" && e.HasMark(audience) {
		rs = append(rs, "marked "+string(audience))
	}
	if e.HasMark(journal.MarkImportant) {
		rs = append(rs, "marked important")
	}
	if e.HasMark(journal.MarkCrossTeam) && audience != journal.MarkCrossTeam {
		rs = append(rs, "marked cross-team")
	}
	if e.Type != journal.TypeNone {
		if e.IsOpen() {
			rs = append(rs, "open "+string(e.Type))
		} else {
			rs = append(rs, "type "+string(e.Type))
		}
	}
	if e.Source != nil {
		rs = append(rs, "imported from "+e.Source.Type)
	}
	return rs
}

func itemFor(e journal.Entry, audience journal.Mark) Item {
	return Item{
		Text:     e.Title(),
		Project:  e.Project,
		Type:     e.Type,
		Time:     e.OccurredAt,
		EntryIDs: []int64{e.ID},
		Reasons:  reasonsFor(e, audience),
		Open:     e.IsOpen(),
	}
}

// rank sorts entries by weight (descending), then chronologically.
func rank(es []journal.Entry, audience journal.Mark) {
	sort.SliceStable(es, func(i, j int) bool {
		wi, wj := weight(es[i], audience), weight(es[j], audience)
		if wi != wj {
			return wi > wj
		}
		return es[i].OccurredAt.Before(es[j].OccurredAt)
	})
}

// section builds a capped section from ranked entries.
func section(key, title string, es []journal.Entry, audience journal.Mark, max int) Section {
	rank(es, audience)
	s := Section{Key: key, Title: title}
	for i, e := range es {
		if max > 0 && i >= max {
			s.Omitted++
			s.OmittedIDs = append(s.OmittedIDs, e.ID)
			continue
		}
		s.Items = append(s.Items, itemFor(e, audience))
	}
	return s
}

// projectName returns the display name for an entry's project.
func projectName(e journal.Entry) string {
	if e.Project == "" {
		return noProject
	}
	return e.Project
}

const noProject = "No project"

type projectGroup struct {
	name    string
	entries []journal.Entry
	weight  int
}

func groupByProject(es []journal.Entry, audience journal.Mark) []projectGroup {
	idx := map[string]int{}
	var groups []projectGroup
	for _, e := range es {
		name := projectName(e)
		i, ok := idx[strings.ToLower(name)]
		if !ok {
			i = len(groups)
			idx[strings.ToLower(name)] = i
			groups = append(groups, projectGroup{name: name})
		}
		groups[i].entries = append(groups[i].entries, e)
		groups[i].weight += weight(e, audience) + 1
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if (groups[i].name == noProject) != (groups[j].name == noProject) {
			return groups[j].name == noProject
		}
		return groups[i].weight > groups[j].weight
	})
	return groups
}

func entryIDs(es []journal.Entry) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// linkResolutions notes on each item what its entry resolved, so a report
// shows what happened to open problems and follow-ups: "Chased ARM capacity
// (resolves #42: ask about ARM capacity)". The resolved entries are cited
// too, and loaded into pool when they fall outside the report's range.
func (b Builder) linkResolutions(ctx context.Context, r *Report, pool map[int64]journal.Entry) error {
	for si := range r.Sections {
		for ii := range r.Sections[si].Items {
			it := &r.Sections[si].Items[ii]
			if len(it.EntryIDs) != 1 {
				continue
			}
			var parts []string
			for _, id := range pool[it.EntryIDs[0]].Resolves {
				re, ok := pool[id]
				if !ok {
					var err error
					if re, err = b.Store.GetByID(ctx, id); err != nil {
						return err
					}
					pool[id] = re
				}
				parts = append(parts, fmt.Sprintf("%s: %s", re.Ref(), re.Title()))
				it.EntryIDs = append(it.EntryIDs, id)
				it.Reasons = append(it.Reasons, "resolves "+re.Ref())
			}
			if len(parts) > 0 {
				it.Text += " (resolves " + strings.Join(parts, "; ") + ")"
			}
		}
	}
	return nil
}

// Refs returns the references (#42, #12a) of entries cited by the report,
// in the order given.
func (r Report) Refs(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if e, ok := r.Entries[id]; ok && e.Num != 0 {
			out[i] = e.Ref()
		} else {
			out[i] = fmt.Sprintf("#%d", id)
		}
	}
	return out
}
