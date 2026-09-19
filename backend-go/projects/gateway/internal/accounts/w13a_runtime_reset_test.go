package accounts

// w13a runtime_reset.go 未覆盖臂补齐：ResetAccountRuntimeState 的跳过分支
// 矩阵（停用/质量隔离/过期/人工关闭调度/显式错误策略冷却/待检查）与失败
// 臂（运行时端口故障），全流程走真实 sqlite + fakeRuntimeEffects。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW13ARuntimeResetSkipArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	effects := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(effects)

	states := []struct {
		name   string
		update string
		args   []any
		want   string
	}{
		{"停用", `UPDATE accounts SET status = 'disabled' WHERE id = ?`, nil, "disabled"},
		{"质量隔离", `UPDATE accounts SET status = 'quality_isolated' WHERE id = ?`, nil, "quality_isolated"},
		{"套餐过期", `UPDATE accounts SET account_expires_at = '2026-01-01T00:00:00Z' WHERE id = ?`, nil, "expired"},
		{"人工关闭调度", `UPDATE accounts SET schedulable = 0 WHERE id = ?`, nil, "manual_unschedulable"},
		{"显式错误策略冷却", `UPDATE accounts SET last_error_code = 'explicit_account_error_policy_cooldown' WHERE id = ?`, nil, "explicit_policy_cooldown"},
		{"系统配额冷却文案", `UPDATE accounts SET last_error_message = '系统继承错误策略「测试」' WHERE id = ?`, nil, "explicit_policy_cooldown"},
	}
	for index, state := range states {
		id := "acc-w13a-rr" + string(rune('a'+index))
		env.seedAccount(t, id, adminID, "w13a-rr-"+state.name, "active")
		args := state.args
		if args == nil {
			args = []any{id}
		}
		env.exec(t, state.update, args...)
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		outcome, err := env.store.ResetAccountRuntimeState(context.Background(), id, revision,
			AccessScope{ViewerID: adminID, IsAdmin: true})
		if err != nil {
			t.Fatalf("%s：重置失败：%v", state.name, err)
		}
		if outcome == nil {
			t.Fatalf("%s：应返回结果", state.name)
		}
		joined := strings.Join(outcome.Result.Skipped, "|")
		if !strings.Contains(joined, state.want) {
			t.Fatalf("%s：应跳过 %q，实际 %v", state.name, state.want, outcome.Result.Skipped)
		}
	}

	// 待检查且已有健康检查错误 → 跳过 pending_test 但仍尝试持续态清理。
	env.seedAccount(t, "acc-w13a-rrp", adminID, "w13a-rr-pending", "active")
	env.exec(t, `UPDATE accounts SET status = 'pending_test', last_health_check_at = ?,
		last_health_check_error_code = 'timeout' WHERE id = 'acc-w13a-rrp'`, time.Now().UTC().Format(time.RFC3339Nano))
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13a-rrp", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("待检查重置失败：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Skipped, "|"), "pending_test") {
		t.Fatalf("应跳过 pending_test：%v", outcome.Result.Skipped)
	}

	// error 持续失败态 → patchAccountFailureStateForReset 清理（306-334）。
	env.seedAccount(t, "acc-w13a-rrz", adminID, "w13a-rr-error", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'upstream_5xx',
		last_error_message = '上游错误', cooldown_until = ? WHERE id = 'acc-w13a-rrz'`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
	var revision int64
	if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = 'acc-w13a-rrz'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13a-rrz", revision,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("error 态重置失败：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Cleared, "|"), "account_persistent") {
		t.Fatalf("error 态应清理持续失败列：%+v", outcome.Result.Cleared)
	}
	// 错误态清理恢复 active 且运行时清空 → 电路围栏推进 + 健康检查派发。
	if !strings.Contains(strings.Join(outcome.Result.Cleared, "|"), "dispatch_revision") {
		t.Fatalf("应推进调度代围栏：%+v", outcome.Result.Cleared)
	}
	if len(effects.healthCheckDispatches) == 0 {
		t.Fatal("应派发健康检查")
	}
	// 恢复后 GatewayRuntime 汇总为 cleared。
	if outcome.Result.GatewayRuntime != "cleared" {
		t.Fatalf("网关运行时应为 cleared：%q", outcome.Result.GatewayRuntime)
	}
}

func TestW13ARuntimeResetFailureArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-rrf", adminID, "w13a-rrf", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'upstream_5xx' WHERE id = 'acc-w13a-rrf'`)

	// 运行时清理端口故障 → failed 含 gateway_runtime（348-350）。
	effects := &fakeRuntimeEffects{failClearRuntime: true}
	env.store.SetRuntimeResetEffects(effects)
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13a-rrf", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("端口故障重置失败：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "gateway_runtime") {
		t.Fatalf("应记 gateway_runtime 失败：%v", outcome.Result.Failed)
	}

	// 端口未装配 → failed 同样含 gateway_runtime（355-357）。
	env.store.SetRuntimeResetEffects(nil)
	var latest int64
	if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = 'acc-w13a-rrf'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13a-rrf", latest,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("无端口重置失败：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "gateway_runtime") {
		t.Fatalf("无端口应记失败：%v", outcome.Result.Failed)
	}
}
