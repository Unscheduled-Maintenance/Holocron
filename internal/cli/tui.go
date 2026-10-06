package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/tui"
)

func runTUI(ctx context.Context, e *env) error {
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	return tui.Run(ctx, a)
}

func newTUICmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive archive (same as running holocron with no arguments)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !e.io.InTTY || !e.io.OutTTY {
				return usagef("the interactive interface needs a terminal")
			}
			return e.launchTUI(cmd.Context(), e)
		},
	}
}

// runCapturePrompt shows the focused capture prompt, then saves the entry
// with any flags given on the command line.
func runCapturePrompt(ctx context.Context, e *env, f *addFlags) error {
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	var ctxParts []string
	if f.project != "" {
		ctxParts = append(ctxParts, "project "+f.project)
	}
	if f.typ != "" {
		ctxParts = append(ctxParts, "type "+f.typ)
	}
	if len(f.tags) > 0 {
		ctxParts = append(ctxParts, "#"+strings.Join(f.tags, " #"))
	}
	if len(f.resolves) > 0 {
		ctxParts = append(ctxParts, "resolves #"+strings.Join(f.resolves, " #"))
	}
	text, ok, err := tui.Capture(ctx, strings.Join(ctxParts, " · "))
	if err != nil {
		return err
	}
	if !ok {
		e.note("Nothing saved.")
		return nil
	}
	res, err := a.Capture(ctx, app.CaptureInput{Text: text, Project: f.project, Type: f.typ, Tags: f.tags, Marks: f.marks, At: f.at, Raw: f.raw, Resolves: f.resolves})
	if err != nil {
		return err
	}
	return reportSaved(e, a, f, res, "Added")
}
