package accounts

// T91-1（R7-B B1-1b 裁决「修」）：流量迁移的源账户门。迁移会把源账户改写成
// disabled / temporary_unavailable，只有 status=active、schedulable=1 且无
// 未结束锁死（account_lock_states 无 ENGAGED / DEAD_CONFIRMED）的账户允许被
// 改写；error / pending_test / quality_isolated / 锁死账户必须先人工恢复，
// 返回明确 400。unchanged 只读预览不写源账户，保持在门之前返回。
//
// 两个分支各自钉住：自有账户（owner）走 trafficManagedRow 的列投影，授权实例
// 走 grantee 的摘要投影（实例行自身的 status/schedulable）。

import (
	"net/http"
	"testing"
	"time"
)

// t91BindGroupAccounts 把账户绑定到 owner 的默认分组（迁移的同组前置）。
func t91BindGroupAccounts(t *testing.T, env *testEnv, ownerID string, ids ...string) {
	t.Helper()
	groupID := "grp-default-" + ownerID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)
			ON CONFLICT (group_id, account_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at`,
			ownerID, groupID, id, now, now)
	}
}

// t91SeedAccountLock 写入账户锁死行（enabled=1 + 指定 lock_state）。
func t91SeedAccountLock(t *testing.T, env *testEnv, accountID, lockState string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, incident_id,
			incident_started_at, deadline_at, original_status, provenance, generation, updated_at)
		VALUES (?, 1, ?, 'incident-t91', ?, ?, 'active', 'manual', 1, ?)
		ON CONFLICT (account_id) DO UPDATE SET enabled = 1, lock_state = EXCLUDED.lock_state,
			incident_id = EXCLUDED.incident_id, generation = EXCLUDED.generation, updated_at = EXCLUDED.updated_at`,
		accountID, lockState, now, now, now)
}

func TestT91TrafficMigrationSourceGateOwner(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-t91-src", adminID, "源账户", "api_key", "active", Credentials{"api_key": "sk-t91-src"})
	env.seedM11Account(t, "acc-t91-dst", adminID, "目标账户", "api_key", "active", Credentials{"api_key": "sk-t91-dst"})
	t91BindGroupAccounts(t, env, adminID, "acc-t91-src", "acc-t91-dst")
	migratePath := "/__aisys__/api/accounts/acc-t91-src/traffic-migration"

	// 非 active 源：三种异常态一律 400 + 「先恢复账户」文案，且源行不改写。
	for _, status := range []string{"error", "pending_test", "quality_isolated"} {
		env.exec(t, `UPDATE accounts SET status = ?, config_revision = config_revision + 1 WHERE id = 'acc-t91-src'`, status)
		revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = 'acc-t91-src'`)
		code, payload := env.do(t, http.MethodPost, migratePath, `{"targetAccountId":"acc-t91-dst"}`)
		if code != http.StatusBadRequest || payload["message"] != trafficSourceNotActiveMessage {
			t.Fatalf("%s 源迁移应 400：%d %v", status, code, payload)
		}
		if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = 'acc-t91-src'`); got != status {
			t.Fatalf("%s 源行不得被改写：%s", status, got)
		}
		if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = 'acc-t91-src'`); got != revision {
			t.Fatalf("%s 源行 revision 不得变化：%s -> %s", status, revision, got)
		}
	}

	// active 但已关闭调度：同一门的第二臂。
	env.exec(t, `UPDATE accounts SET status = 'active', schedulable = 0 WHERE id = 'acc-t91-src'`)
	code, payload := env.do(t, http.MethodPost, migratePath, `{"targetAccountId":"acc-t91-dst"}`)
	if code != http.StatusBadRequest || payload["message"] != trafficSourceUnschedulableMessage {
		t.Fatalf("未调度源迁移应 400：%d %v", code, payload)
	}

	// 锁死态（ENGAGED）：status/schedulable 均正常也必须先恢复。
	env.exec(t, `UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = 'acc-t91-src'`)
	t91SeedAccountLock(t, env, "acc-t91-src", "ENGAGED")
	code, payload = env.do(t, http.MethodPost, migratePath, `{"targetAccountId":"acc-t91-dst"}`)
	if code != http.StatusBadRequest || payload["message"] != trafficSourceLockedMessage {
		t.Fatalf("锁死源迁移应 400：%d %v", code, payload)
	}

	// 只是「armed 未进入事件」的 LOCKED_IDLE 不属未结束锁死：放行。
	env.exec(t, `UPDATE account_lock_states SET lock_state = 'LOCKED_IDLE' WHERE account_id = 'acc-t91-src'`)
	code, payload = env.do(t, http.MethodPost, migratePath, `{"targetAccountId":"acc-t91-dst"}`)
	if code != http.StatusOK {
		t.Fatalf("LOCKED_IDLE 源应放行：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = 'acc-t91-src'`); got != "temporary_unavailable" {
		t.Fatalf("放行后应落临时不可调用：%s", got)
	}

	// unchanged 是只读预览：异常源同样返回 200 且源行保持原状（门不覆盖预览）。
	env.exec(t, `UPDATE accounts SET status = 'error' WHERE id = 'acc-t91-src'`)
	code, payload = env.do(t, http.MethodPost, migratePath,
		`{"targetAccountId":"acc-t91-dst","sourceStatus":"unchanged"}`)
	if code != http.StatusOK || dataMap(t, payload)["sourceStatus"] != "unchanged" {
		t.Fatalf("unchanged 预览应 200：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = 'acc-t91-src'`); got != "error" {
		t.Fatalf("unchanged 不得改写源：%s", got)
	}
}

func TestT91TrafficMigrationSourceGateAuthorized(t *testing.T) {
	env, authzStore, store := w13gTrafficAuthorizedEnv(t)
	ownerID := env.login(t, "t91-owner", "owner-pass", "user")
	memberID := env.login(t, "t91-member", "member-pass", "user")
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.seedProviderAndDefaultGroup(t, memberID)
	// w13gSeedTrafficInstance 内的 team 授权固定用 team-w13g-tm，团队行需同名。
	env.seedTeamMember(t, "team-w13g-tm", ownerID, memberID)
	instA := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-t91-tmsrc-a", "a")
	instB := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-t91-tmsrc-b", "b")
	memberAccess := AccessScope{ViewerID: memberID}

	// 实例行自身非 active → 拒绝。
	for _, status := range []string{"error", "pending_test", "quality_isolated"} {
		env.exec(t, `UPDATE accounts SET status = ? WHERE id = ?`, status, instA)
		if _, err := store.MigrateTraffic(t.Context(), instA,
			TrafficMigrationInput{TargetAccountID: instB}, memberAccess); err == nil ||
			err.Error() != trafficSourceNotActiveMessage {
			t.Fatalf("%s 实例迁移应拒绝：%v", status, err)
		}
	}

	// 实例行 active 但未调度 → 拒绝。
	env.exec(t, `UPDATE accounts SET status = 'active', schedulable = 0 WHERE id = ?`, instA)
	if _, err := store.MigrateTraffic(t.Context(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess); err == nil ||
		err.Error() != trafficSourceUnschedulableMessage {
		t.Fatalf("未调度实例迁移应拒绝：%v", err)
	}

	// 实例行 ENGAGED 锁死 → 拒绝。
	env.exec(t, `UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`, instA)
	t91SeedAccountLock(t, env, instA, "ENGAGED")
	if _, err := store.MigrateTraffic(t.Context(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess); err == nil ||
		err.Error() != trafficSourceLockedMessage {
		t.Fatalf("锁死实例迁移应拒绝：%v", err)
	}

	// 解锁并恢复 → 正常迁移。
	env.exec(t, `UPDATE account_lock_states SET enabled = 0, lock_state = 'UNLOCKED' WHERE account_id = ?`, instA)
	result, err := store.MigrateTraffic(t.Context(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess)
	if err != nil || result == nil || result.SourceStatus != string(trafficSourceTemporaryUnavailable) {
		t.Fatalf("正常实例迁移：%+v %v", result, err)
	}

	// unchanged 预览不写实例行，异常实例同样放行。
	env.exec(t, `UPDATE accounts SET status = 'error' WHERE id = ?`, instA)
	preview, err := store.MigrateTraffic(t.Context(), instA,
		TrafficMigrationInput{TargetAccountID: instB, SourceStatus: trafficSourceUnchanged}, memberAccess)
	if err != nil || preview == nil || preview.SourceStatus != string(trafficSourceUnchanged) {
		t.Fatalf("unchanged 预览：%+v %v", preview, err)
	}
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = ?`, instA); got != "error" {
		t.Fatalf("unchanged 不得改写实例：%s", got)
	}
}
