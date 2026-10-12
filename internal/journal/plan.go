package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// DayLayout is how a planned day is written: a local calendar date.
const DayLayout = "2006-01-02"

var errPlanType = fmt.Errorf("%w: only follow-ups can be planned for a day", ErrInvalid)

// NormalizeDay validates a planned day (YYYY-MM-DD). "" means no day.
func NormalizeDay(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, err := time.Parse(DayLayout, s); err != nil {
		return "", fmt.Errorf("%w: %q is not a day (use YYYY-MM-DD)", ErrInvalid, s)
	}
	return s, nil
}

// DayPlan is the follow-ups planned for one day, in the order they were
// written, finished or not.
type DayPlan struct {
	Day   string // YYYY-MM-DD, or "" when there is no such plan
	Items []Entry
}

// Done counts the resolved items.
func (p DayPlan) Done() int {
	n := 0
	for _, e := range p.Items {
		if e.ResolvedAt != nil {
			n++
		}
	}
	return n
}

// Plans returns the current plan and the next one, relative to today
// (YYYY-MM-DD).
//
// The current plan is the latest planned day that is not after today, so a
// plan stays current until a newer one starts: after a day off, the last
// plan is still there. The next plan is the earliest planned day after
// today. A follow-up planned for an earlier day than the current plan is no
// longer on a plan, just open (see docs/adr/0009-daily-plan.md).
func (s *Store) Plans(ctx context.Context, today string) (current, next DayPlan, err error) {
	if today, err = NormalizeDay(today); err != nil || today == "" {
		return current, next, fmt.Errorf("%w: today is not a day", ErrInvalid)
	}
	var cur, nxt sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT max(planned_for) FROM entries WHERE type = 'follow-up' AND planned_for <= ?),
		(SELECT min(planned_for) FROM entries WHERE type = 'follow-up' AND planned_for > ?)`,
		today, today).Scan(&cur, &nxt)
	if err != nil {
		return current, next, database.Describe(err)
	}
	if current, err = s.dayPlan(ctx, cur.String); err != nil {
		return current, next, err
	}
	next, err = s.dayPlan(ctx, nxt.String)
	return current, next, err
}

func (s *Store) dayPlan(ctx context.Context, day string) (DayPlan, error) {
	p := DayPlan{Day: day}
	if day == "" {
		return p, nil
	}
	var err error
	p.Items, err = s.Find(ctx, Query{PlannedFor: day, Order: OldestFirst})
	return p, err
}
