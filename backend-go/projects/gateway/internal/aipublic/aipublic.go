// Package aipublic implements the X04 slice: the externally maintained
// legacy public API family mounted at /__aipublic__ and ported from
// backend/src/modules/external-integrations (external-integrations.routes.ts
// plus the external-public-*.ts service/mock/sanitize/payload files and the
// external-source-auth.middleware.ts guard).
//
// Route matrix (18 routes, each behind bearer-token auth + per-source
// penalty-window rate limiting + scope checks):
//
//	GET  /__aipublic__/group/list            scope juhe_ai_public:group_list:read
//	POST /__aipublic__/group/add             scope juhe_ai_public:group_add:write
//	POST /__aipublic__/group/update          scope juhe_ai_public:group_update:write
//	POST /__aipublic__/group/del             scope juhe_ai_public:group_delete:write
//	GET  /__aipublic__/route-strategy/list   scope juhe_ai_public:route_strategy_list:read
//	POST /__aipublic__/route-strategy/add    scope juhe_ai_public:route_strategy_add:write
//	POST /__aipublic__/route-strategy/update scope juhe_ai_public:route_strategy_update:write
//	POST /__aipublic__/route-strategy/del    scope juhe_ai_public:route_strategy_delete:write
//	GET  /__aipublic__/api-key/list          scope juhe_ai_public:api_key_list:read
//	POST /__aipublic__/api-key/add           scope juhe_ai_public:api_key_add:write
//	POST /__aipublic__/api-key/update        scope juhe_ai_public:api_key_update:write
//	POST /__aipublic__/api-key/del           scope juhe_ai_public:api_key_delete:write
//	GET  /__aipublic__/account/list          scope juhe_ai_public:account_list:read
//	POST /__aipublic__/account/add           scope juhe_ai_public:account_add:write
//	POST /__aipublic__/account/update        scope juhe_ai_public:account_update:write
//	POST /__aipublic__/account/del           scope juhe_ai_public:account_delete:write
//	GET  /__aipublic__/provider/list         scope juhe_ai_public:provider_list:read
//	GET  /__aipublic__/provider/detail       scope juhe_ai_public:provider_detail:read
//
// The admin management family (/external-integration-sources) and the static
// API catalog already live in internal/policyreads (M16b); this package only
// owns the public caller-facing surface. Resource families reuse the migrated
// stores (groups M05, route-strategies M06, api-keys M07, accounts M08-M10,
// providers T5 read family) exactly like the delegated slice (P03); the
// target "public user" identity is resolved/auto-created through the authsys
// system-account store. The provider family is read-only (list + detail with
// the basic/models tabs) and projects the providers.Store definitions; the
// built-in test token returns deterministic mock payloads without touching
// the resource tables (Node external-public-*.mock.ts).
package aipublic

import (
	"database/sql"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Prefix is the mounted route prefix (Node publicApiPrefix default).
const Prefix = "/__aipublic__"

// DD-A4：/__aipublic__ 请求体上限。aipublic 的 payload 都是小 JSON，取管理
// 面同款契约（kernel Options.JSONBodyLimitBytes 默认 256 KiB，即
// systemApiJsonBodyLimit 的值）；超限响应与管理面一致走 DecodeJSON 的
// 413 {"message":"请求体过大"}（guard 内以 http.MaxBytesReader 包裹 body，
// DecodeJSON 的 readAll 收到 *http.MaxBytesError 后渲染同款 413）。
const publicJSONBodyLimitBytes = 256 * 1024

// DD-A3①：无效 token 认证失败的逐来源 IP 惩罚窗规则。数值对齐 preauth
// invalid-token 熔断的量级（60s 窗，gatewayclientip preAuthInvalidToken-
// Threshold=8 为单 token 指纹口径，per-IP 聚合多 token 指纹故放宽到 10）：
// 单次失败不误伤（失败 10 次内照常 401），连续无效 token 达阈值打开惩罚窗
// （429 + Retry-After），惩罚时长按既有 penalty-window 指数倍增至
// rateLimitMaxPenaltyMs。复用 consumeRateLimit 的既有惩罚窗机制（Redis 驱动
// 与 memory 模型同款），scopeKey 前缀与合法 token 的
// "SourceRefID:TokenID:TokenPrefix" 键空间隔离。
const (
	invalidTokenPenaltyWindowSeconds = 60
	invalidTokenPenaltyMaxRequests   = 10
)

// invalidTokenPenaltyScopePrefix 是失败惩罚窗的 scopeKey 前缀（防与合法
// token 键碰撞）。
const invalidTokenPenaltyScopePrefix = "invalid-token:"

// Scope constants mirror storage/external-integration-source-constants.ts.
const (
	scopeGroupListRead                = "juhe_ai_public:group_list:read"
	scopeStrategyListRead             = "juhe_ai_public:route_strategy_list:read"
	scopeApiKeyListRead               = "juhe_ai_public:api_key_list:read"
	scopeAccountListRead              = "juhe_ai_public:account_list:read"
	scopeProviderListRead             = "juhe_ai_public:provider_list:read"
	scopeProviderDetailRead           = "juhe_ai_public:provider_detail:read"
	scopeGroupAddWrite                = "juhe_ai_public:group_add:write"
	scopeGroupUpdateWrite             = "juhe_ai_public:group_update:write"
	scopeGroupDeleteWrite             = "juhe_ai_public:group_delete:write"
	scopeGroupSuperMembersUpdateWrite = "juhe_ai_public:group_super_members_update:write"
	scopeStrategyAddWrite             = "juhe_ai_public:route_strategy_add:write"
	scopeStrategyUpdateWrite          = "juhe_ai_public:route_strategy_update:write"
	scopeStrategyDeleteWrite          = "juhe_ai_public:route_strategy_delete:write"
	scopeApiKeyAddWrite               = "juhe_ai_public:api_key_add:write"
	scopeApiKeyUpdateWrite            = "juhe_ai_public:api_key_update:write"
	scopeApiKeyDeleteWrite            = "juhe_ai_public:api_key_delete:write"
	scopeAccountAddWrite              = "juhe_ai_public:account_add:write"
	scopeAccountUpdateWrite           = "juhe_ai_public:account_update:write"
	scopeAccountDeleteWrite           = "juhe_ai_public:account_delete:write"
	builtInTestSourceID               = "extsrc_builtin_test"
	builtInTestTokenID                = "exttok_builtin_test"
)

// Deps bundles the X04 collaborators. The resource stores are the same
// instances the management families use; DB backs the aipublic-local token
// auth, owner lookups and the group/strategy binding reads that the stores
// keep private.
type Deps struct {
	DB             *sql.DB
	PGDialect      bool
	Now            func() time.Time
	SystemAccounts *authsys.AccountStore
	Groups         *groups.Store
	Strategies     *routestrategies.Store
	ApiKeys        *apikeys.Store
	AiAccounts     *accounts.Store
	// Providers serves the read-only provider list/detail family (the same
	// *providers.Store instance the management providers family mounts).
	Providers *providers.Store
	// Sink receives the account add/update/delete operation logs (Node
	// recordOperationLogAsync with actor `external:<sourceRefId>`). Nil keeps
	// the routes functional without the log.
	Sink authsys.OperationLogSink
	// Capture mirrors the /__aipublic__ public-api-log capture middleware
	// (Node capturePublicApiLog for the external-integrations family). Nil
	// keeps the routes functional without capture.
	Capture PublicApiLogCapture
	// Redis shared penalty-window state (Node runtimeStateDriver === 'redis'):
	// RedisStateClient is the go-redis state client, RedisNamespace the
	// deployment namespace. Both must be set for the shared driver; otherwise
	// the process-local memory model applies.
	RedisDriver      bool
	RedisStateClient RedisStateClient
	RedisNamespace   string
	// Warn receives the Redis-unavailable fallback warning (nil falls back to
	// the standard log).
	Warn func(message string)
	// rateLimiter is created lazily by limiter(); tests can inject a clock via
	// NewPenaltyWindowLimiter.
	rateLimiter *PenaltyWindowLimiter
	redisOnce   sync.Once
	redisShared *redisPenaltyDriver
}

// Mount wires the 18 public routes (Node app.use(publicApiPrefix,
// externalIntegrationsRouter)).
func (d *Deps) Mount(k *kernel.Kernel) {
	register := func(method, path, scope string, handler http.HandlerFunc) {
		k.Register(method+" "+Prefix+path, d.guard(scope, handler))
	}
	register(http.MethodGet, "/group/list", scopeGroupListRead, d.listGroups)
	register(http.MethodPost, "/group/add", scopeGroupAddWrite, d.addGroup)
	register(http.MethodPost, "/group/update", scopeGroupUpdateWrite, d.updateGroup)
	register(http.MethodPost, "/group/del", scopeGroupDeleteWrite, d.deleteGroup)
	register(http.MethodPost, "/group/super-members/update", scopeGroupSuperMembersUpdateWrite, d.updateSuperMembers)
	register(http.MethodGet, "/route-strategy/list", scopeStrategyListRead, d.listRouteStrategies)
	register(http.MethodPost, "/route-strategy/add", scopeStrategyAddWrite, d.addRouteStrategy)
	register(http.MethodPost, "/route-strategy/update", scopeStrategyUpdateWrite, d.updateRouteStrategy)
	register(http.MethodPost, "/route-strategy/del", scopeStrategyDeleteWrite, d.deleteRouteStrategy)
	register(http.MethodGet, "/api-key/list", scopeApiKeyListRead, d.listApiKeys)
	register(http.MethodPost, "/api-key/add", scopeApiKeyAddWrite, d.addApiKey)
	register(http.MethodPost, "/api-key/update", scopeApiKeyUpdateWrite, d.updateApiKey)
	register(http.MethodPost, "/api-key/del", scopeApiKeyDeleteWrite, d.deleteApiKey)
	register(http.MethodGet, "/account/list", scopeAccountListRead, d.listAccounts)
	register(http.MethodPost, "/account/add", scopeAccountAddWrite, d.addAccount)
	register(http.MethodPost, "/account/update", scopeAccountUpdateWrite, d.updateAccount)
	register(http.MethodPost, "/account/del", scopeAccountDeleteWrite, d.deleteAccount)
	register(http.MethodGet, "/provider/list", scopeProviderListRead, d.listProviders)
	register(http.MethodGet, "/provider/detail", scopeProviderDetailRead, d.providerDetail)
}

var bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// bearerToken mirrors parseBearerToken (case-insensitive scheme, trimmed).
func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return ""
	}
	match := bearerPattern.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	token := strings.TrimSpace(match[1])
	if token == "" {
		return ""
	}
	return token
}

// guard mirrors requireExternalIntegrationSource(scope): bearer parse, token
// validation, rate limiting and the typed 401/403/429 bodies. The capture
// lifecycle wraps the whole guard (Node mounts capturePublicApiLog ahead of
// the auth middleware), so 401/429 responses record with a nil source too.
//
// DD-A4：guard 开头以 http.MaxBytesReader 包裹请求体（publicJSONBodyLimit-
// Bytes），body 经 DecodeJSON 读入时超限渲染与管理面一致的 413。
// DD-A3①：token 校验失败（缺 token 与 typed 401）消费逐来源 IP 惩罚窗——
// 401 认证失败计数、403 授权失败（来源禁用/过期/scope 不足）与基础设施
// 500 不计数；命中惩罚窗渲染同款 429 external_source_rate_limited。
func (d *Deps) guard(scope string, handler http.HandlerFunc) http.Handler {
	return d.withCapture(func(w http.ResponseWriter, r *http.Request, source *captureSourceHolder) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, publicJSONBodyLimitBytes)
		}
		clientIP := kernel.Context(r).ClientIP
		token := bearerToken(r)
		if token == "" {
			d.rejectAuthFailureWithPenalty(w, r, clientIP, http.StatusUnauthorized, "external_source_token_missing", "缺少来源系统 token")
			return
		}
		context, authErr := d.ValidateToken(r.Context(), token, scope)
		if authErr != nil {
			// Infrastructure failures carry no code (Node global handler
			// contract); typed auth failures render {message, code}.
			if authErr.Code == "" {
				kernel.WriteError(w, authErr.StatusCode, authErr.Message)
				return
			}
			if authErr.StatusCode == http.StatusUnauthorized {
				d.rejectAuthFailureWithPenalty(w, r, clientIP, authErr.StatusCode, authErr.Code, authErr.Message)
				return
			}
			writeCodeError(w, authErr.StatusCode, authErr.Code, authErr.Message)
			return
		}
		source.set(context)
		decision := d.consumeRateLimit(r.Context(), context.SourceRefID+":"+context.TokenID+":"+context.TokenPrefix, context.RateLimits)
		if !decision.Allowed {
			w.Header().Set("Retry-After", itoa(decision.RetryAfterSeconds))
			writeCodeErrorDetails(w, http.StatusTooManyRequests, "external_source_rate_limited", "来源系统调用过于频繁，请稍后重试", map[string]any{
				"windowSeconds":     decision.Rule.WindowSeconds,
				"maxRequests":       decision.Rule.MaxRequests,
				"retryAfterSeconds": decision.RetryAfterSeconds,
			})
			return
		}
		handler(w, r.WithContext(withAuthContext(r.Context(), context)))
	})
}

// rejectAuthFailureWithPenalty 消费一次逐来源 IP 无效认证惩罚窗（DD-A3①）：
// 命中惩罚窗渲染 429 external_source_rate_limited（与合法 token 限流响应同
// 形态），否则渲染原 401 typed 失败。clientIP 不可解析（kernel 请求上下文缺
// 失或归空）时跳过惩罚计数，避免未知来源共享惩罚桶互相误伤。
func (d *Deps) rejectAuthFailureWithPenalty(w http.ResponseWriter, r *http.Request, clientIP string, status int, code, message string) {
	if clientIP != "" {
		decision := d.consumeRateLimit(r.Context(), invalidTokenPenaltyScopePrefix+clientIP, []RateLimitRule{
			{WindowSeconds: invalidTokenPenaltyWindowSeconds, MaxRequests: invalidTokenPenaltyMaxRequests},
		})
		if !decision.Allowed {
			w.Header().Set("Retry-After", itoa(decision.RetryAfterSeconds))
			writeCodeErrorDetails(w, http.StatusTooManyRequests, "external_source_rate_limited", "来源系统调用过于频繁，请稍后重试", map[string]any{
				"windowSeconds":     decision.Rule.WindowSeconds,
				"maxRequests":       decision.Rule.MaxRequests,
				"retryAfterSeconds": decision.RetryAfterSeconds,
			})
			return
		}
	}
	writeCodeError(w, status, code, message)
}

// writeCodeError mirrors the middleware error body {message, code}.
func writeCodeError(w http.ResponseWriter, status int, code, message string) {
	kernel.WriteJSON(w, status, map[string]any{"message": message, "code": code})
}

func writeCodeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	kernel.WriteJSON(w, status, map[string]any{"message": message, "code": code, "details": details})
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
