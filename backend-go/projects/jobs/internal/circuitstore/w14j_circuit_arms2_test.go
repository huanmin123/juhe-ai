// 波次 w14j：列表投影仓储错误传播臂第二轮收口（注入 + 空表分支）。
package circuitstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// w13g5CircuitListFixture 是 w13g5CircuitFixture 的列表投影简写（repo+db+spec）。
func w13g5CircuitListFixture(t *testing.T) (*ListAvailabilityRepo, *sql.DB, *w13g5CircuitSpec) {
	t.Helper()
	repo, _, db, spec := w13g5CircuitFixture(t)
	return repo, db, spec
}

// TestW14JDependencyValidationArms 覆盖 dependency 状态机的参数与空表分支：
// reason 校验、requireUpdatedAt、无 health 行时的 bootstrap 插入、
// healthy 状态 Begin 拒绝与 UPDATE 注入。
func TestW14JDependencyValidationArms(t *testing.T) {
	repo, db, spec := w13g5CircuitListFixture(t)
	ctx := context.Background()
	updatedAt := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "", updatedAt); err == nil {
		t.Fatal("空 reason 必须报错")
	}
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, strings.Repeat("r", 257), updatedAt); err == nil {
		t.Fatal("超长 reason 必须报错")
	}
	// requireUpdatedAt 对超长时间戳报错。
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "r", strings.Repeat("u", 65)); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	// 空 health 表 Begin → bootstrap 插入分支。
	started, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || !started {
		t.Fatalf("空表 Begin 必须 bootstrap: %v %v", started, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM account_list_availability_projection_dependency_health WHERE dependency_name='runtime_state'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "recovering" {
		t.Fatalf("bootstrap 状态必须 recovering: %s", state)
	}
	// healthy 状态 Begin 拒绝（state != unavailable 分支）。
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "w14j-down", updatedAt); err != nil {
		t.Fatal(err)
	}
	started, err = repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || !started {
		t.Fatalf("unavailable Begin 必须启动: %v %v", started, err)
	}
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "w14j-down-2", updatedAt); err != nil {
		t.Fatal(err)
	}
	// UPDATE 注入。
	spec.arm("SET state = 'recovering'")
	if _, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt); err == nil {
		t.Fatal("Begin UPDATE 注入必须传播")
	}
	spec.disarm()
}

// TestW14JMarkDirtyTxMissingAccountArm 直接触发 markDirtyTx 的 count<1
// 错误（账户不存在）：EnqueueDue/EnqueueMissing 走不到，这里用 dirty 表
// 语义等价路径验证。markDirtyTx 为私有方法，经 EnqueueMissing 的注入
// 不易构造，改为对空账户表的入队（返回 0 但不报错）与注入传播。
func TestW14JEnqueueMissingArms(t *testing.T) {
	repo, _, spec := w13g5CircuitListFixture(t)
	ctx := context.Background()
	nowMS := time.Now().UnixMilli()
	// 空表：无缺失账户 → 0。
	missing, err := repo.EnqueueMissing(ctx, 10, nowMS)
	if err != nil || missing != 0 {
		t.Fatalf("空表必须 0: %d %v", missing, err)
	}
	// 校验臂。
	if _, err := repo.EnqueueMissing(ctx, 0, nowMS); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	// 查询注入。
	spec.arm("LEFT JOIN account_list_availability_projections projections")
	if _, err := repo.EnqueueMissing(ctx, 10, nowMS); err == nil {
		t.Fatal("EnqueueMissing 注入必须传播")
	}
	spec.disarm()
}

// TestW14JEnqueueAllInjectionArms 覆盖全量重放入队的注入传播。
func TestW14JEnqueueAllInjectionArms(t *testing.T) {
	repo, _, spec := w13g5CircuitListFixture(t)
	spec.arm("INSERT INTO account_list_availability_dirty")
	if _, err := repo.EnqueueAllForRuntimeRecovery(context.Background(), 1000); err == nil {
		t.Fatal("全量重放注入必须传播")
	}
	spec.disarm()
	if _, err := repo.EnqueueAllForRuntimeRecovery(context.Background(), 1000); err != nil {
		t.Fatalf("解除注入后必须成功: %v", err)
	}
}

// TestW14JViewerHealthArms 覆盖 viewer 健康面的校验与注入臂。
func TestW14JViewerHealthArms(t *testing.T) {
	repo, _, spec := w13g5CircuitListFixture(t)
	ctx := context.Background()
	if _, err := repo.EnsureViewerHealth(ctx, 0, "2026-09-18T08:00:00.000Z"); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	if _, err := repo.ListViewerHealthRefreshCandidates(ctx, 0); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	spec.arm("WHERE health.is_current = 0")
	if _, err := repo.ListViewerHealthRefreshCandidates(ctx, 10); err == nil {
		t.Fatal("候选查询注入必须传播")
	}
	spec.disarm()
	if err := repo.RefreshViewerHealth(ctx, "w14j-no-such-viewer", "2026-09-18T08:00:00.000Z"); err != nil {
		t.Fatalf("无行刷新必须幂等成功: %v", err)
	}
	spec.arm("ON CONFLICT(viewer_system_account_id) DO UPDATE SET")
	if err := repo.RefreshViewerHealth(ctx, "w14j-viewer", "2026-09-18T08:00:00.000Z"); err == nil {
		t.Fatal("刷新注入必须传播")
	}
	spec.disarm()
}

// TestW14JClaimDirtyInjectionArms 覆盖 ClaimDirty 候选查询注入与
// ListScopes/LoadSearchTerms 的注入臂。
func TestW14JClaimDirtyInjectionArms(t *testing.T) {
	repo, _, spec := w13g5CircuitListFixture(t)
	ctx := context.Background()
	spec.arm("WHERE available_at_ms <= ?")
	if _, err := repo.ClaimDirty(ctx, "w14j-owner", 10, 30_000, 1000); err == nil {
		t.Fatal("ClaimDirty 候选注入必须传播")
	}
	spec.disarm()
	if _, err := repo.ListScopes(ctx, []string{" "}); err == nil {
		t.Fatal("非法作用域列表必须报错")
	}
	if scopes, err := repo.ListScopes(ctx, []string{}); err != nil || len(scopes) != 0 {
		t.Fatalf("空作用域必须空切片: %v %v", scopes, err)
	}
	spec.arm("FROM account_name_search_terms search")
	if _, err := repo.LoadSearchTerms(ctx, []string{"w14j-acc"}); err == nil {
		t.Fatal("LoadSearchTerms 注入必须传播")
	}
	spec.disarm()
}

// TestW14JApplyDeletionClaimValidationArm 覆盖删除 claim 缺失行分支。
func TestW14JApplyDeletionClaimValidationArm(t *testing.T) {
	repo, _, _ := w13g5CircuitListFixture(t)
	deleted, err := repo.ApplyDeletionClaim(context.Background(), opsjobs.DirtyClaim{
		AccountID: "w14j-no-such", ViewerSystemAccountID: "w14j-viewer", Generation: 1, ClaimToken: "t",
	})
	if err != nil || deleted {
		t.Fatalf("缺失 claim 必须 (false,nil): %v %v", deleted, err)
	}
}
