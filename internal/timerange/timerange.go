// Package timerange turns human date expressions ("today", "7d",
// "last-week", "2026-09-01") into concrete time ranges.
//
// All ranges are computed in a caller-supplied location (normally the
// user's local timezone) using calendar arithmetic, so a "day" is always
// local midnight to local midnight even across daylight-saving changes.
//
// A Range is half-open: Start is inclusive and End is exclusive. User-facing
// --from/--to dates are inclusive calendar days; From/To convert them.
package timerange

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Range is a half-open interval [Start, End). A zero Start means "since the
// beginning of time" and a zero End means "until now and beyond".
type Range struct {
	Start time.Time
	End   time.Time
	// Label is a short human description such as "this week" or
	// "2026-09-01 – 2026-09-30".
	Label string
}

// All is the unbounded range.
func All() Range { return Range{Label: "all time"} }

// IsZero reports whether the range is unbounded at both ends.
func (r Range) IsZero() bool { return r.Start.IsZero() && r.End.IsZero() }

// Contains reports whether t falls inside the range.
func (r Range) Contains(t time.Time) bool {
	if !r.Start.IsZero() && t.Before(r.Start) {
		return false
	}
	if !r.End.IsZero() && !t.Before(r.End) {
		return false
	}
	return true
}

// LastDay returns the final calendar day included in the range (End minus a
// day), or the zero time when End is unbounded.
func (r Range) LastDay() time.Time {
	if r.End.IsZero() {
		return time.Time{}
	}
	return r.End.AddDate(0, 0, -1)
}

// Clock lets callers and tests decide what "now" means.
type Clock struct {
	Now       time.Time
	Loc       *time.Location
	WeekStart time.Weekday
}

// NewClock returns a Clock for now in loc, with weeks starting on weekStart.
func NewClock(now time.Time, loc *time.Location, weekStart time.Weekday) Clock {
	if loc == nil {
		loc = time.Local
	}
	return Clock{Now: now.In(loc), Loc: loc, WeekStart: weekStart}
}

// Midnight returns local midnight on the calendar day containing t.
func (c Clock) Midnight(t time.Time) time.Time {
	t = t.In(c.Loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, c.Loc)
}

// Today returns local midnight today.
func (c Clock) Today() time.Time { return c.Midnight(c.Now) }

// WeekStartOf returns midnight at the start of the week containing t.
func (c Clock) WeekStartOf(t time.Time) time.Time {
	d := c.Midnight(t)
	offset := (int(d.Weekday()) - int(c.WeekStart) + 7) % 7
	return d.AddDate(0, 0, -offset)
}

func (c Clock) monthStart(t time.Time) time.Time {
	t = t.In(c.Loc)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, c.Loc)
}

func (c Clock) quarterStart(t time.Time) time.Time {
	t = t.In(c.Loc)
	m := ((int(t.Month())-1)/3)*3 + 1
	return time.Date(t.Year(), time.Month(m), 1, 0, 0, 0, 0, c.Loc)
}

// Expressions lists the named range expressions, for help text and completion.
var Expressions = []string{
	"today", "yesterday", "this-week", "last-week", "this-month", "last-month",
	"this-quarter", "last-quarter", "this-year", "last-year", "7d", "30d", "all",
}

var (
	relRe     = regexp.MustCompile(`^(\d+)([dwm])$`)
	dayRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	monthRe   = regexp.MustCompile(`^\d{4}-\d{2}$`)
	quarterRe = regexp.MustCompile(`^(\d{4})-?[qQ]([1-4])$`)
)

// Parse resolves a range expression. Supported forms:
//
//	today, yesterday
//	this-week, last-week, this-month, last-month,
//	this-quarter, last-quarter, this-year, last-year
//	Nd, Nw, Nm      the last N days/weeks/months, including today
//	YYYY-MM-DD      a single day
//	YYYY-MM         a calendar month
//	YYYY-Qn         a calendar quarter
//	all             everything
func (c Clock) Parse(expr string) (Range, error) {
	e := strings.ToLower(strings.TrimSpace(expr))
	e = strings.ReplaceAll(e, "_", "-")
	e = strings.ReplaceAll(e, " ", "-")
	today := c.Today()
	switch e {
	case "":
		return Range{}, fmt.Errorf("empty date range")
	case "all", "all-time", "everything":
		return All(), nil
	case "today":
		return Range{today, today.AddDate(0, 0, 1), "today"}, nil
	case "yesterday":
		y := today.AddDate(0, 0, -1)
		return Range{y, today, "yesterday"}, nil
	case "this-week", "week":
		s := c.WeekStartOf(today)
		return Range{s, s.AddDate(0, 0, 7), "this week"}, nil
	case "last-week":
		s := c.WeekStartOf(today).AddDate(0, 0, -7)
		return Range{s, s.AddDate(0, 0, 7), "last week"}, nil
	case "this-month", "month":
		s := c.monthStart(today)
		return Range{s, s.AddDate(0, 1, 0), "this month"}, nil
	case "last-month":
		s := c.monthStart(today).AddDate(0, -1, 0)
		return Range{s, s.AddDate(0, 1, 0), "last month"}, nil
	case "this-quarter", "quarter":
		s := c.quarterStart(today)
		return Range{s, s.AddDate(0, 3, 0), quarterLabel(s)}, nil
	case "last-quarter":
		s := c.quarterStart(today).AddDate(0, -3, 0)
		return Range{s, s.AddDate(0, 3, 0), quarterLabel(s)}, nil
	case "this-year", "year":
		s := time.Date(today.Year(), 1, 1, 0, 0, 0, 0, c.Loc)
		return Range{s, s.AddDate(1, 0, 0), strconv.Itoa(s.Year())}, nil
	case "last-year":
		s := time.Date(today.Year()-1, 1, 1, 0, 0, 0, 0, c.Loc)
		return Range{s, s.AddDate(1, 0, 0), strconv.Itoa(s.Year())}, nil
	}
	if m := relRe.FindStringSubmatch(e); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || n > 100000 {
			return Range{}, fmt.Errorf("invalid relative range %q: the number must be at least 1", expr)
		}
		end := today.AddDate(0, 0, 1)
		var start time.Time
		var unit string
		switch m[2] {
		case "d":
			start = end.AddDate(0, 0, -n)
			unit = "day"
		case "w":
			start = end.AddDate(0, 0, -7*n)
			unit = "week"
		case "m":
			start = end.AddDate(0, -n, 0)
			unit = "month"
		}
		label := fmt.Sprintf("last %d %ss", n, unit)
		if n == 1 {
			label = "last " + unit
		}
		return Range{start, end, label}, nil
	}
	if dayRe.MatchString(e) {
		d, err := time.ParseInLocation("2006-01-02", e, c.Loc)
		if err != nil {
			return Range{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD", expr)
		}
		return Range{d, d.AddDate(0, 0, 1), d.Format("Mon 2 Jan 2006")}, nil
	}
	if monthRe.MatchString(e) {
		d, err := time.ParseInLocation("2006-01", e, c.Loc)
		if err != nil {
			return Range{}, fmt.Errorf("invalid month %q: use YYYY-MM", expr)
		}
		return Range{d, d.AddDate(0, 1, 0), d.Format("January 2006")}, nil
	}
	if m := quarterRe.FindStringSubmatch(e); m != nil {
		y, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		s := time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, c.Loc)
		return Range{s, s.AddDate(0, 3, 0), quarterLabel(s)}, nil
	}
	return Range{}, fmt.Errorf("unrecognised date range %q (try today, yesterday, 7d, this-week, last-month, this-quarter or YYYY-MM-DD)", expr)
}

func quarterLabel(s time.Time) string {
	return fmt.Sprintf("Q%d %d", (int(s.Month())-1)/3+1, s.Year())
}

// Bounds combines the common flag trio into one range:
//
//	rangeExpr  a Parse expression for the whole range
//	since      a Parse expression; the range starts where it starts and is open-ended
//	from, to   inclusive calendar bounds; each accepts any Parse expression and
//	           uses its first (from) or last (to) day
//
// rangeExpr cannot be combined with the others. Empty inputs return def.
func (c Clock) Bounds(rangeExpr, since, from, to string, def Range) (Range, error) {
	if rangeExpr != "" {
		if since != "" || from != "" || to != "" {
			return Range{}, fmt.Errorf("--range cannot be combined with --since, --from or --to")
		}
		return c.Parse(rangeExpr)
	}
	if since != "" && from != "" {
		return Range{}, fmt.Errorf("use either --since or --from, not both")
	}
	if since == "" && from == "" && to == "" {
		return def, nil
	}
	var r Range
	if since != "" {
		s, err := c.Parse(since)
		if err != nil {
			return Range{}, err
		}
		r.Start = s.Start
	}
	if from != "" {
		f, err := c.Parse(from)
		if err != nil {
			return Range{}, fmt.Errorf("--from: %w", err)
		}
		r.Start = f.Start
	}
	if to != "" {
		t, err := c.Parse(to)
		if err != nil {
			return Range{}, fmt.Errorf("--to: %w", err)
		}
		r.End = t.End
	}
	if !r.Start.IsZero() && !r.End.IsZero() && !r.Start.Before(r.End) {
		return Range{}, fmt.Errorf("the start of the range (%s) is after its end (%s)",
			r.Start.Format("2006-01-02"), r.LastDay().Format("2006-01-02"))
	}
	r.Label = c.Describe(r)
	return r, nil
}

// Describe produces a compact human label for an arbitrary range.
func (c Clock) Describe(r Range) string {
	switch {
	case r.IsZero():
		return "all time"
	case r.End.IsZero():
		return "since " + r.Start.In(c.Loc).Format("2006-01-02")
	case r.Start.IsZero():
		return "until " + r.LastDay().In(c.Loc).Format("2006-01-02")
	}
	first := r.Start.In(c.Loc).Format("2006-01-02")
	last := r.LastDay().In(c.Loc).Format("2006-01-02")
	if first == last {
		return r.Start.In(c.Loc).Format("Mon 2 Jan 2006")
	}
	return first + " – " + last
}

// Shift moves a range backwards or forwards by its own length in calendar
// terms where that is meaningful (days, weeks, months, quarters).
func (c Clock) Shift(r Range, n int) Range {
	if r.Start.IsZero() || r.End.IsZero() {
		return r
	}
	s, e := r.Start.In(c.Loc), r.End.In(c.Loc)
	var ns, ne time.Time
	switch {
	case s.Day() == 1 && e.Day() == 1 && monthsBetween(s, e) > 0:
		m := monthsBetween(s, e)
		ns, ne = s.AddDate(0, m*n, 0), e.AddDate(0, m*n, 0)
	default:
		days := daysBetween(s, e)
		ns, ne = s.AddDate(0, 0, days*n), e.AddDate(0, 0, days*n)
	}
	out := Range{Start: ns, End: ne}
	out.Label = c.Describe(out)
	return out
}

func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
}

func daysBetween(a, b time.Time) int {
	// Use calendar dates, not durations, so DST days still count as one.
	ad := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bd := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(bd.Sub(ad).Hours() / 24)
}

// ParseWeekday parses a configured week start ("monday", "sunday", "sat").
func ParseWeekday(s string) (time.Weekday, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return time.Monday, nil
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if s == name || (len(s) >= 3 && strings.HasPrefix(name, s)) {
			return d, nil
		}
	}
	return time.Monday, fmt.Errorf("unknown weekday %q", s)
}
