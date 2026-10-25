package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	"github.com/jackc/pgx/v5/stdlib"
)

// balanceFixtureSchema 建立余额探测依赖的冻结业务/统计表
// （仅测试所需列；生产 DDL 归 maintenance 项目）。accounts 为目标形态
// （§9.11：provider_code 已删，身份 = vendor_code（nullable；余额候选经
// Input.Validate 要求非空真实厂商）+ connection_driver_code）。
func balanceFixtureSchema(ctx context.Context, businessDB, statsDB *sql.DB) error {
	// PG 唯一形态：单库 schema 限定（business/stats 两个参数传入同一测试库；
	// 原 SQLite 双文件夹具随 sqlite 形态退役删除）。
	businessSchema := `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  vendor_code TEXT,
  connection_driver_code TEXT NOT NULL DEFAULT 'generic_http',
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  schedulable INTEGER NOT NULL DEFAULT 1,
  deleted_at TEXT,
  authorization_instance_authorization_id TEXT,
  dispatch_revision INTEGER NOT NULL DEFAULT 1,
  config_revision INTEGER NOT NULL DEFAULT 1,
  credentials_encrypted TEXT NOT NULL,
  proxy_profile_id TEXT,
  balance_query_enabled INTEGER NOT NULL DEFAULT 0,
  balance_query_config_json TEXT NOT NULL DEFAULT '{}',
  name TEXT NOT NULL DEFAULT '',
  protocol_code TEXT,
  protocol_version TEXT,
  balance_query_next_refresh_at TEXT,
  created_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS juhe_business.proxy_profiles (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  host TEXT,
  port INTEGER,
  username TEXT,
  password_encrypted TEXT,
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value_json TEXT,
  PRIMARY KEY (system_account_id, key)
);
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json)
VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"') ON CONFLICT DO NOTHING;
`
	statsSchema := `
CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE TABLE IF NOT EXISTS juhe_stats.account_usage_snapshots (
  system_account_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  source TEXT,
  snapshot_json TEXT,
  refresh_status TEXT,
  last_attempt_at TEXT,
  last_success_at TEXT,
  next_refresh_after TEXT,
  last_error_message TEXT,
  updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, account_id, kind)
);
`
	if _, err := businessDB.ExecContext(ctx, businessSchema); err != nil {
		return err
	}
	if _, err := statsDB.ExecContext(ctx, statsSchema); err != nil {
		return err
	}
	return nil
}
func seedBalanceDetectionAccount(t *testing.T, ctx context.Context, db *sql.DB, secret, baseURL string, dueOffset time.Duration) {
	t.Helper()
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-test-key", "base_url": baseURL})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(secret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(dueOffset).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.accounts (id, system_account_id, vendor_code, connection_driver_code, name, protocol_code, protocol_version, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, created_at, updated_at)
VALUES ('acc-detect-1', 'sys_admin', 'gpt', 'gpt', 'acc-detect-1', 'gpt', '1', 'api_key', 'active', 1, ?, 0, '{}', ?, ?, ?)
`, envelope, due, due, due); err != nil {
		t.Fatal(err)
	}
}

// wgBalanceAssemblyFixture 打开共享覆盖库（w14j 自愈 seed + 行卫生）供
// 余额装配全链测试使用：statsverify 契约表属外部预置（fresh 独立库 DDL
// 回填登记为 B6 遗留切片），装配全链沿用覆盖库（w16j 同惯例）。返回
// sqldialect 改写句柄（测试内保留 `?` 写法）与覆盖库 URL（装配 env 用）。
// 账户/快照行按调用方自有 ID 在前后双向清理，不触碰他人数据。
func wgBalanceAssemblyFixture(t *testing.T, accountIDs []string) (*sql.DB, string) {
	t.Helper()
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("balance: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("打开覆盖库失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("覆盖库不可达（跳过）: %v", err)
	}
	w14jEnsurePGFixture(t, db)
	wrapped, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrapped.Close() })
	wgBalanceCleanupRows(t, wrapped, accountIDs)
	t.Cleanup(func() { wgBalanceCleanupRows(t, wrapped, accountIDs) })
	return wrapped, pgURL
}

// wgBalanceCleanupRows 按账户 ID 清理测试自有行（先快照后账户，幂等）。
func wgBalanceCleanupRows(t *testing.T, db *sql.DB, accountIDs []string) {
	t.Helper()
	for _, id := range accountIDs {
		if _, err := db.Exec(`DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ?`, id); err != nil {
			t.Logf("清理 %s 快照行失败（继续）: %v", id, err)
		}
		if _, err := db.Exec(`DELETE FROM juhe_business.accounts WHERE id = ?`, id); err != nil {
			t.Logf("清理 %s 账户行失败（继续）: %v", id, err)
		}
	}
}

// balanceDetectAssemblyEnv 以隔离 PG 测试库 DSN 构造装配 env（原 sqlite
// 驱动与路径族 env 随 sqlite 形态退役删除；家族开关恒装配不变）。
func balanceDetectAssemblyEnv(t *testing.T, postgresURL string) map[string]string {
	t.Helper()
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":          postgresURL,
		"JUHE_AI_INSTANCE_ID":           "balance-detect-instance",
		"JUHE_AI_WORKER_ROLE":           "ops-worker",
		"JUHE_AI_SECRET":                "0123456789abcdef0123456789abcdef",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS": "2000",
	}
}

func TestWorkerBalanceDetectWiresEnabledOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	// PG 唯一形态：共享覆盖库 + w14j 自愈 seed（statsverify 契约表属外部
	// 预置，fresh 独立库 DDL 回填登记为 B6 遗留；原 sqlite 双文件夹具随
	// sqlite 形态退役删除）。
	businessDB, dsn := wgBalanceAssemblyFixture(t, []string{"acc-detect-1"})
	statsDB := businessDB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balance":"12.5"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	seedBalanceDetectionAccount(t, ctx, businessDB, "0123456789abcdef0123456789abcdef", upstream.URL, -time.Minute)

	env := balanceDetectAssemblyEnv(t, dsn)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if _, wired := assembly.wiredTasks["account-balance-auto-detect-recovery"]; !wired {
		t.Fatalf("余额自动探测任务必须接线：wired=%v disabled=%v", assembly.wiredJobs, assembly.disabledJobs)
	}
	result, err := assembly.runWiredJobOnce(ctx, "account-balance-auto-detect-recovery")
	if err != nil {
		t.Fatalf("runWiredJobOnce: %v", err)
	}
	if result.Outcome != "" && result.Outcome != "success" {
		t.Fatalf("任务结果必须为 success，得到 %s", result.Outcome)
	}

	var enabled int
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled FROM juhe_business.accounts WHERE id = 'acc-detect-1'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("探测命中后账户必须已开启余额查询")
	}
	var configText string
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_config_json FROM juhe_business.accounts WHERE id = 'acc-detect-1'`).Scan(&configText); err != nil {
		t.Fatal(err)
	}
	// Node normalizeAccountBalanceConfig 输出 camelCase 键，intervalMinutes=5。
	if !strings.Contains(configText, `"intervalMinutes":5`) || !strings.Contains(configText, `"adapter":"builtin"`) {
		t.Fatalf("配置 JSON 必须保持 Node camelCase 形状，得到 %s", configText)
	}
	var nextRefreshAt sql.NullString
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_next_refresh_at FROM juhe_business.accounts WHERE id = 'acc-detect-1'`).Scan(&nextRefreshAt); err != nil || !nextRefreshAt.Valid {
		t.Fatalf("开启后必须写入新的 next_refresh_at: %v %v", nextRefreshAt, err)
	}
	if due, err := time.Parse(time.RFC3339Nano, nextRefreshAt.String); err != nil || !due.After(time.Now().UTC()) {
		t.Fatalf("next_refresh_at 必须是未来时间: %s", nextRefreshAt.String)
	}

	var (
		refreshStatus string
		snapshotJSON  string
	)
	if err := statsDB.QueryRowContext(ctx, `SELECT refresh_status, snapshot_json FROM juhe_stats.account_usage_snapshots WHERE account_id = 'acc-detect-1' AND kind = 'relay_balance'`).Scan(&refreshStatus, &snapshotJSON); err != nil {
		t.Fatalf("relay_balance 快照行必须写入: %v", err)
	}
	if refreshStatus != "fresh" {
		t.Fatalf("快照状态必须为 fresh，得到 %s", refreshStatus)
	}
	var snapshotMap map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshotMap); err != nil {
		t.Fatal(err)
	}
	if snapshotMap["status"] != "fresh" {
		t.Fatalf("快照 JSON 状态错误: %v", snapshotMap["status"])
	}
	if snapshotMap["configRevision"] != float64(1) {
		t.Fatalf("快照 JSON 必须携带 configRevision 围栏: %v", snapshotMap["configRevision"])
	}
	if text, ok := snapshotMap["remainingUsd"].(string); !ok {
		t.Fatalf("快照 JSON 必须携带 Node remainingUsd 字段: %v", snapshotMap["remainingUsd"])
	} else {
		// J2 迁移实现的规范小数文本（12.5 规范化为 12.500000）。
		var value float64
		if _, err := fmt.Sscanf(text, "%g", &value); err != nil || value != 12.5 {
			t.Fatalf("remainingUsd 数值错误: %s", text)
		}
	}
}

// TestWorkerBalanceDetectUnsupportedClearsIntent：fake 上游全部适配器 404 →
// unsupported → 探测意图收口（next_refresh_at=NULL），不开启、不写快照。
func TestWorkerBalanceDetectUnsupportedClearsIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	// PG 唯一形态：共享覆盖库 + w14j 自愈 seed（statsverify 契约表属外部
	// 预置，fresh 独立库 DDL 回填登记为 B6 遗留；原 sqlite 双文件夹具随
	// sqlite 形态退役删除）。
	businessDB, dsn := wgBalanceAssemblyFixture(t, []string{"acc-detect-1"})
	statsDB := businessDB
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	seedBalanceDetectionAccount(t, ctx, businessDB, "0123456789abcdef0123456789abcdef", upstream.URL, -time.Minute)

	env := balanceDetectAssemblyEnv(t, dsn)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if _, err := assembly.runWiredJobOnce(ctx, "account-balance-auto-detect-recovery"); err != nil {
		t.Fatalf("runWiredJobOnce: %v", err)
	}

	var enabled int
	var nextRefreshAt sql.NullString
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled, balance_query_next_refresh_at FROM juhe_business.accounts WHERE id = 'acc-detect-1'`).Scan(&enabled, &nextRefreshAt); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatal("unsupported 结果不得开启余额查询")
	}
	if nextRefreshAt.Valid {
		t.Fatalf("unsupported 结果必须收口探测意图（NULL），得到 %s", nextRefreshAt.String)
	}
	var rows int
	if err := statsDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.account_usage_snapshots WHERE account_id = 'acc-detect-1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("unsupported 结果不得写入快照")
	}
}

// TestWorkerPartialJobsStartupLogExhaustive：所有尚未翻转 Wired 的 scheduled
// job 必须全部出现在缺口清单中（以 jobregistry 为准动态计数），且每条给出
// 命名缺口。
func TestWorkerPartialJobsStartupLogExhaustive(t *testing.T) {
	remaining := 0
	for _, entry := range jobregistry.ScheduledEntries() {
		// go-equivalent 任务由自有组件（J1/J2 等）接管，不属于本缺口清单。
		if entry.GoStatus == jobregistry.GoPartial {
			remaining++
		}
	}
	if len(workerPartialJobGaps) != remaining {
		t.Fatalf("缺口清单必须覆盖剩余 %d 个 scheduled job，得到 %d", remaining, len(workerPartialJobGaps))
	}
	for _, gap := range workerPartialJobGaps {
		entry, ok := jobregistry.Find(gap.JobName)
		if !ok {
			t.Fatalf("缺口条目 %s 不在注册表", gap.JobName)
		}
		if entry.GoStatus == jobregistry.GoWired {
			t.Fatalf("%s 已翻转 Wired，应从缺口清单移除", gap.JobName)
		}
		if len(gap.Reason) < 20 {
			t.Fatalf("%s 的缺口说明必须命名缺失适配器: %s", gap.JobName, gap.Reason)
		}
	}
}
