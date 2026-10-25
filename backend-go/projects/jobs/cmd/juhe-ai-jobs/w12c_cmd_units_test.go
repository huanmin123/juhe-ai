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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// parseProbeOutboxRetentionDaysArms：保留天数解析（默认/越界回退/非法回退/
// 合法透传/nil getenv）。原 sqlite 存储 face 臂随 sqlite 形态退役删除，语义
// 由 w16i PG 全链 e2e 承接。
func TestW12CProbeOutboxRetentionParseArms(t *testing.T) {
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
// retention 设置读模型与本地队列
// ---------------------------------------------------------------------------

func TestW12CRetentionSettingsRuntimeArms(t *testing.T) {
	// PG 唯一形态：juhe_business.system_settings 独立测试库（原 SQLite 文件
	// 库夹具随 sqlite 形态退役删除）。
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.system_settings (
	system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
	PRIMARY KEY (system_account_id, key))`)
		return err
	})
	runtime := newRetentionSettingsRuntime(db, slog.Default())
	ctx := context.Background()
	settings, err := runtime.settings(ctx)
	if err != nil || len(settings) != len(retentionPolicySettingKeys) {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	// 非法整数 → 新建读模型绕开 60s 缓存后任务失败。
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings VALUES ('sys_admin', 'usageRecordRetentionDays', '"abc"')`); err != nil {
		t.Fatal(err)
	}
	freshRuntime := newRetentionSettingsRuntime(db, slog.Default())
	if _, err := freshRuntime.settings(ctx); err == nil {
		t.Fatal("非法设置值必须失败")
	}
	if _, err := db.Exec(`DELETE FROM juhe_business.system_settings WHERE key = 'usageRecordRetentionDays'`); err != nil {
		t.Fatal(err)
	}

	// 时区：缺行回落种子默认 → 缓存命中 → 显式值生效 → 非法 JSON 失败 →
	// 非法时区失败 → location。
	name, err := runtime.timezoneName(ctx)
	if err != nil || name != "Asia/Shanghai" {
		t.Fatalf("缺行应回落种子时区 Asia/Shanghai: %q %v", name, err)
	}
	if name, err = runtime.timezoneName(ctx); err != nil || name != "Asia/Shanghai" {
		t.Fatalf("缓存命中: %q %v", name, err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"Asia/Shanghai"')`); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.tzValue = ""
	runtime.tzExpiresAt = time.Time{}
	runtime.mu.Unlock()
	if name, err = runtime.timezoneName(ctx); err != nil || name != "Asia/Shanghai" {
		t.Fatalf("显式时区应生效: %q %v", name, err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.system_settings SET value_json = 'not-json' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.tzValue = ""
	runtime.tzExpiresAt = time.Time{}
	runtime.mu.Unlock()
	if _, err := runtime.timezoneName(ctx); err == nil {
		t.Fatal("非法 JSON 必须失败")
	}
	if _, err := db.Exec(`UPDATE juhe_business.system_settings SET value_json = '"Mars/Phobos"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.timezoneName(ctx); err == nil {
		t.Fatal("非法时区必须失败")
	}
	location, err := runtime.location(ctx)
	if err == nil || location != nil {
		t.Fatalf("非法时区 location 必须失败: %v %v", location, err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.system_settings SET value_json = '"UTC"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if location, err = runtime.location(ctx); err != nil || location == nil {
		t.Fatalf("UTC location: %v %v", location, err)
	}
	// PG 读失败（表缺失）→ 告警 + 回落默认时区（postgresBackgroundJobSettingValue
	// 语义：快照刷新失败不拉停任务）。新建 runtime 绕开 60s 缓存。
	if _, err := db.Exec(`DROP TABLE juhe_business.system_settings`); err != nil {
		t.Fatal(err)
	}
	brokenRuntime := newRetentionSettingsRuntime(db, slog.Default())
	if name, err = brokenRuntime.timezoneName(ctx); err != nil || name != "Asia/Shanghai" {
		t.Fatalf("PG 读失败应回落默认: %q %v", name, err)
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
	// PG 唯一形态：juhe_business.system_settings 独立测试库。
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.system_settings (
	system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
	PRIMARY KEY (system_account_id, key))`)
		return err
	})
	source := jobssettings.NewSource(jobssettings.Options{DB: db, Mode: jobssettings.Postgres, Warn: jobssettingsWarn(slog.Default())})
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
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings VALUES ('sys_admin', 'usageStatsAggregationIntervalSeconds', '999999')`); err != nil {
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
	if got := probeSettings("accountQualityWindowMinutes", 1, 1440); got != 10 {
		t.Fatalf("缺行应回落默认 10: %d", got)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings VALUES ('sys_admin', 'accountQualityWindowMinutes', '"abc"')`); err != nil {
		t.Fatal(err)
	}
	if got := probeSettings("accountQualityWindowMinutes", 1, 1440); got != 10 {
		t.Fatalf("非法值应回落默认 10: %d", got)
	}

	// jobssettingsWarn：nil logger 返回 nil；非 nil 时不 panic。
	if jobssettingsWarn(nil) != nil {
		t.Fatal("nil logger 应返回 nil warn")
	}
	jobssettingsWarn(logger)("event", nil, "message")
}

func TestW12CLoadWorkerConfigAndDisabledStartup(t *testing.T) {
	env := workerSmokeTestEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("smoke env 必须通过 worker 配置校验: %+v %v", config, err)
	}
	// 非法数值 env 失败分支（bad drain timeout）。
	badEnv := getenvFrom(map[string]string{
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS": "abc",
	})
	if _, err := loadWorkerConfig(badEnv); err == nil {
		t.Fatal("非法 drain timeout 必须失败")
	}
	// 禁用任务登记：空清单 + 家族未启用登记。
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
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
	config := workerConfig{}
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
	// 已配置 RedisStateURL 但 namespace 非法 → 真配置错误：仍 nil settler，
	// 但保留 WARN 语义（fence_settler_unavailable）。
	assembly.config.RedisNamespace = "invalid namespace"
	settler, closer, err = assembly.wireHealthProbeFenceSettler()
	if err != nil || settler != nil || closer != nil {
		t.Fatalf("namespace 非法应返回 nil settler: %v %v %v", settler, closer != nil, err)
	}
}

// TestW12CMinimalAssemblyFenceSettlerStandaloneNotWarn 验证模式平等语义：
// standalone（未配置 JUHE_AI_REDIS_STATE_URL）只记 Info 说明，不产生
// fence_settler_unavailable 的 WARN——缺 Redis 是该模式正常 shape，不是
// 能力缺失；只有已配置 URL 但 namespace 非法才允许 WARN。
func TestW12CMinimalAssemblyFenceSettlerStandaloneNotWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	config := workerConfig{}
	assembly := newWorkerAssembly(config, logger)
	defer assembly.closeStores()
	settler, closer, err := assembly.wireHealthProbeFenceSettler()
	if err != nil || settler != nil || closer != nil {
		t.Fatalf("standalone 应返回 nil settler: %v %v %v", settler, closer != nil, err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "account_health_probe_outbox_fence_settler_standalone") {
		t.Fatalf("standalone 分支必须记 Info 说明: %s", logs)
	}
	if strings.Contains(logs, "fence_settler_unavailable") || strings.Contains(logs, "level=WARN") {
		t.Fatalf("standalone 分支不得产生 WARN: %s", logs)
	}
}

func TestW12CJobsHTTPHandlerSupplementalArms(t *testing.T) {
	// worker 就绪但 wired 清单为空：健康载荷仍带 workerEnabled 字段。
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:            ownermode.Active,
		RuntimeRunning:       &running,
		ProxyLatencySnapshot: func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		WorkerReady:          func() bool { return true },
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("/health 必须 200: %d %s", record.Code, record.Body.String())
	}
	if !strings.Contains(record.Body.String(), "workerEnabled") {
		t.Fatalf("健康载荷必须包含 workerEnabled: %s", record.Body.String())
	}
}
