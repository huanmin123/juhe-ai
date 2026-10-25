package statsrebuild

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// 夹具对齐 statsagg 包测试形状：stats 库与业务库都用
// statsagg.OpenSQLiteTestDB（同形 DDL 替身；业务库只使用其中
// resource_authorizations / accounts / system_settings 最小表集）。
// usage_records 走 stats 库镜像形状（SQLite standalone 聚合事实源）。

type rebuildFixture struct {
	statsDB    *sql.DB
	businessDB *sql.DB
	now        time.Time
}

func (f *rebuildFixture) mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 %s 失败: %v", query, err)
	}
}

func (f *rebuildFixture) mustQueryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return value
}

// seedUsageRecord 插入一条最小形状 usage_records（可空维度留 NULL）。
func (f *rebuildFixture) seedUsageRecord(t *testing.T, id, systemAccountID string, success float64, costUsd float64, createdAt string) {
	t.Helper()
	f.mustExec(t, f.statsDB, `INSERT INTO usage_records (
		id, system_account_id, trace_id, traffic_source, success, cost_usd, created_at
	) VALUES (?, ?, ?, 'api', ?, ?, ?)`, id, systemAccountID, "trace-"+id, success, costUsd, createdAt)
}

func queryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return value
}

func (f *rebuildFixture) ensureJobsRuntimeTables(t *testing.T) {
	t.Helper()
	f.mustExec(t, f.statsDB, `CREATE TABLE IF NOT EXISTS background_task_runs (
		run_id TEXT PRIMARY KEY,
		job_name TEXT NOT NULL,
		job_type TEXT NOT NULL,
		worker_role TEXT NOT NULL,
		status TEXT NOT NULL,
		lease_key TEXT NOT NULL,
		owner_id TEXT,
		params_json TEXT NOT NULL DEFAULT '{}',
		result_json TEXT NOT NULL DEFAULT '{}',
		error_message TEXT,
		submitted_at TEXT NOT NULL,
		started_at TEXT,
		heartbeat_at TEXT,
		finished_at TEXT,
		duration_ms INTEGER,
		exit_code INTEGER,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	f.mustExec(t, f.statsDB, `CREATE TABLE IF NOT EXISTS background_job_leases (
		lease_key TEXT PRIMARY KEY,
		job_name TEXT NOT NULL,
		shard_key TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL,
		run_id TEXT,
		fencing_token INTEGER NOT NULL DEFAULT 0,
		lease_until TEXT NOT NULL,
		heartbeat_at TEXT NOT NULL,
		started_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
}

// seedJobsLease 插入一条租约行（时间戳 RFC3339 毫秒 UTC 文本，与
// taskruns.FormatInstant 同形）。
func (f *rebuildFixture) seedJobsLease(t *testing.T, leaseKey string, leaseUntil, heartbeatAt time.Time) {
	t.Helper()
	stamp := func(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000Z") }
	f.mustExec(t, f.statsDB, `INSERT INTO background_job_leases (
		lease_key, job_name, shard_key, owner_id, lease_until, heartbeat_at, started_at, updated_at
	) VALUES (?, 'usage-hot-window-refresh', '', 'owner-test', ?, ?, ?, ?)`,
		leaseKey, stamp(leaseUntil), stamp(heartbeatAt), stamp(heartbeatAt), stamp(heartbeatAt))
}

// seedTaskRunHeartbeat 插入一条已完成任务运行行（heartbeat_at 可控）。
func (f *rebuildFixture) seedTaskRunHeartbeat(t *testing.T, runID string, heartbeatAt time.Time) {
	t.Helper()
	stamp := func(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000Z") }
	f.mustExec(t, f.statsDB, `INSERT INTO background_task_runs (
		run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, heartbeat_at, created_at, updated_at
	) VALUES (?, 'usage-hot-window-refresh', 'snapshot', 'stats-worker', 'completed', 'lease-'+?, ?, ?, ?, ?)`,
		runID, runID, stamp(heartbeatAt.Add(-time.Second)), stamp(heartbeatAt), stamp(heartbeatAt.Add(-time.Second)), stamp(heartbeatAt))
}

// rebuildPoisonRow 向 usage_stats_totals 植入毒化行，用于断言拒绝路径零副作用
// （毒化行必须仍在）与放行路径真正清空（毒化行必须消失）。
func (f *rebuildFixture) rebuildPoisonRow(t *testing.T) {
	t.Helper()
	f.mustExec(t, f.statsDB, `INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, updated_at
	) VALUES ('poison', 'global', 'global', 999, '2026-01-01T00:00:00.000Z')`)
}

func TestPostgresStatementShape(t *testing.T) {
	pgDialect := statsagg.Dialect{}
	for _, table := range rebuildClearedStatsTables {
		statement := clearTableSQL(pgDialect, table)
		if !strings.HasPrefix(statement, "DELETE FROM juhe_stats."+table) {
			t.Fatalf("PG 清空语句缺少 juhe_stats 前缀: %s", statement)
		}
		if strings.Contains(statement, "juhe_business") || strings.Contains(statement, "usage_records") {
			t.Fatalf("PG 清空语句越界: %s", statement)
		}
	}
	reset := resetJobStateSQL(pgDialect)
	if !strings.Contains(reset, "DELETE FROM juhe_stats.stats_job_state") {
		t.Fatalf("PG 游标重置语句缺少 juhe_stats 前缀: %s", reset)
	}
	if !strings.Contains(reset, "$1, $2") {
		t.Fatalf("PG 游标重置语句应使用 $n 占位符: %s", reset)
	}
}

// TestRebuildSQLiteClearListClassifiesEveryStatsSchemaTable 是清表清单防漂移
// 守卫（边界审计 2026-10-06）：枚举 stats 测试库 schema 的全部表名，断言每张
// 表都可归入三类之一——rebuildClearedStatsTables（重建清空面）、
// completionClearedDirtyTables（完成阶段清理的脏队列）、
// rebuildUnclassifiedTableReasons（明确不在重建面，逐表附理由）。
// 统计管线新增派生结果表而忘记进清表清单时，重建会留下旧数据与新重放混写，
// 本守卫在测试阶段拦截；反向断言清表清单的每张表都真实存在于 schema 且与
// 不重建清单无交集，防止清单写错表名后 DELETE 静默少清。
