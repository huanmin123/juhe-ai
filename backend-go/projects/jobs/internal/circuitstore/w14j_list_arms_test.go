// 波次 w14j：列表投影仓储状态机与校验臂收口（SQLite fixture）。
// 依赖状态机全链路：标记不可用 → 重放入队 → claim → 投影/删除 → viewer 健康。
package circuitstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestW14JDependencyRecoveryArms 覆盖 runtime dependency 状态机的
// MarkRuntimeDependencyUnavailable、Begin/CompleteRuntimeDependencyRecovery
// 的错误臂与状态门分支。
func TestW14JDependencyRecoveryArms(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	updatedAt := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('w14j-acc', 'w14j-viewer'); INSERT INTO system_accounts (id) VALUES ('w14j-viewer')`); err != nil {
		t.Fatal(err)
	}
	// MarkRuntimeDependencyUnavailable：无行时 upsert 插入 unavailable。
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "w14j-simulate-failure", updatedAt); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM account_list_availability_projection_dependency_health`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unavailable" {
		t.Fatalf("期待 unavailable: %s", state)
	}
	// unavailable 状态下 Complete 不得成功（门分支）。
	completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || completed {
		t.Fatalf("unavailable 状态 Complete 必须拒绝: %v %v", completed, err)
	}
	// Begin 从 unavailable 启动恢复。
	started, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || !started {
		t.Fatalf("unavailable 状态 Begin 必须启动: %v %v", started, err)
	}
	// recovering 状态 Begin 不得重复启动。
	started, err = repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || started {
		t.Fatalf("recovering 状态 Begin 必须拒绝: %v %v", started, err)
	}
	// recovering 且无 dirty 行 → Complete 成功转 healthy。
	completed, err = repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || !completed {
		t.Fatalf("recovering 状态 Complete 必须成功: %v %v", completed, err)
	}
	// healthy 状态 Complete 不得重复。
	completed, err = repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || completed {
		t.Fatalf("healthy 状态 Complete 必须拒绝: %v %v", completed, err)
	}
	// 全量重放入队（healthy 状态兜底重放）。
	if _, err := repo.EnqueueAllForRuntimeRecovery(ctx, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// TestW14JClaimDirtyValidationArms 覆盖 ClaimDirty 参数校验分支。
func TestW14JClaimDirtyValidationArms(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := repo.ClaimDirty(ctx, "", 1, 1000, 1); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, strings.Repeat("o", 129), 1, 1000, 1); err == nil {
		t.Fatal("超长 owner 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "w14j", 0, 1000, 1); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "w14j", 100000, 1000, 1); err == nil {
		t.Fatal("超限 limit 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "w14j", 1, 0, 1); err == nil {
		t.Fatal("零 lease 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "w14j", 1, 1<<40, 1); err == nil {
		t.Fatal("超限 lease 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "w14j", 1, 1000, -1); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
}

// TestW14JEnqueueDueAndRefreshCandidates 覆盖到期转移入队、viewer 健康刷新
// 候选与刷新路径。
func TestW14JEnqueueDueAndRefreshCandidates(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO accounts (id, system_account_id) VALUES ('w14j-acc', 'w14j-viewer');
		INSERT INTO system_accounts (id) VALUES ('w14j-viewer');
		INSERT INTO account_list_availability_projections (
			viewer_system_account_id, account_id, effective_status, schedulable_bucket,
			provider_code, provider_protocol_profile_id, account_type, name_sort_key,
			priority_sort_key, super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
			created_at_sort_key, payload_json, source_generation, next_transition_at, projected_at
		) VALUES ('w14j-viewer', 'w14j-acc', 'rate_limited', 'cooling', 'openai', 'p1', 'api_key',
			'acc', 0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 1,
			'2026-09-18T07:00:00.000Z', '2026-09-18T07:30:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if bootstrapped, err := repo.EnsureViewerHealth(ctx, 10, now.Format(time.RFC3339Nano)); err != nil || bootstrapped != 1 {
		t.Fatalf("首次必须补齐 viewer 健康行: %d %v", bootstrapped, err)
	}
	candidates, err := repo.ListViewerHealthRefreshCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("投影新于健康行必须产生刷新候选")
	}
	if err := repo.RefreshViewerHealth(ctx, "w14j-viewer", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// 到期转移入队：next_transition_at 已过期。
	// ClaimDirty 需要的 dirty 行由 EnqueueDue 从投影转移产生。
	queued, err := repo.EnqueueDue(ctx, 10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("到期投影必须入队 1 条: %d", queued)
	}
	claims, err := repo.ClaimDirty(ctx, "w14j-owner", 10, 30_000, nowMS)
	if err != nil || len(claims) != 1 {
		t.Fatalf("必须 claim 1 条: %v %v", claims, err)
	}
	// ApplyDeletionClaim：删除投影行。
	deleted, err := repo.ApplyDeletionClaim(ctx, claims[0])
	if err != nil || !deleted {
		t.Fatalf("删除 claim 必须生效: %v %v", deleted, err)
	}
	// 同 claim 重复删除必须 miss。
	deleted, err = repo.ApplyDeletionClaim(ctx, claims[0])
	if err != nil || deleted {
		t.Fatalf("重复删除必须 (false,nil): %v %v", deleted, err)
	}
	// 入队校验臂。
	if _, err := repo.EnqueueDue(ctx, 0, nowMS); err == nil {
		t.Fatal("零 limit 必须报错")
	}
}

// TestW14JNormalizeProjectionWriteArms 覆盖投影写归一化的校验分支。
func TestW14JNormalizeProjectionWriteArms(t *testing.T) {
	created := "2026-01-01T00:00:00.000Z"
	base := opsjobs.ProjectionWrite{
		Claim: opsjobs.DirtyClaim{AccountID: "w14j-acc", ViewerSystemAccountID: "w14j-viewer", Generation: 1, ClaimToken: "w14j-token"},
		Scope: opsjobs.ProjectionScope{AccountID: "w14j-acc", ViewerSystemAccountID: "w14j-viewer", CreatedAt: &created},
		Item: opsjobs.ProjectionItem{
			AccountID:                 "w14j-acc",
			EffectiveStatus:           "active",
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "p1",
			AccountType:               "api_key",
			Name:                      "w14j-账户",
		},
	}
	if _, err := normalizeProjectionWrite(base); err != nil {
		t.Fatalf("合法写不得报错: %v", err)
	}
	badViewer := base
	badViewer.Scope.ViewerSystemAccountID = ""
	if _, err := normalizeProjectionWrite(badViewer); err == nil {
		t.Fatal("空 viewer 必须报错")
	}
	badAccount := base
	badAccount.Item.AccountID = ""
	if _, err := normalizeProjectionWrite(badAccount); err == nil {
		t.Fatal("空 accountId 必须报错")
	}
	longConcurrency := base
	longConcurrency.Item.SourceAccountID = strings.Repeat("s", 257)
	if _, err := normalizeProjectionWrite(longConcurrency); err == nil {
		t.Fatal("超长 concurrencyAccountId 必须报错")
	}
	badProvider := base
	badProvider.Item.ProviderCode = ""
	if _, err := normalizeProjectionWrite(badProvider); err == nil {
		t.Fatal("空 providerCode 必须报错")
	}
}
