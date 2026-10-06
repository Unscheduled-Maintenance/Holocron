package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// CheckStatus is the outcome of one diagnostic check.
type CheckStatus string

// Check outcomes.
const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckInfo CheckStatus = "info"
)

// Check is one diagnostic result. Details never include entry content.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

// DoctorOptions control diagnostics.
type DoctorOptions struct {
	RebuildIndex bool
}

// Doctor inspects configuration, the archive and the environment to explain
// unexpected behaviour. It never modifies the archive unless RebuildIndex is
// set, and never applies migrations.
func Doctor(ctx context.Context, opts Options, dopts DoctorOptions) []Check {
	var checks []Check
	add := func(name string, st CheckStatus, format string, args ...any) {
		checks = append(checks, Check{Name: name, Status: st, Detail: fmt.Sprintf(format, args...)})
	}
	add("platform", CheckInfo, "%s/%s, %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	cfg, paths, err := LoadConfig(opts)
	if err != nil {
		add("config", CheckFail, "%v", err)
		if paths.ConfigFile == "" {
			return checks
		}
	} else if _, statErr := os.Stat(paths.ConfigFile); errors.Is(statErr, fs.ErrNotExist) {
		add("config", CheckOK, "no file at %s; using defaults", paths.ConfigFile)
	} else {
		add("config", CheckOK, "%s is valid", paths.ConfigFile)
	}
	// After a config failure cfg holds the defaults, which would mislead.
	if aliases, aerr := journal.NewTypeAliases(cfg.TypeAliases); err == nil && aerr == nil {
		if len(aliases) == 0 {
			add("type aliases", CheckInfo, "none configured")
		} else {
			add("type aliases", CheckInfo, "%s", aliases.Describe())
		}
	}
	add("database path", CheckInfo, "%s", paths.Database)

	if _, err := os.Stat(paths.Database); errors.Is(err, fs.ErrNotExist) {
		add("database", CheckWarn, "no archive yet; it will be created by the first command that needs it")
	} else {
		checks = append(checks, checkDatabase(ctx, paths, dopts)...)
	}

	if argv, src, err := editor.Resolve(cfg.Editor); err != nil {
		add("editor", CheckWarn, "%v", err)
	} else if _, lerr := exec.LookPath(argv[0]); lerr != nil {
		add("editor", CheckWarn, "%s from %s is not on PATH", argv[0], src)
	} else {
		add("editor", CheckOK, "%s (from %s)", strings.Join(argv, " "), src)
	}

	if p, err := exec.LookPath("git"); err != nil {
		add("git", CheckInfo, "git not found; `holocron import git` is unavailable")
	} else {
		add("git", CheckOK, "%s", p)
	}

	switch provider := strings.ToLower(cfg.AI.Provider); provider {
	case "", "none":
		add("ai", CheckInfo, "disabled (no ai.provider configured); nothing is ever sent anywhere")
	default:
		envName := cfg.AI.APIKeyEnv
		if envName == "" {
			envName = "ANTHROPIC_API_KEY"
		}
		model := cfg.AI.Model
		if model == "" {
			model = "default model"
		}
		if os.Getenv(envName) == "" {
			add("ai", CheckWarn, "%s (%s) is configured but $%s is not set", provider, model, envName)
		} else {
			add("ai", CheckOK, "%s (%s); key found in $%s. Data is sent only when you pass --ai", provider, model, envName)
		}
	}
	return checks
}

func checkDatabase(ctx context.Context, paths config.Paths, dopts DoctorOptions) []Check {
	var checks []Check
	add := func(name string, st CheckStatus, format string, args ...any) {
		checks = append(checks, Check{Name: name, Status: st, Detail: fmt.Sprintf(format, args...)})
	}
	if st, err := os.Stat(paths.Database); err == nil {
		detail := fmt.Sprintf("%.1f KB", float64(st.Size())/1024)
		if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
			add("database file", CheckWarn, "%s; readable by other users (mode %o); consider chmod 600", detail, st.Mode().Perm())
		} else {
			add("database file", CheckOK, "%s", detail)
		}
	}
	db, err := database.Open(ctx, paths.Database, database.Options{NoMigrate: true, MustExist: true})
	if err != nil {
		add("database", CheckFail, "%v", err)
		return checks
	}
	defer db.Close()
	add("sqlite", CheckInfo, "SQLite %s (pure Go, modernc.org/sqlite)", db.SQLiteVersion(ctx))
	if err := db.QuickCheck(ctx); err != nil {
		add("integrity", CheckFail, "%v", err)
	} else {
		add("integrity", CheckOK, "quick_check passed")
	}
	v, err := db.SchemaVersion(ctx)
	switch latest := database.LatestVersion(); {
	case err != nil:
		add("schema", CheckFail, "%v", err)
	case v < latest:
		add("schema", CheckWarn, "version %d; %d migration(s) will be applied on next use (a safety backup is taken first)", v, latest-v)
	case v > latest:
		add("schema", CheckFail, "version %d is newer than this Holocron supports (%d); upgrade Holocron", v, latest)
	default:
		add("schema", CheckOK, "version %d (current)", v)
	}
	if err := db.FTS5Available(ctx); err != nil {
		add("full-text search", CheckFail, "FTS5 unavailable: %v", err)
	} else {
		add("full-text search", CheckOK, "FTS5 available")
	}
	var mode string
	_ = db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	var fk int
	_ = db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	add("sqlite settings", CheckInfo, "journal_mode=%s, foreign_keys=%d", mode, fk)

	if v == database.LatestVersion() {
		store := journal.NewStore(db)
		if dopts.RebuildIndex {
			n, err := store.RebuildIndex(ctx)
			if err != nil {
				add("search index", CheckFail, "rebuild failed: %v", err)
			} else {
				add("search index", CheckOK, "rebuilt for %d entries", n)
			}
		} else if err := store.CheckIndex(ctx); err != nil {
			add("search index", CheckFail, "%v", err)
		} else {
			add("search index", CheckOK, "in sync")
		}
		if st, err := store.Stats(ctx); err == nil {
			span := ""
			if st.First != "" {
				span = fmt.Sprintf(", %s to %s", LocalDate(st.First, time.Local), LocalDate(st.Last, time.Local))
			}
			add("contents", CheckInfo, "%d entries, %d projects, %d tags%s", st.Entries, st.Projects, st.Tags, span)
		}
	}
	backups, _ := filepath.Glob(filepath.Join(paths.BackupDir, "*.db"))
	if len(backups) == 0 {
		add("backups", CheckWarn, "none in %s; run `holocron backup`", paths.BackupDir)
	} else {
		latest, latestTime := "", time.Time{}
		for _, b := range backups {
			if st, err := os.Stat(b); err == nil && st.ModTime().After(latestTime) {
				latest, latestTime = b, st.ModTime()
			}
		}
		add("backups", CheckOK, "%d in %s (latest %s)", len(backups), paths.BackupDir, filepath.Base(latest))
	}
	return checks
}
