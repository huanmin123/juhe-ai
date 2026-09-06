package accounts

import (
	"net/http"
	"testing"
	"time"
)

// PATCH 混交校验与 bounded-recovery 初始退避配对回归：
//
//	混交校验  归档 account-management-patch.repository.ts:1116-1119
//	        patchAccountFailureStateInTransaction：clearFailureState=true 是
//	        独立的重新检查/异常恢复命令，与任何字段修改同时提交时以
//	        「重新检查或异常恢复不能与账户字段修改同时提交」拒绝（400）；
//	        revision CAS 先行，过期版本优先 409（归档 :264-266 → :287 分流）。
//	初始退避  归档 account-runtime-mutation-helpers.ts:9,36-55
//	        temporaryUnavailableInitialBackoffSeconds = 3：bounded-recovery
//	        两个臂（:664 来源账户观察窗口重启、:837 授权实例传播）重武装的
//	        cooldown_until 都是 now+3s，与冷却重试退避表首项一致
//	        （归档 JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_BACKOFF_MS / Go jobs
//	        opsjobs.CircuitBackoffMS[0] = 3_000）。

// TestPatchClearFailureStateRejectsMixedFieldEdits pins the archive mixed-commit
// guard: a clearFailureState=true command riding any field edit is rejected with
// 400 + the archive message, the row stays untouched, and the pure command still
// succeeds afterwards (the guard must not poison the account).
func TestPatchClearFailureStateRejectsMixedFieldEdits(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("mixed"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "temporary_unavailable")

	// 代表性混交样本：标量字段、可空字段、开关字段各取其一，覆盖
	// patchHasMixedEditField 的三个形态（指针/Present 布尔/探活开关）。
	mixedBodies := []string{
		`{"expectedConfigRevision":1,"clearFailureState":true,"name":"renamed"}`,
		`{"expectedConfigRevision":1,"clearFailureState":true,"notes":"note"}`,
		`{"expectedConfigRevision":1,"clearFailureState":true,"status":"active"}`,
		`{"expectedConfigRevision":1,"clearFailureState":true,"schedulable":true}`,
		`{"expectedConfigRevision":1,"clearFailureState":true,"temporaryUnavailableContinuousProbeEnabled":false}`,
	}
	for _, body := range mixedBodies {
		code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, body)
		if code != http.StatusBadRequest || rejected["message"] != "重新检查或异常恢复不能与账户字段修改同时提交" {
			t.Fatalf("mixed commit must be rejected with the archive message: %d %v (body %s)", code, rejected, body)
		}
	}
	// 拒绝路径不得触碰行：状态/版本/冷却投影全部保持播种值。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'temporary_unavailable' AND name = 'mixed' AND config_revision = 1
		AND cooldown_retest_generation = 'cooldown:seed-generation'
		AND cooldown_retest_observation_started_at = '2026-09-01T00:00:00.000Z'`, id) != 1 {
		t.Fatal("rejected mixed commit must leave the row untouched")
	}

	// 纯命令仍然可用：只带 expectedConfigRevision 的 clearFailureState=true
	// 正常恢复（changedFields == ['clearFailureState']）。
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusOK {
		t.Fatalf("pure clearFailureState patch: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if len(changed) != 1 || !changed["clearFailureState"] {
		t.Fatalf("pure command changedFields must be exactly clearFailureState: %v", changed)
	}
}

// TestPatchClearFailureStateFalseWithFieldsStillEdits mirrors the archive
// routing (:287-289 + :319-321): clearFailureState=false routes to the field
// patch and the key itself is filtered out of the requested keys, so field
// edits with an explicit false stay legal.
func TestPatchClearFailureStateFalseWithFieldsStillEdits(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("explicit-false"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":false,"notes":"edited"}`)
	if code != http.StatusOK {
		t.Fatalf("patch with explicit false: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if len(changed) != 1 || !changed["notes"] {
		t.Fatalf("explicit false must not enter changedFields: %v", changed)
	}
}

// TestPatchClearFailureStateMixedDefersToRevisionConflict pins the archive
// ordering (:264-266 CAS before :1116-1119 mixed guard): a stale revision with
// mixed fields renders 409 first, not the mixed-commit 400.
func TestPatchClearFailureStateMixedDefersToRevisionConflict(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("stale-mixed"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":99,"clearFailureState":true,"name":"renamed"}`)
	if code != http.StatusConflict || conflict["message"] != RevisionConflictMessage {
		t.Fatalf("stale revision must win over the mixed guard: %d %v", code, conflict)
	}
}

// TestPatchBoundedRecoveryArmsThreeSecondBackoff pins the archive 3-second
// initial backoff on both bounded-recovery arms: the temporary_unavailable
// source account (:664) and its propagated authorization instances (:837)
// re-arm cooldown_until ≈ now+3s when the continuous probe switches off.
func TestPatchBoundedRecoveryArmsThreeSecondBackoff(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("backoff-source"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, sourceID, "temporary_unavailable")
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-backoff", "temporary_unavailable")

	before := time.Now().UTC()
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision":1,"temporaryUnavailableContinuousProbeEnabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}

	assertThreeSecondWindow := func(label, value string) {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("%s cooldown_until not a timestamp: %q %v", label, value, err)
		}
		// 与 TestPatchStatusMutationGuards 同款窗口：绕过毫秒级往返抖动，
		// 命中 now+3s 而排除 5 分钟旧实现。
		delay := parsed.Sub(before)
		if delay < 2*time.Second || delay > 6*time.Second {
			t.Fatalf("%s must arm the 3s initial backoff, got %v", label, delay)
		}
	}
	assertThreeSecondWindow("temporary_unavailable source",
		env.queryCell(t, `SELECT cooldown_until FROM accounts WHERE id = ?`, sourceID))
	assertThreeSecondWindow("propagated temporary_unavailable instance",
		env.queryCell(t, `SELECT cooldown_until FROM accounts WHERE id = 'inst-backoff'`))
}
