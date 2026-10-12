package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// planToday adds follow-ups planned for testNow's day (Wednesday 7 Oct).
func planToday(t *testing.T, m Model, bodies ...string) Model {
	t.Helper()
	for _, b := range bodies {
		if _, err := m.app.Store.AddEntry(context.Background(), journal.NewEntry{Body: b, Type: journal.TypeFollowUp, PlannedFor: "2026-10-07"}); err != nil {
			t.Fatal(err)
		}
	}
	return send(t, m, key("ctrl+r"))
}

func TestPlanPanelHiddenWithoutPlans(t *testing.T) {
	m := start(t, 100, 30)
	if m.planShown() || strings.Contains(plain(m), "Plan ·") {
		t.Fatal("no plan should mean no panel")
	}
	m = send(t, m, key("tab"))
	if m.planFocus || !strings.Contains(m.status, "No plan") {
		t.Fatalf("tab without a plan: focus %v status %q", m.planFocus, m.status)
	}
}

func TestPlanNextWorkingDay(t *testing.T) {
	m := start(t, 100, 30)
	m = send(t, m, key("P"))
	if m.layer != layerPlan || m.planDay != "2026-10-08" {
		t.Fatalf("P should open the plan input for Thursday: layer %v day %q", m.layer, m.planDay)
	}
	if !strings.Contains(plain(m), "Thu 8 Oct ›") {
		t.Errorf("plan prompt missing:\n%s", plain(m))
	}
	m = typeText(t, m, "Vagrant Registry go bye bye")
	m = send(t, m, key("enter"))
	if m.layer != layerPlan || !strings.Contains(m.status, "Planned #7 for Thu 8 Oct") {
		t.Fatalf("after first item: layer %v status %q", m.layer, m.status)
	}
	m = typeText(t, m, "Pen test remediation +security")
	m = send(t, m, key("enter"), key("esc"))
	if m.layer != layerNone {
		t.Fatal("esc should finish planning")
	}
	view := plain(m)
	for _, want := range []string{"Plan · none yet", "next Thu 8 Oct · 2"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n%s", want, view)
		}
	}
	if len(m.nextPlan.Items) != 2 || m.nextPlan.Items[1].Project != "security" {
		t.Fatalf("next plan = %+v", m.nextPlan.Items)
	}
	checkFits(t, m)
}

func TestPlanPanelResolveAndCarry(t *testing.T) {
	m := start(t, 100, 30)
	m = planToday(t, m, "Vagrant Registry go bye bye", "Pen test remediation")
	view := plain(m)
	for _, want := range []string{"Plan · Wed 7 Oct (today) · 0 of 2 done", "○ #7 Vagrant Registry", "○ #8 Pen test"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n%s", want, view)
		}
	}

	m = send(t, m, key("tab"))
	if !m.planFocus || !strings.Contains(plain(m), "focused") {
		t.Fatal("tab should focus the plan")
	}
	m = send(t, m, key("x"))
	if m.plan.Done() != 1 || !strings.Contains(plain(m), "✓ #7") || !strings.Contains(plain(m), "1 of 2 done") {
		t.Fatalf("x should resolve the first item:\n%s", plain(m))
	}
	m = send(t, m, key("j"), key("esc"))
	if m.planFocus || m.planCursor != 1 {
		t.Fatalf("esc should leave the plan: focus %v cursor %d", m.planFocus, m.planCursor)
	}

	// Planning tomorrow offers the unfinished item, unticked.
	m = send(t, m, key("P"))
	if m.layer != layerPicker || m.picker.kind != pickCarry || len(m.picker.items) != 1 || m.picker.items[0].checked {
		t.Fatalf("P should offer to carry over the unfinished item: %+v", m.picker)
	}
	m = send(t, m, key("space"), key("enter"))
	if m.layer != layerPlan || !strings.Contains(m.status, "Carried 1 item to Thu 8 Oct") {
		t.Fatalf("after carrying: layer %v status %q", m.layer, m.status)
	}
	m = send(t, m, key("esc"))
	if len(m.plan.Items) != 1 || len(m.nextPlan.Items) != 1 || m.nextPlan.Items[0].Ref() != "#8" {
		t.Fatalf("plans after carrying: %+v / %+v", m.plan.Items, m.nextPlan.Items)
	}

	// Nothing left to carry: P goes straight to the input.
	m = send(t, m, key("P"))
	if m.layer != layerPlan {
		t.Fatalf("P with nothing to carry: layer %v", m.layer)
	}
}

func TestPlanPanelFits(t *testing.T) {
	m := start(t, 100, 30)
	var items []string
	for i := 1; i <= 9; i++ {
		items = append(items, fmt.Sprintf("Plan item %d with a fairly long description that will need truncating", i))
	}
	m = planToday(t, m, items...)
	if !strings.Contains(plain(m), "+4 more") {
		t.Errorf("overflow hint missing:\n%s", plain(m))
	}
	m = send(t, m, key("tab"))
	for range 8 {
		m = send(t, m, key("j"))
	}
	if !strings.Contains(plain(m), "Plan item 9") {
		t.Errorf("the selected item should scroll into view:\n%s", plain(m))
	}
	m = send(t, m, key("tab"))
	for _, sz := range [][2]int{{120, 40}, {80, 24}, {60, 20}, {45, 15}, {40, 12}, {30, 8}} {
		m = send(t, m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		checkFits(t, m)
		if len(m.entries) > 0 && !strings.Contains(plain(m), "Follow up with engineering") && m.height >= 14 {
			t.Errorf("%dx%d: the plan crowded out the list:\n%s", sz[0], sz[1], plain(m))
		}
	}
}

func TestDetailShowsPlannedDay(t *testing.T) {
	m := start(t, 80, 30)
	m = planToday(t, m, "Pen test remediation")
	m = send(t, m, key("g"), key("enter"))
	if !strings.Contains(plain(m), "Planned   Wed 7 Oct") {
		t.Errorf("detail missing the planned day:\n%s", plain(m))
	}
}
