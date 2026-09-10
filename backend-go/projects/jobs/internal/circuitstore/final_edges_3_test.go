package circuitstore

// 收尾三：零散可达分支（校验出口、hourly 配额维度、owner lastUsedAt、
// 锁视图的账户缺失分支）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestRepoValidationBranches 覆盖若干入口校验出口。
func TestRepoValidationBranches(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	// ListScopes / LoadSearchTerms 的 id 归一化失败出口。
	if _, err := repo.ListScopes(ctx, []string{"   "}); err == nil {
		t.Fatal("空 id 必须报错")
	}
	if _, err := repo.LoadSearchTerms(ctx, []string{"   "}); err == nil {
		t.Fatal("空 id 必须报错")
	}
	// RefreshViewerHealth 的 viewer 超长出口。
	if err := repo.RefreshViewerHealth(ctx, strings.Repeat("v", 257), "2026-09-04T10:00:00.000Z"); err == nil {
		t.Fatal("viewer 超长必须报错")
	}
	// GetByScopeKey 的超长出口。
	controlRepo, _, _ := openControlPlaneFixture(t)
	if _, err := controlRepo.GetByScopeKey(ctx, strings.Repeat("k", 2049)); err == nil {
		t.Fatal("scopeKey 超长必须报错")
	}
	_ = db
}

// TestLoadAccountLockViewMissingAccount 覆盖 DEAD_CONFIRMED 但账户行缺失的分支。
func TestLoadAccountLockViewMissingAccount(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO account_lock_states (account_id, enabled, lock_state, generation) VALUES ('acc-ghost', 1, 'DEAD_CONFIRMED', 1)`); err != nil {
		t.Fatal(err)
	}
	// 账户行不存在 → 返回视图但不恢复（ErrNoRows 分支）。
	lock, recovered, err := loader.loadAccountLockView(ctx, "acc-ghost")
	if err != nil {
		t.Fatal(err)
	}
	if lock == nil || lock.lockState != "DEAD_CONFIRMED" || recovered {
		t.Fatalf("账户缺失不得恢复: %+v recovered=%v", lock, recovered)
	}
}

// TestQuotaHourlyHoursDimension 覆盖 direct 限额的 hourly 自定义窗口维度。
func TestQuotaHourlyHoursDimension(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	now := loader.now()
	timezone, err := loader.timezone.StatsTimezone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	row := authorizedSourcesRow("acc-hourly")
	row.authorizationLimitsJSON.Valid = true
	row.authorizationLimitsJSON.String = `{"hourly":{"enabled":true,"limit":5,"hours":2}}`
	seed(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd)
		VALUES ('sys-1', 'account_authorization', 'auth-acc-hourly', 2, 6)`)
	exceeded, resetAt, err := loader.loadAuthorizationQuotaStatus(ctx, []managementRow{row}, now, timezone)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded["auth-acc-hourly"] != true {
		t.Fatalf("hourly 自定义窗口超限必须为 true: %v", exceeded)
	}
	if resetAt["auth-acc-hourly"] == "" {
		t.Fatalf("hourly 超限必须给出整点重置: %v", resetAt)
	}
}

// TestComposeEntryOwnerLastUsedAt 覆盖 owner 行 lastUsedAt 候选提取。
func TestComposeEntryOwnerLastUsedAt(t *testing.T) {
	loader := &ProjectionItemLoader{}
	now := effNow()
	row := newSourcesRow("acc-used")
	row.status = "active"
	row.schedulable = 1
	row.lastUsedAt = sql.NullString{String: "2026-07-01T00:00:00.000Z", Valid: true}
	entry, err := loader.composeEntry(composeInput{row: row, payload: map[string]any{}, now: now})
	if err != nil {
		t.Fatal(err)
	}
	if entry.sortLastUsedAt == nil || *entry.sortLastUsedAt != "2026-07-01T00:00:00.000Z" {
		t.Fatalf("owner 行排序键应取 last_used_at: %v", entry.sortLastUsedAt)
	}
	if entry.payload["lastUsedAt"] != "2026-07-01T00:00:00.000Z" {
		t.Fatalf("owner 行 payload 应写 lastUsedAt: %v", entry.payload["lastUsedAt"])
	}
}

// TestProbeStateKeyShapeWithRevision 覆盖 runtime key 的 revision 回落。
func TestProbeStateKeyShapeWithRevision(t *testing.T) {
	if got := AvailabilityProbeRuntimeKey("acc", "kind", 0); got != "availability:acc:kind:r1" {
		t.Fatalf("revision < 1 应回落 1: %s", got)
	}
	if got := AvailabilityProbeRuntimeKey("  acc  ", "kind", 7); got != "availability:acc:kind:r7" {
		t.Fatalf("scope 应修剪: %s", got)
	}
	if !containsFence(&[]string{"a", "b"}, "b") || containsFence(&[]string{"a"}, "b") || containsFence(nil, "b") {
		t.Fatal("containsFence 语义不符")
	}
}
