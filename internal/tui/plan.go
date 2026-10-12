package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// The plan panel sits under the header on the list and detail screens and
// shows the current plan (docs/adr/0009-daily-plan.md). tab focuses it, so
// its items can be resolved without finding them in the list; P plans the
// next working day, first offering to carry over unfinished items.

// sPlanDone marks finished plan items; the ✓ says the same without colour.
var sPlanDone = lipgloss.NewStyle().Foreground(sFaint.GetForeground()).Strikethrough(true)

// maxPlanItems caps how many plan items the panel shows at once.
const maxPlanItems = 6

// plansMsg carries freshly loaded plans.
type plansMsg struct {
	current, next journal.DayPlan
	err           error
}

// loadPlans fetches the current and next plans.
func (m Model) loadPlans() tea.Cmd {
	ctx, a := m.ctx, m.app
	return func() tea.Msg {
		cur, next, err := a.Plans(ctx)
		return plansMsg{current: cur, next: next, err: err}
	}
}

func (m Model) applyPlans(msg plansMsg) Model {
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		return m
	}
	m.plan, m.nextPlan = msg.current, msg.next
	m.planCursor = min(m.planCursor, max(0, len(m.plan.Items)-1))
	if len(m.plan.Items) == 0 {
		m.planFocus = false
	}
	return m
}

// planShown reports whether the panel is on screen.
func (m Model) planShown() bool {
	if m.compact() || m.screen == screenReport {
		return false
	}
	return m.plan.Day != "" || m.nextPlan.Day != ""
}

// planRows is how many plan items fit: a quarter of the screen, at most
// maxPlanItems.
func (m Model) planRows() int {
	return min(len(m.plan.Items), maxPlanItems, max(1, (m.height-8)/4))
}

// planHeight is the panel's height, including its heading and rule.
func (m Model) planHeight() int {
	if !m.planShown() {
		return 0
	}
	return 2 + m.planRows()
}

func (m Model) renderPlan() string {
	var lines []string
	right := ""
	if m.nextPlan.Day != "" {
		right = sDim.Render(fmt.Sprintf("next %s · %d ", m.app.FormatDay(m.nextPlan.Day), len(m.nextPlan.Items)))
	}
	if m.plan.Day == "" {
		lines = append(lines, spread(" "+sHeading.Render("Plan")+sDim.Render(" · none yet · P plans"), right, m.width))
	} else {
		day := m.app.FormatDay(m.plan.Day)
		if m.plan.Day == m.app.Today() {
			day += " (today)"
		}
		head := " " + sHeading.Render("Plan") + sDim.Render(" · "+day+" · ") +
			sSuccess.Render(fmt.Sprintf("%d of %d done", m.plan.Done(), len(m.plan.Items)))
		if m.planFocus {
			head += sCursor.Render("  focused")
		}
		lines = append(lines, spread(head, right, m.width))
		n := m.planRows()
		start := 0
		if m.planCursor >= n {
			start = m.planCursor - n + 1
		}
		for i := start; i < start+n; i++ {
			lines = append(lines, m.renderPlanItem(m.plan.Items[i], m.planFocus && i == m.planCursor))
		}
		if hidden := len(m.plan.Items) - n; hidden > 0 && len(lines) > 1 {
			lines[len(lines)-1] = spread(lines[len(lines)-1], sFaint.Render(fmt.Sprintf("+%d more · tab ", hidden)), m.width)
		}
	}
	lines = append(lines, sRule.Render(strings.Repeat("─", m.width)))
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) renderPlanItem(e journal.Entry, selected bool) string {
	marker := "   "
	if selected {
		marker = " " + sCursor.Render("▌ ")
	}
	box, text := sWarn.Render("○"), e.Title()
	used := 3 + 2 + len(e.Ref()) + 2
	proj := ""
	if e.Project != "" && m.width >= 60 {
		name := truncate(e.Project, 14)
		proj = sProject.Render(name) + " "
		used += lipgloss.Width(name) + 1
	}
	text = truncate(text, max(4, m.width-used-1))
	switch {
	case e.ResolvedAt != nil:
		box, text = sSuccess.Render("✓"), sPlanDone.Render(text)
	case selected:
		text = sSelected.Render(text)
	}
	return marker + box + " " + sFaint.Render(e.Ref()) + " " + proj + text
}

// updatePlanPanel handles keys while the plan panel has focus.
func (m Model) updatePlanPanel(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	items := m.plan.Items
	switch k.String() {
	case "tab", "esc", "shift+tab":
		m.planFocus = false
		return m, nil
	case "up", "k":
		m.planCursor = max(0, m.planCursor-1)
		return m, nil
	case "down", "j":
		m.planCursor = min(len(items)-1, m.planCursor+1)
		return m, nil
	case "x", "space":
		if m.planCursor < len(items) {
			return m, m.toggleResolved(items[m.planCursor])
		}
		return m, nil
	case "e":
		if m.planCursor < len(items) {
			e := items[m.planCursor]
			return m.openForm(&e)
		}
		return m, nil
	case "X":
		if m.planCursor < len(items) {
			return m.openResolveForm(items[m.planCursor])
		}
		return m, nil
	case "q":
		return m, tea.Quit
	case "P", "?", "f1":
		m.planFocus = false
		return m.updateList(k)
	}
	return m, nil
}

// openPlan starts planning the next working day: first a chance to carry
// over the current plan's unfinished items, then an input for new ones.
func (m Model) openPlan() (tea.Model, tea.Cmd) {
	day, err := m.app.PlanDay("")
	if err != nil {
		m.setStatus(errText(err), true)
		return m, nil
	}
	m.planDay = day
	var items []pickItem
	for _, e := range m.plan.Items {
		if e.IsOpen() && e.PlannedFor != day {
			items = append(items, pickItem{label: e.Title(), value: e.UID, detail: e.Ref()})
		}
	}
	if len(items) == 0 {
		return m.openPlanInput()
	}
	m.picker = newPicker(pickCarry, items, "")
	m.picker.title = "Carry over to " + m.app.FormatDay(day) + "? Unticked items stay open as follow-ups"
	m.layer = layerPicker
	return m, nil
}

func (m Model) openPlanInput() (tea.Model, tea.Cmd) {
	m.layer = layerPlan
	m.input = newInput(m.app.FormatDay(m.planDay)+" › ", "What will you do? One item per enter; +project #tag works")
	m.input.SetWidth(max(10, m.width-4))
	return m, m.input.Focus()
}

// applyCarry moves the ticked items onto the new plan, then opens the
// input for new items.
func (m Model) applyCarry() (tea.Model, tea.Cmd) {
	var refs []string
	for _, it := range m.picker.items {
		if it.checked {
			refs = append(refs, it.value)
		}
	}
	next, focus := m.openPlanInput()
	if len(refs) == 0 {
		return next, focus
	}
	ctx, a, day := m.ctx, m.app, m.planDay
	carry := func() tea.Msg {
		moved, err := a.Replan(ctx, refs, day)
		if err != nil {
			return plannedMsg{err: err}
		}
		return plannedMsg{text: fmt.Sprintf("Carried %d %s to %s", len(moved), plural(len(moved), "item", "items"), a.FormatDay(day))}
	}
	return next, tea.Batch(focus, carry)
}

// plannedMsg reports a change to a plan made from the plan input.
type plannedMsg struct {
	text string
	err  error
}

// submitPlanItem saves one plan item and keeps the input open for the next.
func (m Model) submitPlanItem() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		m.layer = layerNone
		return m, nil
	}
	m.input.SetValue("")
	ctx, a, day := m.ctx, m.app, m.planDay
	return m, func() tea.Msg {
		res, err := a.Capture(ctx, app.CaptureInput{Text: value, PlanFor: day})
		if err != nil {
			return plannedMsg{err: err}
		}
		text := fmt.Sprintf("Planned %s for %s", res.Entry.Ref(), a.FormatDay(day))
		if res.CreatedProject != "" {
			text += " · created project " + res.CreatedProject
		}
		return plannedMsg{text: text}
	}
}

func (m Model) finishPlanned(msg plannedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		return m, nil
	}
	m.setStatus(msg.text, false)
	return m, tea.Batch(m.reload(), m.publish())
}
