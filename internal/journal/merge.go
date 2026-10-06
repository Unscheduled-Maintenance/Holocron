package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// Records are the portable form of an archive's contents, used by sync and
// by JSON import. Everything refers to other records by UID, never by a
// local row ID. See docs/adr/0007-multi-device-sync.md.
type Records struct {
	Devices    []DeviceRecord    `json:"devices,omitempty"`
	Projects   []ProjectRecord   `json:"projects,omitempty"`
	Entries    []EntryRecord     `json:"entries,omitempty"`
	Tombstones []TombstoneRecord `json:"tombstones,omitempty"`
	Reports    []ReportLogRecord `json:"reports,omitempty"`
}

// IsEmpty reports whether there is nothing to apply.
func (r Records) IsEmpty() bool {
	return len(r.Devices)+len(r.Projects)+len(r.Entries)+len(r.Tombstones)+len(r.Reports) == 0
}

// DeviceRecord identifies a device and its label.
type DeviceRecord struct {
	UID   string `json:"uid"`
	Label string `json:"label"`
	Name  string `json:"name,omitempty"`
}

// ProjectRecord is a project with the clock of each field. Repository
// paths are local to each computer and are not part of it.
type ProjectRecord struct {
	UID         string     `json:"uid"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Aliases     []string   `json:"aliases,omitempty"`
	URLs        []string   `json:"urls,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Clocks      Clocks     `json:"clocks"`
}

// EntryRecord is an entry with the clock of each field.
type EntryRecord struct {
	UID        string     `json:"uid"`
	Num        int64      `json:"num"`
	Label      string     `json:"label,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
	UTCOffset  int        `json:"utc_offset"`
	Body       string     `json:"body"`
	Type       Type       `json:"type,omitempty"`
	Project    string     `json:"project,omitempty"` // project UID
	Tags       []string   `json:"tags,omitempty"`
	Marks      []Mark     `json:"marks,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy string     `json:"resolved_by,omitempty"` // entry UID
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Source     *Source    `json:"source,omitempty"`
	Clocks     Clocks     `json:"clocks"`
}

// Ref returns the entry's reference: #42 or #12a.
func (r EntryRecord) Ref() string { return FormatRef(r.Num, r.Label) }

// TombstoneRecord says a record was deleted.
type TombstoneRecord struct {
	UID   string `json:"uid"`
	Kind  string `json:"kind"`
	Clock HLC    `json:"clock"`
}

// ReportLogRecord is a report recorded as sent.
type ReportLogRecord struct {
	UID        string     `json:"uid"`
	Kind       string     `json:"kind"`
	Start      *time.Time `json:"range_start,omitempty"`
	End        *time.Time `json:"range_end,omitempty"`
	RecordedAt time.Time  `json:"recorded_at"`
}

// MarshalText encodes a clock for JSON.
func (h HLC) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText decodes a clock from JSON.
func (h *HLC) UnmarshalText(b []byte) error {
	v, err := ParseHLC(string(b))
	if err != nil {
		return err
	}
	*h = v
	return nil
}

func (c Clocks) encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// ReadRecords returns the archive as records. With changedOnly, only rows
// changed here since MarkPublished are included (devices always are).
func (s *Store) ReadRecords(ctx context.Context, changedOnly bool) (Records, error) {
	var out Records
	dirty := ""
	if changedOnly {
		dirty = " WHERE dirty = 1"
	}
	devs, err := s.Devices(ctx)
	if err != nil {
		return out, err
	}
	for _, d := range devs {
		if d.Label != "" {
			out.Devices = append(out.Devices, DeviceRecord{UID: d.UID, Label: d.Label, Name: d.Name})
		}
	}

	// Projects.
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, name, description, archived_at, created_at, updated_at, clocks FROM projects`+dirty)
	if err != nil {
		return out, database.Describe(err)
	}
	projIDs := map[int64]int{}
	for rows.Next() {
		var p ProjectRecord
		var id int64
		var archived sql.NullString
		var created, updated, clocks string
		if err := rows.Scan(&id, &p.UID, &p.Name, &p.Description, &archived, &created, &updated, &clocks); err != nil {
			rows.Close()
			return out, database.Describe(err)
		}
		p.CreatedAt, _ = parseTime(created)
		p.UpdatedAt, _ = parseTime(updated)
		if archived.Valid {
			t, _ := parseTime(archived.String)
			p.ArchivedAt = &t
		}
		p.Clocks = decodeClocks(clocks)
		projIDs[id] = len(out.Projects)
		out.Projects = append(out.Projects, p)
	}
	rows.Close()
	if err := forEachRow(ctx, s.db, `SELECT project_id, alias FROM project_aliases ORDER BY alias`, func(id int64, v string) {
		if i, ok := projIDs[id]; ok {
			out.Projects[i].Aliases = append(out.Projects[i].Aliases, v)
		}
	}); err != nil {
		return out, err
	}
	if err := forEachRow(ctx, s.db, `SELECT project_id, value FROM project_links WHERE kind = 'url' ORDER BY value`, func(id int64, v string) {
		if i, ok := projIDs[id]; ok {
			out.Projects[i].URLs = append(out.Projects[i].URLs, v)
		}
	}); err != nil {
		return out, err
	}

	// Entries.
	rows, err = s.db.QueryContext(ctx, `
		SELECT e.id, e.uid, e.num, coalesce(nd.label, ''), e.occurred_at, e.utc_offset, e.body, coalesce(e.type, ''),
		       coalesce(p.uid, ''), e.resolved_at, coalesce(rb.uid, ''), e.created_at, e.updated_at,
		       e.source_type, e.source_id, e.source_url, e.imported_at, e.clocks
		FROM entries e
		LEFT JOIN devices nd ON nd.id = e.num_device
		LEFT JOIN projects p ON p.id = e.project_id
		LEFT JOIN entries rb ON rb.id = e.resolved_by`+strings.ReplaceAll(dirty, "dirty", "e.dirty")+`
		ORDER BY e.id`)
	if err != nil {
		return out, database.Describe(err)
	}
	entryIDs := map[int64]int{}
	for rows.Next() {
		var r EntryRecord
		var id int64
		var typ, occurred, created, updated, clocks string
		var resolved, srcType, srcID, srcURL, imported sql.NullString
		if err := rows.Scan(&id, &r.UID, &r.Num, &r.Label, &occurred, &r.UTCOffset, &r.Body, &typ, &r.Project,
			&resolved, &r.ResolvedBy, &created, &updated, &srcType, &srcID, &srcURL, &imported, &clocks); err != nil {
			rows.Close()
			return out, database.Describe(err)
		}
		r.Type = Type(typ)
		r.OccurredAt, _ = parseTime(occurred)
		r.CreatedAt, _ = parseTime(created)
		r.UpdatedAt, _ = parseTime(updated)
		if resolved.Valid {
			t, _ := parseTime(resolved.String)
			r.ResolvedAt = &t
		}
		if srcType.Valid {
			r.Source = &Source{Type: srcType.String, ID: srcID.String, URL: srcURL.String}
			if imported.Valid {
				r.Source.ImportedAt, _ = parseTime(imported.String)
			}
		}
		r.Clocks = decodeClocks(clocks)
		entryIDs[id] = len(out.Entries)
		out.Entries = append(out.Entries, r)
	}
	rows.Close()
	if err := forEachRow(ctx, s.db, `SELECT et.entry_id, t.name FROM entry_tags et JOIN tags t ON t.id = et.tag_id ORDER BY t.name`, func(id int64, v string) {
		if i, ok := entryIDs[id]; ok {
			out.Entries[i].Tags = append(out.Entries[i].Tags, v)
		}
	}); err != nil {
		return out, err
	}
	if err := forEachRow(ctx, s.db, `SELECT entry_id, mark FROM entry_marks ORDER BY mark`, func(id int64, v string) {
		if i, ok := entryIDs[id]; ok {
			out.Entries[i].Marks = append(out.Entries[i].Marks, Mark(v))
		}
	}); err != nil {
		return out, err
	}

	// Tombstones and the report log.
	rows, err = s.db.QueryContext(ctx, `SELECT uid, kind, clock FROM tombstones`+dirty)
	if err != nil {
		return out, database.Describe(err)
	}
	for rows.Next() {
		var t TombstoneRecord
		var clock string
		if err := rows.Scan(&t.UID, &t.Kind, &clock); err != nil {
			rows.Close()
			return out, database.Describe(err)
		}
		t.Clock, _ = ParseHLC(clock)
		out.Tombstones = append(out.Tombstones, t)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT uid, kind, range_start, range_end, recorded_at FROM report_log`+dirty)
	if err != nil {
		return out, database.Describe(err)
	}
	for rows.Next() {
		var r ReportLogRecord
		var start, end sql.NullString
		var recorded string
		if err := rows.Scan(&r.UID, &r.Kind, &start, &end, &recorded); err != nil {
			rows.Close()
			return out, database.Describe(err)
		}
		r.RecordedAt, _ = parseTime(recorded)
		if start.Valid {
			t, _ := parseTime(start.String)
			r.Start = &t
		}
		if end.Valid {
			t, _ := parseTime(end.String)
			r.End = &t
		}
		out.Reports = append(out.Reports, r)
	}
	rows.Close()
	return out, nil
}

// MarkPublished clears the changed-here flag on the given records, once
// they have been written somewhere other devices can read them.
func (s *Store) MarkPublished(ctx context.Context, r Records) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, p := range r.Projects {
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET dirty = 0 WHERE uid = ?`, p.UID); err != nil {
				return database.Describe(err)
			}
		}
		for _, e := range r.Entries {
			if _, err := tx.ExecContext(ctx, `UPDATE entries SET dirty = 0 WHERE uid = ?`, e.UID); err != nil {
				return database.Describe(err)
			}
		}
		for _, t := range r.Tombstones {
			if _, err := tx.ExecContext(ctx, `UPDATE tombstones SET dirty = 0 WHERE uid = ?`, t.UID); err != nil {
				return database.Describe(err)
			}
		}
		for _, rl := range r.Reports {
			if _, err := tx.ExecContext(ctx, `UPDATE report_log SET dirty = 0 WHERE uid = ?`, rl.UID); err != nil {
				return database.Describe(err)
			}
		}
		return nil
	})
}

func forEachRow(ctx context.Context, q queryer, query string, fn func(id int64, v string)) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return database.Describe(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return database.Describe(err)
		}
		fn(id, v)
	}
	return database.Describe(rows.Err())
}

// ApplyOptions control how records are merged in.
type ApplyOptions struct {
	// DryRun reports what would change without changing anything.
	DryRun bool
}

// Renumbered records an incoming entry that kept its UID but had to take a
// new number because its number was already used here.
type Renumbered struct {
	UID  string
	From string
	To   string
}

// ApplyResult summarises a merge.
type ApplyResult struct {
	EntriesAdded, EntriesUpdated, EntriesUnchanged, EntriesDeleted int
	// EntriesSkipped counts incoming entries this archive deleted later, or
	// imported here already from the same source.
	EntriesSkipped                                                  int
	ProjectsAdded, ProjectsUpdated, ProjectsMerged, ProjectsDeleted int
	Reports                                                         int
	Renumbered                                                      []Renumbered
	Warnings                                                        []string
}

// Changed reports whether the merge changed anything.
func (r ApplyResult) Changed() bool {
	return r.EntriesAdded+r.EntriesUpdated+r.EntriesDeleted+r.ProjectsAdded+r.ProjectsUpdated+
		r.ProjectsMerged+r.ProjectsDeleted+r.Reports > 0
}

var errDryRun = errors.New("dry run")

// Apply merges records into the archive in one transaction. For every field
// the change with the later clock wins; a delete wins over changes older
// than it, and a change newer than a delete brings the record back.
// Applying the same records again changes nothing.
func (s *Store) Apply(ctx context.Context, in Records, opts ApplyOptions) (ApplyResult, error) {
	var res ApplyResult
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		m := merger{s: s, tx: tx, ctx: ctx, res: &res}
		if err := m.run(in); err != nil {
			return err
		}
		if opts.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return res, err
}

type merger struct {
	s   *Store
	tx  *sql.Tx
	ctx context.Context
	res *ApplyResult
	// renumber allows giving an incoming entry a new number.
	renumber bool
}

// errRenumber defers an entry whose number is taken here.
var errRenumber = errors.New("number taken")

func (m *merger) exec(query string, args ...any) error {
	_, err := m.tx.ExecContext(m.ctx, query, args...)
	return database.Describe(err)
}

func (m *merger) warn(format string, args ...any) {
	m.res.Warnings = append(m.res.Warnings, fmt.Sprintf(format, args...))
}

func (m *merger) run(in Records) error {
	self, err := m.s.self(m.ctx, m.tx)
	if err != nil {
		return err
	}
	if err := m.devices(in.Devices, self); err != nil {
		return err
	}
	// Keep this device's clock ahead of everything it has seen.
	var latest HLC
	for _, p := range in.Projects {
		if l := p.Clocks.Latest(); l.After(latest) {
			latest = l
		}
	}
	for _, e := range in.Entries {
		if l := e.Clocks.Latest(); l.After(latest) {
			latest = l
		}
	}
	for _, t := range in.Tombstones {
		if t.Clock.After(latest) {
			latest = t.Clock
		}
	}
	if err := observe(m.ctx, m.tx, latest); err != nil {
		return err
	}
	for _, p := range in.Projects {
		if err := m.project(p); err != nil {
			return fmt.Errorf("merging project %q: %w", p.Name, err)
		}
	}
	// Entries whose number is free here go first; those that must be
	// renumbered follow, so they never take a number another incoming
	// entry owns.
	var links, deferred []EntryRecord
	for pass, list := range [][]EntryRecord{in.Entries, nil} {
		if pass == 1 {
			list = deferred
		}
		m.renumber = pass == 1
		for _, e := range list {
			linked, err := m.entry(e)
			if errors.Is(err, errRenumber) {
				deferred = append(deferred, e)
				continue
			}
			if err != nil {
				return fmt.Errorf("merging entry %s: %w", e.Ref(), err)
			}
			if linked && e.ResolvedBy != "" {
				links = append(links, e)
			}
		}
	}
	// Resolution links last, so a resolver arriving in the same batch is found.
	for _, e := range links {
		if err := m.exec(`UPDATE entries SET resolved_by = (SELECT id FROM entries WHERE uid = ?) WHERE uid = ?`, e.ResolvedBy, e.UID); err != nil {
			return err
		}
	}
	for _, t := range in.Tombstones {
		if err := m.tombstone(t); err != nil {
			return err
		}
	}
	for _, r := range in.Reports {
		res, err := m.tx.ExecContext(m.ctx, `INSERT OR IGNORE INTO report_log (uid, kind, range_start, range_end, recorded_at, dirty)
			VALUES (?, ?, ?, ?, ?, 0)`, r.UID, r.Kind, nullTimePtr(r.Start), nullTimePtr(r.End), formatTime(r.RecordedAt))
		if err != nil {
			return database.Describe(err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			m.res.Reports++
		}
	}
	return nil
}

// observe advances the stored clock past a clock received from elsewhere.
func observe(ctx context.Context, tx queryer, h HLC) error {
	last, err := lastClock(ctx, tx)
	if err != nil {
		return err
	}
	if h.Wall > last.Wall || h.Wall == last.Wall && h.Count > last.Count {
		return saveClock(ctx, tx, HLC{Wall: h.Wall, Count: h.Count})
	}
	return nil
}

func nullTimePtr(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return nullTime(*t)
}

func (m *merger) devices(in []DeviceRecord, self Device) error {
	for _, d := range in {
		if d.UID == self.UID {
			continue
		}
		existing, err := deviceWhere(m.ctx, m.tx, `uid = ?`, d.UID)
		switch {
		case errors.Is(err, ErrNotFound):
			if other, err := deviceWhere(m.ctx, m.tx, `label = ?`, d.Label); err == nil {
				m.warn("device %s and device %s both use the label %q", d.UID, other.UID, d.Label)
				continue
			}
			if err := m.exec(`INSERT INTO devices (uid, label, name, created_at) VALUES (?, ?, ?, ?)`,
				d.UID, d.Label, d.Name, formatTime(m.s.now())); err != nil {
				return err
			}
		case err != nil:
			return err
		case existing.Label == "" && d.Label != "":
			if err := m.exec(`UPDATE devices SET label = ?, name = ? WHERE id = ?`, d.Label, d.Name, existing.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *merger) tombstoneClock(uid string) (HLC, bool, error) {
	var c string
	err := m.tx.QueryRowContext(m.ctx, `SELECT clock FROM tombstones WHERE uid = ?`, uid).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return HLC{}, false, nil
	}
	if err != nil {
		return HLC{}, false, database.Describe(err)
	}
	h, err := ParseHLC(c)
	return h, err == nil, nil
}

// buried reports whether a record with these clocks was deleted here later.
// A record changed after the delete brings it back, so its tombstone goes.
func (m *merger) buried(uid string, latest HLC) (bool, error) {
	t, ok, err := m.tombstoneClock(uid)
	if err != nil || !ok {
		return false, err
	}
	if !latest.After(t) {
		return true, nil
	}
	return false, m.exec(`DELETE FROM tombstones WHERE uid = ?`, uid)
}

// --- projects ---------------------------------------------------------------

type localProject struct {
	id     int64
	uid    string
	rec    ProjectRecord
	clocks Clocks
}

func (m *merger) localProject(where string, args ...any) (localProject, error) {
	var lp localProject
	var archived sql.NullString
	var updated, clocks string
	err := m.tx.QueryRowContext(m.ctx, `SELECT id, uid, name, description, archived_at, updated_at, clocks FROM projects WHERE `+where, args...).
		Scan(&lp.id, &lp.uid, &lp.rec.Name, &lp.rec.Description, &archived, &updated, &clocks)
	if errors.Is(err, sql.ErrNoRows) {
		return lp, ErrNotFound
	}
	if err != nil {
		return lp, database.Describe(err)
	}
	lp.rec.UpdatedAt, _ = parseTime(updated)
	if archived.Valid {
		t, _ := parseTime(archived.String)
		lp.rec.ArchivedAt = &t
	}
	lp.clocks = decodeClocks(clocks)
	return lp, nil
}

// projectByUID finds a project by UID, following merge redirects.
func (m *merger) projectByUID(uid string) (localProject, error) {
	lp, err := m.localProject(`uid = ?`, uid)
	if errors.Is(err, ErrNotFound) {
		return m.localProject(`id = (SELECT project_id FROM project_redirects WHERE uid = ?)`, uid)
	}
	return lp, err
}

func (m *merger) project(p ProjectRecord) error {
	name, err := NormalizeProjectName(p.Name)
	if err != nil {
		return err
	}
	p.Name = name
	if buried, err := m.buried(p.UID, p.Clocks.Latest()); err != nil || buried {
		return err
	}
	lp, err := m.projectByUID(p.UID)
	if errors.Is(err, ErrNotFound) {
		// A project with the same name made separately elsewhere is the
		// same project: merge them, keeping the lower UID on both sides.
		same, serr := m.s.lookupProject(m.ctx, m.tx, p.Name)
		if serr == nil {
			if p.UID < same.UID {
				if err := m.exec(`UPDATE projects SET uid = ? WHERE id = ?`, p.UID, same.ID); err != nil {
					return err
				}
				if err := m.exec(`INSERT OR REPLACE INTO project_redirects (uid, project_id) VALUES (?, ?)`, same.UID, same.ID); err != nil {
					return err
				}
			} else if err := m.exec(`INSERT OR REPLACE INTO project_redirects (uid, project_id) VALUES (?, ?)`, p.UID, same.ID); err != nil {
				return err
			}
			m.res.ProjectsMerged++
			lp, err = m.localProject(`id = ?`, same.ID)
			if err != nil {
				return err
			}
			return m.updateProject(lp, p, false)
		}
		if !errors.Is(serr, ErrNotFound) {
			return serr
		}
		if err := m.exec(`INSERT INTO projects (uid, name, description, archived_at, created_at, updated_at, clocks, dirty)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0)`, p.UID, p.Name, p.Description, nullTimePtr(p.ArchivedAt),
			formatTime(p.CreatedAt), formatTime(p.UpdatedAt), p.Clocks.encode()); err != nil {
			return err
		}
		m.res.ProjectsAdded++
		lp, err = m.localProject(`uid = ?`, p.UID)
		if err != nil {
			return err
		}
		if err := m.setAliases(lp.id, p.Name, p.Aliases); err != nil {
			return err
		}
		return m.setURLs(lp.id, p.URLs)
	}
	if err != nil {
		return err
	}
	return m.updateProject(lp, p, true)
}

func (m *merger) updateProject(lp localProject, p ProjectRecord, count bool) error {
	theirs := func(f string) bool {
		return p.Clocks.Get(f, ClockFromTime(p.UpdatedAt)).After(lp.clocks.Get(f, ClockFromTime(lp.rec.UpdatedAt)))
	}
	var changed []string
	if theirs(FieldName) && p.Name != lp.rec.Name {
		if other, err := m.s.lookupProject(m.ctx, m.tx, p.Name); err == nil && other.ID != lp.id {
			// Renamed to the name of another project: they are one project.
			keep, drop := lp.id, other.ID
			if other.UID < lp.uid {
				keep, drop = other.ID, lp.id
			}
			if err := m.mergeProjects(keep, drop); err != nil {
				return err
			}
			m.res.ProjectsMerged++
			return nil
		}
		if err := m.exec(`UPDATE projects SET name = ? WHERE id = ?`, p.Name, lp.id); err != nil {
			return err
		}
		changed = append(changed, FieldName)
	}
	if theirs(FieldDescription) && p.Description != lp.rec.Description {
		if err := m.exec(`UPDATE projects SET description = ? WHERE id = ?`, p.Description, lp.id); err != nil {
			return err
		}
		changed = append(changed, FieldDescription)
	}
	if theirs(FieldArchived) && !sameTimePtr(p.ArchivedAt, lp.rec.ArchivedAt) {
		if err := m.exec(`UPDATE projects SET archived_at = ? WHERE id = ?`, nullTimePtr(p.ArchivedAt), lp.id); err != nil {
			return err
		}
		changed = append(changed, FieldArchived)
	}
	if theirs(FieldAliases) {
		if err := m.setAliases(lp.id, p.Name, p.Aliases); err != nil {
			return err
		}
		changed = append(changed, FieldAliases)
	}
	if theirs(FieldURLs) {
		if err := m.setURLs(lp.id, p.URLs); err != nil {
			return err
		}
		changed = append(changed, FieldURLs)
	}
	if len(changed) == 0 {
		return nil
	}
	clocks := lp.clocks
	if clocks == nil {
		clocks = Clocks{}
	}
	for _, f := range changed {
		clocks[f] = p.Clocks.Get(f, ClockFromTime(p.UpdatedAt))
	}
	updated := lp.rec.UpdatedAt
	if p.UpdatedAt.After(updated) {
		updated = p.UpdatedAt
	}
	if err := m.exec(`UPDATE projects SET clocks = ?, updated_at = ? WHERE id = ?`, clocks.encode(), formatTime(updated), lp.id); err != nil {
		return err
	}
	if count {
		m.res.ProjectsUpdated++
	}
	return reindexProject(m.ctx, m.tx, lp.id)
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// setAliases replaces a project's aliases. An alias that is already another
// project's name or alias is left with that project.
func (m *merger) setAliases(id int64, name string, aliases []string) error {
	if err := m.exec(`DELETE FROM project_aliases WHERE project_id = ?`, id); err != nil {
		return err
	}
	for _, a := range aliases {
		if err := addAlias(m.ctx, m.tx, id, a); err != nil {
			if errors.Is(err, ErrConflict) {
				m.warn("project %q lost the alias %q, which another project uses", name, a)
				continue
			}
			return err
		}
	}
	return nil
}

func (m *merger) setURLs(id int64, urls []string) error {
	if err := m.exec(`DELETE FROM project_links WHERE project_id = ? AND kind = 'url'`, id); err != nil {
		return err
	}
	for _, u := range urls {
		if err := addLink(m.ctx, m.tx, id, "url", u); err != nil {
			return err
		}
	}
	return nil
}

// mergeProjects folds project drop into keep: its entries, aliases, links
// and UID (as a redirect) move over, and drop is removed.
func (m *merger) mergeProjects(keep, drop int64) error {
	var dropUID string
	if err := m.tx.QueryRowContext(m.ctx, `SELECT uid FROM projects WHERE id = ?`, drop).Scan(&dropUID); err != nil {
		return database.Describe(err)
	}
	for _, q := range []string{
		`UPDATE entries SET project_id = ? WHERE project_id = ?`,
		`UPDATE OR IGNORE project_aliases SET project_id = ? WHERE project_id = ?`,
		`UPDATE OR IGNORE project_links SET project_id = ? WHERE project_id = ?`,
		`UPDATE project_redirects SET project_id = ? WHERE project_id = ?`,
	} {
		if err := m.exec(q, keep, drop); err != nil {
			return err
		}
	}
	if err := m.exec(`DELETE FROM projects WHERE id = ?`, drop); err != nil {
		return err
	}
	if err := m.exec(`INSERT OR REPLACE INTO project_redirects (uid, project_id) VALUES (?, ?)`, dropUID, keep); err != nil {
		return err
	}
	return reindexProject(m.ctx, m.tx, keep)
}

// --- entries ----------------------------------------------------------------

// entry merges one entry record. It reports whether the record's resolution
// link should be applied.
func (m *merger) entry(r EntryRecord) (bool, error) {
	body, err := NormalizeBody(r.Body)
	if err != nil {
		return false, err
	}
	r.Body = body
	if buried, err := m.buried(r.UID, r.Clocks.Latest()); err != nil || buried {
		if buried {
			m.res.EntriesSkipped++
		}
		return false, err
	}
	var id int64
	var raw, updated string
	err = m.tx.QueryRowContext(m.ctx, `SELECT id, clocks, updated_at FROM entries WHERE uid = ?`, r.UID).Scan(&id, &raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return m.insertEntry(r)
	}
	if err != nil {
		return false, database.Describe(err)
	}
	local, err := m.s.getByID(m.ctx, m.tx, id)
	if err != nil {
		return false, err
	}
	return m.updateEntry(local, decodeClocks(raw), r)
}

func (m *merger) projectID(uid string) (sql.NullInt64, error) {
	if uid == "" {
		return sql.NullInt64{}, nil
	}
	lp, err := m.projectByUID(uid)
	if errors.Is(err, ErrNotFound) {
		return sql.NullInt64{}, nil // the project was deleted
	}
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: lp.id, Valid: true}, nil
}

func (m *merger) insertEntry(r EntryRecord) (bool, error) {
	if r.Source != nil {
		var existing int64
		var existingUID string
		err := m.tx.QueryRowContext(m.ctx, `SELECT id, uid FROM entries WHERE source_type = ? AND source_id = ?`,
			r.Source.Type, r.Source.ID).Scan(&existing, &existingUID)
		if err == nil {
			// The same commit (say) was imported on both devices. Keep the
			// record with the lower UID everywhere.
			if existingUID < r.UID {
				m.res.EntriesSkipped++
				return false, nil
			}
			if err := m.exec(`DELETE FROM entries WHERE id = ?`, existing); err != nil {
				return false, err
			}
			if err := m.exec(`DELETE FROM entries_fts WHERE rowid = ?`, existing); err != nil {
				return false, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, database.Describe(err)
		}
	}
	num, numDevice, err := m.number(r)
	if err != nil {
		return false, err
	}
	pid, err := m.projectID(r.Project)
	if err != nil {
		return false, err
	}
	var src struct{ typ, id, url, at sql.NullString }
	if r.Source != nil {
		src.typ = sql.NullString{String: r.Source.Type, Valid: true}
		src.id = sql.NullString{String: r.Source.ID, Valid: true}
		src.url = sql.NullString{String: r.Source.URL, Valid: r.Source.URL != ""}
		src.at = sql.NullString{String: formatTime(r.Source.ImportedAt), Valid: !r.Source.ImportedAt.IsZero()}
	}
	res, err := m.tx.ExecContext(m.ctx, `
		INSERT INTO entries (uid, num, num_device, occurred_at, utc_offset, body, type, project_id, resolved_at,
		                     created_at, updated_at, source_type, source_id, source_url, imported_at, clocks, dirty)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		r.UID, num, numDevice, formatTime(r.OccurredAt), r.UTCOffset, r.Body, nullString(string(r.Type)), pid,
		nullTimePtr(r.ResolvedAt), formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
		src.typ, src.id, src.url, src.at, r.Clocks.encode())
	if err != nil {
		return false, database.Describe(fmt.Errorf("saving entry: %w", err))
	}
	id, _ := res.LastInsertId()
	tags, err := NormalizeTags(r.Tags)
	if err != nil {
		return false, err
	}
	if err := setTags(m.ctx, m.tx, id, tags); err != nil {
		return false, err
	}
	if err := m.s.setMarks(m.ctx, m.tx, id, r.Marks); err != nil {
		return false, err
	}
	m.res.EntriesAdded++
	return true, reindex(m.ctx, m.tx, id)
}

// number chooses the number for an incoming entry: its own, unless that is
// already used here by another entry or its device is unknown, in which case
// it takes this device's next number. Counters move past any number seen,
// so this device never issues it again.
func (m *merger) number(r EntryRecord) (int64, sql.NullInt64, error) {
	var dev sql.NullInt64
	ok := r.Num > 0
	if ok && r.Label != "" {
		d, err := deviceWhere(m.ctx, m.tx, `label = ?`, r.Label)
		if errors.Is(err, ErrNotFound) {
			ok = false
		} else if err != nil {
			return 0, dev, err
		} else {
			dev = sql.NullInt64{Int64: d.ID, Valid: true}
		}
	}
	if ok {
		var taken int
		if err := m.tx.QueryRowContext(m.ctx, `SELECT count(*) FROM entries WHERE coalesce(num_device, 0) = ? AND num = ?`,
			dev.Int64, r.Num).Scan(&taken); err != nil {
			return 0, dev, database.Describe(err)
		}
		ok = taken == 0
	}
	if !ok && !m.renumber {
		return 0, dev, errRenumber
	}
	if ok {
		if dev.Valid {
			err := m.exec(`UPDATE devices SET next_num = max(next_num, ?) WHERE id = ?`, r.Num+1, dev.Int64)
			return r.Num, dev, err
		}
		v, err := meta(m.ctx, m.tx, "plain_next")
		if err != nil {
			return 0, dev, err
		}
		if next, _ := strconv.ParseInt(v, 10, 64); r.Num >= next {
			if err := setMeta(m.ctx, m.tx, "plain_next", strconv.FormatInt(r.Num+1, 10)); err != nil {
				return 0, dev, err
			}
		}
		return r.Num, dev, nil
	}
	num, dev, err := m.s.nextNumber(m.ctx, m.tx)
	if err != nil {
		return 0, dev, err
	}
	to := FormatRef(num, "")
	if dev.Valid {
		self, _ := m.s.self(m.ctx, m.tx)
		to = FormatRef(num, self.Label)
	}
	m.res.Renumbered = append(m.res.Renumbered, Renumbered{UID: r.UID, From: r.Ref(), To: to})
	return num, dev, nil
}

func (m *merger) updateEntry(local Entry, clocks Clocks, r EntryRecord) (bool, error) {
	fallbackLocal := ClockFromTime(local.UpdatedAt)
	fallbackTheirs := ClockFromTime(r.UpdatedAt)
	theirs := func(f string) bool {
		return r.Clocks.Get(f, fallbackTheirs).After(clocks.Get(f, fallbackLocal))
	}
	var changed []string
	var sets []string
	var args []any
	if theirs(FieldBody) && r.Body != local.Body {
		sets, args = append(sets, "body = ?"), append(args, r.Body)
		changed = append(changed, FieldBody)
	}
	if theirs(FieldTime) && (!r.OccurredAt.Equal(local.OccurredAt) || r.UTCOffset != local.UTCOffset) {
		sets, args = append(sets, "occurred_at = ?", "utc_offset = ?"), append(args, formatTime(r.OccurredAt), r.UTCOffset)
		changed = append(changed, FieldTime)
	}
	if theirs(FieldType) && r.Type != local.Type {
		sets, args = append(sets, "type = ?"), append(args, nullString(string(r.Type)))
		changed = append(changed, FieldType)
	}
	if theirs(FieldProject) {
		pid, err := m.projectID(r.Project)
		if err != nil {
			return false, err
		}
		if pid.Int64 != local.ProjectID {
			sets, args = append(sets, "project_id = ?"), append(args, pid)
			changed = append(changed, FieldProject)
		}
	}
	link := false
	if theirs(FieldResolved) {
		var localBy string
		if local.ResolvedBy != 0 {
			_ = m.tx.QueryRowContext(m.ctx, `SELECT uid FROM entries WHERE id = ?`, local.ResolvedBy).Scan(&localBy)
		}
		if !sameTimePtr(r.ResolvedAt, local.ResolvedAt) || r.ResolvedBy != localBy {
			sets, args = append(sets, "resolved_at = ?", "resolved_by = NULL"), append(args, nullTimePtr(r.ResolvedAt))
			changed = append(changed, FieldResolved)
			link = r.ResolvedBy != ""
		}
	}
	if len(sets) > 0 {
		args = append(args, local.ID)
		if err := m.exec(`UPDATE entries SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return false, err
		}
	}
	if theirs(FieldTags) {
		tags, err := NormalizeTags(r.Tags)
		if err != nil {
			return false, err
		}
		if !slices.Equal(tags, local.Tags) {
			if err := setTags(m.ctx, m.tx, local.ID, tags); err != nil {
				return false, err
			}
			changed = append(changed, FieldTags)
		}
	}
	if theirs(FieldMarks) {
		marks := uniqueSorted(r.Marks)
		if !slices.Equal(marks, local.Marks) {
			if err := m.s.setMarks(m.ctx, m.tx, local.ID, marks); err != nil {
				return false, err
			}
			changed = append(changed, FieldMarks)
		}
	}
	// Adopt the winning clocks even where the values already agree, so the
	// fields converge on every device.
	merged := Clocks{}
	for _, f := range EntryFields {
		mine, other := clocks.Get(f, fallbackLocal), r.Clocks.Get(f, fallbackTheirs)
		if other.After(mine) {
			merged[f] = other
		} else {
			merged[f] = mine
		}
	}
	updated := local.UpdatedAt
	if len(changed) > 0 && r.UpdatedAt.After(updated) {
		updated = r.UpdatedAt
	}
	if err := m.exec(`UPDATE entries SET clocks = ?, updated_at = ? WHERE id = ?`, merged.encode(), formatTime(updated), local.ID); err != nil {
		return false, err
	}
	if len(changed) == 0 {
		m.res.EntriesUnchanged++
		return false, nil
	}
	m.res.EntriesUpdated++
	return link, reindex(m.ctx, m.tx, local.ID)
}

// --- tombstones -------------------------------------------------------------

func (m *merger) tombstone(t TombstoneRecord) error {
	switch t.Kind {
	case "entry":
		var id int64
		var raw, updated string
		err := m.tx.QueryRowContext(m.ctx, `SELECT id, clocks, updated_at FROM entries WHERE uid = ?`, t.UID).Scan(&id, &raw, &updated)
		if err == nil {
			up, _ := parseTime(updated)
			latest := decodeClocks(raw).Latest()
			if latest.IsZero() {
				latest = ClockFromTime(up)
			}
			if latest.After(t.Clock) {
				return nil // changed here after the delete: it stays, and wins
			}
			for _, q := range []string{`DELETE FROM entries WHERE id = ?`, `DELETE FROM entries_fts WHERE rowid = ?`} {
				if err := m.exec(q, id); err != nil {
					return err
				}
			}
			if err := pruneTags(m.ctx, m.tx); err != nil {
				return err
			}
			m.res.EntriesDeleted++
		} else if !errors.Is(err, sql.ErrNoRows) {
			return database.Describe(err)
		}
	case "project":
		lp, err := m.projectByUID(t.UID)
		if err == nil {
			latest := lp.clocks.Latest()
			if latest.IsZero() {
				latest = ClockFromTime(lp.rec.UpdatedAt)
			}
			if latest.After(t.Clock) {
				return nil
			}
			ids, err := entriesOfProject(m.ctx, m.tx, lp.id)
			if err != nil {
				return err
			}
			if err := m.exec(`DELETE FROM projects WHERE id = ?`, lp.id); err != nil {
				return err
			}
			for _, id := range ids {
				if err := reindex(m.ctx, m.tx, id); err != nil {
					return err
				}
			}
			m.res.ProjectsDeleted++
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	default:
		return nil
	}
	// Keep the latest tombstone, so an older copy cannot bring it back.
	return m.exec(`INSERT INTO tombstones (uid, kind, clock, dirty) VALUES (?, ?, ?, 0)
		ON CONFLICT (uid) DO UPDATE SET clock = excluded.clock
		WHERE excluded.clock > tombstones.clock`, t.UID, t.Kind, t.Clock.String())
}

func entriesOfProject(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM entries WHERE project_id = ?`, id)
	if err != nil {
		return nil, database.Describe(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var x int64
		if err := rows.Scan(&x); err != nil {
			return nil, database.Describe(err)
		}
		ids = append(ids, x)
	}
	return ids, database.Describe(rows.Err())
}
