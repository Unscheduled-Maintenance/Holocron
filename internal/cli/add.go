package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

type addFlags struct {
	project string
	typ     string
	tags    []string
	marks   []string
	at      string
	raw     bool
	editor  bool
	json    bool
	quiet   bool
}

func (f *addFlags) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVarP(&f.project, "project", "p", "", "project name or alias (created on first use)")
	fs.StringVar(&f.typ, "type", "", "entry type: "+strings.Join(journal.TypeStrings(), ", ")+" (unique prefixes work)")
	fs.StringSliceVarP(&f.tags, "tag", "t", nil, "tag (repeatable or comma-separated)")
	fs.StringSliceVar(&f.marks, "mark", nil, "report mark: staff, one-on-one, quarterly, important, cross-team")
	fs.StringVar(&f.at, "at", "", "when it happened: 14:30, yesterday 16:00, 2026-10-02 09:15, -2h (default: now)")
	fs.BoolVar(&f.raw, "raw", false, "store the text exactly; do not read +project or #tag shorthand")
	fs.BoolVarP(&f.editor, "editor", "e", false, "write the entry in $VISUAL/$EDITOR")
	fs.BoolVar(&f.json, "json", false, "print the saved entry as JSON")
	fs.BoolVarP(&f.quiet, "quiet", "q", false, "print only the new entry ID")
	_ = cmd.RegisterFlagCompletionFunc("type", fixedCompletion(journal.TypeStrings()...))
	_ = cmd.RegisterFlagCompletionFunc("mark", fixedCompletion(journal.MarkStrings()...))
}

func newAddCmd(e *env) *cobra.Command {
	f := &addFlags{}
	cmd := &cobra.Command{
		Use:     "add [text...]",
		Aliases: []string{"a"},
		Short:   "Record an entry",
		Long: `Record an entry in the archive.

The text can be given as arguments, piped on stdin, or written in your editor
with --editor. With no text at an interactive terminal, a short prompt opens.

Shorthand inside the text sets metadata quickly:
  +project   assign a project (name or alias; created on first use)
  #tag       add a tag (quote the text: shells treat a bare # as a comment)
Tokens at the start or end are removed from the text; in mid-sentence the
word stays without its sigil. "#123", "C#" and "+1" are left alone.
Text starting with a type and a colon ("Decision: keep it") gets that type,
and the prefix is removed from the stored text.
Use --raw to turn shorthand off, or the flags for an unambiguous form.`,
		Example: `  holocron add "Enabled IAM Access Analyzer in all AWS regions"
  holocron add "Investigating unexpected exporter restarts" --project cloudflare --type investigation --tag monitoring
  holocron add "Fixed S3 bucket policy +aws #security"
  holocron add "Decision: retain the existing deployment model" --mark staff
  holocron add --at "yesterday 16:00" "Reviewed the moderation queue"
  echo "Fixed the deployment issue" | holocron add`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cmd.Context(), e, f, args)
		},
	}
	f.register(cmd)
	registerDynamicCompletion(cmd, e)
	return cmd
}

func runAdd(ctx context.Context, e *env, f *addFlags, args []string) error {
	text := strings.Join(args, " ")
	if text == "" && !f.editor {
		if !e.io.InTTY {
			b, err := io.ReadAll(io.LimitReader(e.io.In, 1<<20))
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			text = string(b)
		}
		if strings.TrimSpace(text) == "" {
			if e.io.InTTY && e.io.OutTTY {
				return runCapturePrompt(ctx, e, f)
			}
			return usagef("nothing to add: give the text as an argument, pipe it on stdin, or use --editor")
		}
	}
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	if f.editor {
		return addWithEditor(ctx, e, a, f, text)
	}
	res, err := a.Capture(ctx, app.CaptureInput{Text: text, Project: f.project, Type: f.typ, Tags: f.tags, Marks: f.marks, At: f.at, Raw: f.raw})
	if err != nil {
		return err
	}
	return reportSaved(e, a, f, res, "Added")
}

func reportSaved(e *env, a *app.App, f *addFlags, res app.CaptureResult, verb string) error {
	if res.CreatedProject != "" && !f.quiet && !f.json {
		e.note("%s", e.errStyle().Dim(fmt.Sprintf("Created project %q (rename or alias it with `holocron project edit`).", res.CreatedProject)))
	}
	switch {
	case f.json:
		return writeJSON(e.io.Out, entriesJSON([]journal.Entry{res.Entry})[0])
	case f.quiet:
		fmt.Fprintln(e.io.Out, res.Entry.ID)
	default:
		st := e.out()
		fmt.Fprintf(e.io.Out, "%s %s\n", st.Success(verb), summaryLine(a, st, res.Entry))
	}
	return nil
}

func addWithEditor(ctx context.Context, e *env, a *app.App, f *addFlags, initial string) error {
	argv, _, err := editor.Resolve(a.Config.Editor)
	if err != nil {
		return err
	}
	doc := a.DocFor(nil)
	doc.Body = initial
	doc.Project, doc.Type = f.project, f.typ
	doc.Tags = strings.Join(f.tags, ", ")
	doc.Marks = strings.Join(f.marks, ", ")
	if f.at != "" {
		doc.Time = f.at
	}
	edited, err := editor.Edit(ctx, argv, doc.Format("New Holocron entry"))
	if err != nil {
		return err
	}
	parsed, err := app.ParseEntryDoc(edited)
	if err != nil {
		return err
	}
	res, err := a.CreateFromDoc(ctx, parsed)
	if errors.Is(err, app.ErrEmptyEntry) {
		e.note("Nothing saved: the entry text was empty.")
		return nil
	}
	if err != nil {
		return err
	}
	return reportSaved(e, a, f, res, "Added")
}

func newNowCmd(e *env) *cobra.Command {
	f := &addFlags{}
	cmd := &cobra.Command{
		Use:   "now",
		Short: "Capture an entry in a focused prompt (or your editor with -e)",
		Long: `Open a focused one-line capture prompt. Type the entry (shorthand such as
+project and #tag works), press Enter to save or Esc to cancel.

With --editor, write the entry in $VISUAL/$EDITOR instead.

A handy shell alias:  alias hn='holocron now'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.editor {
				a, err := e.open(cmd.Context())
				if err != nil {
					return err
				}
				return addWithEditor(cmd.Context(), e, a, f, "")
			}
			if !e.io.InTTY || !e.io.OutTTY {
				return usagef("holocron now needs an interactive terminal; use `holocron add` in scripts")
			}
			return runCapturePrompt(cmd.Context(), e, f)
		},
	}
	f.register(cmd)
	registerDynamicCompletion(cmd, e)
	return cmd
}
