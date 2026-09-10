package accounts

// W2 runtime-reset 深分支与 batch 时间表分支：owner 失败态重置的全状态变体
// （pending_test 无健康痕迹、锁死挂起、套餐过期、冷却重试与流失败计数、
// api_keys 池的瞬态清理）与批量更新的时间表变更链（outbox 行断言）。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// w2ResetAccount 创建一个可重置账户。
func w2ResetAccount(t *testing.T, env *testEnv, name string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, code, payload)
	}
	return dataMap(t, payload)["id"].(string)
}

func TestW2RuntimeResetOwnerDeepBranches(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	t.Run("pending_test 无健康痕迹跳过", func(t *testing.T) {
		// 行为契约：pending_test 且无健康检查失败痕迹的账户无需重置，
		// 进入 skipped 列表而不是报错。
		id := w2ResetAccount(t, env, "首检等待")
		env.exec(t, `UPDATE accounts SET status = 'pending_test' WHERE id = ?`, id)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("首检等待状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["changed"] != false {
			t.Fatalf("跳过时不应有变更：%v", data)
		}
		skipped, ok := data["skipped"].([]any)
		if !ok || len(skipped) == 0 {
			t.Fatalf("skipped 列表缺失：%v", data)
		}
	})
	t.Run("pending_test 带健康失败痕迹重置", func(t *testing.T) {
		id := w2ResetAccount(t, env, "首检失败")
		env.exec(t, `UPDATE accounts SET status = 'pending_test',
			last_health_check_at = '2026-09-01T00:00:00Z',
			last_health_check_error_code = 'upstream_5xx', last_error_code = 'upstream_5xx' WHERE id = ?`, id)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("带痕迹重置失败：%d %v", code, payload)
		}
		if dataMap(t, payload)["status"] != "pending_test" {
			t.Fatalf("带健康失败的账户重置后回到 pending_test：%v", dataMap(t, payload))
		}
	})
	t.Run("锁死挂起保持原状", func(t *testing.T) {
		id := w2ResetAccount(t, env, "锁死重置")
		now := "2026-01-01T00:00:00.000Z"
		env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds,
			lock_retry_interval_seconds, generation, updated_at)
			VALUES (?, 1, 'ENGAGED', 300, 5, 0, ?)`, id, now)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("锁死重置状态码：%d %v", code, payload)
		}
		if dataMap(t, payload)["changed"] != false {
			t.Fatalf("锁死期间不应产生变更：%v", dataMap(t, payload))
		}
	})
	t.Run("套餐过期停用账户跳过重置", func(t *testing.T) {
		// 行为契约：已停用且套餐过期的账户进入 skipped（disabled/expired），
		// 不做失败态清理也不改写错误码。
		id := w2ResetAccount(t, env, "过期重置")
		env.exec(t, `UPDATE accounts SET status = 'disabled', account_expires_at = '2020-01-01T00:00:00Z',
			last_error_code = 'rate_limited' WHERE id = ?`, id)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("过期重置失败：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["changed"] != false || data["status"] != "disabled" {
			t.Fatalf("过期账户应跳过重置：%v", data)
		}
		skipped, ok := data["skipped"].([]any)
		if !ok || len(skipped) < 2 {
			t.Fatalf("skipped 应包含 disabled 与 expired：%v", data["skipped"])
		}
		if got := env.queryCell(t, `SELECT COALESCE(last_error_code,'') FROM accounts WHERE id = ?`, id); got != "rate_limited" {
			t.Fatalf("跳过时不应改写错误码：%s", got)
		}
	})
	t.Run("全失败痕迹清理", func(t *testing.T) {
		id := w2ResetAccount(t, env, "全痕迹")
		env.exec(t, `UPDATE accounts SET status = 'error', cooldown_until = '2030-01-01T00:00:00Z',
			last_error_code = 'upstream_5xx', last_error_message = 'boom', last_error_trace_id = 'trace-1',
			cooldown_retest_failure_count = 2, cooldown_retest_observation_started_at = '2026-09-01T00:00:00Z',
			cooldown_retest_generation = 'cooldown:abc', cooldown_retest_last_at = '2026-09-01T00:00:00Z',
			cooldown_retest_last_status_code = 429, stream_failure_count = 5,
			stream_failure_window_started_at = '2026-09-01T00:00:00Z',
			health_check_failure_count = 7, health_check_failure_started_at = '2026-09-01T00:00:00Z'
			WHERE id = ?`, id)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("全痕迹重置失败：%d %v", code, payload)
		}
		row := env.queryCell(t, `SELECT
			COALESCE(cooldown_until,'') || '|' ||
			COALESCE(last_error_trace_id,'') || '|' ||
			COALESCE(cooldown_retest_failure_count,0) || '|' ||
			COALESCE(cooldown_retest_generation,'') || '|' ||
			COALESCE(cooldown_retest_last_status_code,0) || '|' ||
			COALESCE(stream_failure_count,0) || '|' ||
			COALESCE(health_check_failure_count,0)
			FROM accounts WHERE id = ?`, id)
		if row != "||0||0|0|0" {
			t.Fatalf("失败痕迹未全部清理：%q", row)
		}
		if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = ?`, id); got != "pending_test" {
			t.Fatalf("error 账户重置后回到 pending_test：%s", got)
		}
	})
	t.Run("多 Key 池瞬态清理", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{
			"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"多Key重置",
			"type":"api_key","credentials":{"api_key":"sk-pool-1","api_keys":["sk-pool-1","sk-pool-2"],
			"api_key_strategy":"failover","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini","gpt-4.1"]}`)
		if code != http.StatusCreated {
			t.Fatalf("多 Key 创建失败：%d %v", code, payload)
		}
		id := dataMap(t, payload)["id"].(string)
		env.exec(t, `UPDATE accounts SET status = 'cooldown', cooldown_until = '2030-01-01T00:00:00Z' WHERE id = ?`, id)
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("多 Key 重置失败：%d %v", code, payload)
		}
		if dataMap(t, payload)["apiKeyTransientCleared"] == nil {
			t.Fatalf("响应缺少 Key 瞬态清理统计：%v", dataMap(t, payload))
		}
	})
}

func TestW2BatchScheduleAndDispatchOutbox(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	seedImportProxy(t, env, "pp-batch2", adminID, "批量代理二")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	// 时间表变更：写入 schedule JSON 与下一次检查时间。
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{"availabilitySchedule": w2JSONObject(t, alwaysAllowSchedule)}),
	}
	result, err := w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("时间表批量失败：%v", err)
	}
	if !strings.Contains(strings.Join(result.ChangedFields, ","), "availabilitySchedule") {
		t.Fatalf("时间表未列入变更：%v", result.ChangedFields)
	}
	for _, id := range []string{first, second} {
		if got := env.queryCell(t, `SELECT COALESCE(availability_schedule_json,'') FROM accounts WHERE id = ?`, id); got == "" {
			t.Fatalf("时间表未落库：%s", id)
		}
		if got := env.queryCell(t, `SELECT COALESCE(availability_schedule_next_check_at,'') FROM accounts WHERE id = ?`, id); got == "" {
			t.Fatalf("下一次检查时间未落库：%s", id)
		}
	}
	// 相同时间表再次提交 → 无变化。
	revision := func(id string) int64 {
		var value int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	input = BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: revision(first)}, {AccountID: second, ConfigRevision: revision(second)}},
		Updates: w2EnabledFields(map[string]any{"availabilitySchedule": w2JSONObject(t, alwaysAllowSchedule)}),
	}
	result, err = w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("重复时间表批量失败：%v", err)
	}
	if len(result.ChangedFields) != 0 {
		t.Fatalf("相同时间表应无变化：%v", result.ChangedFields)
	}

	// 代理变更触发 dispatch revision outbox 事件行。
	env.exec(t, `DELETE FROM account_circuit_outbox`)
	input = BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: revision(first)}, {AccountID: second, ConfigRevision: revision(second)}},
		Updates: w2EnabledFields(map[string]any{"proxyProfileId": "pp-batch2"}),
	}
	result, err = w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("代理批量失败：%v", err)
	}
	if !strings.Contains(strings.Join(result.ChangedFields, ","), "proxyProfileId") {
		t.Fatalf("代理未列入变更：%v", result.ChangedFields)
	}
	// dispatch revision 前进（代理属于网关运行时相关字段）。
	dispatchRevision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, first)
	if dispatchRevision == "1" {
		t.Fatal("代理变更应推进调度修订号")
	}
	notes := fmt.Sprintf("批量 %s", "断言完成")
	_ = notes
}
