package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

type planFlags struct {
	day    string
	carry  []string
	unplan []string
	addFlags
}

func newPlanCmd(e *env) *cobra.Command {
	f := &planFlags{}
	cmd := &cobra.Command{
		Use:   "plan [item...]",
		Short: "Plan the next working day, or show the plan",
		Long: `Write down what you plan to do next. Each argument (or each line piped on
stdin, with any "*" or "-" bullet removed; a line ending in a colon
without a bullet is taken as a heading and skipped) becomes a follow-up planned for
the next working day. Working days are set by work_days in the
configuration (Monday to Friday by default), so a plan written on a Friday
is for Monday. --for picks another day.

With no items, show the current plan and the next one. The current plan is
the latest one that has started: after a day off it is still the last plan
you wrote, until a newer one starts.

Plan items are follow-ups: resolve them as usual. An unfinished item from
an older plan stays open as an ordinary follow-up. To bring it onto a new
plan instead, carry it over: --carry 42 moves #42 to the plan, keeping its
number and history; --carry all carries every unfinished item from the
current plan.`,
		Example: `  holocron plan "Vagrant Registry go bye bye" "Pen test remediation +security"
  holocron plan --for monday "Quarterly planning"
  holocron plan --carry all
  holocron plan --unplan 42
  pbpaste | holocron plan
  holocron plan`,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd.Context(), e, f, args)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&f.day, "for", "", "the day planned: tomorrow (the next working day; the default), today, a weekday or YYYY-MM-DD")
	fs.StringSliceVar(&f.carry, "carry", nil, "move these follow-ups onto the plan, or \"all\" for the current plan's unfinished items (repeatable)")
	fs.StringSliceVar(&f.unplan, "unplan", nil, "take these follow-ups off their plan; they stay open (repeatable)")
	fs.StringVarP(&f.project, "project", "p", "", "project for every item (created on first use)")
	fs.StringSliceVarP(&f.tags, "tag", "t", nil, "tag for every item (repeatable or comma-separated)")
	fs.StringSliceVar(&f.marks, "mark", nil, "report mark for every item: staff, one-on-one, quarterly, important, cross-team")
	fs.BoolVar(&f.raw, "raw", false, "store the text exactly; do not read +project or #tag shorthand")
	fs.BoolVar(&f.json, "json", false, "print the plan as JSON")
	_ = cmd.RegisterFlagCompletionFunc("for", fixedCompletion("tomorrow", "today", "monday", "tuesday", "wednesday", "thursday", "friday"))
	_ = cmd.RegisterFlagCompletionFunc("carry", entryCompletion(e))
	_ = cmd.RegisterFlagCompletionFunc("unplan", entryCompletion(e))
	_ = cmd.RegisterFlagCompletionFunc("mark", fixedCompletion(journal.MarkStrings()...))
	return cmd
}

func runPlan(ctx context.Context, e *env, f *planFlags, args []string) error {
	items := planItems(args)
	if len(args) == 0 && !e.io.InTTY {
		b, err := io.ReadAll(io.LimitReader(e.io.In, 1<<20))
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		items = planLines(string(b))
	}
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	if len(f.unplan) > 0 {
		if len(items) > 0 || len(f.carry) > 0 {
			return usagef("--unplan cannot be combined with new items or --carry")
		}
		done, err := a.Replan(ctx, f.unplan, "")
		for _, en := range done {
			fmt.Fprintf(e.io.Out, "%s %s\n", e.out().Success("Unplanned"), summaryLine(a, e.out(), en))
		}
		return err
	}
	if len(items) == 0 && len(f.carry) == 0 {
		if f.day != "" {
			return usagef("nothing to plan: give the items as arguments or on stdin")
		}
		return showPlans(ctx, e, a, f.json)
	}
	day, err := a.PlanDay(f.day)
	if err != nil {
		return err
	}

	carry := f.carry
	if len(carry) == 1 && strings.EqualFold(carry[0], "all") {
		unfinished, err := a.Unfinished(ctx, day)
		if err != nil {
			return err
		}
		carry = nil
		for _, en := range unfinished {
			carry = append(carry, en.UID)
		}
		if len(carry) == 0 && len(items) == 0 {
			e.note("Nothing to carry over: the current plan has no unfinished items.")
			return nil
		}
	}
	st := e.out()
	var planned []journal.Entry
	if len(carry) > 0 {
		moved, err := a.Replan(ctx, carry, day)
		for _, en := range moved {
			if !f.json {
				fmt.Fprintf(e.io.Out, "%s %s\n", st.Success("Carried"), summaryLine(a, st, en))
			}
		}
		if err != nil {
			return err
		}
		planned = append(planned, moved...)
	}
	in := app.CaptureInput{Project: f.project, Tags: f.tags, Marks: f.marks, Raw: f.raw}
	results, err := a.Plan(ctx, day, items, in)
	for _, res := range results {
		planned = append(planned, res.Entry)
		if !f.json {
			if err := reportSaved(e, a, &f.addFlags, res, "Planned"); err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	if f.json {
		return writeJSON(e.io.Out, entriesJSON(planned))
	}
	e.hint("%s", e.errStyle().Dim("Plan for "+a.FormatDay(day)+": see it with `holocron plan`."))
	return nil
}

// planItems drops empty arguments.
func planItems(args []string) []string {
	var out []string
	for _, s := range args {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// planLines turns pasted text into items: one per line, without list
// bullets, blank lines or headings ("What I plan to do tomorrow:").
func planLines(text string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		bulleted := false
		for _, bullet := range []string{"* ", "- ", "• "} {
			if rest, ok := strings.CutPrefix(line, bullet); ok {
				line, bulleted = strings.TrimSpace(rest), true
			}
		}
		if !bulleted && strings.HasSuffix(line, ":") {
			continue
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type planJSON struct {
	Day   string `json:"day"`
	Items any    `json:"items"`
}

func showPlans(ctx context.Context, e *env, a *app.App, asJSON bool) error {
	cur, next, err := a.Plans(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		doc := map[string]*planJSON{"current": nil, "next": nil}
		if cur.Day != "" {
			doc["current"] = &planJSON{Day: cur.Day, Items: entriesJSON(cur.Items)}
		}
		if next.Day != "" {
			doc["next"] = &planJSON{Day: next.Day, Items: entriesJSON(next.Items)}
		}
		return writeJSON(e.io.Out, doc)
	}
	st := e.out()
	if cur.Day == "" && next.Day == "" {
		fmt.Fprintln(e.io.Out, st.Dim("No plan yet."))
		e.hint("%s", e.errStyle().Dim(`Plan the next working day with: holocron plan "first thing" "second thing"`))
		return nil
	}
	if cur.Day != "" {
		renderPlan(e.io.Out, st, cur, planHeading(a, cur.Day))
	}
	if next.Day != "" {
		if cur.Day != "" {
			fmt.Fprintln(e.io.Out)
		}
		renderPlan(e.io.Out, st, next, "Next: "+a.FormatDay(next.Day))
	}
	return nil
}

func planHeading(a *app.App, day string) string {
	h := "Plan for " + a.FormatDay(day)
	if day == a.Today() {
		h += " (today)"
	}
	return h
}

func renderPlan(w io.Writer, st style.Styler, p journal.DayPlan, heading string) {
	fmt.Fprintf(w, "%s  %s\n", st.Heading(heading), st.Dim(fmt.Sprintf("%d of %d done", p.Done(), len(p.Items))))
	for _, en := range p.Items {
		box, text := st.Warn("○"), en.Title()
		if en.ResolvedAt != nil {
			box, text = st.Success("✓"), st.Dim(text)
		}
		line := "  " + box + " " + st.Faint(en.Ref()) + "  "
		if en.Project != "" {
			line += st.Project(en.Project) + "  "
		}
		fmt.Fprintln(w, line+text)
	}
}
