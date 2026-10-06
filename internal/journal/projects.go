package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// Project is lightweight organisational context for entries.
type Project struct {
	ID          int64
	UID         string
	Name        string
	Description string
	Aliases     []string
	Paths       []string // local repository paths
	URLs        []string
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Populated by ListProjects.
	EntryCount int
	LastEntry  *time.Time
}

// Archived reports whether the project is archived.
func (p Project) Archived() bool { return p.ArchivedAt != nil }

// NewProject describes a project to create.
type NewProject struct {
	Name        string
	Description string
	Aliases     []string
	Paths       []string
	URLs        []string
}

// CreateProject creates a project.
func (s *Store) CreateProject(ctx context.Context, n NewProject) (Project, error) {
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		p, err := s.insertProject(ctx, tx, n.Name)
		if err != nil {
			return err
		}
		id = p.ID
		if n.Description != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET description = ? WHERE id = ?`, strings.TrimSpace(n.Description), id); err != nil {
				return database.Describe(err)
			}
		}
		for _, a := range n.Aliases {
			if err := addAlias(ctx, tx, id, a); err != nil {
				return err
			}
		}
		for _, v := range n.Paths {
			if err := addLink(ctx, tx, id, "path", v); err != nil {
				return err
			}
		}
		for _, v := range n.URLs {
			if err := addLink(ctx, tx, id, "url", v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return s.projectByID(ctx, s.db, id)
}

func (s *Store) insertProject(ctx context.Context, q queryer, name string) (Project, error) {
	n, err := NormalizeProjectName(name)
	if err != nil {
		return Project{}, err
	}
	if _, err := s.lookupProject(ctx, q, n); err == nil {
		return Project{}, fmt.Errorf("%w: a project or alias named %q already exists", ErrConflict, n)
	}
	now := formatTime(s.now())
	res, err := q.ExecContext(ctx, `INSERT INTO projects (uid, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		NewUID(s.now()), n, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, fmt.Errorf("%w: project %q already exists", ErrConflict, n)
		}
		return Project{}, database.Describe(fmt.Errorf("creating project %q: %w", n, err))
	}
	id, _ := res.LastInsertId()
	clock, err := s.tick(ctx, q)
	if err != nil {
		return Project{}, err
	}
	if err := touchProject(ctx, q, id, clock, ProjectFields...); err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: n}, nil
}

// resolveProject finds a project by name or alias, optionally creating it.
func (s *Store) resolveProject(ctx context.Context, q queryer, nameOrAlias string, create bool) (Project, error) {
	p, err := s.lookupProject(ctx, q, nameOrAlias)
	if err == nil || !errors.Is(err, ErrNotFound) || !create {
		return p, err
	}
	return s.insertProject(ctx, q, nameOrAlias)
}

func (s *Store) lookupProject(ctx context.Context, q queryer, nameOrAlias string) (Project, error) {
	n := strings.TrimPrefix(strings.Join(strings.Fields(nameOrAlias), " "), "+")
	var id int64
	err := q.QueryRowContext(ctx, `
		SELECT id FROM projects WHERE name = ?
		UNION ALL
		SELECT project_id FROM project_aliases WHERE alias = ?
		LIMIT 1`, n, n).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: no project or alias named %q (see `holocron project list`)", ErrNotFound, n)
	}
	if err != nil {
		return Project{}, database.Describe(err)
	}
	return s.projectByID(ctx, q, id)
}

// Project finds a project by name or alias.
func (s *Store) Project(ctx context.Context, nameOrAlias string) (Project, error) {
	return s.lookupProject(ctx, s.db, nameOrAlias)
}

func (s *Store) projectByID(ctx context.Context, q queryer, id int64) (Project, error) {
	var p Project
	var archived sql.NullString
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id, uid, name, description, archived_at, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.UID, &p.Name, &p.Description, &archived, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: no project with ID %d", ErrNotFound, id)
	}
	if err != nil {
		return Project{}, database.Describe(err)
	}
	p.CreatedAt, _ = parseTime(created)
	p.UpdatedAt, _ = parseTime(updated)
	if archived.Valid {
		t, _ := parseTime(archived.String)
		p.ArchivedAt = &t
	}
	if err := s.loadProjectDetails(ctx, q, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Store) loadProjectDetails(ctx context.Context, q queryer, p *Project) error {
	rows, err := q.QueryContext(ctx, `SELECT alias FROM project_aliases WHERE project_id = ? ORDER BY alias`, p.ID)
	if err != nil {
		return database.Describe(err)
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return err
		}
		p.Aliases = append(p.Aliases, a)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT kind, value FROM project_links WHERE project_id = ? ORDER BY kind, value`, p.ID)
	if err != nil {
		return database.Describe(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, v string
		if err := rows.Scan(&kind, &v); err != nil {
			return err
		}
		if kind == "path" {
			p.Paths = append(p.Paths, v)
		} else {
			p.URLs = append(p.URLs, v)
		}
	}
	return rows.Err()
}

// ListProjects returns projects with entry counts, most recently active first.
func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]Project, error) {
	out, err := s.loadProjects(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Archived() != b.Archived() {
			return !a.Archived()
		}
		switch {
		case a.LastEntry != nil && b.LastEntry != nil:
			return a.LastEntry.After(*b.LastEntry)
		case a.LastEntry != nil || b.LastEntry != nil:
			return a.LastEntry != nil
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// ProjectPatch changes a project. Nil fields are unchanged.
type ProjectPatch struct {
	Name          *string
	Description   *string
	AddAliases    []string
	RemoveAliases []string
	AddPaths      []string
	RemovePaths   []string
	AddURLs       []string
	RemoveURLs    []string
	Archived      *bool
}

// UpdateProject applies a patch atomically.
func (s *Store) UpdateProject(ctx context.Context, nameOrAlias string, p ProjectPatch) (Project, error) {
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := s.lookupProject(ctx, tx, nameOrAlias)
		if err != nil {
			return err
		}
		id = cur.ID
		now := formatTime(s.now())
		renamed := false
		var fields []string
		if p.Name != nil {
			n, err := NormalizeProjectName(*p.Name)
			if err != nil {
				return err
			}
			if other, err := s.lookupProject(ctx, tx, n); err == nil && other.ID != id {
				return fmt.Errorf("%w: a project or alias named %q already exists", ErrConflict, n)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`, n, now, id); err != nil {
				return database.Describe(err)
			}
			fields = append(fields, FieldName)
			renamed = true
		}
		if p.Description != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET description = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(*p.Description), now, id); err != nil {
				return database.Describe(err)
			}
			fields = append(fields, FieldDescription)
		}
		if p.Archived != nil {
			var v sql.NullString
			if *p.Archived {
				v = sql.NullString{String: now, Valid: true}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET archived_at = ?, updated_at = ? WHERE id = ?`, v, now, id); err != nil {
				return database.Describe(err)
			}
			fields = append(fields, FieldArchived)
		}
		for _, a := range p.RemoveAliases {
			if _, err := tx.ExecContext(ctx, `DELETE FROM project_aliases WHERE project_id = ? AND alias = ?`, id, strings.TrimSpace(a)); err != nil {
				return database.Describe(err)
			}
			renamed = true
		}
		for _, a := range p.AddAliases {
			if err := addAlias(ctx, tx, id, a); err != nil {
				return err
			}
			renamed = true
		}
		for _, v := range p.RemovePaths {
			if _, err := tx.ExecContext(ctx, `DELETE FROM project_links WHERE project_id = ? AND kind = 'path' AND value = ?`, id, cleanPath(v)); err != nil {
				return database.Describe(err)
			}
		}
		for _, v := range p.AddPaths {
			if err := addLink(ctx, tx, id, "path", v); err != nil {
				return err
			}
		}
		for _, v := range p.RemoveURLs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM project_links WHERE project_id = ? AND kind = 'url' AND value = ?`, id, strings.TrimSpace(v)); err != nil {
				return database.Describe(err)
			}
		}
		for _, v := range p.AddURLs {
			if err := addLink(ctx, tx, id, "url", v); err != nil {
				return err
			}
		}
		if len(p.AddAliases)+len(p.RemoveAliases) > 0 {
			fields = append(fields, FieldAliases)
		}
		if len(p.AddURLs)+len(p.RemoveURLs) > 0 {
			fields = append(fields, FieldURLs)
		}
		if len(fields) > 0 {
			clock, err := s.tick(ctx, tx)
			if err != nil {
				return err
			}
			if err := touchProject(ctx, tx, id, clock, fields...); err != nil {
				return err
			}
		}
		if renamed {
			// Project names and aliases are searchable; refresh affected rows.
			return reindexProject(ctx, tx, id)
		}
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return s.projectByID(ctx, s.db, id)
}

// DeleteProject removes a project. Its entries are kept and become
// unassigned (ON DELETE SET NULL).
func (s *Store) DeleteProject(ctx context.Context, nameOrAlias string) (int, error) {
	var affected int
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		p, err := s.lookupProject(ctx, tx, nameOrAlias)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM entries WHERE project_id = ?`, p.ID)
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
		clock, err := s.tick(ctx, tx)
		if err != nil {
			return err
		}
		if err := bury(ctx, tx, "project", p.UID, clock); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, p.ID); err != nil {
			return database.Describe(err)
		}
		for _, id := range ids {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
		}
		affected = len(ids)
		return nil
	})
	return affected, err
}

// ProjectForPath returns the project whose registered repository path
// contains dir, preferring the most specific match.
func (s *Store) ProjectForPath(ctx context.Context, dir string) (Project, bool, error) {
	dir = canonicalPath(dir)
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, value FROM project_links WHERE kind = 'path'`)
	if err != nil {
		return Project{}, false, database.Describe(err)
	}
	var bestID int64
	bestLen := -1
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return Project{}, false, err
		}
		if root := canonicalPath(v); pathWithin(dir, root) && len(root) > bestLen {
			bestID, bestLen = id, len(root)
		}
	}
	rows.Close()
	if bestLen < 0 {
		return Project{}, false, nil
	}
	p, err := s.projectByID(ctx, s.db, bestID)
	return p, err == nil, err
}

func pathWithin(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func reindexProject(ctx context.Context, tx *sql.Tx, projectID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM entries WHERE project_id = ?`, projectID)
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
	for _, id := range ids {
		if err := reindex(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func addAlias(ctx context.Context, tx *sql.Tx, projectID int64, alias string) error {
	a, err := NormalizeProjectName(alias)
	if err != nil {
		return fmt.Errorf("alias: %w", err)
	}
	var clash int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ? AND id <> ?`, a, projectID).Scan(&clash)
	if err == nil {
		return fmt.Errorf("%w: alias %q is already the name of another project", ErrConflict, a)
	}
	var owner int64
	err = tx.QueryRowContext(ctx, `SELECT project_id FROM project_aliases WHERE alias = ?`, a).Scan(&owner)
	if err == nil {
		if owner == projectID {
			return nil
		}
		return fmt.Errorf("%w: alias %q already belongs to another project", ErrConflict, a)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_aliases (alias, project_id) VALUES (?, ?)`, a, projectID); err != nil {
		return database.Describe(err)
	}
	return nil
}

func addLink(ctx context.Context, tx *sql.Tx, projectID int64, kind, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	if kind == "path" {
		v = cleanPath(v)
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_links (project_id, kind, value) VALUES (?, ?, ?)`, projectID, kind, v)
	return database.Describe(err)
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// loadProjects reads every project with its entry statistics, aliases and
// links in three queries. Counts and latest dates come from per-project
// lookups on the entries(project_id, occurred_at) index rather than
// grouping the whole entries table.
func (s *Store) loadProjects(ctx context.Context, includeArchived bool) ([]Project, error) {
	query := `
		SELECT p.id, p.uid, p.name, p.description, p.archived_at, p.created_at, p.updated_at,
		       (SELECT count(*) FROM entries e WHERE e.project_id = p.id),
		       (SELECT max(e.occurred_at) FROM entries e WHERE e.project_id = p.id)
		FROM projects p`
	if !includeArchived {
		query += ` WHERE p.archived_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, database.Describe(err)
	}
	var out []Project
	index := map[int64]int{}
	for rows.Next() {
		var p Project
		var archived, last sql.NullString
		var created, updated string
		if err := rows.Scan(&p.ID, &p.UID, &p.Name, &p.Description, &archived, &created, &updated, &p.EntryCount, &last); err != nil {
			rows.Close()
			return nil, database.Describe(err)
		}
		p.CreatedAt, _ = parseTime(created)
		p.UpdatedAt, _ = parseTime(updated)
		if archived.Valid {
			t, _ := parseTime(archived.String)
			p.ArchivedAt = &t
		}
		if last.Valid {
			t, _ := parseTime(last.String)
			p.LastEntry = &t
		}
		index[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, database.Describe(err)
	}
	if len(out) == 0 {
		return out, nil
	}

	rows, err = s.db.QueryContext(ctx, `SELECT project_id, alias FROM project_aliases ORDER BY alias`)
	if err != nil {
		return nil, database.Describe(err)
	}
	for rows.Next() {
		var id int64
		var alias string
		if err := rows.Scan(&id, &alias); err != nil {
			rows.Close()
			return nil, database.Describe(err)
		}
		if i, ok := index[id]; ok {
			out[i].Aliases = append(out[i].Aliases, alias)
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT project_id, kind, value FROM project_links ORDER BY kind, value`)
	if err != nil {
		return nil, database.Describe(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, value string
		if err := rows.Scan(&id, &kind, &value); err != nil {
			return nil, database.Describe(err)
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		if kind == "path" {
			out[i].Paths = append(out[i].Paths, value)
		} else {
			out[i].URLs = append(out[i].URLs, value)
		}
	}
	return out, database.Describe(rows.Err())
}

// canonicalPath returns the absolute, symlink-resolved form of p so that two
// spellings of one directory compare equal: on macOS /var is a symlink to
// /private/var, and on Windows a directory may be reached through an 8.3
// short name (C:\Users\RUNNER~1) or with different letter case. Git always
// reports the resolved form, so repository paths must be compared this way.
// When p does not exist, its cleaned absolute form is used.
func canonicalPath(p string) string {
	p = cleanPath(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
