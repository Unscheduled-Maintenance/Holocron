package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// ReportRecord is a report someone recorded as sent.
type ReportRecord struct {
	Kind       string
	Range      timerange.Range
	RecordedAt time.Time
}

// Until returns where the next report should start: the end of the recorded
// range, or the moment it was recorded when that is earlier (a this-week
// report sent on Wednesday covers nothing after Wednesday) or the range was
// open-ended.
func (r ReportRecord) Until() time.Time {
	if r.Range.End.IsZero() || r.RecordedAt.Before(r.Range.End) {
		return r.RecordedAt
	}
	return r.Range.End
}

// RecordReport remembers that a report of this kind covering rng was sent.
func (s *Store) RecordReport(ctx context.Context, kind string, rng timerange.Range) (ReportRecord, error) {
	rec := ReportRecord{Kind: kind, Range: rng, RecordedAt: s.now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO report_log (kind, range_start, range_end, recorded_at) VALUES (?, ?, ?, ?)`,
		kind, nullTime(rng.Start), nullTime(rng.End), formatTime(rec.RecordedAt))
	if err != nil {
		return ReportRecord{}, database.Describe(fmt.Errorf("recording report: %w", err))
	}
	return rec, nil
}

// LastReport returns the most recently recorded report of this kind.
func (s *Store) LastReport(ctx context.Context, kind string) (ReportRecord, error) {
	var start, end sql.NullString
	var recorded string
	err := s.db.QueryRowContext(ctx, `SELECT range_start, range_end, recorded_at FROM report_log
		WHERE kind = ? ORDER BY recorded_at DESC, id DESC LIMIT 1`, kind).Scan(&start, &end, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return ReportRecord{}, fmt.Errorf("%w: no %s report has been recorded", ErrNotFound, kind)
	}
	if err != nil {
		return ReportRecord{}, database.Describe(err)
	}
	rec := ReportRecord{Kind: kind}
	if rec.RecordedAt, err = parseTime(recorded); err != nil {
		return ReportRecord{}, fmt.Errorf("recorded %s report has an unreadable timestamp %q: %w", kind, recorded, err)
	}
	if start.Valid {
		rec.Range.Start, _ = parseTime(start.String)
	}
	if end.Valid {
		rec.Range.End, _ = parseTime(end.String)
	}
	return rec, nil
}

func nullTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(t), Valid: true}
}
