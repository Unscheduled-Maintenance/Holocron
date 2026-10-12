package timerange

import (
	"fmt"
	"strings"
	"time"
)

// WorkWeek is the set of days someone works.
type WorkWeek [7]bool

// DefaultWorkWeek is Monday to Friday.
func DefaultWorkWeek() WorkWeek {
	var w WorkWeek
	for d := time.Monday; d <= time.Friday; d++ {
		w[d] = true
	}
	return w
}

// ParseWorkWeek parses configured working days ("monday", "tue", ...).
// An empty list is the default, Monday to Friday.
func ParseWorkWeek(days []string) (WorkWeek, error) {
	if len(days) == 0 {
		return DefaultWorkWeek(), nil
	}
	var w WorkWeek
	for _, s := range days {
		if strings.TrimSpace(s) == "" {
			return w, fmt.Errorf("empty weekday")
		}
		d, err := ParseWeekday(s)
		if err != nil {
			return w, err
		}
		w[d] = true
	}
	return w, nil
}

// Next returns midnight on the first working day after the day containing
// t, in t's location.
func (w WorkWeek) Next(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	for range 7 {
		d = d.AddDate(0, 0, 1)
		if w[d.Weekday()] {
			return d
		}
	}
	return d // no working days at all: tomorrow
}

// ParseDay resolves the day a plan is for. Supported:
//
//	(empty), next, tomorrow  the next working day
//	today
//	monday, tue, ...         the next such day after today
//	YYYY-MM-DD
//
// "tomorrow" means the next working day, so on a Friday it is Monday. Days
// before today are refused. The result is local midnight.
func (c Clock) ParseDay(s string, w WorkWeek) (time.Time, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	today := c.Today()
	switch e {
	case "", "next", "tomorrow":
		return w.Next(today), nil
	case "today":
		return today, nil
	}
	if d, err := time.ParseInLocation("2006-01-02", e, c.Loc); err == nil {
		if d.Before(today) {
			return time.Time{}, fmt.Errorf("%s is in the past", e)
		}
		return d, nil
	}
	if len(e) >= 3 {
		if wd, err := ParseWeekday(e); err == nil {
			ahead := (int(wd)-int(today.Weekday())+6)%7 + 1
			return today.AddDate(0, 0, ahead), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised day %q (try tomorrow, today, monday or 2026-10-19)", s)
}
