package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// Store provides journal operations over a Holocron database.
type Store struct {
	db  *database.DB
	now func() time.Time
	loc *time.Location
}

// Option configures a Store.
type Option func(*Store)

// WithClock overrides the source of "now" (for tests).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithLocation sets the zone used to record each entry's local offset.
func WithLocation(loc *time.Location) Option { return func(s *Store) { s.loc = loc } }

// NewStore wraps an open database.
func NewStore(db *database.DB, opts ...Option) *Store {
	s := &Store{db: db, now: time.Now, loc: time.Local}
	for _, o := range opts {
		o(s)
	}
	return s
}

// DB exposes the underlying database for maintenance operations.
func (s *Store) DB() *database.DB { return s.db }

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NewEntry describes an entry to create.
type NewEntry struct {
	Body       string
	OccurredAt time.Time // zero means now
	Type       Type
	Project    string // name or alias; empty for none
	// CreateProject creates Project if it does not exist yet.
	CreateProject bool
	Tags          []string
	Marks         []Mark
	Source        *Source
}

// AddEntry creates an entry, its tags and marks, and its search index row
// in a single transaction.
func (s *Store) AddEntry(ctx context.Context, n NewEntry) (Entry, error) {
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.insertEntry(ctx, tx, n, 0, "")
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return s.getByID(ctx, s.db, id)
}

// insertEntry inserts within tx. A non-zero id/uid re-creates a previously
// deleted entry with its original identity (used by undo).
func (s *Store) insertEntry(ctx context.Context, tx *sql.Tx, n NewEntry, id int64, uid string) (int64, error) {
	body, err := NormalizeBody(n.Body)
	if err != nil {
		return 0, err
	}
	if n.Type != TypeNone {
		if _, err := ParseType(string(n.Type)); err != nil {
			return 0, err
		}
	}
	tags, err := NormalizeTags(n.Tags)
	if err != nil {
		return 0, err
	}
	now := s.now()
	at := n.OccurredAt
	if at.IsZero() {
		at = now
	}
	_, offset := at.In(s.loc).Zone()

	var projectID sql.NullInt64
	if strings.TrimSpace(n.Project) != "" {
		p, err := s.resolveProject(ctx, tx, n.Project, n.CreateProject)
		if err != nil {
			return 0, err
		}
		projectID = sql.NullInt64{Int64: p.ID, Valid: true}
	}
	if uid == "" {
		uid = NewUID(now)
	}
	var src struct{ typ, id, url, at sql.NullString }
	if n.Source != nil {
		if n.Source.Type == "" || n.Source.ID == "" {
			return 0, fmt.Errorf("%w: imported entries need a source type and ID", ErrInvalid)
		}
		imported := n.Source.ImportedAt
		if imported.IsZero() {
			imported = now
		}
		src.typ = sql.NullString{String: n.Source.Type, Valid: true}
		src.id = sql.NullString{String: n.Source.ID, Valid: true}
		src.url = sql.NullString{String: n.Source.URL, Valid: n.Source.URL != ""}
		src.at = sql.NullString{String: formatTime(imported), Valid: true}
	}
	var idArg any
	if id > 0 {
		idArg = id
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO entries (id, uid, occurred_at, utc_offset, body, type, project_id,
		                     created_at, updated_at, source_type, source_id, source_url, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idArg, uid, formatTime(at), offset, body, nullString(string(n.Type)), projectID,
		formatTime(now), formatTime(now), src.typ, src.id, src.url, src.at)
	if err != nil {
		if isUniqueViolation(err) && n.Source != nil {
			return 0, fmt.Errorf("%w: %s %s has already been imported", ErrConflict, n.Source.Type, n.Source.ID)
		}
		return 0, database.Describe(fmt.Errorf("saving entry: %w", err))
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setTags(ctx, tx, newID, tags); err != nil {
		return 0, err
	}
	if err := s.setMarks(ctx, tx, newID, n.Marks); err != nil {
		return 0, err
	}
	if err := reindex(ctx, tx, newID); err != nil {
		return 0, err
	}
	return newID, nil
}

// Get loads an entry by reference ("42", "#42" or a UID).
func (s *Store) Get(ctx context.Context, ref string) (Entry, error) {
	id, uid, err := ParseRef(ref)
	if err != nil {
		return Entry{}, err
	}
	if uid != "" {
		err := s.db.QueryRowContext(ctx, `SELECT id FROM entries WHERE uid = ?`, uid).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, fmt.Errorf("%w: no entry with UID %s", ErrNotFound, uid)
		}
		if err != nil {
			return Entry{}, database.Describe(err)
		}
	}
	return s.getByID(ctx, s.db, id)
}

// GetByID loads an entry by numeric ID.
func (s *Store) GetByID(ctx context.Context, id int64) (Entry, error) {
	return s.getByID(ctx, s.db, id)
}

func (s *Store) getByID(ctx context.Context, q queryer, id int64) (Entry, error) {
	rows, err := q.QueryContext(ctx, selectEntries+fromEntries+` WHERE e.id = ?`, id)
	if err != nil {
		return Entry{}, database.Describe(err)
	}
	list, err := scanEntries(rows, false)
	if err != nil {
		return Entry{}, err
	}
	if len(list) == 0 {
		return Entry{}, fmt.Errorf("%w: no entry #%d", ErrNotFound, id)
	}
	if err := attachLabels(ctx, q, list); err != nil {
		return Entry{}, err
	}
	return list[0], nil
}

// Patch describes changes to an entry. Nil fields are left unchanged.
type Patch struct {
	Body       *string
	OccurredAt *time.Time
	Type       *Type
	// Project sets the project by name or alias; a pointer to "" clears it.
	Project       *string
	CreateProject bool
	// Tags replaces all tags; AddTags/RemoveTags adjust them.
	Tags       *[]string
	AddTags    []string
	RemoveTags []string
	// Marks replaces all marks; AddMarks/RemoveMarks adjust them.
	Marks       *[]Mark
	AddMarks    []Mark
	RemoveMarks []Mark
	// Resolved marks a problem or follow-up as resolved (true) or reopens it.
	Resolved *bool
}

// IsEmpty reports whether the patch changes nothing.
func (p Patch) IsEmpty() bool {
	return p.Body == nil && p.OccurredAt == nil && p.Type == nil && p.Project == nil &&
		p.Tags == nil && len(p.AddTags) == 0 && len(p.RemoveTags) == 0 &&
		p.Marks == nil && len(p.AddMarks) == 0 && len(p.RemoveMarks) == 0 && p.Resolved == nil
}

// Update applies a patch atomically and returns the updated entry.
func (s *Store) Update(ctx context.Context, id int64, p Patch) (Entry, error) {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := s.getByID(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now()
		sets := []string{"updated_at = ?"}
		args := []any{formatTime(now)}

		if p.Body != nil {
			body, err := NormalizeBody(*p.Body)
			if err != nil {
				return err
			}
			sets = append(sets, "body = ?")
			args = append(args, body)
		}
		if p.OccurredAt != nil {
			_, off := p.OccurredAt.In(s.loc).Zone()
			sets = append(sets, "occurred_at = ?", "utc_offset = ?")
			args = append(args, formatTime(*p.OccurredAt), off)
		}
		if p.Type != nil {
			if *p.Type != TypeNone {
				if _, err := ParseType(string(*p.Type)); err != nil {
					return err
				}
			}
			sets = append(sets, "type = ?")
			args = append(args, nullString(string(*p.Type)))
		}
		if p.Project != nil {
			var pid sql.NullInt64
			if strings.TrimSpace(*p.Project) != "" {
				proj, err := s.resolveProject(ctx, tx, *p.Project, p.CreateProject)
				if err != nil {
					return err
				}
				pid = sql.NullInt64{Int64: proj.ID, Valid: true}
			}
			sets = append(sets, "project_id = ?")
			args = append(args, pid)
		}
		if p.Resolved != nil {
			var v sql.NullString
			if *p.Resolved {
				v = sql.NullString{String: formatTime(now), Valid: true}
				if cur.ResolvedAt != nil {
					v.String = formatTime(*cur.ResolvedAt)
				}
			}
			sets = append(sets, "resolved_at = ?")
			args = append(args, v)
		}
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, `UPDATE entries SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return database.Describe(fmt.Errorf("updating entry #%d: %w", id, err))
		}

		if p.Tags != nil || len(p.AddTags) > 0 || len(p.RemoveTags) > 0 {
			tags := cur.Tags
			if p.Tags != nil {
				tags = *p.Tags
			}
			tags = append(append([]string{}, tags...), p.AddTags...)
			norm, err := NormalizeTags(tags)
			if err != nil {
				return err
			}
			remove, err := NormalizeTags(p.RemoveTags)
			if err != nil {
				return err
			}
			if err := setTags(ctx, tx, id, without(norm, remove)); err != nil {
				return err
			}
		}
		if p.Marks != nil || len(p.AddMarks) > 0 || len(p.RemoveMarks) > 0 {
			marks := cur.Marks
			if p.Marks != nil {
				marks = *p.Marks
			}
			marks = append(append([]Mark{}, marks...), p.AddMarks...)
			if err := s.setMarks(ctx, tx, id, without(uniqueSorted(marks), p.RemoveMarks)); err != nil {
				return err
			}
		}
		return reindex(ctx, tx, id)
	})
	if err != nil {
		return Entry{}, err
	}
	return s.getByID(ctx, s.db, id)
}

// Delete permanently removes an entry and everything attached to it.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE id = ?`, id)
		if err != nil {
			return database.Describe(fmt.Errorf("deleting entry #%d: %w", id, err))
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no entry #%d", ErrNotFound, id)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM entries_fts WHERE rowid = ?`, id); err != nil {
			return database.Describe(err)
		}
		return pruneTags(ctx, tx)
	})
}

// Restore re-creates a deleted entry with its original ID and UID, which is
// how "undo delete" works. It fails if the ID is somehow in use.
func (s *Store) Restore(ctx context.Context, e Entry) (Entry, error) {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		n := NewEntry{Body: e.Body, OccurredAt: e.OccurredAt, Type: e.Type, Project: e.Project,
			CreateProject: true, Tags: e.Tags, Marks: e.Marks, Source: e.Source}
		if _, err := s.insertEntry(ctx, tx, n, e.ID, e.UID); err != nil {
			return err
		}
		var resolved sql.NullString
		if e.ResolvedAt != nil {
			resolved = sql.NullString{String: formatTime(*e.ResolvedAt), Valid: true}
		}
		_, err := tx.ExecContext(ctx, `UPDATE entries SET created_at = ?, utc_offset = ?, resolved_at = ? WHERE id = ?`,
			formatTime(e.CreatedAt), e.UTCOffset, resolved, e.ID)
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return s.getByID(ctx, s.db, e.ID)
}

func setTags(ctx context.Context, tx *sql.Tx, entryID int64, tags []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM entry_tags WHERE entry_id = ?`, entryID); err != nil {
		return database.Describe(err)
	}
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, t); err != nil {
			return database.Describe(fmt.Errorf("saving tag %q: %w", t, err))
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_tags (entry_id, tag_id) SELECT ?, id FROM tags WHERE name = ?`, entryID, t); err != nil {
			return database.Describe(fmt.Errorf("tagging entry: %w", err))
		}
	}
	return pruneTags(ctx, tx)
}

// pruneTags removes tags no entry uses any more, so `tag list` stays honest.
func pruneTags(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM entry_tags)`)
	return database.Describe(err)
}

func (s *Store) setMarks(ctx context.Context, tx *sql.Tx, entryID int64, marks []Mark) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM entry_marks WHERE entry_id = ?`, entryID); err != nil {
		return database.Describe(err)
	}
	for _, m := range uniqueSorted(marks) {
		if _, err := ParseMark(string(m)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entry_marks (entry_id, mark, created_at) VALUES (?, ?, ?)`,
			entryID, string(m), formatTime(s.now())); err != nil {
			return database.Describe(err)
		}
	}
	return nil
}

// reindex rebuilds the full-text row for one entry from its current state.
func reindex(ctx context.Context, q queryer, id int64) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM entries_fts WHERE rowid = ?`, id); err != nil {
		return database.Describe(fmt.Errorf("updating search index: %w", err))
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO entries_fts (rowid, body, project, tags)
		SELECT e.id, e.body,
		       coalesce(p.name, '') || ' ' || coalesce((SELECT group_concat(alias, ' ') FROM project_aliases WHERE project_id = p.id), ''),
		       coalesce((SELECT group_concat(t.name, ' ') FROM entry_tags et JOIN tags t ON t.id = et.tag_id WHERE et.entry_id = e.id), '')
		FROM entries e LEFT JOIN projects p ON p.id = e.project_id
		WHERE e.id = ?`, id)
	if err != nil {
		return database.Describe(fmt.Errorf("updating search index: %w", err))
	}
	return nil
}

// RebuildIndex regenerates the entire full-text index from the entries
// table. Used by `holocron doctor --rebuild-index`.
func (s *Store) RebuildIndex(ctx context.Context) (int, error) {
	var n int
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM entries_fts`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM entries`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, database.Describe(err)
}

// CheckIndex verifies that every entry has exactly one search index row.
func (s *Store) CheckIndex(ctx context.Context) error {
	var entries, indexed, orphans int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM entries),
		(SELECT count(*) FROM entries_fts WHERE rowid IN (SELECT id FROM entries)),
		(SELECT count(*) FROM entries_fts WHERE rowid NOT IN (SELECT id FROM entries))`).Scan(&entries, &indexed, &orphans)
	if err != nil {
		return database.Describe(err)
	}
	if entries != indexed || orphans != 0 {
		return fmt.Errorf("search index is out of date (%d entries, %d indexed, %d stale rows); run `holocron doctor --rebuild-index`", entries, indexed, orphans)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO entries_fts (entries_fts) VALUES ('integrity-check')`); err != nil {
		return fmt.Errorf("search index integrity check failed: %w; run `holocron doctor --rebuild-index`", err)
	}
	return nil
}

func without[T ~string](in []T, remove []T) []T {
	if len(remove) == 0 {
		return in
	}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[strings.ToLower(string(r))] = true
	}
	out := in[:0:0]
	for _, v := range in {
		if !drop[strings.ToLower(string(v))] {
			out = append(out, v)
		}
	}
	return out
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// AddEntries creates several entries in a single transaction: either all are
// saved or none are. It returns their IDs in order.
func (s *Store) AddEntries(ctx context.Context, items []NewEntry) ([]int64, error) {
	ids := make([]int64, 0, len(items))
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, n := range items {
			id, err := s.insertEntry(ctx, tx, n, 0, "")
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}
