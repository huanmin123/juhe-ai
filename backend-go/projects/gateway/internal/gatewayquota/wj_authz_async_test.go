package gatewayquota

// wj 波次补充（原 authz 异步配额专测随授权额度域退役删除，本文件保留
// API Key 配额异步链路 + shared cache/syncer mock 基建）。

import (
	"context"
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
