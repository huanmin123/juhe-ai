package circuitstore

// w13g5_circuit_list_arms2_test.go 补充列表投影剩余的语句级 err 传播臂。
// 每个子测试独立注入库（失败注入后 sql.Tx 标记 done，连接级事务仅随连接
// 释放）；PG 专属分支由 PG 门禁测试承载。
//
// 不可达清单（覆盖率登记）：
//   - listavailability.go ListViewerHealthRefreshCandidates 的 rows.Scan
//     错误臂：TEXT 列扫描 string 在 modernc 驱动下不产生错误；
//   - upsertProjectionTx / ReleaseForReplay / EnqueueAllForRuntimeRecovery
//     的 result.RowsAffected() 错误臂：modernc 驱动不返回错误。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func TestW13g5ListDependencyHealthBeginValidationArms(t *testing.T) {
	list, _, _, _ := w13g5CircuitFixture(t)
	longUpdated := strings.Repeat("u", 65)
	if _, err := list.BeginRuntimeDependencyRecovery(context.Background(), longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	if _, err := list.CompleteRuntimeDependencyRecovery(context.Background(), longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	if _, err := list.EnsureViewerHealth(context.Background(), 1, longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
}

func TestW13g5ListDirtyEnqueueArms(t *testing.T) {
	ctx := context.Background()
	t.Run("EnqueueMissing 查询失败", func(t *testing.T) {
		list, _, _, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, list.db.DB, "w13g5-acc", "w13g5-viewer")
		spec.armOnce("LEFT JOIN resource_authorizations authorizations")
		if _, err := list.EnqueueMissing(ctx, 10, 1000); err == nil {
			t.Fatal("查询失败必须传播")
		}
	})
	t.Run("EnqueueMissing markDirty 失败", func(t *testing.T) {
		list, _, _, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, list.db.DB, "w13g5-acc", "w13g5-viewer")
		spec.armOnce("INSERT INTO account_list_availability_dirty")
		if _, err := list.EnqueueMissing(ctx, 10, 1000); err == nil {
			t.Fatal("markDirty 失败必须传播")
		}
	})
	t.Run("EnqueueDue 查询失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		spec.armOnce("FROM account_list_availability_projections projections")
		if _, err := list.EnqueueDue(ctx, 10, 1_800_000_000_000); err == nil {
			t.Fatal("查询失败必须传播")
		}
	})
	t.Run("EnqueueDue 提交失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedProjection(t, db, "w13g5-viewer", "w13g5-acc", 1, "2026-09-18T07:00:00.000Z")
		spec.armOnce("w13g5-COMMIT")
		if _, err := list.EnqueueDue(ctx, 10, 1_800_000_000_000); err == nil {
			t.Fatal("提交失败必须传播")
		}
	})
	t.Run("EnqueueAll viewer health 更新失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		spec.armOnce("SET is_current = 0")
		if _, err := list.EnqueueAllForRuntimeRecovery(ctx, 2000); err == nil {
			t.Fatal("viewer health 更新失败必须传播")
		}
	})
}

// w13g5SeedProjection 写入一条最小投影行。
func w13g5SeedProjection(t *testing.T, db *sql.DB, viewer, accountID string, generation int64, nextTransitionAt string) {
	t.Helper()
	next := any(nil)
	if nextTransitionAt != "" {
		next = nextTransitionAt
	}
	if _, err := db.Exec(`INSERT INTO account_list_availability_projections (
		viewer_system_account_id, account_id, effective_status, schedulable_bucket, provider_code,
		provider_protocol_profile_id, account_type, name_sort_key, priority_sort_key,
		super_priority_sort_key, fallback_sort_key, concurrency_sort_key, created_at_sort_key,
		payload_json, source_generation, next_transition_at, projected_at
	) VALUES (?, ?, 'available', 'available', 'openai', 'profile', 'api', 'w13g5-name', 0, 0, 0, 0,
		'2026-01-01T00:00:00.000Z', '{}', ?, ?, '2026-09-18T08:00:00.000Z')`,
		viewer, accountID, generation, next); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5ListApplyClaimsArms2(t *testing.T) {
	ctx := context.Background()
	t.Run("claim 查询失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("WHERE account_id = ? AND generation = ? AND claim_token = ?")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 1)
		write.Claim.ClaimToken = "w13g5-token"
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
			t.Fatal("claim 查询失败必须传播")
		}
	})
	t.Run("tags 写入失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("INSERT INTO account_list_availability_projection_tags")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 1)
		write.Claim.ClaimToken = "w13g5-token"
		write.Item.TagIDs = []string{"w13g5-tag"}
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
			t.Fatal("tags 写入失败必须传播")
		}
	})
	t.Run("search terms 写入失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("INSERT INTO account_list_availability_projection_search_terms")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 1)
		write.Claim.ClaimToken = "w13g5-token"
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
			t.Fatal("search terms 写入失败必须传播")
		}
	})
	t.Run("viewer health 置脏失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("INSERT INTO account_list_availability_projection_viewer_health")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 1)
		write.Claim.ClaimToken = "w13g5-token"
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
			t.Fatal("viewer health 置脏失败必须传播")
		}
	})
}

func TestW13g5ListApplyDeletionClaimArms2(t *testing.T) {
	ctx := context.Background()
	t.Run("投影删除失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedProjection(t, db, "w13g5-viewer", "w13g5-acc", 1, "")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("DELETE FROM account_list_availability_projections")
		defer spec.disarm()
		if _, err := list.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "w13g5-acc", ViewerSystemAccountID: "w13g5-viewer", Generation: 1, ClaimToken: "w13g5-token"}); err == nil {
			t.Fatal("投影删除失败必须传播")
		}
	})
	t.Run("确认删除失败", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
		spec.armOnce("DELETE FROM account_list_availability_dirty")
		defer spec.disarm()
		if _, err := list.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "w13g5-acc", ViewerSystemAccountID: "w13g5-viewer", Generation: 1, ClaimToken: "w13g5-token"}); err == nil {
			t.Fatal("确认删除失败必须传播")
		}
	})
}
