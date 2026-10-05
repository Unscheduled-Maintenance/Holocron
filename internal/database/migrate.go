package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one ordered schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations returns the embedded migrations in version order.
func Migrations() []Migration {
	ms, err := loadMigrations(migrationFS)
	if err != nil {
		// The migration files are compiled into the binary; a malformed name
		// is a programming error caught by the test suite.
		panic(err)
	}
	return ms
}

func loadMigrations(fsys fs.FS) ([]Migration, error) {
	files, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var ms []Migration
	seen := map[int]string{}
	for _, f := range files {
		base := filepath.Base(f)
		num, name, ok := strings.Cut(strings.TrimSuffix(base, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", base)
		}
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: invalid version", base)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", prev, base, v)
		}
		seen[v] = base
		body, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		ms = append(ms, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i, m := range ms {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1; missing version %d", i+1)
		}
	}
	return ms, nil
}

// LatestVersion is the schema version this build of Holocron expects.
func LatestVersion() int { return len(Migrations()) }

const migrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

// SchemaVersion returns the highest applied migration, or 0 for a new file.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return 0, describe(err)
	}
	if exists == 0 {
		return 0, nil
	}
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, describe(err)
	}
	return int(v.Int64), nil
}

// Migrate applies every pending migration, each in its own transaction.
// Before upgrading an archive that already holds data, a safety backup is
// written to backupDir (when non-empty).
func (db *DB) Migrate(ctx context.Context, backupDir string) error {
	return db.migrate(ctx, Migrations(), backupDir)
}

func (db *DB) migrate(ctx context.Context, ms []Migration, backupDir string) error {
	current, err := db.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	latest := len(ms)
	if current > latest {
		return fmt.Errorf("the archive %s uses schema version %d, but this Holocron only understands up to version %d; upgrade Holocron", db.Path, current, latest)
	}
	if current == latest {
		return nil
	}
	if current > 0 && backupDir != "" {
		dest := filepath.Join(backupDir, fmt.Sprintf("holocron-pre-migration-v%d-%s.db", current, time.Now().UTC().Format("20060102-150405")))
		if err := db.Backup(ctx, dest); err != nil {
			return fmt.Errorf("refusing to migrate without a safety backup: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, migrationsTable); err != nil {
		return describe(fmt.Errorf("creating migrations table: %w", err))
	}
	for _, m := range ms[current:] {
		err := db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Name, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
				return err
			}
			// user_version mirrors the schema version for external SQLite tools.
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, m.Version))
			return err
		})
		if err != nil {
			return describe(fmt.Errorf("applying migration %04d_%s: %w", m.Version, m.Name, err))
		}
	}
	return nil
}
