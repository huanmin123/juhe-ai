package accounts

import (
	"net/http"
	"testing"
)

// 授权实例探活开关传播链回归（accounts 波登记缺口）：来源账户 PATCH 翻转
// temporaryUnavailableContinuousProbeEnabled 时，授权实例账户在同一事务内
// 跟进同款 UPDATE（归档 account-management-patch.repository.ts :805-843
// continuousProbeChanged 臂的逐字段移植）。

// seedProbePropagationSource 创建来源账户（普通 gpt api_key 账户）并返回 id。
func seedProbePropagationSource(t *testing.T, env *testEnv, name string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
	if code != http.StatusCreated {
		t.Fatalf("create source %s: %d %v", name, code, payload)
	}
	return dataMap(t, payload)["id"].(string)
}

// seedProbePropagationInstance plants an authorization instance account row
// (authorization_instance_source_account_id → source) with the given status
// and a fully populated cooldown-retest projection.
func (e *testEnv) seedProbePropagationInstance(t *testing.T, adminID, sourceID, instanceID, status string) {
	t.Helper()
	now := "2026-09-01T00:00:00.000Z"
	e.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('ra-'||?, 'account', ?, ?, 'sys-grantee', 'active', 'sys-grantee', ?, ?)`, instanceID, sourceID, adminID, now, now)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, config_revision,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		temporary_unavailable_continuous_probe_enabled,
		cooldown_retest_failure_count, cooldown_retest_observation_started_at, cooldown_retest_generation,
		cooldown_retest_last_at, cooldown_retest_last_status_code, cooldown_until,
		created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', ?, 'cipher', 'sk-***', 'm', 1,
		?, 'ra-'||?, 1,
		2, '2026-08-31T23:00:00.000Z', 'gen-old',
		'2026-08-31T23:30:00.000Z', 429, '2026-09-01T00:05:00.000Z',
		?, ?)`, instanceID, adminID, "instance-"+instanceID, status, sourceID, instanceID, now, now)
}

// TestPatchProbeSwitchPropagatesToAuthorizationInstances pins the Node
// instance follow-up UPDATE: the switch fans out with a config revision bump,
// temporary_unavailable instances get the bounded-recovery reset via the CASE
// WHEN arms, other statuses keep their cooldown projection, and soft-deleted
// instances are excluded.
func TestPatchProbeSwitchPropagatesToAuthorizationInstances(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	sourceID := seedProbePropagationSource(t, env, "probe-source")
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-temp", "temporary_unavailable")
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-active", "active")
	// 软删除实例不参与传播（WHERE deleted_at IS NULL）。
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-deleted", "temporary_unavailable")
	env.exec(t, `UPDATE accounts SET deleted_at = '2026-09-01T01:00:00.000Z' WHERE id = 'inst-deleted'`)

	// 关闭来源账户的持续恢复探活（默认开 → 关；来源账户 active，不触发
	// 其自身 bounded recovery 列重置）。
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": 1, "temporaryUnavailableContinuousProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("patch probe off: %d %v", code, patched)
	}

	// 来源账户自身：开关落 0、revision +1（active 状态无冷却列重置）。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND temporary_unavailable_continuous_probe_enabled = 0
		AND config_revision = 2 AND cooldown_retest_failure_count = 0`, sourceID) != 1 {
		t.Fatal("source account switch must persist without cooldown reset (active status)")
	}
	// temporary_unavailable 实例：开关跟随 + bounded recovery 列全部重置。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-temp'
		AND temporary_unavailable_continuous_probe_enabled = 0
		AND config_revision = 2
		AND cooldown_retest_failure_count = 0
		AND cooldown_retest_observation_started_at IS NOT NULL
		AND cooldown_retest_generation IS NOT NULL AND cooldown_retest_generation <> 'gen-old'
		AND cooldown_retest_last_at IS NULL
		AND cooldown_retest_last_status_code IS NULL
		AND cooldown_until IS NOT NULL AND cooldown_until <> '2026-09-01T00:05:00.000Z'`) != 1 {
		t.Fatal("temporary_unavailable instance must follow the switch with bounded recovery reset")
	}
	// active 实例：开关跟随 + revision +1，冷却投影保持原值（CASE WHEN ELSE 臂）。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-active'
		AND temporary_unavailable_continuous_probe_enabled = 0
		AND config_revision = 2
		AND cooldown_retest_failure_count = 2
		AND cooldown_retest_observation_started_at = '2026-08-31T23:00:00.000Z'
		AND cooldown_retest_generation = 'gen-old'
		AND cooldown_retest_last_at = '2026-08-31T23:30:00.000Z'
		AND cooldown_retest_last_status_code = 429
		AND cooldown_until = '2026-09-01T00:05:00.000Z'`) != 1 {
		t.Fatal("active instance must follow the switch but keep its cooldown projection")
	}
	// 软删除实例不被触碰。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-deleted'
		AND config_revision = 1 AND temporary_unavailable_continuous_probe_enabled = 1
		AND cooldown_retest_failure_count = 2`) != 1 {
		t.Fatal("soft-deleted instance must stay untouched")
	}

	// 重新打开：开关传回 1，activated=false 时冷却列不再被改写。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": 2, "temporaryUnavailableContinuousProbeEnabled": true}`)
	if code != http.StatusOK {
		t.Fatalf("patch probe on: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-temp'
		AND temporary_unavailable_continuous_probe_enabled = 1
		AND config_revision = 3`) != 1 {
		t.Fatal("re-enable must fan the flag back with a revision bump")
	}

	// 未翻转（同值重复提交）：实例 revision 不再递增。
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": 3, "temporaryUnavailableContinuousProbeEnabled": true}`)
	if code != http.StatusOK {
		t.Fatalf("patch probe unchanged: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-temp' AND config_revision = 3`) != 1 {
		t.Fatal("unchanged switch must not bump instance revisions")
	}
	// 完全不含该字段的 PATCH 同样不触碰实例。
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID, `{"expectedConfigRevision": 3, "notes": "touch"}`)
	if code != http.StatusOK {
		t.Fatalf("patch notes: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-temp' AND config_revision = 3`) != 1 {
		t.Fatal("unrelated patch must not touch instances")
	}
}

// TestPatchProbeSwitchSourceBoundedRecoveryIntact keeps the existing source
// account bounded-recovery arm observable alongside the new instance fan-out:
// a temporary_unavailable source gets its own cooldown reset exactly once.
func TestPatchProbeSwitchSourceBoundedRecoveryIntact(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	sourceID := seedProbePropagationSource(t, env, "probe-source-temp")
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable',
		cooldown_retest_failure_count = 3, cooldown_retest_generation = 'src-gen-old',
		cooldown_retest_last_at = '2026-08-31T23:30:00.000Z', cooldown_retest_last_status_code = 500
		WHERE id = ?`, sourceID)
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-child", "temporary_unavailable")

	code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": 1, "temporaryUnavailableContinuousProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND temporary_unavailable_continuous_probe_enabled = 0
		AND cooldown_retest_failure_count = 0
		AND cooldown_retest_generation IS NOT NULL AND cooldown_retest_generation <> 'src-gen-old'
		AND cooldown_retest_last_at IS NULL`, sourceID) != 1 {
		t.Fatal("temporary_unavailable source must keep its own bounded recovery reset")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-child'
		AND config_revision = 2 AND cooldown_retest_generation IS NOT NULL AND cooldown_retest_generation <> 'src-gen-old'`) != 1 {
		t.Fatal("instance must receive its own fresh generation")
	}
}
