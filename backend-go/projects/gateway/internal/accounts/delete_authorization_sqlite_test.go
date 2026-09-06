package accounts

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// 归档 SQLite 删除臂的逐 grant 运行态同步域断言
// (revokeAccountAuthorizationsForDeletedResource,
// account-delete-cleanup.repository.ts:479-492 → revokeResourceAuthorizationGrant,
// write-state.repository.ts:726-733)。接线的 authz 端口让 Delete 把账户资源的
// 活跃 grant 逐个送进 authz 的运行态同步域；与批量臂（上方
// TestDeleteAuthorizationChainTerminalStatePreservation 钉住的 PG 语义）的
// 可观察差异全部钉在这里：
//   - manual source 的 ended_reason 缺省 'authorization_revoked'（非
//     'account_deleted'）；
//   - runtime 终态经条件式 refresh 得到 revoked_by/revoked_reason 直接写为
//     删除者/'authorization_revoked'（非 COALESCE 保留）；
//   - 团队 grant 级联回收团队 source（ended_reason='team_revoked'）；
//   - 配额小时窗绑定按 grant 重同步——已终态（returned）grant 名下的绑定
//     保留（批量臂会按资源全量删除，这是双方言的既定差异）；
//   - 每个被回收的 grant 都触发一次账户健康快照扇出
//     (reason='authorization_grant_changed')。
func TestDeleteAuthorizationChainSQLitePerGrantSync(t *testing.T) {
	env := newTestEnv(t)
	authzStore, err := authz.NewStore(env.db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.store.SetDeletedResourceGrantRevoker(authzStore)
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

	// Direct grant + runtime + active manual source: the manual-source-scoped
	// revoke with the 'authorization_revoked' reason is the arm's signature.
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('rg-live', 'account', ?, ?, 'system_account', 'sys-grantee', 'active', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	e(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, revoked_by, created_by, created_at, updated_at)
		VALUES ('ra-live', 'account', ?, ?, 'sys-grantee', 'active', 'manual', 'prev-actor', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	e(`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('rs-live', 'ra-live', 'manual', 'active', 'sys-grantee', ?, ?)`, now, now)

	// Team grant + runtime + active team source: the member cascade lands
	// through the same per-grant sync domain.
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_team_id, status, created_by, created_at, updated_at)
		VALUES ('rg-team', 'account', ?, ?, 'team', 'team-1', 'active', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	e(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, effective_source_team_id, created_by, created_at, updated_at)
		VALUES ('ra-member', 'account', ?, ?, 'sys-member', 'active', 'team', 'team-1', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	e(`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, source_team_id, status, created_by, created_at, updated_at)
		VALUES ('rs-member-team', 'ra-member', 'team', 'team-1', 'active', 'sys-grantee', ?, ?)`, now, now)

	// A returned grant is skipped by the per-grant scan (its rows and its
	// quota scope binding stay untouched).
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, revoked_by, revoked_at, created_by, created_at, updated_at)
		VALUES ('rg-returned', 'account', ?, ?, 'system_account', 'sys-grantee', 'returned', 'returner-1',
		'2026-08-01T00:00:00.000Z', 'sys-grantee', ?, ?)`, id, adminID, now, now)

	// Quota scope bindings: the live grant's binding is re-synced away; the
	// returned grant's binding survives (dialect difference against the bulk
	// arm, which drops the whole resource's bindings).
	e(`INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id,
		source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('sys-grantee', 'account_authorization', 'ra-live', 'resource_authorization_grant', 'rg-live', 1, ?, ?)`, now, now)
	e(`INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id,
		source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('sys-grantee', 'account_authorization', 'ra-returned', 'resource_authorization_grant', 'rg-returned', 1, ?, ?)`, now, now)

	// The authorization-instance account fans one health snapshot per revoked
	// account grant (rg-live and rg-team) before the soft delete lands.
	e(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-inst', 'sys-grantee', 'gpt', 'prof-gpt', 'openai', 'v1', '授权实例', 'api_key', 'active',
		'sealed', '***', 'gpt-4o-mini', ?, ?, ?)`, id, now, now)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}

	// Grant rows: the direct overwrite carries the deleting actor; the
	// returned grant keeps its original terminal stamp.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-live'
		AND status = 'revoked' AND revoked_by = ? AND revoked_at IS NOT NULL`, adminID) != 1 {
		t.Fatal("live grant must be revoked with the deleting actor overwrite")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-team'
		AND status = 'revoked' AND revoked_by = ?`, adminID) != 1 {
		t.Fatal("team grant must be revoked by the deleting actor")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-returned'
		AND status = 'returned' AND revoked_by = 'returner-1'`) != 1 {
		t.Fatal("returned grant must stay untouched by the per-grant scan")
	}

	// Manual source: revoked with the runtime-sync reason (not account_deleted).
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-live'
		AND status = 'revoked' AND ended_reason = 'authorization_revoked' AND revoked_by = ?
		AND ended_at IS NOT NULL`, adminID) != 1 {
		t.Fatal("manual source must flip with the authorization_revoked reason")
	}
	// Team source: the member cascade marks it team_revoked.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-member-team'
		AND status = 'revoked' AND ended_reason = 'team_revoked'`) != 1 {
		t.Fatal("team source must be revoked by the team cascade")
	}

	// Runtime rows: the direct grant terminal refresh writes the deleting
	// actor and the authorization_revoked reason over the earlier stamps
	// (preserveExpired=false); the team-carried runtime uses that same explicit
	// terminal reason because the archived team cascade passes
	// noActiveSourceReason='authorization_revoked' with preserveExpired=false.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-live'
		AND status = 'revoked' AND revoked_reason = 'authorization_revoked' AND revoked_by = ?
		AND effective_source_type IS NULL AND last_source_changed_at IS NOT NULL`, adminID) != 1 {
		t.Fatal("direct runtime must reach the revoked terminal with overwritten stamps")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-member'
		AND status = 'revoked' AND revoked_reason = 'authorization_revoked'
		AND effective_source_type IS NULL`) != 1 {
		t.Fatal("team runtime must reach the terminal branch of the shared refresh")
	}

	// Quota scope bindings: re-synced per grant, terminal grants keep theirs.
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings
		WHERE source_id = 'rg-live'`) != 0 {
		t.Fatal("the revoked grant's binding must be re-synced away")
	}
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings
		WHERE source_id = 'rg-returned'`) != 1 {
		t.Fatal("the skipped returned grant keeps its binding (dialect difference)")
	}

	// Health fanout: one snapshot per revoked account-bearing grant, inside
	// the delete transaction (before the instance soft delete).
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox
		WHERE account_id = 'acc-inst' AND event_kind = 'snapshot'
		AND reason = 'authorization_grant_changed'`) != 2 {
		t.Fatal("each revoked account grant must fan one health snapshot")
	}
	// The delete itself still lands: source account and instance are soft
	// deleted and the instance carries its account_deleted tombstone.
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND deleted_at IS NOT NULL`, id) != 1 {
		t.Fatal("the source account must still be soft deleted")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-inst' AND deleted_at IS NOT NULL`) != 1 {
		t.Fatal("the instance account must ride the soft delete")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox
		WHERE account_id = 'acc-inst' AND event_kind = 'tombstone'
		AND reason = 'account_deleted'`) != 1 {
		t.Fatal("the instance account must carry its account_deleted tombstone")
	}
}

// Unwired stores keep the bulk arm verbatim: the archived PG semantics stay
// the fallback for the PG dialect and for fixtures without the authz port
// (the bulk-arm assertions live in
// TestDeleteAuthorizationChainTerminalStatePreservation).
func TestDeleteAuthorizationChainSQLiteFallbackWithoutPort(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	now := "2026-09-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('rg-bulk', 'account', ?, ?, 'system_account', 'sys-grantee', 'active', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-bulk', 'account', ?, ?, 'sys-grantee', 'active', 'manual', 'sys-grantee', ?, ?)`,
		id, adminID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('rs-bulk', 'ra-bulk', 'manual', 'active', 'sys-grantee', ?, ?)`, now, now)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	// Bulk arm signature: the source reason is account_deleted, not the
	// per-grant authorization_revoked.
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-bulk'
		AND status = 'revoked' AND ended_reason = 'account_deleted'`) != 1 {
		t.Fatal("the unwired store must keep the bulk arm semantics")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-bulk'
		AND status = 'revoked' AND revoked_reason = 'account_deleted'`) != 1 {
		t.Fatal("the unwired store must keep the bulk runtime terminal")
	}
}
