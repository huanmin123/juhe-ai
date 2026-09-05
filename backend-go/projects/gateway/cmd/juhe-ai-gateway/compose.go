package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/announcements"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	businesssettings "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/delegated"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ipstats"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/legacybridge"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/logreads"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oauthmgmt"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oidc"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/policyreads"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ratelimit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/systemteams"
)

// Mount matrix (Node system-api-app.ts / db-service.ts app.use prefix -> Go
// composition). Every entry is either mounted below or explicitly registered
// as still Node-owned (bridged).
//
//	/__aisys__/api middleware chain (request context, security headers,
//	  compression, no-store, body limit, IP rate limit, user rate limit,
//	  404/405 JSON)                                -> kernel + ratelimit + authsys
//	/__aisys__/api/auth/*                          -> authsys.MountAuth
//	/__aisys__/api/system-accounts                 -> authsys.MountSystemAccounts
//	/__aisys__/api/announcements + /my-announcements -> announcements.Mount
//	/__aisys__/api/my-teams + /system-teams        -> systemteams.Deps.Mount
//	/__aisys__/api/authorizations + /my-authorizations -> authz
//	/__aisys__/api/settings (+ /settings/public)   -> settings.Deps.Mount
//	/__aisys__/api/groups + /my-groups             -> groups.Deps.Mount
//	/__aisys__/api/route-strategies + /my-*        -> routestrategies.Deps.Mount
//	/__aisys__/api/api-keys + /my-api-keys         -> apikeys.Deps.Mount
//	/__aisys__/api/accounts + /my-accounts         -> accounts.Deps.Mount
//	/__aisys__/api/providers + /my-providers       -> providers.Deps.Mount
//	/__aisys__/api/{provider}-oauth + /my-*        -> oauthmgmt.Deps.Mount
//	/__aisys__/api/response-inspection-policies    -> policyreads.InspectionDeps
//	/__aisys__/api/operation-logs + /my-operation-logs -> logreads.Deps (F4)
//	/__aisys__/api/ip-stats                        -> ipstats.Deps.Mount
//	/__aisys__/api/external-integration-sources    -> policyreads.ExternalDeps
//	/__aisys__/api/oauth (admin management)        -> policyreads.OAuthDeps
//	/__aisys__/api/health                          -> kernel health (rate-limit bypass)
//	/.well-known + /oauth (public protocol)        -> oidc.Deps.Mount
//	/__aidelegated__/v1                            -> delegated.Deps.Mount
//
// Registered NOT mounted in this phase (slice not composition-ready; the
// legacy bridge keeps serving them from the Node origin):
//   - /__aisys__/api/authorization-options + /my-authorization-options: the
//     authorization-options family (including the grantee-* reference
//     queries) stays Node-owned (M02 顺延 -> W3, final-migration PLAN.md);
//     authz mounts only the /authorizations + /my-authorizations families.
//   - /__aisys__/api/audit-logs, /runtime-logs, /public-api-logs: the
//     logreads.ReadsDeps needs the F1 runtime-log dataset reader (jobs module
//     owner; cross-module import is forbidden by the three-project baseline).
//   - /__aisys__/api/my-chat: the chat route package is mounted only together
//     with its generation-wave ports (Executor/ModelCatalog/ChatKeys/
//     GatewayKeys/ObjectStore/ImageProcessor/ImageObservations/Compactions/
//     TokenCount) which dispatch into the internal gateway chain.
//   - /__aipublic__ external-integrations legacy family: no Go package yet.
//   - /v1 + gateway protocol paths + openai-compatible files/vector-stores:
//     gated behind JUHE_AI_GATEWAY_CHAIN_ENABLED, see gatewaychain.go.
const (
	systemAPIPrefix = "/__aisys__/api"
	publicAPIPrefix = "/__aipublic__"
	businessSchema  = "juhe_business"
)

type composition struct {
	Kernel http.Handler
	Bus    *inval.Bus
	Bridge *legacybridge.Bridge
	// chain is the assembled /v1 gateway chain (nil when
	// JUHE_AI_GATEWAY_CHAIN_ENABLED is off; /v1 traffic then stays on the
	// legacy bridge).
	chain *gatewayChain
	// DB is the business database handle (SQLite file handle or the shared
	// PostgreSQL pool connection). Exposed for seed/maintenance helpers.
	DB *sql.DB

	db        *sql.DB
	ownDB     bool
	pgDialect bool
	// statsDB backs the ipstats reads (client_ip_* tables live in the stats
	// database, never the business file). SQLite mode owns a dedicated stats
	// file handle closed in Shutdown; PostgreSQL mode aliases the shared pool
	// handle (juhe_stats.* qualification) and closes nothing here.
	statsDB    *sql.DB
	ownStatsDB bool

	producer           *operationlog.Producer
	operationStore     operationlog.Store
	operationLease     operationlog.OwnerLease
	operationLeaseHeld bool

	shutdowns []func()
}

// Shutdown mirrors the Node db-service shutdown order (db-service.ts
// shutdownDbService): background workers drain first (chat generation hub,
// usage/audit dispatchers land with the chain slice), then the F4 producer,
// then the owned SQL handles (stats file, business handle). The HTTP servers
// and the shared pools are closed by main around this.
func (c *composition) Shutdown() {
	for i := len(c.shutdowns) - 1; i >= 0; i-- {
		c.shutdowns[i]()
	}
	if c.ownStatsDB && c.statsDB != nil {
		_ = c.statsDB.Close()
	}
	if c.ownDB && c.db != nil {
		_ = c.db.Close()
	}
}

func newCompositionID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf[:])
}

// SettingValueFunc mirrors delegated.SettingReader; the delegated route family
// and the timezone sources read global settings through it.
type SettingValueFunc func(key string) (string, error)

func settingsValueReader(store *settings.Store) SettingValueFunc {
	return func(key string) (string, error) {
		snapshot, err := store.Load(context.Background())
		if err != nil {
			return "", err
		}
		return settingsString(snapshot[key]), nil
	}
}

func settingsString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// composeSystemAPI assembles the Go system-api composition root: business
// stores over the dual-mode business database, the kernel in the Node
// system-api-app.ts middleware order, every mount-ready management and public
// route family, the legacy bridge fallback and the /health endpoint.
//
// Callers must prove the business owner gates first (businessOwnerGate plus
// cutover evidence verification); this function fails fast on any incomplete
// wiring instead of serving a partial surface.
func composeSystemAPI(cfg runtimeConfig, postgresPools *pgpool.Registry, operationStore operationlog.Store) (*composition, error) {
	if operationStore == nil {
		return nil, errors.New("系统 API 组合根要求 F4 操作日志 store 已启用（JUHE_AI_OPERATION_LOG_* 配置）")
	}

	composed := &composition{pgDialect: cfg.DatabaseDriver == "postgres"}

	// Business database (dual mode): Postgres shares the managed pool
	// registry; SQLite opens a single writer handle over the handoff file.
	if composed.pgDialect {
		handle, err := postgresPools.Acquire(cfg.BusinessPostgresURL, "gateway-system-api", 0, 0)
		if err != nil {
			return nil, fmt.Errorf("open business PostgreSQL pool: %w", err)
		}
		composed.db = handle.DB()
	} else {
		db, err := sql.Open("sqlite", sqliteFileDSN(cfg.BusinessDatabasePath))
		if err != nil {
			return nil, fmt.Errorf("open business sqlite database: %w", err)
		}
		db.SetMaxOpenConns(1)
		if err := configureSQLiteConnection(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure business sqlite database: %w", err)
		}
		composed.db = db
		composed.ownDB = true
	}

	// ipstats data source (Node getStatsDatabase() split): the client_ip_*
	// tables live only in the stats database — PostgreSQL reaches juhe_stats
	// through schema qualification on the shared business pool, SQLite needs
	// its own handle over the dedicated stats file. The stats path is
	// required in SQLite mode like every other Go stats consumer (auditlog F3
	// isolation validation, jobs tablemonitor/statsverify); there is no
	// CWD-relative default.
	//
	// X05 six-database startup preflight (BUG-0167/0168): SQLite mode runs the
	// Node db-service open path (database.ts getBusinessDatabase ->
	// applyBusinessSchema + seedDefaults, plus the lazy per-file schema
	// application for stats/chat/codex-context/dataset/usage-catalog) through
	// the maintenance bootstrap surface. PostgreSQL mode mirrors the Node PG
	// contract (runtime refuses the driver; the external
	// juhe-ai-maintenance --ensure-schema --seed --driver postgres command
	// owns the schema/seed), so no startup ensure runs here.
	if !composed.pgDialect {
		if err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, composed.db); err != nil {
			_ = composed.db.Close()
			return nil, fmt.Errorf("sqlite storage preflight: %w", err)
		}
	}
	if composed.pgDialect {
		composed.statsDB = composed.db
	} else {
		if cfg.StatsDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_STATS_DATABASE_PATH，无法打开 ip-stats stats 数据库")
		}
		statsDB, err := sql.Open("sqlite", sqliteFileDSN(cfg.StatsDatabasePath))
		if err != nil {
			return nil, fmt.Errorf("open stats sqlite database: %w", err)
		}
		statsDB.SetMaxOpenConns(1)
		if err := configureSQLiteConnection(statsDB); err != nil {
			_ = statsDB.Close()
			return nil, fmt.Errorf("configure stats sqlite database: %w", err)
		}
		composed.statsDB = statsDB
		composed.ownStatsDB = true
	}

	ownerGate := businesssettings.OwnerGate{
		Confirmed:         cfg.BusinessHandoffConfirmed,
		SchemaReady:       cfg.BusinessSchemaReady,
		NodeWriterStopped: cfg.BusinessNodeWriterStopped,
	}

	// K5 invalidation bus: every cache-backed store subscribes through it.
	bus := inval.New(time.Now)
	composed.Bus = bus

	businessSettings, err := businesssettings.New(composed.db, businessDialect(composed.pgDialect), businessSchema, ownerGate)
	if err != nil {
		return nil, fmt.Errorf("create business settings store: %w", err)
	}
	settingsStore, err := settings.NewStore(composed.db, composed.pgDialect, time.Now, bus)
	if err != nil {
		return nil, fmt.Errorf("create settings store: %w", err)
	}
	settingValue := settingsValueReader(settingsStore)

	authzStore, err := authz.NewStore(composed.db, composed.pgDialect, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create authorization store: %w", err)
	}
	teamStore, err := systemteams.NewStore(composed.db, composed.pgDialect, time.Now, authzStore)
	if err != nil {
		return nil, fmt.Errorf("create system-teams store: %w", err)
	}
	groupsStore, err := groups.NewStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus)
	if err != nil {
		return nil, fmt.Errorf("create groups store: %w", err)
	}
	routeStrategyStore, err := routestrategies.NewStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus)
	if err != nil {
		return nil, fmt.Errorf("create route-strategy store: %w", err)
	}
	apiKeyStore, err := apikeys.NewStore(composed.db, composed.pgDialect, cfg.Secret, time.Now, newCompositionID, apikeys.BusInvalidator{Bus: bus})
	if err != nil {
		return nil, fmt.Errorf("create api-key store: %w", err)
	}
	accountStore, err := accounts.NewStore(composed.db, composed.pgDialect, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		return nil, fmt.Errorf("create account store: %w", err)
	}
	announcementStore, err := announcements.NewStore(composed.db, composed.pgDialect, time.Now, newCompositionID)
	if err != nil {
		return nil, fmt.Errorf("create announcement store: %w", err)
	}
	oidcStore, err := oidc.NewStore(composed.db, composed.pgDialect, time.Now, cfg.OIDCKeyEncryptionSecret)
	if err != nil {
		return nil, fmt.Errorf("create oidc store: %w", err)
	}
	providerStore, err := providers.NewStore(composed.db, composed.pgDialect, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create provider store: %w", err)
	}
	ipStatsStore, err := ipstats.NewStore(composed.statsDB, composed.pgDialect, time.Now, newCompositionID, bus, settingsTimezone(settingValue))
	if err != nil {
		return nil, fmt.Errorf("create ip-stats store: %w", err)
	}
	// M15 detail hydration (client-ip-stats-detail.repository.ts hydrates in
	// both modes): account/owner display names are business-database reads —
	// the SQLite handle queries the unqualified tables, PostgreSQL qualifies
	// juhe_business.* on the same pool.
	ipStatsStore.SetDetailAccountLookup(ipstats.NewBusinessAccountLookup(composed.db, composed.pgDialect))
	inspectionStore, err := policyreads.NewInspectionStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus)
	if err != nil {
		return nil, fmt.Errorf("create inspection store: %w", err)
	}
	externalStore, err := policyreads.NewExternalStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus, cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("create external-integration store: %w", err)
	}
	oauthPolicyStore, err := policyreads.NewOAuthStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus, cfg.OIDCKeyEncryptionSecret)
	if err != nil {
		return nil, fmt.Errorf("create oauth policy store: %w", err)
	}
	oauthStore, err := oauthmgmt.NewStore(composed.db, composed.pgDialect, cfg.Secret, accountStore, oauthmgmt.NewHTTPTokenExchanger(), time.Now, newCompositionID)
	if err != nil {
		return nil, fmt.Errorf("create oauth management store: %w", err)
	}

	// F4 producer sink: every management mutation lands in the operation log
	// through the in-process producer holding its own owner lease.
	lease, ok, err := operationStore.AcquireOwnerLease(context.Background(), "gateway-system-api-operation-log", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("acquire operation-log producer lease: %w", err)
	}
	if !ok {
		return nil, errors.New("operation-log producer lease is held elsewhere; system api composition refuses to start")
	}
	producer := operationlog.NewProducer(operationStore, lease, operationlog.Config{}, producerLogger{})
	composed.producer = producer
	composed.operationStore = operationStore
	composed.operationLease = lease
	composed.operationLeaseHeld = true
	sink := &authsys.OperationLogProducerSink{Producer: producer, MaxChanges: 100}

	// Auth captcha / login-guard drivers switch on the runtime-state driver
	// (BUG-0171.4): memory keeps the process-local modelcheckauth services,
	// redis shares challenges / issue windows / failure locks across
	// instances through the Node-compatible auth_captcha / auth_login_guard
	// state stores.
	var captchaService authsys.CaptchaIssuer
	var captchaClose func()
	if !cfg.CaptchaDisabled {
		if cfg.RuntimeStateDriver == "redis" {
			// Redis runtime-state driver: challenges and the per-IP issue
			// window live in the shared auth_captcha state store (Node
			// captcha.service.ts async paths) so captcha consumption is atomic
			// across instances.
			svc, closeFn, err := authsys.NewRedisCaptchaService(cfg.RedisStateURL, cfg.RedisNamespace, time.Now)
			if err != nil {
				return nil, fmt.Errorf("create shared captcha service: %w", err)
			}
			captchaService, captchaClose = svc, closeFn
		} else {
			captchaService = modelcheckauth.NewCaptchaService(time.Now)
		}
	}
	if captchaClose != nil {
		composed.shutdowns = append(composed.shutdowns, captchaClose)
	}
	var loginGuard authsys.LoginGuardDriver
	var loginGuardClose func()
	if cfg.RuntimeStateDriver == "redis" {
		// Redis runtime-state driver: failure counters and locks live in the
		// shared auth_login_guard state store (Node login-guard.service.ts
		// async paths) so throttling is shared across instances.
		guard, closeFn, err := authsys.NewRedisLoginGuard(cfg.RedisStateURL, cfg.RedisNamespace, time.Now)
		if err != nil {
			return nil, fmt.Errorf("create shared login guard: %w", err)
		}
		loginGuard, loginGuardClose = guard, closeFn
	} else {
		loginGuard = modelcheckauth.NewLoginGuard(time.Now)
	}
	if loginGuardClose != nil {
		composed.shutdowns = append(composed.shutdowns, loginGuardClose)
	}
	authMode := modelcheckauth.SQLite
	if composed.pgDialect {
		authMode = modelcheckauth.Postgres
	}
	businessAuth, err := businessauth.New(composed.db, authMode, time.Now, businessauth.OwnerGate(ownerGate))
	if err != nil {
		return nil, fmt.Errorf("create business auth service: %w", err)
	}
	// The system-account store is fail-closed behind the same business owner
	// gate: without a proven handoff it never writes the business database
	// (BUG-0169.3).
	systemAccountStore, err := authsys.NewAccountStore(composed.db, authMode, time.Now, authsys.OwnerGate{
		Confirmed:         cfg.BusinessHandoffConfirmed,
		SchemaReady:       cfg.BusinessSchemaReady,
		NodeWriterStopped: cfg.BusinessNodeWriterStopped,
	})
	if err != nil {
		return nil, fmt.Errorf("create system-account store: %w", err)
	}
	// Post-commit double-channel cache invalidation (BUG-0170.5).
	systemAccountStore.SetCacheInvalidator(authsysBusInvalidator{bus: bus})
	// In-transaction default resource bootstrap with Node-compatible API-key
	// secret sealing (BUG-0170.1).
	systemAccountStore.SetSecretSealer(apikeySecretSealer{secret: cfg.Secret})
	systemAccountStore.SetDefaultResourceEnsurer(authsys.NewSQLDefaultResources(systemAccountStore, apikeySecretSealer{secret: cfg.Secret}))
	authDeps := authsys.NewDeps(businessAuth, systemAccountStore, captchaService, loginGuard, time.Now, businessSettings)
	authDeps.Sink = sink
	authDeps.TemporaryAccessIPAllowlist = cfg.TemporaryAccessIPAllowlist
	authDeps.CaptchaDisabled = cfg.CaptchaDisabled
	authDeps.DevAutoLoginUsername = cfg.DevAutoLoginUsername

	// Kernel order: request context -> management security headers ->
	// compression -> no-store -> body limit -> IP rate limit -> routes
	// (require session -> authenticated rate limit inside the auth wrapper)
	// -> 404/405 JSON.
	limiter := &ratelimit.Limiter{
		Settings: ratelimitSettingsProvider(settingsStore),
		Store:    ratelimit.NewMemoryStore(time.Now),
	}
	authDeps.AuthenticatedRateLimit = func(w http.ResponseWriter, r *http.Request, systemAccountID string) bool {
		return limiter.AuthenticatedRateLimit(w, r, systemAccountID)
	}

	kern := kernel.New(kernel.Options{
		SystemAPIPrefix: systemAPIPrefix,
		PublicAPIPrefix: publicAPIPrefix,
		TrustProxyCount: trustProxyCount(cfg.TrustProxy),
		IPRateLimit:     limiter.IPRateLimitMiddleware,
	})

	// Management route families.
	authDeps.MountAuth(kern, cfg.CookieSameSite, cfg.CookieSecure)
	authDeps.MountSystemAccounts(kern, cfg.CookieSameSite, cfg.CookieSecure)
	announcements.Mount(kern, authDeps, announcementStore, sink)
	(&systemteams.Deps{Store: teamStore, Sink: sink, Auth: authDeps}).Mount(kern)
	(&authz.Deps{Store: authzStore, Sink: sink, Auth: authDeps}).Mount(kern)
	(&settings.Deps{Store: settingsStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&groups.Deps{Store: groupsStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&routestrategies.Deps{Store: routeStrategyStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&apikeys.Deps{Store: apiKeyStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&accounts.Deps{Store: accountStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&providers.Deps{Store: providerStore, Auth: authDeps}).Mount(kern)
	(&oauthmgmt.Deps{Store: oauthStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.InspectionDeps{Store: inspectionStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.ExternalDeps{Store: externalStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.OAuthDeps{Store: oauthPolicyStore, Auth: authDeps, OIDCEnabled: cfg.OIDCEnabled, OIDCIssuer: cfg.OIDCIssuer}).Mount(kern)
	(&logreads.Deps{Reader: operationStore, Auth: authDeps}).Mount(kern)
	(&ipstats.Deps{Store: ipStatsStore, Auth: authDeps, Sink: sink}).Mount(kern)

	// Public protocol surface (root-level paths, mirroring oauthPublicRouter)
	// and the delegated API share the protocol rate limiter instance.
	protocolLimiter := oidc.NewProtocolRateLimiter(time.Now)
	(&oidc.Deps{
		Store:       oidcStore,
		Limiter:     protocolLimiter,
		OIDCEnabled: cfg.OIDCEnabled,
		OIDCIssuer:  cfg.OIDCIssuer,
		Now:         time.Now,
	}).Mount(kern)
	(&delegated.Deps{
		Tokens:         oidcStore,
		Limiter:        protocolLimiter,
		Groups:         groupsStore,
		Strategies:     routeStrategyStore,
		ApiKeys:        apiKeyStore,
		AiAccounts:     accountStore,
		DB:             composed.db,
		PGDialect:      composed.pgDialect,
		Settings:       delegatedSettingsAdapter{read: settingValue},
		Usage:          unavailableUsageReader{},
		RedisNamespace: cfg.RedisNamespace,
		Now:            time.Now,
	}).Mount(kern)

	// G20 phase-2 AI gateway /v1 chain: assembles the concrete runtime
	// services (chain_runtime.go) plus the composition adapters
	// (chain_*.go), then mounts the /v1 orchestrator ahead of the legacy
	// bridge. Every frozen port fails fast by name; nothing is wired nil.
	// The chat generation-wave port adapters (chain_chat.go) stay unmounted
	// with the my-chat family until its remaining slices land.
	if cfg.ChainEnabled {
		spoolDirectory := cfg.UsageSpoolDirectory
		if spoolDirectory == "" && cfg.StatsDatabasePath != "" {
			spoolDirectory = filepath.Join(filepath.Dir(cfg.StatsDatabasePath), "usage-record-spool")
		}
		chainServices, chainErr := composeChainRuntimeServices(composed, cfg, settingValue)
		if chainErr != nil {
			return nil, fmt.Errorf("compose gateway chain runtime services: %w", chainErr)
		}
		// Shutdown order is LIFO: services registered first close last, after
		// the chain drained its usage buffer.
		composed.shutdowns = append(composed.shutdowns, chainServices.Close)
		chain, chainShutdown, chainAssembleErr := composeGatewayChain(chainRuntimeDeps{
			Cache:           chainServices.Cache,
			Clock:           gatewaypreauth.SystemClock{},
			AuditLogEnabled: func() bool { return cfg.AuditLogEnabled },
			AuditInputURL:   cfg.AuditInputURL,
			SpoolDirectory:  spoolDirectory,
			Circuits:        chainServices.Circuits,
			IPPolicy:        chainServices.IPPolicy,
			UserLimits:      chainServices.UserLimits,
			ModelsRateLimit: chainServices.ModelsRateLimit,
			APIKeyQuota:     chainServices.APIKeyQuota,
			AuthzQuota:      chainServices.AuthzQuota,
			InflightQuota:   chainServices.InflightQuota,
			Avoidance:       chainServices.Avoidance,
			Affinity:        chainServices.Affinity,
			Recoverable:     chainServices.Recoverable,
		})
		if chainAssembleErr != nil {
			chainServices.Close()
			return nil, fmt.Errorf("compose gateway chain: %w", chainAssembleErr)
		}
		composed.shutdowns = append(composed.shutdowns, chainShutdown)
		composed.chain = chain
		// The /v1 subtree serves the gateway protocol paths; non-protocol
		// paths answer the Node 404 JSON inside the chain (the
		// openai-compatible files / vector-stores families stay on the
		// legacy bridge until their flip, see gatewaychain.go).
		kern.Register("/v1", chain)
		kern.Register("/v1/", chain)
		// cacheDriver==='redis': the system-api limiter switches onto the
		// shared fixed-window redis store (task item 9); memory driver keeps
		// the in-process store built above.
		if chainServices.RateLimitStore != nil {
			limiter.Store = chainServices.RateLimitStore
		}
		slog.Info("gateway chain composed", "trafficSource", "gateway", "spoolDirectory", spoolDirectory != "", "auditDispatch", cfg.AuditInputURL != "")
	}

	// /__aisys__/api/health: readiness contract; the rate limiter bypasses it
	// (isSystemApiHealthPath mirror) and the kernel registers it ahead of the
	// auth chain like Node line 134.
	kern.Register("GET "+systemAPIPrefix+"/health", kernel.HealthHandler(func() (int, any) {
		return http.StatusOK, map[string]any{
			"statusCode":     200,
			"status":         "ok",
			"service":        "juhe-ai-db-service",
			"accountBalance": map[string]any{"enabled": false, "ready": true},
			"proxyLatency":   map[string]any{"enabled": false, "ready": true},
			"checkedAt":      time.Now().UTC().Format(time.RFC3339Nano),
		}
	}))

	// Legacy bridge: every still-Node-owned prefix (including the frontend and
	// the unflipped read families listed in the mount matrix) falls through to
	// the Node origin. Registered kernel routes always win over the bridge.
	if cfg.LegacyBridgeTarget != "" {
		bridge, err := legacybridge.New(cfg.LegacyBridgeTarget)
		if err != nil {
			return nil, fmt.Errorf("create legacy bridge: %w", err)
		}
		bridge.RegisterPrefix("/")
		composed.Bridge = bridge
		kern.RegisterFallback(bridge)
	}

	// Composition shutdown order (Node shutdownDbService mirror): release the
	// F4 producer lease so a successor process can take over immediately. The
	// HTTP servers and shared pools are closed by main around this call.
	composed.shutdowns = append(composed.shutdowns, func() {
		if !composed.operationLeaseHeld {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = composed.operationStore.ReleaseOwnerLease(releaseCtx, composed.operationLease)
		composed.operationLeaseHeld = false
	})

	composed.Kernel = kern.Handler()
	composed.DB = composed.db
	return composed, nil
}

func settingsTimezone(read SettingValueFunc) ipstats.TimezoneSource {
	return func(context.Context) (string, error) {
		return read("usageStatsTimezone")
	}
}

type delegatedSettingsAdapter struct{ read SettingValueFunc }

func (a delegatedSettingsAdapter) SettingValue(key string) (string, error) { return a.read(key) }

// unavailableUsageReader mirrors the Node request-limits snapshot degradation:
// any runtime-state read failure renders usageStatus "unavailable" instead of
// failing the endpoint. The Go runtime-state consumer lands with the chain
// slice; until then the snapshot reports the documented degraded contract.
type unavailableUsageReader struct{}

func (unavailableUsageReader) RequestLimitTotal(context.Context, string) (string, error) {
	return "", errors.New("gateway runtime state reader lands with the chain slice")
}

type producerLogger struct{}

func (producerLogger) Warn(msg string, args ...any)  { slog.Warn(msg, args...) }
func (producerLogger) Error(msg string, args ...any) { slog.Error(msg, args...) }

// authsysBusInvalidator adapts the K5 invalidation bus to the authsys
// post-commit account invalidation channels (BUG-0170.5): runtime cache and
// API-key validation cache subscribers are notified with the Node reason
// strings; the bus publish itself cannot fail, so the validation channel
// only reports failure when a wired shared store errors upstream.
type authsysBusInvalidator struct{ bus *inval.Bus }

func (a authsysBusInvalidator) InvalidateRuntime(reason string) {
	if a.bus == nil {
		return
	}
	a.bus.Invalidate(inval.TopicGatewayRuntime, reason)
}

func (a authsysBusInvalidator) InvalidateAPIKeyValidation(reason string) error {
	if a.bus == nil {
		return errors.New("cache invalidation bus is not wired")
	}
	a.bus.Invalidate(inval.TopicGatewayAPIKeyValidation, reason)
	return nil
}

// apikeySecretSealer seals generated default API-key plaintexts with the
// Node-compatible AES-GCM envelope (apikeys.EncryptJSON mirrors
// storage/crypto.ts encryptJson({key})). The plaintext never leaves the
// sealed envelope.
type apikeySecretSealer struct{ secret string }

func (s apikeySecretSealer) SealSecret(_ context.Context, plaintext string) (string, error) {
	return apikeys.EncryptJSON(s.secret, map[string]string{"key": plaintext})
}

// ratelimitSettingsProvider mirrors currentSystemApiRateLimitSettings
// (system-api-rate-limit.middleware.ts): integer settings with the 0..1000000
// bound, surfaced as 500 through the limiter on failure.
func ratelimitSettingsProvider(store *settings.Store) ratelimit.SettingsProvider {
	return func(ctx context.Context) (ratelimit.Settings, error) {
		snapshot, err := store.Load(ctx)
		if err != nil {
			return ratelimit.Settings{}, err
		}
		load := func(key string) (int, error) {
			raw, ok := snapshot[key]
			if !ok || raw == nil {
				return 0, fmt.Errorf("%s 必须是整数", key)
			}
			number, ok := raw.(float64)
			if !ok {
				parsed, parseErr := strconv.Atoi(settingsString(raw))
				if parseErr != nil {
					return 0, fmt.Errorf("%s 必须是整数", key)
				}
				number = float64(parsed)
			}
			if number != float64(int(number)) || number < 0 || number > 1_000_000 {
				return 0, fmt.Errorf("%s 必须在 0 到 1000000 之间", key)
			}
			return int(number), nil
		}
		ipRead, err := load("systemApiRateLimitIpReadPerMinute")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		ipReadBurst, err := load("systemApiRateLimitIpReadBurstPer10Seconds")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		ipWrite, err := load("systemApiRateLimitIpWritePerMinute")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		ipWriteBurst, err := load("systemApiRateLimitIpWriteBurstPer10Seconds")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		userRead, err := load("systemApiRateLimitUserReadPerMinute")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		userWrite, err := load("systemApiRateLimitUserWritePerMinute")
		if err != nil {
			return ratelimit.Settings{}, err
		}
		return ratelimit.Settings{
			IPReadPerMinute:    ipRead,
			IPReadBurstPer10s:  ipReadBurst,
			IPWritePerMinute:   ipWrite,
			IPWriteBurstPer10s: ipWriteBurst,
			UserReadPerMinute:  userRead,
			UserWritePerMinute: userWrite,
		}, nil
	}
}

func trustProxyCount(raw string) int {
	value := trimSpace(raw)
	if value == "" || value == "false" {
		return 0
	}
	if value == "true" {
		return 1
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

// businessDialect converts the storage dialect for the business-owner stores.
func businessDialect(pg bool) businesssettings.Mode {
	if pg {
		return businesssettings.Postgres
	}
	return businesssettings.SQLite
}

// sqliteFileDSN mirrors operationlog.sqliteDSN: absolute file URL with the
// busy timeout pragma so concurrent readers never fail on lock contention.
func sqliteFileDSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)"
}

// configureSQLiteConnection applies the Node-compatible SQLite pragmas
// (foreign keys, busy timeout, WAL) once per handle.
func configureSQLiteConnection(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, query := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}
