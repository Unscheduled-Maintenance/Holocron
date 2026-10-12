package timerange

import (
	"testing"
	"time"
)

func TestParseDay(t *testing.T) {
	loc := mustLoc(t, "Pacific/Auckland")
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, loc) }
	week := DefaultWorkWeek()
	// Friday 9 October 2026, end of the day.
	fri := NewClock(time.Date(2026, 10, 9, 17, 30, 0, 0, loc), loc, time.Monday)
	// Monday 12 October 2026.
	mon := NewClock(time.Date(2026, 10, 12, 17, 30, 0, 0, loc), loc, time.Monday)

	tests := []struct {
		c    Clock
		expr string
		want time.Time
	}{
		{fri, "", d(12)},
		{fri, "tomorrow", d(12)},
		{fri, "Next", d(12)},
		{fri, "today", d(9)},
		{fri, "friday", d(16)},
		{fri, "tue", d(13)},
		{fri, "2026-10-21", d(21)},
		{mon, "tomorrow", d(13)},
		{mon, "monday", d(19)},
	}
	for _, tt := range tests {
		got, err := tt.c.ParseDay(tt.expr, week)
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("%s ParseDay(%q) = %v, %v; want %v", tt.c.Now.Weekday(), tt.expr, got, err, tt.want)
		}
	}
	for _, bad := range []string{"2026-10-08", "someday", "fr"} {
		if _, err := fri.ParseDay(bad, week); err == nil {
			t.Errorf("ParseDay(%q) succeeded", bad)
		}
	}

	// Someone who works Sunday to Thursday.
	sunThu, err := ParseWorkWeek([]string{"sun", "monday", "tue", "wed", "thursday"})
	if err != nil {
		t.Fatal(err)
	}
	thu := NewClock(time.Date(2026, 10, 8, 17, 0, 0, 0, loc), loc, time.Sunday)
	if got, _ := thu.ParseDay("tomorrow", sunThu); !got.Equal(d(11)) {
		t.Errorf("Sunday–Thursday: tomorrow = %v", got)
	}
}

func TestParseWorkWeek(t *testing.T) {
	w, err := ParseWorkWeek(nil)
	if err != nil || w != DefaultWorkWeek() || w[time.Saturday] || !w[time.Monday] {
		t.Fatalf("default = %v, %v", w, err)
	}
	for _, bad := range [][]string{{"funday"}, {""}} {
		if _, err := ParseWorkWeek(bad); err == nil {
			t.Errorf("ParseWorkWeek(%q) succeeded", bad)
		}
	}
}
