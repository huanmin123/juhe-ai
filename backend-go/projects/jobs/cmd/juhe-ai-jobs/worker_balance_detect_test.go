package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// balanceFixtureSchema 建立余额探测依赖的冻结业务/统计表
// （仅测试所需列；生产 DDL 归 maintenance 项目）。
func balanceFixtureSchema(ctx context.Context, businessDB, statsDB *sql.DB) error {
	businessSchema := `
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  provider_code TEXT NOT NULL DEFAULT 'user-balance-provider',
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
  balance_query_next_refresh_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_profiles (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  host TEXT,
  port INTEGER,
  username TEXT,
  password_encrypted TEXT,
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS system_settings (
  system_account_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value_json TEXT,
  PRIMARY KEY (system_account_id, key)
);
INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"');
`
	if _, err := businessDB.ExecContext(ctx, businessSchema); err != nil {
		return err
	}
	statsSchema := `
CREATE TABLE IF NOT EXISTS account_usage_snapshots (
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
	if _, err := statsDB.ExecContext(ctx, statsSchema); err != nil {
		return err
	}
	return nil
}

// seedBalanceDetectionAccount 写入一个到期探测意图账户。
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
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at)
VALUES ('acc-detect-1', 'sys-1', 'api_key', 'active', 1, ?, 0, '{}', ?, ?)
`, envelope, due, due); err != nil {
		t.Fatal(err)
	}
}

func balanceDetectAssemblyEnv(t *testing.T, businessPath, statsPath string) map[string]string {
	t.Helper()
	return map[string]string{
		"JUHE_AI_JOBS_WORKER_ENABLED":          "true",
		"JUHE_AI_DATABASE_DRIVER":              "sqlite",
		"JUHE_AI_DATABASE_PATH":                businessPath,
		"JUHE_AI_STATS_DATABASE_PATH":          statsPath,
		"JUHE_AI_TASK_RUNS_DATABASE_PATH":      filepath.Join(filepath.Dir(businessPath), "task-runs.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":  filepath.Join(filepath.Dir(businessPath), "usage-catalog.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":             filepath.Join(filepath.Dir(businessPath), "usage-shards"),
		"JUHE_AI_INSTANCE_ID":                  "balance-detect-instance",
		"JUHE_AI_WORKER_ROLE":                  "ops-worker",
		"JUHE_AI_WORKER_REPLICA_INDEX":         "0",
		"JUHE_AI_SECRET":                       "0123456789abcdef0123456789abcdef",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS":        "2000",
		"JUHE_AI_DATASET_DATABASE_PATH":        filepath.Join(filepath.Dir(businessPath), "dataset.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":           filepath.Join(filepath.Dir(businessPath), "chat.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(filepath.Dir(businessPath), "codex-state"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "1",
		"JUHE_AI_JOBS_STATS_ENABLED":           "false",
		"JUHE_AI_JOBS_OAUTH_ENABLED":           "false",
		"JUHE_AI_JOBS_USAGE_WRITER_ENABLED":    "false",
	}
}

func mustOpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestWorkerBalanceDetectWiresEnabledOutcome：fake 上游命中 user_balance
// 适配器 → 跑一轮 → 断言账户开启、配置 JSON camelCase、快照行写入。
func TestWorkerBalanceDetectWiresEnabledOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	statsPath := filepath.Join(root, "stats.sqlite3")
	businessDB := mustOpenSQLite(t, businessPath)
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, statsPath)
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}

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

	env := balanceDetectAssemblyEnv(t, businessPath, statsPath)
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
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled FROM accounts WHERE id = 'acc-detect-1'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("探测命中后账户必须已开启余额查询")
	}
	var configText string
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_config_json FROM accounts WHERE id = 'acc-detect-1'`).Scan(&configText); err != nil {
		t.Fatal(err)
	}
	// Node normalizeAccountBalanceConfig 输出 camelCase 键，intervalMinutes=5。
	if !strings.Contains(configText, `"intervalMinutes":5`) || !strings.Contains(configText, `"adapter":"builtin"`) {
		t.Fatalf("配置 JSON 必须保持 Node camelCase 形状，得到 %s", configText)
	}
	var nextRefreshAt sql.NullString
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-detect-1'`).Scan(&nextRefreshAt); err != nil || !nextRefreshAt.Valid {
		t.Fatalf("开启后必须写入新的 next_refresh_at: %v %v", nextRefreshAt, err)
	}
	if due, err := time.Parse(time.RFC3339Nano, nextRefreshAt.String); err != nil || !due.After(time.Now().UTC()) {
		t.Fatalf("next_refresh_at 必须是未来时间: %s", nextRefreshAt.String)
	}

	var (
		refreshStatus  string
		snapshotJSON   string
	)
	if err := statsDB.QueryRowContext(ctx, `SELECT refresh_status, snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-detect-1' AND kind = 'relay_balance'`).Scan(&refreshStatus, &snapshotJSON); err != nil {
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
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	statsPath := filepath.Join(root, "stats.sqlite3")
	businessDB := mustOpenSQLite(t, businessPath)
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, statsPath)
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	seedBalanceDetectionAccount(t, ctx, businessDB, "0123456789abcdef0123456789abcdef", upstream.URL, -time.Minute)

	env := balanceDetectAssemblyEnv(t, businessPath, statsPath)
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
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled, balance_query_next_refresh_at FROM accounts WHERE id = 'acc-detect-1'`).Scan(&enabled, &nextRefreshAt); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatal("unsupported 结果不得开启余额查询")
	}
	if nextRefreshAt.Valid {
		t.Fatalf("unsupported 结果必须收口探测意图（NULL），得到 %s", nextRefreshAt.String)
	}
	var rows int
	if err := statsDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_snapshots WHERE account_id = 'acc-detect-1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("unsupported 结果不得写入快照")
	}
}

// TestWorkerBalanceDetectDisabledOnMissingContract：业务库缺 accounts 表 →
// 任务登记 disabled 且不进入调度循环，其他任务不受影响。
func TestWorkerBalanceDetectDisabledOnMissingContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	env := balanceDetectAssemblyEnv(t,
		filepath.Join(t.TempDir(), "business.sqlite3"),
		filepath.Join(t.TempDir(), "stats.sqlite3"))
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	for _, job := range assembly.wiredJobs {
		if job == "account-balance-auto-detect-recovery" {
			t.Fatal("契约缺失时余额自动探测任务不得接线")
		}
	}
	found := false
	for _, job := range assembly.disabledJobs {
		if job.JobName == "account-balance-auto-detect-recovery" && strings.Contains(job.Reason, "契约校验失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须登记 disabled 并说明缺口: %+v", assembly.disabledJobs)
	}
	// 其他任务不受影响：task-run-reconcile 仍然接线。
	reconcileWired := false
	for _, job := range assembly.wiredJobs {
		if job == "background-task-run-reconcile" {
			reconcileWired = true
		}
	}
	if !reconcileWired {
		t.Fatal("其他任务不得被牵连")
	}
	// 状态载荷必须可序列化且包含 disabled 清单。
	status := assembly.statusPayload()
	if _, err := json.Marshal(status); err != nil {
		t.Fatalf("status payload 必须可序列化: %v", err)
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

// TestWorkerSmokeRunsMultipleJobsConcurrently：多 job 并跑冒烟——调度循环
// 运行的同时，并发直跑 3 个已注册任务（reconcile / usage-stats-aggregation /
// account-balance-auto-detect-recovery），全部成功且停机排空干净。
func TestWorkerSmokeRunsMultipleJobsConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}
	ctx := context.Background()
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	statsPath := filepath.Join(root, "stats.sqlite3")
	businessDB := mustOpenSQLite(t, businessPath)
	statsDB := mustOpenSQLite(t, statsPath)
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	// usage-stats-aggregation 需要 stats 库的冻结聚合 schema；此导出 schema
	// 是 statsagg 文档化的测试专用引导（禁止生产初始化）。
	for _, statement := range statsagg.SQLiteTestSchema {
		if _, err := statsDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("应用 statsagg 测试 schema 失败: %v", err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			_, _ = w.Write([]byte(`{"balance":"3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	seedBalanceDetectionAccount(t, ctx, businessDB, "0123456789abcdef0123456789abcdef", upstream.URL, -time.Second)
	businessDB.Close()
	statsDB.Close()

	env := workerSmokeTestEnv(t)
	env["JUHE_AI_DATABASE_PATH"] = businessPath
	env["JUHE_AI_STATS_DATABASE_PATH"] = statsPath
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if len(assembly.wiredJobs) < 17 {
		t.Fatalf("至少应接线 17 个 GoWired 任务（17 基线 + balance），得到 %d", len(assembly.wiredJobs))
	}

	components := assembly.components()
	runCtx, cancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() { runErr <- components[0].Run(runCtx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !assembly.ready() {
		time.Sleep(2 * time.Millisecond)
	}
	if !assembly.ready() {
		t.Fatal("调度循环必须就绪")
	}

	var wg sync.WaitGroup
	errs := make([]error, 3)
	names := []string{
		"background-task-run-reconcile",
		"usage-stats-aggregation",
		"account-balance-auto-detect-recovery",
	}
	for index, name := range names {
		wg.Add(1)
		go func(slot int, jobName string) {
			defer wg.Done()
			_, err := assembly.runWiredJobOnce(ctx, jobName)
			errs[slot] = err
		}(index, name)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发执行 %s 失败: %v", names[index], err)
		}
	}
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("调度循环退出错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("调度循环未按时停机")
	}
}
