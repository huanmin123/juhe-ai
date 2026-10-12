package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	"github.com/jackc/pgx/v5/stdlib"
)

var w1PGDatabasePattern = regexp.MustCompile(`^juhe_ai_sub2api_dev_[a-z][a-z0-9_]{2,39}$`)

var w1StatsTables = []struct {
	table  string
	column string
}{
	{table: "usage_stats_totals"},
	{table: "usage_stats_minute", column: "stat_minute"},
	{table: "usage_stats_hourly", column: "stat_hour"},
	{table: "usage_stats_daily", column: "stat_date"},
	{table: "usage_stats_weekly", column: "stat_week"},
	{table: "usage_stats_monthly", column: "stat_month"},
}

const (
	w1PGUpdatedAt = "2026-10-12T00:00:00.000Z"
	w1PGCreatedAt = "2026-01-05T03:04:05.123Z"
)

type w1CostTuple struct {
	request, success, errors, total, successCost float64
}

type w1PGHarness struct {
	db    *sql.DB
	store *RecordCleanupStore
	keys  statsagg.UsageStatsTimeKeys
}

func openW1PGHarness(t *testing.T) *w1PGHarness {
	t.Helper()
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL"))
	if rawURL == "" {
		t.Skip("requires JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL (isolated PG)")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL URL: %s", redactW1PGError(err, rawURL))
	}
	databaseName, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, "/"))
	if err != nil || !w1PGDatabasePattern.MatchString(databaseName) {
		t.Fatalf("isolated PostgreSQL URL database name must match juhe_ai_sub2api_dev_* (got %q)", databaseName)
	}
	db, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), rawURL)
	if err != nil {
		t.Fatalf("open isolated PostgreSQL: %s", redactW1PGError(err, rawURL))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping isolated PostgreSQL: %s", redactW1PGError(err, rawURL))
	}
	var currentDatabase string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		t.Fatalf("verify current_database: %s", redactW1PGError(err, rawURL))
	}
	if currentDatabase != databaseName {
		t.Fatalf("current_database() = %q, want URL database %q", currentDatabase, databaseName)
	}
	keys, err := statsagg.UsageStatsTimeKeysFor(w1PGCreatedAt, time.FixedZone("W1PG", 8*3600))
	if err != nil {
		t.Fatalf("time keys: %v", err)
	}
	h := &w1PGHarness{db: db, keys: keys, store: &RecordCleanupStore{Stats: &DB{DB: db, Postgres: true}}}
	t.Cleanup(func() { h.deleteFixtures(t) })
	return h
}

func (h *w1PGHarness) deleteFixtures(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, item := range w1StatsTables {
		if _, err := h.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM juhe_stats.%s WHERE system_account_id LIKE $1", item.table), "subtract-fixture-%"); err != nil {
			t.Errorf("delete fixture rows from %s: %s", item.table, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
		}
	}
}

func (h *w1PGHarness) seed(t *testing.T, systemID, scopeID string, tuple w1CostTuple) {
	t.Helper()
	ctx := context.Background()
	for _, item := range w1StatsTables {
		columns := "system_account_id, scope_type, scope_id"
		values := "$1, 'account', $2"
		args := []any{systemID, scopeID}
		if item.column != "" {
			columns += ", " + item.column
			values += ", $3"
			args = append(args, h.w1TimeValue(item.column))
		}
		columns += ", request_count, success_count, error_count, total_cost_usd, success_cost_usd, updated_at"
		for _, value := range []any{int64(tuple.request), int64(tuple.success), int64(tuple.errors), tuple.total, tuple.successCost, w1PGUpdatedAt} {
			values += fmt.Sprintf(", $%d", len(args)+1)
			args = append(args, value)
		}
		query := fmt.Sprintf("INSERT INTO juhe_stats.%s (%s) VALUES (%s)", item.table, columns, values)
		if _, err := h.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %s: %s", item.table, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
		}
	}
}

func (h *w1PGHarness) w1TimeValue(column string) string {
	switch column {
	case "stat_minute":
		return h.keys.StatMinute
	case "stat_hour":
		return h.keys.StatHour
	case "stat_date":
		return h.keys.StatDate
	case "stat_week":
		return h.keys.StatWeek
	case "stat_month":
		return h.keys.StatMonth
	default:
		return ""
	}
}

func (h *w1PGHarness) assertTuple(t *testing.T, systemID, scopeID string, want w1CostTuple, deleted bool) {
	t.Helper()
	ctx := context.Background()
	for _, item := range w1StatsTables {
		query := fmt.Sprintf("SELECT request_count, success_count, error_count, total_cost_usd, success_cost_usd FROM juhe_stats.%s WHERE system_account_id=$1 AND scope_type='account' AND scope_id=$2", item.table)
		args := []any{systemID, scopeID}
		if item.column != "" {
			query += " AND " + item.column + "=$3"
			args = append(args, h.w1TimeValue(item.column))
		}
		var got w1CostTuple
		err := h.db.QueryRowContext(ctx, query, args...).Scan(&got.request, &got.success, &got.errors, &got.total, &got.successCost)
		if deleted {
			if err == nil {
				t.Fatalf("%s fixture row should be deleted, got %+v", item.table, got)
			}
			if err != sql.ErrNoRows {
				t.Fatalf("read deleted %s fixture: %s", item.table, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
			}
			continue
		}
		if err != nil {
			t.Fatalf("read %s fixture: %s", item.table, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
		}
		if got != want {
			t.Fatalf("%s tuple = %+v, want %+v", item.table, got, want)
		}
	}
}

func (h *w1PGHarness) apply(t *testing.T, systemID, scopeID string, subtract statsagg.UsageStatsAccumulator, initial, want w1CostTuple, deleted bool) {
	t.Helper()
	h.seed(t, systemID, scopeID, initial)
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin subtract transaction: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	entry := &postgresAggregatedStatsEntry{systemAccountID: systemID, scopeType: "account", scopeID: scopeID, accumulator: subtract}
	if err := h.store.subtractPostgresUsageStatsTotals(context.Background(), tx, []*postgresAggregatedStatsEntry{entry}, w1PGUpdatedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("subtract totals: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	for _, bucket := range usageStatsBucketDefs {
		timeEntry := &postgresAggregatedTimeEntry{bucket: bucket, timeValue: h.w1TimeValue(bucket.ColumnName), systemAccountID: systemID, scopeType: "account", scopeID: scopeID, accumulator: subtract}
		if err := h.store.subtractPostgresUsageStatsTimeBucket(context.Background(), tx, bucket, []*postgresAggregatedTimeEntry{timeEntry}, w1PGUpdatedAt); err != nil {
			_ = tx.Rollback()
			t.Fatalf("subtract %s: %s", bucket.TableName, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit subtract transaction: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	h.assertTuple(t, systemID, scopeID, want, deleted)
}

func TestW1PostgresSuccessCostCaseCorrectionAllBuckets(t *testing.T) {
	h := openW1PGHarness(t)
	subtract := statsagg.UsageStatsAccumulator{RequestCount: 1, ErrorCount: 1, TotalCostUsd: 2}
	h.apply(t, "subtract-fixture-case", "case-account", subtract, w1CostTuple{2, 1, 1, 10, 0}, w1CostTuple{1, 1, 0, 8, 8}, false)
}

func TestW1PostgresSuccessCostMixedErrorNoCorrection(t *testing.T) {
	h := openW1PGHarness(t)
	subtract := statsagg.UsageStatsAccumulator{RequestCount: 1, ErrorCount: 1, TotalCostUsd: 2}
	h.apply(t, "subtract-fixture-mixed", "mixed-account", subtract, w1CostTuple{3, 1, 2, 10, 0}, w1CostTuple{2, 1, 1, 8, 0}, false)
}

func TestW1PostgresSuccessCostSuccessDeleteZeroDeleteAndLegacyCorrection(t *testing.T) {
	h := openW1PGHarness(t)
	success := statsagg.UsageStatsAccumulator{RequestCount: 1, SuccessCount: 1, TotalCostUsd: 2, SuccessCostUsd: 2}
	// 正常成功行：总成本与成功成本一致，成功删除后都扣减 2。
	h.apply(t, "subtract-fixture-success", "success-account", success, w1CostTuple{3, 3, 0, 5, 5}, w1CostTuple{2, 2, 0, 3, 3}, false)
	// legacy error=0 错行：CASE 将扣减后的 success_cost_usd（1）补正为 total（3）。
	h.apply(t, "subtract-fixture-legacy", "legacy-account", success, w1CostTuple{3, 3, 0, 5, 3}, w1CostTuple{2, 2, 0, 3, 3}, false)
	// 仍有错误记录时不补正，仅按成功记录成本扣减。
	h.apply(t, "subtract-fixture-mixed-success", "mixed-success-account", success, w1CostTuple{3, 2, 1, 10, 6}, w1CostTuple{2, 1, 1, 8, 4}, false)
	h.apply(t, "subtract-fixture-zero", "zero-account", success, w1CostTuple{1, 1, 0, 2, 2}, w1CostTuple{}, true)
}

func TestW1PostgresSuccessCostRollbackRestoresFixture(t *testing.T) {
	h := openW1PGHarness(t)
	systemID, scopeID := "subtract-fixture-rollback", "rollback-account"
	initial := w1CostTuple{2, 1, 1, 10, 0}
	h.seed(t, systemID, scopeID, initial)
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin rollback transaction: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	entry := &postgresAggregatedStatsEntry{systemAccountID: systemID, scopeType: "account", scopeID: scopeID, accumulator: statsagg.UsageStatsAccumulator{RequestCount: 1, ErrorCount: 1, TotalCostUsd: 2}}
	if err := h.store.subtractPostgresUsageStatsTotals(context.Background(), tx, []*postgresAggregatedStatsEntry{entry}, w1PGUpdatedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("subtract before rollback: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	for _, bucket := range usageStatsBucketDefs {
		timeEntry := &postgresAggregatedTimeEntry{bucket: bucket, timeValue: h.w1TimeValue(bucket.ColumnName), systemAccountID: systemID, scopeType: "account", scopeID: scopeID, accumulator: entry.accumulator}
		if err := h.store.subtractPostgresUsageStatsTimeBucket(context.Background(), tx, bucket, []*postgresAggregatedTimeEntry{timeEntry}, w1PGUpdatedAt); err != nil {
			_ = tx.Rollback()
			t.Fatalf("subtract %s before rollback: %s", bucket.TableName, redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback subtract transaction: %s", redactW1PGError(err, os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL")))
	}
	h.assertTuple(t, systemID, scopeID, initial, false)
}

func redactW1PGError(err error, rawURL string) string {
	if err == nil {
		return "<nil>"
	}
	message := err.Error()
	if rawURL != "" {
		message = strings.ReplaceAll(message, rawURL, "<redacted-pg-url>")
		if parsed, parseErr := url.Parse(rawURL); parseErr == nil && parsed.User != nil {
			message = strings.ReplaceAll(message, parsed.User.String(), "<redacted-userinfo>")
			if password, ok := parsed.User.Password(); ok {
				message = strings.ReplaceAll(message, password, "<redacted-password>")
			}
		}
	}
	return strings.ReplaceAll(message, "password=", "password=<redacted>")
}
