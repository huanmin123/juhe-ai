package statsverify

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// test helpers ---------------------------------------------------------------

// openTestStore opens a Go-owned SQLite store pair inside a temp directory
// with the usageStatsTimezone setting pre-seeded (default UTC).
func openTestStore(t *testing.T, timezone string) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(StoreConfig{
		Mode:               StoreSQLite,
		SQLiteStatsPath:    filepath.Join(dir, "stats.db"),
		SQLiteBusinessPath: filepath.Join(dir, "business.db"),
	})
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.business.ExecContext(ctx,
		`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', ?)`,
		mustJSONString(timezone)); err != nil {
		t.Fatalf("seed timezone: %v", err)
	}
	return store
}

func mustJSONString(value string) string {
	return `"` + value + `"`
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// insertUsageRecord seeds one usage_records row with defaults mirroring the
// Node UsageStatsRecordRow shape.
func insertUsageRecord(t *testing.T, ctx context.Context, store *Store, row UsageStatsRecordRow) {
	t.Helper()
	clientIP := nullString(row.ClientIP)
	accountID := nullString(row.AccountID)
	success := row.Success
	firstTokenMs := nullInt(row.FirstTokenMs)
	durationMs := nullInt(row.DurationMs)
	inputTokens := nullInt(row.InputTokens)
	outputTokens := nullInt(row.OutputTokens)
	cacheReadTokens := nullInt(row.CacheReadTokens)
	cacheWriteTokens := nullInt(row.CacheWriteTokens)
	cacheWrite1hTokens := nullInt(row.CacheWrite1hTokens)
	thinkingTokens := nullInt(row.ThinkingTokens)
	inputImageTokens := nullInt(row.InputImageTokens)
	outputImageTokens := nullInt(row.OutputImageTokens)
	cacheReadCost := nullFloat(row.CacheReadCostUsd)
	cacheWriteCost := nullFloat(row.CacheWriteCostUsd)
	costUsd := nullFloat(row.CostUsd)
	trafficSource := row.TrafficSource
	if trafficSource == "" {
		trafficSource = "gateway"
	}
	if row.ID == "" {
		t.Fatal("usage record requires explicit ID")
	}
	if row.CreatedAt == "" {
		t.Fatal("usage record requires created_at")
	}
	mustExec(t, ctx, store.db, `
		INSERT INTO usage_records (
		  id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id, account_id, model,
		  status_code, success, first_token_ms, duration_ms,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, cost_usd, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.ID, row.SystemAccountID, row.SystemAccountID+"-trace", trafficSource, clientIP, nil, nil, accountID, nil,
		nil, success, firstTokenMs, durationMs,
		inputTokens, outputTokens, cacheReadTokens, cacheReadCost,
		cacheWriteTokens, cacheWrite1hTokens, cacheWriteCost,
		thinkingTokens, inputImageTokens, outputImageTokens, costUsd, row.CreatedAt)
}

func nullString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPtr(value int) *int         { return &value }
func f64Ptr(value float64) *float64 { return &value }
func strPtr(value string) *string   { return &value }

// queryInt runs a scalar integer query and fails the test on error.
func queryInt(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

// fixedUTC builds a FixedClock at the given RFC3339 instant.
func fixedUTC(t *testing.T, value string) *FixedClock {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return NewFixedClock(parsed.UTC())
}
