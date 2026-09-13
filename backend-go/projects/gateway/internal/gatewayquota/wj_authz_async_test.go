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
// suffix 区分同一测试内的多个实例：内存 SQLite 以测试名+标签共享，重名会
// 复用同一物理库导致建表冲突。
func wjNewAuthzService(t *testing.T, modes Modes, clock *fakeClock, suffix string) (*AuthorizationQuotaService, *sql.DB, *sql.DB) {
	t.Helper()
	business := newTestDB(t, "wj-authz-biz-"+suffix)
	statsDB := newTestDB(t, "wj-authz-stats-"+suffix)
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
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
	stats, err := NewStatsStore(statsDB, false)
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
func wjNewAuthzRedisService(t *testing.T, clock *fakeClock, shared SharedJSONCache, snapshot *SnapshotCache, dbService DBServiceClient, log LogHook) *AuthorizationQuotaService {
	t.Helper()
	statsDB := newTestDB(t, "wj-authz-redis-stats")
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:     Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business:  newTestDB(t, "wj-authz-redis-biz"),
		Stats:     stats,
		Timezone:  mustTZ(t, time.UTC),
		Snapshot:  snapshot,
		Shared:    shared,
		DBService: dbService,
		Now:       clock.Now,
		Log:       log,
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
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 10})

	// nil account：只看 group scope，已超额 → 拒绝。
	decision, err := service.CheckAuthorizationQuota(ctx, GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}, nil, clock.Now())
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("nil account 且 group 超额必须拒绝: (%+v, %v)", decision, err)
	}

	// 非 nil account 且 group 未超额 → 允许。先清缓存：首次判定已写入 5s
	// 运行时缓存，不清会继续返回缓存的拒绝决策。
	if _, err := statsDB.Exec(`UPDATE usage_stats_daily SET total_cost_usd = 1 WHERE scope_id = 'ga1'`); err != nil {
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
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 1})

	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	first, err := base.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil || !first.Allowed {
		t.Fatalf("首次检查必须允许: (%+v, %v)", first, err)
	}
	// 超额后 5s 运行时缓存必须继续放行（memory 命中分支）。
	if _, err := statsDB.Exec(`UPDATE usage_stats_daily SET total_cost_usd = 99 WHERE scope_id = 'ga1'`); err != nil {
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
func TestWJAuthorizationQuotaAsyncServerRoleDBFallback(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{ServerRole: true}, clock, "fallback")
	logs := &logRecorder{}
	service.log = logs.hook
	dbService := &mockDBService{}
	service.dbService = dbService
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	account := &AccountAuthorizationSummary{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}

	// 快照不完整且受限 scope 缺失 → DB service 精确补判成功，结果回写缓存。
	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	if err := service.snapshot.ReplaceGatewayQuotaSnapshot(incomplete); err != nil {
		t.Fatalf("replace: %v", err)
	}
	dbService.mu.Lock()
	dbService.checkAuthzDec = DeniedDecision(AuthorizationQuotaExceededMessage)
	dbService.mu.Unlock()
	decision, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("DB service 补判拒绝必须生效: (%+v, %v)", decision, err)
	}
	if dbService.checkAuthzCalls != 1 || dbService.checkAuthzGroup[0] != "ga1" || dbService.checkAuthzAcct[0] != "aa1" {
		t.Fatalf("DB service 补判参数不符: calls=%d group=%v acct=%v", dbService.checkAuthzCalls, dbService.checkAuthzGroup, dbService.checkAuthzAcct)
	}

	// DB service 失败 → 保护策略继续使用快照判定（受限缺失 → 拒绝）并记录 warn。
	dbService.mu.Lock()
	dbService.checkAuthzErr = errors.New("ipc down")
	dbService.mu.Unlock()
	service.ClearCache(ctx)
	decision, err = service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed {
		t.Fatalf("补判失败必须按快照保护拒绝: (%+v, %v)", decision, err)
	}
	if !logs.has("gateway_authorization_quota_snapshot_fallback_failed|") {
		t.Fatalf("必须记录快照回退失败事件: %v", logs.items)
	}

	// DB service 未配置 → 补判错误 → 同样走快照保护。
	service.dbService = nil
	service.ClearCache(ctx)
	decision, err = service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed {
		t.Fatalf("无 DB service 必须按快照保护拒绝: (%+v, %v)", decision, err)
	}
}

// TestWJCheckAuthorizationQuotaAsyncRedisServerRole 固定 Redis 模式 server
// 角色的共享快照判定树：命中即判、缺失精确补判、失败保护拒绝。
func TestWJCheckAuthorizationQuotaAsyncRedisServerRole(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, shared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	logs := &logRecorder{}
	dbService := &mockDBService{}
	service := wjNewAuthzRedisService(t, clock, shared, snapshot, dbService, logs.hook)
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	account := &AccountAuthorizationSummary{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}

	// 完整快照且条目拒绝 group scope → 直接按快照拒绝，不触碰 DB service。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Hour); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	decision, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("共享快照拒绝必须生效: (%+v, %v)", decision, err)
	}
	if dbService.checkAuthzCalls != 0 {
		t.Fatalf("快照命中不得调用 DB service")
	}

	// 二次调用直接命中 Redis shared cache 中已缓存的拒绝决策（越过 1s memo
	// 后快照仍可用，返回值必须与缓存一致且不触发 DB service）。
	clock.Advance(2 * time.Second) // 越过 1s memo
	dbService.mu.Lock()
	dbService.checkAuthzDec = AllowedDecision()
	dbService.mu.Unlock()
	decision, err = service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("shared 缓存命中必须返回缓存的拒绝决策: (%+v, %v)", decision, err)
	}
	if dbService.checkAuthzCalls != 0 {
		t.Fatalf("shared 命中不得调用 DB service, calls=%d", dbService.checkAuthzCalls)
	}

	// 快照缺失 → DB service 精确补判成功。
	if err := runtimeState.Delete(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	decision, err = service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || !decision.Allowed {
		t.Fatalf("无快照时 DB service 补判必须生效: (%+v, %v)", decision, err)
	}
	if dbService.checkAuthzCalls != 1 {
		t.Fatalf("无快照必须精确补判, calls=%d", dbService.checkAuthzCalls)
	}

	// 无快照且 DB service 失败 → 保护性拒绝并记录事件。
	dbService.mu.Lock()
	dbService.checkAuthzErr = errors.New("ipc down")
	dbService.mu.Unlock()
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	decision, err = service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account)
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("补判失败必须保护性拒绝: (%+v, %v)", decision, err)
	}
	if !logs.has("gateway_authorization_quota_redis_exact_check_failed|") {
		t.Fatalf("必须记录 Redis 精确补判失败事件: %v", logs.items)
	}

	// Redis transport 故障（server 关闭）→ 快照可用性检查错误传播。
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	server.Close()
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, account); err == nil {
		t.Fatal("Redis 故障时快照可用性检查错误必须传播")
	}
}

// TestWJCheckAuthorizationQuotaBatchAsyncRedisServerRole 固定 Redis server
// 角色的批量判定树：快照可判的直接判、缺失的走 DB service 批量补判。
func TestWJCheckAuthorizationQuotaBatchAsyncRedisServerRole(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, shared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	logs := &logRecorder{}
	dbService := &mockDBService{}
	service := wjNewAuthzRedisService(t, clock, shared, snapshot, dbService, logs.hook)
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
		{ID: "u2", AccountAuthorizationID: "aa2"},
	}

	// 完整快照不需要 DB service 补判：u1 的 aa1 无条目 → 按快照允许；
	// u2 无受限授权 → 允许。全程不调用 DB service。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: AllowedDecision()},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Hour); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	dbService.mu.Lock()
	dbService.checkBatchDecs = []Decision{AllowedDecision()}
	dbService.mu.Unlock()
	output, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("批量 Redis 判定: %v", err)
	}
	if !output["u1"].Allowed || !output["u2"].Allowed {
		t.Fatalf("完整快照批量必须都允许: %+v", output)
	}
	if dbService.checkBatchCalls != 0 {
		t.Fatalf("完整快照不得调用 DB service, calls=%d", dbService.checkBatchCalls)
	}

	// 不完整快照：u1 受限且条目缺失 → 必须批量精确补判并回填结果。
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	incomplete.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: AllowedDecision()},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, incomplete, time.Hour); err != nil {
		t.Fatalf("write incomplete snapshot: %v", err)
	}
	output, err = service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("不完整快照批量: %v", err)
	}
	if dbService.checkBatchCalls != 1 {
		t.Fatalf("不完整快照缺失 scope 必须批量补判, calls=%d", dbService.checkBatchCalls)
	}
	if !output["u1"].Allowed || !output["u2"].Allowed {
		t.Fatalf("补判结果必须都允许: %+v", output)
	}

	// 补判失败 → 仍缺失的 u1 保护性拒绝；u2 已由快照判定，保留允许结果。
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	dbService.mu.Lock()
	dbService.checkBatchErr = errors.New("ipc down")
	dbService.mu.Unlock()
	output, err = service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("批量 Redis 判定失败路径: %v", err)
	}
	if output["u1"].Allowed || output["u1"].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("补判失败 u1 必须保护性拒绝: %+v", output["u1"])
	}
	if !output["u2"].Allowed {
		t.Fatalf("快照已判定的 u2 必须保留允许: %+v", output["u2"])
	}
	if !logs.has("gateway_authorization_quota_batch_redis_exact_check_failed|") {
		t.Fatalf("必须记录批量补判失败事件: %v", logs.items)
	}

	// 完整快照且所有受限 scope 均有条目 → 全部按快照判定，不调用 DB service。
	service.ClearCacheAsync(ctx)
	clock.Advance(2 * time.Second)
	dbService.mu.Lock()
	dbService.checkBatchErr = nil
	dbService.checkBatchCalls = 0
	dbService.mu.Unlock()
	doc = snapshotFixture("2026-09-04T01:00:00.000Z", true, true)
	doc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: AllowedDecision()},
		{ScopeType: ScopeTypeAccountAuthorization, AuthorizationID: "aa1", Decision: AllowedDecision()},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Hour); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	output, err = service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("快照全可判批量: %v", err)
	}
	if dbService.checkBatchCalls != 0 {
		t.Fatalf("快照全可判不得额外调用 DB service, calls=%d", dbService.checkBatchCalls)
	}
	if !output["u1"].Allowed || !output["u2"].Allowed {
		t.Fatalf("快照全可判必须都允许: %+v", output)
	}
}

// TestWJAuthorizationQuotaBatchServerRoleSharedAuthzDedup 固定 server 角色
// 非共享缓存批量路径的 scope 去重语义：两个账户共享同一账户授权 ID 时，
// 只精确补判一次，且两个账户都必须得到决策（含兜底快照判定循环）。
func TestWJAuthorizationQuotaBatchServerRoleSharedAuthzDedup(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{ServerRole: true}, clock, "dedup")
	dbService := &mockDBService{}
	service.dbService = dbService
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	// u1 与 u3 共享 aa1：cacheKey 相同，u3 不进入精确补判名单但必须得到决策。
	accounts := []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
		{ID: "u3", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
	}

	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	if err := service.snapshot.ReplaceGatewayQuotaSnapshot(incomplete); err != nil {
		t.Fatalf("replace: %v", err)
	}
	// DB service 只返回一条决策（与去重后的缺失名单对齐），u1 拒绝、
	// u3 依赖兜底快照判定 → 受限缺失 → 保护性拒绝。
	dbService.mu.Lock()
	dbService.checkBatchDecs = []Decision{DeniedDecision(AuthorizationQuotaExceededMessage)}
	dbService.mu.Unlock()
	output, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("共享授权批量: %v", err)
	}
	for _, accountID := range []string{"u1", "u3"} {
		if output[accountID].Allowed || output[accountID].Message != AuthorizationQuotaExceededMessage {
			t.Fatalf("%s 必须得到拒绝决策: %+v", accountID, output[accountID])
		}
	}
	if dbService.checkBatchCalls != 1 {
		t.Fatalf("共享 scope 只能补判一次, calls=%d", dbService.checkBatchCalls)
	}
}

// TestWJAuthorizationQuotaReadOnlyAndExact 固定只读与精确异步入口的契约：
// 只读绕过运行时缓存；ExactAsync 在非 PostgreSQL 下回退同步路径，在
// PostgreSQL 模式下走批量成本装载（SQLite 无法绑定 $n 占位符 → 错误传播）。
func TestWJAuthorizationQuotaReadOnlyAndExact(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "roex")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 3})

	// 只读单数入口：未超额 → 允许。
	decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "aa1", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("只读未超额必须允许: (%+v, %v)", decision, err)
	}
	// 只读批量入口：超额 → 拒绝（不读缓存，直接查库）。
	if _, err := statsDB.Exec(`UPDATE usage_stats_daily SET total_cost_usd = 10 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update cost: %v", err)
	}
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "", clock.Now())
	if err != nil || decision.Allowed || decision.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("只读超额必须拒绝: (%+v, %v)", decision, err)
	}

	// ExactAsync 非 PostgreSQL → 回退同步路径，决策与同步一致。
	decision, err = service.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", clock.Now())
	if err != nil || decision.Allowed {
		t.Fatalf("ExactAsync 回退必须拒绝: (%+v, %v)", decision, err)
	}
	batch, err := service.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1", AccountAuthorizationID: "aa1"}}, clock.Now())
	if err != nil || len(batch) != 1 || batch[0].Allowed {
		t.Fatalf("ExactAsync 批量回退必须拒绝: (%+v, %v)", batch, err)
	}

	// PostgreSQL 模式：StatsStore 以 PG 方言执行（$n 占位符 + juhe_stats
	// schema 限定表名），SQLite 无法执行 → 错误必须传播（生产中该路径由
	// 真实 PostgreSQL 承载）。
	pgBusiness := newTestDB(t, "wj-authz-pg-biz")
	authzSchema(t, pgBusiness)
	pgStatsDB := newTestDB(t, "wj-authz-pg-stats")
	statsSchema(t, pgStatsDB)
	pgStats, err := NewStatsStore(pgStatsDB, true)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	pgService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{PostgresDatabase: true},
		Business: pgBusiness,
		Stats:    pgStats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	if _, err := pgService.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1"}}, clock.Now()); err == nil {
		t.Fatal("PostgreSQL 模式批量成本装载在 SQLite 上必须报错")
	}

	// ClearCacheAsync 非 Redis 模式只清内存缓存。
	service.ClearCacheAsync(ctx)
}

// TestWJAuthorizationQuotaRedisCacheSetFailure 固定 Redis shared cache 写入
// 失败只记录日志、不改变决策的契约（Node void 写入的对齐行为）。
func TestWJAuthorizationQuotaRedisCacheSetFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, _, snapshot, _ := newRedisQuotaStack(t, clock)
	logs := &logRecorder{}
	failing := &wjMockShared{setErr: errors.New("redis write down")}
	dbService := &mockDBService{}
	service := wjNewAuthzRedisService(t, clock, failing, snapshot, dbService, logs.hook)
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}

	// DB service 补判成功但 shared 写入失败：异步入口按契约向上传播写入
	// 错误（setCacheEntryAsync 会 await shared.Set）。
	dbService.mu.Lock()
	dbService.checkAuthzDec = AllowedDecision()
	dbService.mu.Unlock()
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil || err.Error() != "redis write down" {
		t.Fatalf("异步入口 shared 写失败必须传播: %v", err)
	}
	if failing.setCalls == 0 {
		t.Fatal("必须尝试写 shared cache")
	}

	// setCacheEntry 的 best-effort 分支（对齐 Node void 写入）：写失败只记录
	// 日志、不向上传播。
	service.setCacheEntry("runtime\x00key", newCachedDecision(AllowedDecision(), clock.Now().UnixMilli()), false)
	if !logs.has("authorization_quota_shared_cache_set_failed|") {
		t.Fatalf("必须记录 shared 写失败事件: %v", logs.items)
	}

	// shared Get 返回错误 → 错误传播，不得静默当作 miss。
	failing.mu.Lock()
	failing.getErr = errors.New("redis read down")
	failing.mu.Unlock()
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil || err.Error() != "redis read down" {
		t.Fatalf("shared 读取错误必须传播: %v", err)
	}

	// ClearCacheAsync 在 Redis 模式下传播 shared Clear 错误。
	failing.mu.Lock()
	failing.clearErr = errors.New("redis clear down")
	failing.mu.Unlock()
	if err := service.ClearCacheAsync(ctx); err == nil || err.Error() != "redis clear down" {
		t.Fatalf("ClearCacheAsync 必须传播 shared 清理错误: %v", err)
	}
}

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
	stats, err := NewStatsStore(db, false)
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
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
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
// 失效同步器先执行，PG 方言在 SQLite 上无法执行 → 错误传播（生产中由真实
// PostgreSQL 承载该路径）。
func TestWJAPIKeyQuotaExactAsyncPostgresDialect(t *testing.T) {
	db := newTestDB(t, "wj-apikey-pg")
	statsSchema(t, db)
	pgStats, err := NewStatsStore(db, true)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	syncer := &wjMockSyncer{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisRuntimeState: true},
		Stats:    pgStats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Syncer:   syncer,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey); err == nil {
		t.Fatal("PostgreSQL 精确批量在 SQLite 上必须报错")
	}
	if syncer.calls != 2 {
		t.Fatalf("ExactAsync 各入口都必须先同步失效, calls=%d", syncer.calls)
	}
	// 同步路径（ExactAsync 直调）同样传播装载错误。
	if _, err := service.CheckAPIKeyQuotaExactAsync(ctx, apiKey, clock.Now()); err == nil {
		t.Fatal("ExactAsync PG 方言在 SQLite 上必须报错")
	}
	// 精确成本读取同样走批量装载并传播错误。
	if _, err := service.ReadAPIKeyQuotaCostsExactAsync(ctx, apiKey, clock.Now()); err == nil {
		t.Fatal("ReadAPIKeyQuotaCostsExactAsync PG 方言必须报错")
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
