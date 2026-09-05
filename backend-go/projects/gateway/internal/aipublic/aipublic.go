// Package aipublic implements the X04 slice: the externally maintained
// legacy public API family mounted at /__aipublic__ and ported from
// backend/src/modules/external-integrations (external-integrations.routes.ts
// plus the external-public-*.ts service/mock/sanitize/payload files and the
// external-source-auth.middleware.ts guard).
//
// Route matrix (16 routes, each behind bearer-token auth + per-source
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
//
// The admin management family (/external-integration-sources) and the static
// API catalog already live in internal/policyreads (M16b); this package only
// owns the public caller-facing surface. Resource families reuse the migrated
// stores (groups M05, route-strategies M06, api-keys M07, accounts M08-M10)
// exactly like the delegated slice (P03); the target "public user" identity
// is resolved/auto-created through the authsys system-account store. The
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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Prefix is the mounted route prefix (Node publicApiPrefix default).
const Prefix = "/__aipublic__"

// Scope constants mirror storage/external-integration-source-constants.ts.
const (
	scopeGroupListRead       = "juhe_ai_public:group_list:read"
	scopeStrategyListRead    = "juhe_ai_public:route_strategy_list:read"
	scopeApiKeyListRead      = "juhe_ai_public:api_key_list:read"
	scopeAccountListRead     = "juhe_ai_public:account_list:read"
	scopeGroupAddWrite       = "juhe_ai_public:group_add:write"
	scopeGroupUpdateWrite    = "juhe_ai_public:group_update:write"
	scopeGroupDeleteWrite    = "juhe_ai_public:group_delete:write"
	scopeStrategyAddWrite    = "juhe_ai_public:route_strategy_add:write"
	scopeStrategyUpdateWrite = "juhe_ai_public:route_strategy_update:write"
	scopeStrategyDeleteWrite = "juhe_ai_public:route_strategy_delete:write"
	scopeApiKeyAddWrite      = "juhe_ai_public:api_key_add:write"
	scopeApiKeyUpdateWrite   = "juhe_ai_public:api_key_update:write"
	scopeApiKeyDeleteWrite   = "juhe_ai_public:api_key_delete:write"
	scopeAccountAddWrite     = "juhe_ai_public:account_add:write"
	scopeAccountUpdateWrite  = "juhe_ai_public:account_update:write"
	scopeAccountDeleteWrite  = "juhe_ai_public:account_delete:write"
	builtInTestSourceID      = "extsrc_builtin_test"
	builtInTestTokenID       = "exttok_builtin_test"
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

// Mount wires the 16 public routes (Node app.use(publicApiPrefix,
// externalIntegrationsRouter)).
func (d *Deps) Mount(k *kernel.Kernel) {
	register := func(method, path, scope string, handler http.HandlerFunc) {
		k.Register(method+" "+Prefix+path, d.guard(scope, handler))
	}
	register(http.MethodGet, "/group/list", scopeGroupListRead, d.listGroups)
	register(http.MethodPost, "/group/add", scopeGroupAddWrite, d.addGroup)
	register(http.MethodPost, "/group/update", scopeGroupUpdateWrite, d.updateGroup)
	register(http.MethodPost, "/group/del", scopeGroupDeleteWrite, d.deleteGroup)
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
func (d *Deps) guard(scope string, handler http.HandlerFunc) http.Handler {
	return d.withCapture(func(w http.ResponseWriter, r *http.Request, source *captureSourceHolder) {
		token := bearerToken(r)
		if token == "" {
			writeCodeError(w, http.StatusUnauthorized, "external_source_token_missing", "缺少来源系统 token")
			return
		}
		context, authErr := d.ValidateToken(r.Context(), token, scope)
		if authErr != nil {
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
