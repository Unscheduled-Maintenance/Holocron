package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// TagCount is a tag with the number of entries using it.
type TagCount struct {
	Name  string
	Count int
}

// ListTags returns every tag in use, most used first.
func (s *Store) ListTags(ctx context.Context) ([]TagCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.name, count(*)
		FROM tags t
		JOIN entry_tags et ON et.tag_id = t.id
		GROUP BY t.id
		ORDER BY count(*) DESC, t.name`)
	if err != nil {
		return nil, database.Describe(err)
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// RenameTag renames a tag everywhere, merging into the target if it exists.
// It returns the number of entries affected.
func (s *Store) RenameTag(ctx context.Context, from, to string) (int, error) {
	f, err := NormalizeTag(from)
	if err != nil {
		return 0, err
	}
	t, err := NormalizeTag(to)
	if err != nil {
		return 0, err
	}
	var affected int
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var fromID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, f).Scan(&fromID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no tag %q", ErrNotFound, f)
			}
			return database.Describe(err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT entry_id FROM entry_tags WHERE tag_id = ?`, fromID)
		if err != nil {
			return database.Describe(err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, t); err != nil {
			return database.Describe(err)
		}
		if f == t {
			// Same tag (perhaps only a case change); nothing to merge.
			return nil
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_tags (entry_id, tag_id) SELECT ?, id FROM tags WHERE name = ?`, id, t); err != nil {
				return database.Describe(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, fromID); err != nil {
			return database.Describe(err)
		}
		clock, err := s.tick(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
			if err := touchEntry(ctx, tx, id, clock, FieldTags); err != nil {
				return err
			}
		}
		affected = len(ids)
		return nil
	})
	return affected, err
}

// ImportedIDs returns which of the given source IDs are already in the
// archive for sourceType.
func (s *Store) ImportedIDs(ctx context.Context, sourceType string, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		args := []any{sourceType}
		ph := make([]byte, 0, 2*(end-start))
		for i, id := range ids[start:end] {
			if i > 0 {
				ph = append(ph, ',')
			}
			ph = append(ph, '?')
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT source_id, id FROM entries WHERE source_type = ? AND source_id IN (`+string(ph)+`)`, args...)
		if err != nil {
			return nil, database.Describe(err)
		}
		for rows.Next() {
			var sid string
			var id int64
			if err := rows.Scan(&sid, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[sid] = id
		}
		rows.Close()
	}
	return out, nil
}

// ImportEntries adds several imported entries in one transaction. Entries
// whose source is already present are skipped, never duplicated. It returns
// the created entries.
func (s *Store) ImportEntries(ctx context.Context, items []NewEntry) ([]Entry, int, error) {
	var ids []int64
	skipped := 0
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, n := range items {
			if n.Source == nil {
				return fmt.Errorf("%w: imported entries need a source", ErrInvalid)
			}
			var existing int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM entries WHERE source_type = ? AND source_id = ?`, n.Source.Type, n.Source.ID).Scan(&existing)
			if err == nil {
				skipped++
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return database.Describe(err)
			}
			id, err := s.insertEntry(ctx, tx, n, nil)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		e, err := s.getByID(ctx, s.db, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, skipped, nil
}

// Stats summarises the archive for diagnostics.
type Stats struct {
	Entries  int
	Projects int
	Tags     int
	First    string
	Last     string
}

// Stats returns archive totals.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	var first, last sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM entries), (SELECT count(*) FROM projects), (SELECT count(*) FROM tags),
		(SELECT min(occurred_at) FROM entries), (SELECT max(occurred_at) FROM entries)`).
		Scan(&st.Entries, &st.Projects, &st.Tags, &first, &last)
	st.First, st.Last = first.String, last.String
	return st, database.Describe(err)
}
