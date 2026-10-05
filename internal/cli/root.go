// Package cli implements Holocron's command-line interface with Cobra. Each
// command parses flags, calls the shared application layer (internal/app,
// internal/journal, internal/report, internal/export) and renders the result.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

// Version information, set at build time with -ldflags.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Exit codes. Documented in the README.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitNotFound    = 3
	ExitInterrupted = 130
)

// IO bundles the streams a command uses, so tests can substitute buffers.
type IO struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	InTTY  bool
	OutTTY bool
	ErrTTY bool
	// Width is the terminal width when Out is a terminal.
	Width int
}

// SystemIO returns the process's standard streams.
func SystemIO() IO {
	io := IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	io.InTTY = term.IsTerminal(int(os.Stdin.Fd()))
	io.OutTTY = term.IsTerminal(int(os.Stdout.Fd()))
	io.ErrTTY = term.IsTerminal(int(os.Stderr.Fd()))
	if io.OutTTY {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			io.Width = w
		}
	}
	return io
}

// globals are the persistent flags shared by every command.
type globals struct {
	db     string
	config string
	color  string
	utc    bool
}

// env is what a running command needs: streams, flags and lazily opened app.
type env struct {
	io  IO
	g   *globals
	app *app.App
	// launchTUI starts the interactive interface; replaced in tests.
	launchTUI func(ctx context.Context, e *env) error
}

func (e *env) appOptions() app.Options {
	return app.Options{ConfigPath: e.g.config, DBPath: e.g.db, UTC: e.g.utc}
}

// open returns the application, opening the archive on first use.
func (e *env) open(ctx context.Context) (*app.App, error) {
	if e.app != nil {
		return e.app, nil
	}
	a, err := app.Open(ctx, e.appOptions())
	if err != nil {
		return nil, err
	}
	e.app = a
	return a, nil
}

func (e *env) close() {
	if e.app != nil {
		_ = e.app.Close()
		e.app = nil
	}
}

// out returns a styler for stdout.
func (e *env) out() style.Styler { return style.New(style.ColorEnabled(e.g.color, e.io.OutTTY)) }

// errStyle returns a styler for stderr.
func (e *env) errStyle() style.Styler { return style.New(style.ColorEnabled(e.g.color, e.io.ErrTTY)) }

// note prints a diagnostic or hint to stderr.
func (e *env) note(format string, args ...any) {
	fmt.Fprintf(e.io.Err, format+"\n", args...)
}

// hint prints guidance only for people at a terminal, never into pipelines.
func (e *env) hint(format string, args ...any) {
	if e.io.OutTTY && e.io.ErrTTY {
		fmt.Fprintln(e.io.Err, e.errStyle().Dim(fmt.Sprintf(format, args...)))
	}
}

// usageError marks an error as caused by invalid usage (exit code 2).
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// NewRootCommand builds the command tree.
func NewRootCommand(streams IO) (*cobra.Command, *env) {
	g := &globals{}
	e := &env{io: streams, g: g, launchTUI: runTUI}
	root := &cobra.Command{
		Use:   "holocron",
		Short: "Holocron is your personal work archive",
		Long: `Holocron is your personal work archive.

Capture what you do in a few seconds, find it again years later, and turn it
into weekly updates, staff reports, one-on-one notes and quarterly evidence.
Everything stays in a local SQLite file that you own.

Run holocron with no arguments in a terminal to open the interactive archive.`,
		Example: `  holocron add "Enabled IAM Access Analyzer in all AWS regions" --project aws --tag security
  holocron add "Fixed S3 bucket policy +aws #security"
  holocron today
  holocron search cloudtrail --since 30d
  holocron report staff`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if e.io.InTTY && e.io.OutTTY {
				return e.launchTUI(cmd.Context(), e)
			}
			return cmd.Help()
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.PersistentFlags().StringVar(&g.db, "db", "", "path to the archive database (default: platform data directory; env HOLOCRON_DB)")
	root.PersistentFlags().StringVar(&g.config, "config", "", "path to the configuration file (env HOLOCRON_CONFIG)")
	root.PersistentFlags().StringVar(&g.color, "color", "auto", "colour output: auto, always or never (NO_COLOR is honoured)")
	root.PersistentFlags().BoolVar(&g.utc, "utc", false, "show and interpret times in UTC instead of local time")
	_ = root.RegisterFlagCompletionFunc("color", fixedCompletion("auto", "always", "never"))
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error { return usageError{flagError{err}} })

	root.AddGroup(
		&cobra.Group{ID: "capture", Title: "Capture:"},
		&cobra.Group{ID: "browse", Title: "Browse and search:"},
		&cobra.Group{ID: "entries", Title: "Manage entries:"},
		&cobra.Group{ID: "reports", Title: "Reports and export:"},
		&cobra.Group{ID: "archive", Title: "Archive maintenance:"},
	)
	root.SetHelpCommandGroupID("archive")
	root.SetCompletionCommandGroupID("archive")

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("capture", newAddCmd(e), newNowCmd(e))
	add("browse", newDayCmd(e, "today", "Show today's entries", "today"),
		newDayCmd(e, "yesterday", "Show yesterday's entries", "yesterday"),
		newDayCmd(e, "week", "Show this week's entries", "this-week"),
		newListCmd(e), newSearchCmd(e), newShowCmd(e))
	add("entries", newEditCmd(e), newDeleteCmd(e), newMarkCmd(e, false), newMarkCmd(e, true), newResolveCmd(e),
		newProjectCmd(e), newTagCmd(e))
	add("reports", newReportCmd(e), newExportCmd(e), newImportCmd(e))
	add("archive", newBackupCmd(e), newRestoreCmd(e), newConfigCmd(e), newDoctorCmd(e), newTUICmd(e), newVersionCmd(e))
	return root, e
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, args []string, streams IO) int {
	root, e := NewRootCommand(streams)
	defer e.close()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code := exitCode(ctx, err)
	es := e.errStyle()
	msg := err.Error()
	if code == ExitUsage && isSyntaxError(err) {
		msg += "\n" + es.Dim("Run 'holocron --help' or add --help to a command for usage.")
	}
	fmt.Fprintf(streams.Err, "%s %s\n", es.Error("holocron:"), msg)
	return code
}

func exitCode(ctx context.Context, err error) int {
	var ue usageError
	switch {
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		return ExitInterrupted
	case errors.As(err, &ue), errors.Is(err, journal.ErrInvalid):
		return ExitUsage
	case errors.Is(err, journal.ErrNotFound):
		return ExitNotFound
	case strings.HasPrefix(err.Error(), "unknown command"), strings.HasPrefix(err.Error(), "accepts "),
		strings.HasPrefix(err.Error(), "requires "), strings.HasPrefix(err.Error(), "unknown flag"):
		return ExitUsage
	case errors.Is(err, database.ErrBusy):
		return ExitError
	}
	return ExitError
}

// isSyntaxError reports whether err came from parsing the command line
// itself, where pointing at --help is useful.
func isSyntaxError(err error) bool {
	var fe flagError
	if errors.As(err, &fe) {
		return true
	}
	s := err.Error()
	for _, p := range []string{"unknown command", "accepts ", "requires ", "unknown flag", "unknown shorthand"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// flagError wraps errors from flag parsing.
type flagError struct{ error }

func (f flagError) Unwrap() error { return f.error }

func fixedCompletion(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func newVersionCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Holocron version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v := "holocron " + Version
			if Commit != "" {
				v += " (" + Commit
				if Date != "" {
					v += ", " + Date
				}
				v += ")"
			}
			fmt.Fprintf(e.io.Out, "%s\nschema version %d\n", v, database.LatestVersion())
			return nil
		},
	}
}
