package accounts

import (
	"net/http"
	"testing"
)

// owner 列表“恢复可调度 / 异常恢复”（PATCH clearFailureState）的原子状态翻转
// 回归（BUG-0288）：
//
//	契约来源  AI账户健康状态收敛设计.md §5 与核心功能设计 §191：人工恢复是
//	        显式确认——恢复语义为状态原子翻转，恢复后不得残留“冷却态但无
//	        冷却锚点”的账户行；同仓先例是授权实例路径
//	        m11_authorized_dispatch.go patchAuthorizedDispatchTx 的
//	        clearFailure → active + schedulable。
//	缺陷形态  修复前 owner 路径只清 last_error_*/cooldown_until/retest 锚点
//	        不改 status，冷却态行锚点全空后被 jobs J1 validCooldownFence
//	        fail-closed 判 direct_input_invalid 隔离，复测永远够不着，
//	        账户永久卡死“临时不可调用”。
//	目标裁定  temporary_unavailable / rate_limited → active（受时间计划与
//	        套餐到期约束，计划当前不生效落 disabled）；error → 不可调度的
//	        pending_test 并立即投递后台激活检查（路由尾部
//	        DispatchAccountHealthCheck，reason 沿用词表 configuration）。
//	不回归    active 行的锚点清理语义（既有 clearFailureState 行为）保持；
//	        混交守卫、revision CAS、assertStatusMutationAllowed 不变。

// seedRestoreAccount 走真实创建链建一个 active 账户再种冷却/错误投影，贴近
// 生产恢复动作的入口形态。
func seedRestoreAccount(t *testing.T, env *testEnv, name, status string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, status)
	return id
}

// TestPatchClearFailureStateRestoresCoolingToActive：temporary_unavailable /
// rate_limited 冷却态恢复后原子翻转为 active + schedulable，冷却/错误/retest
// 锚点族清空——不再产生会被 J1 fence 隔离的“冷却态但锚点空”行（BUG-0288）。
func TestPatchClearFailureStateRestoresCoolingToActive(t *testing.T) {
	for _, status := range []string{"temporary_unavailable", "rate_limited"} {
		t.Run(status, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			id := seedRestoreAccount(t, env, "restore-"+status, status)
			// 生产冷却态 schedulable 已被压 0，恢复必须重新开启调度。
			env.exec(t, `UPDATE accounts SET schedulable = 0 WHERE id = ?`, id)

			code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
				`{"expectedConfigRevision":1,"clearFailureState":true}`)
			if code != http.StatusOK {
				t.Fatalf("%s restore: %d %v", status, code, patched)
			}
			changed := changedFieldSet(t, patched)
			if !changed["clearFailureState"] || !changed["status"] {
				t.Fatalf("%s changedFields must carry clearFailureState and status: %v", status, changed)
			}
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
				AND status = 'active' AND schedulable = 1
				AND cooldown_until IS NULL
				AND cooldown_retest_generation IS NULL
				AND cooldown_retest_observation_started_at IS NULL
				AND last_error_code IS NULL
				AND last_error_message IS NULL
				AND config_revision = 2`, id) != 1 {
				t.Fatalf("%s row must atomically flip to active with all anchors cleared", status)
			}
		})
	}
}

// TestPatchClearFailureStateRestoresErrorToPendingTest：error 恢复进入不可
// 调度的 pending_test，立即投递后台激活检查并清掉恢复前错误标记（BUG-0288；
// 归一化的“账户配置已保存，等待后台检查”对恢复语义不准确，恢复即清）。
func TestPatchClearFailureStateRestoresErrorToPendingTest(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	effects := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(effects)
	id := seedRestoreAccount(t, env, "restore-error", "error")
	// 预置既有复检排期，验证恢复清掉旧排期并立即重新派发。
	env.exec(t, `UPDATE accounts SET next_health_check_at = '2026-09-01T01:00:00.000Z' WHERE id = ?`, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusOK {
		t.Fatalf("error restore: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'pending_test' AND schedulable = 0
		AND next_health_check_at IS NULL
		AND last_error_message IS NULL
		AND cooldown_until IS NULL`, id) != 1 {
		t.Fatal("error restore must land unschedulable pending_test with the error marker cleared")
	}
	effects.mu.Lock()
	dispatches := append([][2]string{}, effects.healthCheckDispatches...)
	effects.mu.Unlock()
	// 创建链自带一条 activation 探针（既有行为）；恢复必须精确追加一条
	// configuration 激活检查，不得复用/吞掉既有排期。
	configurationDispatches := 0
	for _, dispatch := range dispatches {
		if dispatch == [2]string{id, "configuration"} {
			configurationDispatches++
		}
	}
	if configurationDispatches != 1 {
		t.Fatalf("error restore must dispatch exactly one configuration check, got %v", dispatches)
	}
}

// TestPatchClearFailureStateKeepsActiveRowActive：active + 冷却/错误残留的
// 恢复不翻转状态（无恢复目标），仅保留既有锚点清理语义（不回归）。
func TestPatchClearFailureStateKeepsActiveRowActive(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := seedRestoreAccount(t, env, "restore-active", "active")

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusOK {
		t.Fatalf("active restore: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if changed["status"] {
		t.Fatalf("active row must not flip status: %v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'active'
		AND cooldown_until IS NULL
		AND last_error_code IS NULL
		AND cooldown_retest_generation IS NULL`, id) != 1 {
		t.Fatal("active row must keep its status while the anchors are cleared")
	}
}

// TestPatchClearFailureStateDefersToClosedSchedule：人工恢复仍受可用时间计划
// 约束——行内计划当前不生效时恢复落 disabled（对齐 ForceActivatePending），
// 不产生计划外的 active 行（BUG-0288）。
func TestPatchClearFailureStateDefersToClosedSchedule(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := seedRestoreAccount(t, env, "restore-schedule", "temporary_unavailable")
	// dateRange 已整体过期：任何执行时刻都不允许，对测试运行时钟鲁棒。
	env.exec(t, `UPDATE accounts SET availability_schedule_json = ? WHERE id = ?`,
		`{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		  "windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],
		  "dateRange":{"startDate":"2020-01-01","endDate":"2020-01-02"}}`, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusOK {
		t.Fatalf("closed-schedule restore: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'disabled'
		AND cooldown_until IS NULL`, id) != 1 {
		t.Fatal("restore must land disabled while the availability schedule is closed")
	}
}

// TestPatchClearFailureStateExpiredPackageRejected：套餐已到期时恢复到 active
// 触发既有启用守卫，400 校验错误、行完全不翻转（BUG-0288 复用状态机到期臂）。
func TestPatchClearFailureStateExpiredPackageRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := seedRestoreAccount(t, env, "restore-expired", "temporary_unavailable")
	env.exec(t, `UPDATE accounts SET account_expires_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, id)

	code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusBadRequest || rejected["message"] != "账户套餐已到期，不能启用或参与调度" {
		t.Fatalf("expired package must reject the restore: %d %v", code, rejected)
	}
	// 拒绝路径不得触碰行：状态、锚点与版本全部保持播种值。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'temporary_unavailable' AND config_revision = 1
		AND cooldown_retest_generation = 'cooldown:seed-generation'`, id) != 1 {
		t.Fatal("rejected restore must leave the row untouched")
	}
}
