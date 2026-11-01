package accounts

// T91-2（R7-B B1-7 裁决「修」）：运行时重置对 quality_isolated 账户不仅恢复
// active + schedulable，还必须在同一事务里结束质量处罚行
// （account_quality_enforcements.state = 'cleared'，与 patch.go
// releaseAccountLockAndQualityEnforcement 的清理语义一致：清 recovery_due_at /
// 租约列并落 cleared_at）。pending_test / error 分支保持既有语义不变。

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// t91SeedQualityEnforcement 写入质量处罚活动行。
func t91SeedQualityEnforcement(t *testing.T, env *testEnv, accountID, ownerID, enforcementID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO account_quality_enforcements (account_id, system_account_id, enforcement_id,
			generation, state, updated_at, recovery_due_at, recovery_lease_owner, recovery_lease_until)
		VALUES (?, ?, ?, 2, 'active', ?, ?, 'lease-owner', ?)
		ON CONFLICT (account_id) DO UPDATE SET enforcement_id = EXCLUDED.enforcement_id,
			generation = EXCLUDED.generation, state = 'active', updated_at = EXCLUDED.updated_at,
			recovery_due_at = EXCLUDED.recovery_due_at, recovery_lease_owner = EXCLUDED.recovery_lease_owner,
			recovery_lease_until = EXCLUDED.recovery_lease_until, cleared_at = NULL`,
		accountID, ownerID, enforcementID, now, now, now)
}

func TestT91RuntimeResetClearsQualityEnforcement(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.store.SetRuntimeResetEffects(&fakeRuntimeEffects{})
	env.seedAccount(t, "acc-t91-q", adminID, "t91-q", "active")
	env.exec(t, `UPDATE accounts SET status = 'quality_isolated', schedulable = 0,
		last_error_code = 'model_quality_isolated', last_error_message = '模型质量不达标，已隔离'
		WHERE id = 'acc-t91-q'`)
	t91SeedQualityEnforcement(t, env, "acc-t91-q", adminID, "enf-t91-q")

	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-t91-q", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("质量隔离重置失败：%v", err)
	}
	if outcome.Result.Status != "active" || !outcome.Result.Schedulable {
		t.Fatalf("重置后应恢复 active + 可调度：%+v", outcome.Result)
	}
	if got := env.queryCell(t, `SELECT status || '|' || schedulable || '|' || COALESCE(last_error_code, '')
		FROM accounts WHERE id = 'acc-t91-q'`); got != "active|1|" {
		t.Fatalf("账户行未恢复：%s", got)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_quality_enforcements
		WHERE account_id = 'acc-t91-q' AND state = 'cleared'
			AND recovery_due_at IS NULL AND recovery_lease_owner IS NULL AND recovery_lease_until IS NULL
			AND cleared_at IS NOT NULL`) != 1 {
		t.Fatal("质量处罚行必须以 patch.go 同款语义结束（cleared + 清租约/排期 + cleared_at）")
	}
	clearedAt := env.queryCell(t, `SELECT COALESCE(cleared_at, '') FROM account_quality_enforcements WHERE account_id = 'acc-t91-q'`)
	if clearedAt == "" {
		t.Fatal("cleared_at 必须落值")
	}

	// 幂等：重复重置不再改写已结束的处罚行（cleared_at 不变），账户保持恢复态。
	second, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-t91-q",
		outcome.Result.ConfigRevision, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || second == nil {
		t.Fatalf("重复重置失败：%v", err)
	}
	if second.Result.Status != "active" || !second.Result.Schedulable {
		t.Fatalf("重复重置应保持恢复态：%+v", second.Result)
	}
	if got := env.queryCell(t, `SELECT COALESCE(cleared_at, '') FROM account_quality_enforcements WHERE account_id = 'acc-t91-q'`); got != clearedAt {
		t.Fatalf("重复重置不得改写 cleared_at：%s -> %s", clearedAt, got)
	}
	if rows := env.count(t, `SELECT COUNT(*) FROM account_quality_enforcements WHERE account_id = 'acc-t91-q'`); rows != 1 {
		t.Fatalf("不应产生新处罚行：%d", rows)
	}

	// HTTP 面：quality_isolated 不再进 skipped（治理已执行，不再是跳过项）。
	env.seedAccount(t, "acc-t91-q2", adminID, "t91-q2", "active")
	env.exec(t, `UPDATE accounts SET status = 'quality_isolated', schedulable = 0 WHERE id = 'acc-t91-q2'`)
	t91SeedQualityEnforcement(t, env, "acc-t91-q2", adminID, "enf-t91-q2")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-t91-q2/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("质量隔离重置 HTTP：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["status"] != "active" || data["schedulable"] != true {
		t.Fatalf("HTTP 重置后状态：%v", data)
	}
	if skipped := data["skipped"]; skipped != nil {
		for _, key := range skipped.([]any) {
			if key == "quality_isolated" {
				t.Fatalf("quality_isolated 不应再出现在 skipped：%v", skipped)
			}
		}
	}
}
