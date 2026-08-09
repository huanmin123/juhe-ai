package tablemonitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	postgresTableMonitorSmokeURLVariable      = "JUHE_AI_TABLE_MONITOR_POSTGRES_SMOKE_URL"
	postgresTableMonitorSmokeRequiredVariable = "JUHE_AI_TABLE_MONITOR_POSTGRES_SMOKE_REQUIRED"
)

func TestPostgresTableMonitorAdapterSmoke(t *testing.T) {
	url := strings.TrimSpace(os.Getenv(postgresTableMonitorSmokeURLVariable))
	if url == "" {
		if postgresTableMonitorSmokeRequired(os.Getenv(postgresTableMonitorSmokeRequiredVariable)) {
			t.Fatalf("%s=true/1 时必须设置 %s", postgresTableMonitorSmokeRequiredVariable, postgresTableMonitorSmokeURLVariable)
		}
		t.Skipf("未设置 %s；PostgreSQL table-monitor adapter smoke 被显式跳过，不能视为 PostgreSQL 验证通过", postgresTableMonitorSmokeURLVariable)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: url})
	if err != nil {
		t.Fatalf("打开 PostgreSQL smoke store 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 PostgreSQL smoke store 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
		}
	})

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("连接 PostgreSQL smoke 数据库失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	prefix := postgresTableMonitorSmokePrefix(t)
	ownerA := prefix + "-owner-a"
	ownerB := prefix + "-owner-b"
	cleanupOwner := prefix + "-owner-cleanup"
	leaseA, acquired, err := store.AcquireOwnerLease(ctx, ownerA, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner A 必须获得 PostgreSQL lease: lease=%#v acquired=%t err=%s", leaseA, acquired, redactPostgresTableMonitorSmokeError(err, url))
	}
	assertPostgresTableMonitorTablesEmpty(t, ctx, store, url, false)
	cleanupLease := leaseA
	t.Cleanup(func() {
		cleanupPostgresTableMonitorSmoke(t, store, cleanupLease, cleanupOwner, url)
	})

	if _, acquired, err := store.AcquireOwnerLease(ctx, ownerB, time.Minute); err != nil || acquired {
		t.Fatalf("owner B 在 A 未过期时必须不能获得 lease: acquired=%t err=%s", acquired, redactPostgresTableMonitorSmokeError(err, url))
	}

	staleAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	retainedAt := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	if err := store.WriteSample(ctx, leaseA, postgresTableMonitorSmokeSample(prefix, staleAt, retainedAt)); err != nil {
		t.Fatalf("owner A WriteSample 写入 PostgreSQL snapshots 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}

	updated, err := store.db.ExecContext(ctx, `UPDATE juhe_stats.table_monitor_owner_leases
SET lease_until = $1, updated_at = $1
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = $2 AND fence_token = $3`, staleAt, leaseA.OwnerID, leaseA.FenceToken)
	if err != nil {
		t.Fatalf("使 owner A lease 过期失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("使 owner A lease 过期必须只更新一行，实际为 %d，错误为 %s", rows, redactPostgresTableMonitorSmokeError(err, url))
	}

	leaseB, acquired, err := store.AcquireOwnerLease(ctx, ownerB, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("过期后 owner B 必须获得 lease: lease=%#v acquired=%t err=%s", leaseB, acquired, redactPostgresTableMonitorSmokeError(err, url))
	}
	cleanupLease = leaseB
	if leaseB.FenceToken <= leaseA.FenceToken {
		t.Fatalf("owner B fence token 必须递增: A=%d B=%d", leaseA.FenceToken, leaseB.FenceToken)
	}

	if err := store.WriteSample(ctx, leaseA, postgresTableMonitorSmokeSample(prefix+"-stale-owner", staleAt, staleAt)); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("旧 owner A WriteSample 必须返回 ErrOwnerLeaseLost，实际为 %s", redactPostgresTableMonitorSmokeError(err, url))
	}

	deleted, err := store.Cleanup(ctx, leaseB, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil {
		t.Fatalf("owner B Cleanup 清理过期 snapshots 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if deleted != 2 {
		t.Fatalf("Cleanup 必须清理 1 条 database 和 1 条 table snapshot，实际删除 %d 条", deleted)
	}
	assertPostgresTableMonitorCounts(t, ctx, store, prefix, 1, 1, url)

	deleted, err = store.Cleanup(ctx, leaseB, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 100)
	if err != nil {
		t.Fatalf("owner B 最终 Cleanup 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if deleted != 2 {
		t.Fatalf("最终 Cleanup 必须清理保留的 1 条 database 和 1 条 table snapshot，实际删除 %d 条", deleted)
	}
	if err := store.ReleaseOwnerLease(ctx, leaseB); err != nil {
		t.Fatalf("释放 owner B lease 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	assertPostgresTableMonitorTablesEmpty(t, ctx, store, url, false)
	var ownerID string
	if err := store.db.QueryRowContext(ctx, `SELECT owner_id
FROM juhe_stats.table_monitor_owner_leases
WHERE lease_key = 'table-monitor-sampling-retention'`).Scan(&ownerID); err != nil {
		t.Fatalf("读取释放后的 owner lease 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if ownerID != "" {
		t.Fatalf("释放后的 owner lease owner_id 必须为空，实际为 %q", ownerID)
	}
	assertPostgresTableMonitorSamplerSmoke(t, ctx, store, url, prefix)
}

func postgresTableMonitorSmokeRequired(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.EqualFold(trimmed, "true") || trimmed == "1"
}

func postgresTableMonitorSmokePrefix(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("生成 PostgreSQL smoke 唯一标识失败: %v", err)
	}
	return "table-monitor-pg-smoke-" + hex.EncodeToString(raw)
}

func postgresTableMonitorSmokeSample(prefix string, staleAt, retainedAt time.Time) collectedSample {
	return collectedSample{
		databases: []DatabaseSnapshot{
			{Role: prefix + "-stale-db", Path: "postgres:smoke-stale", SampledAt: staleAt, TableCount: 1, IndexCount: 0},
			{Role: prefix + "-retained-db", Path: "postgres:smoke-retained", SampledAt: retainedAt, TableCount: 1, IndexCount: 0},
		},
		tables: []TableSnapshot{
			{Role: prefix + "-stale-table", TableName: "stale", SampledAt: staleAt, TableKind: "table", IndexCount: 0},
			{Role: prefix + "-retained-table", TableName: "retained", SampledAt: retainedAt, TableKind: "table", IndexCount: 0},
		},
	}
}

func assertPostgresTableMonitorTablesEmpty(t *testing.T, ctx context.Context, store *Store, url string, includeOwnerLease bool) {
	t.Helper()
	tables := []string{"database_storage_snapshots", "table_storage_snapshots"}
	if includeOwnerLease {
		tables = append(tables, "table_monitor_owner_leases")
	}
	for _, table := range tables {
		var count int
		if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM juhe_stats.%s", table)).Scan(&count); err != nil {
			t.Fatalf("统计 PostgreSQL smoke 表 juhe_stats.%s 失败: %s", table, redactPostgresTableMonitorSmokeError(err, url))
		}
		if count != 0 {
			t.Fatalf("PostgreSQL smoke 仅允许使用专用、空、可销毁数据库；juhe_stats.%s 当前有 %d 行", table, count)
		}
	}
}

func assertPostgresTableMonitorCounts(t *testing.T, ctx context.Context, store *Store, prefix string, wantDatabases, wantTables int, url string) {
	t.Helper()
	var databases, tables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.database_storage_snapshots WHERE database_role LIKE $1`, prefix+"%").Scan(&databases); err != nil {
		t.Fatalf("读取 PostgreSQL database snapshots 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.table_storage_snapshots WHERE database_role LIKE $1`, prefix+"%").Scan(&tables); err != nil {
		t.Fatalf("读取 PostgreSQL table snapshots 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if databases != wantDatabases || tables != wantTables {
		t.Fatalf("PostgreSQL snapshots 数量不正确: databases=%d tables=%d，期望 databases=%d tables=%d", databases, tables, wantDatabases, wantTables)
	}
}

func assertPostgresTableMonitorSamplerSmoke(t *testing.T, ctx context.Context, store *Store, url, prefix string) {
	t.Helper()
	for _, schema := range []string{"juhe_business", "juhe_dataset", "juhe_usage", "juhe_stats", "juhe_codex_context"} {
		if _, err := store.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema)); err != nil {
			t.Fatalf("创建 PostgreSQL 采样源 schema %s 失败: %s", schema, redactPostgresTableMonitorSmokeError(err, url))
		}
		if _, err := store.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.table_monitor_sampler_smoke (
  id BIGINT PRIMARY KEY,
  payload TEXT NOT NULL
)`, schema)); err != nil {
			t.Fatalf("创建 PostgreSQL 采样源表 %s 失败: %s", schema, redactPostgresTableMonitorSmokeError(err, url))
		}
		if _, err := store.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s.table_monitor_sampler_smoke (id, payload) VALUES (1, 'smoke') ON CONFLICT (id) DO NOTHING", schema)); err != nil {
			t.Fatalf("写入 PostgreSQL 采样源表 %s 失败: %s", schema, redactPostgresTableMonitorSmokeError(err, url))
		}
	}

	cfg := Config{
		InstanceID:           prefix + "-sampler-owner",
		OwnerLease:           time.Minute,
		Mode:                 ModePostgres,
		PostgresURL:          url,
		Interval:             time.Minute,
		RetentionDays:        30,
		MaxTables:            64,
		MaxConcurrentSources: 5,
		RetentionBatchSize:   1000,
		RetentionMaxBatches:  1000,
	}
	sampledAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var result SampleResult
	if err := RunWithOwnerLease(ctx, cfg, store, func(ownerCtx context.Context) error {
		var runErr error
		result, runErr = RunOnce(ownerCtx, cfg, store, sampledAt)
		return runErr
	}); err != nil {
		t.Fatalf("PostgreSQL RunOnce 采样 smoke 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if result.DatabaseSnapshots != 5 || result.TableSnapshots < 5 {
		t.Fatalf("PostgreSQL RunOnce 快照数量不符合预期: %+v", result)
	}

	var fileBytes, tableBytes, totalBytes int64
	var tableKind string
	if err := store.db.QueryRowContext(ctx, `SELECT database.file_bytes, table_snapshot.table_bytes, table_snapshot.total_bytes, table_snapshot.table_kind
FROM juhe_stats.database_storage_snapshots AS database
JOIN juhe_stats.table_storage_snapshots AS table_snapshot
  ON table_snapshot.database_role = database.database_role
 AND table_snapshot.sampled_at = database.sampled_at
WHERE database.database_role = 'business'
  AND table_snapshot.table_name = 'table_monitor_sampler_smoke'
  AND database.sampled_at = $1`, sampledAt).Scan(&fileBytes, &tableBytes, &totalBytes, &tableKind); err != nil {
		t.Fatalf("读取 PostgreSQL RunOnce 采样结果失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if fileBytes <= 0 || tableBytes <= 0 || totalBytes < tableBytes || tableKind != "table" {
		t.Fatalf("PostgreSQL RunOnce 采样字段不正确: file=%d table=%d total=%d kind=%q", fileBytes, tableBytes, totalBytes, tableKind)
	}
	var expectedSchemaBytes int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(pg_total_relation_size(c.oid)), 0)::bigint
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'juhe_business' AND c.relkind IN ('r', 'p', 'm')`).Scan(&expectedSchemaBytes); err != nil {
		t.Fatalf("计算 PostgreSQL schema 精确 relation-size 期望值失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
	if fileBytes != expectedSchemaBytes {
		t.Fatalf("PostgreSQL database file_bytes 必须汇总 pg_total_relation_size: got=%d want=%d", fileBytes, expectedSchemaBytes)
	}
}

func cleanupPostgresTableMonitorSmoke(t *testing.T, store *Store, lease OwnerLease, cleanupOwner, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := store.Cleanup(ctx, lease, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 1000); err != nil {
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Errorf("PostgreSQL smoke 清理写入数据失败: %s", redactPostgresTableMonitorSmokeError(err, url))
			return
		}
		var acquired bool
		var acquireErr error
		lease, acquired, acquireErr = store.AcquireOwnerLease(ctx, cleanupOwner, time.Minute)
		if acquireErr != nil || !acquired {
			t.Errorf("PostgreSQL smoke 清理前无法取得有效 owner lease: acquired=%t err=%s", acquired, redactPostgresTableMonitorSmokeError(acquireErr, url))
			return
		}
		if _, cleanupErr := store.Cleanup(ctx, lease, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 1000); cleanupErr != nil {
			t.Errorf("PostgreSQL smoke 使用清理 owner 删除数据失败: %s", redactPostgresTableMonitorSmokeError(cleanupErr, url))
		}
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Errorf("PostgreSQL smoke 释放清理 owner lease 失败: %s", redactPostgresTableMonitorSmokeError(err, url))
	}
}

func redactPostgresTableMonitorSmokeError(err error, url string) string {
	if err == nil {
		return "<nil>"
	}
	return strings.ReplaceAll(err.Error(), url, "[redacted PostgreSQL URL]")
}
