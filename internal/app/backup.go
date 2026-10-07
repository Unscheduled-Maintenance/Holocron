package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/devsync"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// DefaultBackupPath returns a timestamped path in the backup directory.
func (a *App) DefaultBackupPath(prefix string) string {
	if prefix == "" {
		prefix = "holocron"
	}
	return filepath.Join(a.Paths.BackupDir, fmt.Sprintf("%s-%s.db", prefix, a.now().Format("20060102-150405")))
}

// Backup writes a verified, consistent copy of the archive. dest may be a
// file path, an existing directory, or empty for the default location.
func (a *App) Backup(ctx context.Context, dest string) (string, ArchiveInfo, error) {
	abs, err := a.backupDest(dest, "")
	if err != nil {
		return "", ArchiveInfo{}, err
	}
	if err := a.DB.Backup(ctx, abs); err != nil {
		return "", ArchiveInfo{}, err
	}
	info, err := InspectArchive(ctx, abs)
	if err != nil {
		return abs, info, fmt.Errorf("backup written to %s but failed verification: %w", abs, err)
	}
	return abs, info, nil
}

// EncryptedBackup is Backup, encrypted with age so that only the chosen
// passphrase, SSH keys or sync key can open it. The copy is verified before
// it is encrypted. The unencrypted copy is made in the backup directory, next
// to the archive, and removed afterwards. Default names end in .db.age.
func (a *App) EncryptedBackup(ctx context.Context, dest string, lock devsync.BackupLock) (string, ArchiveInfo, error) {
	abs, err := a.backupDest(dest, ".age")
	if err != nil {
		return "", ArchiveInfo{}, err
	}
	if _, err := os.Stat(abs); err == nil {
		return "", ArchiveInfo{}, fmt.Errorf("backup destination %s already exists", abs)
	}
	plain := filepath.Join(a.Paths.BackupDir, fmt.Sprintf(".holocron-encrypting-%d.tmp", time.Now().UnixNano()))
	if err := a.DB.Backup(ctx, plain); err != nil {
		return "", ArchiveInfo{}, err
	}
	defer os.Remove(plain)
	info, err := InspectArchive(ctx, plain)
	if err != nil {
		return "", info, fmt.Errorf("the backup failed verification, so nothing was written: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", info, fmt.Errorf("creating backup directory: %w", err)
	}
	if err := a.Sync.EncryptBackup(ctx, plain, abs, lock); err != nil {
		return "", info, err
	}
	info.Path = abs
	if st, err := os.Stat(abs); err == nil {
		info.Size = st.Size()
	}
	return abs, info, nil
}

// backupDest resolves a backup destination: a file path, an existing
// directory, or empty for the default location. suffix is added to default
// names.
func (a *App) backupDest(dest, suffix string) (string, error) {
	dest = config.ExpandHome(strings.TrimSpace(dest))
	if dest == "" {
		dest = a.DefaultBackupPath("") + suffix
	} else if st, err := os.Stat(dest); err == nil && st.IsDir() {
		dest = filepath.Join(dest, filepath.Base(a.DefaultBackupPath(""))+suffix)
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	if samePath(abs, a.Paths.Database) {
		return "", errors.New("the backup destination is the live archive itself; choose another path")
	}
	return abs, nil
}

// DecryptBackup decrypts an encrypted backup into the backup directory, for
// inspecting or restoring. With an empty Unlock it tries the sync keys this
// computer keeps, then sync.key_command. The caller removes the returned file.
func DecryptBackup(ctx context.Context, opts Options, src string, u devsync.Unlock) (string, error) {
	cfg, paths, err := LoadConfig(opts)
	if err != nil {
		return "", err
	}
	keychain := opts.Keychain
	if keychain == nil {
		keychain = devsync.SystemKeychain{}
	}
	s := &devsync.Syncer{Keychain: keychain, KeyCommand: editor.SplitCommand(cfg.Sync.KeyCommand)}
	// The archive says which sync set this computer belongs to.
	if _, err := os.Stat(paths.Database); err == nil {
		if db, err := database.Open(ctx, paths.Database, database.Options{NoMigrate: true, MustExist: true}); err == nil {
			defer db.Close()
			s.Store = journal.NewStore(db)
		}
	}
	if err := os.MkdirAll(paths.BackupDir, 0o700); err != nil {
		return "", err
	}
	plain := filepath.Join(paths.BackupDir, fmt.Sprintf(".holocron-decrypting-%d.tmp", time.Now().UnixNano()))
	if err := s.DecryptBackup(ctx, src, plain, u); err != nil {
		return "", err
	}
	return plain, nil
}

// ArchiveInfo describes an archive file.
type ArchiveInfo struct {
	Path          string
	SchemaVersion int
	Entries       int
	Projects      int
	First, Last   string
	Size          int64
}

// InspectArchive opens a file read-only, checks its integrity and summarises it.
func InspectArchive(ctx context.Context, path string) (ArchiveInfo, error) {
	info := ArchiveInfo{Path: path}
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return info, err
	}
	defer func() { _ = db.CloseReadOnly() }()
	if err := db.QuickCheck(ctx); err != nil {
		return info, err
	}
	if info.SchemaVersion, err = db.SchemaVersion(ctx); err != nil {
		return info, err
	}
	if info.SchemaVersion == 0 {
		return info, fmt.Errorf("%s is empty, not a Holocron archive", path)
	}
	if info.SchemaVersion > database.LatestVersion() {
		return info, fmt.Errorf("%s uses schema version %d, newer than this Holocron supports (%d); upgrade Holocron first", path, info.SchemaVersion, database.LatestVersion())
	}
	var first, last *string
	err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM entries), (SELECT count(*) FROM projects),
		(SELECT min(occurred_at) FROM entries), (SELECT max(occurred_at) FROM entries)`).
		Scan(&info.Entries, &info.Projects, &first, &last)
	if err != nil {
		return info, database.Describe(fmt.Errorf("reading %s: %w", path, err))
	}
	if first != nil {
		info.First, info.Last = *first, *last
	}
	if st, err := os.Stat(path); err == nil {
		info.Size = st.Size()
	}
	return info, nil
}

// RestoreResult describes a completed restore.
type RestoreResult struct {
	Restored     ArchiveInfo
	SafetyBackup string // copy of the archive that was replaced ("" if none existed)
	Database     string
}

// Restore replaces the live archive with the backup at src. The current
// archive is first backed up to the backup directory, the replacement is
// staged next to the live file and moved into place atomically, and the
// result is migrated if it came from an older version of Holocron.
//
// Close every other Holocron process before restoring.
func Restore(ctx context.Context, opts Options, src string) (RestoreResult, error) {
	var res RestoreResult
	_, paths, err := LoadConfig(opts)
	if err != nil {
		return res, err
	}
	res.Database = paths.Database
	src, err = filepath.Abs(config.ExpandHome(src))
	if err != nil {
		return res, err
	}
	if samePath(src, paths.Database) {
		return res, errors.New("the file to restore is the live archive itself")
	}
	if st, err := os.Stat(src + "-wal"); err == nil && st.Size() > 0 {
		return res, fmt.Errorf("%s has an uncheckpointed write-ahead log (%s-wal), so it may be a live archive rather than a backup; open it with Holocron once (`holocron --db %s doctor`) or restore from a file made by `holocron backup`", src, src, src)
	}
	if res.Restored, err = InspectArchive(ctx, src); err != nil {
		return res, fmt.Errorf("cannot restore %s: %w", src, err)
	}

	// Highest entry and project numbers ever issued by the live archive. The
	// restored archive must never hand these out again, or a reference such
	// as "#57" written down after the backup was taken could silently start
	// pointing at a different record.
	issued := map[string]int64{}
	// The same goes for entry numbers (ADR 0007): the plain-number counter and
	// each device's labelled-number counter. Older archives have neither.
	deviceNext := map[string]int64{}
	var plainNext int64
	if _, err := os.Stat(paths.Database); err == nil {
		live, err := database.Open(ctx, paths.Database, database.Options{NoMigrate: true, MustExist: true})
		if err != nil {
			return res, fmt.Errorf("opening the current archive for a safety backup: %w", err)
		}
		if rows, err := live.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence`); err == nil {
			for rows.Next() {
				var name string
				var seq int64
				if rows.Scan(&name, &seq) == nil {
					issued[name] = seq
				}
			}
			rows.Close()
		}
		if rows, err := live.QueryContext(ctx, `SELECT uid, next_num FROM devices`); err == nil {
			for rows.Next() {
				var uid string
				var next int64
				if rows.Scan(&uid, &next) == nil {
					deviceNext[uid] = next
				}
			}
			rows.Close()
		}
		_ = live.QueryRowContext(ctx, `SELECT value FROM sync_meta WHERE key = 'plain_next'`).Scan(&plainNext)
		res.SafetyBackup = filepath.Join(paths.BackupDir, fmt.Sprintf("holocron-pre-restore-%s.db", time.Now().Format("20060102-150405")))
		if err := live.Backup(ctx, res.SafetyBackup); err != nil {
			_ = live.Close()
			return res, fmt.Errorf("refusing to restore without a safety backup of the current archive: %w", err)
		}
		if err := live.Close(); err != nil {
			return res, fmt.Errorf("closing the current archive: %w", err)
		}
	} else if err := os.MkdirAll(filepath.Dir(paths.Database), 0o700); err != nil {
		return res, err
	}

	staged := paths.Database + ".restoring"
	_ = os.Remove(staged)
	if err := copyFile(src, staged); err != nil {
		_ = os.Remove(staged)
		return res, fmt.Errorf("staging the backup: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(paths.Database + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(staged)
			return res, fmt.Errorf("the archive appears to be in use by another Holocron process (%w); close it and retry", err)
		}
	}
	if err := os.Rename(staged, paths.Database); err != nil {
		_ = os.Remove(staged)
		return res, fmt.Errorf("replacing the archive (is another Holocron process running?): %w", err)
	}
	// Open once to apply any migrations the older backup needs.
	db, err := database.Open(ctx, paths.Database, database.Options{BackupDir: paths.BackupDir, MustExist: true})
	if err != nil {
		return res, fmt.Errorf("the backup was restored but could not be opened: %w (the previous archive is at %s)", err, res.SafetyBackup)
	}
	for name, seq := range issued {
		if _, err := db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = ?`, seq, name); err != nil {
			_ = db.Close()
			return res, fmt.Errorf("preserving ID numbering after restore: %w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = ?)`, name, seq, name); err != nil {
			_ = db.Close()
			return res, fmt.Errorf("preserving ID numbering after restore: %w", err)
		}
	}
	if plainNext > 0 {
		if _, err := db.ExecContext(ctx, `UPDATE sync_meta SET value = max(CAST(value AS INTEGER), ?) WHERE key = 'plain_next'`, plainNext); err != nil {
			_ = db.Close()
			return res, fmt.Errorf("preserving entry numbering after restore: %w", err)
		}
	}
	for uid, next := range deviceNext {
		if _, err := db.ExecContext(ctx, `UPDATE devices SET next_num = max(next_num, ?) WHERE uid = ?`, next, uid); err != nil {
			_ = db.Close()
			return res, fmt.Errorf("preserving entry numbering after restore: %w", err)
		}
	}
	return res, db.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	sa, errA := os.Stat(ca)
	sb, errB := os.Stat(cb)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// LocalDate renders a stored UTC timestamp as a calendar date in loc.
func LocalDate(stored string, loc *time.Location) string {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", stored)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, stored)
	}
	if err != nil {
		if len(stored) >= 10 {
			return stored[:10]
		}
		return stored
	}
	return t.In(loc).Format("2006-01-02")
}
