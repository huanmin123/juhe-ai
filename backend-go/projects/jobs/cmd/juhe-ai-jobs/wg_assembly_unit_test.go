package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

func TestCloseStoresReportsCloserError(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	order := []string{}
	assembly.addCloser(func() error { order = append(order, "first"); return nil })
	assembly.addCloser(func() error { order = append(order, "second"); return errors.New("关闭失败") })
	assembly.closeStores()
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("closeStores 必须逆向执行全部 closer: %v", order)
	}
	if assembly.closers != nil {
		t.Fatal("closeStores 后 closer 清单必须清空")
	}
}

// TestStatusPayloadWithoutScheduler 验证手工构造的最小装配体也能输出健康载荷
// （nil 调度器分支不 panic）。
func TestStatusPayloadWithoutScheduler(t *testing.T) {
	assembly := &workerAssembly{config: workerConfig{}}
	payload := assembly.statusPayload()
	if payload["workerEnabled"] != true {
		t.Fatalf("健康载荷基础字段错误: %v", payload)
	}
	// workerDriver 键已随 driver 概念退役删除（对齐 w10b 契约）。
	if _, ok := payload["workerDriver"]; ok {
		t.Fatalf("workerDriver 键必须已删除: %v", payload)
	}
	if _, ok := payload["workerJobs"].([]jobsched.Snapshot); !ok {
		t.Fatalf("nil 调度器必须输出空快照列表: %T", payload["workerJobs"])
	}
}

// TestRankSnapshotCoreStagesBranches 锁定 usage-rank-snapshots-refresh 的
// databaseDriver 注册分叉：PG 剔除 ai_performance_summary_windows。
func TestRankSnapshotCoreStagesBranches(t *testing.T) {
	// D13：rank 阶段集合恒 PG 形态（ai_performance 由独立 job 承担，不入列）。
	pgStages := rankSnapshotCoreStages()
	for _, stage := range pgStages {
		if stage == statsagg.StageAiPerformanceSummaryWindows {
			t.Fatal("rank 阶段不得包含 ai_performance_summary_windows")
		}
	}
	// BUG-0285：热任务只保留 scope 阶段——overview 阶段生产实测 ~52-55s 超
	// 35s 超时，且其写面已由 usage-overview-windows-refresh 覆盖。
	hot := hotUsageWindowStages()
	if len(hot) != 1 || hot[0] != statsagg.StageUsageScopeRangeWindows {
		t.Fatalf("热窗口 stage 必须仅为 usage_scope_range_windows: %v", hot)
	}
}

// TestRegisterDisabledJobLogsUnknownRegistry 验证非注册表 job 的登记分支。
func TestRegisterDisabledJobLogsUnknownRegistry(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	assembly.registerDisabledJob("wg-not-in-registry", "测试缺口")
	if len(assembly.disabledJobs) != 1 || assembly.disabledJobs[0].JobName != "wg-not-in-registry" {
		t.Fatalf("登记清单错误: %+v", assembly.disabledJobs)
	}
}

// TestRunWiredJobOnceUnknownName 验证未注册任务的显式报错。
func TestRunWiredJobOnceUnknownName(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, nil)
	if _, err := assembly.runWiredJobOnce(context.Background(), "wg-nope"); err == nil {
		t.Fatal("未注册任务必须报错")
	}
}

// noopWiredTask 是调度注册测试的空任务。
func noopWiredTask(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
	return jobsched.TaskResult{}, nil
}

// TestScheduleWiredJobRejectsNonGoWired 验证非 GoWired 名称被拒绝注册。
func TestScheduleWiredJobRejectsNonGoWired(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	assembly.scheduleWiredJob("wg-definitely-not-a-job", noopWiredTask)
	if len(assembly.wiredJobs) != 0 || len(assembly.wiredTasks) != 0 {
		t.Fatalf("非 GoWired 任务不得注册: %v", assembly.wiredJobs)
	}
}

// TestScheduleWiredJobDriverForkRejectsRegistration 验证 PG 独占任务在
// ResolveScheduleForDriver 反向分支下拒绝注册（T6b 冻结语义）。
func TestScheduleWiredJobDriverForkRejectsRegistration(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	// usage-scope-range-windows-refresh 仅默认/SQLite 分支注册。
	assembly.scheduleWiredJob("usage-scope-range-windows-refresh", noopWiredTask)
	if len(assembly.wiredJobs) != 0 {
		t.Fatalf("PG 分支不得注册 SQLite 独占任务: %v", assembly.wiredJobs)
	}
}

func TestWithLeaseSkipsWhenSQLiteOrNoStore(t *testing.T) {
	assembly := &workerAssembly{config: workerConfig{}}
	called := false
	wrapped := assembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		called = true
		return jobsched.TaskResult{}, nil
	})
	if _, err := wrapped(context.Background(), jobsched.TaskContext{}); err != nil || !called {
		t.Fatalf("SQLite 必须直跑任务: %v %v", called, err)
	}
	nilStoreAssembly := &workerAssembly{config: workerConfig{}}
	nilStoreCalled := false
	nilStoreWrapped := nilStoreAssembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		nilStoreCalled = true
		return jobsched.TaskResult{}, nil
	})
	if _, err := nilStoreWrapped(context.Background(), jobsched.TaskContext{}); err != nil || !nilStoreCalled {
		t.Fatalf("无租约存储必须直跑任务: %v %v", nilStoreCalled, err)
	}
}

func TestIngestDrainGateWithoutWriter(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, nil)
	probe := assembly.ingestDrainProbe()
	// probe 本身是闭包；writer 缺席在调用时返回 (nil, nil) 等价 Node
	// ingest worker 不可达。
	status, err := probe(context.Background())
	if err != nil || status != nil {
		t.Fatalf("writer 缺席时 probe 必须返回 nil 状态: %v %v", status, err)
	}
	gate := assembly.ingestDrainGate()
	if err := gate(context.Background()); err == nil {
		t.Fatal("writer 缺席时门控必须失败本轮")
	}
	if _, err := assembly.ingestDrainSafety(context.Background()); err == nil {
		t.Fatal("writer 缺席时 safety 必须失败")
	}
	// ctx 取消时 probe 直接返回 ctx 错误。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 ctx 必须透传错误: %v", err)
	}
}

// TestBusinessDBDialectHelpers 覆盖业务库句柄的方言助手矩阵（PG 唯一形态；
// 原 SQLite 不加前缀/原样 bind/RFC3339Nano 文本断言随 sqlite 形态退役删除）。
func TestBusinessDBDialectHelpers(t *testing.T) {
	pg := &businessDB{}
	if pg.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("PG 表名必须限定 schema: %s", pg.table("accounts"))
	}
	if bound := pg.bind("WHERE id = ? AND name = ?"); bound != "WHERE id = $1 AND name = $2" {
		t.Fatalf("PG bind 必须改写占位符: %s", bound)
	}
	if statsTable("account_usage_snapshots") != "juhe_stats.account_usage_snapshots" {
		t.Fatal("PG 统计表必须限定 juhe_stats")
	}
	if timeParam(time.Unix(0, 0).UTC()) != time.Unix(0, 0).UTC() {
		t.Fatal("PG 时间参数必须保持 time.Time")
	}
	if textParam("x") != "x" {
		t.Fatal("textParam 必须原样返回")
	}
	// accounts 布尔标志列在 PG 里也是 integer(0/1)，统一输出数字，
	// 输出 TRUE/FALSE 会报 operator does not exist: integer = boolean。
	for _, expected := range []string{"1", "0"} {
		if got := boolLit(expected == "1"); got != expected {
			t.Fatalf("boolLit(%v)=%s 期望 %s", expected == "1", got, expected)
		}
	}
}

// TestScanNullTimeMatrix 覆盖可空时间扫描的全部类型分支。
func TestScanNullTimeMatrix(t *testing.T) {
	valid := "2026-09-10T08:30:00.123456789Z"
	expected := time.Date(2026, 9, 10, 8, 30, 0, 123456789, time.UTC)
	cases := []struct {
		name     string
		postgres bool
		value    any
		wantNil  bool
		wantTime time.Time
		wantErr  bool
	}{
		{"nil 值", false, nil, true, time.Time{}, false},
		{"SQLite 字符串", false, valid, false, expected, false},
		{"SQLite 字节串", false, []byte(valid), false, expected, false},
		{"SQLite time.Time", false, expected, false, expected, false},
		{"SQLite 非法字符串", false, "nope", false, time.Time{}, true},
		{"SQLite 非法字节串", false, []byte("bad"), false, time.Time{}, true},
		{"SQLite 非法类型", false, 42, false, time.Time{}, true},
		{"PG time.Time", true, expected, false, expected, false},
		{"PG 字节串", true, []byte(valid), false, expected, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			parsed, err := scanNullTime(item.value)
			if item.wantErr {
				if err == nil {
					t.Fatalf("必须报错，得到 %v", parsed)
				}
				return
			}
			if err != nil {
				t.Fatalf("必须成功: %v", err)
			}
			if item.wantNil {
				if parsed != nil {
					t.Fatalf("必须返回 nil，得到 %v", parsed)
				}
				return
			}
			if parsed == nil || !parsed.Equal(item.wantTime) {
				t.Fatalf("时间不一致: %v 期望 %v", parsed, item.wantTime)
			}
		})
	}
}

func TestBalanceConfigJSONHelpers(t *testing.T) {
	// intervalMinutes=0 归一为 5；preferred 为空省略。
	normalized, err := normalizeBalanceConfigJSON("builtin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if normalized != `{"adapter":"builtin","intervalMinutes":5}` {
		t.Fatalf("归一化输出漂移: %s", normalized)
	}
	withPreferred, err := normalizeBalanceConfigJSON("builtin", 30, "openai_user_balance")
	if err != nil {
		t.Fatal(err)
	}
	if withPreferred != `{"adapter":"builtin","intervalMinutes":30,"preferredBuiltinAdapter":"openai_user_balance"}` {
		t.Fatalf("preferred 字段输出漂移: %s", withPreferred)
	}
	if !balanceConfigJSONEqual(`{"adapter":"builtin","intervalMinutes":10}`, normalizeBalanceConfigJSONMust(t, "builtin", 10, "")) {
		t.Fatal("语义相同的配置必须判定相等")
	}
	if balanceConfigJSONEqual(`{"adapter":"builtin","intervalMinutes":10}`, normalizeBalanceConfigJSONMust(t, "builtin", 30, "")) {
		t.Fatal("不同配置不得判定相等")
	}
	if balanceConfigJSONEqual(`not-json`, `{"adapter":"builtin"}`) {
		t.Fatal("非法存储 JSON 必须判定不等")
	}
	if balanceConfigJSONEqual(`"string"`, `{"adapter":"builtin"}`) {
		t.Fatal("非对象存储 JSON 必须判定不等")
	}
}

func normalizeBalanceConfigJSONMust(t *testing.T, adapter string, interval int, preferred string) string {
	t.Helper()
	text, err := normalizeBalanceConfigJSON(adapter, interval, preferred)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// TestBalanceValueHelpers 覆盖余额探测的值转换助手。
func TestBalanceValueHelpers(t *testing.T) {
	if !hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"sk-a", "sk-b"}}) {
		t.Fatal("api_keys 列表非空必须命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"  ", ""}}) {
		t.Fatal("api_keys 全空白不得命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{}}) {
		t.Fatal("空 api_keys 不得命中")
	}
	if !hasAtLeastOneAPIKey(map[string]any{"api_key": " sk-x "}) {
		t.Fatal("非空 api_key 必须命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_key": "   "}) {
		t.Fatal("空白 api_key 不得命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{}) {
		t.Fatal("无 Key 字段不得命中")
	}
	if textOrEmpty("x") != "x" || textOrEmpty(42) != "" {
		t.Fatal("textOrEmpty 类型分支错误")
	}
	if intOrZero(float64(7.9)) != 7 || intOrZero(3) != 3 || intOrZero(json.Number("9")) != 9 || intOrZero("x") != 0 {
		t.Fatal("intOrZero 类型分支错误")
	}
	if _, err := parseBalanceInstant("bad"); err == nil {
		t.Fatal("非法时间戳必须报错")
	}
	parsed, err := parseBalanceInstant("2026-09-10T00:00:00Z")
	if err != nil || parsed.IsZero() {
		t.Fatalf("合法时间戳必须解析: %v %v", parsed, err)
	}
	if optionalString("") != nil || *optionalString("v") != "v" {
		t.Fatal("optionalString 空值分支错误")
	}
	if nullableTextPtr(nil) != nil || nullableTextPtr(ptrString("x")) != "x" {
		t.Fatal("nullableTextPtr 空值分支错误")
	}
	// decryptCredentials：明文 JSON、合法封套、非法封套三条路径。
	runtime := &balanceDetectRuntime{secret: "0123456789abcdef0123456789abcdef"}
	plain, ok := runtime.decryptCredentials(`{"api_key":"sk-1"}`)
	if !ok || len(plain) != 1 {
		t.Fatalf("明文 JSON 凭据必须接受: %v %v", plain, ok)
	}
	if _, ok := runtime.decryptCredentials(`{"api_key":"  "}`); ok {
		t.Fatal("无 Key 凭据必须拒绝")
	}
	envelope, err := accountbalance.EncryptV1Envelope(runtime.secret, []byte(`{"api_key":"sk-env"}`))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, ok := runtime.decryptCredentials(envelope)
	if !ok || decrypted["api_key"] != "sk-env" {
		t.Fatalf("封套凭据必须解密: %v %v", decrypted, ok)
	}
	if _, ok := runtime.decryptCredentials("not-an-envelope"); ok {
		t.Fatal("非法封套必须拒绝")
	}
}

// wgMaintenanceJob 构造一个最小 record-maintenance 任务。
func wgMaintenanceJob(jobType, id string) retention.RecordMaintenanceJob {
	return retention.RecordMaintenanceJob{Type: jobType, ID: id, APIKeyID: id, AccountID: id, SystemAccountID: "sys_wg"}
}

// TestRecordMaintenanceQueue 验证本地维护队列的入队/截断/超限丢弃与停机排空。
func TestRecordMaintenanceQueue(t *testing.T) {
	job := wgMaintenanceJob("api_key", "acc-1")
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 2, MaxBytes: 64 * 1024})
	if queued, reason := queue.enqueue(job); !queued || reason != "" {
		t.Fatalf("正常入队必须成功: %v %v", queued, reason)
	}
	// 队列满 → 显式丢弃并说明原因。
	queue.enqueue(job)
	if queued, reason := queue.enqueue(job); queued || reason != "worker_local_queue_full" {
		t.Fatalf("超限必须丢弃: %v %v", queued, reason)
	}
	if queue.size() != 2 {
		t.Fatalf("队列长度错误: %d", queue.size())
	}
	batch := queue.takeBatch(1)
	if len(batch) != 1 || queue.size() != 1 {
		t.Fatalf("takeBatch 截断错误: %d/%d", len(batch), queue.size())
	}
	// oversize：单任务超过 MaxBytes。
	tinyQueue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 4})
	if queued, reason := tinyQueue.enqueue(job); queued || reason != "oversize" {
		t.Fatalf("超大任务必须丢弃: %v %v", queued, reason)
	}
	if queue.size() != 1 {
		t.Fatalf("队列长度错误: %d", queue.size())
	}
	// estimateJobBytes 正常序列化。
	if estimateJobBytes(job) <= 0 {
		t.Fatal("任务字节数必须大于 0")
	}
}

// TestRecordMaintenanceQueueDrainShutdown 验证停机排空的失败即停语义。
func TestRecordMaintenanceQueueDrainShutdown(t *testing.T) {
	job := wgMaintenanceJob("account", "acc-2")
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	queue.enqueue(job)
	queue.drainShutdown(func(_ context.Context, _ retention.RecordMaintenanceJob) (map[string]any, error) {
		return nil, errors.New("执行失败")
	})
	if queue.size() != 0 {
		t.Fatalf("排空后队列必须清空（失败即停不重入队）: %d", queue.size())
	}
	queue.enqueue(job)
	queue.enqueue(job)
	queue.drainShutdown(func(_ context.Context, _ retention.RecordMaintenanceJob) (map[string]any, error) {
		return map[string]any{}, nil
	})
	if queue.size() != 0 {
		t.Fatalf("成功排空后队列必须清空: %d", queue.size())
	}
}
