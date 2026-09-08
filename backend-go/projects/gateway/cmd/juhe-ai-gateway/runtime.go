package main

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// Node runtime.ts:399-400: the default development secret and the minimum
// production secret length used by assertProductionSecret.
const (
	defaultRuntimeSecret          = "juhe-ai-dev-secret-change-me"
	minimumProductionSecretLength = 32
)

// runtimeConfig mirrors the env conventions of the Node composition root
// (backend/src/config/runtime.ts) for the scope the Go gateway composition
// consumes: runtime mode, storage drivers (sqlite/postgres, memory/redis),
// dual-mode database paths, Redis URLs + namespace, secret, cookie/cors/
// oidc/trust-proxy HTTP security and the composition gates.
//
// Validation mirrors the Node fail-fast contract: an enabled redis driver
// without its URL, an enabled OIDC without issuer/secret, a none-cookie
// without secure, a production process without a strong secret or an explicit
// CORS origin allowlist, or an enabled composition without the business owner
// handoff gates exits at startup instead of serving a partial owner.
//
// The production signal for every HTTP security gate is the normalized
// NODE_ENV (Node isProductionRuntime, runtime.ts:979-980); the same-process
// operationlog/config.go and auditlog/input_server.go compositions read
// NODE_ENV too. JUHE_AI_NODE_ENV only overrides the chat tool environment
// (falling back to NODE_ENV when unset) and carries no production semantics;
// ownermode and JUHE_AI_DEPLOY_MODE carry none either.
type runtimeConfig struct {
	RuntimeMode        string // "standalone" | "performance"
	DatabaseDriver     string // "sqlite" | "postgres"
	CacheDriver        string // "memory" | "redis"
	RuntimeStateDriver string // "memory" | "redis"

	PostgresURL    string
	RedisCacheURL  string
	RedisStateURL  string
	RedisNamespace string
	Secret         string

	DatabasePath             string
	ChatDatabasePath         string
	DatasetDatabasePath      string
	RuntimeLogDatabasePath   string
	UsageCatalogDatabasePath string
	StatsDatabasePath        string
	TableMonitorDatabasePath string
	// Codex context state shard layout (Node JUHE_AI_CODEX_CONTEXT_STATE_*,
	// Node default 16 shards within the 1..256 bound). Consumed by the SQLite
	// six-database startup preflight.
	CodexContextShardRoot  string
	CodexContextShardCount int

	OpenAICompatibleFilesRoot string

	// Chat mount config (Node runtimeConfig.chat + chatAssetsRoot).
	ChatAssetsRoot              string
	ChatMaxTurnsPerConversation int64
	ChatRetentionDays           int
	ChatDiagnosticToolEnabled   bool
	ChatToolEnvironment         string

	Host string
	Port int

	CookieSecure   bool
	CookieSameSite string
	TrustProxy     string

	// HTTP security CORS slice (Node runtimeConfig.httpSecurity.cors,
	// JUHE_AI_ALLOWED_ORIGINS): non-production without explicit origins keeps
	// the local-dev allow-any contract; production must pin the exact admin
	// frontend origins and refuses '*'.
	CORSAllowedOrigins []string
	CORSAllowAnyOrigin bool

	CaptchaDisabled            bool
	DevAutoLoginUsername       string
	TemporaryAccessIPAllowlist []string

	OIDCEnabled             bool
	OIDCIssuer              string
	OIDCKeyEncryptionSecret string

	// SystemAPIEnabled gates the Go system-api composition
	// (/__aisys__/api + /__aipublic__ + /__aidelegated__/v1). It mirrors the
	// opt-in pattern of every earlier migration wave: the composition assembles
	// only when the operator explicitly hands the system API to this process.
	SystemAPIEnabled bool
	// ChainEnabled gates the AI gateway /v1 composition. When enabled the
	// startup assembles the full serving chain; when disabled /v1 traffic
	// answers the kernel 404 JSON contract (X01: the legacy bridge proxy was
	// deleted together with the archived Node origin).
	ChainEnabled bool

	// Chain collaborator config: the audit capture switch (Node
	// runtimeConfig.auditLog.enabled, JUHE_AI_AUDIT_LOG_ENABLED default true)
	// and the durable usage-record spool directory
	// (JUHE_AI_USAGE_SPOOL_DIRECTORY；兼容旧名 JUHE_AI_USAGE_SPOOL_DIR，
	// DIRECTORY 优先). The F3 loopback audit input URL
	// (derived from JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS) is deleted since
	// 去跨进程战役第四刀 — chain audit dispatch goes through the in-process
	// producer.
	AuditLogEnabled     bool
	UsageSpoolDirectory string
	// Usage spool capacity knobs (Node runtimeConfig.usageSpool,
	// runtime.ts:661-666): JUHE_AI_USAGE_SPOOL_MAX_ITEMS [1000, 5_000_000]
	// default 250_000, JUHE_AI_USAGE_SPOOL_MAX_MB [64, 102_400] default
	// 4_096, JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE [1, 5_000] default 500,
	// JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS [100, 60_000] default 1_000.
	// D-209：Go 曾完全未读取这四个容量 env（chain_compose 硬编码同值默认），
	// 部署调参静默失效。
	UsageSpoolMaxItems         int
	UsageSpoolMaxBytes         int
	UsageSpoolReplayBatchSize  int
	UsageSpoolReplayIntervalMs int
	// UsageFinalizationMaxItems mirrors Node
	// runtimeConfig.gateway.usageFinalizationMaxItems
	// (JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS, default 2048,
	// [1, 1_000_000]). D-147：chain_compose 曾硬传 (0,0) 使该 env 与
	// JUHE_AI_CONCURRENCY_GLOBAL_MAX 对收尾队列失效。
	UsageFinalizationMaxItems int

	// UpstreamURLSecurity mirrors Node runtimeConfig.upstreamUrlSecurity
	// (runtime.ts:1679-1690): JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS
	// (production refuses it) and the normalized IP-origin keys of
	// JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST. D-192/D-146：请求期
	// DNS resolve-all + 钉扎策略的组合根配置。
	UpstreamURLSecurity UpstreamURLSecurityConfig

	// Read-face collaborators (X04 404 项补齐):
	// GoRuntimeMetrics is the shared sampler/store env family
	// (JUHE_AI_GO_RUNTIME_METRICS_*): the gateway self-samples its Go runtime
	// (role default gateway) and serves the go-runtime-trend route by querying
	// the same store in-process. Disabled (default) keeps the route on the
	// empty-items degradation.
	GoRuntimeMetrics gometrics.Config
	// AccountHealthProbeDeadlineMS is the probe deadline window the gateway
	// writes into every account_health_probe_request_outbox row
	// (JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS, the same env
	// the old jobs-side publisher read: Node integerConfig default 65000,
	// range [1000, 600000]). An invalid value keeps the outbox writer inert
	// (0 → dispatches report input_unavailable), matching the old
	// jobs-side missing-assembly semantics.
	AccountHealthProbeDeadlineMS int64
	// AccountHealthOutcomeSQLitePath is the J1 jobs outcome store the ai-health
	// reads merge (Node JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH).
	AccountHealthOutcomeSQLitePath string
	// AccountHealthOutcomePostgresURL is the performance-topology J1 outcome
	// database the ai-health reads merge (jobs JUHE_AI_JOBS_OUTCOME_POSTGRES_URL
	// counterpart; Node merged readPostgresOutcomesForAccounts from the same DB).
	AccountHealthOutcomePostgresURL string
	// ConcurrencyGlobalMax mirrors Node runtimeConfig.concurrency.globalMax
	// (JUHE_AI_CONCURRENCY_GLOBAL_MAX, default 5000): the DEFAULT
	// high-concurrency scheduling policy queue bounds and the dispatch
	// candidate window limit derive from it.
	ConcurrencyGlobalMax int
	// DispatchAccountCandidateLimit mirrors Node
	// runtimeConfig.gateway.dispatchAccountCandidateLimit
	// (JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT, default
	// ConcurrencyGlobalMax, integerConfig bounded 1..50000): the dispatch
	// candidate window final limit; scan limit = limit * 2.
	DispatchAccountCandidateLimit int
	// FrontendDistPath is the frontend dist directory backing the
	// /__aisys__/help static surface (Node derives it from backendRoot).
	FrontendDistPath string

	// Runtime-logs grep surface (X04 404 项补齐): Node runtimeConfig.log
	// fields the grep family reads (JUHE_AI_LOG_DIR / JUHE_AI_LOG_FILE_ENABLED
	// / JUHE_AI_LOG_MAX_FILES / JUHE_AI_LOG_RETENTION_DAYS). The gateway only
	// scans these files; an empty directory keeps the family on the
	// file-logging-disabled contract.
	LogDir           string
	LogFileEnabled   bool
	LogMaxFiles      int
	LogRetentionDays int

	// Business owner handoff gates for the business database this composition
	// would own. Names mirror the J3b owner contract (modelcheckowner).
	BusinessOwner               string
	BusinessDatabasePath        string
	BusinessPostgresURL         string
	BusinessHandoffConfirmed    bool
	BusinessNodeWriterStopped   bool
	BusinessSchemaReady         bool
	BusinessOwnerEpoch          string
	BusinessCutoverEvidencePath string
}

// UpstreamURLSecurityConfig mirrors Node runtimeConfig.upstreamUrlSecurity.
// It aliases the shared transport type so the composition can hand the
// loaded config straight to the dispatch URL policy.
type UpstreamURLSecurityConfig = sharedupstreamhttp.URLSecurityConfig

func hasAnyRawConfig(getenv func(string) string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(getenv(key)) != "" {
			return true
		}
	}
	return false
}

func envBoolTrue(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// strictEnvBool mirrors Node strictBooleanConfig (runtime.ts:1575-1582):
// true/1/yes/on -> true and false/0/no/off -> false (case-insensitive), any
// other non-empty value fails fast at startup, and an empty value keeps the
// caller's fallback.
func strictEnvBool(name, raw string, fallback bool) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s 只能配置为 true/false/1/0/yes/no/on/off: %q", name, raw)
	}
}

// parseTruncatedInt mirrors Node numberConfig (runtime.ts:1384-1395): the raw
// value must parse as a finite number, Math.trunc applies before the range
// check, so "10.5" is accepted as 10 while a non-number fails fast with its
// own message.
func parseTruncatedInt(name, raw string, min, max int) (int, error) {
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%s 必须配置为数字: %q", name, raw)
	}
	truncated := int(math.Trunc(number))
	if truncated < min || truncated > max {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间: %q", name, min, max, raw)
	}
	return truncated, nil
}

// productionRuntime mirrors Node isProductionRuntime (runtime.ts:979-980):
// the normalized NODE_ENV is the single production signal for every HTTP
// security gate — secret strength (runtime.ts:949-960), the cookie secure
// default (runtime.ts:1521), the dev auto-login disable (development.ts) and
// the CORS allowlist requirement (runtime.ts:1519-1557). JUHE_AI_NODE_ENV is
// not a production signal (it only overrides the chat tool environment below);
// ownermode and JUHE_AI_DEPLOY_MODE carry no production semantics in this
// codebase.
func productionRuntime(getenv func(string) string) bool {
	return strings.ToLower(strings.TrimSpace(getenv("NODE_ENV"))) == "production"
}

func loadRuntimeConfig(getenv func(string) string) (runtimeConfig, error) {
	cfg := runtimeConfig{}

	// ChatToolEnvironment keeps the explicit JUHE_AI_NODE_ENV override and
	// falls back to NODE_ENV when unset (the same variable the single
	// production signal reads, Node runtime.ts:532 nodeEnv); the
	// production/test/development validation stays further down.
	cfg.ChatToolEnvironment = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_NODE_ENV")))
	if cfg.ChatToolEnvironment == "" {
		cfg.ChatToolEnvironment = strings.ToLower(strings.TrimSpace(getenv("NODE_ENV")))
	}
	production := productionRuntime(getenv)
	if cfg.ChatToolEnvironment == "" {
		cfg.ChatToolEnvironment = "development"
	}

	performanceHints := hasAnyRawConfig(getenv,
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_CACHE_URL",
		"JUHE_AI_REDIS_STATE_URL")
	cfg.RuntimeMode = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_RUNTIME_MODE")))
	if cfg.RuntimeMode == "" {
		if performanceHints {
			cfg.RuntimeMode = "performance"
		} else {
			cfg.RuntimeMode = "standalone"
		}
	}
	if cfg.RuntimeMode != "standalone" && cfg.RuntimeMode != "performance" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_RUNTIME_MODE 必须为 standalone 或 performance: %q", cfg.RuntimeMode)
	}

	cfg.DatabaseDriver = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")))
	if cfg.DatabaseDriver == "" {
		if cfg.RuntimeMode == "performance" {
			cfg.DatabaseDriver = "postgres"
		} else {
			cfg.DatabaseDriver = "sqlite"
		}
	}
	if cfg.DatabaseDriver != "sqlite" && cfg.DatabaseDriver != "postgres" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres: %q", cfg.DatabaseDriver)
	}

	cfg.CacheDriver = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_CACHE_DRIVER")))
	if cfg.CacheDriver == "" {
		if cfg.RuntimeMode == "performance" {
			cfg.CacheDriver = "redis"
		} else {
			cfg.CacheDriver = "memory"
		}
	}
	if cfg.CacheDriver != "memory" && cfg.CacheDriver != "redis" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_CACHE_DRIVER 必须为 memory 或 redis: %q", cfg.CacheDriver)
	}

	cfg.RuntimeStateDriver = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_RUNTIME_STATE_DRIVER")))
	if cfg.RuntimeStateDriver == "" {
		if cfg.RuntimeMode == "performance" {
			cfg.RuntimeStateDriver = "redis"
		} else {
			cfg.RuntimeStateDriver = "memory"
		}
	}
	if cfg.RuntimeStateDriver != "memory" && cfg.RuntimeStateDriver != "redis" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_RUNTIME_STATE_DRIVER 必须为 memory 或 redis: %q", cfg.RuntimeStateDriver)
	}

	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	cfg.RedisCacheURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_CACHE_URL"))
	cfg.RedisStateURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	// JUHE_AI_QUEUE_DRIVER / JUHE_AI_REDIS_QUEUE_URL are deliberately not read
	// anywhere in the Go gateway or jobs projects (dead Node-era config): the
	// queue URL had no consumer and its performance-mode mandatory check only
	// forced deployments to configure a value that was never used.
	if cfg.CacheDriver == "redis" && cfg.RedisCacheURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_CACHE_DRIVER=redis 时缺少 JUHE_AI_REDIS_CACHE_URL")
	}
	if cfg.RuntimeStateDriver == "redis" && cfg.RedisStateURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_RUNTIME_STATE_DRIVER=redis 时缺少 JUHE_AI_REDIS_STATE_URL")
	}
	if cfg.DatabaseDriver == "postgres" && cfg.PostgresURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_DATABASE_DRIVER=postgres 时缺少 JUHE_AI_POSTGRES_URL")
	}

	cfg.Secret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
	// Node assertProductionSecret (runtime.ts:949-960): production refuses the
	// default development secret and any secret shorter than 32 characters.
	if production && (cfg.Secret == defaultRuntimeSecret || len(cfg.Secret) < minimumProductionSecretLength) {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_SECRET 在生产环境必须配置为至少 %d 位的稳定随机密钥，不能使用默认开发密钥或过短密钥", minimumProductionSecretLength)
	}
	cfg.RedisNamespace = strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE"))

	cfg.DatabasePath = strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH"))
	cfg.ChatDatabasePath = strings.TrimSpace(getenv("JUHE_AI_CHAT_DATABASE_PATH"))
	cfg.DatasetDatabasePath = strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH"))
	cfg.RuntimeLogDatabasePath = strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH"))
	cfg.UsageCatalogDatabasePath = strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH"))
	cfg.StatsDatabasePath = strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH"))
	cfg.TableMonitorDatabasePath = strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH"))
	cfg.CodexContextShardRoot = strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"))
	cfg.CodexContextShardCount = 16
	if raw := strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT")); raw != "" {
		shardCount, err := strconv.Atoi(raw)
		if err != nil || shardCount < 1 || shardCount > 256 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 必须在 1 到 256 之间: %q", raw)
		}
		cfg.CodexContextShardCount = shardCount
	}
	if cfg.DatabaseDriver == "sqlite" && cfg.DatabasePath == "" {
		return runtimeConfig{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_DATABASE_PATH")
	}

	cfg.OpenAICompatibleFilesRoot = strings.TrimSpace(getenv("JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT"))

	// Chat mount config (Node runtimeConfig.chat + chatAssetsRoot).
	cfg.ChatAssetsRoot = strings.TrimSpace(getenv("JUHE_AI_CHAT_ASSETS_ROOT"))
	if cfg.ChatAssetsRoot == "" {
		cfg.ChatAssetsRoot = "data/chat-assets"
	}
	cfg.ChatMaxTurnsPerConversation = 50
	if raw := strings.TrimSpace(getenv("JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION")); raw != "" {
		maxTurns, err := strconv.Atoi(raw)
		if err != nil || maxTurns < 1 || maxTurns > 1000 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION 必须在 1 到 1000 之间: %q", raw)
		}
		cfg.ChatMaxTurnsPerConversation = int64(maxTurns)
	}
	cfg.ChatRetentionDays = 3
	if raw := strings.TrimSpace(getenv("JUHE_AI_CHAT_RETENTION_DAYS")); raw != "" {
		retentionDays, err := strconv.Atoi(raw)
		if err != nil || retentionDays < 1 || retentionDays > 365 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 365 之间: %q", raw)
		}
		cfg.ChatRetentionDays = retentionDays
	}
	cfg.ChatDiagnosticToolEnabled = envBoolTrue(getenv("JUHE_AI_CHAT_DIAGNOSTIC_TOOL_ENABLED"))
	// ChatToolEnvironment was normalized at the top of this function
	// (JUHE_AI_NODE_ENV with the NODE_ENV fallback); only the value validation
	// stays here.
	if cfg.ChatToolEnvironment != "production" && cfg.ChatToolEnvironment != "test" && cfg.ChatToolEnvironment != "development" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_NODE_ENV（未设置时回落 NODE_ENV）必须是 production、test 或 development: %q", cfg.ChatToolEnvironment)
	}

	cfg.Host = strings.TrimSpace(getenv("JUHE_AI_HOST"))
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	cfg.Port = 3000
	if raw := strings.TrimSpace(getenv("JUHE_AI_PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_PORT 必须是 1-65535 的整数: %q", raw)
		}
		cfg.Port = port
	}

	// Node httpSecurityConfig (runtime.ts:1519-1557): the CORS allowlist and
	// the cookie secure default are production-aware (see the production
	// signal at the top of this function); an explicit env value keeps the
	// existing override precedence.
	var allowedOrigins []string
	{
		rawValue := strings.TrimSpace(getenv("JUHE_AI_ALLOWED_ORIGINS"))
		parts := strings.Split(rawValue, ",")
		trimmed := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				trimmed = append(trimmed, part)
			}
		}
		hasWildcard := false
		for _, part := range trimmed {
			if part == "*" {
				hasWildcard = true
				break
			}
		}
		seen := map[string]bool{}
		for _, part := range trimmed {
			if part == "*" {
				continue
			}
			origin, originErr := normalizeAllowedOrigin("JUHE_AI_ALLOWED_ORIGINS", part)
			if originErr != nil {
				return runtimeConfig{}, originErr
			}
			if !seen[origin] {
				seen[origin] = true
				allowedOrigins = append(allowedOrigins, origin)
			}
		}
		if hasWildcard {
			// Node allowedOriginsConfig: '*' keeps the local-dev reflection
			// contract outside production and is refused in production.
			if production {
				return runtimeConfig{}, fmt.Errorf("JUHE_AI_ALLOWED_ORIGINS 不允许配置 *；需要逐项填写完整后台前端 Origin")
			}
			allowedOrigins = nil
		} else if production && len(allowedOrigins) == 0 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_ALLOWED_ORIGINS 在生产环境必须显式配置后台前端 Origin，不能继续反射任意跨域来源")
		}
		cfg.CORSAllowedOrigins = allowedOrigins
		// Node runtime.ts:1525: allowAnyOrigin = !production && allowedOrigins.length === 0.
		cfg.CORSAllowAnyOrigin = !production && len(allowedOrigins) == 0
	}

	// Node strictBooleanConfig('JUHE_AI_COOKIE_SECURE', production)
	// (runtime.ts:1521,1575-1582): production defaults Secure on; an explicit
	// true/1/yes/on or false/0/no/off value overrides it and any other
	// non-empty value fails fast at startup.
	cookieSecure, secureErr := strictEnvBool("JUHE_AI_COOKIE_SECURE", getenv("JUHE_AI_COOKIE_SECURE"), production)
	if secureErr != nil {
		return runtimeConfig{}, secureErr
	}
	cfg.CookieSecure = cookieSecure
	cfg.CookieSameSite = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_COOKIE_SAME_SITE")))
	if cfg.CookieSameSite == "" {
		cfg.CookieSameSite = "lax"
	}
	if cfg.CookieSameSite != "lax" && cfg.CookieSameSite != "strict" && cfg.CookieSameSite != "none" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_COOKIE_SAME_SITE 必须为 lax、strict 或 none: %q", cfg.CookieSameSite)
	}
	if cfg.CookieSameSite == "none" && !cfg.CookieSecure {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_COOKIE_SAME_SITE=none 时必须启用 JUHE_AI_COOKIE_SECURE=true")
	}
	// Node trustProxyConfig (runtime.ts:1615-1630): empty keeps false;
	// true/yes/on、false/no/off（小写）按布尔处理；整数 0-16 是反向代理跳数；
	// 其余值启动即失败。D-211：Go 曾把非法值静默当 0 且无 16 上限。
	trustProxy, trustProxyErr := trustProxyConfig("JUHE_AI_TRUST_PROXY", getenv("JUHE_AI_TRUST_PROXY"))
	if trustProxyErr != nil {
		return runtimeConfig{}, trustProxyErr
	}
	cfg.TrustProxy = trustProxy

	cfg.CaptchaDisabled = envBoolTrue(getenv("JUHE_AI_AUTH_CAPTCHA_DISABLED"))
	cfg.DevAutoLoginUsername = strings.TrimSpace(getenv("JUHE_AI_DEV_AUTO_LOGIN_USERNAME"))
	// Node development.ts assertDevelopmentAutoLoginConfig: the development
	// auto-login must never be enabled under the production signal; the
	// non-production local isolated dev instance keeps working.
	if production && cfg.DevAutoLoginUsername != "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用")
	}
	// Node temporaryAccessIpAllowlistConfig (runtime.ts:969-979): each comma
	// entry strips the ::ffff: prefix and must be a single IPv4/IPv6 address
	// (no hostnames, CIDR or wildcards); duplicates are dropped. D-211：Go
	// 曾对非法项零校验直接透传。
	allowlist, allowlistErr := temporaryAccessIPAllowlistConfig("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST", getenv("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST"))
	if allowlistErr != nil {
		return runtimeConfig{}, allowlistErr
	}
	cfg.TemporaryAccessIPAllowlist = allowlist

	cfg.OIDCEnabled = envBoolTrue(getenv("JUHE_AI_OIDC_ENABLED"))
	cfg.OIDCIssuer = strings.TrimSpace(getenv("JUHE_AI_OIDC_ISSUER"))
	cfg.OIDCKeyEncryptionSecret = strings.TrimSpace(getenv("JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET"))
	if cfg.OIDCEnabled {
		if cfg.OIDCIssuer == "" {
			return runtimeConfig{}, fmt.Errorf("启用 JUHE_AI_OIDC_ENABLED 时必须显式配置 JUHE_AI_OIDC_ISSUER")
		}
		if cfg.OIDCKeyEncryptionSecret == "" {
			return runtimeConfig{}, fmt.Errorf("启用 JUHE_AI_OIDC_ENABLED 时必须显式配置 JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET")
		}
	}

	cfg.SystemAPIEnabled = envBoolTrue(getenv("JUHE_AI_GATEWAY_SYSTEM_API_ENABLED"))
	cfg.ChainEnabled = envBoolTrue(getenv("JUHE_AI_GATEWAY_CHAIN_ENABLED"))
	if cfg.ChainEnabled && !cfg.SystemAPIEnabled {
		return runtimeConfig{}, fmt.Errorf("启用 JUHE_AI_GATEWAY_CHAIN_ENABLED 时必须同时启用 JUHE_AI_GATEWAY_SYSTEM_API_ENABLED")
	}

	// Chain collaborator config (mirrors the Node runtime.ts audit + spool
	// fields the gateway chain reads).
	cfg.AuditLogEnabled = true
	if raw := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_ENABLED")); raw != "" {
		cfg.AuditLogEnabled = envBoolTrue(raw)
	}
	// D-209：JUHE_AI_USAGE_SPOOL_DIRECTORY 是现行名；高性能部署指南与
	// install-performance-topology.sh 仍使用旧名 JUHE_AI_USAGE_SPOOL_DIR，
	// DIRECTORY 未配置时回落旧名（DIRECTORY 优先，两处都配置时旧名静默失效
	// 与 Node 覆盖语义一致）。
	cfg.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIRECTORY"))
	if cfg.UsageSpoolDirectory == "" {
		cfg.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIR"))
	}
	// Node runtimeConfig.usageSpool（runtime.ts:661-666）四个容量旋钮：
	// numberConfig 边界与默认值逐一对齐；非数字或越界启动报错（D-209）。
	cfg.UsageSpoolMaxItems = 250_000
	if raw := strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_MAX_ITEMS")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_USAGE_SPOOL_MAX_ITEMS", raw, 1_000, 5_000_000)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.UsageSpoolMaxItems = parsed
	}
	cfg.UsageSpoolMaxBytes = 4_096 * 1024 * 1024
	if raw := strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_MAX_MB")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_USAGE_SPOOL_MAX_MB", raw, 64, 102_400)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.UsageSpoolMaxBytes = parsed * 1024 * 1024
	}
	cfg.UsageSpoolReplayBatchSize = 500
	if raw := strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE", raw, 1, 5_000)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.UsageSpoolReplayBatchSize = parsed
	}
	cfg.UsageSpoolReplayIntervalMs = 1_000
	if raw := strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS", raw, 100, 60_000)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.UsageSpoolReplayIntervalMs = parsed
	}
	// D-147：usage finalization 队列容量旋钮（Node
	// runtimeConfig.gateway.usageFinalizationMaxItems，runtime.ts:754，
	// integerConfig 默认 2048、范围 [1, 1_000_000]）。
	cfg.UsageFinalizationMaxItems = 2048
	if raw := strings.TrimSpace(getenv("JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS", raw, 1, 1_000_000)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.UsageFinalizationMaxItems = parsed
	}
	// D-192/D-146：上游 URL 安全配置（Node runtimeConfig.upstreamUrlSecurity，
	// runtime.ts:1679-1712）。allowPrivateBaseUrls 在生产信号下启动即失败；
	// allowlist 逐项必须为 http/https IP Origin 并归一化 origin key。
	upstreamSecurity, upstreamSecurityErr := upstreamURLSecurityConfig(production, getenv)
	if upstreamSecurityErr != nil {
		return runtimeConfig{}, upstreamSecurityErr
	}
	cfg.UpstreamURLSecurity = upstreamSecurity

	// 去跨进程战役第三刀：gateway 进程内自采样 Go 运行时指标并直接查库提供
	// go-runtime-trend（同名 JUHE_AI_GO_RUNTIME_METRICS_* env 家族，role 默认
	// gateway；原跨进程 metrics 代理 env 已删除）。默认关闭，关闭时采样器不
	// 装配、路由回空 items。
	goRuntimeMetrics, err := gometrics.LoadConfig(getenv, "gateway")
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("load Go runtime metrics config: %w", err)
	}
	cfg.GoRuntimeMetrics = goRuntimeMetrics
	// 健康检查派发 outbox 行的探针 deadline 窗口（原 jobs 侧发布器读取的同名
	// env；默认 65000、范围 [1000, 600000] 对齐 Node integerConfig。非法值按
	// 0 处理：outbox writer 保持 inert，派发显式 input_unavailable，不阻塞
	// 启动——与原 jobs 装配失败即 503 的降级语义一致）。
	cfg.AccountHealthProbeDeadlineMS = 65_000
	if raw := strings.TrimSpace(getenv("JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1_000 || parsed > 10*60_000 {
			slogOnceWarn("gateway.runtime.accountHealthProbeDeadline", "JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS 必须是 [1000, 600000] 内的整数，健康检查派发保持 input_unavailable")
			cfg.AccountHealthProbeDeadlineMS = 0
		} else {
			cfg.AccountHealthProbeDeadlineMS = parsed
		}
	}
	cfg.AccountHealthOutcomeSQLitePath = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH"))
	cfg.AccountHealthOutcomePostgresURL = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL"))
	cfg.ConcurrencyGlobalMax = 5000
	// Node integerConfig('JUHE_AI_CONCURRENCY_GLOBAL_MAX', 5_000, 1, 50_000)
	// （runtime.ts:410）：非整数/越界启动报错，上限 50000 对齐 integerConfig。
	if raw := strings.TrimSpace(getenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_CONCURRENCY_GLOBAL_MAX 必须配置为整数: %q", raw)
		}
		if parsed < 1 || parsed > 50000 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_CONCURRENCY_GLOBAL_MAX 必须在 1-50000 范围内: %d", parsed)
		}
		cfg.ConcurrencyGlobalMax = parsed
	}
	// Node：dispatchAccountCandidateLimit = integerConfig(
	// 'JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT', globalConcurrencyMax,
	// 1, 50_000)（runtime.ts:770）：缺省回落 globalMax，非整数/越界启动报错。
	cfg.DispatchAccountCandidateLimit = cfg.ConcurrencyGlobalMax
	if raw := strings.TrimSpace(getenv("JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须配置为整数: %q", raw)
		}
		if parsed < 1 || parsed > 50000 {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须在 1-50000 范围内: %d", parsed)
		}
		cfg.DispatchAccountCandidateLimit = parsed
	}
	cfg.FrontendDistPath = strings.TrimSpace(getenv("JUHE_AI_FRONTEND_DIST_PATH"))

	// Runtime-logs grep surface: Node numberConfig (runtime.ts:891-892,
	// 'JUHE_AI_LOG_MAX_FILES', 500, 1, 500 / 'JUHE_AI_LOG_RETENTION_DAYS',
	// 30, 1, 30) fails fast on a non-number or out-of-range value instead of
	// clamping it silently; the value truncates before the range check
	// (Math.trunc, runtime.ts:1391), so "10.5" passes as 10. Defaults stay
	// 500/30.
	cfg.LogDir = strings.TrimSpace(getenv("JUHE_AI_LOG_DIR"))
	cfg.LogFileEnabled = true
	if raw := strings.TrimSpace(getenv("JUHE_AI_LOG_FILE_ENABLED")); raw != "" {
		cfg.LogFileEnabled = envBoolTrue(raw)
	}
	cfg.LogMaxFiles = 500
	if raw := strings.TrimSpace(getenv("JUHE_AI_LOG_MAX_FILES")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_LOG_MAX_FILES", raw, 1, 500)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.LogMaxFiles = parsed
	}
	cfg.LogRetentionDays = 30
	if raw := strings.TrimSpace(getenv("JUHE_AI_LOG_RETENTION_DAYS")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_LOG_RETENTION_DAYS", raw, 1, 30)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.LogRetentionDays = parsed
	}

	cfg.BusinessOwner = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_BUSINESS_OWNER")))
	cfg.BusinessDatabasePath = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_DATABASE_PATH"))
	cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_POSTGRES_URL"))
	cfg.BusinessHandoffConfirmed = envBoolTrue(getenv("JUHE_AI_BUSINESS_HANDOFF_CONFIRMED"))
	cfg.BusinessNodeWriterStopped = envBoolTrue(getenv("JUHE_AI_BUSINESS_NODE_WRITER_STOPPED"))
	cfg.BusinessSchemaReady = envBoolTrue(getenv("JUHE_AI_BUSINESS_SCHEMA_READY"))
	cfg.BusinessOwnerEpoch = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_OWNER_EPOCH"))
	cfg.BusinessCutoverEvidencePath = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH"))

	return cfg, nil
}

// businessOwnerGate validates the business database owner handoff the same way
// the J3b owner contract does (modelcheckowner.LoadConfig): an enabled system
// api composition must prove it owns the business database before any store is
// opened; otherwise the process fails closed.
func (c *runtimeConfig) businessOwnerGate() error {
	if !c.SystemAPIEnabled {
		return nil
	}
	if c.BusinessOwner != "gateway" {
		return fmt.Errorf("启用系统 API 组合根时 JUHE_AI_BUSINESS_OWNER 必须为 gateway")
	}
	if !c.BusinessHandoffConfirmed {
		return fmt.Errorf("Business owner handoff 未确认（JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true），必须保持关闭")
	}
	if !c.BusinessNodeWriterStopped {
		return fmt.Errorf("Business owner handoff 已确认但 Node writer 未停止（JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true），必须保持关闭")
	}
	if !c.BusinessSchemaReady {
		return fmt.Errorf("Business schema readiness 未确认（JUHE_AI_BUSINESS_SCHEMA_READY=true），必须保持关闭")
	}
	if c.BusinessOwnerEpoch == "" {
		return fmt.Errorf("Business owner handoff 已确认但 JUHE_AI_BUSINESS_OWNER_EPOCH 未提供，必须保持关闭")
	}
	if c.BusinessCutoverEvidencePath == "" {
		return fmt.Errorf("Business owner handoff 已确认但 JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH 未提供，必须保持关闭")
	}
	if c.DatabaseDriver == "postgres" {
		if c.BusinessPostgresURL == "" {
			return fmt.Errorf("postgres 模式缺少 JUHE_AI_BUSINESS_POSTGRES_URL")
		}
	} else if c.BusinessDatabasePath == "" {
		return fmt.Errorf("sqlite 模式缺少 JUHE_AI_BUSINESS_DATABASE_PATH")
	}
	return nil
}

// normalizeAllowedOrigin mirrors Node normalizeAllowedOrigin
// (runtime.ts:1559-1573): a configured entry must be a bare http/https origin
// — no path, query, fragment or userinfo — and canonicalizes to the
// lowercased scheme/host with the default port dropped (Node URL.origin).
func normalizeAllowedOrigin(name, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%s 包含无效 Origin：%s", name, value)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s 只允许 http 或 https Origin：%s", name, value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%s 只能填写 Origin，不要包含路径、查询、片段或用户名密码：%s", name, value)
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	return scheme + "://" + host, nil
}

// corsPolicy projects the parsed JUHE_AI_ALLOWED_ORIGINS slice onto the
// kernel CORSPolicy consumed by the management-surface CORS middleware.
func (c *runtimeConfig) corsPolicy() kernel.CORSPolicy {
	return kernel.CORSPolicy{AllowAnyOrigin: c.CORSAllowAnyOrigin, AllowedOrigins: c.CORSAllowedOrigins}
}

// trustProxyConfig mirrors Node trustProxyConfig (runtime.ts:1615-1630):
// empty keeps the disabled default; true/yes/on and false/no/off are the
// boolean spellings; a non-negative integer up to 16 is the reverse-proxy
// hop count; anything else fails the startup (D-211).
func trustProxyConfig(name, raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	switch value {
	case "true", "yes", "on":
		return "true", nil
	case "false", "no", "off":
		return "false", nil
	}
	numeric, err := strconv.Atoi(value)
	if err != nil || numeric < 0 || numeric > 16 {
		return "", fmt.Errorf("%s 只能配置为 true/false 或 0-16 的反向代理跳数: %q", name, raw)
	}
	return value, nil
}

// temporaryAccessIPAllowlistConfig mirrors Node temporaryAccessIpAllowlist
// config (runtime.ts:969-979): comma-separated single IPv4/IPv6 addresses
// only (the ::ffff: prefix is stripped, duplicates dropped); any other entry
// fails the startup (D-211).
func temporaryAccessIPAllowlistConfig(name, raw string) ([]string, error) {
	allowlist := make([]string, 0)
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		normalized := strings.TrimPrefix(trimmed, "::ffff:")
		normalized = strings.TrimPrefix(normalized, "::FFFF:")
		if net.ParseIP(normalized) == nil {
			return nil, fmt.Errorf("%s 只能填写逗号分隔的单个 IPv4 或 IPv6 地址，不支持域名、CIDR 或通配符: %q", name, trimmed)
		}
		if !seen[normalized] {
			seen[normalized] = true
			allowlist = append(allowlist, normalized)
		}
	}
	return allowlist, nil
}

// upstreamURLSecurityConfig mirrors Node upstreamUrlSecurityConfig
// (runtime.ts:1679-1712): JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS is a
// strict boolean refused under the production signal; the private origin
// allowlist accepts only http/https IP origins and keeps the normalized
// origin keys (D-192/D-146).
func upstreamURLSecurityConfig(production bool, getenv func(string) string) (UpstreamURLSecurityConfig, error) {
	config := UpstreamURLSecurityConfig{PrivateOriginAllowlist: map[string]bool{}}
	allowPrivate, err := strictEnvBool("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS", getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS"), false)
	if err != nil {
		return config, err
	}
	if production && allowPrivate {
		return config, fmt.Errorf("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS 只能用于本地开发或回归测试，生产环境不能启用")
	}
	config.AllowPrivateBaseUrls = allowPrivate
	for _, part := range strings.Split(getenv("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST"), ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		originKey, originErr := sharedupstreamhttp.NormalizePrivateUpstreamOrigin("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST", trimmed)
		if originErr != nil {
			return config, originErr
		}
		config.PrivateOriginAllowlist[originKey] = true
	}
	return config, nil
}
