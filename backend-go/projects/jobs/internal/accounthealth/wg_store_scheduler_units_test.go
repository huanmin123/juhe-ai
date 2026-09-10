package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wgNewStore 构造已初始化 schema 的 SQLite jobs store。
func wgNewStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "jobs.sqlite3")})
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("初始化 schema: %v", err)
	}
	return store
}

// TestOpenStoreRejectsInvalidConfig 覆盖 OpenStore 的门禁分支。
func TestOpenStoreRejectsInvalidConfig(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: "  "}); err == nil {
		t.Fatal("缺路径必须报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: ""}); err == nil {
		t.Fatal("PG 缺 URL 必须报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://x", PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 4}); err == nil {
		t.Fatal("idle > open 必须报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("oracle")}); err == nil {
		t.Fatal("非法 mode 必须报错")
	}
}

// TestEnsureSchemaCanceledContext 覆盖 schema 锁的 ctx 取消分支。
func TestEnsureSchemaCanceledContext(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "jobs.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.EnsureSchema(ctx); err == nil {
		t.Fatal("取消的 ctx 必须报错")
	}
}

// TestOwnerLeaseLifecycle 覆盖租约获取/竞争/续约/释放的完整语义。
func TestOwnerLeaseLifecycle(t *testing.T) {
	store := wgNewStore(t)
	ctx := context.Background()
	// 参数无效。
	if _, _, err := store.AcquireOwnerLease(ctx, "  ", time.Minute); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "owner-a", 0); err == nil {
		t.Fatal("非正 duration 必须报错")
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired || lease.FenceToken < 1 {
		t.Fatalf("首次获取必须成功: %+v %v %v", lease, acquired, err)
	}
	// 未过期 → 竞争失败。
	if _, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("未过期租约不得被抢占: %v %v", acquired, err)
	}
	// 续约：错误 owner → false；duration 非法 → 报错。
	if ok, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: "owner-x", FenceToken: lease.FenceToken}, time.Minute); err != nil || ok {
		t.Fatalf("错误 owner 续约必须 false: %v %v", ok, err)
	}
	if _, err := store.RenewOwnerLease(ctx, lease, 0); err == nil {
		t.Fatal("非正续约 duration 必须报错")
	}
	if ok, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !ok {
		t.Fatalf("正确续约必须成功: %v %v", ok, err)
	}
	// 释放：参数无效报错；正确 owner 释放成功；过期 owner 重复释放仍成功
	// （DELETE 幂等，不报错）。
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "", FenceToken: 1}); err == nil {
		t.Fatal("空 owner 释放必须报错")
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("正确释放: %v", err)
	}
	// 释放后行已删除，重新获取从初始 fence 开始。
	next, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute)
	if err != nil || !acquired || next.FenceToken < 1 {
		t.Fatalf("释放后必须可重新获取: %+v %v %v", next, acquired, err)
	}
	// 过期接管（行仍存在）：lease_until 置为过去 → 冲突更新 fence+1。
	if _, err := store.db.Exec(`UPDATE account_health_owner_leases SET lease_until = ? WHERE lease_key = 'account-health-owner'`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	takeover, acquired, err := store.AcquireOwnerLease(ctx, "owner-c", time.Minute)
	if err != nil || !acquired || takeover.FenceToken <= next.FenceToken {
		t.Fatalf("过期接管必须递增 fence: %+v %v %v", takeover, acquired, err)
	}
}

// TestNullableHelpers 覆盖可空值写侧转换助手。
func TestNullableHelpers(t *testing.T) {
	if nullableStatus(0) != nil || nullableStatus(3) != 3 {
		t.Fatal("nullableStatus 分支错误")
	}
	if nullableText("  ") != nil || nullableText("x") != "x" {
		t.Fatal("nullableText 分支错误")
	}
	if nullableTime(nil) != nil || nullableTime(&time.Time{}) != nil {
		t.Fatal("零值时间必须写 NULL")
	}
	stamp := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if value, ok := nullableTime(&stamp).(time.Time); !ok || value != stamp {
		t.Fatalf("有效时间必须转 UTC: %v", nullableTime(&stamp))
	}
	if nullableTimeText(nil) != nil {
		t.Fatal("nil 时间文本必须为 nil")
	}
	if text, ok := nullableTimeText(&stamp).(string); !ok || text != "2026-09-10T08:00:00Z" {
		t.Fatalf("时间文本格式错误: %v", nullableTimeText(&stamp))
	}
}

// TestOutcomeCooldownFenceSources 覆盖 cooldown fence 三级来源与 observation。
func TestOutcomeCooldownFenceSources(t *testing.T) {
	observed := time.Date(2026, 9, 10, 7, 0, 0, 0, time.UTC)
	fence := &CooldownFence{ObservationStartedAt: observed, Generation: "gen-1"}
	if outcomeCooldownFence(Outcome{}) != nil {
		t.Fatal("无 fence 必须为 nil")
	}
	direct := Outcome{CooldownFence: fence}
	if outcomeCooldownFence(direct) != fence {
		t.Fatal("outcome 级 fence 优先")
	}
	fromProjection := Outcome{Projection: &Projection{CooldownFence: fence}}
	if outcomeCooldownFence(fromProjection) != fence {
		t.Fatal("projection 级 fence 必须命中")
	}
	fromExpected := Outcome{Projection: &Projection{ExpectedCooldownFence: fence}}
	if outcomeCooldownFence(fromExpected) != fence {
		t.Fatal("ExpectedCooldownFence 必须兜底")
	}
	// observation：缺 fence / 零值时间 / 缺 generation → nil。
	if cooldownObservation(Outcome{}) != nil {
		t.Fatal("缺 fence observation 必须为 nil")
	}
	if cooldownObservation(Outcome{CooldownFence: &CooldownFence{Generation: "g"}}) != nil {
		t.Fatal("零值 observation 必须为 nil")
	}
	if cooldownObservation(Outcome{CooldownFence: &CooldownFence{ObservationStartedAt: observed}}) != nil {
		t.Fatal("缺 generation observation 必须为 nil")
	}
	if value, ok := cooldownObservation(direct).(time.Time); !ok || !value.Equal(observed.UTC()) {
		t.Fatalf("完整 fence 必须返回 observation: %v", cooldownObservation(direct))
	}
	if cooldownObservationText(direct) == nil {
		t.Fatal("observation 文本不得为 nil")
	}
}

// TestSQLiteDSNFormat 锁定 DSN 形状（Windows 盘符以 / 前缀进入 file URI）。
func TestSQLiteDSNFormat(t *testing.T) {
	dsn, err := sqliteDSN("tmp/j1.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "file:/") || !contains(dsn, "_pragma=busy_timeout(5000)") {
		t.Fatalf("DSN 形状错误: %s", dsn)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestSchedulerHelpersUnit 覆盖调度器小助手（min/wait/maxPause/Recovery）。
func TestSchedulerHelpersUnit(t *testing.T) {
	if minDuration(3*time.Second, 5*time.Second) != 3*time.Second || minDuration(9*time.Second, 5*time.Second) != 5*time.Second {
		t.Fatal("minDuration 分支错误")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须透传: %v", err)
	}
	if err := waitContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("正常等待必须返回 nil: %v", err)
	}
	if cooldownMaxPause(Schedule{}) != defaultCooldownMaxPauseMinutes*time.Minute {
		t.Fatal("MaxPause 缺省必须回落 2 分钟")
	}
	if cooldownMaxPause(Schedule{MaxPauseMinutes: 30}) != 30*time.Minute {
		t.Fatal("显式 MaxPause 必须生效")
	}
	if cooldownMaxRecovery(Schedule{}) != defaultCooldownMaxRecoveryHours*time.Hour {
		t.Fatal("MaxRecovery 缺省必须回落 12 小时")
	}
	if cooldownMaxRecovery(Schedule{MaxRecoveryHours: 6}) != 6*time.Hour {
		t.Fatal("显式 MaxRecovery 必须生效")
	}
	// passiveDelayBefore 边界：deadline<=1ms、偏移裁剪。
	if passiveDelayBefore(0) != time.Millisecond {
		t.Fatal("零 deadline 必须裁剪到 1ms")
	}
	if delay := passiveDelayBefore(time.Second); delay <= 0 || delay >= time.Second {
		t.Fatalf("正常 deadline 必须留出提前量: %v", delay)
	}
}

// TestNewRunnerWithDirectInputReaderWiresSuppression 覆盖 PG 直读 runner
// 的装配（抑制快照 provider 注入）。
func TestNewRunnerWithDirectInputReaderWiresSuppression(t *testing.T) {
	store := wgNewStore(t)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "biz.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reader, err := NewPostgresDirectInputReader(db, "reader-secret", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunnerWithDirectInputReader(Config{InstanceID: "wg"}, store, nil, reader)
	if runner == nil {
		t.Fatal("runner 不得为 nil")
	}
	// reader 配置无效 → 构造报错。
	if _, err := NewPostgresDirectInputReader(nil, "s", time.Hour, nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
	if _, err := NewPostgresDirectInputReader(db, "s", 30*time.Second, nil); err == nil {
		t.Fatal("TTL 下界必须报错")
	}
}

// TestRunnerStatusAndRunGuards 覆盖 Status/Ready/setError 与 Run 的门禁。
func TestRunnerStatusAndRunGuards(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-status", ScanInterval: time.Hour, OwnerLease: time.Minute}, store, nil)
	status := runner.Status()
	if status.OwnerHeld || !status.LastSuccess.IsZero() {
		t.Fatalf("初始状态必须为空: %+v", status)
	}
	if runner.Ready() {
		t.Fatal("未运行不得就绪")
	}
	// Run 带取消 ctx：lease 获取 → runOwned 检测取消 → 释放 → 循环退出。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 Run 必须返回 Canceled: %v", err)
	}
	// 未初始化 store → 报错。
	if err := (*Runner)(nil).Run(ctx); err == nil {
		t.Fatal("nil runner 必须报错")
	}
	if err := NewRunner(Config{}, nil, nil).Run(ctx); err == nil {
		t.Fatal("缺 store 必须报错")
	}
}

// TestValidateScheduledInputMatrix 表驱动覆盖调度输入校验真值表。
func TestValidateScheduledInputMatrix(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	valid := Input{
		AccountID: "a", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Schedule:    Schedule{HealthIntervalMS: 300_000, HealthJitterMS: 1000, FailureThreshold: 3, FailureRetryMS: 10_000, CooldownNeutralBaseMS: 1000, CooldownNeutralMaxMS: 2000, CooldownFailureBackoffMS: 3000, MaxPauseMinutes: 2, MaxRecoveryHours: 12},
		Eligibility: Eligibility{BoundGroup: true, AuthorizationEligible: true, AccountStatus: "active"},
	}
	if err := validateScheduledInput(valid, now); err != nil {
		t.Fatalf("合法输入必须通过: %v", err)
	}
	invalid := []struct {
		name   string
		mutate func(input *Input)
	}{
		{"缺账户", func(i *Input) { i.AccountID = " " }},
		{"缺 InputVersion", func(i *Input) { i.InputVersion = 0 }},
		{"过期", func(i *Input) { i.ExpiresAt = now.Add(-time.Minute) }},
		{"间隔过短", func(i *Input) { i.Schedule.HealthIntervalMS = 1000 }},
		{"抖动超限", func(i *Input) { i.Schedule.HealthJitterMS = maxScheduleMilliseconds + 1 }},
		{"抖动大于间隔", func(i *Input) { i.Schedule.HealthJitterMS = i.Schedule.HealthIntervalMS + 1 }},
		{"失败阈值 0", func(i *Input) { i.Schedule.FailureThreshold = 0 }},
		{"失败重试过短", func(i *Input) { i.Schedule.FailureRetryMS = 100 }},
		{"暂停上限越界", func(i *Input) { i.Schedule.MaxPauseMinutes = 1441 }},
		{"恢复上限越界", func(i *Input) { i.Schedule.MaxRecoveryHours = 24*30 + 1 }},
		{"缺绑定", func(i *Input) { i.Eligibility.BoundGroup = false }},
		{"缺授权", func(i *Input) { i.Eligibility.AuthorizationEligible = false }},
		{"状态不可调度", func(i *Input) { i.Eligibility.AccountStatus = "disabled" }},
		{"冷却缺 cooldown_until", func(i *Input) {
			i.Eligibility.AccountStatus = "temporary_unavailable"
		}},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			mutated := valid
			item.mutate(&mutated)
			if err := validateScheduledInput(mutated, now); err == nil {
				t.Fatal("非法输入必须报错")
			}
		})
	}
}

// TestPersistTaskFailureIdempotent 覆盖确定性任务失败的幂等持久化。
func TestPersistTaskFailureIdempotent(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-failure"}, store, nil)
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "wg-failure", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	input := Input{AccountID: "acct-fail", InputVersion: 3, ConfigRevision: 1, DispatchRevision: 1}
	if err := runner.persistTaskFailure(ctx, lease, input, time.Now(), "invalid_input", "坏输入"); err != nil {
		t.Fatalf("首次失败持久化: %v", err)
	}
	// 幂等键稳定：第二次不追加、不报错。
	if err := runner.persistTaskFailure(ctx, lease, input, time.Now().Add(time.Minute), "invalid_input", "坏输入"); err != nil {
		t.Fatalf("重复失败持久化必须幂等: %v", err)
	}
	found, err := store.HasRequest(ctx, invalidInputRequestID(input))
	if err != nil || !found {
		t.Fatalf("失败 outcome 必须可查询: %v %v", found, err)
	}
}

// TestPersistExplicitTerminalPersistsOutcome 覆盖显式请求的终态持久化。
func TestPersistExplicitTerminalPersistsOutcome(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-explicit"}, store, nil)
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "wg-explicit", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	request := ProbeRequest{
		RequestID: "req-explicit-1", AccountID: "acct-explicit",
		InputVersion: 2, ConfigRevision: 4, DispatchRevision: 5,
		Deadline: time.Now().Add(time.Minute),
	}
	if err := runner.persistExplicitTerminal(ctx, lease, request, OutcomeTaskFailed, time.Now(), "request_expired", "显式请求过期"); err != nil {
		t.Fatalf("终态持久化: %v", err)
	}
	found, err := store.HasRequest(ctx, "req-explicit-1")
	if err != nil || !found {
		t.Fatalf("终态必须幂等可查: %v %v", found, err)
	}
}
