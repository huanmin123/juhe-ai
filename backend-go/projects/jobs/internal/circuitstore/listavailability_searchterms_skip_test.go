package circuitstore

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestUpsertProjectionSearchTermsUnchangedSkip 词集指纹跳过契约（问题-0313
// 后续批次 1-4）：
//  1. 同词集二次应用（generation 推进、名称不变）：search_terms 删写被整体
//     跳过——注入 INSERT 失败仍应用成功；
//  2. 词集变化：删写照常执行——同样注入下错误按原语义传播；
//  3. 库内残行 + 新词集为空（searchIndexComplete=false）：残行被清除。
func TestUpsertProjectionSearchTermsUnchangedSkip(t *testing.T) {
	ctx := context.Background()

	t.Run("同词集重应用跳过删写", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-claim-token")
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{w13g5ProjectionWrite("w13g5-acc", 1)}); err != nil {
			t.Fatalf("首次应用失败: %v", err)
		}
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 2, "w13g5-claim-token-2")
		spec.armOnce("INSERT INTO account_list_availability_projection_search_terms")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 2)
		write.Claim.ClaimToken = "w13g5-claim-token-2"
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err != nil {
			t.Fatalf("同词集重应用应跳过 search_terms 删写: %v", err)
		}
	})

	t.Run("词集变化删写照常", func(t *testing.T) {
		list, _, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-claim-token")
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{w13g5ProjectionWrite("w13g5-acc", 1)}); err != nil {
			t.Fatalf("首次应用失败: %v", err)
		}
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 2, "w13g5-claim-token-2")
		spec.armOnce("INSERT INTO account_list_availability_projection_search_terms")
		defer spec.disarm()
		write := w13g5ProjectionWrite("w13g5-acc", 2)
		write.Claim.ClaimToken = "w13g5-claim-token-2"
		write.SearchTerms = []string{"w13g5-renamed"}
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
			t.Fatal("词集变化必须执行删写（注入失败应传播）")
		}
	})

	t.Run("空词集清残行", func(t *testing.T) {
		list, _, db, _ := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-claim-token")
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{w13g5ProjectionWrite("w13g5-acc", 1)}); err != nil {
			t.Fatalf("首次应用失败: %v", err)
		}
		w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 2, "w13g5-claim-token-2")
		write := w13g5ProjectionWrite("w13g5-acc", 2)
		write.Claim.ClaimToken = "w13g5-claim-token-2"
		write.SearchTerms = nil
		if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err != nil {
			t.Fatalf("空词集应用失败: %v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_projection_search_terms WHERE account_id = 'w13g5-acc'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("空词集应清除残行: %d", count)
		}
	})
}
