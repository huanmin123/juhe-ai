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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/aipublic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/announcements"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	businesssettings "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/delegated"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/helpweb"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ipstats"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/logreads"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oauthmgmt"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oidc"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/policyreads"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/proxyprofiles"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/publicapilogs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ratelimit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/statreads"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/systemteams"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/uibootstrap"
)

// Mount matrix (Node system-api-app.ts / db-service.ts app.use prefix -> Go
// composition). X01 go-only terminal state: every family below is mounted in
// Go, the Node origin is archived, and the internal/legacybridge proxy package
// is deleted. Unmounted prefixes answer the kernel 404/405 JSON contract.
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
//	/__aisys__/api/{audit-logs,runtime-logs,public-api-logs} -> logreads.ReadsDeps
//	  (X04: F3/F1/F5 dataset readers over the audit, runtime-log and dataset
//	  databases; the runtime-logs grep family scans JUHE_AI_LOG_DIR files.
//	  Node mounts no my-* variants for these three families)
//	/__aisys__/api/ip-stats                        -> ipstats.Deps.Mount
//	/__aisys__/api/external-integration-sources    -> policyreads.ExternalDeps
//	/__aisys__/api/oauth (admin management)        -> policyreads.OAuthDeps
//	/__aisys__/api/health                          -> kernel health (rate-limit bypass)
//	/.well-known + /oauth (public protocol)        -> oidc.Deps.Mount
//	/__aidelegated__/v1                            -> delegated.Deps.Mount
//	/__aisys__/api/stats + /my-stats               -> statreads.Deps.Mount (X04)
//	/__aisys__/api/usage-records + /my-*           -> statreads.Deps.Mount (X04)
//	/__aisys__/api/authorization-options + /my-*   -> authz.Deps.MountAuthorizationOptions (X04)
//	/__aisys__/api/proxies                         -> proxyprofiles.Mount (X04)
//	/__aisys__/api/table-monitor (3 GET + cleanup POST) -> tablemonitor.Deps.Mount
//	  (the cleanup POST enqueues the Node-shaped job through the durable
//	  record_maintenance_jobs channel; the jobs wave drains it)
//	/__aisys__/api/ui-bootstrap + /my-*            -> uibootstrap.Deps.Mount (X04)
//	/__aisys__/help (static help center)           -> helpweb.Deps.Mount (X04; disabled
//	  unless JUHE_AI_FRONTEND_DIST_PATH points at the frontend dist)
//
// Slice provenance notes (X01 go-only terminal state: every prefix below is
// mounted in Go; the archived Node origin serves nothing):
//   - /__aisys__/api/audit-logs, /runtime-logs, /public-api-logs: mounted
//     through logreads.ReadsDeps (X04 404 项补齐); the F1 runtime-log dataset
//     stays jobs-owned (the gateway only opens a read-only handle, the F1
//     indexer keeps the schema).
//   - /__aisys__/api/my-chat: mounted with the chain (G20 phase-3,
//     composeChatFamily): the chat database owner + the generation-wave ports
//     (Executor/ModelCatalog/ChatKeys/GatewayKeys/ObjectStore/ImageProcessor/
//     ImageObservations/Compactions/TokenCount) dispatch into the internal
//     gateway chain.
//   - /__aipublic__ external-integrations legacy family -> aipublic.Deps.Mount
//     (X04: bearer-token auth + per-source penalty-window rate limiting +
//     scope checks; the admin management family stays in policyreads).
//   - /v1 + gateway protocol paths + openai-compatible files/vector-stores:
//     gated behind JUHE_AI_GATEWAY_CHAIN_ENABLED, see gatewaychain.go (the
//     compat families mount into the chain's non-protocol paths).
const (
	systemAPIPrefix = "/__aisys__/api"
	publicAPIPrefix = "/__aipublic__"
	businessSchema  = "juhe_business"
)

type composition struct {
	Kernel http.Handler
	Bus    *inval.Bus
	// chain is the assembled /v1 gateway chain (nil when
	// JUHE_AI_GATEWAY_CHAIN_ENABLED is off; /v1 traffic then answers the
	// kernel 404 JSON contract).
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
	// chatDB backs the my-chat family (chat_* tables in the dedicated chat
	// database / juhe_chat schema). Same dual-mode ownership as statsDB.
	chatDB    *sql.DB
	ownChatDB bool

	// kernel + authDeps are retained so later families (my-chat) can mount
	// with the session middleware.
	kernel   *kernel.Kernel
	authDeps *authsys.Deps
	// settingsStore is the single process settings repository: the management
	// route family writes through it and the chain runtime read models read
	// through the same instance so a PATCH is immediately visible (Node
	// clears the one systemSettingsCache on write).
	settingsStore *settings.Store
	// chainServices retains the concrete chain runtime services (the chat
	// family reads the runtime cache through them).
	chainServices *chainRuntimeServices

	producer       *operationlog.Producer
	operationStore operationlog.Store
	// AuthzStore retains the authorization store so main can attach the
	// T6d gateway-side expiry reconciliation component (compose_authz_expiry_sync.go).
	AuthzStore *authz.Store

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
	if c.ownChatDB && c.chatDB != nil {
		_ = c.chatDB.Close()
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
// route family and the /health endpoint.
//
// Callers must prove the business owner gates first (businessOwnerGate plus
// cutover evidence verification); this function fails fast on any incomplete
// wiring instead of serving a partial surface.
func composeSystemAPI(cfg runtimeConfig, postgresPools *pgpool.Registry, operationStore operationlog.Store, operationLease *operationlog.LeaseKeeper, auditConfig auditlog.Config) (*composition, error) {
	if operationStore == nil {
		return nil, errors.New("系统 API 组合根要求 F4 操作日志 store 已启用（JUHE_AI_OPERATION_LOG_* 配置）")
	}
	if operationLease == nil {
		return nil, errors.New("系统 API 组合根要求 F4 共享租约持有者（main 已与 F4 input server 共享同一 owner lease）")
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

	// X04 read faces: SQLite mode additionally opens the usage-catalog
	// registry (usage-records shard walk) and the table-monitor snapshot
	// database. PostgreSQL mode reaches juhe_usage/juhe_stats through the
	// shared pool, mirroring the Node PG branches.
	var usageCatalogDB *sql.DB
	if composed.pgDialect {
		usageCatalogDB = composed.db
	} else {
		if cfg.UsageCatalogDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_USAGE_CATALOG_DATABASE_PATH，无法打开 usage-records 目录数据库")
		}
		catalog, err := sql.Open("sqlite", sqliteFileDSN(cfg.UsageCatalogDatabasePath))
		if err != nil {
			return nil, fmt.Errorf("open usage catalog sqlite database: %w", err)
		}
		catalog.SetMaxOpenConns(1)
		if err := configureSQLiteConnection(catalog); err != nil {
			_ = catalog.Close()
			return nil, fmt.Errorf("configure usage catalog sqlite database: %w", err)
		}
		usageCatalogDB = catalog
		composed.shutdowns = append(composed.shutdowns, func() { _ = catalog.Close() })
	}
	var tableMonitorDB *sql.DB
	if composed.pgDialect {
		tableMonitorDB = composed.db
	} else {
		if cfg.TableMonitorDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_TABLE_MONITOR_DATABASE_PATH，无法打开表监控快照数据库")
		}
		monitorDB, err := sql.Open("sqlite", sqliteFileDSN(cfg.TableMonitorDatabasePath))
		if err != nil {
			return nil, fmt.Errorf("open table monitor sqlite database: %w", err)
		}
		monitorDB.SetMaxOpenConns(1)
		if err := configureSQLiteConnection(monitorDB); err != nil {
			_ = monitorDB.Close()
			return nil, fmt.Errorf("configure table monitor sqlite database: %w", err)
		}
		tableMonitorDB = monitorDB
		composed.shutdowns = append(composed.shutdowns, func() { _ = monitorDB.Close() })
	}

	// X04 404 项补齐 (logreads three-family reads): the audit F3 dataset, the
	// runtime-log F1 dataset and the F5 dataset file. Node opens all three
	// read-only (database.ts shouldOpenSqliteDatabaseReadOnly), so SQLite mode
	// opens read-only handles here and PostgreSQL mode reaches juhe_dataset
	// through the shared pools: audit through the F3 store pool (the same
	// JUHE_AI_AUDIT_LOG_POSTGRES_URL pool main acquired), runtime-log and
	// public-api-logs through the business pool like every other dataset
	// repository. The runtime-log file stays F1-jobs-owned: a missing file
	// keeps composing (the routes answer 500 like the Node readOnly open
	// would) instead of failing the whole composition.
	var auditDatasetDB *sql.DB
	if composed.pgDialect {
		if auditConfig.Mode == auditlog.ModePostgres && auditConfig.PostgresPool != nil {
			auditDatasetDB = auditConfig.PostgresPool.DB()
		}
		if auditDatasetDB == nil {
			return nil, errors.New("postgres 模式缺少 F3 audit 数据集连接池，无法挂载审计日志读面")
		}
	} else {
		if auditConfig.AuditDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_AUDIT_LOG_DATABASE_PATH，无法打开审计日志读面数据库")
		}
		if _, err := os.Stat(auditConfig.AuditDatabasePath); err != nil {
			return nil, fmt.Errorf("audit 数据集文件不存在（F3 input server 应已初始化）: %w", err)
		}
		handle, err := openSQLiteReadOnly(auditConfig.AuditDatabasePath)
		if err != nil {
			return nil, fmt.Errorf("open audit dataset sqlite database: %w", err)
		}
		auditDatasetDB = handle
		composed.shutdowns = append(composed.shutdowns, func() { _ = handle.Close() })
	}
	logReadsMode := logreads.ReadSQLite
	if composed.pgDialect {
		logReadsMode = logreads.ReadPostgres
	}
	var runtimeLogDatasetDB *sql.DB
	if composed.pgDialect {
		runtimeLogDatasetDB = composed.db
	} else {
		if cfg.RuntimeLogDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_RUNTIME_LOG_DATABASE_PATH，无法打开运行日志读面数据库")
		}
		handle, err := openSQLiteReadOnly(cfg.RuntimeLogDatabasePath)
		if err != nil {
			return nil, fmt.Errorf("open runtime-log dataset sqlite database: %w", err)
		}
		runtimeLogDatasetDB = handle
		composed.shutdowns = append(composed.shutdowns, func() { _ = handle.Close() })
	}
	var publicApiLogDatasetDB *sql.DB
	if composed.pgDialect {
		publicApiLogDatasetDB = composed.db
	} else {
		if cfg.DatasetDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_DATASET_DATABASE_PATH，无法打开公开接口日志读面数据库")
		}
		if _, err := os.Stat(cfg.DatasetDatabasePath); err != nil {
			return nil, fmt.Errorf("dataset 数据集文件不存在（启动 preflight 应已创建）: %w", err)
		}
		handle, err := openSQLiteReadOnly(cfg.DatasetDatabasePath)
		if err != nil {
			return nil, fmt.Errorf("open public-api-log dataset sqlite database: %w", err)
		}
		publicApiLogDatasetDB = handle
		composed.shutdowns = append(composed.shutdowns, func() { _ = handle.Close() })
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
	// The usageStatsTimezone guard probes the dedicated stats database (Node
	// getStatsDatabase()); probing the business handle 500s every online
	// timezone change in the six-database SQLite split. PostgreSQL mode never
	// probes (the guard rejects the online change first).
	if !composed.pgDialect {
		settingsStore.SetStatsDatabase(composed.statsDB)
	}
	composed.settingsStore = settingsStore
	settingValue := settingsValueReader(settingsStore)

	// X04 404 项补齐 (logreads three-family reads): build the audit/runtime/
	// public-api log readers over the dataset handles opened above.
	auditLogReader, err := logreads.NewAuditLogQueryReader(auditDatasetDB, logReadsMode, logreads.AuditQueryDirectories{
		HotSearchDirectory:   auditConfig.HotSearchDirectory,
		PayloadBlobDirectory: auditConfig.PayloadBlobDirectory,
	})
	if err != nil {
		return nil, fmt.Errorf("create audit log reader: %w", err)
	}
	// Retention days mirror Node runtimeLogIndexRetentionDaysFromSettings:
	// the integer runtimeLogIndexRetentionDays setting (else the 14-day
	// default), clamped to 1..90 by the reader.
	runtimeLogReader, err := logreads.NewRuntimeLogSQLReaderWithSources(runtimeLogDatasetDB, logReadsMode, func() int {
		value, readErr := settingValue("runtimeLogIndexRetentionDays")
		if readErr != nil {
			return 14
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return 14
		}
		return parsed
	}, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create runtime log reader: %w", err)
	}
	publicApiLogReader, err := logreads.NewPublicApiLogSQLStore(publicApiLogDatasetDB, logReadsMode)
	if err != nil {
		return nil, fmt.Errorf("create public api log reader: %w", err)
	}
	grepService := logreads.NewRuntimeLogGrep(logreads.RuntimeLogGrepConfig{
		FileEnabled:   cfg.LogFileEnabled,
		Directory:     cfg.LogDir,
		MaxFiles:      cfg.LogMaxFiles,
		RetentionDays: cfg.LogRetentionDays,
	})

	authzStore, err := authz.NewStore(composed.db, composed.pgDialect, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create authorization store: %w", err)
	}
	composed.AuthzStore = authzStore
	teamStore, err := systemteams.NewStore(composed.db, composed.pgDialect, time.Now, authzStore)
	if err != nil {
		return nil, fmt.Errorf("create system-teams store: %w", err)
	}
	// WithGlobalConcurrencyMax carries the parsed JUHE_AI_CONCURRENCY_GLOBAL_MAX
	// into the DEFAULT scheduling-policy projection (Node reads
	// runtimeConfig.concurrency.globalMax live; the store default stays 5000).
	groupsStore, err := groups.NewStore(composed.db, composed.pgDialect, time.Now, newCompositionID, bus, groups.WithGlobalConcurrencyMax(cfg.ConcurrencyGlobalMax))
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
	// M07 handover closures: the list/detail usage projection reads the stats
	// database through the StatsUsageSource (zero summaries stay the nil-source
	// degradation), and the delete-transaction cleanup handoff reaches the
	// dataset placement. PostgreSQL registers the target in-transaction through
	// the shared juhe_dataset schema; SQLite needs the writable dataset file
	// handle Node opens for registerDeletedApiKeyRecordCleanupTarget.
	usageSource, err := apikeys.NewStatsUsageSource(composed.statsDB, composed.pgDialect)
	if err != nil {
		return nil, fmt.Errorf("create api-key stats usage source: %w", err)
	}
	apiKeyStore.SetUsageSource(usageSource)
	apiKeyStore.SetCleanupSubmitter(apiKeyCleanupSubmitter{store: apiKeyStore})
	if !composed.pgDialect {
		if cfg.DatasetDatabasePath == "" {
			return nil, errors.New("sqlite 模式缺少 JUHE_AI_DATASET_DATABASE_PATH，无法登记 API Key 删除清理目标")
		}
		apiKeyDatasetDB, err := sql.Open("sqlite", sqliteFileDSN(cfg.DatasetDatabasePath))
		if err != nil {
			return nil, fmt.Errorf("open api-key cleanup dataset sqlite database: %w", err)
		}
		apiKeyDatasetDB.SetMaxOpenConns(1)
		if err := configureSQLiteConnection(apiKeyDatasetDB); err != nil {
			_ = apiKeyDatasetDB.Close()
			return nil, fmt.Errorf("configure api-key cleanup dataset sqlite database: %w", err)
		}
		apiKeyStore.SetDatasetDB(apiKeyDatasetDB)
		composed.shutdowns = append(composed.shutdowns, func() { _ = apiKeyDatasetDB.Close() })
	}
	accountStore, err := accounts.NewStore(composed.db, composed.pgDialect, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		return nil, fmt.Errorf("create account store: %w", err)
	}
	// T2 audit wiring: every AI-account management write path invalidates the
	// gateway runtime cache through the K5 bus post-commit (batch edit,
	// management patch, soft delete; Node invalidateGatewayRuntimeAfterBusinessWrite).
	accountStore.SetCacheInvalidator(accountsBusInvalidator{bus: bus})
	// SQLite account deletion performs the Node per-grant authorization
	// runtime sync only through this composition-root port; PostgreSQL keeps
	// its existing bulk transaction path inside accounts.Delete.
	accountStore.SetDeletedResourceGrantRevoker(authzStore)
	// 手动账号测试派发装配（test_effects.go）：POST /accounts/{id}/test 的
	// worker 派发经 jobs internal-api loopback（HMAC 签名，见
	// compose_account_test_dispatch.go）。jobs 缺席时桥接返回 false，路由
	// 落 Node worker-unavailable 契约（任务置败 + 503）——与 nil 端口降级
	// 契约一致，组合根不因 jobs 缺席而失败。
	accountStore.SetTestDispatchEffects(newJobsAccountTestDispatchBridge(cfg.JobsInternalURL, cfg.Secret, nil))
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
	oauthStore, err := oauthmgmt.NewStore(composed.db, composed.pgDialect, cfg.Secret, accountStore, oauthmgmt.NewHTTPTokenExchanger(), time.Now, newCompositionID,
		// T2 audit wiring: the rotation post-commit invalidation channels ride
		// the same bus adapter as the accounts slice, and the in-transaction
		// circuit dispatch-revision fence reuses the accounts family advance
		// (Node oauth-credential-rotation.repository.ts:202-226).
		oauthmgmt.WithCacheInvalidator(accountsBusInvalidator{bus: bus}),
		oauthmgmt.WithDispatchRevisionAdvancer(accountStore))
	if err != nil {
		return nil, fmt.Errorf("create oauth management store: %w", err)
	}

	// F4 producer sink: every management mutation lands in the operation log
	// through the in-process producer. The producer shares the process-wide
	// LeaseKeeper with the F4 input server (single owner_id/fence_token for
	// both writers of this process); the renewal lifecycle is owned by the
	// keeper, while the producer only extends the same lease per record with
	// the configured owner-lease TTL.
	producer := operationlog.NewProducer(operationStore, operationLease.Lease(), operationlog.Config{OwnerLease: operationLease.TTL()}, producerLogger{})
	composed.producer = producer
	composed.operationStore = operationStore
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
	composed.authDeps = authDeps
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
	composed.kernel = kern

	// Management route families.
	authDeps.MountAuth(kern, cfg.CookieSameSite, cfg.CookieSecure)
	authDeps.MountSystemAccounts(kern, cfg.CookieSameSite, cfg.CookieSecure)
	announcements.Mount(kern, authDeps, announcementStore, sink)
	(&systemteams.Deps{Store: teamStore, Sink: sink, Auth: authDeps}).Mount(kern)
	// The authorization family (M04) plus its X04 authorization-options
	// surface mount through authzDeps below.
	(&settings.Deps{Store: settingsStore, Auth: authDeps, Sink: sink}).Mount(kern)
	// The groups family mounts the M05 return-authorization route through the
	// authz return domain (Node returnGroupAuthorizationForGranteeAsync).
	(&groups.Deps{Store: groupsStore, Auth: authDeps, Sink: sink, Authz: authzStore}).Mount(kern)
	(&routestrategies.Deps{Store: routeStrategyStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&apikeys.Deps{Store: apiKeyStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&accounts.Deps{Store: accountStore, Auth: authDeps, Sink: sink}).Mount(kern)
	// providers built-in PATCH (update_model_configuration) operation log:
	// same authsys producer sink as the other management families (the Deps
	// port existed without its composition wiring until this wave's assembly
	// handover).
	(&providers.Deps{Store: providerStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&oauthmgmt.Deps{Store: oauthStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.InspectionDeps{Store: inspectionStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.ExternalDeps{Store: externalStore, Auth: authDeps, Sink: sink}).Mount(kern)
	(&policyreads.OAuthDeps{Store: oauthPolicyStore, Auth: authDeps, OIDCEnabled: cfg.OIDCEnabled, OIDCIssuer: cfg.OIDCIssuer}).Mount(kern)
	(&logreads.Deps{Reader: operationStore, Auth: authDeps}).Mount(kern)
	// X04 404 项补齐: the audit-logs / runtime-logs / public-api-logs read
	// families (Node system-api-app.ts lines: /audit-logs, /runtime-logs,
	// /public-api-logs; none of them has a my-* variant).
	(&logreads.ReadsDeps{
		Audit:   auditLogReader,
		Runtime: runtimeLogReader,
		Public:  publicApiLogReader,
		Grep:    grepService,
		Auth:    authDeps,
	}).Mount(kern)
	(&ipstats.Deps{Store: ipStatsStore, Auth: authDeps, Sink: sink}).Mount(kern)
	// X04 404 项补齐: stats + usage-records (statreads), ui-bootstrap,
	// authorization-options (authz), proxies (proxyprofiles) and the
	// table-monitor family (cleanup POST dispatches through the durable
	// record_maintenance_jobs table; jobs-side drain stays jobs-owned).
	authzDeps := &authz.Deps{Store: authzStore, Sink: sink, Auth: authDeps}
	authzDeps.Mount(kern)
	authzDeps.MountAuthorizationOptions(kern)
	proxyStore, err := proxyprofiles.NewStore(proxyprofiles.Deps{
		DB:        composed.db,
		PGDialect: composed.pgDialect,
		Secret:    cfg.Secret,
		Now:       time.Now,
		NewID:     newCompositionID,
	})
	if err != nil {
		return nil, fmt.Errorf("create proxy store: %w", err)
	}
	proxyprofiles.Mount(kern, authDeps, proxyStore, sink)
	(&uibootstrap.Deps{DB: composed.db, PGDialect: composed.pgDialect, Auth: authDeps}).Mount(kern)
	tableMonitorStore, err := tablemonitor.NewStore(tableMonitorDB, composed.pgDialect)
	if err != nil {
		return nil, fmt.Errorf("create table monitor store: %w", err)
	}
	recordMaintenanceDispatch, err := tablemonitor.NewDurableRecordMaintenanceDispatch(composed.db, composed.pgDialect, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create record maintenance dispatch: %w", err)
	}
	(&tablemonitor.Deps{
		Store:    tableMonitorStore,
		Cache:    tablemonitor.NewOverviewCache(),
		Dispatch: recordMaintenanceDispatch,
		Sink:     sink,
	}).Mount(kern, authDeps)
	var healthOutcomes *statreads.HealthOutcomeSource
	if cfg.AccountHealthOutcomeSQLitePath != "" {
		healthOutcomes = &statreads.HealthOutcomeSource{SQLitePath: cfg.AccountHealthOutcomeSQLitePath}
	}
	if healthOutcomes == nil {
		healthOutcomes = &statreads.HealthOutcomeSource{}
	}
	// J1 durable outcomes in the performance topology live in the jobs
	// Postgres database (juhe_jobs); the SQLite path above stays the
	// standalone-mode source. Mirrors Node readPostgresOutcomesForAccounts.
	healthOutcomes.PostgresURL = cfg.AccountHealthOutcomePostgresURL
	(&statreads.Deps{
		Business:            composed.db,
		Stats:               composed.statsDB,
		UsageCatalog:        usageCatalogDB,
		PGDialect:           composed.pgDialect,
		Auth:                authDeps,
		Now:                 time.Now,
		Timezone:            statreads.NewSystemSettingsTimezoneSource(composed.db, composed.pgDialect),
		GoRuntimeMetricsURL: cfg.GoRuntimeMetricsURL,
		HealthOutcomes:      healthOutcomes,
		RuntimeMode:         cfg.RuntimeMode,
	}).Mount(kern)
	// X04: the /__aisys__/help static help center, session-gated like the Node
	// web layer (requireHelpSession + role redirects over dist/help).
	(&helpweb.Deps{
		Auth:         authDeps,
		DistPath:     cfg.FrontendDistPath,
		DevAutoLogin: devAutoLoginResolver(authDeps, cfg.DevAutoLoginUsername),
	}).Mount(kern)

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
		Tokens:     oidcStore,
		Limiter:    protocolLimiter,
		Groups:     groupsStore,
		Strategies: routeStrategyStore,
		ApiKeys:    apiKeyStore,
		AiAccounts: accountStore,
		// T2 audit wiring: committed api-key patches invalidate through the
		// same bus (delegated.Deps.Inval carries the apikeys.CacheInvalidator).
		Inval:          apikeys.BusInvalidator{Bus: bus},
		DB:             composed.db,
		PGDialect:      composed.pgDialect,
		Settings:       delegatedSettingsAdapter{read: settingValue},
		Usage:          unavailableUsageReader{},
		RedisNamespace: cfg.RedisNamespace,
		Now:            time.Now,
	}).Mount(kern)

	// G20 phase-2 AI gateway /v1 chain: assembles the concrete runtime
	// services (chain_runtime.go) plus the composition adapters
	// (chain_*.go), then mounts the /v1 orchestrator on the kernel. Every
	// frozen port fails fast by name; nothing is wired nil.
	// chainServices is hoisted so the /__aipublic__ family (mounted after this
	// block) can share the runtime-state redis client for its penalty-window
	// limiter.
	var chainServices *chainRuntimeServices
	if cfg.ChainEnabled {
		spoolDirectory := cfg.UsageSpoolDirectory
		if spoolDirectory == "" && cfg.StatsDatabasePath != "" {
			spoolDirectory = filepath.Join(filepath.Dir(cfg.StatsDatabasePath), "usage-record-spool")
		}
		services, chainErr := composeChainRuntimeServices(composed, cfg, settingValue)
		if chainErr != nil {
			return nil, fmt.Errorf("compose gateway chain runtime services: %w", chainErr)
		}
		chainServices = services
		// Runtime-reset port assembly (compose_accounts_reset.go): the
		// maintenance reset endpoint reaches the gateway runtime surfaces
		// through this bridge. With the chain disabled the port stays nil and
		// the endpoint keeps its self-contained degraded contract.
		resetBridge, resetBridgeErr := newAccountsRuntimeResetBridge(composed, settingValue, chainServices, cfg.Secret)
		if resetBridgeErr != nil {
			return nil, fmt.Errorf("compose accounts runtime reset bridge: %w", resetBridgeErr)
		}
		accountStore.SetRuntimeResetEffects(resetBridge)
		// M11 runtime handover assembly (F1-1): the traffic-migration route
		// reaches the gateway session affinity through this bridge (Node
		// migrateServerOpenAIAccountTrafficRuntime local branch). With the
		// chain disabled the port stays nil and the route keeps its
		// { migratedSessionCount: 0 } degraded contract.
		accountStore.SetTrafficRuntimeMigrator(trafficRuntimeMigratorBridge{affinity: chainServices.Identity.Affinity})
		// 显式账户错误策略装配（chain_error_policy_effects.go）：失败派发器的
		// 决策服务 + cooldown/disable 状态写侧桥。装配失败按组合根约定
		// fail-fast；链条关闭时端口随进程退出，无需单独关闭。
		errorPolicyBridge, errorPolicyService, errorPolicyBridgeErr := newChainErrorPolicyEffectsBridge(composed, cfg.Secret)
		if errorPolicyBridgeErr != nil {
			return nil, fmt.Errorf("compose account error policy effects bridge: %w", errorPolicyBridgeErr)
		}
		// Shutdown order is LIFO: services registered first close last, after
		// the chain drained its usage buffer.
		composed.shutdowns = append(composed.shutdowns, chainServices.Close)
		chain, chainShutdown, chainAssembleErr := composeGatewayChain(chainRuntimeDeps{
			Cache:           chainServices.Cache,
			Clock:           gatewaypreauth.SystemClock{},
			AuditLogEnabled: func() bool { return cfg.AuditLogEnabled },
			AuditInputURL:   cfg.AuditInputURL,
			QueueDefaults: gatewayclientip.HighConcurrencyPolicyDefaults{
				MaxQueueSize:        cfg.ConcurrencyGlobalMax,
				PerAPIKeyQueueLimit: cfg.ConcurrencyGlobalMax,
			},
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
			// G20 phase-3: hybrid Redis collaborators + the G14 session
			// identity services (both degrade by driver axes, never nil).
			HybridScoringCache: chainServices.HybridScoringCache,
			HybridRuntimeState: chainServices.HybridRuntimeState,
			Identity:           chainServices.Identity,
			// T2 终局遗留①: the auxiliary dispatch loop replays in-process over
			// the routing runtime cache + shared provider driver + engine
			// transport (Node dispatchHybridAuxiliaryChatCompletion).
			HybridAuxiliary: newChainHybridAuxiliaryDispatcher(chainServices.Cache),
			// 显式账户错误策略：failureKind / 换 Key 授权 / cooldown-disable
			// 状态变更 / system quota 归因（Node decideAccountErrorPolicy 接线）。
			AccountErrorPolicy:        errorPolicyService,
			AccountErrorPolicyEffects: errorPolicyBridge,
			// 失败派发链：request-failure 健康检查派发桥目标 + turn-retry
			// Redis 状态驱动（StateClient 为 nil 时适配器返回 nil，链条保持
			// memory 驱动——见 chain_request_failure_health.go /
			// chain_turn_retry_redis.go）。
			JobsInternalURL:     cfg.JobsInternalURL,
			TurnRetryStateStore: newChainTurnRetryRedisStateStoreOrNil(chainServices.StateClient, cfg.RedisNamespace),
		})
		if chainAssembleErr != nil {
			chainServices.Close()
			return nil, fmt.Errorf("compose gateway chain: %w", chainAssembleErr)
		}
		composed.shutdowns = append(composed.shutdowns, chainShutdown)
		composed.chain = chain
		composed.chainServices = chainServices
		// The /v1 subtree serves the gateway protocol paths; the
		// openai-compatible files / vector-stores families mount into the
		// chain's non-protocol paths (chain_openaicompat.go); everything else
		// answers the Node 404 JSON inside the chain.
		kern.Register("/v1", chain)
		kern.Register("/v1/", chain)
		if chainErr := mountChainOpenAICompatFamilies(composed, chain, cfg, chainServices); chainErr != nil {
			return nil, fmt.Errorf("mount openai-compatible families: %w", chainErr)
		}
		// G20 phase-3 my-chat family: the chat database owner + the
		// generation-wave ports mount together (the family dispatches into
		// the in-process chain).
		chatDB, ownChatDB, chatOpenErr := openChatDatabase(cfg, postgresPools, composed.db, composed.pgDialect)
		if chatOpenErr != nil {
			return nil, chatOpenErr
		}
		composed.chatDB = chatDB
		composed.ownChatDB = ownChatDB
		if _, chatErr := composeChatFamily(composed, cfg, chatDB, chainServices, chain); chatErr != nil {
			return nil, fmt.Errorf("compose my-chat family: %w", chatErr)
		}
		// cacheDriver==='redis': the system-api limiter switches onto the
		// shared fixed-window redis store (task item 9); memory driver keeps
		// the in-process store built above.
		if chainServices.RateLimitStore != nil {
			limiter.Store = chainServices.RateLimitStore
		}
		slog.Info("gateway chain composed", "trafficSource", "gateway", "spoolDirectory", spoolDirectory != "", "auditDispatch", cfg.AuditInputURL != "", "chatFamily", "mounted")
	}

	// X04: the /__aipublic__ externally maintained legacy family mounts after
	// the chain runtime services exist (the penalty-window limiter shares the
	// runtime-state redis client); bearer-token sources
	// (external_integration_sources/token tables) are validated by the
	// aipublic guard; resource families reuse the same store instances as the
	// management mounts above.
	aipublicDeps := &aipublic.Deps{
		DB:             composed.db,
		PGDialect:      composed.pgDialect,
		Now:            time.Now,
		SystemAccounts: systemAccountStore,
		Groups:         groupsStore,
		Strategies:     routeStrategyStore,
		ApiKeys:        apiKeyStore,
		AiAccounts:     accountStore,
		Sink:           sink,
	}
	// Penalty-window limiter shares the runtime-state redis keyspace with the
	// gatewayproxyhealth limiter family (memory fallback keeps single-instance
	// semantics when runtimeStateDriver !== 'redis').
	if chainServices != nil && chainServices.StateClient != nil {
		aipublicDeps.RedisDriver = true
		aipublicDeps.RedisStateClient = chainServices.StateClient
		aipublicDeps.RedisNamespace = cfg.RedisNamespace
	}
	// capturePublicApiLog (Node /__aipublic__ prefix middleware): a writable
	// dataset handle feeds the P05 pipeline (bounded channel → batch insert).
	// The read families open dataset handles read-only; the capture writer is
	// the go-only gateway's one writable dataset consumer.
	var captureDatasetDB *sql.DB
	if composed.pgDialect {
		captureDatasetDB = composed.db
	} else if cfg.DatasetDatabasePath != "" {
		if _, err := os.Stat(cfg.DatasetDatabasePath); err == nil {
			handle, err := sql.Open("sqlite", "file:"+cfg.DatasetDatabasePath)
			if err == nil {
				captureDatasetDB = handle
				composed.shutdowns = append(composed.shutdowns, func() { _ = handle.Close() })
			}
		}
	}
	if captureDatasetDB != nil {
		if logStore, err := publicapilogs.NewStore(captureDatasetDB, composed.pgDialect, time.Now, nil); err == nil {
			pipeline := publicapilogs.NewPipeline(logStore, publicapilogs.Config{})
			composed.shutdowns = append(composed.shutdowns, func() { pipeline.Close(context.Background()) })
			aipublicDeps.Capture = aipublic.PublicApiLogCaptureSink(pipeline.Enqueue)
		}
	}
	aipublicDeps.Mount(kern)

	// /__aisys__/api/health: readiness contract; the rate limiter bypasses it
	// (isSystemApiHealthPath mirror) and the kernel registers it ahead of the
	// auth chain like Node line 134. The account-balance dependency resolves
	// through the archived Node accountBalanceGoOwnerHealth hotfix contract:
	// blue-green ownerMode splitting with standby judged by jobs reachability
	// plus the peer ownerMode; the answer stays 200 and degrades only in the
	// body (resolveSystemApiHealth mirror).
	kern.Register("GET "+systemAPIPrefix+"/health", kernel.HealthHandler(func() (int, any) {
		accountBalance := accountBalanceGoOwnerHealth(os.Getenv, accountBalanceHealthDeps{})
		return http.StatusOK, map[string]any{
			"statusCode":     200,
			"status":         accountBalanceSystemHealthStatus(accountBalance.Ready),
			"service":        "juhe-ai-db-service",
			"accountBalance": accountBalance,
			"proxyLatency":   map[string]any{"enabled": false, "ready": true},
			"checkedAt":      time.Now().UTC().Format(time.RFC3339Nano),
		}
	}))

	// X01 go-only terminal state: no legacy bridge fallback remains. The
	// Node origin is archived, so every prefix without a registered route
	// resolves to the kernel 404 JSON contract (405 converts to the same
	// 404 JSON, mirroring Express).
	// Composition shutdown order (Node shutdownDbService mirror): the F4
	// producer is drained by the composed shutdown before main closes the
	// shared LeaseKeeper, which then releases the F4 persistence lease so a
	// successor process can take over immediately. The HTTP servers and
	// shared pools are closed by main around this call.
	composed.Kernel = kern.Handler()
	composed.DB = composed.db
	return composed, nil
}

func settingsTimezone(read SettingValueFunc) ipstats.TimezoneSource {
	return func(context.Context) (string, error) {
		return read("usageStatsTimezone")
	}
}

// apiKeyCleanupSubmitter adapts the apikeys after-commit maintenance handoff
// onto the durable dataset target row: the Go jobs scheduler drains
// api_key_record_cleanup_targets (api-key-record-cleanup-retry), the Go
// equivalent of Node's record-maintenance enqueue plus background retry pass.
// Errors never fail the delete — the store surfaces them as
// DeleteResult.CleanupSubmitError (Node catch-and-continue).
type apiKeyCleanupSubmitter struct{ store *apikeys.Store }

func (s apiKeyCleanupSubmitter) SubmitAPIKeyRelatedCleanup(ctx context.Context, apiKeyID, systemAccountID string) error {
	return s.store.RegisterCleanupTarget(ctx, apiKeyID, systemAccountID)
}

// devAutoLoginResolver wires the development auto-login account into the help
// static surface (Node serve development auto login through the db-service
// loopback /auth/me); without a configured username it resolves nothing.
func devAutoLoginResolver(authDeps *authsys.Deps, username string) func(*http.Request) *authsys.AuthContext {
	if username == "" || authDeps.Accounts == nil {
		return nil
	}
	return func(*http.Request) *authsys.AuthContext {
		summary, err := authDeps.Accounts.FindByUsername(context.Background(), username)
		if err != nil || summary.ID == "" || summary.Status != "active" {
			return nil
		}
		return &authsys.AuthContext{
			SystemAccountID: summary.ID,
			Username:        summary.Username,
			DisplayName:     summary.DisplayName,
			Role:            summary.Role,
			SessionID:       "development-auto-login",
		}
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

// accountsBusInvalidator adapts the K5 invalidation bus onto the AI-account
// management write-path invalidation channels (T2 audit wiring): the
// accounts.CacheInvalidator batch port (问题-0172 T1), the patch/delete
// runtime channel and the oauthmgmt rotation ports all publish the Node
// reason strings onto the shared topics. The per-account lookup channel stays
// a documented no-op until the Go management slice grows a lookup cache (the
// gateway runtime accountsCache is cleared wholesale by the runtime channel
// that always follows in the same post-commit tail).
type accountsBusInvalidator struct{ bus *inval.Bus }

func (a accountsBusInvalidator) InvalidateAccountLookup(accountID string) error {
	_ = accountID
	return nil
}

func (a accountsBusInvalidator) InvalidateGatewayRuntime(reason string) error {
	if a.bus == nil {
		return errors.New("cache invalidation bus is not wired")
	}
	a.bus.Invalidate(inval.TopicGatewayRuntime, reason)
	return nil
}

// InvalidateRuntime / InvalidateAPIKeyValidation mirror the authsys
// double-channel shape for the oauthmgmt rotation (CacheInvalidator port).
func (a accountsBusInvalidator) InvalidateRuntime(reason string) error {
	return a.InvalidateGatewayRuntime(reason)
}

func (a accountsBusInvalidator) InvalidateAPIKeyValidation(reason string) error {
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

// openSQLiteReadOnly opens a read-only SQLite handle over an existing file
// (Node createSqliteDatabase readOnly open: the gateway never creates or
// migrates another owner's database).
func openSQLiteReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
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
