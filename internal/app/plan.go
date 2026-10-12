package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// The daily plan: follow-ups planned for a day. See
// docs/adr/0009-daily-plan.md.

// WorkWeek returns the configured working days.
func (a *App) WorkWeek() timerange.WorkWeek { return a.workWeek }

// Today returns today's date as YYYY-MM-DD.
func (a *App) Today() string { return a.Clock().Today().Format(journal.DayLayout) }

// PlanDay resolves the day a plan is for ("", "tomorrow", "monday",
// "2026-10-19", ...) as YYYY-MM-DD. The default is the next working day.
func (a *App) PlanDay(s string) (string, error) {
	d, err := a.Clock().ParseDay(s, a.workWeek)
	if err != nil {
		return "", fmt.Errorf("%w: %w", journal.ErrInvalid, err)
	}
	return d.Format(journal.DayLayout), nil
}

// FormatDay renders a planned day for people: "Tue 13 Oct", with the year
// when it is not this year.
func (a *App) FormatDay(day string) string {
	d, err := time.Parse(journal.DayLayout, day)
	if err != nil {
		return day
	}
	if d.Year() != a.Now().Year() {
		return d.Format("Mon 2 Jan 2006")
	}
	return d.Format("Mon 2 Jan")
}

// Plans returns the current plan (the latest one not after today) and the
// next one (the earliest after today).
func (a *App) Plans(ctx context.Context) (current, next journal.DayPlan, err error) {
	return a.Store.Plans(ctx, a.Today())
}

// Plan captures each item as a follow-up planned for day (YYYY-MM-DD from
// PlanDay). Items use the same shorthand as quick capture; in supplies the
// project, tags and marks they share.
func (a *App) Plan(ctx context.Context, day string, items []string, in CaptureInput) ([]CaptureResult, error) {
	var out []CaptureResult
	for _, text := range items {
		item := in
		item.Text, item.PlanFor = text, day
		res, err := a.Capture(ctx, item)
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// Replan moves follow-ups onto day's plan, keeping their numbers and
// history; day "" takes them off any plan.
func (a *App) Replan(ctx context.Context, refs []string, day string) ([]journal.Entry, error) {
	var out []journal.Entry
	for _, ref := range splitRefs(refs) {
		e, err := a.Store.Get(ctx, ref)
		if err != nil {
			return out, err
		}
		if slices.ContainsFunc(out, func(x journal.Entry) bool { return x.ID == e.ID }) {
			continue
		}
		if e.Type != journal.TypeFollowUp {
			return out, fmt.Errorf("%w: %s is not a follow-up, so it cannot be planned", journal.ErrInvalid, e.Ref())
		}
		if e, err = a.Store.Update(ctx, e.ID, journal.Patch{PlannedFor: &day}); err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Unfinished returns the current plan's open items that are not already
// planned for day: what could be carried over to it.
func (a *App) Unfinished(ctx context.Context, day string) ([]journal.Entry, error) {
	cur, _, err := a.Plans(ctx)
	if err != nil {
		return nil, err
	}
	var out []journal.Entry
	for _, e := range cur.Items {
		if e.IsOpen() && e.PlannedFor != day {
			out = append(out, e)
		}
	}
	return out, nil
}
