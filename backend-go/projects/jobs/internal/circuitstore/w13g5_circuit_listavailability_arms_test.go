package circuitstore

// w13g5_circuit_listavailability_arms_test.go 覆盖列表投影仓储的深层 err
// 传播臂、输入校验臂与数据驱动分支：依赖健康状态机、dirty 入队/认领/释放、
// viewer health、投影归一化与 apply/delete 流程。
//
// 注入失败后 sql.Tx 标记 done，驱动级事务仅随连接释放；涉及 Commit 失败的
// 阶段一律使用独立 fixture，避免连接级事务残留（对齐 controlplane arms）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

var w13g5FixedNow = func() time.Time { return time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC) }

func TestW13g5ListDependencyHealthArms(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	longUpdated := strings.Repeat("u", 65)
	if err := list.EnsureRuntimeDependency(ctx, longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	if err := list.EnsureRuntimeDependency(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := list.TouchRuntimeDependency(ctx, "2026-09-18T08:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	// Touch updatedAt 校验臂。
	if err := list.TouchRuntimeDependency(ctx, longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	// Mark 校验臂。
	if err := list.MarkRuntimeDependencyUnavailable(ctx, "", ""); err == nil {
		t.Fatal("空 reason 必须报错")
	}
	if err := list.MarkRuntimeDependencyUnavailable(ctx, strings.Repeat("r", 257), ""); err == nil {
		t.Fatal("超长 reason 必须报错")
	}
	if err := list.MarkRuntimeDependencyUnavailable(ctx, "w13g5-reason", ""); err != nil {
		t.Fatal(err)
	}
	// 二次 Mark：conflict 且已 unavailable → generation 保持分支。
	if err := list.MarkRuntimeDependencyUnavailable(ctx, "w13g5-reason-2", ""); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := db.QueryRow(`SELECT generation FROM account_list_availability_projection_dependency_health WHERE dependency_name = 'runtime_state'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 2 {
		// Ensure 先插入 recovering(generation=1)，首次 unavailable 冲突时状态不同 → +1；
		// 第二次 unavailable 冲突保持。
		t.Fatalf("generation 应为 2: %d", generation)
	}
	// Mark 的 updatedAt 校验臂（语句成功后再入错误参数）。
	if err := list.MarkRuntimeDependencyUnavailable(ctx, "w13g5-reason-3", longUpdated); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	// Begin recovery：unavailable → recovering。
	began, err := list.BeginRuntimeDependencyRecovery(ctx, "")
	if err != nil || !began {
		t.Fatalf("恢复必须开始: %v %v", began, err)
	}
	// Complete：无 dirty → healthy（true）。
	completed, err := list.CompleteRuntimeDependencyRecovery(ctx, "")
	if err != nil || !completed {
		t.Fatalf("恢复必须完成: %v %v", completed, err)
	}
	// Begin：healthy → false。
	began, err = list.BeginRuntimeDependencyRecovery(ctx, "")
	if err != nil || began {
		t.Fatalf("healthy 时不得重新恢复: %v %v", began, err)
	}
	// Complete 的 updatedAt 不参与（无 updatedAt 参数）。
	if err := list.MarkRuntimeDependencyUnavailable(ctx, "w13g5-reason-4", ""); err != nil {
		t.Fatal(err)
	}
	// dirty 存在 → Complete false。
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
	completed, err = list.CompleteRuntimeDependencyRecovery(ctx, "")
	if err != nil || completed {
		t.Fatalf("存在 dirty 时不得完成恢复: %v %v", completed, err)
	}
}

func TestW13g5ListDependencyHealthNoRowAndErrorArms(t *testing.T) {
	ctx := context.Background()
	t.Run("no-row bootstrap", func(t *testing.T) {
		list, _, _, _ := w13g5CircuitFixture(t)
		began, err := list.BeginRuntimeDependencyRecovery(ctx, "")
		if err != nil || !began {
			t.Fatalf("缺行时必须 bootstrap: %v %v", began, err)
		}
	})
	t.Run("BeginTx 失败", func(t *testing.T) {
		list, _, _, spec := w13g5CircuitFixture(t)
		spec.arm("w13g5-BEGIN")
		defer spec.disarm()
		if _, err := list.BeginRuntimeDependencyRecovery(ctx, ""); err == nil {
			t.Fatal("BeginTx 失败必须传播")
		}
	})
	t.Run("Complete RowsAffected 失败", func(t *testing.T) {
		list, _, _, spec := w13g5CircuitFixture(t)
		list.db.Exec(`INSERT INTO account_list_availability_projection_dependency_health (dependency_name, state, generation, reason, updated_at) VALUES ('runtime_state', 'recovering', 1, NULL, '2026-09-18T08:00:00.000Z')`)
		spec.armOnce("SET state = 'healthy'")
		if _, err := list.CompleteRuntimeDependencyRecovery(ctx, ""); err == nil {
			t.Fatal("RowsAffected 失败必须传播")
		}
	})
}

func TestW13g5ListMarkDirtyAndEnqueueArms(t *testing.T) {
	list, _, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	// EnqueueMissing 校验臂。
	if _, err := list.EnqueueMissing(ctx, 0, 0); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	if _, err := list.EnqueueMissing(ctx, maximumDirtyClaimLimit+1, 0); err == nil {
		t.Fatal("超限 limit 必须报错")
	}
	// 成功路径：缺投影的账户入队。
	count, err := list.EnqueueMissing(ctx, 10, 1000)
	if err != nil || count != 1 {
		t.Fatalf("缺投影账户必须入队: %d %v", count, err)
	}
	// 已入队后不再入队。
	count, err = list.EnqueueMissing(ctx, 10, 1000)
	if err != nil || count != 0 {
		t.Fatalf("已入队账户不得重复入队: %d %v", count, err)
	}
	// EnqueueDue：投影到期转移。
	if _, err := db.Exec(`INSERT INTO account_list_availability_projections (
		viewer_system_account_id, account_id, effective_status, schedulable_bucket, provider_code,
		provider_protocol_profile_id, account_type, name_sort_key, priority_sort_key,
		super_priority_sort_key, fallback_sort_key, concurrency_sort_key, created_at_sort_key,
		payload_json, source_generation, next_transition_at, projected_at
	) VALUES ('w13g5-viewer', 'w13g5-acc', 'available', 'available', 'openai', 'profile', 'api',
		'w13g5-name', 0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 1, '2026-09-18T07:00:00.000Z', '2026-09-18T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	// dirty 已存在（EnqueueMissing 写入）→ EnqueueDue 跳过。
	if _, err := db.Exec(`DELETE FROM account_list_availability_dirty`); err != nil {
		t.Fatal(err)
	}
	dueCount, err := list.EnqueueDue(ctx, 10, 1_800_000_000_000)
	if err != nil || dueCount != 1 {
		t.Fatalf("到期投影必须入队: %d %v", dueCount, err)
	}
	if _, err := list.EnqueueDue(ctx, 0, 0); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	// EnqueueAllForRuntimeRecovery。
	recovered, err := list.EnqueueAllForRuntimeRecovery(ctx, 2000)
	if err != nil || recovered != 1 {
		t.Fatalf("恢复入队必须覆盖 1 个账户: %d %v", recovered, err)
	}
	// BeginTx 失败臂（独立校验，不落事务）。
	spec.arm("w13g5-BEGIN")
	if _, err := list.EnqueueAllForRuntimeRecovery(ctx, 2000); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
	spec.disarm()
}

func TestW13g5ListMarkDirtyTxValidationArms(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := list.markDirtyTx(ctx, tx, "", "reason", 0, 0); err == nil {
		t.Fatal("空 accountId 必须报错")
	}
	if err := list.markDirtyTx(ctx, tx, strings.Repeat("a", 257), "reason", 0, 0); err == nil {
		t.Fatal("超长 accountId 必须报错")
	}
	if err := list.markDirtyTx(ctx, tx, "w13g5-acc", "", 0, 0); err == nil {
		t.Fatal("空 reason 必须报错")
	}
	// 账户不存在 → count<1 明确错误。
	if err := list.markDirtyTx(ctx, tx, "w13g5-missing", "reason", 0, 0); err == nil {
		t.Fatal("账户缺失必须报错")
	}
	if err := list.markDirtyTx(ctx, tx, "w13g5-acc", "reason", 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5ListViewerHealthArms(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	if _, err := list.EnsureViewerHealth(ctx, 0, ""); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	changed, err := list.EnsureViewerHealth(ctx, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = changed
	if _, err := list.EnsureViewerHealth(ctx, 10, strings.Repeat("u", 65)); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	// RefreshViewerHealth 校验臂。
	if err := list.RefreshViewerHealth(ctx, "", ""); err == nil {
		t.Fatal("空 viewer 必须报错")
	}
	if err := list.RefreshViewerHealth(ctx, strings.Repeat("v", 257), ""); err == nil {
		t.Fatal("超长 viewer 必须报错")
	}
	if err := list.RefreshViewerHealth(ctx, "w13g5-viewer", ""); err != nil {
		t.Fatal(err)
	}
	// 候选列表：is_current=0 且无 dirty 的 viewer（Refresh 已置 1，用第二个 viewer）。
	if _, err := db.Exec(`INSERT INTO system_accounts (id) VALUES ('w13g5-viewer-2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := list.EnsureViewerHealth(ctx, 10, "2026-09-18T08:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	candidates, err := list.ListViewerHealthRefreshCandidates(ctx, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("候选必须含 1 个 viewer: %v %v", candidates, err)
	}
	if _, err := list.ListViewerHealthRefreshCandidates(ctx, 0); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	// 数据驱动校验臂：超长 viewer id。
	if _, err := db.Exec(`INSERT INTO account_list_availability_projection_viewer_health (
		viewer_system_account_id, projection_count, is_current, updated_at
	) VALUES (?, 0, 0, '2026-09-18T08:00:00.000Z')`, strings.Repeat("v", 300)); err != nil {
		t.Fatal(err)
	}
	if _, err := list.ListViewerHealthRefreshCandidates(ctx, 10); err == nil {
		t.Fatal("超长 viewer 必须报错")
	}
}

func TestW13g5ListClaimDirtyArms(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	// 校验臂。
	if _, err := list.ClaimDirty(ctx, "", 1, 1, 0); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := list.ClaimDirty(ctx, "w13g5-owner", 0, 1, 0); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	if _, err := list.ClaimDirty(ctx, "w13g5-owner", 1, 0, 0); err == nil {
		t.Fatal("lease<1 必须报错")
	}
	if _, err := list.ClaimDirty(ctx, "w13g5-owner", 1, 1, -1); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
	// SQLite 成功路径。
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 3, "")
	claims, err := list.ClaimDirty(ctx, "w13g5-owner", 10, 30_000, 1000)
	if err != nil || len(claims) != 1 || claims[0].AttemptCount != 1 {
		t.Fatalf("claim 必须成功且累加 attempt: %+v %v", claims, err)
	}
	if claims[0].ClaimToken == "" {
		t.Fatal("claim 必须携带 token")
	}
	// 提交失败臂（独立 fixture）。
	list2, _, db2, spec2 := w13g5CircuitFixture(t)
	w13g5SeedDirty(t, db2, "w13g5-acc", "w13g5-viewer", 3, "")
	spec2.armOnce("w13g5-COMMIT")
	if _, err := list2.ClaimDirty(ctx, "w13g5-owner", 10, 30_000, 1000); err == nil {
		t.Fatal("提交失败必须传播")
	}
	// BeginTx 失败臂。
	spec2.arm("w13g5-BEGIN")
	if _, err := list2.ClaimDirty(ctx, "w13g5-owner", 10, 30_000, 1000); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
}

func TestW13g5ListScopesAndSearchTermsArms(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	if _, err := db.Exec(`INSERT INTO resource_authorizations (id, status) VALUES ('w13g5-authz', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET authorization_instance_authorization_id = 'w13g5-authz' WHERE id = 'w13g5-acc'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_name_search_documents (account_id) VALUES ('w13g5-acc')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_name_search_terms (account_id, term) VALUES ('w13g5-acc', 'w13g5-term')`); err != nil {
		t.Fatal(err)
	}
	scopes, err := list.ListScopes(ctx, []string{"w13g5-acc"})
	if err != nil || len(scopes) != 1 {
		t.Fatalf("scope 读取失败: %v %v", scopes, err)
	}
	if _, err := list.ListScopes(ctx, []string{""}); err == nil {
		t.Fatal("空 id 必须报错")
	}
	if _, err := list.ListScopes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	terms, err := list.LoadSearchTerms(ctx, []string{"w13g5-acc"})
	if err != nil || len(terms["w13g5-acc"]) != 1 {
		t.Fatalf("搜索词读取失败: %v %v", terms, err)
	}
	if _, err := list.LoadSearchTerms(ctx, []string{strings.Repeat("a", 257)}); err == nil {
		t.Fatal("超长 id 必须报错")
	}
}

func w13g5ProjectionWrite(accountID string, generation int64) opsjobs.ProjectionWrite {
	available := true
	return opsjobs.ProjectionWrite{
		Claim: opsjobs.DirtyClaim{AccountID: accountID, ViewerSystemAccountID: "w13g5-viewer", Generation: generation, ClaimToken: "w13g5-claim-token"},
		Item: opsjobs.ProjectionItem{
			AccountID: accountID, SourceAccountID: accountID, ProviderCode: "openai",
			ProviderProtocolProfileID: "profile", AccountType: "api", Name: "w13g5-name",
			EffectiveStatus: "available", Payload: map[string]any{"accessType": "owner"},
			EffectiveAvailable: &available,
		},
		SearchTerms: []string{"w13g5-term"},
		Scope:       opsjobs.ProjectionScope{ViewerSystemAccountID: "w13g5-viewer", CreatedAt: strPtrOrEmpty("2026-01-01T00:00:00.000Z")},
		Now:         w13g5FixedNow(),
	}
}

func strPtrOrEmpty(v string) *string { return &v }

func TestW13g5ListApplyClaimsArms(t *testing.T) {
	list, _, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	// claim 围栏校验臂。
	bad := []opsjobs.ProjectionWrite{w13g5ProjectionWrite("w13g5-acc", 0)}
	if _, err := list.ApplyClaims(ctx, bad); err == nil {
		t.Fatal("无效 claim 围栏必须报错")
	}
	// 空 writes。
	if result, err := list.ApplyClaims(ctx, nil); err != nil || len(result) != 0 {
		t.Fatalf("空 writes 必须返回空结果: %v %v", result, err)
	}
	// claim 不存在 → false。
	missing := []opsjobs.ProjectionWrite{w13g5ProjectionWrite("w13g5-acc", 1)}
	result, err := list.ApplyClaims(ctx, missing)
	if err != nil || result["w13g5-claim-token"] {
		t.Fatalf("claim 缺失必须返回 false: %v %v", result, err)
	}
	// 归一化错误传播臂（providerCode 为空）。
	invalid := w13g5ProjectionWrite("w13g5-acc", 1)
	invalid.Item.ProviderCode = ""
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-claim-token")
	if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{invalid}); err == nil {
		t.Fatal("归一化失败必须传播")
	}
	// 成功路径。
	w13g5SeedDirty(t, db, "w13g5-acc2", "w13g5-viewer", 2, "w13g5-claim-token-2")
	w13g5SeedAccount(t, db, "w13g5-acc2", "w13g5-viewer")
	valid := w13g5ProjectionWrite("w13g5-acc2", 2)
	valid.Claim.ClaimToken = "w13g5-claim-token-2"
	result, err = list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{valid})
	if err != nil || !result["w13g5-claim-token-2"] {
		t.Fatalf("apply 必须成功: %v %v", result, err)
	}
	// BeginTx 失败臂。
	spec.arm("w13g5-BEGIN")
	if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{valid}); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
}

func TestW13g5ListApplyClaimsStaleGenerationArm(t *testing.T) {
	list, _, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	// 既有投影 source_generation 更高 → upsert WHERE 不命中 → written=false。
	if _, err := db.Exec(`INSERT INTO account_list_availability_projections (
		viewer_system_account_id, account_id, effective_status, schedulable_bucket, provider_code,
		provider_protocol_profile_id, account_type, name_sort_key, priority_sort_key,
		super_priority_sort_key, fallback_sort_key, concurrency_sort_key, created_at_sort_key,
		payload_json, source_generation, projected_at
	) VALUES ('w13g5-viewer', 'w13g5-acc', 'available', 'available', 'openai', 'profile', 'api',
		'w13g5-name', 0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 9, '2026-09-18T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 3, "w13g5-claim-token")
	write := w13g5ProjectionWrite("w13g5-acc", 3)
	if _, err := list.ApplyClaims(ctx, []opsjobs.ProjectionWrite{write}); err == nil {
		t.Fatal("低代被覆盖必须报错")
	}
}

func TestW13g5ListApplyDeletionClaimArms(t *testing.T) {
	list, _, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	// claim 缺失 → false。
	found, err := list.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "w13g5-acc", Generation: 1, ClaimToken: "w13g5-token"})
	if err != nil || found {
		t.Fatalf("claim 缺失必须返回 false: %v %v", found, err)
	}
	// BeginTx 失败臂。
	spec.arm("w13g5-BEGIN")
	if _, err := list.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "w13g5-acc", Generation: 1, ClaimToken: "w13g5-token"}); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
	spec.disarm()
	// 成功路径：有投影 → deleted=true 且 viewer health 置脏。
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	if _, err := db.Exec(`INSERT INTO account_list_availability_projections (
		viewer_system_account_id, account_id, effective_status, schedulable_bucket, provider_code,
		provider_protocol_profile_id, account_type, name_sort_key, priority_sort_key,
		super_priority_sort_key, fallback_sort_key, concurrency_sort_key, created_at_sort_key,
		payload_json, source_generation, projected_at
	) VALUES ('w13g5-viewer', 'w13g5-acc', 'available', 'available', 'openai', 'profile', 'api',
		'w13g5-name', 0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 1, '2026-09-18T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 2, "w13g5-token")
	found, err = list.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "w13g5-acc", ViewerSystemAccountID: "w13g5-viewer", Generation: 2, ClaimToken: "w13g5-token"})
	if err != nil || !found {
		t.Fatalf("删除 claim 必须成功: %v %v", found, err)
	}
}

func TestW13g5ListReleaseForReplayArms(t *testing.T) {
	list, _, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	input := opsjobs.ListAvailabilityReplayInput{AccountID: "w13g5-acc", Generation: 1, ClaimToken: "w13g5-token", Reason: "w13g5-retry", NowMS: 1000}
	// 校验臂。
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{ClaimToken: "t", Reason: "r", Generation: 1}); err == nil {
		t.Fatal("空 accountId 必须报错")
	}
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{AccountID: "a", ClaimToken: "t", Reason: "r", Generation: 0}); err == nil {
		t.Fatal("generation<1 必须报错")
	}
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{AccountID: "a", ClaimToken: "", Reason: "r", Generation: 1}); err == nil {
		t.Fatal("空 claimToken 必须报错")
	}
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{AccountID: "a", ClaimToken: "t", Reason: "", Generation: 1}); err == nil {
		t.Fatal("空 reason 必须报错")
	}
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{AccountID: "a", ClaimToken: "t", Reason: "r", Generation: 1, RetryDelayMS: -1}); err == nil {
		t.Fatal("负 retryDelay 必须报错")
	}
	if _, err := list.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{AccountID: "a", ClaimToken: "t", Reason: "r", Generation: 1, NowMS: -1}); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
	// 未命中 → false。
	found, err := list.ReleaseForReplay(ctx, input)
	if err != nil || found {
		t.Fatalf("未命中必须返回 false: %v %v", found, err)
	}
	// UPDATE 失败臂。
	w13g5SeedDirty(t, db, "w13g5-acc", "w13g5-viewer", 1, "w13g5-token")
	spec.armOnce("SET reason = ?")
	if _, err := list.ReleaseForReplay(ctx, input); err == nil {
		t.Fatal("UPDATE 失败必须传播")
	}
	spec.disarm()
	// 成功 → true。
	found, err = list.ReleaseForReplay(ctx, input)
	if err != nil || !found {
		t.Fatalf("释放必须成功: %v %v", found, err)
	}
}

func TestW13g5ListProjectionValidationArms(t *testing.T) {
	base := w13g5ProjectionWrite("w13g5-acc", 1)
	cases := []struct {
		name  string
		mutat func(w *opsjobs.ProjectionWrite)
	}{
		{"空 viewer", func(w *opsjobs.ProjectionWrite) { w.Scope.ViewerSystemAccountID = "" }},
		{"超长 viewer", func(w *opsjobs.ProjectionWrite) { w.Scope.ViewerSystemAccountID = strings.Repeat("v", 257) }},
		{"空 accountID", func(w *opsjobs.ProjectionWrite) { w.Item.AccountID = "" }},
		{"超长 concurrency", func(w *opsjobs.ProjectionWrite) { w.Item.SourceAccountID = strings.Repeat("s", 257) }},
		{"空 providerCode", func(w *opsjobs.ProjectionWrite) { w.Item.ProviderCode = "" }},
		{"空 profileID", func(w *opsjobs.ProjectionWrite) { w.Item.ProviderProtocolProfileID = "" }},
		{"空 accountType", func(w *opsjobs.ProjectionWrite) { w.Item.AccountType = "" }},
		{"空 name", func(w *opsjobs.ProjectionWrite) { w.Item.Name = "   " }},
		{"空 createdAt", func(w *opsjobs.ProjectionWrite) { w.Scope.CreatedAt = strPtrOrEmpty("") }},
		{"负并发", func(w *opsjobs.ProjectionWrite) { w.Item.CurrentConcurrency = -1 }},
		{"零代", func(w *opsjobs.ProjectionWrite) { w.Claim.Generation = 0 }},
		{"空状态", func(w *opsjobs.ProjectionWrite) { w.Item.EffectiveStatus = "" }},
		{"空 tag", func(w *opsjobs.ProjectionWrite) { w.Item.TagIDs = []string{""} }},
		{"空 term", func(w *opsjobs.ProjectionWrite) { w.SearchTerms = []string{""} }},
		{"超长 tag", func(w *opsjobs.ProjectionWrite) { w.Item.TagIDs = []string{strings.Repeat("t", 257)} }},
		{"超长 term", func(w *opsjobs.ProjectionWrite) { w.SearchTerms = []string{strings.Repeat("s", 257)} }},
		{"不可序列化 payload", func(w *opsjobs.ProjectionWrite) { w.Item.Payload = map[string]any{"bad": make(chan int)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write := base
			tc.mutat(&write)
			if _, err := normalizeProjectionWrite(write); err == nil {
				t.Fatalf("%s 必须报错", tc.name)
			}
		})
	}
	// 成功归一化：payload 覆盖可用性键、tag/term 去重生效。
	payloadBase := w13g5ProjectionWrite("w13g5-acc", 1)
	payloadBase.Item.Payload = map[string]any{"effectiveAvailable": false}
	payloadBase.Item.EffectiveAvailable = nil
	normalized, err := normalizeProjectionWrite(payloadBase)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.schedulableBucket != "disabled" {
		t.Fatalf("payload effectiveAvailable=false 必须落入 disabled 桶: %+v", normalized)
	}
	normalized, err = normalizeProjectionWrite(base)
	if err != nil {
		t.Fatal(err)
	}
	// nextTransitionAt 候选过滤。
	base.Item.NextTransitionCandidates = []string{"2026-09-19T00:00:00.000Z"}
	normalized, err = normalizeProjectionWrite(base)
	if err != nil || normalized.nextTransitionAt == nil {
		t.Fatalf("未来转移候选必须保留: %+v %v", normalized.nextTransitionAt, err)
	}
}

func TestW13g5ListSmallHelpers(t *testing.T) {
	updated, err := optionalUpdatedAt("")
	if err != nil || updated != "" {
		t.Fatalf("空 updatedAt 必须通过: %v %v", updated, err)
	}
	if _, err := optionalUpdatedAt(strings.Repeat("u", 65)); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	if _, err := normalizedIDList([]string{"ok", "", strings.Repeat("x", 257)}); err == nil {
		t.Fatal("非法 id 必须报错")
	}
	if got := boolParam(true); got != 1 {
		t.Fatalf("布尔参数 true: %v", got)
	}
	if got := boolParam(false); got != 0 {
		t.Fatalf("布尔参数 false: %v", got)
	}
	if got := instantParam(true, "w13g5-not-time", w13g5FixedNow); got != "w13g5-not-time" {
		t.Fatalf("不可解析时间必须原样返回: %v", got)
	}
	if got := instantParam(false, "w13g5-time", w13g5FixedNow); got != "w13g5-time" {
		t.Fatalf("SQLite 时间原样返回: %v", got)
	}
	if got := placeholdersFor(3); got != "?, ?, ?" {
		t.Fatalf("占位符生成: %s", got)
	}
}

