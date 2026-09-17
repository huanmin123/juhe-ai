package accounts

// w13a patch_runtime_state.go 未覆盖臂补齐：nextRuntimeState 状态机全分支
// 直测（disabled/error、冷却重置、过期、clearRetest 代际保留）、
// applyRuntimeStateColumns 列差异、冷却端口回退、Key 池隔离与保留活跃 Key
// 判定、操作变更字段查重。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestW13ANextRuntimeStateArms(t *testing.T) {
	env := newTestEnv(t)
	armed := sqlNullString("2026-09-17T09:00:00.000Z")
	generation := sqlNullString("w13a-gen")
	before := patchRuntimeStateBefore{
		status:                     "rate_limited",
		cooldownUntil:              armed,
		lastErrorCode:              sqlNullString("rate_limit"),
		lastErrorMessage:           sqlNullString("触发限流"),
		lastErrorTraceID:           sqlNullString("trace-1"),
		cooldownRetestFailureCount: 2,
		cooldownRetestObservation:  sqlNullString("2026-09-17T07:00:00.000Z"),
		cooldownRetestGeneration:   generation,
		cooldownRetestLastAt:       sqlNullString("2026-09-17T07:30:00.000Z"),
		cooldownRetestLastStatusCode: sql.NullInt64{
			Int64: 429, Valid: true,
		},
	}

	// disabled 臂：清冷却/观察并 clearRetest（178-180 区域）。
	state := env.store.nextRuntimeState(before, patchRuntimeStateInput{nextStatus: "disabled", hasStatusInput: true})
	if state.cooldownUntil.Valid || state.lastErrorCode.Valid || state.cooldownRetestObservation.Valid {
		t.Fatalf("disabled 应清空冷却与错误投影：%+v", state)
	}
	if state.cooldownRetestFailureCount != 0 || state.cooldownRetestLastAt.Valid || state.cooldownRetestLastStatusCode.Valid {
		t.Fatalf("disabled 应清空重试观察：%+v", state)
	}
	// error 臂：保留错误列，仅清冷却/观察，不触发 clearRetest 的代际清空。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{nextStatus: "error", hasStatusInput: true})
	if !state.lastErrorCode.Valid || state.lastErrorCode.String != "rate_limit" {
		t.Fatalf("error 应保留错误码：%+v", state)
	}
	if state.cooldownUntil.Valid || state.cooldownRetestObservation.Valid {
		t.Fatalf("error 应清空冷却与观察：%+v", state)
	}
	if state.cooldownRetestGeneration != generation {
		t.Fatalf("观察被清但代际应一并清空：%+v", state)
	}

	// 冷却重置臂（148-154）：目标冷却态与当前相同但冷却窗口失效 → 重新武装。
	disarmed := before
	disarmed.cooldownUntil = sql.NullString{}
	state = env.store.nextRuntimeState(disarmed, patchRuntimeStateInput{nextStatus: "rate_limited", hasStatusInput: true})
	if !state.cooldownUntil.Valid {
		t.Fatalf("失效冷却应重新武装：%+v", state)
	}
	if !state.cooldownRetestObservation.Valid || !state.cooldownRetestGeneration.Valid {
		t.Fatalf("冷却重置应写入观察起点与新代际：%+v", state)
	}
	if state.lastErrorMessage.String != "手动设置为限流中" {
		t.Fatalf("rate_limited 文案不一致：%+v", state)
	}
	// temporary_unavailable 重置文案与 clearRetest。
	state = env.store.nextRuntimeState(disarmed, patchRuntimeStateInput{nextStatus: "temporary_unavailable", hasStatusInput: true})
	if state.lastErrorMessage.String != "手动设置为临时不可调用" || state.cooldownRetestFailureCount != 0 {
		t.Fatalf("temporary_unavailable 重置不一致：%+v", state)
	}

	// 套餐过期臂：强制停用 + account_expired + clearRetest。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{expiredByPackage: true})
	if state.lastErrorCode.String != "account_expired" || state.cooldownUntil.Valid {
		t.Fatalf("过期应清冷却并写 account_expired：%+v", state)
	}
}

func TestW13AApplyRuntimeStateColumnsArms(t *testing.T) {
	before := patchRuntimeStateBefore{
		status:                       "active",
		cooldownRetestLastStatusCode: sql.NullInt64{Int64: 500, Valid: true},
	}
	// 目标态仅状态码列差异 → 只该列进入赋值集（222-224）。
	state := patchRuntimeState{
		cooldownRetestLastStatusCode: sql.NullInt64{Int64: 503, Valid: true},
	}
	seen := map[string]any{}
	applyRuntimeStateColumns(func(column string, value any) { seen[column] = value }, before, state)
	if len(seen) != 1 || seen["cooldown_retest_last_status_code"] == nil {
		t.Fatalf("仅状态码列应进入赋值集：%v", seen)
	}
	// nullInt64Value/nullStringValue 的 Valid/Invalid 双形态（256/267）。
	if got := nullInt64Value(sql.NullInt64{Int64: 7, Valid: true}); got != int64(7) {
		t.Fatalf("Valid 状态码应透传：%v", got)
	}
	if got := nullInt64Value(sql.NullInt64{}); got != nil {
		t.Fatalf("Invalid 状态码应为 nil：%v", got)
	}
	if got := nullStringValue(sql.NullString{String: "x", Valid: true}); got != "x" {
		t.Fatalf("Valid 文本应透传：%v", got)
	}
	if got := nullStringValue(sql.NullString{}); got != nil {
		t.Fatalf("Invalid 文本应为 nil：%v", got)
	}
}

func TestW13ARuntimeMutationInitialCooldownUntil(t *testing.T) {
	env := newTestEnv(t)
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	// temporary_unavailable：3 秒初始退避。
	if got := env.store.runtimeMutationInitialCooldownUntil("temporary_unavailable", now); !got.Valid ||
		got.String != "2026-09-17T08:00:03.000Z" {
		t.Fatalf("temporary_unavailable 初始退避不一致：%+v", got)
	}
	// rate_limited：settings 端口未装配 → 2 分钟回退（50-50）。
	if got := env.store.runtimeMutationInitialCooldownUntil("rate_limited", now); !got.Valid ||
		got.String != "2026-09-17T08:02:00.000Z" {
		t.Fatalf("rate_limited 回退窗口不一致：%+v", got)
	}
	// 其他状态：不武装。
	if got := env.store.runtimeMutationInitialCooldownUntil("active", now); got.Valid {
		t.Fatalf("非冷却态不应武装：%+v", got)
	}
	// settings 端口装配后使用装配值。
	env.store.SetRuntimeCooldownSettings(w13aFixedCooldownSettings{minutes: 7})
	if got := env.store.runtimeMutationInitialCooldownUntil("rate_limited", now); got.String != "2026-09-17T08:07:00.000Z" {
		t.Fatalf("装配端口应生效：%+v", got)
	}
}

type w13aFixedCooldownSettings struct{ minutes int }

func (s w13aFixedCooldownSettings) DefaultTemporaryUnschedulableMinutes() int { return s.minutes }

func TestW13AAPIKeyPoolIsolationArms(t *testing.T) {
	// 非 api_key 类型不隔离（288-290 区域）。
	if isAccountAPIKeyPoolIsolationEnabled("gpt", "openai", "v1", "oauth", Credentials{"api_keys": []any{"a", "b"}}) {
		t.Fatal("oauth 类型不应隔离")
	}
	// 单 Key 池不隔离。
	if isAccountAPIKeyPoolIsolationEnabled("gpt", "openai", "v1", "api_key", Credentials{"api_keys": []any{"a"}}) {
		t.Fatal("单 Key 不应隔离")
	}
	// 多 Key gpt 账户隔离。
	if !isAccountAPIKeyPoolIsolationEnabled("gpt", "openai", "v1", "api_key", Credentials{"api_keys": []any{"a", "b"}}) {
		t.Fatal("多 Key gpt 应隔离")
	}
	// anthropic 协议档案支持判定（350-352）。
	if !isAccountAPIKeyPoolIsolationEnabled("other", "anthropic", "v1", "api_key", Credentials{"api_keys": []any{"a", "b"}}) {
		t.Fatal("anthropic 协议应支持隔离")
	}
}

func TestW13ARetainedActiveAPIKeyState(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-pool", adminID, "w13a-pool", "active")

	current := Credentials{"api_keys": []any{"sk-w13a-a", "sk-w13a-b"}}
	next := Credentials{"api_keys": []any{"sk-w13a-b", "sk-w13a-c"}}
	entries := accountAPIKeyEntries(testSecret, current)
	if len(entries) == 0 {
		t.Fatal("应解析出 Key 条目")
	}
	now := "2026-09-17T00:00:00.000Z"
	for _, entry := range entries {
		env.exec(t, `INSERT INTO account_api_key_runtime_states (id, system_account_id, account_id,
			key_fingerprint, key_index, status, updated_at) VALUES (?, ?, 'acc-w13a-pool', ?, 0, 'active', ?)`,
			"w13a-rt-"+entry.fingerprint[:12], adminID, entry.fingerprint, now)
	}
	// 池中保留的 Key 均为 active → 有保留（367-368 之前的 true 返回）。
	retained, err := env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), env.db,
		"acc-w13a-pool", current, next)
	if err != nil || !retained {
		t.Fatalf("活跃 Key 保留应返回 true：%v %v", retained, err)
	}
	// 保留 Key 全部置为非 active → 无保留（374 false 返回）。
	env.exec(t, `UPDATE account_api_key_runtime_states SET status = 'disabled' WHERE account_id = 'acc-w13a-pool'`)
	retained, err = env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), env.db,
		"acc-w13a-pool", current, next)
	if err != nil || retained {
		t.Fatalf("无活跃保留应返回 false：%v %v", retained, err)
	}
	// 下一池为空 → 提前 false（356-359）。
	retained, err = env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), env.db,
		"acc-w13a-pool", current, Credentials{})
	if err != nil || retained {
		t.Fatalf("空下一池应返回 false：%v %v", retained, err)
	}
	// 查询错误与扫描错误臂（363-365 前半）。
	if _, err := env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), &w13aFakeQueryer{
		probe: func() *sql.Row { return env.db.QueryRow("SELECT 1") },
	}, "acc", current, next); err == nil {
		t.Fatal("查询错误应上抛")
	}
	if _, err := env.store.hasRetainedActiveAccountAPIKeyState(context.Background(), &w13aFakeQueryer{
		rows: func() (*sql.Rows, error) { return env.db.Query("SELECT 1 AS one") },
	}, "acc", current, next); err == nil {
		t.Fatal("扫描错误应上抛")
	}
}

func TestW13AChangesHaveField(t *testing.T) {
	changes := []PatchChange{{Field: "name"}}
	if !changesHaveField(changes, "name") || changesHaveField(changes, "status") {
		t.Fatal("changesHaveField 判定不一致")
	}
	if changesHaveField(nil, "name") {
		t.Fatal("空变更不应命中")
	}
}
