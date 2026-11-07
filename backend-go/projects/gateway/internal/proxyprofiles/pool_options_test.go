package proxyprofiles

// 代理池接入测试（代理池设计 §8.2/§4.5/§6.2/§7.4）：options kinds/
// selectedPoolIds 契约与兼容、池选项 SQL、代理删除的池引用守卫、连接变更
// 与重新启用的池健康投影重置、提交后失效。PG 部分走 pgtestkit 授权门
// （缺 JUHE_AI_TEST_EXTERNAL=1 跳过）；kinds/selectedIds 解析为纯逻辑，
// 离线可跑。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestParseProxyOptionKindsDefaultsToProfile(t *testing.T) {
	kinds, message := parseProxyOptionKinds(urlValuesOf(map[string][]string{}))
	if message != "" || !kinds.profile || kinds.pool {
		t.Fatalf("default kinds wrong: %#v %q", kinds, message)
	}
	if kinds, message = parseProxyOptionKinds(urlValuesOf(map[string][]string{"kinds": {""}})); message != "" || !kinds.profile || kinds.pool {
		t.Fatalf("empty kinds wrong: %#v %q", kinds, message)
	}
	if kinds, message = parseProxyOptionKinds(urlValuesOf(map[string][]string{"kinds": {"profile,pool"}})); message != "" || !kinds.profile || !kinds.pool {
		t.Fatalf("combined kinds wrong: %#v %q", kinds, message)
	}
	if kinds, message = parseProxyOptionKinds(urlValuesOf(map[string][]string{"kinds": {" pool "}})); message != "" || kinds.profile || !kinds.pool {
		t.Fatalf("pooled kinds wrong: %#v %q", kinds, message)
	}
	if _, message = parseProxyOptionKinds(urlValuesOf(map[string][]string{"kinds": {"profile,pool,other"}})); message != "代理选项 kinds 无效" {
		t.Fatalf("unknown kind not rejected: %q", message)
	}
	if _, message = parseProxyOptionKinds(urlValuesOf(map[string][]string{"kinds": {"profile,"}})); message != "代理选项 kinds 无效" {
		t.Fatalf("trailing comma not rejected: %q", message)
	}
}

func TestParseSelectedPoolOptionIds(t *testing.T) {
	if _, message := parseSelectedPoolOptionIds(url.Values{"selectedPoolIds": {"a,b"}}); message != "代理选项 selectedPoolIds 无效" {
		t.Fatalf("csv pool ids not rejected: %q", message)
	}
	if _, message := parseSelectedPoolOptionIds(url.Values{"selectedPoolIds[0]": {"a"}}); message != "代理选项 selectedPoolIds 无效" {
		t.Fatalf("indexed pool ids not rejected: %q", message)
	}
	many := url.Values{}
	for index := 0; index < 21; index++ {
		many["selectedPoolIds[]"] = append(many["selectedPoolIds[]"], "pool-"+strconv.Itoa(index))
	}
	if _, message := parseSelectedPoolOptionIds(many); message != "代理选项 selectedPoolIds 最多 20 个" {
		t.Fatalf("pool ids cap not enforced: %q", message)
	}
	ids, message := parseSelectedPoolOptionIds(url.Values{"selectedPoolIds": {"b", " a "}, "selectedPoolIds[]": {"b"}})
	if message != "" || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("pool ids merge wrong: %#v %q", ids, message)
	}
}

// poolTestSchema 扩展 proxySchema：池三表 + accounts.proxy_pool_id +
// proxy_profiles 池健康列（与 proxyprofiles_test.go 的 proxySchema 同源，
// 这里独立建表避免交叉影响既有测试）。
const poolTestSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq;
	CREATE TABLE juhe_business.proxy_profiles (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
		type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT,
		password_encrypted TEXT, enabled BOOLEAN NOT NULL DEFAULT true, test_status TEXT NOT NULL DEFAULT 'unknown',
		latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT,
		last_tested_at TIMESTAMPTZ,
		pool_health_status TEXT CHECK (pool_health_status IS NULL OR pool_health_status IN ('healthy','degraded','dead','unknown')),
		pool_health_failure_streak INTEGER NOT NULL DEFAULT 0,
		pool_health_checked_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
	CREATE UNIQUE INDEX idx_proxy_profiles_name_unique ON juhe_business.proxy_profiles(name);
	CREATE TABLE juhe_business.proxy_pools (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
		strategy TEXT NOT NULL CHECK (strategy IN ('round_robin', 'weighted')),
		enabled BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
	CREATE UNIQUE INDEX idx_proxy_pools_name_unique ON juhe_business.proxy_pools(name);
	CREATE TABLE juhe_business.proxy_pool_members (
		pool_id TEXT NOT NULL REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT,
		proxy_profile_id TEXT NOT NULL REFERENCES juhe_business.proxy_profiles(id) ON DELETE RESTRICT,
		priority INTEGER NOT NULL CHECK (priority >= 0),
		weight INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
		enabled BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (pool_id, proxy_profile_id),
		UNIQUE (pool_id, priority));
	CREATE TABLE juhe_business.accounts (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, deleted_at TEXT, proxy_profile_id TEXT,
		proxy_pool_id TEXT REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT);
`

type poolProfileFixture struct {
	store  *Store
	db     *sql.DB
	inval  *recordingInvalidator
	health *recordingHealthResetter
	now    time.Time
}

type recordingInvalidator struct {
	reasons []string
}

func (r *recordingInvalidator) Invalidate(topic, reason string) {
	r.reasons = append(r.reasons, reason)
}

type recordingHealthResetter struct {
	ids []string
}

func (r *recordingHealthResetter) ResetMemberHealth(profileID string) {
	r.ids = append(r.ids, profileID)
}

func newPoolProfileFixture(t *testing.T) *poolProfileFixture {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, poolTestSchema)
		return err
	})
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	fixture := &poolProfileFixture{db: db, inval: &recordingInvalidator{}, health: &recordingHealthResetter{}, now: now}
	sequence := 0
	store, err := NewStore(Deps{
		DB:     db,
		Secret: "test-secret",
		Now:    func() time.Time { return now },
		NewID: func(prefix string) string {
			sequence++
			return prefix + "_test_" + strconv.Itoa(sequence)
		},
		Inval:        fixture.inval,
		MemberHealth: fixture.health,
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	fixture.store = store
	return fixture
}

func (f *poolProfileFixture) seedProfile(t *testing.T, id, name string, enabled bool) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ($1, 'sa-1', $2, 'http', 'h.internal', 8080, $3, '2026-11-01', '2026-11-01')`, id, name, enabled); err != nil {
		t.Fatalf("seed profile %s: %v", id, err)
	}
}

func TestProxyOptionsKindsCompatibilityAndPoolArm(t *testing.T) {
	fixture := newPoolProfileFixture(t)
	fixture.seedProfile(t, "p-1", "Beta代理", true)
	fixture.seedProfile(t, "p-2", "Alpha代理", true)
	fixture.seedProfile(t, "p-3", "停用代理", false)
	if _, err := fixture.db.Exec(`INSERT INTO juhe_business.proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES ('pool-1', 'sa-1', 'Alpha池', 'round_robin', true, '2026-11-01', '2026-11-01'),
		       ('pool-2', 'sa-1', '停用池', 'weighted', false, '2026-11-02', '2026-11-02')`); err != nil {
		t.Fatalf("seed pools: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
		VALUES ('pool-1', 'p-1', 0, 1, true, '2026-11-01', '2026-11-01'),
		       ('pool-1', 'p-2', 1, 2, false, '2026-11-01', '2026-11-01')`); err != nil {
		t.Fatalf("seed members: %v", err)
	}
	// 缺省 kinds=profile：现有调用方形状不变（id/name/type/enabled + kind）。
	profileOptions, err := fixture.store.ListOptions(context.Background(), "", 50, []string{"p-1"})
	if err != nil {
		t.Fatalf("profile options: %v", err)
	}
	if len(profileOptions) != 2 || profileOptions[0].ID != "p-2" || profileOptions[1].ID != "p-1" ||
		profileOptions[0].Kind != OptionKindProfile || profileOptions[0].Type != "http" || profileOptions[1].Type != "http" {
		t.Fatalf("default options wrong: %#v", profileOptions)
	}
	if profileOptions[0].MemberCount != nil {
		t.Fatalf("profile item carries pool counts: %#v", profileOptions[0])
	}
	// kinds=pool：仅 enabled 池；不返回成员/host/凭据；计数正确
	//（停用成员不可分配 → member=2 assignable=1）。
	poolOptions, err := fixture.store.ListPoolOptions(context.Background(), "", 50, []string{"pool-2"})
	if err != nil {
		t.Fatalf("pool options: %v", err)
	}
	if len(poolOptions) != 1 || poolOptions[0].ID != "pool-1" || poolOptions[0].Kind != OptionKindPool ||
		poolOptions[0].Type != "" || !poolOptions[0].Enabled ||
		poolOptions[0].MemberCount == nil || *poolOptions[0].MemberCount != 2 ||
		poolOptions[0].AssignableMemberCount == nil || *poolOptions[0].AssignableMemberCount != 1 {
		t.Fatalf("pool options wrong: %#v", poolOptions)
	}
	encoded, _ := json.Marshal(poolOptions)
	for _, forbidden := range []string{"members", "host", "port", "username", "password", "h.internal"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("pool options leak %s: %s", forbidden, string(encoded))
		}
	}
	// 停用池只在显式选中时也不返回（enabled 口径一致）。
	selectedDisabled, err := fixture.store.ListPoolOptions(context.Background(), "停用", 50, []string{"pool-2"})
	if err != nil || len(selectedDisabled) != 0 {
		t.Fatalf("disabled pool leaked: %#v %v", selectedDisabled, err)
	}
	// 跨 kind 合并（optionsHandler 同一排序路径）：name ASC。
	combined := []optionRow{}
	profileRows, err := fixture.store.profileOptionRows(context.Background(), "", 50, nil)
	if err != nil {
		t.Fatalf("profile rows: %v", err)
	}
	poolRows, err := fixture.store.poolOptionRows(context.Background(), "", 50, nil)
	if err != nil {
		t.Fatalf("pool rows: %v", err)
	}
	combined = append(combined, profileRows...)
	combined = append(combined, poolRows...)
	sortMergedOptionRows(combined)
	merged := optionSummaries(combined)
	if len(merged) != 3 || merged[0].Name != "Alpha代理" || merged[1].Name != "Alpha池" || merged[2].Name != "Beta代理" {
		t.Fatalf("cross-kind merge wrong: %#v", merged)
	}
}

func TestProxyDeleteBlockedByPoolMembership(t *testing.T) {
	fixture := newPoolProfileFixture(t)
	fixture.seedProfile(t, "p-1", "被引用代理", true)
	if _, err := fixture.db.Exec(`INSERT INTO juhe_business.proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES ('pool-1', 'sa-1', '池', 'round_robin', true, '2026-11-01', '2026-11-01')`); err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
		VALUES ('pool-1', 'p-1', 0, 1, true, '2026-11-01', '2026-11-01')`); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	_, err := fixture.store.Delete(context.Background(), "p-1")
	var referenced *PoolMemberReferenceError
	if err == nil || !asPoolReferenceError(err, &referenced) {
		t.Fatalf("pool reference guard missing: %v", err)
	}
	if referenced.ErrorCode() != "proxy_pool_member_reference_conflict" || referenced.Error() != "代理被代理池引用，请先解除成员关系" {
		t.Fatalf("reference conflict wrong: %q %q", referenced.ErrorCode(), referenced.Error())
	}
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.proxy_profiles WHERE id = 'p-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("profile deleted despite reference: %d %v", count, err)
	}
	// 解除成员关系后可删除，并补上失效与健康键清理（§7.4）。
	if _, err := fixture.db.Exec(`DELETE FROM juhe_business.proxy_pool_members WHERE pool_id = 'pool-1'`); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	name, err := fixture.store.Delete(context.Background(), "p-1")
	if err != nil || name != "被引用代理" {
		t.Fatalf("delete after unreference failed: %q %v", name, err)
	}
	if len(fixture.inval.reasons) != 1 || fixture.inval.reasons[0] != "proxy_profile_updated" {
		t.Fatalf("delete invalidation wrong: %#v", fixture.inval.reasons)
	}
	if len(fixture.health.ids) != 1 || fixture.health.ids[0] != "p-1" {
		t.Fatalf("delete health reset wrong: %#v", fixture.health.ids)
	}
}

func asPoolReferenceError(err error, target **PoolMemberReferenceError) bool {
	typed, ok := err.(*PoolMemberReferenceError)
	if ok {
		*target = typed
	}
	return ok
}

func TestProxyPatchResetsPoolHealthOnConnectionChange(t *testing.T) {
	fixture := newPoolProfileFixture(t)
	fixture.seedProfile(t, "p-1", "代理一", true)
	if _, err := fixture.db.Exec(`UPDATE juhe_business.proxy_profiles
		SET pool_health_status = 'dead', pool_health_failure_streak = 3, pool_health_checked_at = '2026-11-02', test_status = 'passed' WHERE id = 'p-1'`); err != nil {
		t.Fatalf("seed health: %v", err)
	}
	// 连接字段变更：test 状态与池健康投影同时重置（§6.2）。
	host := "new.internal"
	outcome, err := fixture.store.Patch(context.Background(), "p-1", proxyInput{Host: &host, ExpectedUpdatedAt: "2026-11-01T00:00:00.000000Z"})
	if err != nil || outcome == nil || !outcome.Mutation.Changed {
		t.Fatalf("connection patch failed: %v %#v", err, outcome)
	}
	var status sql.NullString
	var streak int
	var checkedAt sql.NullString
	if err := fixture.db.QueryRow(`SELECT pool_health_status, pool_health_failure_streak, to_char(pool_health_checked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
		FROM juhe_business.proxy_profiles WHERE id = 'p-1'`).Scan(&status, &streak, &checkedAt); err != nil {
		t.Fatalf("read health: %v", err)
	}
	if !status.Valid || status.String != "unknown" || streak != 0 || checkedAt.Valid {
		t.Fatalf("connection change did not reset pool health: %s %d %s", status.String, streak, checkedAt.String)
	}
	if len(fixture.health.ids) != 1 || fixture.health.ids[0] != "p-1" {
		t.Fatalf("member health key not reset: %#v", fixture.health.ids)
	}
	if len(fixture.inval.reasons) != 1 || fixture.inval.reasons[0] != "proxy_profile_updated" {
		t.Fatalf("patch invalidation wrong: %#v", fixture.inval.reasons)
	}
}

func TestProxyPatchResetsCheckedAtOnReEnable(t *testing.T) {
	fixture := newPoolProfileFixture(t)
	fixture.seedProfile(t, "p-1", "代理一", false)
	if _, err := fixture.db.Exec(`UPDATE juhe_business.proxy_profiles
		SET pool_health_status = 'degraded', pool_health_failure_streak = 1, pool_health_checked_at = '2026-11-02' WHERE id = 'p-1'`); err != nil {
		t.Fatalf("seed health: %v", err)
	}
	// 重新启用：只清 pool_health_checked_at，状态/streak 保留既有检测事实。
	enabled := true
	outcome, err := fixture.store.Patch(context.Background(), "p-1", proxyInput{Enabled: &enabled, ExpectedUpdatedAt: "2026-11-01T00:00:00.000000Z"})
	if err != nil || outcome == nil || !outcome.Mutation.Changed {
		t.Fatalf("re-enable patch failed: %v %#v", err, outcome)
	}
	var status sql.NullString
	var streak int
	var checkedAt sql.NullString
	if err := fixture.db.QueryRow(`SELECT pool_health_status, pool_health_failure_streak, to_char(pool_health_checked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
		FROM juhe_business.proxy_profiles WHERE id = 'p-1'`).Scan(&status, &streak, &checkedAt); err != nil {
		t.Fatalf("read health: %v", err)
	}
	if !status.Valid || status.String != "degraded" || streak != 1 || checkedAt.Valid {
		t.Fatalf("re-enable reset wrong: %s %d %s", status.String, streak, checkedAt.String)
	}
	if len(fixture.health.ids) != 0 {
		t.Fatalf("re-enable should not delete health keys: %#v", fixture.health.ids)
	}
}

func TestProxyCreatePublishesProfileInvalidation(t *testing.T) {
	fixture := newPoolProfileFixture(t)
	name := "新代理"
	proxyType := "http"
	host := "h.internal"
	port := 8080
	input := proxyInput{Name: &name, Type: &proxyType, Host: &host, Port: &port}
	if _, err := fixture.store.Create(context.Background(), input, "sa-1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(fixture.inval.reasons) != 1 || fixture.inval.reasons[0] != "proxy_profile_updated" {
		t.Fatalf("create invalidation wrong: %#v", fixture.inval.reasons)
	}
}
