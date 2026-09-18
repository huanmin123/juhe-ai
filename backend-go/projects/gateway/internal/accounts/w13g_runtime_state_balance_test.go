package accounts

// w13g 运行时状态归一与余额草稿补测：nextRuntimeState 状态矩阵、
// applyRuntimeStateColumns 变更检测、余额/模型目录端口访问器与
// prepareBalanceDraft 契约臂。
//
// 不可达登记（w13g）：
// - （无新增；本文件覆盖路径均可从包内入口触发。）

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func w13gNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func TestW13GNextRuntimeStateMatrix(t *testing.T) {
	env := newTestEnv(t)
	before := patchRuntimeStateBefore{
		status:                       "rate_limited",
		cooldownUntil:                w13gNullString("2026-01-01T00:00:00Z"),
		lastErrorCode:                w13gNullString("up"),
		lastErrorMessage:             w13gNullString("m"),
		lastErrorTraceID:             w13gNullString("tr"),
		cooldownRetestFailureCount:   3,
		cooldownRetestObservation:    w13gNullString("2026-01-01T00:00:00Z"),
		cooldownRetestGeneration:     w13gNullString("g"),
		cooldownRetestLastAt:         w13gNullString("2026-01-01T00:00:00Z"),
		cooldownRetestLastStatusCode: sql.NullInt64{Int64: 429, Valid: true},
	}
	now := time.Now().UTC()

	assertCleared := func(t *testing.T, state patchRuntimeState) {
		t.Helper()
		if state.cooldownUntil.Valid || state.lastErrorCode.Valid || state.lastErrorTraceID.Valid {
			t.Fatalf("应清空冷却与错误码：%+v", state)
		}
		if state.cooldownRetestFailureCount != 0 || state.cooldownRetestLastAt.Valid || state.cooldownRetestLastStatusCode.Valid {
			t.Fatalf("应清空重试列：%+v", state)
		}
	}

	// → active：全部清理。
	state := env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "active", now: now})
	assertCleared(t, state)
	if state.cooldownRetestGeneration.Valid {
		t.Fatalf("无新观察时 generation 应清空：%+v", state)
	}
	// → pending_test：保留定制文案。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "pending_test", now: now})
	assertCleared(t, state)
	if !state.lastErrorMessage.Valid || state.lastErrorMessage.String != "账户配置已保存，等待后台检查" {
		t.Fatalf("pending 文案：%+v", state.lastErrorMessage)
	}
	// → disabled：全部清理。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "disabled", now: now})
	assertCleared(t, state)
	// → error：清冷却与观察，保留错误码/文案与重试列（clearRetest 仅在
	// disabled 臂置位）。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "error", now: now})
	if state.cooldownUntil.Valid || state.cooldownRetestObservation.Valid {
		t.Fatalf("error 应清冷却与观察：%+v", state)
	}
	if !state.lastErrorCode.Valid || state.lastErrorCode.String != "up" {
		t.Fatalf("error 应保留错误码：%+v", state.lastErrorCode)
	}
	if state.cooldownRetestFailureCount != 3 || !state.cooldownRetestLastAt.Valid {
		t.Fatalf("error 应保留重试列：%+v", state)
	}
	// → temporary_unavailable（状态变化）→ 重新武装冷却 + 观察 + generation。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true,
		nextStatus: "temporary_unavailable", now: now})
	if !state.cooldownUntil.Valid || !state.cooldownRetestObservation.Valid || !state.cooldownRetestGeneration.Valid {
		t.Fatalf("临时不可用应重新武装：%+v", state)
	}
	if !state.lastErrorMessage.Valid || state.lastErrorMessage.String != "手动设置为临时不可调用" {
		t.Fatalf("临时不可用文案：%+v", state.lastErrorMessage)
	}
	// → rate_limited（状态相同且冷却已武装）→ 不重新武装，原样保留。
	armed := before
	state = env.store.nextRuntimeState(armed, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "rate_limited", now: now})
	if state.lastErrorMessage != before.lastErrorMessage || !state.cooldownUntil.Valid {
		t.Fatalf("已武装限流不应重置：%+v", state)
	}
	// 冷却未武装（空 cooldown）→ 重新武装。
	disarmed := before
	disarmed.cooldownUntil = sql.NullString{}
	state = env.store.nextRuntimeState(disarmed, patchRuntimeStateInput{hasStatusInput: true, nextStatus: "rate_limited", now: now})
	if !state.cooldownUntil.Valid || state.lastErrorMessage.String != "手动设置为限流中" {
		t.Fatalf("未武装应重置：%+v", state)
	}
	// 套餐过期：优先覆盖。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{hasStatusInput: true,
		nextStatus: "active", now: now, expiredByPackage: true})
	if !state.lastErrorCode.Valid || state.lastErrorCode.String != "account_expired" {
		t.Fatalf("过期错误码：%+v", state.lastErrorCode)
	}
	if state.cooldownRetestGeneration.Valid {
		t.Fatalf("过期应清 generation：%+v", state)
	}
	// 无状态输入且连接未变 → 原样保留。
	state = env.store.nextRuntimeState(before, patchRuntimeStateInput{now: now})
	if state.cooldownUntil != before.cooldownUntil || state.cooldownRetestFailureCount != 3 {
		t.Fatalf("无输入应原样：%+v", state)
	}
}

func TestW13GApplyRuntimeStateColumns(t *testing.T) {
	changed := map[string]any{}
	setColumn := func(column string, value any) { changed[column] = value }
	before := patchRuntimeStateBefore{
		cooldownUntil:     w13gNullString("old"),
		lastErrorCode:     w13gNullString("up"),
		lastErrorMessage:  w13gNullString("m"),
		lastErrorTraceID:  w13gNullString("tr"),
		cooldownRetestFailureCount: 2,
	}
	after := patchRuntimeState{
		cooldownUntil:                sql.NullString{},
		lastErrorCode:                sql.NullString{},
		lastErrorMessage:             w13gNullString("m"),
		lastErrorTraceID:             sql.NullString{},
		cooldownRetestFailureCount:   0,
		cooldownRetestObservation:    sql.NullString{},
		cooldownRetestGeneration:     sql.NullString{},
		cooldownRetestLastAt:         sql.NullString{},
		cooldownRetestLastStatusCode: sql.NullInt64{},
	}
	applyRuntimeStateColumns(setColumn, before, after)
	for _, column := range []string{"cooldown_until", "last_error_code", "last_error_trace_id", "cooldown_retest_failure_count"} {
		if _, exists := changed[column]; !exists {
			t.Fatalf("%s 应进入赋值集：%v", column, changed)
		}
	}
	if _, exists := changed["last_error_message"]; exists {
		t.Fatalf("未变化的列不应进入赋值集：%v", changed)
	}
}

// w13gBalanceRefresher / w13gCatalogRefresher 是端口访问器的最小 fake。
type w13gBalanceRefresher struct{}

func (w13gBalanceRefresher) RefreshManual(context.Context, BalanceRefreshCandidate) (BalanceManualRefreshOutcome, error) {
	return BalanceManualRefreshOutcome{}, nil
}

func (w13gBalanceRefresher) TestDraft(context.Context, BalanceDraftProbeInput) (map[string]any, error) {
	return nil, nil
}

type w13gCatalogRefresher struct{}

func (w13gCatalogRefresher) RefreshDraftModelCatalog(context.Context, ModelCatalogDiscoveryInput) (map[string]any, error) {
	return nil, nil
}

func TestW13GRefresherPortsAndBalanceDraft(t *testing.T) {
	env := newTestEnv(t)
	// 端口未接线 → nil。
	if env.store.BalanceRefresherPort() != nil || env.store.ModelCatalogRefresherPort() != nil {
		t.Fatal("未接线端口应为 nil")
	}
	env.store.SetManualBalanceRefresher(w13gBalanceRefresher{})
	env.store.SetModelCatalogRefresher(w13gCatalogRefresher{})
	if env.store.BalanceRefresherPort() == nil || env.store.ModelCatalogRefresherPort() == nil {
		t.Fatal("接线后端口应非 nil")
	}

	// prepareBalanceDraft：缺字段。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{},
		AccessScope{ViewerID: "x"}); err == nil || !strings.Contains(err.Error(), "账户分组无效") {
		t.Fatalf("缺字段：%v", err)
	}
	// 分组缺失。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-w13g-none", "providerCode": "gpt", "type": "api_key",
	}, AccessScope{ViewerID: "x"}); err == nil || !strings.Contains(err.Error(), "账户分组无效") {
		t.Fatalf("分组缺失：%v", err)
	}
	// groups 表缺失 → 错误传播。
	env.exec(t, `DROP TABLE groups`)
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-w13g-none", "providerCode": "gpt", "type": "api_key",
	}, AccessScope{ViewerID: "x"}); err == nil {
		t.Fatal("groups 缺失应报错")
	}
}
