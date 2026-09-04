package gatewayproxyhealth

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Ports runtime/authenticated-models-rate-limit.service.ts and
// runtime/public-models-rate-limit.service.ts.

// Compile-time G05 port satisfaction (gatewaypreauth ports are read-only).
var (
	_ gatewaypreauth.UserRequestLimits            = (*UserRequestLimitsService)(nil)
	_ gatewaypreauth.AuthenticatedModelsRateLimit = (*AuthenticatedModelsRateLimitService)(nil)
)

// AuthenticatedModelsRateLimitDecision mirrors the Node union shape used by
// the pre-auth consumer.
type AuthenticatedModelsRateLimitService struct {
	limiter    *PenaltyWindowRateLimiter
	apiKeyIPSt *PenaltyWindowRateLimitStore
	apiKeySt   *PenaltyWindowRateLimitStore
	clock      Clock
	log        ProxyHealthLogFunc
}

var (
	authenticatedModelsAPIKeyIPRules = []PenaltyWindowRateLimitRule{
		{WindowSeconds: 10, MaxRequests: 20},
		{WindowSeconds: 60, MaxRequests: 60},
	}
	authenticatedModelsAPIKeyRules = []PenaltyWindowRateLimitRule{
		{WindowSeconds: 10, MaxRequests: 100},
		{WindowSeconds: 60, MaxRequests: 300},
	}
)

// NewAuthenticatedModelsRateLimitService builds the service with the Node
// store names ('gateway_authenticated_models_api_key_ip' /
// 'gateway_authenticated_models_api_key', both fixed_window mode).
func NewAuthenticatedModelsRateLimitService(clock Clock, limiter *PenaltyWindowRateLimiter, log ProxyHealthLogFunc) *AuthenticatedModelsRateLimitService {
	return &AuthenticatedModelsRateLimitService{
		limiter: limiter,
		apiKeyIPSt: NewPenaltyWindowRateLimitStore(clock, PenaltyWindowStoreOptions{
			Name:        "gateway_authenticated_models_api_key_ip",
			MaxEntries:  100_000,
			PenaltyMode: PenaltyModeFixedWindow,
			RedisDriver: limiter != nil && limiter.redisDriver,
		}),
		apiKeySt: NewPenaltyWindowRateLimitStore(clock, PenaltyWindowStoreOptions{
			Name:        "gateway_authenticated_models_api_key",
			MaxEntries:  20_000,
			PenaltyMode: PenaltyModeFixedWindow,
			RedisDriver: limiter != nil && limiter.redisDriver,
		}),
		clock: clock,
		log:   log,
	}
}

// ConsumeAuthenticatedModelsRateLimit mirrors consumeAuthenticatedModelsRateLimit
// (nowMs is injectable for tests; production passes nil → clock).
func (s *AuthenticatedModelsRateLimitService) ConsumeAuthenticatedModelsRateLimit(ctx context.Context, input gatewaypreauth.AuthenticatedModelsRateLimitInput, nowMs *int64) (gatewaypreauth.AuthenticatedModelsRateLimitDecision, error) {
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if apiKeyID == "" {
		return gatewaypreauth.AuthenticatedModelsRateLimitDecision{Allowed: true}, nil
	}
	decision, err := s.limiter.ConsumeGroupsAsync(ctx, []PenaltyWindowGroup{
		{
			Scope:    "api_key",
			Store:    s.apiKeySt,
			ScopeKey: apiKeyID,
			Rules:    authenticatedModelsAPIKeyRules,
		},
		{
			Scope:    "api_key_ip",
			Store:    s.apiKeyIPSt,
			ScopeKey: apiKeyID + ":ip:" + normalizedClientIP(input.ClientIP),
			Rules:    authenticatedModelsAPIKeyIPRules,
		},
	}, nowMs)
	if err != nil {
		if s.log != nil {
			s.log(map[string]any{
				"event":    "authenticated_models_rate_limit_unavailable",
				"apiKeyId": apiKeyID,
				"error":    err.Error(),
			}, "认证模型列表 Redis 限流不可用，本次请求按 fail-closed 拒绝")
		}
		retryAfter := int64(5)
		return gatewaypreauth.AuthenticatedModelsRateLimitDecision{
			Allowed:           false,
			Unavailable:       true,
			RetryAfterSeconds: &retryAfter,
		}, nil
	}
	if decision.Allowed {
		return gatewaypreauth.AuthenticatedModelsRateLimitDecision{Allowed: true}, nil
	}
	scope := decision.Scope
	if scope == "" {
		scope = "api_key"
	}
	blocked := gatewaypreauth.AuthenticatedModelsRateLimitDecision{
		Allowed:           false,
		Scope:             string(scope),
		RetryAfterSeconds: int64Ptr(1),
	}
	if decision.Limit != nil {
		blocked.Limit = decision.Limit
	} else if decision.Rule != nil {
		limit := decision.Rule.MaxRequests
		blocked.Limit = &limit
	}
	if decision.RetryAfterSeconds != nil {
		blocked.RetryAfterSeconds = decision.RetryAfterSeconds
	}
	return blocked, nil
}

// Consume implements gatewaypreauth.AuthenticatedModelsRateLimit (G05 port):
// the orchestration seam carries no nowMs; the service clock supplies it.
func (s *AuthenticatedModelsRateLimitService) Consume(ctx context.Context, input gatewaypreauth.AuthenticatedModelsRateLimitInput) (gatewaypreauth.AuthenticatedModelsRateLimitDecision, error) {
	return s.ConsumeAuthenticatedModelsRateLimit(ctx, input, nil)
}

// ClearForTest mirrors clearAuthenticatedModelsRateLimitForTest.
func (s *AuthenticatedModelsRateLimitService) ClearForTest() {
	s.apiKeyIPSt.Clear()
	s.apiKeySt.Clear()
}

func normalizedClientIP(clientIP string) string {
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

// PublicModelsRateLimitDecision mirrors PublicModelsRateLimitDecision.
type PublicModelsRateLimitDecision struct {
	Allowed           bool
	Limit             int64
	Remaining         int64
	RetryAfterSeconds *int64
	ResetAtMs         int64
}

var publicModelsRateLimitRule = PenaltyWindowRateLimitRule{WindowSeconds: 60, MaxRequests: 60}

// PublicModelsRateLimitService mirrors public-models-rate-limit.service.ts.
type PublicModelsRateLimitService struct {
	limiter *PenaltyWindowRateLimiter
	store   *PenaltyWindowRateLimitStore
	clock   Clock
}

// NewPublicModelsRateLimitService builds the service ('gateway_public_models',
// default exponential penalty mode, 15min max penalty).
func NewPublicModelsRateLimitService(clock Clock, limiter *PenaltyWindowRateLimiter) *PublicModelsRateLimitService {
	return &PublicModelsRateLimitService{
		limiter: limiter,
		store: NewPenaltyWindowRateLimitStore(clock, PenaltyWindowStoreOptions{
			Name:         "gateway_public_models",
			MaxEntries:   20_000,
			MaxPenaltyMs: 15 * 60_000,
			RedisDriver:  limiter != nil && limiter.redisDriver,
		}),
		clock: clock,
	}
}

// ConsumePublicModelsRateLimit mirrors consumePublicModelsRateLimit.
func (s *PublicModelsRateLimitService) ConsumePublicModelsRateLimit(ctx context.Context, clientIP string) (PublicModelsRateLimitDecision, error) {
	decision, err := s.limiter.ConsumeAsync(ctx, s.store, publicModelsRateLimitKey(clientIP), []PenaltyWindowRateLimitRule{publicModelsRateLimitRule}, nil)
	if err != nil {
		return PublicModelsRateLimitDecision{}, err
	}
	nowMs := ClockNowMs(s.clock)
	if !decision.Allowed {
		retryAfterSeconds := int64(1)
		if decision.RetryAfterSeconds != nil {
			retryAfterSeconds = *decision.RetryAfterSeconds
		}
		return PublicModelsRateLimitDecision{
			Allowed:           false,
			Limit:             publicModelsRateLimitRule.MaxRequests,
			Remaining:         0,
			RetryAfterSeconds: &retryAfterSeconds,
			ResetAtMs:         nowMs + retryAfterSeconds*1000,
		}, nil
	}
	return PublicModelsRateLimitDecision{
		Allowed:   true,
		Limit:     publicModelsRateLimitRule.MaxRequests,
		Remaining: publicModelsRateLimitRule.MaxRequests,
		ResetAtMs: nowMs + publicModelsRateLimitRule.WindowSeconds*1000,
	}, nil
}

// ClearForTest mirrors clearPublicModelsRateLimitForTest.
func (s *PublicModelsRateLimitService) ClearForTest() {
	s.store.Clear()
}

func publicModelsRateLimitKey(clientIP string) string {
	normalized := strings.TrimSpace(clientIP)
	if normalized == "" {
		return "ip:unknown"
	}
	return "ip:" + normalized
}

// ---------------------------------------------------------------------------
// G05 port wrapper for the user request limits.

// UserRequestLimitsService implements gatewaypreauth.UserRequestLimits: the
// counter consumes and the coordinator start is forwarded.
type UserRequestLimitsService struct {
	counter     *UserRequestLimitCounter
	coordinator *UserRequestLimitCoordinator
}

// NewUserRequestLimitsService wires the counter and coordinator.
func NewUserRequestLimitsService(counter *UserRequestLimitCounter, coordinator *UserRequestLimitCoordinator) *UserRequestLimitsService {
	return &UserRequestLimitsService{counter: counter, coordinator: coordinator}
}

// Counter exposes the underlying counter (admin/diagnostics reuse).
func (s *UserRequestLimitsService) Counter() *UserRequestLimitCounter { return s.counter }

// Coordinator exposes the coordinator for graceful shutdown wiring.
func (s *UserRequestLimitsService) Coordinator() *UserRequestLimitCoordinator { return s.coordinator }

// Consume implements gatewaypreauth.UserRequestLimits.Consume.
func (s *UserRequestLimitsService) Consume(input gatewaypreauth.UserRequestLimitConsumeInput) gatewaypreauth.UserRequestLimitDecision {
	decision := s.counter.Consume(UserRequestLimitConsumeInput{
		SystemAccountID: input.SystemAccountID,
		Settings:        input.Settings,
		Overrides:       input.Overrides,
		NowMs:           input.NowMs,
	})
	out := gatewaypreauth.UserRequestLimitDecision{Allowed: decision.Allowed}
	if decision.Allowed {
		return out
	}
	out.Window = gatewaypreauth.UserRequestLimitWindow(decision.Window)
	out.Limit = decision.Limit
	out.RetryAfterSeconds = decision.RetryAfterSeconds
	return out
}

// StartCoordinator implements gatewaypreauth.UserRequestLimits.StartCoordinator.
func (s *UserRequestLimitsService) StartCoordinator() {
	s.coordinator.StartCoordinator()
}

// SettingsForTest builds a GatewaySettings with only the limit fields set
// (test convenience mirroring the Node partial Pick<GatewaySettings> input).
func SettingsForTest(perMinute, perDay, perWeek, perMonth *int64, timezone string) gatewayruntimecache.GatewaySettings {
	return gatewayruntimecache.GatewaySettings{
		GatewayUserRequestLimitPerMinute: perMinute,
		GatewayUserRequestLimitPerDay:    perDay,
		GatewayUserRequestLimitPerWeek:   perWeek,
		GatewayUserRequestLimitPerMonth:  perMonth,
		UsageStatsTimezone:               timezone,
	}
}
