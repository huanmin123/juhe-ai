package circuitstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// openListAvailabilityFixture 构建临时 SQLite 业务库并安装投影读模型测试
// schema（与 Node business-schema 同形最小列集）。
func openListAvailabilityFixture(t *testing.T) (*ListAvailabilityRepo, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT '2026-01-01T00:00:00.000Z',
			deleted_at TEXT,
			authorization_instance_authorization_id TEXT
		)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, status TEXT NOT NULL)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY)`,
		`CREATE TABLE account_name_search_terms (account_id TEXT NOT NULL, term TEXT NOT NULL)`,
		`CREATE TABLE account_name_search_documents (account_id TEXT PRIMARY KEY)`,
		`CREATE TABLE account_list_availability_projection_dependency_health (
			dependency_name TEXT PRIMARY KEY,
			state TEXT NOT NULL,
			generation INTEGER NOT NULL,
			reason TEXT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE account_list_availability_dirty (
			account_id TEXT PRIMARY KEY,
			viewer_system_account_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			applied_generation INTEGER NOT NULL,
			reason TEXT NOT NULL,
			available_at_ms INTEGER NOT NULL,
			claim_token TEXT,
			claimed_by TEXT,
			claim_until_ms INTEGER,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE account_list_availability_projections (
			viewer_system_account_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			source_account_id TEXT,
			authorization_id TEXT,
			effective_status TEXT NOT NULL,
			schedulable_bucket TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			provider_protocol_profile_id TEXT NOT NULL,
			account_type TEXT NOT NULL,
			bound_group_id TEXT,
			name_sort_key TEXT NOT NULL,
			priority_sort_key INTEGER NOT NULL,
			super_priority_sort_key INTEGER NOT NULL,
			fallback_sort_key INTEGER NOT NULL,
			concurrency_sort_key INTEGER NOT NULL,
			account_expires_at_sort_key TEXT,
			last_used_at_sort_key TEXT,
			created_at_sort_key TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			source_generation INTEGER NOT NULL,
			next_transition_at TEXT,
			projected_at TEXT NOT NULL,
			PRIMARY KEY (viewer_system_account_id, account_id)
		)`,
		`CREATE TABLE account_list_availability_projection_index (
			viewer_system_account_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			effective_status TEXT NOT NULL,
			schedulable_bucket TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			provider_protocol_profile_id TEXT NOT NULL,
			account_type TEXT NOT NULL,
			bound_group_id TEXT,
			name_sort_key TEXT NOT NULL,
			priority_sort_key INTEGER NOT NULL,
			super_priority_sort_key INTEGER NOT NULL,
			fallback_sort_key INTEGER NOT NULL,
			concurrency_sort_key INTEGER NOT NULL,
			account_expires_at_sort_key TEXT,
			last_used_at_sort_key TEXT,
			created_at_sort_key TEXT NOT NULL,
			access_type_sort_key TEXT NOT NULL,
			search_index_complete INTEGER NOT NULL DEFAULT 0,
			authorization_quota_exceeded INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (viewer_system_account_id, account_id)
		)`,
		`CREATE TABLE account_list_availability_projection_tags (
			viewer_system_account_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			PRIMARY KEY (viewer_system_account_id, account_id, tag_id)
		)`,
		`CREATE TABLE account_list_availability_projection_search_terms (
			viewer_system_account_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			term TEXT NOT NULL,
			name_sort_key TEXT NOT NULL,
			created_at_sort_key TEXT NOT NULL,
			PRIMARY KEY (viewer_system_account_id, account_id, term)
		)`,
		`CREATE TABLE account_list_availability_runtime_overlays (
			account_id TEXT PRIMARY KEY,
			current_concurrency INTEGER NOT NULL,
			observed_at TEXT NOT NULL,
			next_reconcile_at TEXT
		)`,
		`CREATE TABLE account_list_availability_projection_viewer_health (
			viewer_system_account_id TEXT PRIMARY KEY,
			projection_count INTEGER NOT NULL,
			oldest_projected_at TEXT,
			next_transition_at TEXT,
			is_current INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := NewListAvailabilityRepo(ListAvailabilityConfig{DB: db, Postgres: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return repo, db
}

// TestListAvailabilityDependencyStateMachine 覆盖 runtime dependency fail-closed
// 状态机与全量重放入队/完成。
func TestListAvailabilityDependencyStateMachine(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1'), ('acc-2', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}

	if err := repo.EnsureRuntimeDependency(ctx, updatedAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRuntimeDependencyUnavailable(ctx, "account_runtime_availability_unavailable", updatedAt); err != nil {
		t.Fatal(err)
	}
	// Touch 仅对 healthy 生效（不应报错且不改变状态）。
	if err := repo.TouchRuntimeDependency(ctx, updatedAt); err != nil {
		t.Fatal(err)
	}
	started, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("unavailable → recovering 必须启动一次重放")
	}
	again, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil || again {
		t.Fatalf("recovering 状态不得重复启动重放: %v %v", again, err)
	}
	enqueued, err := repo.EnqueueAllForRuntimeRecovery(ctx, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("全量重放应入队 2 个账户: %d", enqueued)
	}
	completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed {
		t.Fatal("仍有 dirty 行时 recovery 不得完成")
	}
}

// TestListAvailabilityClaimProjectRefresh 覆盖 claim→scope→apply→viewer
// 刷新→recovery 完成的完整维护链与删除型 claim。
func TestListAvailabilityClaimProjectRefresh(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	nowMS := now.UnixMilli()
	updatedAt := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1'), ('acc-2', 'viewer-1');
		INSERT INTO system_accounts (id) VALUES ('viewer-1');`); err != nil {
		t.Fatal(err)
	}

	if err := repo.EnsureRuntimeDependency(ctx, updatedAt); err != nil {
		t.Fatal(err)
	}
	// EnsureRuntimeDependency 已以 initial_projection_bootstrap 建行为
	// recovering（Node 同语义），begin 此时不得重复启动。
	started, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("recovering 状态不得重复启动重放")
	}
	if _, err := repo.EnqueueAllForRuntimeRecovery(ctx, nowMS); err != nil {
		t.Fatal(err)
	}
	bootstrapped, err := repo.EnsureViewerHealth(ctx, 100, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapped != 1 {
		t.Fatalf("应补齐 viewer-1 健康行: %d", bootstrapped)
	}
	missing, err := repo.EnqueueMissing(ctx, 100, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("全量重放后不得再发现缺失行: %d", missing)
	}
	claims, err := repo.ClaimDirty(ctx, "owner-1", 100, 30_000, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("应 claim 2 条 dirty: %d", len(claims))
	}

	scopes, err := repo.ListScopes(ctx, claimAccounts(claims))
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("应返回 2 个投影作用域: %d", len(scopes))
	}
	scopeByAccount := map[string]opsjobs.ProjectionScope{}
	for _, scope := range scopes {
		scopeByAccount[scope.AccountID] = scope
	}

	searchTerms, err := repo.LoadSearchTerms(ctx, claimAccounts(claims))
	if err != nil {
		t.Fatal(err)
	}
	if len(searchTerms) != 0 {
		t.Fatalf("无搜索文档时 terms 必须为空: %v", searchTerms)
	}

	// acc-1 投影为 rate_limited（cooling），acc-2 为 active（enabled）。
	writes := []opsjobs.ProjectionWrite{
		{
			Claim: claims[0],
			Scope: scopeByAccount[claims[0].AccountID],
			Item: opsjobs.ProjectionItem{
				AccountID:                 claims[0].AccountID,
				EffectiveStatus:           "rate_limited",
				ProviderCode:              "openai",
				ProviderProtocolProfileID: "profile-1",
				AccountType:               "api_key",
				Name:                      "账户甲",
				Payload:                   map[string]any{"id": claims[0].AccountID, "accessType": "owner"},
				TagIDs:                    []string{"tag-b", "tag-a"},
			},
			SearchTerms: []string{"账", "账户", "户甲"},
			Now:         now,
		},
		{
			Claim: claims[1],
			Scope: scopeByAccount[claims[1].AccountID],
			Item: opsjobs.ProjectionItem{
				AccountID:                 claims[1].AccountID,
				EffectiveStatus:           "active",
				ProviderCode:              "openai",
				ProviderProtocolProfileID: "profile-1",
				AccountType:               "api_key",
				Name:                      "账户乙",
				Payload:                   map[string]any{"id": claims[1].AccountID},
			},
			Now: now,
		},
	}
	applied, err := repo.ApplyClaims(ctx, writes)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if !applied[claim.ClaimToken] {
			t.Fatalf("claim %s 未被应用", claim.ClaimToken)
		}
	}
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_dirty`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("应用后 dirty 必须清空: %d", dirtyCount)
	}
	var bucket, payloadGeneration string
	if err := db.QueryRow(`SELECT schedulable_bucket, payload_json FROM account_list_availability_projections WHERE account_id = ?`, claims[0].AccountID).
		Scan(&bucket, &payloadGeneration); err != nil {
		t.Fatal(err)
	}
	if bucket != "cooling" {
		t.Fatalf("rate_limited 必须归入 cooling: %s", bucket)
	}
	var tagCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_projection_tags WHERE account_id = ?`, claims[0].AccountID).Scan(&tagCount); err != nil {
		t.Fatal(err)
	}
	if tagCount != 2 {
		t.Fatalf("tag 必须去重写入 2 行: %d", tagCount)
	}
	var overlayCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_runtime_overlays`).Scan(&overlayCount); err != nil {
		t.Fatal(err)
	}
	if overlayCount != 2 {
		t.Fatalf("投影写入必须回写 overlay 快照: %d", overlayCount)
	}

	// viewer 刷新 → is_current；随后 recovery 可完成。
	candidates, err := repo.ListViewerHealthRefreshCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != "viewer-1" {
		t.Fatalf("应返回 viewer-1 刷新候选: %v", candidates)
	}
	if err := repo.RefreshViewerHealth(ctx, "viewer-1", updatedAt); err != nil {
		t.Fatal(err)
	}
	var isCurrent int
	if err := db.QueryRow(`SELECT is_current FROM account_list_availability_projection_viewer_health WHERE viewer_system_account_id = 'viewer-1'`).Scan(&isCurrent); err != nil {
		t.Fatal(err)
	}
	if isCurrent != 1 {
		t.Fatalf("刷新后 viewer 健康必须 current: %d", isCurrent)
	}
	completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("dirty 清空后 recovery 必须完成")
	}

	// 删除型 claim：acc-2 不再可见（无 scope）→ tombstone 删除投影。
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_dirty (
			account_id, viewer_system_account_id, generation, applied_generation, reason,
			available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('acc-2', 'viewer-1', 2, 0, 'projection_scope_removed', ?, 0, ?, ?)`, nowMS, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	deletionClaims, err := repo.ClaimDirty(ctx, "owner-1", 100, 30_000, nowMS)
	if err != nil || len(deletionClaims) != 1 {
		t.Fatalf("删除型 claim 认领失败: %v %v", deletionClaims, err)
	}
	appliedDeletion, err := repo.ApplyDeletionClaim(ctx, deletionClaims[0])
	if err != nil {
		t.Fatal(err)
	}
	if !appliedDeletion {
		t.Fatal("存在投影行的删除型 claim 必须返回 true")
	}
	var projectionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_projections WHERE account_id = 'acc-2'`).Scan(&projectionCount); err != nil {
		t.Fatal(err)
	}
	if projectionCount != 0 {
		t.Fatalf("删除型 claim 必须移除投影行: %d", projectionCount)
	}

	// ReleaseForReplay：新 claim 释放后 available_at 推进且 claim 清空。
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_dirty (
			account_id, viewer_system_account_id, generation, applied_generation, reason,
			available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('acc-1', 'viewer-1', 3, 0, 'projection_refresh_failed', ?, 1, ?, ?)`, nowMS, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	replayClaims, err := repo.ClaimDirty(ctx, "owner-1", 100, 30_000, nowMS)
	if err != nil || len(replayClaims) != 1 {
		t.Fatalf("重放 claim 认领失败: %v %v", replayClaims, err)
	}
	released, err := repo.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{
		AccountID:    replayClaims[0].AccountID,
		Generation:   replayClaims[0].Generation,
		ClaimToken:   replayClaims[0].ClaimToken,
		Reason:       "projection_refresh_failed",
		RetryDelayMS: 1_000,
		NowMS:        nowMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("合法 claim 必须可释放重放")
	}
	var pendingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_dirty WHERE claim_token IS NULL AND available_at_ms > ?`, nowMS).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 {
		t.Fatalf("释放后应保留 1 条延迟重放: %d", pendingCount)
	}
}

func claimAccounts(claims []opsjobs.DirtyClaim) []string {
	ids := make([]string, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.AccountID)
	}
	return ids
}
