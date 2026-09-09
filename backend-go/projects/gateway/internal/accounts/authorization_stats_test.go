package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// fakeAuthorizationStats records the requested resource ids and serves fixed
// stats, so the projection contract can be asserted without a database.
type fakeAuthorizationStats struct {
	mu       sync.Mutex
	requests [][]string
	stats    map[string]AuthorizationStats
	err      error
}

func (f *fakeAuthorizationStats) ResourceAuthorizationStatsByResourceIds(_ context.Context, _ string, resourceIDs []string) (map[string]AuthorizationStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, append([]string(nil), resourceIDs...))
	if f.err != nil {
		return nil, f.err
	}
	return f.stats, nil
}

func (f *fakeAuthorizationStats) requested() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// newStatsTestEnv mirrors newAuthorizedTestEnv but also returns the accounts
// store so the test can wire the stats source fake.
func newStatsTestEnv(t *testing.T) (*testEnv, *Store, *authz.Store) {
	t.Helper()
	base := newTestEnv(t)
	for _, statement := range m10AuthzDDL {
		if _, err := base.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	authzStore, err := authz.NewStore(base.db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(base.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	base.deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: base.deps, Authorized: authzStore}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	wired := &testEnv{deps: base.deps, k: k, server: server, jar: map[string]string{}, db: base.db}
	return wired, store, authzStore
}

// TestListAuthorizationStatsProjection 锁定 Node account-summary.repository.ts
// :1608-1610 的三字段投影：owner 行注入 loader 计数（usageAvailable =
// count>0 && canManage），缺失 ID 保持零值，authorized 行恒 0/false，源错误
// 使列表失败。
func TestListAuthorizationStatsProjection(t *testing.T) {
	env, store, _ := newStatsTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-stats-1", adminID, "有授权账户", "active")
	env.seedAccount(t, "acc-stats-2", adminID, "无授权账户", "active")

	source := &fakeAuthorizationStats{stats: map[string]AuthorizationStats{
		"acc-stats-1": {AuthorizationCount: 2, AuthorizationTeamCount: 1},
	}}
	store.SetAuthorizationStatsSource(source)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("list failed: %d %v", code, payload)
	}
	items := listItems(t, payload)
	one := items["acc-stats-1"]
	if one["authorizationCount"] != float64(2) || one["authorizationTeamCount"] != float64(1) ||
		one["authorizationUsageAvailable"] != true {
		t.Fatalf("owner projection: %v", one)
	}
	// 缺失 ID 保持零值（Node `?? {0,0}` + usageAvailable=false 边界）。
	two := items["acc-stats-2"]
	if two["authorizationCount"] != float64(0) || two["authorizationTeamCount"] != float64(0) ||
		two["authorizationUsageAvailable"] != false {
		t.Fatalf("missing projection: %v", two)
	}
	// loader 以 'account' 资源类型批量收到当页全部账户 ID。
	if len(source.requested()) != 1 {
		t.Fatalf("expected one batched request, got %v", source.requested())
	}

	// 源错误使列表失败（与 hydrateListUsage 同款失败契约）。
	failing := &fakeAuthorizationStats{err: context.DeadlineExceeded}
	store.SetAuthorizationStatsSource(failing)
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/accounts", ""); code == http.StatusOK {
		t.Fatalf("source error must fail the list")
	}
}

// TestListAuthorizationStatsAuthorizedView 锁定 isAuthorizedView 分支：授权
// 实例行（grantee self 列表）三字段恒 0/false，即使 loader 带回了计数。
func TestListAuthorizationStatsAuthorizedView(t *testing.T) {
	env, store, authzStore := newStatsTestEnv(t)
	ownerID := env.login(t, "owner2", "owner-pass", "user")
	memberID := env.login(t, "member2", "member-pass", "user")
	env.seedAccount(t, "acc-inst-src", ownerID, "源账户", "active")
	env.seedTeamMember(t, "team-stats", ownerID, memberID)

	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-inst-src",
		GranteeType: "team", GranteeID: "team-stats",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-inst-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("team runtime row missing")
	}
	env.seedAuthorizationInstance(t, "acc-inst", memberID, runtimeID, "acc-inst-src")

	source := &fakeAuthorizationStats{stats: map[string]AuthorizationStats{
		"acc-inst": {AuthorizationCount: 3, AuthorizationTeamCount: 2},
	}}
	store.SetAuthorizationStatsSource(source)

	env.login(t, "member2", "member-pass", "user")
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member list: %d %v", code, listed)
	}
	items := listItems(t, listed)
	instance := items["acc-inst"]
	if instance["accessType"] != "authorized" {
		t.Fatalf("instance accessType: %v", instance["accessType"])
	}
	if instance["authorizationCount"] != float64(0) || instance["authorizationTeamCount"] != float64(0) ||
		instance["authorizationUsageAvailable"] != false {
		t.Fatalf("authorized view must zero the trio: %v", instance)
	}
}

// TestHydrateAuthorizationStatsCanManageBoundary 单测 canManageResourceOwner
// 边界：非管理员且非 owner 的 viewer 不满足 usageAvailable（Node
// canManageResourceOwner 分支），admin 恒满足。
func TestHydrateAuthorizationStatsCanManageBoundary(t *testing.T) {
	env, store, _ := newStatsTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-boundary", "someone-else", "他人账户", "active")

	source := &fakeAuthorizationStats{stats: map[string]AuthorizationStats{
		"acc-boundary": {AuthorizationCount: 1, AuthorizationTeamCount: 0},
	}}
	store.SetAuthorizationStatsSource(source)

	records := []listRow{{id: "acc-boundary", systemAccountID: "someone-else"}}
	build := func() []ListItem {
		items := []ListItem{{ID: "acc-boundary", AccessType: "owner"}}
		if err := store.hydrateAuthorizationStats(context.Background(), AccessScope{ViewerID: "viewer-x", IsAdmin: false}, items, records); err != nil {
			t.Fatal(err)
		}
		return items
	}
	item := build()[0]
	if item.AuthorizationCount != 1 || item.AuthorizationTeamCount != 0 || item.AuthorizationUsageAvailable {
		t.Fatalf("non-manager viewer must not see usageAvailable: %+v", item)
	}

	adminItems := []ListItem{{ID: "acc-boundary", AccessType: "owner"}}
	if err := store.hydrateAuthorizationStats(context.Background(), AccessScope{ViewerID: adminID, IsAdmin: true}, adminItems, records); err != nil {
		t.Fatal(err)
	}
	if !adminItems[0].AuthorizationUsageAvailable {
		t.Fatalf("admin must see usageAvailable: %+v", adminItems[0])
	}

	ownerItems := []ListItem{{ID: "acc-boundary", AccessType: "owner"}}
	if err := store.hydrateAuthorizationStats(context.Background(), AccessScope{ViewerID: "someone-else", IsAdmin: false}, ownerItems, records); err != nil {
		t.Fatal(err)
	}
	if !ownerItems[0].AuthorizationUsageAvailable {
		t.Fatalf("owner must see usageAvailable: %+v", ownerItems[0])
	}

	// count=0 边界：即使 canManage 为真，usageAvailable 仍为 false。
	zeroSource := &fakeAuthorizationStats{stats: map[string]AuthorizationStats{
		"acc-boundary": {AuthorizationCount: 0, AuthorizationTeamCount: 0},
	}}
	store.SetAuthorizationStatsSource(zeroSource)
	zeroItems := []ListItem{{ID: "acc-boundary", AccessType: "owner"}}
	if err := store.hydrateAuthorizationStats(context.Background(), AccessScope{ViewerID: adminID, IsAdmin: true}, zeroItems, records); err != nil {
		t.Fatal(err)
	}
	if zeroItems[0].AuthorizationUsageAvailable {
		t.Fatalf("zero count must render usageAvailable=false: %+v", zeroItems[0])
	}

	// nil source 降级：字段保持零值。
	if err := store.hydrateAuthorizationStats(context.Background(), AccessScope{ViewerID: adminID, IsAdmin: true}, []ListItem{{ID: "acc-boundary"}}, records); err != nil {
		t.Fatal(err)
	}
}
