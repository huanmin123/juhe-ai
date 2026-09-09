package authz

import (
	"context"
	"testing"
	"time"
)

// seedAuthorizationRow 直接播 resource_authorizations 运行态行（loader 聚合面）。
func (f *fixture) seedAuthorizationRow(t *testing.T, id, resourceType, resourceID, granteeID, status string) {
	t.Helper()
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id,
		resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'owner', ?, ?, 'owner', ?, ?)`, id, resourceType, resourceID, granteeID, status, now, now); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedAuthorizationSource(t *testing.T, id, authorizationID, sourceType, teamID, status string) {
	t.Helper()
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_sources (id, authorization_id,
		source_type, source_team_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'owner', ?, ?)`, id, authorizationID, sourceType, teamID, status, now, now); err != nil {
		t.Fatal(err)
	}
}

// exec 直通 SQL（缓存命中断言需要删行）。
func (f *fixture) exec(t *testing.T, query string) {
	t.Helper()
	if _, err := f.db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

// TestResourceAuthorizationStatsAggregation 锁定聚合语义（Node
// loadResourceAuthorizationStatsByResourceIdsAsync 的 SQL 逐列对照）：
// 0 授权 → {0,0}；多团队只数 active team 来源的 DISTINCT source_team_id；
// 非团队来源与已结束来源不计入。
func TestResourceAuthorizationStatsAggregation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// acc-none：无任何授权 → {0,0}。
	// acc-multi：两条活跃授权（不同 grantee，运行态表 UNIQUE 三元组约束）；
	// 授权 A 挂 team1/team2 两个活跃团队来源 + 一个 superseded 团队来源 + 一个
	// manual 来源；授权 B 挂 team2 活跃来源（与 A 的 team2 去重后 DISTINCT =
	// team1, team2 → 2）。
	f.seedAuthorizationRow(t, "ra-a1", "account", "acc-multi", "grantee-1", StatusActive)
	f.seedAuthorizationRow(t, "ra-a2", "account", "acc-multi", "grantee-2", StatusActive)
	f.seedAuthorizationRow(t, "ra-a3", "account", "acc-multi", "grantee-3", StatusRevoked)
	f.seedAuthorizationSource(t, "src-1", "ra-a1", "team", "team1", "active")
	f.seedAuthorizationSource(t, "src-2", "ra-a1", "team", "team2", "active")
	f.seedAuthorizationSource(t, "src-3", "ra-a1", "team", "team3", "superseded")
	f.seedAuthorizationSource(t, "src-4", "ra-a1", "manual", "", "active")
	f.seedAuthorizationSource(t, "src-5", "ra-a2", "team", "team2", "active")

	stats, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "account", []string{"acc-none", "acc-multi", "acc-multi", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("result ids: %v", stats)
	}
	if got := stats["acc-none"]; got != (ResourceAuthorizationStats{}) {
		t.Fatalf("no-authz stats: %+v", got)
	}
	got := stats["acc-multi"]
	if got.AuthorizationCount != 2 || got.AuthorizationTeamCount != 2 {
		t.Fatalf("multi stats: %+v", got)
	}

	// 空 ID 集合返回空映射。
	if empty, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "account", nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ids: %v %v", empty, err)
	}
}

// TestResourceAuthorizationStatsCacheHit 锁定 Node 同等缓存语义：零值与命中
// 值均入缓存；命中不回库（删行后仍返回缓存值）；updateAgeOnGet 刷新 TTL；
// TTL 过期后回库；定点失效删除单键。
func TestResourceAuthorizationStatsCacheHit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedAuthorizationRow(t, "ra-1", "account", "acc-1", "grantee-1", StatusActive)
	f.seedAuthorizationSource(t, "src-1", "ra-1", "team", "team1", "active")

	load := func() ResourceAuthorizationStats {
		stats, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "account", []string{"acc-1"})
		if err != nil {
			t.Fatal(err)
		}
		return stats["acc-1"]
	}
	if got := load(); got.AuthorizationCount != 1 || got.AuthorizationTeamCount != 1 {
		t.Fatalf("initial: %+v", got)
	}

	// 缓存命中：删除行后仍返回缓存值。
	f.exec(t, "DELETE FROM resource_authorizations")
	f.exec(t, "DELETE FROM resource_authorization_sources")
	if got := load(); got.AuthorizationCount != 1 || got.AuthorizationTeamCount != 1 {
		t.Fatalf("cached hit after delete: %+v", got)
	}

	// updateAgeOnGet：推进 4 分钟后命中刷新 TTL，再推进 4 分钟仍命中。
	f.now = f.now.Add(4 * time.Minute)
	if got := load(); got.AuthorizationCount != 1 {
		t.Fatalf("refreshed hit: %+v", got)
	}
	f.now = f.now.Add(4 * time.Minute)
	if got := load(); got.AuthorizationCount != 1 {
		t.Fatalf("updateAgeOnGet must extend the TTL: %+v", got)
	}

	// TTL 过期（再推进 5 分钟）回库：行已删 → {0,0}。
	f.now = f.now.Add(5 * time.Minute)
	if got := load(); got != (ResourceAuthorizationStats{}) {
		t.Fatalf("expired entry must reload: %+v", got)
	}

	// 重新播行后定点失效：删除缓存键后可读到新值。
	f.seedAuthorizationRow(t, "ra-2", "account", "acc-1", "grantee-2", StatusActive)
	f.store.InvalidateResourceAuthorizationStatsCache("account", "acc-1")
	if got := load(); got.AuthorizationCount != 1 || got.AuthorizationTeamCount != 0 {
		t.Fatalf("targeted invalidation must expose the new row: %+v", got)
	}

	// 全表失效：新团队来源（先 superseded 不计入，改 active 后聚合变化）+
	// 清空缓存可读到新值。
	f.seedAuthorizationSource(t, "src-2", "ra-2", "team", "team9", "superseded")
	f.store.InvalidateResourceAuthorizationStatsCache("account", "acc-1")
	if got := load(); got.AuthorizationTeamCount != 0 {
		t.Fatalf("superseded team source must stay out: %+v", got)
	}
	f.exec(t, "UPDATE resource_authorization_sources SET status = 'active'")
	f.store.InvalidateResourceAuthorizationStatsCache("")
	if got := load(); got.AuthorizationTeamCount != 1 {
		t.Fatalf("full invalidation must expose the new aggregate: %+v", got)
	}
}

// TestResourceAuthorizationStatsChunking 锁定 500 分块：>500 个 ID 分多批聚合，
// 跨批结果一致。
func TestResourceAuthorizationStatsChunking(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids := make([]string, 0, 505)
	for index := 0; index < 505; index++ {
		id := "acc-" + itoa(index)
		ids = append(ids, id)
		if index < 504 {
			f.seedAuthorizationRow(t, "ra-"+id, "account", id, "grantee-"+id, StatusActive)
		}
	}
	stats, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "account", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != len(ids) {
		t.Fatalf("result size: %d", len(stats))
	}
	// 前 504 个各 1 条活跃授权；最后一个是无聚合行的 ID → {0,0}。
	if stats["acc-0"].AuthorizationCount != 1 || stats["acc-503"].AuthorizationCount != 1 {
		t.Fatalf("chunked counts: %+v / %+v", stats["acc-0"], stats["acc-503"])
	}
	if got := stats["acc-504"]; got != (ResourceAuthorizationStats{}) {
		t.Fatalf("missing id must degrade to zero stats: %+v", got)
	}
}
