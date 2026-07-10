package ratelimit

import (
	"context"
	"fmt"
	"time"

	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultStoreName  = "external_source_public_api"
	defaultMaxPenalty = 15 * time.Minute
	defaultMaxIdle    = 24 * time.Hour
)

type PenaltyWindowClient interface {
	AllowPenaltyWindow(context.Context, []redisplatform.PenaltyWindowLimit) (redisplatform.PenaltyWindowDecision, error)
}

type Limiter struct {
	client     PenaltyWindowClient
	storeName  string
	maxPenalty time.Duration
	maxIdle    time.Duration
	now        func() time.Time
}

type Options struct {
	Client     PenaltyWindowClient
	StoreName  string
	MaxPenalty time.Duration
	MaxIdle    time.Duration
	Now        func() time.Time
}

type Decision struct {
	Allowed           bool
	Rule              port.PublicAPIRateLimitRule
	RetryAfterSeconds int
}

func NewLimiter(opts Options) (*Limiter, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("public api rate limiter client is required")
	}
	storeName := opts.StoreName
	if storeName == "" {
		storeName = DefaultStoreName
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	maxPenalty := opts.MaxPenalty
	if maxPenalty <= 0 {
		maxPenalty = defaultMaxPenalty
	}
	maxIdle := opts.MaxIdle
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdle
	}
	return &Limiter{
		client:     opts.Client,
		storeName:  storeName,
		maxPenalty: maxPenalty,
		maxIdle:    maxIdle,
		now:        now,
	}, nil
}

func (l *Limiter) Allow(ctx context.Context, authContext publicapiauth.AuthContext) (Decision, error) {
	if l == nil || l.client == nil {
		return Decision{}, fmt.Errorf("public api rate limiter is required")
	}

	activeRules := activeRateLimitRules(authContext.RateLimits)
	if len(activeRules) == 0 {
		return Decision{Allowed: true}, nil
	}

	now := l.now().UTC()
	scopeKey := publicapiauth.ExternalSourceRateLimitKey(authContext)
	limits := make([]redisplatform.PenaltyWindowLimit, 0, len(activeRules))
	for _, rule := range activeRules {
		limits = append(limits, redisplatform.PenaltyWindowLimit{
			StoreName:  l.storeName,
			ScopeKey:   scopeKey,
			Window:     time.Duration(rule.WindowSeconds) * time.Second,
			Limit:      rule.MaxRequests,
			MaxPenalty: l.maxPenalty,
			MaxIdle:    l.maxIdle,
			Now:        now,
		})
	}

	decision, err := l.client.AllowPenaltyWindow(ctx, limits)
	if err != nil {
		return Decision{}, err
	}
	if decision.Allowed {
		return Decision{Allowed: true}, nil
	}

	ruleIndex := decision.BlockedWindowIndex - 1
	if ruleIndex < 0 || ruleIndex >= len(activeRules) {
		ruleIndex = 0
	}
	return Decision{
		Allowed:           false,
		Rule:              activeRules[ruleIndex],
		RetryAfterSeconds: max(1, decision.RetryAfterSeconds),
	}, nil
}

func activeRateLimitRules(rules []port.PublicAPIRateLimitRule) []port.PublicAPIRateLimitRule {
	active := make([]port.PublicAPIRateLimitRule, 0, len(rules))
	for _, rule := range rules {
		if rule.WindowSeconds > 0 && rule.MaxRequests > 0 {
			active = append(active, rule)
		}
	}
	return active
}
