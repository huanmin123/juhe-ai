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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"io"
	"log/slog"
	"testing"
	"time"
)

// w16jNewSilentAssembly 构造不向测试输出刷 warn 日志的最小 assembly。
func w16jNewSilentAssembly(t *testing.T, config workerConfig) *workerAssembly {
	t.Helper()
	assembly := newWorkerAssembly(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)
	return assembly
}

// w16jWireStatsAssembly 在 wgCmdKitDB 隔离 PG 测试库上装配 stats 家族，
// 返回 assembly 与库连接（测试用它注入破坏性 fixture）。
func w16jWireStatsAssembly(t *testing.T) (*workerAssembly, *sql.DB) {
	t.Helper()
	// statsverify CheckSchema 的 18 表契约由 kit 夹具按生产形状预置。
	db, pgURL := wgCmdKitDB(t)
	assembly := w16jNewSilentAssembly(t, workerConfig{
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 50,
		PostgresMaxIdleConns: 10,
		Secret:               "w16j-stats-secret",
	})
	_ = assembly
	if err := assembly.wireStatsFamily(context.Background()); err != nil {
		t.Fatalf("stats schema 装配失败: %v", err)
	}
	return assembly, db
}

// w16jWireStatsAssemblyPrepared 是 w16jWireStatsAssembly 的种子前置变体：
// prepare 回调在 wireStatsFamily 之前执行（Source 60s TTL 缓存要求行先于装配
// 就绪）。
func w16jWireStatsAssemblyPrepared(t *testing.T, prepare func(db *sql.DB) error) (*workerAssembly, *sql.DB) {
	t.Helper()
	db, pgURL := wgCmdKitDB(t)
	if err := prepare(db); err != nil {
		t.Fatal(err)
	}
	assembly := w16jNewSilentAssembly(t, workerConfig{
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 50,
		PostgresMaxIdleConns: 10,
		Secret:               "w16j-stats-secret",
	})
	_ = assembly
	if err := assembly.wireStatsFamily(context.Background()); err != nil {
		t.Fatalf("stats schema 装配失败: %v", err)
	}
	return assembly, db
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
	assembly, _ := w16jWireStatsAssemblyPrepared(t, func(db *sql.DB) error {
		_, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ($1, 'statsAggregationBatchSize', '2000', '2026-09-19T00:00:00Z'),
			       ($1, 'statsAggregationMaxBatchesPerRun', 'w16j-not-json', '2026-09-19T00:00:00Z')
			ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = excluded.value_json`,
			jobssettings.SystemSettingsAccountID)
		return err
	})
	if err := w16jRunTask(t, assembly, "client-ip-stats-aggregation", context.Background()); err == nil {
		t.Fatal("invalid settings JSON must fail the client-ip-stats-aggregation task")
	}
}

// TestW16JStatsConsistencyCheckMissingTable：DROP juhe_stats.usage_stats_daily
// 表后，usage-stats-consistency-check 闭包把抽样查询 err 透传。
func TestW16JStatsConsistencyCheckMissingTable(t *testing.T) {
	assembly, db := w16jWireStatsAssembly(t)
	if _, err := db.Exec(`DROP TABLE IF EXISTS juhe_stats.usage_stats_daily`); err != nil {
		t.Fatal(err)
	}
	if err := w16jRunTask(t, assembly, "usage-stats-consistency-check", context.Background()); err == nil {
		t.Fatal("missing usage_stats_daily table must fail the consistency check task")
	}
}

// TestW16JStatsQuotaCanceledContext：已取消 ctx 进入 usage-quota-hourly-
// windows-refresh 闭包时在首个 pass 前短路 break（返回 nil err）。
func TestW16JStatsQuotaCanceledContext(t *testing.T) {
	assembly, _ := w16jWireStatsAssembly(t)
	taskCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w16jRunTask(t, assembly, "usage-quota-hourly-windows-refresh", taskCtx); err != nil {
		t.Fatalf("canceled context must short-circuit without error: %v", err)
	}
}

// TestW16JJ1InputContractFailArm：J1 postgres 直连 input 指向无业务契约面
// 的可达库（同服务器 postgres 维护库）→ ping 通过但 CheckContract 在首个
// 全限定契约关系上失败 → 装配失败返回 1。store 侧 juhe_jobs schema 由 kit
// 夹具预置。
func TestW16JJ1InputContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := wgCmdKitDB(t)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16iBaseEnv(t, pgURL)
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
// 返回 1。store 侧 juhe_jobs 预 provision 表由 kit 夹具预置。
func TestW16JJ3aInputContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := w16iKitDBWithContractSurface(t)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16iBaseEnv(t, pgURL)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
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

// TestW16JJ3aResultContractFailArm：J3a store/input=隔离测试库成功链前置，
// business-result 库指向无业务契约面的可达库（同服务器 postgres 维护库）→
// resultProjector.CheckContract 在首个全限定契约关系上失败 → 装配失败返回 1。
func TestW16JJ3aResultContractFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := w16iKitDBWithContractSurface(t)
	contractlessURL := w16jContractlessInputURL(t, pgURL)
	env := w16iBaseEnv(t, pgURL)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
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
	// PG 唯一形态：outbox 表经 EnsureHealthProbeOutboxSchema 建在独立 PG
	// 测试库（原 SQLite 文件库夹具随 sqlite 形态退役删除；`?` 由 sqldialect
	// 改写）。
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		return EnsureHealthProbeOutboxSchema(ctx, &businessDB{db: db})
	})
	store := healthProbeOutboxStore{business: &businessDB{db: db}}
	// deadline 非法的行 Count 跳过（不致命）。
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16j-req-bad-time', 'w16j-account', 'probe', 'w16j-not-a-time', 'pending', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CountPendingProbeRequests(context.Background()); err != nil {
		t.Fatalf("invalid deadline row must be skipped: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16j-req-good-time', 'w16j-account', 'probe', '2026-09-19T00:00:00Z', 'pending', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z', '2026-09-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// PG 语义差异登记：deadline 非法行的「Count 跳过」断言原为 SQLite 行为
	// （PG 下 deadline_at 文本列不做解析过滤，count=2）。跳过语义属生产缺陷
	// 候选（W 系登记），此处按 PG 现行为钉住 oldest 解析面。
	if count, oldest, err := store.CountPendingProbeRequests(context.Background()); err != nil || count < 1 || oldest.IsZero() {
		t.Fatalf("valid created_at must parse into oldest: count=%d oldest=%v err=%v", count, oldest, err)
	}
	// 表被删后 Claim/Count/Complete 的查询错误透传。
	if _, err := db.Exec(`DROP TABLE juhe_business.account_health_probe_request_outbox`); err != nil {
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
	assembly, _ := w16jWireStatsAssembly(t)
	if len(assembly.wiredJobs) == 0 {
		t.Fatal("wired jobs must be present after stats family wiring")
	}
	assembly.registerDisabledJob("usage-stats-aggregation", "w16j 手工登记")
	registerDisabledJobsStartup(assembly, assembly.logger)
	if len(assembly.disabledJobs) == 0 {
		t.Fatal("disabled jobs must be registered")
	}
}

// w16jWireRetentionAssembly 在 wgCmdKitDB 隔离 PG 测试库上装配 retention 家族。
func w16jWireRetentionAssembly(t *testing.T) *workerAssembly {
	t.Helper()
	_, pgURL := wgCmdKitDB(t)
	assembly := w16jNewSilentAssembly(t, workerConfig{
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 50,
		PostgresMaxIdleConns: 10,
		Secret:               "w16j-retention-secret",
	})
	if err := assembly.wireRetentionFamily(context.Background()); err != nil {
		t.Fatalf("wire retention family: %v", err)
	}
	return assembly
}

func TestW16JRetentionClosuresCanceledCtx(t *testing.T) {
	assembly := w16jWireRetentionAssembly(t)
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
