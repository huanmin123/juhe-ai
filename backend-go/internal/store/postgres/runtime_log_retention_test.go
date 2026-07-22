package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

func TestRuntimeLogRetentionSQLContracts(t *testing.T) {
	for _, want := range []string{
		"LOCK TABLE juhe_dataset.runtime_log_facet_summary",
		"juhe_dataset.runtime_log_level_facets",
		"juhe_dataset.runtime_log_event_facets",
		"IN SHARE ROW EXCLUSIVE MODE",
	} {
		if !strings.Contains(runtimeLogRetentionFacetLockSQL, want) {
			t.Fatalf("facet lock SQL missing %q", want)
		}
	}
	for _, want := range []string{
		"WHERE time < $1::text",
		"ORDER BY time ASC, id ASC",
		"LIMIT $2::int",
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM juhe_dataset.runtime_logs",
		"RETURNING runtime_logs.time, runtime_logs.level",
	} {
		if !strings.Contains(runtimeLogRetentionDeleteSQL, want) {
			t.Fatalf("delete SQL missing %q", want)
		}
	}
	for _, want := range []string{"GREATEST(0, total_count - $2::bigint)", "ORDER BY time ASC, id ASC", "ORDER BY time DESC, id DESC"} {
		if !strings.Contains(runtimeLogRetentionSummaryUpdateSQL, want) {
			t.Fatalf("summary SQL missing %q", want)
		}
	}
	if !strings.Contains(runtimeLogRetentionLevelUpdateSQL, "GREATEST(0, facets.count - decrements.count)") {
		t.Fatal("level facet SQL must prevent negative counts")
	}
	for _, want := range []string{
		"GREATEST(0, facets.count - decrements.count)",
		"COALESCE(NULLIF(BTRIM(logs.event), ''), '') = decrements.event",
		"ORDER BY logs.time DESC, logs.id DESC",
	} {
		if !strings.Contains(runtimeLogRetentionEventUpdateSQL, want) {
			t.Fatalf("event facet SQL missing %q", want)
		}
	}
	for _, want := range []string{
		"updated_at < $1::text",
		"cursor_offset >= file_size",
		"last_error_message IS NULL",
		"ORDER BY updated_at ASC, log_file ASC",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(runtimeLogRetentionCursorDeleteSQL, want) {
			t.Fatalf("cursor SQL missing %q", want)
		}
	}
}

func TestCleanupRuntimeLogIndexBeforeCommitsFacetMaintenanceInOneTransaction(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{
		earliest: "2026-07-01T00:00:00.000Z",
		deleted: []runtimeLogRetentionDeletedRow{
			{time: "2026-07-01T00:00:00.000Z", level: "info", event: "job_start"},
			{time: "2026-07-01T00:00:01.000Z", level: "info", event: "job_start"},
		},
	}
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		CutoffISO: "2026-07-08T00:00:00.000Z",
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("cleanupRuntimeLogIndexBefore() error = %v", err)
	}
	if deleted != 2 || !tx.committed || tx.rolledBack {
		t.Fatalf("deleted=%d committed=%v rolledBack=%v", deleted, tx.committed, tx.rolledBack)
	}
	joined := strings.Join(tx.statements, "\n")
	for _, want := range []string{
		"LOCK TABLE juhe_dataset.runtime_log_facet_summary",
		"SELECT COALESCE((SELECT earliest_time",
		"DELETE FROM juhe_dataset.runtime_logs",
		"UPDATE juhe_dataset.runtime_log_facet_summary",
		"UPDATE juhe_dataset.runtime_log_level_facets",
		"UPDATE juhe_dataset.runtime_log_event_facets",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("transaction statements missing %q:\n%s", want, joined)
		}
	}
}

func TestCleanupRuntimeLogIndexBeforeRollsBackOnFacetFailure(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{
		earliest:     "2026-07-01T00:00:00.000Z",
		deleted:      []runtimeLogRetentionDeletedRow{{time: "2026-07-01T00:00:00.000Z", level: "warn", event: "failed"}},
		failContains: "UPDATE juhe_dataset.runtime_log_level_facets",
	}
	_, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		CutoffISO: "2026-07-08T00:00:00.000Z",
		Limit:     1,
	})
	if err == nil || !tx.rolledBack || tx.committed {
		t.Fatalf("error=%v committed=%v rolledBack=%v", err, tx.committed, tx.rolledBack)
	}
}

func TestParseRuntimeLogIndexRetentionDaysMatchesNodeFallback(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		value int
		found bool
	}{
		{raw: "14", value: 14, found: true},
		{raw: "0", value: 0, found: true},
		{raw: "\"14\"", found: false},
		{raw: "14.5", found: false},
		{raw: "null", found: false},
	} {
		value, found := parseRuntimeLogIndexRetentionDays(tc.raw)
		if value != tc.value || found != tc.found {
			t.Fatalf("parseRuntimeLogIndexRetentionDays(%q)=(%d,%v), want (%d,%v)", tc.raw, value, found, tc.value, tc.found)
		}
	}
}

type runtimeLogRetentionBeginnerStub struct{ tx *runtimeLogRetentionTxStub }

func (s runtimeLogRetentionBeginnerStub) BeginRuntimeLogRetentionTx(context.Context) (runtimeLogRetentionTx, error) {
	return s.tx, nil
}

type runtimeLogRetentionDeletedRow struct{ time, level, event string }

type runtimeLogRetentionTxStub struct {
	earliest     string
	deleted      []runtimeLogRetentionDeletedRow
	statements   []string
	failContains string
	committed    bool
	rolledBack   bool
}

func (s *runtimeLogRetentionTxStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	if s.failContains != "" && strings.Contains(sql, s.failContains) {
		return pgconn.CommandTag{}, errors.New("injected failure")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s *runtimeLogRetentionTxStub) Query(_ context.Context, sql string, _ ...any) (runtimeLogRetentionRows, error) {
	s.statements = append(s.statements, sql)
	return &runtimeLogRetentionRowsStub{rows: append([]runtimeLogRetentionDeletedRow{}, s.deleted...)}, nil
}

func (s *runtimeLogRetentionTxStub) QueryRow(_ context.Context, sql string, _ ...any) runtimeLogRetentionRow {
	s.statements = append(s.statements, sql)
	return runtimeLogRetentionRowStub{value: s.earliest}
}

func (s *runtimeLogRetentionTxStub) Commit(context.Context) error   { s.committed = true; return nil }
func (s *runtimeLogRetentionTxStub) Rollback(context.Context) error { s.rolledBack = true; return nil }

type runtimeLogRetentionRowStub struct{ value string }

func (r runtimeLogRetentionRowStub) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.value
	return nil
}

type runtimeLogRetentionRowsStub struct {
	rows  []runtimeLogRetentionDeletedRow
	index int
}

func (r *runtimeLogRetentionRowsStub) Next() bool { return r.index < len(r.rows) }
func (r *runtimeLogRetentionRowsStub) Scan(dest ...any) error {
	row := r.rows[r.index]
	r.index++
	*(dest[0].(*string)) = row.time
	*(dest[1].(*string)) = row.level
	*(dest[2].(*string)) = row.event
	return nil
}
func (r *runtimeLogRetentionRowsStub) Err() error { return nil }
func (r *runtimeLogRetentionRowsStub) Close()     {}
