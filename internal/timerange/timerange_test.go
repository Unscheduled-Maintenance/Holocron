package timerange

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s unavailable: %v", name, err)
	}
	return loc
}

func TestParse(t *testing.T) {
	loc := mustLoc(t, "Pacific/Auckland")
	// Monday 5 October 2026, mid-afternoon.
	c := NewClock(time.Date(2026, 10, 5, 15, 30, 0, 0, loc), loc, time.Monday)
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, loc) }

	tests := []struct {
		expr       string
		start, end time.Time
	}{
		{"today", d(2026, 10, 5), d(2026, 10, 6)},
		{"Yesterday", d(2026, 10, 4), d(2026, 10, 5)},
		{"7d", d(2026, 9, 29), d(2026, 10, 6)},
		{"1d", d(2026, 10, 5), d(2026, 10, 6)},
		{"30d", d(2026, 9, 6), d(2026, 10, 6)},
		{"2w", d(2026, 9, 22), d(2026, 10, 6)},
		{"this-week", d(2026, 10, 5), d(2026, 10, 12)},
		{"this week", d(2026, 10, 5), d(2026, 10, 12)},
		{"last-week", d(2026, 9, 28), d(2026, 10, 5)},
		{"this-month", d(2026, 10, 1), d(2026, 11, 1)},
		{"last-month", d(2026, 9, 1), d(2026, 10, 1)},
		{"this-quarter", d(2026, 10, 1), d(2027, 1, 1)},
		{"last-quarter", d(2026, 7, 1), d(2026, 10, 1)},
		{"this-year", d(2026, 1, 1), d(2027, 1, 1)},
		{"2026-09-14", d(2026, 9, 14), d(2026, 9, 15)},
		{"2026-02", d(2026, 2, 1), d(2026, 3, 1)},
		{"2026-Q3", d(2026, 7, 1), d(2026, 10, 1)},
		{"2026q1", d(2026, 1, 1), d(2026, 4, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			r, err := c.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.expr, err)
			}
			if !r.Start.Equal(tt.start) || !r.End.Equal(tt.end) {
				t.Fatalf("Parse(%q) = [%v, %v), want [%v, %v)", tt.expr, r.Start, r.End, tt.start, tt.end)
			}
			if r.Label == "" {
				t.Errorf("Parse(%q) has no label", tt.expr)
			}
		})
	}

	for _, bad := range []string{"", "fortnight", "0d", "2026-13-01", "2026-Q5"} {
		if _, err := c.Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
	if r, err := c.Parse("all"); err != nil || !r.IsZero() {
		t.Errorf("Parse(all) = %v, %v", r, err)
	}
}

func TestWeekStartSunday(t *testing.T) {
	c := NewClock(time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC), time.UTC, time.Sunday) // Wednesday
	r, err := c.Parse("this-week")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC); !r.Start.Equal(want) {
		t.Fatalf("start = %v, want %v", r.Start, want)
	}
}

func TestDaylightSavingDay(t *testing.T) {
	// New Zealand DST starts on Sunday 27 September 2026: that day is 23 hours long.
	loc := mustLoc(t, "Pacific/Auckland")
	c := NewClock(time.Date(2026, 9, 27, 20, 0, 0, 0, loc), loc, time.Monday)
	r, err := c.Parse("today")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.End.Sub(r.Start); got != 23*time.Hour {
		t.Fatalf("DST day length = %v, want 23h", got)
	}
	if r.End.Hour() != 0 {
		t.Fatalf("range should end at local midnight, got %v", r.End)
	}
	// A late-evening entry on the DST day must fall inside "today".
	if !r.Contains(time.Date(2026, 9, 27, 23, 30, 0, 0, loc)) {
		t.Fatal("23:30 on DST day not contained in today")
	}
}

func TestBounds(t *testing.T) {
	c := NewClock(time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC), time.UTC, time.Monday)
	def := Range{Label: "default"}
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, time.UTC) }

	r, err := c.Bounds("", "", "", "", def)
	if err != nil || r.Label != "default" {
		t.Fatalf("empty bounds = %v, %v", r, err)
	}
	r, err = c.Bounds("", "", "2026-09-01", "2026-09-30", def)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Start.Equal(d(9, 1)) || !r.End.Equal(d(10, 1)) {
		t.Fatalf("inclusive from/to = [%v, %v)", r.Start, r.End)
	}
	if r.Label != "2026-09-01 – 2026-09-30" {
		t.Errorf("label = %q", r.Label)
	}
	r, err = c.Bounds("", "30d", "", "", def)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Start.Equal(d(9, 6)) || !r.End.IsZero() {
		t.Fatalf("since = [%v, %v)", r.Start, r.End)
	}
	r, err = c.Bounds("", "", "last-month", "last-month", def)
	if err != nil || !r.Start.Equal(d(9, 1)) || !r.End.Equal(d(10, 1)) {
		t.Fatalf("expression from/to = %v, %v", r, err)
	}

	errCases := [][4]string{
		{"today", "7d", "", ""},
		{"", "7d", "2026-09-01", ""},
		{"", "", "2026-09-30", "2026-09-01"},
		{"", "", "nope", ""},
	}
	for _, ec := range errCases {
		if _, err := c.Bounds(ec[0], ec[1], ec[2], ec[3], def); err == nil {
			t.Errorf("Bounds%v succeeded, want error", ec)
		}
	}
}

func TestShift(t *testing.T) {
	c := NewClock(time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC), time.UTC, time.Monday)
	week, _ := c.Parse("this-week")
	prev := c.Shift(week, -1)
	if want := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC); !prev.Start.Equal(want) {
		t.Fatalf("shifted week start = %v, want %v", prev.Start, want)
	}
	month, _ := c.Parse("this-month")
	prevMonth := c.Shift(month, -1)
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !prevMonth.Start.Equal(want) || !prevMonth.End.Equal(month.Start) {
		t.Fatalf("shifted month = [%v, %v)", prevMonth.Start, prevMonth.End)
	}
	q, _ := c.Parse("this-quarter")
	if next := c.Shift(q, 1); !next.Start.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("shifted quarter start = %v", next.Start)
	}
}

func TestParseMoment(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 5, 15, 30, 0, 0, loc)
	c := NewClock(now, loc, time.Monday)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"09:14", time.Date(2026, 10, 5, 9, 14, 0, 0, loc)},
		{"2:05pm", time.Date(2026, 10, 5, 14, 5, 0, 0, loc)},
		{"12:10am", time.Date(2026, 10, 5, 0, 10, 0, 0, loc)},
		{"yesterday 16:00", time.Date(2026, 10, 4, 16, 0, 0, 0, loc)},
		{"yesterday", time.Date(2026, 10, 4, 12, 0, 0, 0, loc)},
		{"today 08:00", time.Date(2026, 10, 5, 8, 0, 0, 0, loc)},
		{"2026-10-02 09:15", time.Date(2026, 10, 2, 9, 15, 0, 0, loc)},
		{"2026-10-02", time.Date(2026, 10, 2, 12, 0, 0, 0, loc)},
		{"-2h", now.Add(-2 * time.Hour)},
		{"90m ago", now.Add(-90 * time.Minute)},
		{"-1d", now.AddDate(0, 0, -1)},
		{"2026-10-01T10:00:00Z", time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got, err := c.ParseMoment(tt.in)
		if err != nil {
			t.Errorf("ParseMoment(%q): %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseMoment(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"25:00", "teatime", "2026-10-02 9", "yesterday noonish"} {
		if _, err := c.ParseMoment(bad); err == nil {
			t.Errorf("ParseMoment(%q) succeeded, want error", bad)
		}
	}
}

func TestParseWeekday(t *testing.T) {
	for in, want := range map[string]time.Weekday{"": time.Monday, "sunday": time.Sunday, "Sat": time.Saturday, "MON": time.Monday} {
		got, err := ParseWeekday(in)
		if err != nil || got != want {
			t.Errorf("ParseWeekday(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseWeekday("caturday"); err == nil {
		t.Error("expected error")
	}
}
