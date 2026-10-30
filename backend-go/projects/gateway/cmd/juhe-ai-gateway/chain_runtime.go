package main

// G20 phase-2 composition root: builds the concrete runtime services the /v1
// chain consumes from the composed business/stats handles and the runtime
// config driver axes. Every constructor mirrors the Node service factory
// (the runtimeConfig axes in the comments are the backend/src/config/runtime.ts
// fields each service forks on).
//
// Fail-fast contract: any constructor error (missing database, redis driver
// without URL, quota wiring violation) aborts startup with the named cause;
// nothing in the returned bundle is nil.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ratelimit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
)

// chainRuntimeServices bundles every concrete service the chain assembly
// consumes plus their shutdown handles.
type chainRuntimeServices struct {
	Cache           *gatewayruntimecache.Service
	Circuits        gatewaypreauth.PreAuthCircuits
	IPPolicy        gatewaypreauth.ClientIPPolicy
	UserLimits      gatewaypreauth.UserRequestLimits
	ModelsRateLimit gatewaypreauth.AuthenticatedModelsRateLimit
	Avoidance       gatewaypreauth.ClientIPAccountAvoidanceFactory
	Affinity        *gatewaygemini.InteractionAffinity
	Recoverable     gatewaypreauth.RecoverableWait
	// DispatchRecoverableWait 是同一 G11 等待引擎的 dispatch 侧句柄
	//（chain_dispatch_recoverable_wait.go 适配成
	// gatewaydispatch.RecoverableSuppressionWaiter 挂 engine.RecoverableWait；
	// 与 Recoverable 共用实例——引擎无状态，唤醒面同为
	// DefaultRecoverableWaitCoordinator）。
	DispatchRecoverableWait *gatewaycircuit.PreAuthRecoverableWait
	APIKeyQuota             *gatewayquota.APIKeyQuotaService
	AuthzQuota              *gatewayquota.AuthorizationQuotaService
	InflightQuota           *gatewayquota.InflightQuotaService
	Accounts                *chainAccountsSelector
	// Identity carries the G14 session identity + affinity services (nil is
	// rejected by the chain assembly: the preflight dereferences them).
	Identity *sessionIdentityServices
	// GroupAffinity 是组级会话绑定服务（策略路由分组会话亲和设计 §3）：独立
	// 数据面，消费方为 preauth 键派生/黏住端口、引擎最终成功写回端口与响应层
	// 三层删除。nil 兼容未装配组合（功能整体不生效）。
	GroupAffinity *gatewaysession.GroupAffinityService
	// RateLimitStore is the cacheDriver==='redis' fixed-window store for the
	// system-api rate limiter (nil keeps the memory store); see task item 9.
	RateLimitStore ratelimit.Store

	// QuotaStats is the shared G07 stats reader; the accounts runtime-reset
	// bridge (compose_accounts_reset.go) reuses it for the
	// AuthorizationQuotaExceeded port so both consumers read the same
	// juhe_stats projections through one instance.
	QuotaStats *gatewayquota.StatsStore
	// LatencyDegradation is the store-backed normal-route latency degradation
	// service. The dispatch-side ordering port stays explicitly degraded
	// (chain_dispatch.go degradedLatency); this instance owns the
	// runtime-state store the maintenance runtime-reset clear goes through
	// (accounts RuntimeResetEffects port) and is the store the dispatch
	// collaborator will mount when that slice lands.
	LatencyDegradation *gatewayproxyhealth.LatencyDegradationService
	// StateClient is the runtime-state redis client (nil unless
	// runtimeStateDriver === 'redis'); shared with the aipublic penalty-window
	// limiter so both limiter families sit in one Redis keyspace.
	StateClient *redis.Client
	// RealtimeTokens is the M5b realtime ephemeral token service (Realtime
	// design §4): built over the same runtime-state client; nil when the state
	// driver is not redis — the client_secrets endpoint degrades explicitly
	// (503), it has no correct non-redis behavior.
	RealtimeTokens *realtimetoken.Service
	// AccountAPIKeyGuard is the process-local api-key failure guard
	// (gatewayaccounteffects); the runtime-reset bridge reaches the failure
	// guard / transient tombstone clears through it.
	AccountAPIKeyGuard *gatewayaccounteffects.AccountAPIKeyFailureGuard
	// AccountAPIKeyEffects 是 D-111（BUG-0175 W4-B）的账户 API Key 运行态
	// 效果链（guard + 瞬态避让 + 合并持久写），经 chainAPIKeyEffectsPort
	// 挂上 dispatch 的 APIKeyEffectsPort。
	AccountAPIKeyEffects *gatewayaccounteffects.AccountAPIKeyEffects
	// ConfiguredPolicyAvoidance 是 D-132（BUG-0175 W4-B）的配置策略账号避让
	// 写读面（gateway-configured-account-policy-avoidance 键空间）。
	ConfiguredPolicyAvoidance *gatewayaccounteffects.ConfiguredPolicyAvoidanceService

	// ---- W2-C production wiring (BUG-0175: D-109/D-110/D-131/D-133/
	// D-134/D-136/D-137/D-129). 2026-10-24 存储形态收口后这些字段在
	// composeChainRuntimeServices 中按 PostgreSQL + Redis 恒定形态装配；
	// 组合测试的 nil 仅出现在未装配组合（链条回落 disabled*/degraded*）。----
	// AccountCircuits is the D-131 account-circuit service.
	AccountCircuits *gatewaycircuit.CircuitService
	// ClientIPSlots is the D-109 high-concurrency client-IP slot family.
	ClientIPSlots *gatewayclientip.ClientIPConcurrency
	// HighConcurrencyQueue is the D-109 high-concurrency group short queue
	// (Node runtime/high-concurrency-queue.service.ts singleton fork; the
	// memory driver keeps process-local queues, the redis driver shares the
	// juhe-ai:state:high-concurrency-queue keyspace).
	HighConcurrencyQueue *gatewayclientip.HighConcurrencyGroupQueue
	// ConcurrencyTracker 是唯一的账户并发事实源（E2E-FINDING #13）：组合根
	// 必须把同一实例传给 engine.Concurrency（chainRuntimeDeps.
	// ConcurrencyTracker）与本 HighConcurrencyQueue；事实源分裂会让 queue
	// 读数恒 0、WaitForCapacity 永远立即 Ready、请求永不入队且排队者
	// 永不被唤醒。
	ConcurrencyTracker *gatewayclientip.MemoryAccountConcurrency
	// SuppressionStore is the D-134 local account suppression state.
	SuppressionStore *gatewaycircuit.LocalSuppressionStore
	// SuppressionWaiter is the D-134 recoverable wait engine behind the
	// dispatch suppression wait.
	SuppressionWaiter *gatewaycircuit.PreAuthRecoverableWait
	// ProxyHealth is the D-136 upstream bucket health service.
	ProxyHealth *gatewayproxyhealth.ProxyHealthService
	// HotQuality is the D-137 hot-quality runtime (store pair per driver).
	HotQuality *gatewayhotquality.GatewayHotQualityRuntime
	// KeyModelStore is the D-133 key-model foreground admission store
	// (selected eagerly per runtime state driver).
	KeyModelStore gatewayaccounteffects.KeyModelRuntimeStore

	closeFuncs []func()
}

// Close releases the owned redis clients / cache subscriptions in reverse
// construction order.
func (s *chainRuntimeServices) Close() {
	for i := len(s.closeFuncs) - 1; i >= 0; i-- {
		s.closeFuncs[i]()
	}
}

// settingsTimezoneProvider adapts the settings reader onto the quota
// StatsTimezoneProvider (usageStatsTimezone mirror).
type settingsTimezoneProvider struct{ read SettingValueFunc }

func (p settingsTimezoneProvider) StatsTimezone(ctx context.Context) (*time.Location, error) {
	value, err := p.read("usageStatsTimezone")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		value = "UTC"
	}
	return time.LoadLocation(value)
}

// ipstatsTimezone adapts the same reader onto the client-ip policy timezone
// source (ipstats.TimezoneSource shape).
func ipstatsTimezone(read SettingValueFunc) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		value, err := read("usageStatsTimezone")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return "UTC", nil
		}
		return value, nil
	}
}

// quotaRuntimeStateBridge adapts the quota runtime-state store onto the
// gatewaygemini affinity store (single-key views over one named store; the
// store prefix bakes the Node createRuntimeStateStore layout).
type quotaRuntimeStateBridge struct {
	store     *gatewayquota.RedisRuntimeState
	storeName string
}

func (b quotaRuntimeStateBridge) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	return b.store.GetJSON(ctx, b.storeName, key, dst)
}

func (b quotaRuntimeStateBridge) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	return b.store.SetJSON(ctx, b.storeName, key, value, ttl)
}

func (b quotaRuntimeStateBridge) Delete(ctx context.Context, key string) error {
	return b.store.Delete(ctx, b.storeName, key)
}

// redisEvalClient adapts *redis.Client onto the Eval(...) (any, error)
// contract shared by the ratelimit fixed-window store and the user request
// limit coordinator (Node RedisCommandClient.eval).
type redisEvalClient struct{ client *redis.Client }

func (c redisEvalClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return c.client.Eval(ctx, script, keys, args...).Result()
}

// redisStateClientProvider mirrors getRedisClient(stateUrl) with failure
// invalidation (UserRequestLimitRedisClientProvider).
type redisStateClientProvider struct {
	client *redis.Client
}

func (p redisStateClientProvider) Client(context.Context) (gatewayproxyhealth.UserRequestLimitRedisClient, error) {
	if p.client == nil {
		return nil, errors.New("runtime state redis 客户端不可用")
	}
	return redisEvalClient{client: p.client}, nil
}

func (p redisStateClientProvider) Invalidate(context.Context, gatewayproxyhealth.UserRequestLimitRedisClient) {
}

// chainRuntimeLogger funnels the service warns through slog.
type chainRuntimeLogger struct{ inner *slog.Logger }

func (l chainRuntimeLogger) Warn(event string, fields map[string]any, message string) {
	args := []any{"event", event}
	for key, value := range fields {
		args = append(args, key, value)
	}
	l.inner.Warn(message, args...)
}

// chainCircuitWaitLogger adapts slog onto the gatewaycircuit.Logger
// (Info/Warn fields,message lines of the wait engine).
type chainCircuitWaitLogger struct{ inner *slog.Logger }

func (l chainCircuitWaitLogger) Info(fields map[string]any, message string) {
	l.inner.Info(message, fieldsArgs(fields)...)
}

func (l chainCircuitWaitLogger) Warn(fields map[string]any, message string) {
	l.inner.Warn(message, fieldsArgs(fields)...)
}

// composeChainRuntimeServices builds every runtime service from the composed
// handles. cfg drives the driver axes; settingValue reads global settings
// (timezone); secret decrypts account credentials.
func composeChainRuntimeServices(composed *composition, cfg runtimeConfig, settingValue SettingValueFunc) (*chainRuntimeServices, error) {
	if composed == nil {
		return nil, errors.New("网关链组合根缺少 composition")
	}
	if settingValue == nil {
		return nil, errors.New("网关链组合根缺少全局设置读取器")
	}
	// ---- W2-C production wiring (BUG-0175: D-109/D-110/D-131/D-133/
	// D-134/D-136/D-137/D-129). 2026-10-24 存储形态收口：原按驱动轴分叉的
	// 装配全部收敛为 PostgreSQL + Redis 恒定形态（redis 客户端恒建、
	// require-shared 恒开、memory 臂删除）。 ----
	logger := chainRuntimeLogger{inner: slog.Default()}
	// services 承载逆序 closeFuncs；任何一步失败都必须先 Close 再返回，
	// 否则已启动的 coordinator goroutine、cache、circuit 等会泄漏到进程。
	services := &chainRuntimeServices{}

	// ---- redis clients ----
	factory, closeFactory, err := gatewayruntimecache.NewRedisSharedCacheFactory(cfg.RedisCacheURL, cfg.RedisNamespace)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway redis shared cache factory: %w", err)
	}
	sharedFactory := factory
	services.closeFuncs = append(services.closeFuncs, closeFactory)
	cacheOptions, parseErr := redis.ParseURL(cfg.RedisCacheURL)
	if parseErr != nil {
		services.Close()
		return nil, fmt.Errorf("parse cache redis url: %w", parseErr)
	}
	cacheClient := redis.NewClient(cacheOptions)
	services.closeFuncs = append(services.closeFuncs, func() { _ = cacheClient.Close() })
	// system-api rate limiter fixed-window store (task item 9).
	services.RateLimitStore = &ratelimit.RedisStore{Client: redisEvalClient{client: cacheClient}}

	stateOptions, err := redis.ParseURL(cfg.RedisStateURL)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("parse state redis url: %w", err)
	}
	stateClient := redis.NewClient(stateOptions)
	services.closeFuncs = append(services.closeFuncs, func() { _ = stateClient.Close() })
	// Exposed for siblings that share the runtime-state client (aipublic
	// penalty-window limiter).
	services.StateClient = stateClient
	// M5b realtime ephemeral token 面（Realtime 设计 §4）：与状态键空间
	// 共用同一客户端（键位 realtime_token:<t> 经 namespace 隔离）。
	services.RealtimeTokens = realtimetoken.NewService(realtimetoken.GoRedisClient{Client: stateClient}, cfg.RedisNamespace)
	// K5 invalidation shared-version persistence (inval.SharedStore, T2
	// audit wiring): the runtime-state redis is also the SyncInvalidationsOnRead
	// gate of the runtime cache below. Go-only int64 monotonic protocol —
	// see internal/inval package docs.
	if composed.Bus != nil {
		composed.Bus.SetSharedStore(inval.NewRedisSharedStore(stateClient, cfg.RedisNamespace))
	}

	// ---- runtime cache (G10) ----
	models, err := gatewayruntimecache.NewSQLReadModels(composed.db, time.Now, nil, nil, nil)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway runtime sql read models: %w", err)
	}
	// One settings repository per process: the read models must see a
	// management settings PATCH immediately (Node clears the shared
	// systemSettingsCache on write) instead of a stale 60s snapshot.
	models.SetSettingsStore(composed.settingsStore)
	selector, selectorErr := newChainAccountsSelectorWithStats(composed.db, composed.statsDB, cfg.Secret, time.Now, cfg.DispatchAccountCandidateLimit)
	if selectorErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway accounts selector: %w", selectorErr)
	}
	services.Accounts = selector
	models.SetAccountsSelector(selector)
	// /v1/models 账户并集装载器（网关模型列表账户并集设计 §6.3/§6.4）：复用
	// 同一 selector 实例——db 句柄、table/bind 方言与授权臂判定同源，避免第二
	// 份装配漂移。
	unionLoader, unionLoaderErr := newChainGroupModelUnionLoader(selector)
	if unionLoaderErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway group model union loader: %w", unionLoaderErr)
	}
	// D-110（BUG-0175）：G08 动态路由选择器（Node
	// api-key-group-route-selector.service.ts）。redis 驱动共享轮转/权重
	// 计数器（存储形态收口后恒 redis）。
	routeSelector := gatewayrouting.NewAPIKeyGroupRouteSelector("redis", gatewayrouting.NewRedisRouteStateCounter(cfg.RedisStateURL), cfg.RedisStateURL)
	catalogSource, catalogErr := newChainCatalogSource(composed.db)
	if catalogErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway model catalog source: %w", catalogErr)
	}
	models.SetCatalogSource(catalogSource)
	// Live concurrency stays process-local (Node standalone semantics; the
	// redis-driver live counter lands with the concurrency flip slice). One
	// tracker instance is shared by the cache overlay, the dispatch store and
	// the suppression precheck projection.
	concurrencyTracker := gatewayclientip.NewMemoryAccountConcurrency(nil)
	// E2E-FINDING #13：tracker 同步暴露到 services，供生产组合根传入
	// chainRuntimeDeps.ConcurrencyTracker（engine.Concurrency），与
	// HighConcurrencyQueue 共用同一事实源。
	services.ConcurrencyTracker = concurrencyTracker
	models.SetConcurrencySource(concurrencyTracker)
	cacheOptions2 := gatewayruntimecache.Options{
		Bus:    composed.Bus,
		Logger: chainCacheEventLogger{inner: slog.Default()},
		// D3（存储形态收口）：UpdateAgeOnGet 是 standalone memory 缓存的读龄
		// 语义，redis 形态恒 false（原 `cfg.RuntimeMode == "standalone"` 判据
		// 收敛为常量）。
		UpdateAgeOnGet: false,
		// redis 运行态下跨实例失效同步恒开（原 SyncInvalidationsOnRead:
		// redisState 判据收敛）。
		SyncInvalidationsOnRead: true,
		// 2026-09-18 热路径加固：跨实例失效同步合并窗口（0 恢复逐读同步）。
		SyncMinIntervalMs: cfg.RuntimeInvalidationSyncMinIntervalMs,
		// D-110: the dynamic group-binding orderer seam (nil would keep the
		// stored binding order for every dynamic strategy mode).
		Orderer: newChainGroupBindingOrderer(routeSelector),
		// settings_updated / 跨实例失效同步（reason 为空）到达时清空本进程
		// settings 快照（Node clearSettingsRepositoryCache）：本进程写入方在
		// Store.Update 内自清；跨实例同步只能走该回调，否则远端实例的 60s
		// settings 快照会滞留到自然过期。回调仅内存置空，无 IO。
		ClearSettingsCache: composed.settingsStore.InvalidateSnapshotCache,
		// /v1/models 分组模型并集装载端口（chain_gatewaykeymodels.go）。
		Union: unionLoader,
	}
	// 模型目录 / 分组访问 / 响应检查策略的跨进程事实源固定为 Redis（PG+Redis
	// 唯一形态）；进程内 LRU 只作易失 L1。Shared 为 nil 时 runtime cache 会把
	// 本地缓存当作事实源，堆随 API Key/分组/模型组合增长（2026-10-21 生产堆
	// 逼近 GOMEMLIMIT 的接线根因）。
	cacheOptions2.Shared = sharedFactory
	cacheOptions2.RequireShared = true
	cache, err := gatewayruntimecache.New(models, cacheOptions2)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway runtime cache: %w", err)
	}
	services.Cache = cache
	services.closeFuncs = append(services.closeFuncs, cache.Close)

	// ---- G13 client-ip circuits / policy / avoidance ----
	circuits, err := gatewayclientip.NewErrorCircuit(gatewayclientip.ErrorCircuitOptions{
		RuntimeStateDriver: "redis",
		StateRedisURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway error circuit: %w", err)
	}
	services.Circuits = circuits
	services.closeFuncs = append(services.closeFuncs, circuits.Close)

	policySource, err := gatewayclientip.NewSQLPolicySource(composed.statsDB, time.Now, ipstatsTimezone(settingValue))
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create client-ip policy source: %w", err)
	}
	policyCache, err := gatewayclientip.NewPolicyCache(gatewayclientip.PolicyCacheOptions{
		// 单进程拓扑下 ProcessRole/WorkerRole 保持零值：shouldUseStatsWriterBridge
		// 恒 false，IP 命中缓冲直接经 Source.RecordClientIPPolicyHits 写本进程
		// 统计库（Node stats-writer 桥仅多进程拆分时启用，接线时需同步补齐角色）。
		// Logger 让命中缓冲丢弃/写回失败可观测（零值时这些警告被静默丢弃）。
		// CacheDriver/RuntimeMode 传 redis 形态既有值（存储形态收口后判据收敛）。
		Logger:      chainCacheEventLogger{inner: slog.Default()},
		CacheDriver: "redis",
		RuntimeMode: "performance",
		Source:      policySource,
		Shared:      sharedFactory,
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create client-ip policy cache: %w", err)
	}
	services.IPPolicy = policyCache
	services.closeFuncs = append(services.closeFuncs, policyCache.Close)

	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{
		RuntimeStateDriver: "redis",
		StateRedisURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create client-ip account avoidance: %w", err)
	}
	services.Avoidance = avoidance
	services.closeFuncs = append(services.closeFuncs, avoidance.Close)

	// ---- G13 proxy health (models rate limit + user request limits) ----
	// gatewayproxyhealth.Clock nil means the wall clock.
	penaltyLimiter := gatewayproxyhealth.NewPenaltyWindowRateLimiter(nil, true, stateClient, cfg.RedisNamespace)
	services.ModelsRateLimit = gatewayproxyhealth.NewAuthenticatedModelsRateLimitService(nil, penaltyLimiter, func(fields map[string]any, message string) {
		logger.Warn("gateway_models_rate_limit", fields, message)
	})
	counter := gatewayproxyhealth.NewUserRequestLimitCounter(nil, gatewayproxyhealth.UserRequestLimitCounterOptions{})
	coordinator := gatewayproxyhealth.NewUserRequestLimitCoordinator(counter, nil, gatewayproxyhealth.UserRequestLimitCoordinatorOptions{
		RedisEnabled:   true,
		Namespace:      cfg.RedisNamespace,
		ClientProvider: redisStateClientProvider{client: stateClient},
		Log: func(fields map[string]any, message string) {
			logger.Warn("gateway_user_request_limit_coordinator", fields, message)
		},
		ServerInstanceID: newCompositionID("gateway-chain"),
	})
	coordinator.StartCoordinator()
	services.closeFuncs = append(services.closeFuncs, func() { coordinator.StopCoordinator(nil) })
	services.UserLimits = gatewayproxyhealth.NewUserRequestLimitsService(counter, coordinator)

	// ---- G07 quota services ----
	quotaModes := gatewayquota.Modes{
		RedisCache:        true,
		RedisRuntimeState: true,
	}
	statsStore, err := gatewayquota.NewStatsStore(composed.statsDB)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway quota stats store: %w", err)
	}
	services.QuotaStats = statsStore
	snapshotRuntimeState, err := gatewayquota.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, gatewayquota.GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway quota snapshot runtime state: %w", err)
	}
	snapshotCache, err := gatewayquota.NewSnapshotCache(quotaModes, snapshotRuntimeState, time.Now, nil)
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway quota snapshot cache: %w", err)
	}
	newQuotaShared := func(name string) (gatewayquota.SharedJSONCache, error) {
		return gatewayquota.NewRedisSharedCache(cacheClient, cfg.RedisNamespace, name)
	}
	apiKeyQuotaShared, err := newQuotaShared("gateway:api-key-quota")
	if err != nil {
		return nil, err
	}
	apiKeyQuota, err := gatewayquota.NewAPIKeyQuotaService(gatewayquota.APIKeyQuotaConfig{
		Modes:    quotaModes,
		Stats:    statsStore,
		Timezone: settingsTimezoneProvider{read: settingValue},
		Snapshot: snapshotCache,
		Shared:   apiKeyQuotaShared,
		Now:      time.Now,
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create api-key quota service: %w", err)
	}
	services.APIKeyQuota = apiKeyQuota
	authzQuotaShared, err := newQuotaShared("gateway:authorization-quota")
	if err != nil {
		return nil, err
	}
	authzQuota, err := gatewayquota.NewAuthorizationQuotaService(gatewayquota.AuthorizationQuotaConfig{
		Modes:    quotaModes,
		Business: composed.db,
		Stats:    statsStore,
		Timezone: settingsTimezoneProvider{read: settingValue},
		Snapshot: snapshotCache,
		Shared:   authzQuotaShared,
		Now:      time.Now,
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create authorization quota service: %w", err)
	}
	services.AuthzQuota = authzQuota
	// Estimator mounts the pricing catalog cost estimator (G20 phase-3): the
	// in-flight reservation then derives real USD estimates like the Node
	// estimateGatewayRequestCostUsd path.
	inflightQuota, err := gatewayquota.NewInflightQuotaService(gatewayquota.InflightQuotaConfig{
		APIKeys:   apiKeyQuota,
		Estimator: newChainCostEstimator(cache),
		// D-129（BUG-0175）：快照 miss 的精确成本兜底走进程内 stats 数据库
		// 直读（Node 为 db-service IPC；Go 无跨进程，同一进程共享数据库），
		// 避免「快照恒 miss → 保护性恒 429」的反向破坏。
		DBService: newChainQuotaDBService(apiKeyQuota, authzQuota),
	})
	if err != nil {
		services.Close()
		return nil, fmt.Errorf("create inflight quota service: %w", err)
	}
	services.InflightQuota = inflightQuota

	// ---- Gemini interaction affinity (redis runtime state) ----
	affinityState, affinityErr := gatewayquota.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, "gateway-gemini-interaction-affinity")
	if affinityErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gemini interaction affinity state: %w", affinityErr)
	}
	services.Affinity = gatewaygemini.NewInteractionAffinity(quotaRuntimeStateBridge{store: affinityState, storeName: "gateway-gemini-interaction-affinity"}).WithLogger(slog.Default())

	// ---- W2-C production wiring (BUG-0175) ----
	// D-109：high_concurrency 分组的 client-IP 并发槽（Node
	// client-ip-concurrency.service.ts；redis 驱动共享 180s TTL 槽）。
	clientIPSlots, slotsErr := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{
		RuntimeStateDriver: "redis",
		StateRedisURL:      cfg.RedisStateURL,
		PolicyDefaults: gatewayclientip.HighConcurrencyPolicyDefaults{
			MaxQueueSize:        cfg.ConcurrencyGlobalMax,
			PerAPIKeyQueueLimit: cfg.ConcurrencyGlobalMax,
		},
	})
	if slotsErr != nil {
		services.Close()
		return nil, fmt.Errorf("create client-ip concurrency slots: %w", slotsErr)
	}
	services.ClientIPSlots = clientIPSlots
	services.closeFuncs = append(services.closeFuncs, clientIPSlots.Close)

	// D-109 补齐（E2E-FINDING #5）：high_concurrency 分组的分组级短队列
	//（Node runtime/high-concurrency-queue.service.ts 单例 fork）。此前
	// engine.HighConcurrencyQueue 从未装配，并发满时直接 nil panic
	//（4×EOF + panic 栈）。实现复用 gatewayclientip 队列：redis 驱动共享
	// juhe-ai:state:high-concurrency-queue 键空间；账户并发事实源与
	// cache/dispatch 同用一个 concurrencyTracker（G10 seam）。
	highConcurrencyQueue, queueErr := gatewayclientip.NewHighConcurrencyGroupQueue(gatewayclientip.HighConcurrencyQueueOptions{
		RuntimeStateDriver: "redis",
		StateRedisURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
		PolicyDefaults: gatewayclientip.HighConcurrencyPolicyDefaults{
			MaxQueueSize:        cfg.ConcurrencyGlobalMax,
			PerAPIKeyQueueLimit: cfg.ConcurrencyGlobalMax,
		},
		Concurrency: concurrencyTracker,
	})
	if queueErr != nil {
		services.Close()
		return nil, fmt.Errorf("create high-concurrency group queue: %w", queueErr)
	}
	services.HighConcurrencyQueue = highConcurrencyQueue
	services.closeFuncs = append(services.closeFuncs, highConcurrencyQueue.Close)

	// D-131：账户电路服务（Node GatewayAccountCircuitService 单例 fork；
	// SUSPECT/confirmation/父升级/恢复状态机的存储在 redis 运行态共享——
	// 存储形态收口后恒 redis）。缺陷 E（熔断观测断链）：persist 配置把主链
	// 熔断状态转换接到既有 control-plane 持久化管道（juhe_business.
	// account_circuit_incidents CAS + outbox；jobs 投影与管理页
	// circuitSummary 复用同一业务库事实源），owner gate 与
	// businesssettings/businessauth/groupdirtycursor 同源（cfg.Business*，
	// 零配置自动认领时已置真）；gate 未就绪/契约缺失时保持既有无持久观测
	// 行为（见 chain_circuit_controlplane.go）。
	accountCircuitRuntime, closeAccountCircuits, circuitsErr := newChainAccountCircuitService("redis", cfg.RedisStateURL, cfg.RedisNamespace, chainAccountCircuitPersistConfig{
		DB:                composed.db,
		Confirmed:         cfg.BusinessHandoffConfirmed,
		SchemaReady:       cfg.BusinessSchemaReady,
		NodeWriterStopped: cfg.BusinessNodeWriterStopped,
	}, chainCircuitWaitLogger{inner: slog.Default()})
	if circuitsErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway account circuit service: %w", circuitsErr)
	}
	services.AccountCircuits = accountCircuitRuntime.Service
	services.closeFuncs = append(services.closeFuncs, closeAccountCircuits)

	// D-134：本地账户屏蔽状态（Node account-local-suppression-store 单例；
	// 进程内实现，装配未设 CanUseProcessLocal——构造器默认恒可用），
	// 恢复等待引擎复用同一 wait coordinator 语义。并发投影挂共享计数器的
	// total 账户并发（Node getAccountCurrentConcurrency）。
	// R4：CircuitSuppressionDelayLadderMs 为测试注入点；nil 保持生产阶梯
	// 3000/5000/10000（NewLocalSuppressionStore 内部回落默认），生产行为不变。
	suppressionWaiter := gatewaycircuit.NewPreAuthRecoverableWait(nil, chainCircuitWaitLogger{inner: slog.Default()})
	services.SuppressionWaiter = suppressionWaiter
	services.SuppressionStore = gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{
		AccountConcurrency: func(concurrencyAccountID string) int {
			return concurrencyTracker.CurrentAccountConcurrency(concurrencyAccountID, "")
		},
		Logger:        chainSuppressionLogger{},
		DelayLadderMs: cfg.CircuitSuppressionDelayLadderMs,
	})

	// D-136：上游桶健康服务（Node proxy-health.service.ts 单例 fork；
	// runtimeStateStore 在 redis 运行态跨实例共享）。
	proxyHealthStore, healthErr := gatewayproxyhealth.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, "gateway-proxy-health")
	if healthErr != nil {
		services.Close()
		return nil, fmt.Errorf("create proxy health runtime state: %w", healthErr)
	}
	services.ProxyHealth = gatewayproxyhealth.NewProxyHealthService(nil, proxyHealthStore, gatewayproxyhealth.ProxyHealthOptions{}, func(fields map[string]any, message string) {
		logger.Warn("gateway_proxy_health", fields, message)
	})

	// D-137：热质量运行时（Node hot-quality-runtime.service.ts 单例；
	// PG+Redis 唯一形态，GetGatewayHotQualityRuntime 按 redis 驱动轴构建
	// store 对）。
	hotQuality, hotQualityErr := gatewayhotquality.GetGatewayHotQualityRuntime(context.Background(), gatewayhotquality.RuntimeDriverConfig{
		RedisStateURL:  cfg.RedisStateURL,
		RedisNamespace: cfg.RedisNamespace,
	})
	if hotQualityErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway hot quality runtime: %w", hotQualityErr)
	}
	services.HotQuality = hotQuality

	// G19 观测暗区接线（chain_obs_wiring.go）：gatewayobs 单例 Observer 挂上
	// D-131 账户电路观测口（circuit_dispatch/mutation/transition →
	// Observation）与 D-137 热质量 Observer/Logger，并向 wall budget 等进程
	// 级槽发布。构造失败按本文件组合根契约 fail-fast。
	if obsErr := wireGatewayObservabilityArms(services, cfg); obsErr != nil {
		services.Close()
		return nil, fmt.Errorf("wire gateway observability arms: %w", obsErr)
	}

	// D-133：key-model 前台准入状态存储（Node getKeyModelRuntimeStore；
	// redis 惰性构建）。启动时急切选择，redis 是唯一运行态驱动轴，构造
	// 失败 fail-fast，不在请求路径上回退。
	keyModelStores := newChainKeyModelRuntimeStoreSelector(cfg.RedisStateURL, cfg.RedisNamespace)
	keyModelStore, keyModelErr := keyModelStores.Select("redis")
	if keyModelErr != nil {
		services.Close()
		return nil, fmt.Errorf("select key-model runtime store: %w", keyModelErr)
	}
	services.KeyModelStore = keyModelStore
	// D4（存储形态收口）：原 memory 形态的 key-model 能力熔断记忆恢复驱动
	//（chain_keymodel_recovery.go）与账户电路恢复组件
	//（chain_circuit_recovery.go）随 memory 运行态退役删除——redis 形态
	// 恢复职责归 jobs（keymodelrecovery / account-circuit-recovery 任务族），
	// 生产现状即如此，gateway 进程不再装配任何进程内恢复驱动。

	// ---- recoverable wait (G11 circuit wait engine) ----
	// preauth 与 dispatch 共用同一等待引擎实例（无状态；dispatch 侧经
	// chainDispatchRecoverableWait 适配挂 engine.RecoverableWait——此前生产
	// 未装配，本地抑制耗尽路径直接快速退出，等待恢复能力缺失）。
	chainRecoverableWait := gatewaycircuit.NewPreAuthRecoverableWait(
		gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{}),
		chainCircuitWaitLogger{inner: slog.Default()},
	)
	services.Recoverable = chainRecoverableWait
	services.DispatchRecoverableWait = chainRecoverableWait

	// ---- normal-route latency degradation (redis runtime state) ----
	// Node createRuntimeStateStore('gateway-normal-route-latency-degradation');
	// redis shares it across instances.
	latencyStore, storeErr := gatewayproxyhealth.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, "gateway-normal-route-latency-degradation")
	if storeErr != nil {
		services.Close()
		return nil, fmt.Errorf("create normal route latency degradation runtime state: %w", storeErr)
	}
	// 手动模式开关位（后台自动探测总开关设计 §5）：手动账户降级不写
	// nextProbeAt / probe-index。主键单列 SELECT 只在慢样本/达标样本落库时
	// 触发；查询失败由服务侧 fail-open（与 flag=1 默认路径一致）。
	latencyProbeDisabled := newChainBackgroundProbeDisabledLookup(composed.db)
	services.LatencyDegradation = gatewayproxyhealth.NewLatencyDegradationService(latencyStore, nil, gatewayproxyhealth.LatencyDegradationOptions{
		BackgroundProbeDisabled: func(ctx context.Context, account gatewayproxyhealth.SuppressibleGatewayAccount) (bool, error) {
			return latencyProbeDisabled(ctx, account.ID)
		},
	})

	// ---- account api-key failure guard (redis transient tombstones) ----
	// gatewayaccounteffects module state: the Redis transient tombstones
	// through the lazy store factory (Node gatewayAccountApiKeyTransientStateStore()).
	guardConfig := gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "redis"}
	guardFactory := func() (gatewayaccounteffects.AccountApiKeyTransientStateStore, error) {
		return gatewayaccounteffects.NewRedisAccountApiKeyTransientStateStore(gatewayaccounteffects.RedisAccountApiKeyTransientStateStoreOptions{
			RedisURL:  cfg.RedisStateURL,
			Namespace: cfg.RedisNamespace,
		})
	}
	services.AccountAPIKeyGuard = gatewayaccounteffects.NewAccountAPIKeyFailureGuard(guardConfig, gatewayaccounteffects.SystemClock{}, nil, guardFactory)

	// ---- W4-B（BUG-0175）：D-111 API Key 效果链 + D-132 配置策略避让 ----
	// Key 级持久写走 accountkeystates（Node db-service record_account_api_key_*
	// 的进程内直写），运行态失效经 K5 总线。
	keyStatesStore, keyStatesErr := accountkeystates.NewStore(accountkeystates.Config{
		DB:     composed.db,
		Secret: cfg.Secret,
		Now:    time.Now,
		InvalidateRuntimeCache: func(reason string) {
			if composed.Bus != nil {
				composed.Bus.Invalidate(inval.TopicGatewayRuntime, reason)
			}
		},
	})
	if keyStatesErr != nil {
		services.Close()
		return nil, fmt.Errorf("create account api-key effects key states store: %w", keyStatesErr)
	}
	services.AccountAPIKeyEffects = gatewayaccounteffects.NewAccountAPIKeyEffects(
		guardConfig,
		services.AccountAPIKeyGuard,
		&chainAccountAPIKeyWriter{keyStates: keyStatesStore},
		func() {
			if composed.Bus != nil {
				composed.Bus.Invalidate(inval.TopicGatewayRuntime, "gateway_account_api_key_runtime")
			}
		},
		gatewayaccounteffects.SystemClock{},
		gatewayaccounteffects.RealScheduler{},
		gatewayaccounteffects.SlogLogger(slog.Default()),
	)

	// D-132：避让状态存储在 redis 运行态（与 jobs 只读消费同一
	// `juhe-ai:<ns>:state:gateway-configured-account-policy-avoidance:` 键空间）。
	avoidanceStore, avoidanceErr := gatewayproxyhealth.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, gatewayaccounteffects.ConfiguredPolicyAvoidanceStoreName)
	if avoidanceErr != nil {
		services.Close()
		return nil, fmt.Errorf("create configured policy avoidance runtime state: %w", avoidanceErr)
	}
	services.ConfiguredPolicyAvoidance = gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(
		avoidanceStore,
		newChainListAvailabilityDirtyMarker(composed),
		func() {
			if composed.Bus != nil {
				composed.Bus.Invalidate(inval.TopicGatewayRuntime, "gateway_configured_policy_avoidance")
			}
		},
		gatewayaccounteffects.SystemClock{},
	)

	// ---- G14 session identity + affinity services ----
	identityService, identityErr := gatewaysession.NewIdentityService(cfg.Secret)
	if identityErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway session identity service: %w", identityErr)
	}
	// redis 驱动轴恒定（存储形态收口）：账户级亲和的 cache/state 都走 Redis。
	affinityConfig := gatewaysession.AffinityConfig{
		CacheDriver:        gatewaysession.CacheDriverRedis,
		RuntimeStateDriver: gatewaysession.RuntimeStateDriverRedis,
		RedisCacheURL:      cfg.RedisCacheURL,
		RedisNamespace:     cfg.RedisNamespace,
		Secret:             cfg.Secret,
	}
	affinityService, affinityErr := gatewaysession.NewAffinityService(affinityConfig)
	if affinityErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway session affinity service: %w", affinityErr)
	}
	// 组级会话黏性（策略路由分组会话亲和设计 §3）：独立数据面（键空间/记录/
	// 端口独立），驱动与账户级亲和同源（redisCache 恒置位走 Redis，Redis
	// TTL 1h 读不刷新）。D5（存储形态收口）：memory TTL 臂（24h）随 memory
	// 驱动退役；trafficMigrationPreference 在 redis 下本无实现，生产行为
	// （关闭）不变。
	groupAffinityConfig := gatewaysession.GroupAffinityConfig{
		CacheDriver:    gatewaysession.CacheDriverRedis,
		RedisCacheURL:  cfg.RedisCacheURL,
		RedisNamespace: cfg.RedisNamespace,
		Secret:         cfg.Secret,
	}
	groupAffinityService, groupAffinityErr := gatewaysession.NewGroupAffinityService(groupAffinityConfig)
	if groupAffinityErr != nil {
		services.Close()
		return nil, fmt.Errorf("create gateway group affinity service: %w", groupAffinityErr)
	}
	services.Identity = &sessionIdentityServices{
		Identity: identityService,
		Affinity: affinityService,
		Secret:   cfg.Secret,
		// 内容指纹谱系判别器（内容指纹会话身份设计 §4.3）：进程内 LRU（容量
		// 10000 / TTL 24h），单实例生产判别精确；多实例退化为每实例
		// best-effort（设计 §7.5），不引入跨进程状态。
		ContentFingerprintLineage: gatewaysession.NewContentFingerprintLineageService(gatewaysession.ContentFingerprintLineageConfig{}),
	}
	services.GroupAffinity = groupAffinityService
	return services, nil
}

// chainRuntimeCacheLogger adapts slog onto the gatewayruntimecache.Logger
// (Warn(event, fields, message)).
type chainRuntimeCacheLogger struct{ inner *slog.Logger }

func (l chainRuntimeCacheLogger) Warn(event string, fields map[string]any, message string) {
	l.inner.Warn(message, append([]any{"event", event}, fieldsArgs(fields)...)...)
}

// chainCacheEventLogger is the alias used at the runtime cache construction.
type chainCacheEventLogger = chainRuntimeCacheLogger
