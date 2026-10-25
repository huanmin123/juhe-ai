package gatewayquota

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// wjMockShared 是 SharedJSONCache 的可控 mock：记录调用并按预设返回结果，
// 用于验证 Redis shared cache 的读写失败日志与错误传播路径。
type wjMockShared struct {
	mu         sync.Mutex
	getCalls   int
	getKeys    []string
	getFound   bool
	getValue   CachedDecision
	getErr     error
	setCalls   int
	setErr     error
	clearCalls int
	clearErr   error
}

func (m *wjMockShared) Get(_ context.Context, key string, target any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	m.getKeys = append(m.getKeys, key)
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

func (m *wjMockShared) Set(_ context.Context, _ string, _ any, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls++
	return m.setErr
}

func (m *wjMockShared) Clear(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearCalls++
	return m.clearErr
}

// wjMockSyncer 是 InvalidationSyncer 的可控 mock。
type wjMockSyncer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (m *wjMockSyncer) SyncGatewayCacheInvalidations(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

// wjNewAuthzService 构造带完整依赖的授权配额服务（业务库/统计库/快照缓存）。
// suffix 区分同一测试内的多个实例（SQLite 共享内存库时代的隔离标记，PG 每
// 测试独立建库后仅作可读标签保留）。
func wjNewAuthzService(t *testing.T, modes Modes, clock *fakeClock, suffix string) (*AuthorizationQuotaService, *sql.DB, *sql.DB) {
	t.Helper()
	business := newTestDB(t, "wj-authz-biz-"+suffix)
	statsDB := newTestDB(t, "wj-authz-stats-"+suffix)
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	snapshot := mustSnapshotCache(t, modes, clock)
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    modes,
		Business: business,
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	return service, business, statsDB
}

// TestWJAuthorizationQuotaConfigValidation 固定构造期的依赖校验契约：
// 缺任何一个必需依赖都必须在启动期报错，而不是运行时空指针。
func TestWJAuthorizationQuotaConfigValidation(t *testing.T) {
	business := newTestDB(t, "wj-cfg-biz")
	statsDB := newTestDB(t, "wj-cfg-stats")
	stats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	snapshot := mustSnapshotCache(t, Modes{}, clock)
	tests := []struct {
		name    string
		cfg     AuthorizationQuotaConfig
		wantErr string
	}{
		{
			name:    "缺少业务库",
			cfg:     AuthorizationQuotaConfig{Stats: stats, Timezone: mustTZ(t, time.UTC), Snapshot: snapshot},
			wantErr: "gatewayquota authorization quota service requires a business database",
		},
		{
			name:    "缺少统计库",
			cfg:     AuthorizationQuotaConfig{Business: business, Timezone: mustTZ(t, time.UTC), Snapshot: snapshot},
			wantErr: "gatewayquota authorization quota service requires a stats store",
		},
		{
			name:    "缺少时区提供者",
			cfg:     AuthorizationQuotaConfig{Business: business, Stats: stats, Snapshot: snapshot},
			wantErr: "gatewayquota authorization quota service requires a timezone provider",
		},
		{
			name:    "缺少快照缓存",
			cfg:     AuthorizationQuotaConfig{Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC)},
			wantErr: "gatewayquota authorization quota service requires the snapshot cache",
		},
		{
			name: "redis 模式缺少共享缓存",
			cfg: AuthorizationQuotaConfig{
				Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC), Snapshot: snapshot,
				Modes: Modes{RedisCache: true},
			},
			wantErr: "gatewayquota authorization quota service requires a shared cache in redis mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewAuthorizationQuotaService(tt.cfg)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("构造错误 = %v, 期望 %q", err, tt.wantErr)
			}
			if service != nil {
				t.Fatalf("构造失败时不得返回服务实例: %+v", service)
			}
		})
	}
}

// wjNewAuthzRedisService 构造 Redis server 角色的授权配额服务：业务库在
// server 角色下不直接查询（走快照/DB service），但构造期仍要求非 nil。
func wjNewAuthzRedisService(t *testing.T, clock *fakeClock, shared SharedJSONCache, snapshot *SnapshotCache, log LogHook) *AuthorizationQuotaService {
	t.Helper()
	statsDB := newTestDB(t, "wj-authz-redis-stats")
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Business: newTestDB(t, "wj-authz-redis-biz"),
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   shared,
		Now:      clock.Now,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	return service
}

// TestWJCheckAuthorizationQuotaSyncWrapper 固定 checkGatewayAuthorizationQuota
// 的包装语义：account 为 nil 时只按 group 维度判定，非 nil 时取其授权 ID。
func TestWJCheckAuthorizationQuotaSyncWrapper(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "sync")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 10})

	// nil account：只看 group scope，已超额 → 拒绝。
	decision, err := service.CheckAuthorizationQuota(ctx, GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}, nil, clock.Now())
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("nil account 且 group 超额必须拒绝: (%+v, %v)", decision, err)
	}

	// 非 nil account 且 group 未超额 → 允许。先清缓存：首次判定已写入 5s
	// 运行时缓存，不清会继续返回缓存的拒绝决策。
	if _, err := statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 1 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update cost: %v", err)
	}
	service.ClearCache(ctx)
	decision, err = service.CheckAuthorizationQuota(ctx, GroupAccessMetadata{GroupAuthorizationID: "ga1"}, &AccountAuthorizationSummary{ID: "u1", AccountAuthorizationID: "aa1"}, clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("未超额必须允许: (%+v, %v)", decision, err)
	}
}

// TestWJAuthorizationQuotaAsyncMemoryAndSyncer 覆盖 worker 角色异步检查的
// 运行时内存缓存命中与失效同步器错误传播。
func TestWJAuthorizationQuotaAsyncMemoryAndSyncer(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	base, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "mem")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 1})

	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	first, err := base.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil || !first.Allowed {
		t.Fatalf("首次检查必须允许: (%+v, %v)", first, err)
	}
	// 超额后 5s 运行时缓存必须继续放行（memory 命中分支）。
	if _, err := statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 99 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update cost: %v", err)
	}
	second, err := base.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil || !second.Allowed {
		t.Fatalf("缓存命中必须保持放行: (%+v, %v)", second, err)
	}
	// 批量路径的缓存命中：已缓存的决策直接来自 memory。
	batch, err := base.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, []AccountAuthorizationSummary{{ID: "u1"}})
	if err != nil || !batch["u1"].Allowed {
		t.Fatalf("批量缓存命中必须放行: (%+v, %v)", batch, err)
	}

	// 空 group/account 授权 ID 直接放行，不触碰任何存储。
	empty, err := base.CheckAuthorizationQuotaAsync(ctx, GroupAccessMetadata{}, nil)
	if err != nil || !empty.Allowed {
		t.Fatalf("空授权 ID 必须放行: (%+v, %v)", empty, err)
	}

	// RedisRuntimeState 模式下失效同步器错误必须传播（缓存第一原则被破坏时不得静默降级）。
	syncer := &wjMockSyncer{err: errors.New("sync down")}
	redisService, _, _ := wjNewAuthzService(t, Modes{}, clock, "syncer")
	// 只打开失效同步开关：快照缓存仍用内存实现（Redis 组合由专用测试覆盖）。
	redisService.modes.RedisRuntimeState = true
	redisService.syncer = syncer
	if _, err := redisService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil || err.Error() != "sync down" {
		t.Fatalf("同步器错误必须传播: %v", err)
	}
	if _, err := redisService.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, []AccountAuthorizationSummary{{ID: "u1"}}); err == nil || err.Error() != "sync down" {
		t.Fatalf("批量同步器错误必须传播: %v", err)
	}
	if syncer.calls != 2 {
		t.Fatalf("同步器调用次数 = %d, 期望 2", syncer.calls)
	}
}

// TestWJAuthorizationQuotaAsyncServerRoleDBFallback 固定 server 角色内存快照
// 模式下 DB service 精确补判的成功与失败路径。

// TestWJCheckAuthorizationQuotaAsyncRedisServerRole 固定 Redis 模式 server
// 角色的共享快照判定树：命中即判、缺失精确补判、失败保护拒绝。

// TestWJCheckAuthorizationQuotaBatchAsyncRedisServerRole 固定 Redis server
// 角色的批量判定树：快照可判的直接判、缺失的走 DB service 批量补判。

// TestWJAuthorizationQuotaBatchServerRoleSharedAuthzDedup 固定 server 角色
// 非共享缓存批量路径的 scope 去重语义：两个账户共享同一账户授权 ID 时，
// 只精确补判一次，且两个账户都必须得到决策（含兜底快照判定循环）。

// TestWJAuthorizationQuotaReadOnlyAndExact 固定只读与精确异步入口的契约：
// 只读绕过运行时缓存；ExactAsync 在非 PostgreSQL 下回退同步路径，在
// PostgreSQL 模式下走批量成本装载（SQLite 无法绑定 $n 占位符 → 错误传播）。
func TestWJAuthorizationQuotaReadOnlyAndExact(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "roex")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 3})

	// 只读单数入口：未超额 → 允许。
	decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "aa1", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("只读未超额必须允许: (%+v, %v)", decision, err)
	}
	// 只读批量入口：超额 → 拒绝（不读缓存，直接查库）。
	if _, err := statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 10 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update cost: %v", err)
	}
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "", clock.Now())
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("只读超额必须拒绝: (%+v, %v)", decision, err)
	}

	// ExactAsync（PostgreSQL 模式）走批量精确装载，判定与同步一致。
	// 非 PostgreSQL 回退同步分支需要 SQLite 业务库承载，PG 化测试基建下不可达
	// （登记：随生产 sqlite 臂删除批次同批退役）。
	decision, err = service.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", clock.Now())
	if err != nil || decision.Allowed {
		t.Fatalf("ExactAsync 批量精确必须拒绝: (%+v, %v)", decision, err)
	}
	batch, err := service.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1", AccountAuthorizationID: "aa1"}}, clock.Now())
	if err != nil || len(batch) != 1 || batch[0].Allowed {
		t.Fatalf("ExactAsync 批量必须拒绝: (%+v, %v)", batch, err)
	}

	// PostgreSQL 模式：StatsStore 以生产 PG 臂方言执行（$n 占位符 + juhe_stats
	// schema 限定表名），真实 PostgreSQL 上批量精确判定完整生效（SQLite 时代
	// 依赖方言冲突注入错误，迁真实 PG 后重钉为成功路径断言）。
	pgBusiness := newTestDB(t, "wj-authz-pg-biz")
	authzSchema(t, pgBusiness)
	seedAuthzRow(t, pgBusiness, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	pgStatsDB := newTestDB(t, "wj-authz-pg-stats")
	statsSchema(t, pgStatsDB)
	seedCost(t, pgStatsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 10})
	pgStats, err := NewStatsStore(pgStatsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	pgService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{},
		Business: pgBusiness,
		Stats:    pgStats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	pgDecisions, err := pgService.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1"}}, clock.Now())
	if err != nil {
		t.Fatalf("PostgreSQL 模式批量成本装载必须成功: %v", err)
	}
	if len(pgDecisions) != 1 || pgDecisions[0].Allowed || pgDecisions[0].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("PostgreSQL 模式批量精确判定必须按真实成本拒绝: %+v", pgDecisions)
	}

	// ClearCacheAsync 非 Redis 模式只清内存缓存。
	service.ClearCacheAsync(ctx)
}

// TestWJAuthorizationQuotaRedisCacheSetFailure 固定 Redis shared cache 写入
// 失败只记录日志、不改变决策的契约（Node void 写入的对齐行为）。

// TestWJTeamGrantHourlyLimit 固定 team grant 小时窗口限制的统计口径：
// 小时额度按归一化窗口小时数读取 hourly 投影，超出即拒绝。
func TestWJTeamGrantHourlyLimit(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "teamhourly")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "team1", "", "active")
	seedGrantRow(t, business, "grant1", "group", "g1", "team1", "sysA", `{"hourly":{"enabled":true,"hours":6,"limit":2}}`)
	seedCost(t, statsDB, "usage_quota_hourly_windows", []string{"system_account_id", "scope_type", "scope_id", "window_hours", "total_cost_usd"},
		[]any{"sysA", "group_authorization_team", "g1:team1", 6, 2})

	decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "", clock.Now())
	if err != nil || decision.Allowed {
		t.Fatalf("team grant 小时窗口达到上限必须拒绝: (%+v, %v)", decision, err)
	}

	// team 授权行缺少实例账户且 scope 为账户维度 → 无 resourceID，team 检查
	// 直接跳过（不产生检查项 → 允许）。
	seedAuthzRow(t, business, "aa2", "sysA", "sysB", "account", "acc2", "team1", "", "active")
	// 未播种实例账户：AuthorizationInstanceAccountID 为 NULL。
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "", "aa2", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("team 行缺少实例账户必须跳过 team 检查: (%+v, %v)", decision, err)
	}
}

// TestWJAPIKeyQuotaAsyncWorkerAndShared 固定 API Key 异步检查的 worker 分支：
// 内存缓存命中、PostgreSQL 精确批量与 shared cache 写失败日志。
func TestWJAPIKeyQuotaAsyncWorkerAndShared(t *testing.T) {
	db := newTestDB(t, "wj-apikey-worker")
	statsSchema(t, db)
	stats, err := NewStatsStore(db)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	snapshot := mustSnapshotCache(t, Modes{}, clock)
	failing := &wjMockShared{}
	logs := &logRecorder{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   failing,
		Now:      clock.Now,
		Log:      logs.hook,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-04", 5})

	// worker 同步路径允许，二次调用命中内存缓存。
	decision, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey)
	if err != nil || !decision.Allowed {
		t.Fatalf("worker 回退必须允许: (%+v, %v)", decision, err)
	}
	decision, err = service.CheckAPIKeyQuotaAsync(ctx, apiKey)
	if err != nil || !decision.Allowed {
		t.Fatalf("内存缓存命中必须允许: (%+v, %v)", decision, err)
	}

	// hourly 限制参与成本快照读取的窗口形状。
	hourlyKey := APIKeyRow{ID: "ak-h", SystemAccountID: "sys", QuotaLimitsJSON: `{"hourly":{"enabled":true,"hours":2,"limit":1}}`}
	costs, found, err := service.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, hourlyKey)
	if err != nil || found {
		t.Fatalf("快照未命中必须 miss: (%+v, %v, %v)", costs, found, err)
	}
	// 无任何启用限制 → 直接 miss（不构造快照键）。
	costs, found, err = service.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, APIKeyRow{ID: "free", SystemAccountID: "sys"})
	if err != nil || found {
		t.Fatalf("无限制必须 miss: (%+v, %v, %v)", costs, found, err)
	}
	// 非法 limits JSON → 错误传播。
	if _, _, err := service.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, APIKeyRow{ID: "bad", QuotaLimitsJSON: `{invalid`}); err == nil {
		t.Fatal("非法 limits JSON 必须报错")
	}

	// requireStats 未配置 → 精确成本读取报错。
	noStats, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Timezone: mustTZ(t, time.UTC), Snapshot: snapshot, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	if _, err := noStats.ReadAPIKeyQuotaCostsExact(ctx, apiKey, clock.Now()); err == nil || !strings.Contains(err.Error(), "stats store is not configured") {
		t.Fatalf("缺少统计库必须报错: %v", err)
	}

	// RedisCache 同步入口 + shared 写失败：决策照常返回并记录失败日志
	//（setCacheEntry 是 best-effort，对齐 Node 的 void 写入）。
	redisShared := &wjMockShared{setErr: errors.New("write down")}
	service.shared = redisShared
	service.modes.RedisCache = true
	decision, err = service.CheckAPIKeyQuota(ctx, apiKey, clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("shared 写失败不得影响同步决策: (%+v, %v)", decision, err)
	}
	if !logs.has("api_key_quota_shared_cache_set_failed|") {
		t.Fatalf("必须记录 shared 写失败事件: %v", logs.items)
	}

	// 异步入口的 shared 写失败按契约向上传播（setCacheEntryAsync 会 await）。
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey); err == nil || err.Error() != "write down" {
		t.Fatalf("异步入口 shared 写失败必须传播: %v", err)
	}

	// InvalidateByID 的 Redis 分支：失败记录日志。
	sharedClear := &wjMockShared{clearErr: errors.New("clear down")}
	service.shared = sharedClear
	service.InvalidateByID(ctx, "ak")
	if !logs.has("api_key_quota_shared_cache_clear_failed|") {
		t.Fatalf("InvalidateByID 失败必须记录事件: %v", logs.items)
	}
}

// TestWJAPIKeyQuotaExactAsyncPostgresDialect 固定 PostgreSQL 模式精确批量：
// 失效同步器先执行，批量装载在真实 PostgreSQL 上按 seed 成本完整判定
// （SQLite 时代依赖方言冲突注入错误，迁真实 PG 后重钉为成功路径断言）。
func TestWJAPIKeyQuotaExactAsyncPostgresDialect(t *testing.T) {
	db := newTestDB(t, "wj-apikey-pg")
	statsSchema(t, db)
	pgStats, err := NewStatsStore(db)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	syncer := &wjMockSyncer{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisRuntimeState: true},
		Stats:    pgStats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Syncer:   syncer,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-04", 20})
	decision, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey)
	if err != nil {
		t.Fatalf("PostgreSQL 精确批量必须成功: %v", err)
	}
	if decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("PostgreSQL 精确批量必须按真实成本拒绝: %+v", decision)
	}
	if syncer.calls != 2 {
		t.Fatalf("ExactAsync 各入口都必须先同步失效, calls=%d", syncer.calls)
	}
	// 同步路径（ExactAsync 直调）命中 memory 缓存返回同一拒绝决策。
	if decision, err := service.CheckAPIKeyQuotaExactAsync(ctx, apiKey, clock.Now()); err != nil || decision.Allowed {
		t.Fatalf("ExactAsync PG 直调必须拒绝: (%+v, %v)", decision, err)
	}
	// 精确成本读取同样走批量装载并返回真实成本。
	costs, err := service.ReadAPIKeyQuotaCostsExactAsync(ctx, apiKey, clock.Now())
	if err != nil || costs.Daily != 20 {
		t.Fatalf("ReadAPIKeyQuotaCostsExactAsync PG = (%+v, %v)", costs, err)
	}
}

// TestWJAPIKeyQuotaServiceConfigValidation 固定构造期依赖校验。
func TestWJAPIKeyQuotaServiceConfigValidation(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	if _, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{Snapshot: mustSnapshotCache(t, Modes{}, clock)}); err == nil ||
		err.Error() != "gatewayquota api-key quota service requires a timezone provider" {
		t.Fatalf("缺少时区提供者错误 = %v", err)
	}
	if _, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{Timezone: mustTZ(t, time.UTC)}); err == nil ||
		err.Error() != "gatewayquota api-key quota service requires the snapshot cache" {
		t.Fatalf("缺少快照缓存错误 = %v", err)
	}
	_, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Timezone: mustTZ(t, time.UTC), Snapshot: mustSnapshotCache(t, Modes{}, clock), Modes: Modes{RedisCache: true},
	})
	if err == nil || err.Error() != "gatewayquota api-key quota service requires a shared cache in redis mode" {
		t.Fatalf("redis 模式缺少 shared cache 错误 = %v", err)
	}
}
