package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
)

func newConfigCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show configuration and file locations",
		Long: `Holocron works without a configuration file. When you want to change a
default, run holocron config edit to open (and create) the TOML file in your
editor. Secrets such as API keys never belong in it.`,
	}
	var asJSON bool
	path := &cobra.Command{
		Use:   "path",
		Short: "Print where Holocron keeps its configuration, archive and backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, p, err := app.LoadConfig(e.appOptions())
			if err != nil && p.ConfigFile == "" {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, map[string]string{"config": p.ConfigFile, "database": p.Database, "data_dir": p.DataDir, "backups": p.BackupDir})
			}
			st := e.out()
			exists := func(f string) string {
				if _, err := os.Stat(f); errors.Is(err, fs.ErrNotExist) {
					return st.Dim("  (not created yet)")
				}
				return ""
			}
			fmt.Fprintf(e.io.Out, "%s  %s%s\n", st.Dim("config  "), p.ConfigFile, exists(p.ConfigFile))
			fmt.Fprintf(e.io.Out, "%s  %s%s\n", st.Dim("database"), p.Database, exists(p.Database))
			fmt.Fprintf(e.io.Out, "%s  %s%s\n", st.Dim("backups "), p.BackupDir, exists(p.BackupDir))
			return err
		},
	}
	path.Flags().BoolVar(&asJSON, "json", false, "print as JSON")

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (defaults plus your file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, p, err := app.LoadConfig(e.appOptions())
			if err != nil {
				return err
			}
			text, err := config.Encode(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "# Effective configuration (file: %s)\n# database resolves to %s\n\n%s", p.ConfigFile, p.Database, text)
			return nil
		},
	}

	edit := &cobra.Command{
		Use:   "edit",
		Short: "Open the configuration file in your editor (creating it if needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, p, err := app.LoadConfig(e.appOptions())
			if err != nil && p.ConfigFile == "" {
				return err
			}
			if _, statErr := os.Stat(p.ConfigFile); errors.Is(statErr, fs.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(p.ConfigFile, []byte(config.Template), 0o600); err != nil {
					return err
				}
				e.note("Created %s", p.ConfigFile)
			}
			if !e.io.InTTY {
				fmt.Fprintln(e.io.Out, p.ConfigFile)
				return nil
			}
			argv, _, rerr := editor.Resolve(cfg.Editor)
			if rerr != nil {
				return rerr
			}
			c := editor.Command(cmd.Context(), argv, p.ConfigFile)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("editor failed: %w", err)
			}
			if _, _, err := app.LoadConfig(e.appOptions()); err != nil {
				return fmt.Errorf("the configuration now has a problem; run `holocron config edit` again to fix it: %w", err)
			}
			e.note("Configuration is valid.")
			return nil
		},
	}
	cmd.AddCommand(path, show, edit)
	return cmd
}

func newDoctorCmd(e *env) *cobra.Command {
	var asJSON, rebuild bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, archive health, search index and environment",
		Long: `Explain why Holocron might be behaving strangely: configuration validity,
database integrity, schema version, FTS5 availability, search index
consistency, backups, editor and AI provider setup. Doctor does not migrate
or modify the archive unless --rebuild-index is given, and its output never
includes entry text.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := app.Doctor(cmd.Context(), e.appOptions(), app.DoctorOptions{RebuildIndex: rebuild})
			failed := false
			for _, c := range checks {
				if c.Status == app.CheckFail {
					failed = true
				}
			}
			if asJSON {
				if err := writeJSON(e.io.Out, checks); err != nil {
					return err
				}
			} else {
				st := e.out()
				for _, c := range checks {
					var badge string
					switch c.Status {
					case app.CheckOK:
						badge = st.Success("ok  ")
					case app.CheckWarn:
						badge = st.Warn("warn")
					case app.CheckFail:
						badge = st.Error("FAIL")
					default:
						badge = st.Dim("info")
					}
					fmt.Fprintf(e.io.Out, "%s  %s %s\n", badge, st.Dim(padRight(c.Name, 17)), c.Detail)
				}
			}
			if failed {
				return errors.New("one or more checks failed")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	cmd.Flags().BoolVar(&rebuild, "rebuild-index", false, "rebuild the full-text search index from the entries")
	return cmd
}
