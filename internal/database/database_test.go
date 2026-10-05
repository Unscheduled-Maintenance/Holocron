package database

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "archive", "holocron.db")
	db, err := Open(context.Background(), path, Options{BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func TestOpenCreatesAndMigrates(t *testing.T) {
	ctx := context.Background()
	db, _ := openTemp(t)
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestVersion() || v < 1 {
		t.Fatalf("schema version = %d, want %d", v, LatestVersion())
	}
	var uv int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != v {
		t.Fatalf("user_version = %d (%v), want %d", uv, err, v)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (%v)", fk, err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (%v)", mode, err)
	}
	if err := db.FTS5Available(ctx); err != nil {
		t.Fatalf("FTS5 unavailable: %v", err)
	}
	if err := db.QuickCheck(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, dir := openTemp(t)
	path := db.Path
	if _, err := db.Exec(`INSERT INTO tags (name) VALUES ('keep-me')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db2, err := Open(ctx, path, Options{BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var n int
	_ = db2.QueryRow(`SELECT count(*) FROM tags`).Scan(&n)
	if n != 1 {
		t.Fatalf("data lost on reopen: %d tags", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "backups")); len(entries) != 0 {
		t.Fatalf("no migration was pending, but a safety backup was written")
	}
}

func TestMigrationUpgradePreservesDataAndBacksUp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "h.db")
	backups := filepath.Join(dir, "backups")

	v1 := []Migration{{1, "one", `CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`}}
	v2 := append(append([]Migration{}, v1...), Migration{2, "two", `ALTER TABLE things ADD COLUMN colour TEXT NOT NULL DEFAULT 'blue'`})

	db, err := Open(ctx, path, Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx, v1, backups); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO things (name) VALUES ('lamp')`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx, v2, backups); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var name, colour string
	if err := db.QueryRow(`SELECT name, colour FROM things`).Scan(&name, &colour); err != nil {
		t.Fatal(err)
	}
	if name != "lamp" || colour != "blue" {
		t.Fatalf("after upgrade got %q/%q", name, colour)
	}
	if v, _ := db.SchemaVersion(ctx); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
	files, _ := filepath.Glob(filepath.Join(backups, "holocron-pre-migration-v1-*.db"))
	if len(files) != 1 {
		t.Fatalf("expected one pre-migration backup, found %v", files)
	}

	// A failing migration must roll back completely.
	bad := append(append([]Migration{}, v2...), Migration{3, "bad", `ALTER TABLE things ADD COLUMN size INTEGER; INSERT INTO nowhere VALUES (1);`})
	if err := db.migrate(ctx, bad, ""); err == nil {
		t.Fatal("bad migration succeeded")
	}
	if v, _ := db.SchemaVersion(ctx); v != 2 {
		t.Fatalf("failed migration changed version to %d", v)
	}
	if _, err := db.Exec(`SELECT size FROM things`); err == nil {
		t.Fatal("failed migration left a partial schema change behind")
	}
	_ = db.Close()
}

func TestRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	db, dir := openTemp(t)
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (999, 'future', '2030-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	path := db.Path
	_ = db.Close()
	_, err := Open(ctx, path, Options{BackupDir: filepath.Join(dir, "backups")})
	if err == nil || !strings.Contains(err.Error(), "upgrade Holocron") {
		t.Fatalf("expected newer-schema refusal, got %v", err)
	}
}

func TestRefusesForeignDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "other.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE invoices (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := Open(ctx, path, Options{}); err == nil || !strings.Contains(err.Error(), "not a Holocron archive") {
		t.Fatalf("expected refusal, got %v", err)
	}

	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("this is not sqlite\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, junk, Options{}); err == nil {
		t.Fatal("opened a text file as a database")
	}
}

func TestMustExist(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "nope.db"), Options{MustExist: true})
	if err == nil {
		t.Fatal("expected error for missing database")
	}
}

func TestBackupWhileOpen(t *testing.T) {
	ctx := context.Background()
	db, dir := openTemp(t)
	// Leave data in the WAL (not yet checkpointed) to prove the backup sees it.
	for _, n := range []string{"alpha", "beta", "gamma"} {
		if _, err := db.Exec(`INSERT INTO tags (name) VALUES (?)`, n); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(dir, "out", "backup.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := db.Backup(ctx, dest); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
	b, err := Open(ctx, dest, Options{MustExist: true, NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	_ = b.QueryRow(`SELECT count(*) FROM tags`).Scan(&n)
	if n != 3 {
		t.Fatalf("backup has %d tags, want 3", n)
	}
	if v, _ := b.SchemaVersion(ctx); v != LatestVersion() {
		t.Fatalf("backup schema version %d", v)
	}
}

func TestTxRollsBack(t *testing.T) {
	ctx := context.Background()
	db, _ := openTemp(t)
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO tags (name) VALUES ('ghost')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO tags (name) VALUES ('ghost')`) // unique violation
		return err
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM tags`).Scan(&n)
	if n != 0 {
		t.Fatalf("rollback left %d rows", n)
	}
}

func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	ms := Migrations()
	if len(ms) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range ms {
		if m.Version != i+1 || m.Name == "" || strings.TrimSpace(m.SQL) == "" {
			t.Fatalf("malformed migration %+v", m)
		}
	}
}

func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "live.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tags (name) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "with space", "copy.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, _ := os.ReadFile(dest)

	ro, err := OpenReadOnly(ctx, dest)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	var n int
	if err := ro.QueryRow(`SELECT count(*) FROM tags`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read = %d, %v", n, err)
	}
	if _, err := ro.Exec(`INSERT INTO tags (name) VALUES ('y')`); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
	if err := ro.QuickCheck(ctx); err != nil {
		t.Fatal(err)
	}
	_ = ro.CloseReadOnly()
	after, _ := os.ReadFile(dest)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only open modified the file")
	}
	if _, err := os.Stat(dest + "-wal"); err == nil {
		t.Fatal("read-only open left a WAL file")
	}
}
