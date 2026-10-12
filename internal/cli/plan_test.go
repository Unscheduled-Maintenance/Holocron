package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanCommand(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.ok("plan"), "No plan yet.")

	// Today's plan, as if it had been written yesterday evening.
	out := h.ok("plan", "--for", "today", `Vagrant Registry "go bye bye"`, "Pen test remediation +security")
	mustContain(t, out, "Planned", "#1", "#2", "security")
	h.ok("resolve", "1")

	future := time.Now().AddDate(0, 0, 30).Format("2006-01-02")
	r := h.runIn("What I plan to do:\n\n* Quarterly planning\n- Review #ops alerts\n", "plan", "--for", future)
	if r.code != 0 {
		t.Fatalf("stdin plan: %+v", r)
	}
	mustContain(t, r.out, "#3", "#4")

	show := h.ok("plan")
	mustContain(t, show, "(today)", "1 of 2 done", "✓ #1", "○ #2", "security", "Next: ", "0 of 2 done", "Quarterly planning", "Review ops alerts")
	if strings.Index(show, "Pen test") > strings.Index(show, "Next: ") {
		t.Errorf("current plan should come first:\n%s", show)
	}

	// Carrying the unfinished item keeps its number.
	out = h.ok("plan", "--carry", "all", "--for", future)
	mustContain(t, out, "Carried", "#2")
	mustNotContain(t, out, "#1")
	show = h.ok("plan")
	mustContain(t, show, "1 of 1 done", "0 of 3 done", "Pen test remediation")

	mustContain(t, h.ok("plan", "--unplan", "2"), "Unplanned", "#2")
	mustContain(t, h.ok("list", "--open"), "Pen test remediation")
	mustNotContain(t, h.ok("plan"), "Pen test remediation")
	mustContain(t, h.ok("show", "3"), "Planned")

	var doc map[string]struct {
		Day   string `json:"day"`
		Items []struct {
			Ref        string  `json:"ref"`
			PlannedFor *string `json:"planned_for"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(h.ok("plan", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["next"].Day != future || len(doc["next"].Items) != 2 || *doc["next"].Items[0].PlannedFor != future {
		t.Errorf("plan --json = %+v", doc)
	}
}

func TestPlanErrors(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "A note")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"plan", "--for", "someday", "x"}, "unrecognised day"},
		{[]string{"plan", "--for", "2020-01-01", "x"}, "in the past"},
		{[]string{"plan", "Note: not a follow-up"}, "a plan item is a follow-up"},
		{[]string{"plan", "--carry", "1"}, "not a follow-up"},
		{[]string{"plan", "--unplan", "1", "x"}, "cannot be combined"},
		{[]string{"plan", "--for", "monday"}, "nothing to plan"},
	} {
		r := h.run(tc.args...)
		if r.code == 0 || !strings.Contains(r.err, tc.want) {
			t.Errorf("holocron %s: exit %d, stderr %q; want %q", strings.Join(tc.args, " "), r.code, r.err, tc.want)
		}
	}
	if r := h.run("plan", "--carry", "all"); r.code != 0 || !strings.Contains(r.err, "Nothing to carry over") {
		t.Errorf("carry with no plan: %+v", r)
	}
}

func TestPlanSkipsNonWorkingDays(t *testing.T) {
	h := newHarness(t)
	// Every day but today is a day off, so "tomorrow" is a week away.
	today := strings.ToLower(time.Now().Weekday().String())
	cfg := `work_days = ["` + today + "\"]\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.ok("plan", "--json", "Something")
	want := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	mustContain(t, out, `"planned_for": "`+want+`"`)
}
