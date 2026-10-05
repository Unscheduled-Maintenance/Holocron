// Package database opens Holocron's SQLite archive, applies versioned
// migrations, and performs online backups.
//
// Holocron uses modernc.org/sqlite, a pure-Go SQLite build, so the binary
// needs no CGO toolchain. See docs/adr/0001-sqlite-driver.md.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DB is an open Holocron archive.
type DB struct {
	*sql.DB
	Path string
}

// Options controls how a database is opened.
type Options struct {
	// BackupDir receives an automatic safety backup before migrations are
	// applied to an existing archive. Empty disables the safety backup.
	BackupDir string
	// NoMigrate opens the database without applying pending migrations.
	// Used by diagnostics that must not modify the file.
	NoMigrate bool
	// MustExist fails instead of creating a new database.
	MustExist bool
}

// Connection settings, applied to every pooled connection:
//
//	foreign_keys=ON      enforce ON DELETE CASCADE / SET NULL relationships
//	journal_mode=WAL     readers (the TUI) never block a writer (the CLI) and
//	                     a crash cannot leave a half-written transaction
//	synchronous=FULL     a committed entry survives power loss; journal writes
//	                     are tiny, so the cost is unnoticeable
//	busy_timeout=5000    wait up to 5s for another Holocron process's write
//	                     instead of failing immediately with SQLITE_BUSY
//	cache_size=-32000    a 32 MB page cache (the default is 2 MB). Five years of
//	                     heavy use is about 10 MB, so the whole archive and its
//	                     search index stay cached; memory is used only as pages
//	                     are read. Large searches were spending a third of their
//	                     time re-reading pages from the file.
//	_txlock=immediate    write transactions take the write lock up front,
//	                     avoiding deadlocking read-to-write upgrades
func dsn(path string) string {
	return path + "?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=cache_size(-32000)" +
		"&_txlock=immediate"
}

// Open opens (creating if necessary) and migrates the archive at path.
func Open(ctx context.Context, path string, opts Options) (*DB, error) {
	if path == "" {
		return nil, errors.New("no database path configured")
	}
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if !exists {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("cannot access database %s: %w", path, statErr)
		}
		if opts.MustExist {
			return nil, fmt.Errorf("database %s does not exist", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("creating database directory: %w", err)
		}
	}

	sqldb, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	db := &DB{DB: sqldb, Path: path}
	if err := db.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, describe(fmt.Errorf("opening database %s: %w", path, err))
	}
	if err := db.checkIsHolocron(ctx, exists); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	if !exists {
		// Owner-only permissions: entries often contain confidential work notes.
		_ = os.Chmod(path, 0o600)
	}
	if !opts.NoMigrate {
		if err := db.Migrate(ctx, opts.BackupDir); err != nil {
			_ = sqldb.Close()
			return nil, err
		}
	}
	return db, nil
}

// checkIsHolocron refuses to adopt an unrelated, non-empty SQLite file.
func (db *DB) checkIsHolocron(ctx context.Context, existed bool) error {
	if !existed {
		return nil
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table'`).Scan(&tables); err != nil {
		return describe(fmt.Errorf("reading database %s: %w", db.Path, err))
	}
	if tables == 0 {
		return nil
	}
	var ok int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&ok)
	if err != nil {
		return describe(err)
	}
	if ok == 0 {
		return fmt.Errorf("%s is a SQLite database but not a Holocron archive; refusing to modify it", db.Path)
	}
	return nil
}

// Close checkpoints the write-ahead log so the main database file is
// self-contained, then closes the connection pool.
func (db *DB) Close() error {
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return db.DB.Close()
}

// Tx runs fn inside a write transaction, committing on success and rolling
// back on error or panic.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return describe(fmt.Errorf("starting transaction: %w", err))
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return describe(fmt.Errorf("committing transaction: %w", err))
	}
	return nil
}

// Backup writes a consistent, compacted copy of the live database to dest
// using VACUUM INTO, which is safe while other connections are reading or
// writing. dest must not already exist.
func (db *DB) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup destination %s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("creating backup directory: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		_ = os.Remove(dest)
		return describe(fmt.Errorf("writing backup %s: %w", dest, err))
	}
	_ = os.Chmod(dest, 0o600)
	return nil
}

// FTS5Available reports whether the SQLite build supports FTS5.
func (db *DB) FTS5Available(ctx context.Context) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE VIRTUAL TABLE temp.holocron_fts_probe USING fts5(x)`); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `DROP TABLE temp.holocron_fts_probe`)
	return err
}

// SQLiteVersion returns the linked SQLite library version.
func (db *DB) SQLiteVersion(ctx context.Context) string {
	var v string
	_ = db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&v)
	return v
}

// QuickCheck runs PRAGMA quick_check and returns nil when the file is sound.
func (db *DB) QuickCheck(ctx context.Context) error {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return describe(err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := rows.Err(); err != nil {
		return describe(err)
	}
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("... and %d more", len(problems)-5))
		}
		return fmt.Errorf("integrity problems: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ErrBusy reports that another process holds the database lock.
var ErrBusy = errors.New("the archive is locked by another Holocron process; try again in a moment")

// describe turns low-level SQLite failures into errors a person can act on,
// keeping the original error wrapped for debugging.
func describe(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return fmt.Errorf("%w (%w)", ErrBusy, err)
		case sqlite3.SQLITE_NOTADB:
			return fmt.Errorf("the file is not a SQLite database or is encrypted: %w", err)
		case sqlite3.SQLITE_CORRUPT:
			return fmt.Errorf("the database file is damaged; restore from a backup (see `holocron restore`): %w", err)
		case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM, sqlite3.SQLITE_CANTOPEN:
			return fmt.Errorf("cannot write to the database; check file permissions: %w", err)
		case sqlite3.SQLITE_FULL:
			return fmt.Errorf("the disk is full: %w", err)
		}
	}
	return err
}

// Describe is exported for callers that run their own queries.
func Describe(err error) error { return describe(err) }
