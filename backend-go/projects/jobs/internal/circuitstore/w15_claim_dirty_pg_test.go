package circuitstore

// w15_claim_dirty_pg_test.go 在 pgtestkit 隔离 PG 测试库（生产对齐 schema，
// openListAvailabilityFixture 同款 kit 夹具）上真实运行 ClaimDirty 的 PG CTE
// 分支（clock_timestamp + FOR UPDATE SKIP LOCKED）。原 shared.env 门禁共享
// 覆盖库装配已退役：每测试独立 fresh 库，PG 不可达由 pgtestkit 显式 Fatal
// 而非 t.Skip；隔离库随测试结束删除，不再需要行级清理。连接串不进日志/断言。

import (
	"context"
	"testing"
	"time"
)

func TestW15ClaimDirtyPostgres(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// viewer 父行与账户父行（kit 最小列形；kit schema 无 FK，父行仅为保持
	// 「脏行归属账户」的夹具语义）。
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id) VALUES ('w15-sys') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("system_accounts 种子失败: %v", err)
	}
	const acct = "w15-acct"
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.accounts (id, system_account_id)
		VALUES ('w15-acct', 'w15-sys') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("accounts 种子失败: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.account_list_availability_dirty
		(account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms, created_at_ms, updated_at_ms)
		VALUES ('w15-acct', 'w15-sys', 1, 0, 'w15-test', 1, 1, 1)
		ON CONFLICT (account_id) DO UPDATE SET generation = 1, available_at_ms = 1, claim_token = NULL, claimed_by = NULL, claim_until_ms = NULL`); err != nil {
		t.Fatalf("dirty 种子行插入失败: %v", err)
	}

	// boundDB 包装路径：QueryRowContext 经 sqldialect.BindSQL 转发。
	bound := &boundDB{DB: db}
	var probe int
	if err := bound.QueryRowContext(ctx, `SELECT 1`).Scan(&probe); err != nil || probe != 1 {
		t.Fatalf("boundDB QueryRowContext 应可用: %v", err)
	}

	claims, err := repo.ClaimDirty(ctx, "w15-owner", 5, 60_000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("PG CTE claim 应成功: %v", err)
	}
	found := false
	for _, c := range claims {
		if c.AccountID == acct {
			found = true
			if c.ClaimToken == "" {
				t.Fatal("claim 必须携带 claim token")
			}
			if c.Generation != 1 {
				t.Fatalf("generation=%d want 1", c.Generation)
			}
		}
	}
	if !found {
		t.Fatalf("应至少认领到 w15-acct，实际 claims=%d", len(claims))
	}

	// 重复 claim 在租约内不应再返回该行。
	again, err := repo.ClaimDirty(ctx, "w15-owner-2", 5, 60_000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("二次 claim 应成功: %v", err)
	}
	for _, c := range again {
		if c.AccountID == acct {
			t.Fatal("租约内的行不应被二次认领")
		}
	}
}
