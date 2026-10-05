package journal

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// Order controls result ordering.
type Order int

// Result orderings.
const (
	NewestFirst Order = iota
	OldestFirst
	// Relevance orders text searches by BM25 score; without text it is NewestFirst.
	Relevance
)

// Query selects entries. Every field is optional; criteria combine with AND.
type Query struct {
	// Text is a full-text search expression (see BuildMatch).
	Text  string
	Range timerange.Range
	// Projects matches any of these projects (names or aliases). The special
	// value "-" matches entries with no project.
	Projects []string
	// Tags requires every listed tag.
	Tags []string
	// Types matches any of these types.
	Types []Type
	// Marks matches any of these report marks.
	Marks []Mark
	// OpenOnly restricts to unresolved problems and follow-ups.
	OpenOnly bool
	// Imported filters by provenance: nil for all, true for imported only,
	// false for hand-written only.
	Imported *bool
	// SourceType restricts to entries imported from a given source.
	SourceType string
	Limit      int
	Offset     int
	Order      Order
}

const selectEntries = `
SELECT e.id, e.uid, e.occurred_at, e.utc_offset, e.body, coalesce(e.type, ''),
       coalesce(e.project_id, 0), coalesce(p.name, ''), e.resolved_at,
       e.created_at, e.updated_at,
       e.source_type, e.source_id, e.source_url, e.imported_at`

const fromEntries = `
FROM entries e
LEFT JOIN projects p ON p.id = e.project_id`

// Find returns the entries matching q.
func (s *Store) Find(ctx context.Context, q Query) ([]Entry, error) {
	where, args, match, err := s.buildWhere(ctx, q)
	if err != nil {
		return nil, err
	}
	var order string
	switch {
	case q.Order == Relevance && match != "":
		order = "\nORDER BY bm25(entries_fts, 10.0, 2.0, 2.0), e.occurred_at DESC"
	case q.Order == OldestFirst:
		order = "\nORDER BY e.occurred_at ASC, e.id ASC"
	default:
		order = "\nORDER BY e.occurred_at DESC, e.id DESC"
	}
	filter := fromEntries
	if match != "" {
		filter += "\nJOIN entries_fts ON entries_fts.rowid = e.id"
	}
	if len(where) > 0 {
		filter += "\nWHERE " + strings.Join(where, "\n  AND ")
	}

	var sb strings.Builder
	sb.WriteString(selectEntries)
	if match != "" {
		fmt.Fprintf(&sb, `, snippet(entries_fts, 0, '%s', '%s', '…', 32)`, HighlightStart, HighlightEnd)
	}
	sb.WriteString(filter + order)
	if q.Limit > 0 {
		sb.WriteString("\nLIMIT ? OFFSET ?")
		args = append(args, q.Limit, q.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		if match != "" && strings.Contains(err.Error(), "fts5") {
			return nil, fmt.Errorf("%w: cannot search for %q: %w", ErrInvalid, q.Text, err)
		}
		return nil, database.Describe(fmt.Errorf("searching entries: %w", err))
	}
	list, err := scanEntries(rows, match != "")
	if err != nil {
		return nil, err
	}
	return list, attachLabels(ctx, s.db, list)
}

// Count returns how many entries match q (ignoring Limit/Offset).
func (s *Store) Count(ctx context.Context, q Query) (int, error) {
	where, args, match, err := s.buildWhere(ctx, q)
	if err != nil {
		return 0, err
	}
	query := `SELECT count(*)` + fromEntries
	if match != "" {
		query += "\nJOIN entries_fts ON entries_fts.rowid = e.id"
	}
	if len(where) > 0 {
		query += "\nWHERE " + strings.Join(where, " AND ")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, database.Describe(err)
	}
	return n, nil
}

func (s *Store) buildWhere(ctx context.Context, q Query) (where []string, args []any, match string, err error) {
	if strings.TrimSpace(q.Text) != "" {
		match, err = BuildMatch(q.Text)
		if err != nil {
			return nil, nil, "", err
		}
		if match != "" {
			where = append(where, "entries_fts MATCH ?")
			args = append(args, match)
		}
	}
	if !q.Range.Start.IsZero() {
		where = append(where, "e.occurred_at >= ?")
		args = append(args, formatTime(q.Range.Start))
	}
	if !q.Range.End.IsZero() {
		where = append(where, "e.occurred_at < ?")
		args = append(args, formatTime(q.Range.End))
	}
	if len(q.Projects) > 0 {
		var ids []string
		none := false
		for _, name := range q.Projects {
			if name == "-" {
				none = true
				continue
			}
			p, err := s.resolveProject(ctx, s.db, name, false)
			if err != nil {
				return nil, nil, "", err
			}
			ids = append(ids, "?")
			args = append(args, p.ID)
		}
		var ors []string
		if len(ids) > 0 {
			ors = append(ors, "e.project_id IN ("+strings.Join(ids, ",")+")")
		}
		if none {
			ors = append(ors, "e.project_id IS NULL")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if len(q.Tags) > 0 {
		tags, err := NormalizeTags(q.Tags)
		if err != nil {
			return nil, nil, "", err
		}
		for _, t := range tags {
			// IN (subquery) builds the matching set once from the tag index;
			// a correlated EXISTS would run once for every entry scanned.
			where = append(where, "e.id IN (SELECT et.entry_id FROM entry_tags et JOIN tags t ON t.id = et.tag_id WHERE t.name = ?)")
			args = append(args, t)
		}
	}
	if len(q.Types) > 0 {
		ph := make([]string, len(q.Types))
		for i, t := range q.Types {
			ph[i] = "?"
			args = append(args, string(t))
		}
		where = append(where, "e.type IN ("+strings.Join(ph, ",")+")")
	}
	if len(q.Marks) > 0 {
		ph := make([]string, len(q.Marks))
		for i, m := range q.Marks {
			ph[i] = "?"
			args = append(args, string(m))
		}
		where = append(where, "e.id IN (SELECT m.entry_id FROM entry_marks m WHERE m.mark IN ("+strings.Join(ph, ",")+"))")
	}
	if q.OpenOnly {
		where = append(where, "e.type IN ('problem', 'follow-up') AND e.resolved_at IS NULL")
	}
	if q.Imported != nil {
		if *q.Imported {
			where = append(where, "e.source_type IS NOT NULL")
		} else {
			where = append(where, "e.source_type IS NULL")
		}
	}
	if q.SourceType != "" {
		where = append(where, "e.source_type = ?")
		args = append(args, q.SourceType)
	}
	return where, args, match, nil
}

func scanEntries(rows *sql.Rows, withSnippet bool) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var typ, occurred, created, updated string
		var resolved, srcType, srcID, srcURL, imported sql.NullString
		dest := []any{&e.ID, &e.UID, &occurred, &e.UTCOffset, &e.Body, &typ,
			&e.ProjectID, &e.Project, &resolved, &created, &updated,
			&srcType, &srcID, &srcURL, &imported}
		if withSnippet {
			dest = append(dest, &e.Snippet)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, database.Describe(fmt.Errorf("reading entry: %w", err))
		}
		e.Type = Type(typ)
		var err error
		if e.OccurredAt, err = parseTime(occurred); err != nil {
			return nil, fmt.Errorf("entry #%d has an unreadable timestamp %q: %w", e.ID, occurred, err)
		}
		e.CreatedAt, _ = parseTime(created)
		e.UpdatedAt, _ = parseTime(updated)
		if resolved.Valid {
			t, _ := parseTime(resolved.String)
			e.ResolvedAt = &t
		}
		if srcType.Valid {
			e.Source = &Source{Type: srcType.String, ID: srcID.String, URL: srcURL.String}
			if imported.Valid {
				e.Source.ImportedAt, _ = parseTime(imported.String)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, database.Describe(err)
	}
	return out, nil
}

// BuildMatch converts a person's search text into an FTS5 MATCH expression.
//
//	word          matches words starting with "word"; common English suffixes
//	              are also relaxed, so "investigating" finds "investigated"
//	"exact words" matches the phrase exactly
//	-word         excludes entries containing the word
//	a OR b        matches either side
//
// Everything else is quoted, so punctuation can never produce an FTS5 syntax
// error.
func BuildMatch(text string) (string, error) {
	var pos, neg []string
	var ops []string // operator preceding each positive term after the first
	pendingOr := false
	for _, tok := range tokenizeQuery(text) {
		switch {
		case tok.phrase:
			term := quoteFTS(tok.text)
			if term == "" {
				continue
			}
			if tok.negate {
				neg = append(neg, term)
				continue
			}
			pos = append(pos, term)
		case tok.text == "OR":
			pendingOr = len(pos) > 0
			continue
		default:
			term := wordTerm(tok.text)
			if term == "" {
				continue
			}
			if tok.negate {
				neg = append(neg, term)
				continue
			}
			pos = append(pos, term)
		}
		if len(pos) > 1 {
			if pendingOr {
				ops = append(ops, " OR ")
			} else {
				ops = append(ops, " AND ")
			}
		}
		pendingOr = false
	}
	if len(pos) == 0 {
		if len(neg) > 0 {
			return "", fmt.Errorf("%w: a search needs at least one word to look for, not only exclusions", ErrInvalid)
		}
		return "", nil
	}
	var b strings.Builder
	b.WriteString("(")
	b.WriteString(pos[0])
	for i, p := range pos[1:] {
		b.WriteString(ops[i])
		b.WriteString(p)
	}
	b.WriteString(")")
	for _, n := range neg {
		b.WriteString(" NOT ")
		b.WriteString(n)
	}
	return b.String(), nil
}

type queryToken struct {
	text           string
	phrase, negate bool
}

func tokenizeQuery(s string) []queryToken {
	var out []queryToken
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		neg := false
		if rs[i] == '-' && i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
			neg = true
			i++
		}
		if rs[i] == '"' {
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				j++
			}
			out = append(out, queryToken{text: string(rs[i+1 : j]), phrase: true, negate: neg})
			i = j + 1
			continue
		}
		j := i
		for j < len(rs) && !unicode.IsSpace(rs[j]) {
			j++
		}
		out = append(out, queryToken{text: string(rs[i:j]), negate: neg})
		i = j
	}
	return out
}

// quoteFTS turns arbitrary text into a quoted FTS5 string, or "" when the
// text contains nothing searchable.
func quoteFTS(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func wordTerm(w string) string {
	q := quoteFTS(w)
	if q == "" {
		return ""
	}
	// Words containing punctuation ("s3://bucket", "follow-up") become
	// phrases; prefix matching applies to the last token only.
	stem := relaxSuffix(strings.ToLower(w))
	if stem != "" && stem != strings.ToLower(w) {
		return "(" + q + "* OR " + quoteFTS(stem) + "*)"
	}
	return q + "*"
}

// relaxSuffix strips one common English inflection so that a prefix search
// for the remaining stem also finds other forms of the word. This is a
// deliberately small heuristic, not a full stemmer.
func relaxSuffix(w string) string {
	if len(w) < 5 || strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) {
		return ""
	}
	rules := []struct {
		suffix string
		min    int
	}{
		{"ations", 4}, {"ation", 4}, {"ings", 3}, {"ing", 3}, {"ied", 4}, {"ies", 4},
		{"ers", 5}, {"er", 5}, {"ed", 3}, {"es", 4}, {"s", 4},
	}
	for _, r := range rules {
		if strings.HasSuffix(w, r.suffix) && len(w)-len(r.suffix) >= r.min {
			return w[:len(w)-len(r.suffix)]
		}
	}
	return ""
}

// attachLabels loads the tags and marks of every entry in es in a few set
// queries, instead of two correlated subqueries per row (which made SQLite
// build a small sorted temporary table for every entry returned).
//
// When the entries' IDs are dense — recent entries, the common case — one
// range scan over each label table's primary key is cheapest. When they are
// scattered across years, the IDs are passed as a single JSON array and
// probed individually. Tag names come from the small tags table and labels
// are sorted in Go, so neither query needs a join or a sort.
func attachLabels(ctx context.Context, q queryer, es []Entry) error {
	if len(es) == 0 {
		return nil
	}
	index := make(map[int64]int, len(es))
	lo, hi := es[0].ID, es[0].ID
	for i := range es {
		index[es[i].ID] = i
		lo, hi = min(lo, es[i].ID), max(hi, es[i].ID)
	}
	var where string
	var args []any
	if hi-lo+1 <= int64(3*len(es)) {
		where, args = "entry_id BETWEEN ? AND ?", []any{lo, hi}
	} else {
		ids := make([]byte, 0, len(es)*7)
		ids = append(ids, '[')
		for i := range es {
			if i > 0 {
				ids = append(ids, ',')
			}
			ids = strconv.AppendInt(ids, es[i].ID, 10)
		}
		ids = append(ids, ']')
		where, args = "entry_id IN (SELECT value FROM json_each(?))", []any{string(ids)}
	}

	names := map[int64]string{}
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM tags`)
	if err != nil {
		return database.Describe(fmt.Errorf("loading tags: %w", err))
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return database.Describe(err)
		}
		names[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return database.Describe(err)
	}

	rows, err = q.QueryContext(ctx, `SELECT entry_id, tag_id FROM entry_tags WHERE `+where, args...)
	if err != nil {
		return database.Describe(fmt.Errorf("loading tags: %w", err))
	}
	for rows.Next() {
		var id, tag int64
		if err := rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return database.Describe(err)
		}
		if i, ok := index[id]; ok {
			es[i].Tags = append(es[i].Tags, names[tag])
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return database.Describe(err)
	}

	rows, err = q.QueryContext(ctx, `SELECT entry_id, mark FROM entry_marks WHERE `+where, args...)
	if err != nil {
		return database.Describe(fmt.Errorf("loading marks: %w", err))
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var mark string
		if err := rows.Scan(&id, &mark); err != nil {
			return database.Describe(err)
		}
		if i, ok := index[id]; ok {
			es[i].Marks = append(es[i].Marks, Mark(mark))
		}
	}
	if err := rows.Err(); err != nil {
		return database.Describe(err)
	}
	for i := range es {
		if len(es[i].Tags) > 1 {
			slices.Sort(es[i].Tags)
		}
		if len(es[i].Marks) > 1 {
			slices.Sort(es[i].Marks)
		}
	}
	return nil
}
