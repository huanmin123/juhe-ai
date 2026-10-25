package main

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
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
// consumes: PostgreSQL + Redis URLs + namespace, secret, cookie/cors/
// trust-proxy HTTP security, the local file-face derivations (usage spool,
// codex-context segments, logs, chat-assets) and the composition gates.
//
// Validation mirrors the Node fail-fast contract: missing PostgreSQL/Redis
// URLs, a none-cookie without secure, a production process without a strong
// secret or an explicit CORS origin allowlist, or an enabled composition
// without the business owner connection gates exits at startup instead of
// serving a partial owner.
//
// The production signal for every HTTP security gate is the normalized
// NODE_ENV (Node isProductionRuntime, runtime.ts:979-980); the same-process
// operationlog/config.go and auditlog/config.go compositions read
// NODE_ENV too. JUHE_AI_NODE_ENV only overrides the chat tool environment
// (falling back to NODE_ENV when unset) and carries no production semantics;
// ownermode and JUHE_AI_DEPLOY_MODE carry none either.
//
// 2026-10-24 存储形态收口（PLAN-20261024T051925945Z）：sqlite + memory 本地
// 模式彻底退役，PostgreSQL + Redis 是唯一存储形态——JUHE_AI_RUNTIME_MODE /
// JUHE_AI_DATABASE_DRIVER / JUHE_AI_CACHE_DRIVER / JUHE_AI_RUNTIME_STATE_DRIVER
// 四开关与全部 sqlite 库路径 env 随之删除，三个连接串 env
// （JUHE_AI_POSTGRES_URL / JUHE_AI_REDIS_CACHE_URL / JUHE_AI_REDIS_STATE_URL）
// 无条件必填（缺失启动即失败）。普通文件目录派生保留：usage-record-spool、
// codex-context segments、logs、chat-assets 等仍按 internal/datadir 固定名表
// 派生到 JUHE_AI_DATA_DIR（缺省 ./data）下；非生产未配置 JUHE_AI_SECRET 时
// 回退内置开发密钥。
type runtimeConfig struct {
	PostgresURL    string
	RedisCacheURL  string
	RedisStateURL  string
	RedisNamespace string
	Secret         string

	// CodexContextRoot is the codex-context segments root (Node
	// runtimeConfig.codexContextRoot, JUHE_AI_CODEX_CONTEXT_ROOT, default
	// <DATA_DIR>/codex-context): the Responses↔Chat bridge SegmentStore root
	// on the gateway and the retention segments cleanup root on jobs — both
	// processes must derive the same directory.
	CodexContextRoot string

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

	// MustChangePasswordGateDisabled mirrors
	// JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED: dev-only bypass of the
	// seeded initial-password gate (BUG-0224). Production refuses to start
	// with the switch on, same fence as DevAutoLoginUsername.
	MustChangePasswordGateDisabled bool

	// SystemAPIEnabled gates the Go system-api composition
	// (/__aisys__/api + /__aipublic__). 2026-09-19 起
	// 默认开启（开源零配置可启动）：未配置时视为 true，显式 false 才关闭；
	// 非法值启动即失败（strictEnvBool）。
	SystemAPIEnabled bool
	// ChainEnabled gates the AI gateway /v1 composition. When enabled the
	// startup assembles the full serving chain; when disabled /v1 traffic
	// answers the kernel 404 JSON contract (X01: the legacy bridge proxy was
	// deleted together with the archived Node origin). 2026-09-19 起默认开启，
	// 2026-09-21 起 chain 与 system API 均恒开（开关移除），原两者联动
	// 校验随之消失。
	ChainEnabled bool

	// Chain collaborator config: the audit capture fact (Node
	// runtimeConfig.auditLog.enabled；2026-09-21 起 JUHE_AI_AUDIT_LOG_ENABLED
	// 开关移除，捕获恒开)
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
	// RuntimeInvalidationSyncMinIntervalMs 合并 runtime 读前跨实例失效版本
	// 同步（JUHE_AI_RUNTIME_INVALIDATION_SYNC_MIN_INTERVAL_MS，默认 100，
	// [0, 60_000]；0 恢复逐读同步契约）。单实例部署无感知：自身失效在发布时
	// 已采纳到本地计数，只有跨实例传播观察窗口。
	RuntimeInvalidationSyncMinIntervalMs int

	// UpstreamURLSecurity mirrors Node runtimeConfig.upstreamUrlSecurity
	// (runtime.ts:1679-1690): JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS
	// (production refuses it) and the normalized IP-origin keys of
	// JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST. D-192/D-146：请求期
	// DNS resolve-all + 钉扎策略的组合根配置。
	UpstreamURLSecurity UpstreamURLSecurityConfig

	// AccountHealthProbeDeadlineMS is the probe deadline window the gateway
	// writes into every account_health_probe_request_outbox row
	// (JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS, the same env
	// the old jobs-side publisher read: Node integerConfig default 65000,
	// range [1000, 600000]). An invalid value keeps the outbox writer inert
	// (0 → dispatches report input_unavailable), matching the old
	// jobs-side missing-assembly semantics.
	AccountHealthProbeDeadlineMS int64
	// AccountHealthOutcomePostgresURL is the J1 outcome
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

	// CircuitSuppressionDelayLadderMs is the R4 test injection point for the
	// local account suppression backoff ladder
	// (gatewaycircuit.LocalSuppressionStoreOptions.DelayLadderMs). nil keeps
	// the production ladder (3000/5000/10000ms) — default behaviour is
	// production behaviour. Deliberately not env-backed: tests assign the
	// field directly on a constructed cfg; operators have no reason to move a
	// production availability contract.
	CircuitSuppressionDelayLadderMs []int64

	// UpstreamRetryBackoffDelaysMs is the R4 test injection point for the
	// dispatch attempt-loop backoff caps
	// (gatewaydispatch.EngineConfig.UpstreamRetryBackoffDelaysMs：capacity
	// queued 1000ms / capacity plain 500ms / recoverable wait 3000ms 的语义位
	// 数组). nil keeps the production hardcoded caps — default behaviour is
	// production behaviour. Deliberately not env-backed: tests assign the
	// field directly on a constructed cfg; operators have no reason to move a
	// production availability retry cadence.
	UpstreamRetryBackoffDelaysMs []int64

	// Runtime-logs grep surface (X04 404 项补齐): Node runtimeConfig.log
	// fields the grep family reads (JUHE_AI_LOG_DIR / JUHE_AI_LOG_MAX_FILES /
	// JUHE_AI_LOG_RETENTION_DAYS；JUHE_AI_LOG_FILE_ENABLED 开关已于 2026-09-21
	// 移除，文件日志恒开). The gateway only scans these files; an empty
	// directory simply yields no matches.
	LogDir           string
	LogFileEnabled   bool
	LogMaxFiles      int
	LogRetentionDays int
	// LogMaxFileMB 是 gateway 自身文件日志写侧的大小轮转阈值（MB）。恢复
	// Node 既有 env 契约（archived config/runtime.ts numberConfig
	// JUHE_AI_LOG_MAX_FILE_MB：默认 100，1..1024），Go 侧同名同默认同范围，
	// 越界 fail-fast。
	LogMaxFileMB int

	// Business owner connection gates for the business database this
	// composition owns. Names mirror the J3b owner contract (modelcheckowner).
	// 2026-10-24 存储形态收口：owner 事实一律按零配置自动认领（PostgreSQL 是
	// 唯一存储形态，独立凭据仍可显式配置 BUSINESS_POSTGRES_URL，空值回落主
	// JUHE_AI_POSTGRES_URL）。
	BusinessOwner             string
	BusinessPostgresURL       string
	BusinessHandoffConfirmed  bool
	BusinessNodeWriterStopped bool
	BusinessSchemaReady       bool
	BusinessOwnerEpoch        string
}

// UpstreamURLSecurityConfig mirrors Node runtimeConfig.upstreamUrlSecurity.
// It aliases the shared transport type so the composition can hand the
// loaded config straight to the dispatch URL policy.
type UpstreamURLSecurityConfig = sharedupstreamhttp.URLSecurityConfig

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

	// 2026-10-24 存储形态收口：JUHE_AI_RUNTIME_MODE / JUHE_AI_DATABASE_DRIVER /
	// JUHE_AI_CACHE_DRIVER / JUHE_AI_RUNTIME_STATE_DRIVER 随 sqlite+memory 模式
	// 退役删除；PostgreSQL + Redis 是唯一存储形态，三个连接串无条件必填
	// （缺失启动即失败，保留原 fail-fast 文案口径）。
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	cfg.RedisCacheURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_CACHE_URL"))
	cfg.RedisStateURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	// JUHE_AI_QUEUE_DRIVER / JUHE_AI_REDIS_QUEUE_URL are deliberately not read
	// anywhere in the Go gateway or jobs projects (dead Node-era config): the
	// queue URL had no consumer and its performance-mode mandatory check only
	// forced deployments to configure a value that was never used.
	if cfg.RedisCacheURL == "" {
		return runtimeConfig{}, fmt.Errorf("缺少 JUHE_AI_REDIS_CACHE_URL（PostgreSQL + Redis 是唯一存储形态，三个连接串均为必填）")
	}
	if cfg.RedisStateURL == "" {
		return runtimeConfig{}, fmt.Errorf("缺少 JUHE_AI_REDIS_STATE_URL（PostgreSQL + Redis 是唯一存储形态，三个连接串均为必填）")
	}
	if cfg.PostgresURL == "" {
		return runtimeConfig{}, fmt.Errorf("缺少 JUHE_AI_POSTGRES_URL（PostgreSQL + Redis 是唯一存储形态，三个连接串均为必填）")
	}

	cfg.Secret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
	// Node assertProductionSecret (runtime.ts:949-960): production refuses the
	// default development secret and any secret shorter than 32 characters.
	if production && (cfg.Secret == defaultRuntimeSecret || len(cfg.Secret) < minimumProductionSecretLength) {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_SECRET 在生产环境必须配置为至少 %d 位的稳定随机密钥，不能使用默认开发密钥或过短密钥", minimumProductionSecretLength)
	}
	// 2026-09-19 零配置回退（Node runtime.ts:399-400 原语义）：非生产未配置
	// JUHE_AI_SECRET 时回落内置开发密钥——apikeys/accounts/oauth 组合根的空
	// 密钥守卫会让空 env 启动失败，零配置 standalone 必须有可用密钥。生产
	// 分支已在上方拒绝空/短/默认密钥，不受影响。
	if cfg.Secret == "" {
		cfg.Secret = defaultRuntimeSecret
	}
	// 2026-09-22 namespace canonical 化：加载层统一剥除 `juhe-ai:` 根前缀，
	// 全前缀配置（如 `juhe-ai:dev`）与短名（`dev`）落同一短形式，避免下游
	// 直接拼接型实现（circuit_runtime/revision.go 等）产生
	// `juhe-ai:juhe-ai:dev:...` 键空间分裂，也与 key_model_runtime 的双格式
	// 去重型输入收敛一致。短名输入逐字节不变。
	cfg.RedisNamespace = rediscfg.CanonicalRedisNamespace(getenv("JUHE_AI_REDIS_NAMESPACE"))

	// 路径类 env 派生（普通文件目录面，PG+Redis 形态仍消费；internal/datadir
	// 固定名表）：usage-record-spool（D1，gateway↔jobs 用量交接）、codex-context
	// segments 根、chat-assets、logs 均落 <JUHE_AI_DATA_DIR=./data>/<固定名>。
	// sqlite 专库文件族路径派生已随存储形态退役删除。
	dataDir := datadir.Dir(getenv)
	// Codex Context segments 根（JUHE_AI_CODEX_CONTEXT_ROOT，缺省
	// <DATA_DIR>/codex-context）：gateway Responses↔Chat 桥 SegmentStore 与
	// jobs retention segments 清理的同源根，两侧派生规则必须一致。
	cfg.CodexContextRoot = datadir.Path(getenv, dataDir, "JUHE_AI_CODEX_CONTEXT_ROOT", datadir.CodexContextRoot)

	cfg.OpenAICompatibleFilesRoot = strings.TrimSpace(getenv("JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT"))

	// Chat mount config (Node runtimeConfig.chat + chatAssetsRoot).
	cfg.ChatAssetsRoot = strings.TrimSpace(getenv("JUHE_AI_CHAT_ASSETS_ROOT"))
	if cfg.ChatAssetsRoot == "" {
		cfg.ChatAssetsRoot = filepath.Join(dataDir, datadir.ChatAssetsDirectory)
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
	// 2026-09-21 起对话内诊断工具为常驻能力，JUHE_AI_CHAT_DIAGNOSTIC_TOOL_ENABLED 开关移除。
	cfg.ChatDiagnosticToolEnabled = true
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

	// Node strictBooleanConfig('JUHE_AI_AUTH_CAPTCHA_DISABLED')：true/1/yes/on
	// 关闭验证码、false/0/no/off 保持开启（大小写不敏感），其他非空值启动即
	// 失败。管理面（compose.go authDeps）与 J3b 面（main.go 装配 captcha
	// 服务）都必须只消费本字段，不得各自解析同一 env——否则 `1` 这类布尔
	// 语法只在其中一面生效（布尔语法漂移）。
	captchaDisabled, captchaDisabledErr := strictEnvBool("JUHE_AI_AUTH_CAPTCHA_DISABLED", getenv("JUHE_AI_AUTH_CAPTCHA_DISABLED"), false)
	if captchaDisabledErr != nil {
		return runtimeConfig{}, captchaDisabledErr
	}
	cfg.CaptchaDisabled = captchaDisabled
	cfg.DevAutoLoginUsername = strings.TrimSpace(getenv("JUHE_AI_DEV_AUTO_LOGIN_USERNAME"))
	// Node development.ts assertDevelopmentAutoLoginConfig: the development
	// auto-login must never be enabled under the production signal; the
	// non-production local isolated dev instance keeps working.
	if production && cfg.DevAutoLoginUsername != "" {
		return runtimeConfig{}, fmt.Errorf("JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用")
	}
	// JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED（2026-10-22，dev 联调）：
	// 首登强制改密门禁（BUG-0224）的默认值按环境翻转——非生产默认旁路（本地
	// 单人开发不被强制改密打断），生产默认硬门禁。旁路语义：Gateway 管理面与
	// J3b 自助面在 actor/credentials 构造处把 must_change_password 归一为
	// false，auth/me、session 403 gate 与登录面随之全部放行；业务库中的
	// must_change_password 存储位不写，显式 false 或生产即恢复门禁。非生产
	// 显式开启/关闭均可；生产显式 true 启动失败（与 dev 自动登录同一非生产
	// 约束），生产默认恒为门禁生效。
	gateRaw := strings.TrimSpace(getenv("JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED"))
	if gateRaw != "" {
		gateDisabled, gateDisabledErr := strictEnvBool("JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED", gateRaw, false)
		if gateDisabledErr != nil {
			return runtimeConfig{}, gateDisabledErr
		}
		if production && gateDisabled {
			return runtimeConfig{}, fmt.Errorf("JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED 不能在 NODE_ENV=production 时启用")
		}
		cfg.MustChangePasswordGateDisabled = gateDisabled
	} else {
		cfg.MustChangePasswordGateDisabled = !production
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

	// 2026-09-19 产品决策：system-api 与 /v1 网关链默认开启（开源零配置可
	// 启动；生产切流用显式配置控制）。strictEnvBool 保留非法值 fail-fast 与
	// 显式 false/0/no/off 关闭语义；联动校验（chain 开须 system 开）保留，
	// 默认双 true 自洽，显式 chain=true+system=false 仍被拒。
	// 2026-09-21 起系统 API 与 /v1 网关链为常驻能力，JUHE_AI_GATEWAY_SYSTEM_API_ENABLED
	// 与 JUHE_AI_GATEWAY_CHAIN_ENABLED 开关移除（原 strictEnvBool 非法值
	// fail-fast 与联动校验随之失效）。
	cfg.SystemAPIEnabled = true
	cfg.ChainEnabled = true

	// Chain collaborator config (mirrors the Node runtime.ts audit + spool
	// fields the gateway chain reads). 2026-09-21 起链路审计为常驻能力，
	// JUHE_AI_AUDIT_LOG_ENABLED 开关移除。
	cfg.AuditLogEnabled = true
	// D-209：JUHE_AI_USAGE_SPOOL_DIRECTORY 是现行名；高性能部署指南与
	// install-performance-topology.sh 仍使用旧名 JUHE_AI_USAGE_SPOOL_DIR，
	// DIRECTORY 未配置时回落旧名（DIRECTORY 优先，两处都配置时旧名静默失效
	// 与 Node 覆盖语义一致）。
	cfg.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIRECTORY"))
	if cfg.UsageSpoolDirectory == "" {
		cfg.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIR"))
	}
	// D1（PLAN-20261024T051925945Z）：spool 目录派生改挂 JUHE_AI_DATA_DIR
	// （缺省 <cwd>/data），不再以已退役的 JUHE_AI_STATS_DATABASE_PATH 目录为
	// 派生根。PG 零配置派生路径不变（<DATA_DIR>/usage-record-spool——stats
	// 库路径时代即派生到同一 DATA_DIR 下），gateway↔jobs 交接链不受影响；
	// 显式 JUHE_AI_USAGE_SPOOL_DIRECTORY（含旧名 DIR 回落）覆盖语义保留。
	if cfg.UsageSpoolDirectory == "" {
		cfg.UsageSpoolDirectory = filepath.Join(dataDir, datadir.UsageRecordSpoolDirectory)
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
	// 2026-09-18 热路径加固：runtime 读前跨实例失效同步合并窗口（redis
	// 运行态驱动下每请求两次 GET → 每窗口至多一次）。默认 100ms；0 恢复
	// 逐读同步契约。
	cfg.RuntimeInvalidationSyncMinIntervalMs = 100
	if raw := strings.TrimSpace(getenv("JUHE_AI_RUNTIME_INVALIDATION_SYNC_MIN_INTERVAL_MS")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_RUNTIME_INVALIDATION_SYNC_MIN_INTERVAL_MS", raw, 0, 60_000)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.RuntimeInvalidationSyncMinIntervalMs = parsed
	}
	// D-192/D-146：上游 URL 安全配置（Node runtimeConfig.upstreamUrlSecurity，
	// runtime.ts:1679-1712）。allowPrivateBaseUrls 在生产信号下启动即失败；
	// allowlist 逐项必须为 http/https IP Origin 并归一化 origin key。
	upstreamSecurity, upstreamSecurityErr := upstreamURLSecurityConfig(production, getenv)
	if upstreamSecurityErr != nil {
		return runtimeConfig{}, upstreamSecurityErr
	}
	cfg.UpstreamURLSecurity = upstreamSecurity

	// 健康检查派发 outbox 行的探针 deadline 窗口（原 jobs 侧发布器读取的同名
	// env；范围 [1000, 600000] 对齐 Node integerConfig。默认原为 65000（Node
	// 60s 阶梯 + 5s 余量）；2026-10-07 诊断阶梯统一上调为 20s/30s/40s（90s
	// 预算）后同步调为 95000。非法值按 0 处理：outbox writer 保持 inert，派
	// 发显式 input_unavailable，不阻塞启动——与原 jobs 装配失败即 503 的降
	// 级语义一致）。
	cfg.AccountHealthProbeDeadlineMS = 95_000
	if raw := strings.TrimSpace(getenv("JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1_000 || parsed > 10*60_000 {
			slogOnceWarn("gateway.runtime.accountHealthProbeDeadline", "JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS 必须是 [1000, 600000] 内的整数，健康检查派发保持 input_unavailable")
			cfg.AccountHealthProbeDeadlineMS = 0
		} else {
			cfg.AccountHealthProbeDeadlineMS = parsed
		}
	}
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
	// 2026-09-28 默认开启整改（对齐 jobs runtimelog 的派生哲学）：
	// JUHE_AI_LOG_DIR 未配置时派生 <DATA_DIR>/logs（固定名见 internal/datadir，
	// 与 jobs 侧派生对称，零配置部署下 gateway 写侧/grep 面与 jobs 索引器/
	// 保留清理共享同一目录）；显式配置路径优先；"disabled" 字面量（大小写
	// 不敏感）= 显式关闭文件日志写侧与 grep 面，LogDir 保持空。目录由写侧
	// NewFileSink 代建（BUG-0195），无目录派生副作用。
	cfg.LogDir = datadir.Path(getenv, dataDir, "JUHE_AI_LOG_DIR", datadir.RuntimeLogsDirectory)
	if strings.EqualFold(cfg.LogDir, "disabled") {
		cfg.LogDir = ""
	}
	// 2026-09-21 起文件日志为常驻能力，JUHE_AI_LOG_FILE_ENABLED 开关移除。
	cfg.LogFileEnabled = true
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
	// 写侧轮转阈值：恢复 Node 既有 env 契约（archived config/runtime.ts
	// numberConfig('JUHE_AI_LOG_MAX_FILE_MB', 100, 1, 1024)），同名同默认
	// 同范围；解析复用 numberConfig 语义（parseTruncatedInt：非数字或越界
	// fail-fast，Math.trunc 先于范围检查）。
	cfg.LogMaxFileMB = 100
	if raw := strings.TrimSpace(getenv("JUHE_AI_LOG_MAX_FILE_MB")); raw != "" {
		parsed, parsedErr := parseTruncatedInt("JUHE_AI_LOG_MAX_FILE_MB", raw, 1, 1024)
		if parsedErr != nil {
			return runtimeConfig{}, parsedErr
		}
		cfg.LogMaxFileMB = parsed
	}

	cfg.BusinessOwner = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_BUSINESS_OWNER")))
	cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_POSTGRES_URL"))
	// 清理批次 C1（2026-09-30）：Node→Go 切流已完成且 Node 进程已于
	// 2026-09-05 清零（不可恢复），BUSINESS 切流五证 env 家族与 cutover
	// evidence 校验链整体退役——owner 事实一律按零配置自动认领（原显式配置
	// 分支在所有现存部署中从未被配置过，五源核对零引用）。ownermode 输入
	// 字段保留并恒为 auto-claim 真值组合；业务连接回落共享
	// JUHE_AI_POSTGRES_URL 的语义不变（独立凭据仍可显式配置）。
	// 2026-10-24 存储形态收口：sqlite 臂删除，自动认领无条件化。
	cfg.BusinessOwner = "gateway"
	cfg.BusinessHandoffConfirmed = true
	cfg.BusinessNodeWriterStopped = true
	cfg.BusinessSchemaReady = true
	cfg.BusinessOwnerEpoch = "standalone"
	if cfg.BusinessPostgresURL == "" {
		cfg.BusinessPostgresURL = cfg.PostgresURL
	}

	return cfg, nil
}

// businessOwnerGate keeps only the connection precondition after the C1
// cutover-gate retirement (2026-09-30) and the 2026-10-24 storage-shape
// retirement: the handoff/cutover evidence family is gone with the Node
// cutover era; an enabled system api composition still fails closed when its
// business PostgreSQL connection is not resolvable.
func (c *runtimeConfig) businessOwnerGate() error {
	if !c.SystemAPIEnabled {
		return nil
	}
	if c.BusinessPostgresURL == "" {
		return fmt.Errorf("缺少 JUHE_AI_BUSINESS_POSTGRES_URL（未配置时回落 JUHE_AI_POSTGRES_URL）")
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
//
// 2026-09-19 用户决策（PLAN-20260919T000723744Z，开源项目优先易用性）：
// 默认放行私网/本机上游 Base URL，不再拒绝生产信号；strictEnvBool 的
// fallback 翻转为 true，JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=false/0
// 可选择性恢复限制。
func upstreamURLSecurityConfig(production bool, getenv func(string) string) (UpstreamURLSecurityConfig, error) {
	config := UpstreamURLSecurityConfig{PrivateOriginAllowlist: map[string]bool{}}
	allowPrivate, err := strictEnvBool("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS", getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS"), true)
	if err != nil {
		return config, err
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
