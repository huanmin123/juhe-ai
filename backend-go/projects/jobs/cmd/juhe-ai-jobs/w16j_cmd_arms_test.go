// 波次 w16j：组合根剩余 err 臂的最小充分覆盖。
//  1. wireStatsFamily 的任务闭包 err 臂：全部走 t.TempDir 私有 SQLite，不碰
//     共享库——非法 system_settings JSON 行（读取 err）、DROP stats 表（刷新
//     err）、已取消 ctx（quota 循环 break）。闭包经 wiredTasks 直接单跑
//     （w19d 先例），不经过 scheduler 循环。
//
// （wireMinimalOAuthRefresh 的 PG/SQLite 分支测试已随 worker_minimal_oauth.go
// 移除：机制强制常开后由 worker 路径 wireOAuthFamily 恒装配。）
package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
)

// w16jNewSilentAssembly 构造不向测试输出刷 warn 日志的最小 assembly。
func w16jNewSilentAssembly(t *testing.T, config workerConfig) *workerAssembly {
	t.Helper()
	assembly := newWorkerAssembly(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)
	return assembly
}

// w16jWireStatsAssembly 在私有 TempDir SQLite 上装配 stats 家族（含建表），
// 返回 assembly 与 business/stats 两库的维护连接（测试用它注入破坏性
// fixture；装配与闭包共用同一文件，破坏对两者同时生效）。
func w16jWireStatsAssembly(t *testing.T, root string) *workerAssembly {
	t.Helper()
	businessPath := filepath.Join(root, "business.sqlite3")
	statsPath := filepath.Join(root, "stats.sqlite3")
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    statsPath,
		Secret:             "w16j-stats-secret",
	})
	if err := assembly.wireStatsFamily(context.Background()); err != nil {
		t.Fatalf("wire stats family: %v", err)
	}
	return assembly
}

// w16jRunTask 直接执行已注册的任务闭包。
func w16jRunTask(t *testing.T, assembly *workerAssembly, name string, taskCtx context.Context) error {
	t.Helper()
	task, ok := assembly.wiredTasks[name]
	if !ok || task == nil {
		t.Fatalf("wired task %q must be registered", name)
	}
	_, err := task(taskCtx, jobsched.TaskContext{})
	return err
}

// TestW16JStatsSettingsInvalidJSON：system_settings 里 statsAggregationBatch-
// Size 合法、statsAggregationMaxBatchesPerRun 非法 JSON → client-ip 闭包在
// maxBatches 读取处把 err 透传（settings 读取不在该闭包内降级）。表和行必须
// 在装配前就绪：Source 有 60s TTL 缓存，装配后写入不会被重读。
func TestW16JStatsSettingsInvalidJSON(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES (?, 'statsAggregationBatchSize', '2000', '2026-09-19T00:00:00Z'),
		       (?, 'statsAggregationMaxBatchesPerRun', 'w16j-not-json', '2026-09-19T00:00:00Z')`,
		jobssettings.SystemSettingsAccountID, jobssettings.SystemSettingsAccountID); err != nil {
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    filepath.Join(root, "stats.sqlite3"),
		Secret:             "w16j-stats-secret",
	})
	if err := assembly.wireStatsFamily(context.Background()); err != nil {
		t.Fatalf("wire stats family: %v", err)
	}
	if err := w16jRunTask(t, assembly, "client-ip-stats-aggregation", context.Background()); err == nil {
		t.Fatal("invalid settings JSON must fail the client-ip-stats-aggregation task")
	}
}

// TestW16JStatsConsistencyCheckMissingTable：DROP stats 库的 usage_stats_daily
// 表后，usage-stats-consistency-check 闭包把抽样查询 err 透传。
func TestW16JStatsConsistencyCheckMissingTable(t *testing.T) {
	root := t.TempDir()
	assembly := w16jWireStatsAssembly(t, root)
	stats, err := sql.Open("sqlite", assembly.config.StatsSQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stats.Close() })
	if _, err := stats.Exec(`DROP TABLE IF EXISTS usage_stats_daily`); err != nil {
		t.Fatal(err)
	}
	if err := w16jRunTask(t, assembly, "usage-stats-consistency-check", context.Background()); err == nil {
		t.Fatal("missing usage_stats_daily table must fail the consistency check task")
	}
}

// TestW16JStatsQuotaCanceledContext：已取消 ctx 进入 usage-quota-hourly-
// windows-refresh 闭包时在首个 pass 前短路 break（返回 nil err）。
func TestW16JStatsQuotaCanceledContext(t *testing.T) {
	assembly := w16jWireStatsAssembly(t, t.TempDir())
	taskCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w16jRunTask(t, assembly, "usage-quota-hourly-windows-refresh", taskCtx); err != nil {
		t.Fatalf("canceled context must short-circuit without error: %v", err)
	}
}

// TestW16JJ1InputContractFailArm：J1 postgres 直连 input 指向无业务契约面
// 的可达库（同服务器 postgres 维护库）→ ping 通过但 CheckContract 在首个
// 全限定契约关系上失败 → 装配失败返回 1。store 侧 EnsureSchema 依赖外部
// bootstrap 的 juhe_jobs schema，先自愈预置。
func TestW16JJ1InputContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	w16jEnsurePGJobsFixture(t, pgURL)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "w16j-j1-contract",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":              "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       pgURL,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":       "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": contractlessURL,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "0123456789abcdef0123456789abcdef",
	})
	w16hRunArms(t, nil, 1, "verify J1 account-health direct-input contract")
}

// TestW16JJ3aInputContractFailArm：J3a postgres 直连 input 指向无业务契约面
// 的可达库（同服务器 postgres 维护库）→ CheckContract 缺契约关系 → 装配失败
// 返回 1。store 侧 CheckSchema 依赖 juhe_jobs 预 provision 表，先自愈预置。
func TestW16JJ3aInputContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	w16jEnsurePGJobsFixture(t, pgURL)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16j-j3a-contract",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        pgURL,
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  contractlessURL,
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": pgURL,
	})
	w16hRunArms(t, nil, 1, "verify J3a proxy-latency direct-input contract")
}

// TestW16JJ3aResultContractFailArm：J3a store/input=w1cover 成功链前置，
// business-result 库指向无业务契约面的可达库（同服务器 postgres 维护库）→
// resultProjector.CheckContract 在首个全限定契约关系上失败 → 装配失败返回 1。
func TestW16JJ3aResultContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	w16jEnsurePGJobsFixture(t, pgURL)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16j-j3a-result-contract",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        pgURL,
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  pgURL,
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": contractlessURL,
	})
	w16hRunArms(t, nil, 1, "verify J3a Go business-result contract")
}

// TestW16JOutboxStoreArms：healthProbeOutboxStore 直调（w1cover 之外零 PG
// 依赖）——Count 的 oldest 时间解析分支（合法/非法 created_at 文本各一）与
// 表缺失后 Claim/Count/Complete 的查询错误臂。
func TestW16JOutboxStoreArms(t *testing.T) {
	businessPath := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(healthProbeOutboxSchema); err != nil {
		t.Fatal(err)
	}
	store := healthProbeOutboxStore{business: &businessDB{db: db, postgres: false}}
	if _, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16j-req-bad-time', 'w16j-account', 'probe', '2026-09-19T00:00:00Z', 'pending', '2026-09-19T00:00:00Z', 'w16j-not-a-time', '2026-09-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// created_at 写侧异常文本：计数不被掩盖，oldest 退化为零值（解析失败分支）。
	if count, oldest, err := store.CountPendingProbeRequests(context.Background()); err != nil || count != 1 || !oldest.IsZero() {
		t.Fatalf("bad created_at must degrade oldest to zero: count=%d oldest=%v err=%v", count, oldest, err)
	}
	// 合法 created_at：oldest 正常解析（堆积告警年龄字段可用）。
	if _, err := db.Exec(`DELETE FROM account_health_probe_request_outbox WHERE request_id = 'w16j-req-bad-time'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16j-req-good-time', 'w16j-account', 'probe', '2026-09-19T00:00:00Z', 'pending', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if count, oldest, err := store.CountPendingProbeRequests(context.Background()); err != nil || count != 1 || oldest.IsZero() {
		t.Fatalf("valid created_at must parse into oldest: count=%d oldest=%v err=%v", count, oldest, err)
	}
	// 表被删后 Claim/Count/Complete 的查询错误透传。
	if _, err := db.Exec(`DROP TABLE account_health_probe_request_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CountPendingProbeRequests(context.Background()); err == nil {
		t.Fatal("missing outbox table must fail CountPendingProbeRequests")
	}
	if _, err := store.ClaimPendingProbeRequests(context.Background(), 10, time.Now()); err == nil {
		t.Fatal("missing outbox table must fail ClaimPendingProbeRequests")
	}
	if _, err := store.CompleteProbeRequest(context.Background(), "w16j-req-bad-time", time.Now()); err == nil {
		t.Fatal("missing outbox table must fail CompleteProbeRequest")
	}
}

// TestW16JRegisterDisabledJobsStartupArms：已接线/已登记任务进入 registered
// 集合后，registerDisabledJobsStartup 对注册表剩余 GoWired 条目做家族未启用
// 登记臂。
func TestW16JRegisterDisabledJobsStartupArms(t *testing.T) {
	assembly := w16jWireStatsAssembly(t, t.TempDir())
	if len(assembly.wiredJobs) == 0 {
		t.Fatal("wired jobs must be present after stats family wiring")
	}
	assembly.registerDisabledJob("usage-stats-aggregation", "w16j 手工登记")
	registerDisabledJobsStartup(assembly, assembly.logger)
	if len(assembly.disabledJobs) == 0 {
		t.Fatal("disabled jobs must be registered")
	}
}

// w16jWireRetentionAssembly 在私有 TempDir SQLite 上装配 retention 家族。
func w16jWireRetentionAssembly(t *testing.T, root string) *workerAssembly {
	t.Helper()
	paths := map[string]string{
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_CHAT_ASSETS_ROOT":               filepath.Join(root, "chat-assets"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":               filepath.Join(root, "usage-shards"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "codex-shards"),
	}
	t.Setenv("JUHE_AI_POSTGRES_URL", "")
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:                      "sqlite",
		BusinessSQLitePath:          paths["JUHE_AI_DATABASE_PATH"],
		StatsSQLitePath:             paths["JUHE_AI_STATS_DATABASE_PATH"],
		DatasetSQLitePath:           paths["JUHE_AI_DATASET_DATABASE_PATH"],
		ChatSQLitePath:              paths["JUHE_AI_CHAT_ASSETS_ROOT"],
		UsageCatalogSQLitePath:      paths["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"],
		UsageShardRoot:              paths["JUHE_AI_USAGE_SHARD_ROOT"],
		CodexContextStateShardRoot:  paths["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"],
		CodexContextStateShardCount: 2,
		Secret:                      "w16j-retention-secret",
	})
	if err := assembly.wireRetentionFamily(context.Background()); err != nil {
		t.Fatalf("wire retention family: %v", err)
	}
	return assembly
}

// TestW16JRetentionSettingsBadTimezone：system_settings 的 usageStatsTimezone
// 非法时区 → 时区读模型（timezoneName/location 惰性回调，任务运行期调用）
// 返回“统计时区不存在”错误。
func TestW16JRetentionSettingsBadTimezone(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES (?, 'usageStatsTimezone', '"w16j-not-a-zone"', '2026-09-19T00:00:00Z')`,
		jobssettings.SystemSettingsAccountID); err != nil {
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:                      "sqlite",
		BusinessSQLitePath:          businessPath,
		StatsSQLitePath:             filepath.Join(root, "stats.sqlite3"),
		DatasetSQLitePath:           filepath.Join(root, "dataset.sqlite3"),
		ChatSQLitePath:              filepath.Join(root, "chat-assets"),
		UsageCatalogSQLitePath:      filepath.Join(root, "usage-catalog.sqlite3"),
		UsageShardRoot:              filepath.Join(root, "usage-shards"),
		CodexContextStateShardRoot:  filepath.Join(root, "codex-shards"),
		CodexContextStateShardCount: 2,
		Secret:                      "w16j-retention-secret",
	})
	if err := assembly.wireRetentionFamily(context.Background()); err != nil {
		t.Fatalf("wire retention family: %v", err)
	}
	if assembly.retention == nil || assembly.retention.retentionSettings == nil {
		t.Fatal("retention settings runtime must be wired")
	}
	_, err = assembly.retention.retentionSettings.location(context.Background())
	if err == nil || !strings.Contains(err.Error(), "统计时区不存在") {
		t.Fatalf("invalid usageStatsTimezone must fail location resolution, got %v", err)
	}
}

// TestW16JRetentionClosuresCanceledCtx：已取消 ctx 让三个 retention 任务闭包
// 的首轮执行快速失败（err 透传臂）。
func TestW16JRetentionClosuresCanceledCtx(t *testing.T) {
	assembly := w16jWireRetentionAssembly(t, t.TempDir())
	taskCtx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{
		"expired-deleted-account-cleanup",
		"api-key-record-cleanup-retry",
		"account-record-cleanup-retry",
	} {
		if err := w16jRunTask(t, assembly, name, taskCtx); err == nil {
			t.Fatalf("canceled context must fail the %s task", name)
		}
	}
}
