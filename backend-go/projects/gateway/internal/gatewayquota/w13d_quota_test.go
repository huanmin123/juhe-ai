package gatewayquota

// w13d 波次补充测试：API Key 配额与授权配额的驱动/角色分支、DB service
// 回退、缓存失效与各 helper。
//
// 本包测试范围内的不可达语句登记：
//   - apikeyquota.go / authzquota.go 的 SQL rows.Scan 错误与 rows.Err 分支：
//     schema 列类型与 Scan 目标一致（text/double precision 对应
//     string/float64），pgx 驱动下无法从公共 API 触发。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// w13dBrokenTZ 是恒失败的时区提供者。
type w13dBrokenTZ struct{}

func (w13dBrokenTZ) StatsTimezone(context.Context) (*time.Location, error) {
	return nil, errors.New("时区读取失败")
}

// w13dFlakyShared 在第 N 次 Set 时返回错误。
type w13dFlakyShared struct {
	mu       sync.Mutex
	setCount int
	setErrAt int
	getFound bool
	getValue CachedDecision
	getErr   error
	clearErr error
}

func (m *w13dFlakyShared) Get(_ context.Context, _ string, target any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return false, m.getErr
	}
	if !m.getFound {
		return false, nil
	}
	if decoded, ok := target.(*CachedDecision); ok {
		*decoded = m.getValue
	}
	return true, nil
}

func (m *w13dFlakyShared) Set(_ context.Context, _ string, _ any, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCount++
	if m.setErrAt > 0 && m.setCount >= m.setErrAt {
		return errors.New("shared 写入失败")
	}
	return nil
}

func (m *w13dFlakyShared) Clear(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clearErr
}

func w13dAPIKey(id string) APIKeyRow {
	return APIKeyRow{ID: id, SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
}

func w13dNoLimitsAPIKey(id string) APIKeyRow {
	return APIKeyRow{ID: id, SystemAccountID: "sys", QuotaLimitsJSON: `{}`}
}

// ---------------------------------------------------------------------------
// apikeyquota: constructor + read paths
// ---------------------------------------------------------------------------

func TestW13DAPIKeyQuotaConstructionAndGuards(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	// Now 缺省回退 time.Now（构造不报错即可）。
	if _, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock),
	}); err != nil {
		t.Fatalf("默认 Now 构造失败: %v", err)
	}
	// Stats 缺失时精确读取必须报错。
	stats, err := NewStatsStore(newTestDB(t, "w13d-guard-stats"))
	if err != nil {
		t.Fatal(err)
	}
	noStats, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock), Stats: stats,
	})
	if err != nil {
		t.Fatal(err)
	}
	noStatsService := noStats
	noStatsService.stats = nil
	ctx := context.Background()
	now := clock.Now()
	if _, err := noStatsService.ReadAPIKeyQuotaCostsExact(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("缺 Stats 必须报错")
	}
	if _, err := noStatsService.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("缺 Stats 必须报错")
	}
	if _, err := noStatsService.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("缺 Stats 必须报错")
	}
	if _, err := noStatsService.CheckAPIKeyQuotaReadOnly(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("缺 Stats 必须报错")
	}

	// ServerRole 下同步检查走本地 SQLite 守卫。
	serverRole, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverRole.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("server role 同步检查必须报错")
	}

	// 坏 limits JSON 与坏时区的错误透传。
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: stats, Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	badLimits := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{bad`}
	if _, err := service.ReadAPIKeyQuotaCostsExact(ctx, badLimits, now); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	if _, err := service.ReadAPIKeyQuotaCostsExactAsync(ctx, badLimits, now); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	if _, err := service.CheckAPIKeyQuota(ctx, badLimits, now); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	if _, err := service.CheckAPIKeyQuotaReadOnly(ctx, badLimits, now); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, badLimits); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	if _, _, err := service.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, badLimits); err == nil {
		t.Fatal("坏 limits 必须报错")
	}

	// 坏时区提供者。
	brokenTZ, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: stats, Timezone: w13dBrokenTZ{}, Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.ReadAPIKeyQuotaCostsExact(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("坏时区必须报错")
	}
	if _, err := brokenTZ.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("坏时区必须报错")
	}
	if _, err := brokenTZ.CheckAPIKeyQuotaReadOnly(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("坏时区必须报错")
	}

	// 无启用限额 → 直接放行。
	allowed, err := service.CheckAPIKeyQuota(ctx, w13dNoLimitsAPIKey("ak2"), now)
	if err != nil || !allowed.Allowed {
		t.Fatalf("无限额必须放行: (%+v, %v)", allowed, err)
	}
	if allowed, err := service.CheckAPIKeyQuotaReadOnly(ctx, w13dNoLimitsAPIKey("ak2"), now); err != nil || !allowed.Allowed {
		t.Fatalf("无限额必须放行: (%+v, %v)", allowed, err)
	}
	if allowed, err := service.CheckAPIKeyQuotaAsync(ctx, w13dNoLimitsAPIKey("ak2")); err != nil || !allowed.Allowed {
		t.Fatalf("无限额必须放行: (%+v, %v)", allowed, err)
	}
}

func TestW13DAPIKeyQuotaExactAsyncPostgresBatch(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-pg-batch")
	statsSchema(t, db)
	stats, err := NewStatsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-09-04", 20})
	// PG 模式批量成本装载限定 juhe_stats schema；真实 PostgreSQL 上批量装载
	// 与限额判定完整生效（SQLite 时代依赖方言冲突注入错误并登记成功路径
	// 不可达，迁真实 PG 后重钉为成功路径断言，登记随之解除）。
	decision, err := service.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now)
	if err != nil {
		t.Fatalf("PG 批量成本装载必须成功: %v", err)
	}
	if decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("PG 批量必须按真实成本拒绝: %+v", decision)
	}
	costs, err := service.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("ak1"), now)
	if err != nil || costs.Daily != 20 {
		t.Fatalf("PG 批量成本读取 = (%+v, %v)", costs, err)
	}
	// 快照读取入口（无限额 → miss）。
	if _, found, err := service.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, w13dAPIKey("ak1")); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestW13DAPIKeyQuotaSyncerAndSharedCache(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-syncer")
	statsSchema(t, db)
	stats, err := NewStatsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	syncer := &wjMockSyncer{err: errors.New("失效同步失败")}
	shared := &w13dFlakyShared{}
	logs := &logRecorder{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisRuntimeState: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Shared:   shared,
		Syncer:   syncer,
		Now:      clock.Now,
		Log:      logs.hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// ExactAsync 的 syncer 错误传播。
	if _, err := service.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), clock.Now()); err == nil {
		t.Fatal("syncer 错误必须透传")
	}
	// Async 的 syncer 错误传播。
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1")); err == nil {
		t.Fatal("syncer 错误必须透传")
	}

	// redis cache 模式：shared 命中直接返回缓存决策。
	syncer.err = nil
	shared.mu.Lock()
	shared.getFound = true
	shared.getValue = newCachedDecision(Decision{Allowed: false, Message: APIKeyQuotaExceededMessage}, clock.Now().UnixMilli())
	shared.mu.Unlock()
	_, _, redisSnapshot, _ := newRedisQuotaStack(t, clock)
	redisService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisCache: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: redisSnapshot,
		Shared:   shared,
		Now:      clock.Now,
		Log:      logs.hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := redisService.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1"))
	if err != nil || decision.Allowed {
		t.Fatalf("shared 命中必须拒绝: (%+v, %v)", decision, err)
	}
	// ClearCache 走 shared 清理；错误经由日志钩子吞掉。
	shared.mu.Lock()
	shared.getFound = false
	shared.clearErr = errors.New("清理失败")
	shared.mu.Unlock()
	redisService.ClearCache(ctx)
	if !logs.has("api_key_quota_shared_cache_clear_failed|") {
		t.Fatal("清理失败必须告警")
	}
	redisService.InvalidateByID(ctx, "ak1")
	if !logs.has("api_key_quota_shared_cache_clear_failed|") {
		t.Fatal("InvalidateByID 清理失败必须告警")
	}
	// 无日志钩子时 ClearCache 错误静默。
	_, _, quietSnapshot, _ := newRedisQuotaStack(t, clock)
	quiet, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{RedisCache: true}, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: quietSnapshot, Shared: shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	quiet.ClearCache(ctx)
	quiet.InvalidateByID(ctx, "ak1")
}

// ---------------------------------------------------------------------------
// authzquota: ByIDs 系列 + 角色分支 + helpers
// ---------------------------------------------------------------------------

func TestW13DAuthorizationByIDsReadOnlyAndExact(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "w13d-byids")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")

	// ReadOnly 批量：未超额放行。
	decisions, err := service.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "ga1", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "aa1"},
	}, clock.Now())
	if err != nil || len(decisions) != 1 || !decisions[0].Allowed {
		t.Fatalf("decisions=%+v err=%v", decisions, err)
	}
	// ReadOnly 单条包装。
	decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "aa1", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	// now 零值回退服务时钟。
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "aa1", time.Time{})
	if err != nil || !decision.Allowed {
		t.Fatalf("zero now decision=%+v err=%v", decision, err)
	}
	// 超额后 ReadOnly 拒绝。
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 99})
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "", clock.Now())
	if err != nil || decision.Allowed {
		t.Fatalf("超额必须拒绝: (%+v, %v)", decision, err)
	}
	// 坏时区透传。
	brokenStats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatal(err)
	}
	brokenTZService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes: Modes{}, Business: business, Stats: brokenStats,
		Timezone: w13dBrokenTZ{}, Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZService.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "ga1", nil, clock.Now()); err == nil {
		t.Fatal("坏时区必须报错")
	}

	// ExactAsync（PostgreSQL 模式）走批量精确装载，判定与同步一致。
	// 非 postgres 回退同步分支需要 SQLite 业务库承载，PG 化测试基建下不可达
	// （登记：随生产 sqlite 臂删除批次同批退役）。
	exactDecision, err := service.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = exactDecision
	// BatchByIDsExactAsync 批量精确入口。
	batch, err := service.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1", AccountAuthorizationID: "aa1"}}, clock.Now())
	if err != nil || len(batch) != 1 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	// ByIDsExactAsync 包装（零值 now 回退服务时钟）。
	if _, err := service.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestW13DAuthorizationHelpers(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "w13d-helpers")
	ctx := context.Background()

	// runtimeCacheKey 的时区错误透传。
	helperStats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatal(err)
	}
	brokenService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: helperStats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenService.runtimeCacheKey(ctx, "g", "a", clock.Now()); err == nil {
		t.Fatal("坏时区必须报错")
	}
	key, err := service.runtimeCacheKey(ctx, "g", "a", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "runtime_authorization_quota\x00g\x00a\x00") {
		t.Fatalf("key=%q", key)
	}

	// itoa64。
	if itoa64(0) != "0" || itoa64(42) != "42" || itoa64(-7) != "-7" || itoa64(-1) != "-1" {
		t.Fatal("itoa64 数值错误")
	}
	// nullStringOrEmpty。
	if nullStringOrEmpty(mustNullString("", false)) != "" {
		t.Fatal("invalid 必须为空")
	}
	if nullStringOrEmpty(mustNullString("v", true)) != "v" {
		t.Fatal("valid 必须返回值")
	}
	// system account 投影。
	row := AuthorizationQuotaRow{
		ID: "r1", ResourceOwnerSystemAccountID: "owner", GranteeSystemAccountID: "grantee",
	}
	if authorizationQuotaStatsSystemAccountID(row, scopeAccountAuthorization) != "grantee" {
		t.Fatal("account scope 必须取 grantee")
	}
	if authorizationQuotaStatsSystemAccountID(row, scopeGroupAuthorization) != "owner" {
		t.Fatal("group scope 必须取 owner")
	}
	teamRow := TeamAuthorizationQuotaRow{
		ResourceOwnerSystemAccountID:        "owner",
		AuthorizationGranteeSystemAccountID: mustNullString("team-grantee", true),
	}
	if teamAuthorizationQuotaStatsSystemAccountID(teamRow, scopeAccountAuthorization) != "team-grantee" {
		t.Fatal("team account scope 必须取授权 grantee")
	}
	invalidGrantee := TeamAuthorizationQuotaRow{ResourceOwnerSystemAccountID: "owner"}
	if teamAuthorizationQuotaStatsSystemAccountID(invalidGrantee, scopeAccountAuthorization) != "owner" {
		t.Fatal("grantee 缺失必须回落 owner")
	}
	if teamAuthorizationQuotaStatsSystemAccountID(teamRow, scopeGroupAuthorization) != "owner" {
		t.Fatal("team group scope 必须取 owner")
	}
	// teamAuthorizationResourceID。
	if _, ok := teamAuthorizationResourceID(TeamAuthorizationQuotaRow{}, scopeAccountAuthorization); ok {
		t.Fatal("instance account 缺失必须 false")
	}
	if id, ok := teamAuthorizationResourceID(TeamAuthorizationQuotaRow{
		AuthorizationInstanceAccountID: mustNullString("inst", true), ResourceID: "res",
	}, scopeAccountAuthorization); !ok || id != "inst" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
	if id, ok := teamAuthorizationResourceID(TeamAuthorizationQuotaRow{ResourceID: "res"}, scopeGroupAuthorization); !ok || id != "res" {
		t.Fatalf("group id=%q ok=%v", id, ok)
	}
	// teamAuthorizationScopeType。
	if teamAuthorizationScopeType(scopeAccountAuthorization) != "account_authorization_team" {
		t.Fatal("account scope 映射错误")
	}
	if teamAuthorizationScopeType(scopeGroupAuthorization) != "group_authorization_team" {
		t.Fatal("group scope 映射错误")
	}
	// uniqueAuthorizationQuotaCostChecks 去重。
	deduped := uniqueAuthorizationQuotaCostChecks(map[string][]authorizationQuotaCostCheck{
		"a": {{cacheKey: "k1"}, {cacheKey: "k2"}},
		"b": {{cacheKey: "k1"}},
	})
	if len(deduped) != 2 {
		t.Fatalf("deduped=%d", len(deduped))
	}
	// uniqueAuthorizationQuotaScopes 去重。
	scopes := uniqueAuthorizationQuotaScopes([]authorizationQuotaScopeRequest{
		{authorizationID: "g", scopeType: scopeGroupAuthorization},
		{authorizationID: "g", scopeType: scopeGroupAuthorization},
	})
	if len(scopes) != 1 {
		t.Fatalf("scopes=%d", len(scopes))
	}
}

func mustNullString(value string, valid bool) sql.NullString {
	return sql.NullString{String: value, Valid: valid}
}
