package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// runtimeConfig mirrors the env conventions of the Node composition root
// (backend/src/config/runtime.ts) for the scope the Go gateway composition
// consumes: runtime mode, storage drivers (sqlite/postgres, memory/redis,
// memory/redis_stream), dual-mode database paths, Redis URLs + namespace,
// secret, cookie/oidc/trust-proxy HTTP security and the composition gates.
//
// Validation mirrors the Node fail-fast contract: an enabled redis/queue
// driver without its URL, an enabled OIDC without issuer/secret, a
// none-cookie without secure, or an enabled composition without the business
// owner handoff gates exits at startup instead of serving a partial owner.
type runtimeConfig struct {
	RuntimeMode        string // "standalone" | "performance"
	DatabaseDriver     string // "sqlite" | "postgres"
	CacheDriver        string // "memory" | "redis"
	RuntimeStateDriver string // "memory" | "redis"
	QueueDriver        string // "memory" | "redis_stream"

	PostgresURL    string
	RedisCacheURL  string
	RedisStateURL  string
	RedisQueueURL  string
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
	// runtimeConfig.auditLog.enabled, JUHE_AI_AUDIT_LOG_ENABLED default true),
	// the F3 loopback audit input server base URL (derived from
	// JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS) and the durable usage-record
	// spool directory (JUHE_AI_USAGE_SPOOL_DIRECTORY).
	AuditLogEnabled     bool
	AuditInputURL       string
	UsageSpoolDirectory string

	// Read-face collaborators (X04 404 项补齐):
	// GoRuntimeMetricsURL is the loopback origin of the Go jobs metrics
	// server that the /stats/system-metrics/go-runtime-trend route proxies
	// (Node JUHE_AI_GO_RUNTIME_METRICS_URL, default http://127.0.0.1:3305).
	GoRuntimeMetricsURL string
	// AccountHealthOutcomeSQLitePath is the J1 jobs outcome store the ai-health
	// reads merge (Node JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH).
	AccountHealthOutcomeSQLitePath string
	// AccountHealthOutcomePostgresURL is the performance-topology J1 outcome
	// database the ai-health reads merge (jobs JUHE_AI_JOBS_OUTCOME_POSTGRES_URL
	// counterpart; Node merged readPostgresOutcomesForAccounts from the same DB).
	AccountHealthOutcomePostgresURL string
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

func loadRuntimeConfig(getenv func(string) string) (runtimeConfig, error) {
	cfg := runtimeConfig{}

	performanceHints := hasAnyRawConfig(getenv,
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_CACHE_URL",
		"JUHE_AI_REDIS_STATE_URL",
		"JUHE_AI_REDIS_QUEUE_URL")
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

	cfg.QueueDriver = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_QUEUE_DRIVER")))
	if cfg.QueueDriver == "" {
		if cfg.RuntimeMode == "performance" {
			cfg.QueueDriver = "redis_stream"
		} else {
			cfg.QueueDriver = "memory"
		}
	}
	if cfg.QueueDriver != "memory" && cfg.QueueDriver != "redis_stream" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_QUEUE_DRIVER 必须为 memory 或 redis_stream: %q", cfg.QueueDriver)
	}

	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	cfg.RedisCacheURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_CACHE_URL"))
	cfg.RedisStateURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	cfg.RedisQueueURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_QUEUE_URL"))
	if cfg.CacheDriver == "redis" && cfg.RedisCacheURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_CACHE_DRIVER=redis 时缺少 JUHE_AI_REDIS_CACHE_URL")
	}
	if cfg.RuntimeStateDriver == "redis" && cfg.RedisStateURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_RUNTIME_STATE_DRIVER=redis 时缺少 JUHE_AI_REDIS_STATE_URL")
	}
	if cfg.QueueDriver == "redis_stream" && cfg.RedisQueueURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_QUEUE_DRIVER=redis_stream 时缺少 JUHE_AI_REDIS_QUEUE_URL")
	}
	if cfg.DatabaseDriver == "postgres" && cfg.PostgresURL == "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_DATABASE_DRIVER=postgres 时缺少 JUHE_AI_POSTGRES_URL")
	}

	cfg.Secret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
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
	cfg.ChatToolEnvironment = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_NODE_ENV")))
	if cfg.ChatToolEnvironment == "" {
		cfg.ChatToolEnvironment = "development"
	}
	if cfg.ChatToolEnvironment != "production" && cfg.ChatToolEnvironment != "test" && cfg.ChatToolEnvironment != "development" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_NODE_ENV 必须是 production、test 或 development: %q", cfg.ChatToolEnvironment)
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

	cfg.CookieSecure = envBoolTrue(getenv("JUHE_AI_COOKIE_SECURE"))
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
	cfg.TrustProxy = strings.TrimSpace(getenv("JUHE_AI_TRUST_PROXY"))

	cfg.CaptchaDisabled = envBoolTrue(getenv("JUHE_AI_AUTH_CAPTCHA_DISABLED"))
	cfg.DevAutoLoginUsername = strings.TrimSpace(getenv("JUHE_AI_DEV_AUTO_LOGIN_USERNAME"))
	cfg.TemporaryAccessIPAllowlist = commaList(getenv("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST"))

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
	auditInputAddress := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS"))
	if auditInputAddress != "" {
		host, port, err := net.SplitHostPort(auditInputAddress)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS 必须是 host:port: %q", auditInputAddress)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		cfg.AuditInputURL = "http://" + net.JoinHostPort(host, port)
	}
	cfg.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIRECTORY"))

	cfg.GoRuntimeMetricsURL = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_URL"))
	if cfg.GoRuntimeMetricsURL == "" {
		cfg.GoRuntimeMetricsURL = "http://127.0.0.1:3305"
	}
	cfg.AccountHealthOutcomeSQLitePath = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH"))
	cfg.AccountHealthOutcomePostgresURL = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL"))
	cfg.FrontendDistPath = strings.TrimSpace(getenv("JUHE_AI_FRONTEND_DIST_PATH"))

	// Runtime-logs grep surface: Node numberConfig clamps instead of failing.
	cfg.LogDir = strings.TrimSpace(getenv("JUHE_AI_LOG_DIR"))
	cfg.LogFileEnabled = true
	if raw := strings.TrimSpace(getenv("JUHE_AI_LOG_FILE_ENABLED")); raw != "" {
		cfg.LogFileEnabled = envBoolTrue(raw)
	}
	cfg.LogMaxFiles = 500
	if parsed, err := strconv.Atoi(strings.TrimSpace(getenv("JUHE_AI_LOG_MAX_FILES"))); err == nil {
		cfg.LogMaxFiles = min(max(parsed, 1), 500)
	}
	cfg.LogRetentionDays = 30
	if parsed, err := strconv.Atoi(strings.TrimSpace(getenv("JUHE_AI_LOG_RETENTION_DAYS"))); err == nil {
		cfg.LogRetentionDays = min(max(parsed, 1), 30)
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
