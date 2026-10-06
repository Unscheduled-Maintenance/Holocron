package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// collapseImported folds runs of unmarked imported records (such as many
// small commits) into a single summary item per source, so they do not
// drown out hand-written entries. Fewer than three are left alone.
func collapseImported(es []journal.Entry) (kept []journal.Entry, summaries []Item) {
	bySource := map[string][]journal.Entry{}
	var order []string
	for _, e := range es {
		if e.Source == nil || len(e.Marks) > 0 || (e.Type != journal.TypeNone && e.Type != journal.TypeWork) {
			kept = append(kept, e)
			continue
		}
		if _, ok := bySource[e.Source.Type]; !ok {
			order = append(order, e.Source.Type)
		}
		bySource[e.Source.Type] = append(bySource[e.Source.Type], e)
	}
	for _, src := range order {
		group := bySource[src]
		if len(group) < 3 {
			kept = append(kept, group...)
			continue
		}
		noun := "records"
		if src == "git" {
			noun = "commits"
		}
		latest := group[len(group)-1]
		summaries = append(summaries, Item{
			Text:     fmt.Sprintf("%d %s imported from %s (latest: %s)", len(group), noun, src, latest.Title()),
			Project:  latest.Project,
			Time:     group[0].OccurredAt,
			EntryIDs: entryIDs(group),
			Reasons:  []string{fmt.Sprintf("%d unmarked records imported from %s, summarised together", len(group), src)},
		})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].OccurredAt.Before(kept[j].OccurredAt) })
	return kept, summaries
}

func (b Builder) buildDay(r *Report, es []journal.Entry) {
	r.Title = "Day report — " + b.rangeTitle(r.Range)
	if len(es) == 0 {
		return
	}
	var projects []string
	seen := map[string]bool{}
	for _, e := range es {
		if e.Project != "" && !seen[e.Project] {
			seen[e.Project] = true
			projects = append(projects, e.Project)
		}
	}
	if len(projects) > 0 {
		r.Summary = append(r.Summary, "Projects: "+joinNames(projects))
	}
	kept, summaries := collapseImported(es)
	timeline := Section{Key: "timeline", Title: "Timeline"}
	for _, e := range kept {
		timeline.Items = append(timeline.Items, itemFor(e, ""))
	}
	timeline.Items = append(timeline.Items, summaries...)
	sort.SliceStable(timeline.Items, func(i, j int) bool { return timeline.Items[i].Time.Before(timeline.Items[j].Time) })
	// Open problems and follow-ups are flagged [open] in the timeline itself.
	r.Sections = append(r.Sections, timeline)
}

func (b Builder) buildWeek(r *Report, es []journal.Entry, opts Options) {
	r.Title = "Week report — " + b.rangeTitle(r.Range)
	if len(es) == 0 {
		return
	}
	days := map[string]bool{}
	for _, e := range es {
		days[e.OccurredAt.In(b.Clock.Loc).Format("2006-01-02")] = true
	}
	// Open problems and follow-ups get their own closing section, so each entry
	// appears exactly once.
	var settled, open []journal.Entry
	for _, e := range es {
		if e.IsOpen() {
			open = append(open, e)
		} else {
			settled = append(settled, e)
		}
	}
	groups := groupByProject(settled, "")
	var names []string
	for _, g := range groups {
		if g.name != noProject {
			names = append(names, g.name)
		}
	}
	r.Summary = append(r.Summary, "Active on "+plural(len(days), "day", "days"))
	if len(names) > 0 {
		r.Summary = append(r.Summary, "Projects: "+joinNames(names))
	}
	for _, g := range groups {
		kept, summaries := collapseImported(g.entries)
		s := section("project:"+strings.ToLower(g.name), g.name, kept, "", opts.MaxItems)
		s.Items = append(s.Items, summaries...)
		if s.Omitted > 0 {
			s.Note = fmt.Sprintf("Plus %s of lower-priority work not listed.", plural(s.Omitted, "entry", "entries"))
			s.omittedInNote = true
		}
		r.Sections = append(r.Sections, s)
	}
	if len(open) > 0 {
		r.Sections = append(r.Sections, section("open", "Still open", open, "", opts.MaxItems))
	}
}

func (b Builder) buildStaff(r *Report, es, open []journal.Entry, opts Options) {
	r.Title = "Staff update — " + b.rangeTitle(r.Range)
	used := map[int64]bool{}
	take := func(pred func(journal.Entry) bool, pool []journal.Entry) []journal.Entry {
		var out []journal.Entry
		for _, e := range pool {
			if !used[e.ID] && pred(e) {
				used[e.ID] = true
				out = append(out, e)
			}
		}
		return out
	}
	marked := func(e journal.Entry) bool {
		return e.HasMark(journal.MarkStaff) || e.HasMark(journal.MarkImportant)
	}

	crossTeam := take(func(e journal.Entry) bool { return e.HasMark(journal.MarkCrossTeam) }, es)
	completed := take(func(e journal.Entry) bool {
		if len(e.Resolves) > 0 {
			return true // closing an open problem or follow-up is completed work
		}
		switch e.Type {
		case journal.TypeAccomplishment:
			return true
		case journal.TypeWork, journal.TypeNone, journal.TypeNote:
			return marked(e)
		}
		return false
	}, es)
	decisions := take(func(e journal.Entry) bool { return e.Type == journal.TypeDecision }, es)
	problems := take(func(e journal.Entry) bool {
		return e.Type == journal.TypeProblem || (e.Type == journal.TypeInvestigation && marked(e))
	}, es)
	inRange := func(e journal.Entry) bool { return r.Range.Contains(e.OccurredAt) }
	upcoming := take(func(e journal.Entry) bool {
		return e.Type == journal.TypeFollowUp && e.IsOpen() && (inRange(e) || marked(e))
	}, append(append([]journal.Entry{}, es...), open...))

	max := opts.MaxItems
	add := func(key, title string, list []journal.Entry) {
		if len(list) > 0 {
			r.Sections = append(r.Sections, section(key, title, list, journal.MarkStaff, max))
		}
	}
	add("completed", "Completed", completed)
	add("decisions", "Decisions", decisions)
	add("problems", "Problems and investigations", problems)
	add("upcoming", "Coming up", upcoming)
	add("cross-team", "For other teams", crossTeam)

	leftOut := 0
	for _, e := range es {
		if !used[e.ID] {
			leftOut++
		}
	}
	if leftOut > 0 {
		r.Summary = append(r.Summary, fmt.Sprintf("Left out %s of routine work; include one with `holocron mark <id> staff`.",
			plural(leftOut, "entry", "entries")))
	}
}

func (b Builder) buildOneOnOne(r *Report, es, open []journal.Entry, opts Options) {
	r.Title = "One-on-one — " + b.rangeTitle(r.Range)
	used := map[int64]bool{}
	take := func(pred func(journal.Entry) bool, pool []journal.Entry) []journal.Entry {
		var out []journal.Entry
		for _, e := range pool {
			if !used[e.ID] && pred(e) {
				used[e.ID] = true
				out = append(out, e)
			}
		}
		return out
	}
	all := append(append([]journal.Entry{}, es...), open...)
	discuss := take(func(e journal.Entry) bool { return e.HasMark(journal.MarkOneOnOne) }, all)
	wins := take(func(e journal.Entry) bool {
		return e.Type == journal.TypeAccomplishment || (e.HasMark(journal.MarkImportant) && !e.IsOpen()) || len(e.Resolves) > 0
	}, es)
	decisions := take(func(e journal.Entry) bool { return e.Type == journal.TypeDecision }, es)
	problems := take(func(e journal.Entry) bool { return e.Type == journal.TypeProblem && e.IsOpen() }, all)
	followUps := take(func(e journal.Entry) bool { return e.Type == journal.TypeFollowUp && e.IsOpen() }, all)

	max := opts.MaxItems
	add := func(key, title string, list []journal.Entry) {
		if len(list) > 0 {
			r.Sections = append(r.Sections, section(key, title, list, journal.MarkOneOnOne, max))
		}
	}
	add("discuss", "To discuss", discuss)
	add("wins", "Wins", wins)
	add("decisions", "Decisions", decisions)
	add("problems", "Open problems", problems)
	add("follow-ups", "Open follow-ups", followUps)
	if fr := friction(es, open); len(fr.Items) > 0 {
		r.Sections = append(r.Sections, fr)
	}
}

// friction finds tags and projects that keep appearing on problems and
// investigations: the recurring sources of difficulty worth raising.
func friction(es, open []journal.Entry) Section {
	s := Section{Key: "friction", Title: "Recurring friction",
		Note: "Tags or projects with two or more problems or investigations in this period."}
	type bucket struct {
		label string
		ids   []int64
		seen  map[int64]bool
	}
	buckets := map[string]*bucket{}
	var order []string
	addTo := func(key, label string, id int64) {
		bk, ok := buckets[key]
		if !ok {
			bk = &bucket{label: label, seen: map[int64]bool{}}
			buckets[key] = bk
			order = append(order, key)
		}
		if !bk.seen[id] {
			bk.seen[id] = true
			bk.ids = append(bk.ids, id)
		}
	}
	for _, e := range append(append([]journal.Entry{}, es...), open...) {
		if e.Type != journal.TypeProblem && e.Type != journal.TypeInvestigation {
			continue
		}
		for _, t := range e.Tags {
			addTo("tag:"+t, "#"+t, e.ID)
		}
		if e.Project != "" {
			addTo("project:"+strings.ToLower(e.Project), e.Project, e.ID)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return len(buckets[order[i]].ids) > len(buckets[order[j]].ids) })
	for _, k := range order {
		bk := buckets[k]
		if len(bk.ids) < 2 {
			continue
		}
		sort.Slice(bk.ids, func(i, j int) bool { return bk.ids[i] < bk.ids[j] })
		s.Items = append(s.Items, Item{
			Text:     fmt.Sprintf("%s — %s", bk.label, plural(len(bk.ids), "problem or investigation", "problems or investigations")),
			EntryIDs: bk.ids,
			Reasons:  []string{"repeated problems or investigations share this " + strings.SplitN(k, ":", 2)[0]},
		})
		if len(s.Items) == 5 {
			break
		}
	}
	return s
}

func (b Builder) buildQuarter(r *Report, es []journal.Entry, opts Options) {
	r.Title = "Quarter report — " + b.rangeTitle(r.Range)
	if len(es) == 0 {
		return
	}
	days := map[string]bool{}
	for _, e := range es {
		days[e.OccurredAt.In(b.Clock.Loc).Format("2006-01-02")] = true
	}
	groups := groupByProject(es, journal.MarkQuarterly)
	projects := 0
	for _, g := range groups {
		if g.name != noProject {
			projects++
		}
	}
	r.Summary = append(r.Summary, fmt.Sprintf("Work recorded on %s across %s.",
		plural(len(days), "day", "days"), plural(projects, "project", "projects")))

	totalWeeks := 0
	if !r.Range.Start.IsZero() && !r.Range.End.IsZero() {
		totalWeeks = int((r.Range.End.Sub(r.Range.Start).Hours()/24)+6) / 7
	}
	for _, g := range groups {
		var notable, routine []journal.Entry
		weeks := map[string]bool{}
		for _, e := range g.entries {
			wk := b.Clock.WeekStartOf(e.OccurredAt).Format("2006-01-02")
			weeks[wk] = true
			if e.Type == journal.TypeAccomplishment || e.Type == journal.TypeDecision ||
				e.HasMark(journal.MarkQuarterly) || e.HasMark(journal.MarkImportant) {
				notable = append(notable, e)
			} else {
				routine = append(routine, e)
			}
		}
		s := section("project:"+strings.ToLower(g.name), g.name, notable, journal.MarkQuarterly, opts.MaxItems)
		first := g.entries[0].OccurredAt.In(b.Clock.Loc)
		last := g.entries[len(g.entries)-1].OccurredAt.In(b.Clock.Loc)
		span := first.Format("2 Jan")
		if last.Format("2006-01-02") != first.Format("2006-01-02") {
			span += " – " + last.Format("2 Jan")
		}
		note := "Active " + span
		if totalWeeks > 1 {
			note = fmt.Sprintf("Active in %d of %d weeks (%s)", len(weeks), totalWeeks, span)
		}
		if other := len(routine) + s.Omitted; other > 0 {
			note += fmt.Sprintf("; plus %s of routine work, investigations and notes.", plural(other, "entry", "entries"))
			s.OmittedIDs = append(s.OmittedIDs, entryIDs(routine)...)
			s.Omitted = other
			s.omittedInNote = true
		} else {
			note += "."
		}
		s.Note = note
		r.Sections = append(r.Sections, s)
	}
	if th := themes(es); len(th.Items) > 0 {
		r.Sections = append(r.Sections, th)
	}
}

// themes lists tags used on at least three entries, with the projects they
// span: a deterministic view of what the period was about.
func themes(es []journal.Entry) Section {
	s := Section{Key: "themes", Title: "Themes", Note: "Tags used on three or more entries."}
	type agg struct {
		ids      []int64
		projects map[string]bool
	}
	tags := map[string]*agg{}
	for _, e := range es {
		for _, t := range e.Tags {
			a, ok := tags[t]
			if !ok {
				a = &agg{projects: map[string]bool{}}
				tags[t] = a
			}
			a.ids = append(a.ids, e.ID)
			if e.Project != "" {
				a.projects[e.Project] = true
			}
		}
	}
	var names []string
	for t, a := range tags {
		if len(a.ids) >= 3 {
			names = append(names, t)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(tags[names[i]].ids) != len(tags[names[j]].ids) {
			return len(tags[names[i]].ids) > len(tags[names[j]].ids)
		}
		return names[i] < names[j]
	})
	for i, t := range names {
		if i == 8 {
			break
		}
		a := tags[t]
		var ps []string
		for p := range a.projects {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		text := fmt.Sprintf("#%s — %s", t, plural(len(a.ids), "entry", "entries"))
		if len(ps) > 0 {
			text += " across " + joinNames(ps)
		}
		s.Items = append(s.Items, Item{Text: text, EntryIDs: a.ids, Reasons: []string{"tag #" + t + " recurs"}})
	}
	return s
}
