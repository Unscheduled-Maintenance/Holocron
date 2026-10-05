package timerange

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	clockRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?\s*(am|pm)?$`)
	agoRe   = regexp.MustCompile(`^-?(\d+)\s*(m|min|mins|h|hr|hrs|d)(?:\s+ago)?$`)
)

// ParseMoment resolves a point in time for back-dating an entry. Supported:
//
//	now
//	HH:MM, H:MMpm          today at that time
//	-90m, -2h, 3h ago, -1d relative to now
//	yesterday [HH:MM]      (noon when no time is given)
//	today HH:MM
//	YYYY-MM-DD [HH:MM]     (noon when no time is given)
//	RFC 3339               2026-10-05T09:14:00+13:00
func (c Clock) ParseMoment(s string) (time.Time, error) {
	in := strings.TrimSpace(s)
	e := strings.ToLower(in)
	if e == "" || e == "now" {
		return c.Now, nil
	}
	if t, err := time.Parse(time.RFC3339, in); err == nil {
		return t, nil
	}
	if m := agoRe.FindStringSubmatch(e); m != nil {
		n, _ := strconv.Atoi(m[1])
		var d time.Duration
		switch m[2] {
		case "m", "min", "mins":
			d = time.Duration(n) * time.Minute
		case "h", "hr", "hrs":
			d = time.Duration(n) * time.Hour
		case "d":
			return c.Now.AddDate(0, 0, -n), nil
		}
		return c.Now.Add(-d), nil
	}

	datePart, timePart := e, ""
	if i := strings.IndexByte(e, ' '); i > 0 {
		datePart, timePart = strings.TrimSpace(e[:i]), strings.TrimSpace(e[i+1:])
	} else if clockRe.MatchString(e) {
		datePart, timePart = "today", e
	}

	var day time.Time
	switch datePart {
	case "today":
		day = c.Today()
	case "yesterday":
		day = c.Today().AddDate(0, 0, -1)
	default:
		d, err := time.ParseInLocation("2006-01-02", datePart, c.Loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("unrecognised time %q (try 14:30, yesterday 16:00, 2026-10-02 09:15 or -2h)", in)
		}
		day = d
	}
	if timePart == "" {
		return day.Add(12 * time.Hour), nil
	}
	m := clockRe.FindStringSubmatch(timePart)
	if m == nil {
		return time.Time{}, fmt.Errorf("unrecognised time of day %q (use HH:MM)", timePart)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec := 0
	if m[3] != "" {
		sec, _ = strconv.Atoi(m[3])
	}
	switch m[4] {
	case "am":
		if h == 12 {
			h = 0
		}
	case "pm":
		if h < 12 {
			h += 12
		}
	}
	if h > 23 || mi > 59 || sec > 59 {
		return time.Time{}, fmt.Errorf("invalid time of day %q", timePart)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, mi, sec, 0, c.Loc), nil
}
