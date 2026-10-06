package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Unscheduled-Maintenance/Holocron/internal/ai"
	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

// newAIProvider is replaced in tests.
var newAIProvider = func(cfg config.AIConfig) (ai.Provider, error) { return ai.New(cfg, os.Getenv) }

// runAIReport sends the entries a deterministic report selected (and only
// those) to the configured provider, then shows the result with a source
// list. Failures fall back to the deterministic report.
// runAIReport rewrites the report with the AI provider. It reports whether a
// report was delivered, so --record is skipped when the person cancels.
func runAIReport(ctx context.Context, e *env, a *app.App, rep report.Report, f *reportFlags) (bool, error) {
	if strings.ToLower(f.format) == "json" {
		return false, usagef("--ai produces Markdown; use --format text or markdown")
	}
	if rep.IsEmpty() {
		e.note("The report has no entries to rewrite; nothing was sent.")
		return true, renderDeterministic(e, a, rep, f)
	}
	provider, err := newAIProvider(a.Config.AI)
	if err != nil {
		return false, err
	}
	ids := rep.SourceIDs()
	disclosure := fmt.Sprintf("This sends the full text of %d selected %s (%s) to %s.",
		len(ids), plural(len(ids), "entry", "entries"), idList(rep.Refs(ids)), provider.Name())
	if !f.yes {
		if !e.io.InTTY {
			return false, usagef("%s Pass --yes to confirm non-interactively.", disclosure)
		}
		if !confirm(e, disclosure+" Continue?") {
			e.note("Nothing was sent.")
			return false, nil
		}
	}
	resp, err := provider.Generate(ctx, ai.BuildRequest(rep))
	if err != nil {
		e.note("%s AI report failed: %v. Showing the deterministic report instead.", e.errStyle().Warn("warning:"), err)
		if rerr := renderDeterministic(e, a, rep, f); rerr != nil {
			return false, rerr
		}
		return false, fmt.Errorf("AI report failed: %w", err)
	}
	doc := resp.Text + "\n" + ai.Provenance(resp.Text, rep, provider.Name())
	if f.output != "" && !f.yes && e.io.InTTY {
		fmt.Fprintln(e.io.Out, doc)
		if !confirm(e, "Write this report to "+expandPath(f.output)+"?") {
			e.note("Not written.")
			return false, nil
		}
	}
	return true, writeOutput(e, f.output, []byte(doc), "AI report")
}

func renderDeterministic(e *env, a *app.App, rep report.Report, f *reportFlags) error {
	var buf bytes.Buffer
	ro := report.RenderOptions{ShowIDs: f.ids, Explain: f.explain, Loc: a.Loc, TimeLayout: a.TimeLayout(),
		Styler: style.New(f.output == "" && style.ColorEnabled(e.g.color, e.io.OutTTY))}
	var err error
	if strings.HasPrefix(strings.ToLower(f.format), "m") {
		err = report.Markdown(&buf, rep, ro)
	} else {
		err = report.Text(&buf, rep, ro)
	}
	if err != nil {
		return err
	}
	return writeOutput(e, f.output, buf.Bytes(), "report")
}

func idList(refs []string) string {
	parts := make([]string, 0, len(refs))
	for i, ref := range refs {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("+%d more", len(refs)-10))
			break
		}
		parts = append(parts, ref)
	}
	return strings.Join(parts, " ")
}
