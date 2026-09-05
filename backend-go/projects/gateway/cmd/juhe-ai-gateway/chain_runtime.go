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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ratelimit"
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
	APIKeyQuota     *gatewayquota.APIKeyQuotaService
	AuthzQuota      *gatewayquota.AuthorizationQuotaService
	InflightQuota   *gatewayquota.InflightQuotaService
	Accounts        *chainAccountsSelector
	// HybridScoringCache / HybridRuntimeState are the cacheDriver==='redis'
	// collaborators of the hybrid route resolver (nil keeps the memory
	// drivers, mirroring the Node runtimeConfig axes).
	HybridScoringCache gatewayhybrid.SharedJSONCache
	HybridRuntimeState gatewayhybrid.RuntimeStateStore
	// Identity carries the G14 session identity + affinity services (nil is
	// rejected by the chain assembly: the preflight dereferences them).
	Identity *sessionIdentityServices
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
	// AccountAPIKeyGuard is the process-local api-key failure guard
	// (gatewayaccounteffects); the runtime-reset bridge reaches the failure
	// guard / transient tombstone clears through it.
	AccountAPIKeyGuard *gatewayaccounteffects.AccountAPIKeyFailureGuard

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
	logger := chainRuntimeLogger{inner: slog.Default()}
	services := &chainRuntimeServices{}
	redisCache := cfg.CacheDriver == "redis"
	redisState := cfg.RuntimeStateDriver == "redis"

	// ---- redis clients ----
	var cacheClient *redis.Client
	var sharedFactory gatewayruntimecache.SharedCacheFactory
	if redisCache {
		factory, closeFactory, err := gatewayruntimecache.NewRedisSharedCacheFactory(cfg.RedisCacheURL, cfg.RedisNamespace)
		if err != nil {
			return nil, fmt.Errorf("create gateway redis shared cache factory: %w", err)
		}
		sharedFactory = factory
		services.closeFuncs = append(services.closeFuncs, closeFactory)
		options, parseErr := redis.ParseURL(cfg.RedisCacheURL)
		if parseErr != nil {
			return nil, fmt.Errorf("parse cache redis url: %w", parseErr)
		}
		cacheClient = redis.NewClient(options)
		services.closeFuncs = append(services.closeFuncs, func() { _ = cacheClient.Close() })
		// system-api rate limiter fixed-window store (task item 9).
		services.RateLimitStore = &ratelimit.RedisStore{Client: redisEvalClient{client: cacheClient}}
	}
	var stateClient *redis.Client
	if redisState {
		options, err := redis.ParseURL(cfg.RedisStateURL)
		if err != nil {
			return nil, fmt.Errorf("parse state redis url: %w", err)
		}
		stateClient = redis.NewClient(options)
		services.closeFuncs = append(services.closeFuncs, func() { _ = stateClient.Close() })
		// Exposed for siblings that share the runtime-state client (aipublic
		// penalty-window limiter). nil when runtimeStateDriver !== 'redis'.
		services.StateClient = stateClient
		// K5 invalidation shared-version persistence (inval.SharedStore, T2
		// audit wiring): the runtime-state redis, mirroring the Node topology
		// (publish + sync both gated on runtimeStateDriver === 'redis', which
		// is also the SyncInvalidationsOnRead gate of the runtime cache below).
		// Go-only int64 monotonic protocol — see internal/inval package docs.
		if composed.Bus != nil {
			composed.Bus.SetSharedStore(inval.NewRedisSharedStore(stateClient, cfg.RedisNamespace))
		}
	}

	// ---- runtime cache (G10) ----
	models, err := gatewayruntimecache.NewSQLReadModels(composed.db, composed.pgDialect, time.Now, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create gateway runtime sql read models: %w", err)
	}
	// One settings repository per process: the read models must see a
	// management settings PATCH immediately (Node clears the shared
	// systemSettingsCache on write) instead of a stale 60s snapshot.
	models.SetSettingsStore(composed.settingsStore)
	selector, selectorErr := newChainAccountsSelectorWithStats(composed.db, composed.statsDB, composed.pgDialect, cfg.Secret, time.Now)
	if selectorErr != nil {
		return nil, fmt.Errorf("create gateway accounts selector: %w", selectorErr)
	}
	services.Accounts = selector
	models.SetAccountsSelector(selector)
	catalogSource, catalogErr := newChainCatalogSource(composed.db, composed.pgDialect)
	if catalogErr != nil {
		return nil, fmt.Errorf("create gateway model catalog source: %w", catalogErr)
	}
	models.SetCatalogSource(catalogSource)
	// Live concurrency stays process-local (Node standalone semantics; the
	// redis-driver live counter lands with the concurrency flip slice).
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{
		Bus:                     composed.Bus,
		Logger:                  chainCacheEventLogger{inner: slog.Default()},
		UpdateAgeOnGet:          cfg.RuntimeMode == "standalone",
		SyncInvalidationsOnRead: redisState,
	})
	if err != nil {
		return nil, fmt.Errorf("create gateway runtime cache: %w", err)
	}
	services.Cache = cache
	services.closeFuncs = append(services.closeFuncs, cache.Close)

	// ---- G13 client-ip circuits / policy / avoidance ----
	circuits, err := gatewayclientip.NewErrorCircuit(gatewayclientip.ErrorCircuitOptions{
		RuntimeStateDriver: cfg.RuntimeStateDriver,
		StateRedisURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
	})
	if err != nil {
		return nil, fmt.Errorf("create gateway error circuit: %w", err)
	}
	services.Circuits = circuits
	services.closeFuncs = append(services.closeFuncs, circuits.Close)

	policySource, err := gatewayclientip.NewSQLPolicySource(composed.statsDB, composed.pgDialect, time.Now, ipstatsTimezone(settingValue))
	if err != nil {
		return nil, fmt.Errorf("create client-ip policy source: %w", err)
	}
	policyCache, err := gatewayclientip.NewPolicyCache(gatewayclientip.PolicyCacheOptions{
		CacheDriver: cfg.CacheDriver,
		RuntimeMode: cfg.RuntimeMode,
		Source:      policySource,
		Shared:      sharedFactory,
	})
	if err != nil {
		return nil, fmt.Errorf("create client-ip policy cache: %w", err)
	}
	services.IPPolicy = policyCache
	services.closeFuncs = append(services.closeFuncs, policyCache.Close)

	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{
		RuntimeStateDriver: cfg.RuntimeStateDriver,
		StateRedisURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
	})
	if err != nil {
		return nil, fmt.Errorf("create client-ip account avoidance: %w", err)
	}
	services.Avoidance = avoidance
	services.closeFuncs = append(services.closeFuncs, avoidance.Close)

	// ---- G13 proxy health (models rate limit + user request limits) ----
	// gatewayproxyhealth.Clock nil means the wall clock.
	penaltyLimiter := gatewayproxyhealth.NewPenaltyWindowRateLimiter(nil, redisState, stateClient, cfg.RedisNamespace)
	services.ModelsRateLimit = gatewayproxyhealth.NewAuthenticatedModelsRateLimitService(nil, penaltyLimiter, func(fields map[string]any, message string) {
		logger.Warn("gateway_models_rate_limit", fields, message)
	})
	counter := gatewayproxyhealth.NewUserRequestLimitCounter(nil, gatewayproxyhealth.UserRequestLimitCounterOptions{})
	coordinator := gatewayproxyhealth.NewUserRequestLimitCoordinator(counter, nil, gatewayproxyhealth.UserRequestLimitCoordinatorOptions{
		RedisEnabled:   redisState,
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
		PostgresDatabase:  composed.pgDialect,
		RedisCache:        redisCache,
		RedisRuntimeState: redisState,
		ServerRole:        false,
	}
	statsStore, err := gatewayquota.NewStatsStore(composed.statsDB, composed.pgDialect)
	if err != nil {
		return nil, fmt.Errorf("create gateway quota stats store: %w", err)
	}
	services.QuotaStats = statsStore
	var snapshotRuntimeState gatewayquota.RuntimeStateStore
	if redisCache && redisState {
		snapshotRuntimeState, err = gatewayquota.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, gatewayquota.GatewayQuotaSnapshotRuntimeStateStoreName)
		if err != nil {
			return nil, fmt.Errorf("create gateway quota snapshot runtime state: %w", err)
		}
	}
	snapshotCache, err := gatewayquota.NewSnapshotCache(quotaModes, snapshotRuntimeState, time.Now, nil)
	if err != nil {
		return nil, fmt.Errorf("create gateway quota snapshot cache: %w", err)
	}
	newQuotaShared := func(name string) (gatewayquota.SharedJSONCache, error) {
		if !redisCache {
			return nil, nil
		}
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
		return nil, fmt.Errorf("create authorization quota service: %w", err)
	}
	services.AuthzQuota = authzQuota
	// Estimator mounts the pricing catalog cost estimator (G20 phase-3): the
	// in-flight reservation then derives real USD estimates like the Node
	// estimateGatewayRequestCostUsd path.
	inflightQuota, err := gatewayquota.NewInflightQuotaService(gatewayquota.InflightQuotaConfig{
		APIKeys:   apiKeyQuota,
		Estimator: newChainCostEstimator(cache),
	})
	if err != nil {
		return nil, fmt.Errorf("create inflight quota service: %w", err)
	}
	services.InflightQuota = inflightQuota

	// ---- Gemini interaction affinity (runtimeStateDriver fork) ----
	if redisState && stateClient != nil {
		affinityState, affinityErr := gatewayquota.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, "gateway-gemini-interaction-affinity")
		if affinityErr != nil {
			return nil, fmt.Errorf("create gemini interaction affinity state: %w", affinityErr)
		}
		services.Affinity = gatewaygemini.NewInteractionAffinity(quotaRuntimeStateBridge{store: affinityState, storeName: "gateway-gemini-interaction-affinity"})
	} else {
		// Node runtimeStateDriver !== 'redis' fallback: the affinity service
		// keeps an in-process TTL cache (documented NewInteractionAffinity
		// nil-store behaviour).
		services.Affinity = gatewaygemini.NewInteractionAffinity(nil)
	}

	// ---- recoverable wait (G11 circuit wait engine) ----
	services.Recoverable = gatewaycircuit.NewPreAuthRecoverableWait(
		gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{}),
		chainCircuitWaitLogger{inner: slog.Default()},
	)

	// ---- normal-route latency degradation (runtimeStateDriver fork) ----
	// Node createRuntimeStateStore('gateway-normal-route-latency-degradation');
	// memory keeps the process-local store, redis shares it across instances.
	var latencyStore gatewayproxyhealth.RuntimeStateStore
	if redisState && stateClient != nil {
		store, storeErr := gatewayproxyhealth.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace, "gateway-normal-route-latency-degradation")
		if storeErr != nil {
			return nil, fmt.Errorf("create normal route latency degradation runtime state: %w", storeErr)
		}
		latencyStore = store
	} else {
		latencyStore = gatewayproxyhealth.NewMemoryRuntimeStateStore(nil)
	}
	services.LatencyDegradation = gatewayproxyhealth.NewLatencyDegradationService(latencyStore, nil, gatewayproxyhealth.LatencyDegradationOptions{})

	// ---- account api-key failure guard (runtimeStateDriver fork) ----
	// gatewayaccounteffects module state: the process-local suppression map in
	// the memory driver, the Redis transient tombstones through the lazy
	// store factory in the redis driver (Node
	// gatewayAccountApiKeyTransientStateStore()).
	guardConfig := gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: cfg.RuntimeStateDriver}
	var guardFactory gatewayaccounteffects.TransientStateStoreFactory
	if redisState {
		guardFactory = func() (gatewayaccounteffects.AccountApiKeyTransientStateStore, error) {
			return gatewayaccounteffects.NewRedisAccountApiKeyTransientStateStore(gatewayaccounteffects.RedisAccountApiKeyTransientStateStoreOptions{
				RedisURL:  cfg.RedisStateURL,
				Namespace: cfg.RedisNamespace,
			})
		}
	}
	services.AccountAPIKeyGuard = gatewayaccounteffects.NewAccountAPIKeyFailureGuard(guardConfig, gatewayaccounteffects.SystemClock{}, nil, guardFactory)

	// ---- hybrid Redis collaborators (cacheDriver==='redis') ----
	// Node createSharedJsonCache('gateway:hybrid-scoring-result') +
	// createRuntimeStateStore('gateway-hybrid-route-affinity').
	if redisCache && cacheClient != nil {
		scoringCache, scoringErr := gatewayhybrid.NewRedisSharedJSONCache(cacheClient, cfg.RedisNamespace)
		if scoringErr != nil {
			return nil, fmt.Errorf("create hybrid scoring shared cache: %w", scoringErr)
		}
		services.HybridScoringCache = scoringCache
	}
	if redisState && stateClient != nil {
		hybridState, hybridErr := gatewayhybrid.NewRedisRuntimeStateStore(stateClient, cfg.RedisNamespace)
		if hybridErr != nil {
			return nil, fmt.Errorf("create hybrid route affinity state: %w", hybridErr)
		}
		services.HybridRuntimeState = hybridState
	}

	// ---- G14 session identity + affinity services ----
	identityService, identityErr := gatewaysession.NewIdentityService(cfg.Secret)
	if identityErr != nil {
		return nil, fmt.Errorf("create gateway session identity service: %w", identityErr)
	}
	affinityConfig := gatewaysession.AffinityConfig{
		CacheDriver:        gatewaysession.CacheDriverMemory,
		RuntimeStateDriver: gatewaysession.RuntimeStateDriverMemory,
		Secret:             cfg.Secret,
	}
	if redisCache {
		affinityConfig.CacheDriver = gatewaysession.CacheDriverRedis
		affinityConfig.RedisCacheURL = cfg.RedisCacheURL
		affinityConfig.RedisNamespace = cfg.RedisNamespace
	}
	if redisState {
		affinityConfig.RuntimeStateDriver = gatewaysession.RuntimeStateDriverRedis
	}
	affinityService, affinityErr := gatewaysession.NewAffinityService(affinityConfig)
	if affinityErr != nil {
		return nil, fmt.Errorf("create gateway session affinity service: %w", affinityErr)
	}
	services.Identity = &sessionIdentityServices{
		Identity: identityService,
		Affinity: affinityService,
		Secret:   cfg.Secret,
	}
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
