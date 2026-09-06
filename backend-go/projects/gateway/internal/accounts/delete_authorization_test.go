package accounts

import (
	"net/http"
	"testing"
)

// 第三轮常驻审查 #2 的终态断言：Delete 的资源授权链回收镜像归档 PG 异步臂
// (revokeAccountAuthorizationsForDeletedResourceAsync,
// account-delete-cleanup.repository.ts:420-492)。归档两个方言在该链路上都
// 终态 'revoked'；方言差异在到达方式（SQLite 逐 grant 运行态同步域 vs PG
// 批量臂），不在终态。本测试钉住 PG 批量臂的边界保留规则——它们就是 Go
// 侧可观察的「双方言终态裁决」：
//   - grants：status NOT IN ('revoked','returned') 过滤，COALESCE 保留既有
//     revoked_by/revoked_at；
//   - sources：只翻 'active'/'superseded'，COALESCE 保留 ended_at，ended_reason
//     缺省 'account_deleted'；
//   - authorizations：status <> 'returned' 过滤（已 returned 行完全不动），
//     COALESCE 保留 revoked_by/revoked_at/revoked_reason，effective source
//     置空；
//   - 配额小时窗绑定按资源全量删除（含已终态 grant 名下的绑定）。
func TestDeleteAuthorizationChainTerminalStatePreservation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	now := "2026-09-01T00:00:00.000Z"
	e := func(q string, args ...any) {
		t.Helper()
		env.exec(t, q, args...)
	}

	// Grant already at a terminal state must stay untouched (including its
	// earlier revoked_by stamp).
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, revoked_by, revoked_at, created_by, created_at, updated_at)
		VALUES ('rg-returned', 'account', ?, ?, 'system_account', 'sys-grantee', 'returned', 'returner-1',
		'2026-08-01T00:00:00.000Z', 'sys-grantee', ?, ?)`, id, adminID, now, now)
	// Live grant becomes revoked with the deleting actor.
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('rg-active', 'account', ?, ?, 'system_account', 'sys-grantee', 'active', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)

	// Authorization already returned: excluded by status <> 'returned' and
	// fully preserved (its sources keep standing).
	e(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-returned', 'account', ?, ?, 'sys-grantee', 'returned', 'manual', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	e(`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('rs-of-returned', 'ra-returned', 'manual', 'active', 'sys-grantee', ?, ?)`, now, now)

	// Live authorization: revoked with reason account_deleted, earlier
	// revoked_by preserved via COALESCE, effective source nulled.
	e(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, revoked_by, created_by, created_at, updated_at)
		VALUES ('ra-active', 'account', ?, ?, 'sys-grantee', 'active', 'manual', 'prev-actor', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	// Superseded source flips too; an earlier ended_at survives the COALESCE.
	e(`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, ended_at, created_by, created_at, updated_at)
		VALUES ('rs-superseded', 'ra-active', 'team', 'superseded', '2026-08-15T00:00:00.000Z', 'sys-grantee', ?, ?)`, now, now)
	// Quota bindings ride the whole-resource deletion (even for the returned
	// grant).
	e(`INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id,
		source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('sys-grantee', 'account_authorization', 'ra-active', 'resource_authorization_grant', 'rg-active', 1, ?, ?)`, now, now)
	e(`INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id,
		source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('sys-grantee', 'account_authorization', 'ra-returned', 'resource_authorization_grant', 'rg-returned', 1, ?, ?)`, now, now)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}

	// Grants: returned row untouched, live row revoked by the actor.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-returned'
		AND status = 'returned' AND revoked_by = 'returner-1'`) != 1 {
		t.Fatal("returned grant must stay returned with its original stamp")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-active'
		AND status = 'revoked' AND revoked_by = ? AND revoked_at IS NOT NULL`, adminID) != 1 {
		t.Fatal("live grant must be revoked by the deleting actor")
	}

	// Authorizations: returned row fully preserved, live row terminal.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-returned'
		AND status = 'returned' AND effective_source_type = 'manual' AND revoked_reason IS NULL`) != 1 {
		t.Fatal("returned authorization must be excluded from the bulk revoke")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-active'
		AND status = 'revoked' AND revoked_reason = 'account_deleted' AND revoked_by = 'prev-actor'
		AND effective_source_type IS NULL AND effective_source_team_id IS NULL
		AND last_source_changed_at IS NOT NULL`) != 1 {
		t.Fatal("live authorization must reach the revoked terminal state with preserved stamps")
	}

	// Sources: only the returned authorization's sources keep standing.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-of-returned'
		AND status = 'active' AND ended_reason IS NULL`) != 1 {
		t.Fatal("sources of the returned authorization must stay active")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-superseded'
		AND status = 'revoked' AND ended_reason = 'account_deleted'
		AND ended_at = '2026-08-15T00:00:00.000Z'`) != 1 {
		t.Fatal("superseded source must flip with preserved ended_at and reason account_deleted")
	}

	// Quota scope bindings: dropped for the whole resource.
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings
		WHERE source_id IN ('rg-active', 'rg-returned')`) != 0 {
		t.Fatal("quota scope bindings of the resource must be dropped regardless of grant state")
	}
}
