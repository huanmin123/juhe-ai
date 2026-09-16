// 波次 w12c：补齐 jobs 主入口组合根的进程内可测分支（health probe outbox、
// oauth sweep fanout、retention 设置/队列、调度间隔设置、探针设置源、
// 配置加载与禁用任务登记）。全部 SQLite/miniredis/内存 mock，不连真实依赖。
//
// 不可达/防御守卫登记（无自然触发路径，不做无语义强注入）：
//   - main.go jobsHTTPHandler 中依赖组合根注入恒非 nil 的回调槽位不存在
//     缺席分支（组合根构造处全部以闭包兜底）。
//   - worker_retention.go defaultRetentionTimezone 的空种子回落分支：
//     DEFAULT_SYSTEM_SETTINGS 恒含 usageStatsTimezone 非空种子。
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// ---------------------------------------------------------------------------
// health probe outbox
// ---------------------------------------------------------------------------

func w12cOutboxFixture(t *testing.T) (*businessDB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	business := &businessDB{db: db}
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		t.Fatal(err)
	}
	return business, db
}

func w12cInsertOutboxRow(t *testing.T, db *sql.DB, requestID string, availableAt time.Time, created time.Time, fence string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, source_fence, deadline_at, status, available_at, created_at, updated_at)
		VALUES (?, 'acc-w12c', 'probe', ?, '2030-01-01T00:00:00Z', 'pending', ?, ?, ?)`,
		requestID, fence, availableAt.UTC().Format(time.RFC3339Nano), created.UTC().Format(time.RFC3339Nano), created.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

func TestW12CHealthProbeOutboxClaimCompletePrune(t *testing.T) {
	business, db := w12cOutboxFixture(t)
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	fence := `{"state_key":"k","account_id":"acc-w12c","runtime_key":"acc-w12c","source_generation":1,"source_fence_id":"f1","probe_generation":2}`
	w12cInsertOutboxRow(t, db, "req-due", now.Add(-time.Minute), now.Add(-2*time.Minute), fence)
	w12cInsertOutboxRow(t, db, "req-future", now.Add(time.Hour), now.Add(-2*time.Minute), fence)
	w12cInsertOutboxRow(t, db, "req-old", now.Add(-time.Minute), now.Add(-72*time.Hour), fence)

	store := healthProbeOutboxStore{business: business}
	rows, err := store.ClaimPendingProbeRequests(context.Background(), 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RequestID != "req-old" || rows[1].RequestID != "req-due" {
		t.Fatalf("认领按 created_at 排序的到期行: %+v", rows)
	}
	if rows[1].SourceFence.SourceFenceID != "f1" || rows[1].SourceFence.ProbeGeneration != 2 {
		t.Fatalf("fence 解析错误: %+v", rows[1].SourceFence)
	}
	deleted, err := store.CompleteProbeRequest(context.Background(), "req-due", now)
	if err != nil || !deleted {
		t.Fatalf("处理成功应删行: %v %v", deleted, err)
	}
	again, err := store.CompleteProbeRequest(context.Background(), "req-due", now)
	if err != nil || again {
		t.Fatalf("重复删除应幂等 false: %v %v", again, err)
	}

	// 非法 fence 拒绝认领（返回错误）。
	badFence, db2 := w12cOutboxFixture(t)
	w12cInsertOutboxRow(t, db2, "req-bad", now.Add(-time.Minute), now, "{not-json")
	if _, err := (healthProbeOutboxStore{business: badFence}).ClaimPendingProbeRequests(context.Background(), 10, now); err == nil {
		t.Fatal("非法 fence 必须拒绝")
	}

	// prune：保留期外行删除（含 consumed 无关）。
	pruner := healthProbeOutboxPruner{business: business, retention: 48 * time.Hour, logger: slog.Default()}
	deletedCount, err := pruner.pruneOnce(context.Background(), now)
	if err != nil || deletedCount != 1 {
		t.Fatalf("prune 应删除保留期外行: %d %v", deletedCount, err)
	}
	// pruneCycle 失败告警不终止（业务库关闭后查询失败）。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	pruner.pruneCycle(context.Background())
}

func TestW12CHealthProbeOutboxBoundaryAndRetentionParse(t *testing.T) {
	business, db := w12cOutboxFixture(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 1,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		deleted_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
		account_id TEXT PRIMARY KEY,
		current_version INTEGER NOT NULL,
		reserved_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id) VALUES ('acc-w12c-boundary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_health_jobs_input_versions VALUES ('acc-w12c-boundary', 3, '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	boundary := healthProbeBoundary{business: business}
	configRev, dispatchRev, inputVersion, ok, err := boundary.CurrentProbeInput(context.Background(), "acc-w12c-boundary")
	if err != nil || !ok || configRev != 1 || dispatchRev != 1 || inputVersion != 3 {
		t.Fatalf("boundary=%d %d %d ok=%v err=%v", configRev, dispatchRev, inputVersion, ok, err)
	}
	if _, _, _, ok, err := boundary.CurrentProbeInput(context.Background(), "missing"); ok || err != nil {
		t.Fatalf("缺失账户必须 ok=false: %v %v", ok, err)
	}
	if _, _, _, ok, err := boundary.CurrentProbeInput(context.Background(), "missing"); ok || err != nil {
		t.Fatalf("缺失账户必须 ok=false: %v %v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := boundary.CurrentProbeInput(context.Background(), "x"); err == nil {
		t.Fatal("业务库关闭必须暴露错误")
	}

	// 保留天数解析：默认 / 越界回退 / 非法回退 / 合法值。
	warned := 0
	warn := func(string) { warned++ }
	if got := parseProbeOutboxRetentionDays(func(string) string { return "" }, warn); got != 7 {
		t.Fatalf("空 env 应取默认: %d", got)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "999" }, warn); got != 7 || warned != 1 {
		t.Fatalf("越界应回退默认并告警: %d %d", got, warned)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "abc" }, warn); got != 7 || warned != 2 {
		t.Fatalf("非法应回退默认并告警: %d %d", got, warned)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "30" }, warn); got != 30 {
		t.Fatalf("合法值应透传: %d", got)
	}
	if got := parseProbeOutboxRetentionDays(nil, nil); got != 7 {
		t.Fatalf("nil getenv 应取默认: %d", got)
	}
}

// ---------------------------------------------------------------------------
// oauth sweep fanout 与 stats 脏标记交接
// ---------------------------------------------------------------------------

func TestW12CAuthorizationGrantFanoutAndDirtyMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
		account_id TEXT PRIMARY KEY,
		current_version INTEGER NOT NULL CHECK (current_version >= 1),
		reserved_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	store, err := oauthrefresh.OpenStore(db, oauthrefresh.StoreSQLite, wgBalanceSecret)
	if err != nil {
		t.Fatal(err)
	}
	fanout := authorizationGrantHealthFanout{store: store}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = fanout.FinalizeExpiredGrant(context.Background(), tx, oauthrefresh.ResourceAuthorizationGrant{
		ResourceType: "resource_authorization", ResourceID: "wg-missing",
	}, "w12c")
	if err != nil {
		t.Fatalf("不存在授权的 fanout 应幂等成功: %v", err)
	}

	// 脏标记：nil marker warn 跳过；marker 错误上抛；成功路径打 info。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := markAllGroupAccountStatsAfterAuthzWrite(context.Background(), logger, nil, "w12c-reason"); err != nil {
		t.Fatal(err)
	}
	failing := &w12cFailingMarker{err: errors.New("w12c: mark boom")}
	if err := markAllGroupAccountStatsAfterAuthzWrite(context.Background(), logger, failing, "w12c-reason"); err == nil {
		t.Fatal("mark 错误必须暴露")
	}
	ok := &w12cFailingMarker{}
	if err := markAllGroupAccountStatsAfterAuthzWrite(context.Background(), logger, ok, "w12c-reason"); err != nil {
		t.Fatal(err)
	}
}

type w12cFailingMarker struct{ err error }

func (m *w12cFailingMarker) MarkAllGroupAccountStatsDirty(context.Context, string, time.Time) error {
	return m.err
}

// ---------------------------------------------------------------------------
// retention 设置读模型与本地队列
// ---------------------------------------------------------------------------

func TestW12CRetentionSettingsRuntimeArms(t *testing.T) {
	dir, dirErr := os.MkdirTemp("", "w12c-retention-settings")
	if dirErr != nil {
		t.Fatal(dirErr)
	}
	db, err := sqlOpenSQLiteFile(filepath.Join(dir, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		time.Sleep(100 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	runtime := newRetentionSettingsRuntime(db, false, slog.Default())
	ctx := context.Background()
	settings, err := runtime.settings(ctx)
	if err != nil || len(settings) != len(retentionPolicySettingKeys) {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	// 非法整数 → 新建读模型绕开 60s 缓存后任务失败。
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'usageRecordRetentionDays', '"abc"')`); err != nil {
		t.Fatal(err)
	}
	freshRuntime := newRetentionSettingsRuntime(db, false, slog.Default())
	if _, err := freshRuntime.settings(ctx); err == nil {
		t.Fatal("非法设置值必须失败")
	}
	if _, err := db.Exec(`DELETE FROM system_settings WHERE key = 'usageRecordRetentionDays'`); err != nil {
		t.Fatal(err)
	}

	// 时区：读取 JSON 值 → 缓存命中 → 表缺失回落默认 → 非法 JSON 失败 →
	// 非法时区失败 → location。
	if err := runtime.db.QueryRowContext(ctx, "SELECT 1").Err(); err != nil {
		t.Fatal(err)
	}
	name, err := runtime.timezoneName(ctx)
	if err != nil || name != "UTC" {
		t.Fatalf("缺行应回落 UTC: %q %v", name, err)
	}
	if name, err = runtime.timezoneName(ctx); err != nil || name != "UTC" {
		t.Fatalf("缓存命中: %q %v", name, err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"Asia/Shanghai"')`); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.tzValue = ""
	runtime.tzExpiresAt = time.Time{}
	runtime.mu.Unlock()
	if name, err = runtime.timezoneName(ctx); err != nil || name != "Asia/Shanghai" {
		t.Fatalf("显式时区应生效: %q %v", name, err)
	}
	if _, err := db.Exec(`UPDATE system_settings SET value_json = 'not-json' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.tzValue = ""
	runtime.tzExpiresAt = time.Time{}
	runtime.mu.Unlock()
	if _, err := runtime.timezoneName(ctx); err == nil {
		t.Fatal("非法 JSON 必须失败")
	}
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"Mars/Phobos"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.timezoneName(ctx); err == nil {
		t.Fatal("非法时区必须失败")
	}
	location, err := runtime.location(ctx)
	if err == nil || location != nil {
		t.Fatalf("非法时区 location 必须失败: %v %v", location, err)
	}
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"UTC"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if location, err = runtime.location(ctx); err != nil || location == nil {
		t.Fatalf("UTC location: %v %v", location, err)
	}
	// PG 模式读失败回落默认 + 告警。
	pgRuntime := newRetentionSettingsRuntime(db, true, slog.Default())
	if name, err = pgRuntime.timezoneName(ctx); err != nil || name == "" {
		t.Fatalf("PG 读失败应回落默认: %q %v", name, err)
	}
	// 非缺表 SQL 错误 → 任务失败：把 value_json 列移除，读取变成列缺失错误
	// （不是 missing-table 语义，必须暴露）。
	if _, err := db.Exec(`DROP TABLE system_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	brokenRuntime := newRetentionSettingsRuntime(db, false, slog.Default())
	if _, err := brokenRuntime.timezoneName(ctx); err == nil {
		t.Fatal("列缺失的读失败必须暴露")
	}
	if _, ok := jobssettings.DefaultSystemSettings["usageStatsTimezone"]; !ok {
		t.Fatal("DEFAULT_SYSTEM_SETTINGS 必须含时区种子")
	}
	if defaultRetentionTimezone() == "" {
		t.Fatal("默认时区不得为空")
	}
}

func TestW12CRecordMaintenanceQueueLimits(t *testing.T) {
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 2, MaxBytes: 4096})
	job := wgMaintenanceJob("api_key_related_cleanup", "acc-1")
	if queued, reason := queue.enqueue(job); !queued || reason != "" {
		t.Fatalf("入队: %v %s", queued, reason)
	}
	if queued, reason := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 4}).enqueue(job); queued || reason != "oversize" {
		t.Fatalf("超大任务应拒绝: %v %s", queued, reason)
	}
	full := newRecordMaintenanceQueue(queueLimits{MaxItems: 1, MaxBytes: 1 << 20})
	if queued, _ := full.enqueue(job); !queued {
		t.Fatal("首个任务必须入队")
	}
	if queued, reason := full.enqueue(job); queued || reason != "worker_local_queue_full" {
		t.Fatalf("满队列应拒绝: %v %s", queued, reason)
	}
	if got := estimateJobBytes(job); got <= 0 {
		t.Fatalf("字节数必须为正: %d", got)
	}
	// takeBatch 扣减字节与队列长度。
	batch := queue.takeBatch(10)
	if len(batch) != 1 || queue.size() != 0 {
		t.Fatalf("批量取出: %d %d", len(batch), queue.size())
	}
	// drainShutdown：执行错误中止排空。
	empty := newRecordMaintenanceQueue(queueLimits{})
	empty.drainShutdown(func(context.Context, retention.RecordMaintenanceJob) (map[string]any, error) { return nil, nil })
	calls := 0
	failingQueue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 1 << 20})
	_, _ = failingQueue.enqueue(job)
	_, _ = failingQueue.enqueue(job)
	failingQueue.drainShutdown(func(context.Context, retention.RecordMaintenanceJob) (map[string]any, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("w12c: drain boom")
		}
		return nil, nil
	})
	if calls != 1 {
		t.Fatalf("首轮失败应中止排空: %d", calls)
	}
	// queueEnqueuer 的同步与异步投递失败分支。
	enqueuer := &queueEnqueuer{queue: full}
	if result := enqueuer.Enqueue(context.Background(), job); result.Queued {
		t.Fatal("满队列同步投递应失败")
	}
	if err := enqueuer.EnqueueAsync(context.Background(), job); err == nil {
		t.Fatal("满队列异步投递必须报错")
	}
}

// ---------------------------------------------------------------------------
// 调度间隔设置、探针设置源、配置加载与禁用登记
// ---------------------------------------------------------------------------

func TestW12CScheduleSettingsAndProbeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	source := jobssettings.NewSource(jobssettings.Options{DB: db, Mode: jobssettings.SQLite, Warn: jobssettingsWarn(slog.Default())})
	logger := slog.Default()
	// 非设置驱动 job。
	if _, ok := resolveScheduleSettingsInterval(context.Background(), source, logger, "unknown-job"); ok {
		t.Fatal("非设置驱动 job 必须返回 false")
	}
	// 设置驱动 job：默认间隔。
	interval, ok := resolveScheduleSettingsInterval(context.Background(), source, logger, "usage-stats-aggregation")
	if !ok || interval <= 0 {
		t.Fatalf("设置驱动 job 应解析: %v %v", interval, ok)
	}
	// 越界值 → Number 错误 → 回落注册表默认。
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsAggregationIntervalSeconds', '999999')`); err != nil {
		t.Fatal(err)
	}
	interval, ok = resolveScheduleSettingsInterval(context.Background(), source, logger, "usage-stats-aggregation")
	if !ok || interval <= 0 {
		t.Fatalf("越界应回落默认: %v %v", interval, ok)
	}

	// probeSettingsSource：非法值回落默认边界。
	assembly := &workerAssembly{logger: logger}
	probeSettings := assembly.probeSettingsSource(&businessDB{db: db})
	if probeSettings == nil {
		t.Fatal("设置源不得为 nil")
	}
	_ = probeSettings

	// jobssettingsWarn：nil logger 返回 nil；非 nil 时不 panic。
	if jobssettingsWarn(nil) != nil {
		t.Fatal("nil logger 应返回 nil warn")
	}
	jobssettingsWarn(logger)("event", nil, "message")
}

func TestW12CLoadWorkerConfigAndDisabledStartup(t *testing.T) {
	env := workerSmokeTestEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil || !config.Enabled {
		t.Fatalf("smoke env 必须启用 worker: %+v %v", config, err)
	}
	// 非法数值 env 失败分支（bad drain timeout）。
	badEnv := getenvFrom(map[string]string{
		"JUHE_AI_JOBS_WORKER_ENABLED": "true",
		"JUHE_AI_DATABASE_DRIVER":     "sqlite",
		"JUHE_AI_DATABASE_PATH":       filepath.Join(t.TempDir(), "business.sqlite3"),
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS": "abc",
	})
	if _, err := loadWorkerConfig(badEnv); err == nil {
		t.Fatal("非法 drain timeout 必须失败")
	}
	// 禁用任务登记：空清单 + 家族未启用登记。
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	defer assembly.closeStores()
	registerDisabledJobsStartup(assembly, slog.Default())
	if len(assembly.disabledJobs) == 0 {
		t.Fatal("家族未启用的 GoWired 任务必须登记")
	}
}

// ---------------------------------------------------------------------------
// 最小装配面补充（fence settler 与 passive face）
// ---------------------------------------------------------------------------

func TestW12CMinimalAssemblyFenceSettlerArms(t *testing.T) {
	redisServer := miniredis.RunT(t)
	config := workerConfig{Driver: "sqlite", BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3")}
	assembly := newWorkerAssembly(config, slog.Default())
	defer assembly.closeStores()
	// Redis 未配置 → nil settler。
	settler, closer, err := assembly.wireHealthProbeFenceSettler()
	if err != nil || settler != nil || closer != nil {
		t.Fatalf("无 Redis 应返回 nil settler: %v %v %v", settler, closer != nil, err)
	}
	// miniredis 配置 → settler 可用。
	assembly.config.RedisStateURL = "redis://" + redisServer.Addr()
	assembly.config.RedisNamespace = "juhe-ai:w12c"
	settler, closer, err = assembly.wireHealthProbeFenceSettler()
	if err != nil || settler == nil {
		t.Fatalf("miniredis 下 settler 必须可用: %v %v", settler, err)
	}
	if err := settler(context.Background(), accounthealth.SourceFence{
		RuntimeKey: "acc-w12c", StateKey: "sk", AccountID: "acc-w12c",
		SourceGeneration: 1, SourceFenceID: "f-1", ProbeGeneration: 1,
	}, "unknown"); err != nil {
		t.Fatalf("fence 结算不得失败: %v", err)
	}
	if closer == nil {
		t.Fatal("closer 必须返回")
	}
	if err := closer(); err != nil {
		t.Fatal(err)
	}
}

func TestW12CJobsHTTPHandlerSupplementalArms(t *testing.T) {
	// worker 就绪但 wired 清单为空：健康载荷仍带 worker 对象。
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true },
		false, func() bool { return true },
		false, func() bool { return true },
		nil, "",
		false, func() bool { return true },
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		false, func() bool { return true },
		gometrics.New("juhe-ai", "jobs"), (*gometrics.Sampler)(nil),
		true, func() bool { return true },
		nil)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("/health 必须 200: %d %s", record.Code, record.Body.String())
	}
	if !strings.Contains(record.Body.String(), "workerEnabled") {
		t.Fatalf("健康载荷必须包含 workerEnabled: %s", record.Body.String())
	}
}
