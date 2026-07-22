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
	for _, statement := range []string{
		runtimeLogRetentionLockTimeoutSQL,
		runtimeLogRetentionStatementTimeoutSQL,
		runtimeLogRetentionTryAdvisoryLockSQL,
		runtimeLogRetentionEarliestCountedSQL,
	} {
		if strings.Contains(strings.ToUpper(statement), "LOCK TABLE") {
			t.Fatalf("runtime log retention must not take a facet table lock:\n%s", statement)
		}
	}
	if !strings.Contains(runtimeLogRetentionLockTimeoutSQL, "SET LOCAL lock_timeout") {
		t.Fatal("lock timeout SQL is missing")
	}
	if !strings.Contains(runtimeLogRetentionStatementTimeoutSQL, "SET LOCAL statement_timeout") {
		t.Fatal("statement timeout SQL is missing")
	}
	if !strings.Contains(runtimeLogRetentionTryAdvisoryLockSQL, "pg_try_advisory_xact_lock") {
		t.Fatal("cleanup instances must use a non-blocking transaction advisory lock")
	}
	for _, want := range []string{"bucket_key = 'current'", "FOR UPDATE NOWAIT"} {
		if !strings.Contains(runtimeLogRetentionEarliestCountedSQL, want) {
			t.Fatalf("summary row lock SQL missing %q", want)
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
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        2,
	})
	if err != nil {
		t.Fatalf("cleanupRuntimeLogIndexBefore() error = %v", err)
	}
	if deleted != 2 || !tx.committed || tx.rolledBack {
		t.Fatalf("deleted=%d committed=%v rolledBack=%v", deleted, tx.committed, tx.rolledBack)
	}
	joined := strings.Join(tx.statements, "\n")
	for _, want := range []string{
		"SET LOCAL lock_timeout",
		"SELECT pg_try_advisory_xact_lock",
		"DELETE FROM juhe_dataset.runtime_logs",
		"FOR UPDATE NOWAIT",
		"UPDATE juhe_dataset.runtime_log_facet_summary",
		"UPDATE juhe_dataset.runtime_log_level_facets",
		"UPDATE juhe_dataset.runtime_log_event_facets",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("transaction statements missing %q:\n%s", want, joined)
		}
	}
	deleteIndex := strings.Index(joined, "DELETE FROM juhe_dataset.runtime_logs")
	summaryLockIndex := strings.Index(joined, "FOR UPDATE NOWAIT")
	if deleteIndex < 0 || summaryLockIndex < 0 || deleteIndex >= summaryLockIndex {
		t.Fatalf("runtime rows must be selected/deleted before taking the cross-runtime summary row lock:\n%s", joined)
	}
}

func TestCleanupRuntimeLogIndexBeforeFailsClosedWithoutExclusiveGoOwner(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{}
	beginCalls := 0
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx, beginCalls: &beginCalls}, port.RuntimeLogRetentionCleanupInput{
		CutoffISO: "2026-07-08T00:00:00.000Z",
		Limit:     2,
	})
	if deleted != 0 || !errors.Is(err, port.ErrRuntimeLogRetentionDeferred) {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	if beginCalls != 0 || len(tx.statements) != 0 || tx.committed || tx.rolledBack {
		t.Fatalf("fail-closed owner gate must run before opening a transaction: beginCalls=%d statements=%v committed=%v rolledBack=%v", beginCalls, tx.statements, tx.committed, tx.rolledBack)
	}
}

func TestCleanupRuntimeLogIndexBeforeDefersWhenAnotherGoCleanerOwnsAdvisoryLock(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{denyAdvisory: true}
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        2,
	})
	if deleted != 0 || !errors.Is(err, port.ErrRuntimeLogRetentionDeferred) {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("advisory contention must rollback: committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	if strings.Contains(strings.Join(tx.statements, "\n"), "DELETE FROM juhe_dataset.runtime_logs") {
		t.Fatal("advisory contention must not touch runtime log rows")
	}
}

func TestCleanupRuntimeLogIndexBeforeCommitsEmptyBatchWithoutLockingSummary(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{}
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        2,
	})
	if err != nil || deleted != 0 || !tx.committed || tx.rolledBack {
		t.Fatalf("deleted=%d error=%v committed=%v rolledBack=%v", deleted, err, tx.committed, tx.rolledBack)
	}
	if strings.Contains(strings.Join(tx.statements, "\n"), "FOR UPDATE NOWAIT") {
		t.Fatal("empty cleanup batch must not delay the Node importer on the summary row")
	}
}

func TestCleanupRuntimeLogIndexBeforeDefersWhenNodeWriterOwnsSummaryRow(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{
		deleted:    []runtimeLogRetentionDeletedRow{{time: "2026-07-01T00:00:00.000Z", level: "warn", event: "failed"}},
		summaryErr: &pgconn.PgError{Code: "55P03", Message: "could not obtain lock on row"},
	}
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        1,
	})
	if deleted != 0 || !errors.Is(err, port.ErrRuntimeLogRetentionDeferred) {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("summary contention must restore the provisional delete: committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	joined := strings.Join(tx.statements, "\n")
	if strings.Contains(joined, "UPDATE juhe_dataset.runtime_log_facet_summary") {
		t.Fatal("summary contention must not mutate facets")
	}
}

func TestCleanupRuntimeLogIndexBeforeRollsBackOnFacetFailure(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{
		earliest:     "2026-07-01T00:00:00.000Z",
		deleted:      []runtimeLogRetentionDeletedRow{{time: "2026-07-01T00:00:00.000Z", level: "warn", event: "failed"}},
		failContains: "UPDATE juhe_dataset.runtime_log_level_facets",
	}
	_, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        1,
	})
	if err == nil || !tx.rolledBack || tx.committed {
		t.Fatalf("error=%v committed=%v rolledBack=%v", err, tx.committed, tx.rolledBack)
	}
}

func TestCleanupRuntimeLogIndexBeforeDefersOnFacetLockTimeout(t *testing.T) {
	tx := &runtimeLogRetentionTxStub{
		earliest:     "2026-07-01T00:00:00.000Z",
		deleted:      []runtimeLogRetentionDeletedRow{{time: "2026-07-01T00:00:00.000Z", level: "warn", event: "failed"}},
		failContains: "UPDATE juhe_dataset.runtime_log_level_facets",
		failErr:      &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
	}
	deleted, err := cleanupRuntimeLogIndexBefore(context.Background(), runtimeLogRetentionBeginnerStub{tx: tx}, port.RuntimeLogRetentionCleanupInput{
		GoExclusiveIndexCleanupOwner: true,
		CutoffISO:                    "2026-07-08T00:00:00.000Z",
		Limit:                        1,
	})
	if deleted != 0 || !errors.Is(err, port.ErrRuntimeLogRetentionDeferred) {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("facet lock timeout must rollback: committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
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

type runtimeLogRetentionBeginnerStub struct {
	tx         *runtimeLogRetentionTxStub
	beginCalls *int
}

func (s runtimeLogRetentionBeginnerStub) BeginRuntimeLogRetentionTx(context.Context) (runtimeLogRetentionTx, error) {
	if s.beginCalls != nil {
		(*s.beginCalls)++
	}
	return s.tx, nil
}

type runtimeLogRetentionDeletedRow struct{ time, level, event string }

type runtimeLogRetentionTxStub struct {
	earliest     string
	deleted      []runtimeLogRetentionDeletedRow
	statements   []string
	failContains string
	failErr      error
	denyAdvisory bool
	summaryErr   error
	committed    bool
	rolledBack   bool
}

func (s *runtimeLogRetentionTxStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	if s.failContains != "" && strings.Contains(sql, s.failContains) {
		if s.failErr != nil {
			return pgconn.CommandTag{}, s.failErr
		}
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
	if strings.Contains(sql, "pg_try_advisory_xact_lock") {
		return runtimeLogRetentionRowStub{value: !s.denyAdvisory}
	}
	return runtimeLogRetentionRowStub{value: s.earliest, err: s.summaryErr}
}

func (s *runtimeLogRetentionTxStub) Commit(context.Context) error   { s.committed = true; return nil }
func (s *runtimeLogRetentionTxStub) Rollback(context.Context) error { s.rolledBack = true; return nil }

type runtimeLogRetentionRowStub struct {
	value any
	err   error
}

func (r runtimeLogRetentionRowStub) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	switch value := r.value.(type) {
	case bool:
		*(dest[0].(*bool)) = value
	case string:
		*(dest[0].(*string)) = value
	default:
		return errors.New("unsupported runtime log retention row stub value")
	}
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
