package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenReadOnly opens an existing archive without modifying it in any way:
// no migrations, no journal-mode change, no -wal file. It is used to
// inspect backups before restoring them and by diagnostics.
func OpenReadOnly(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s does not exist", path)
		}
		return nil, fmt.Errorf("cannot access %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("sqlite", readOnlyURI(abs))
	if err != nil {
		return nil, err
	}
	db := &DB{DB: sqldb, Path: abs}
	if err := db.checkIsHolocron(ctx, true); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

func readOnlyURI(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths become file:///C:/...
	}
	u := url.URL{Scheme: "file", Path: p}
	// immutable=1 is not used: a live archive may still be written by
	// another process, so SQLite must keep honouring locks.
	return u.String() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
}

// CloseReadOnly closes a database opened with OpenReadOnly. Unlike Close it
// never attempts a checkpoint.
func (db *DB) CloseReadOnly() error { return db.DB.Close() }
