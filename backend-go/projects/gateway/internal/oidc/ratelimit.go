// ratelimit.go ports oidc-rate-limit.middleware.ts: the shared penalty-window
// protocol limiter guarding both the OAuth public surface and the delegated
// API (`delegatedOAuthRateLimit`). Node backs this with
// modules/rate-limit/penalty-window-rate-limit.ts; this slice implements the
// fixed-window decision the protocol middleware observes (per endpoint class
// + client IP), including the 429 body and Retry-After contract. Penalty
// escalation beyond the window (maxPenaltyMs) stays with the shared rate-limit
// slice.
package oidc

import (
	"net/http"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ProtocolRateLimitRule mirrors PenaltyWindowRateLimitRule.
type ProtocolRateLimitRule struct {
	WindowSeconds int
	MaxRequests   int
}

var oauthProtocolRules = map[string][]ProtocolRateLimitRule{
	"token":     {{WindowSeconds: 60, MaxRequests: 30}},
	"decision":  {{WindowSeconds: 60, MaxRequests: 30}},
	"authorize": {{WindowSeconds: 60, MaxRequests: 120}},
	"userinfo":  {{WindowSeconds: 60, MaxRequests: 120}},
	"discovery": {{WindowSeconds: 60, MaxRequests: 240}},
	"delegated": {{WindowSeconds: 60, MaxRequests: 300}},
}

// ProtocolRateLimiter is the in-memory fixed-window limiter shared by the
// oauth public router and the delegated API router.
type ProtocolRateLimiter struct {
	mu      sync.Mutex
	counts  map[string]*rateWindow
	now     func() time.Time
	maxEntr int
}

type rateWindow struct {
	count     int
	windowEnd int64 // unix ms
}

// NewProtocolRateLimiter builds the limiter (mirrors the
// oauth_protocol_public store: 50k entries, 1h idle).
func NewProtocolRateLimiter(now func() time.Time) *ProtocolRateLimiter {
	if now == nil {
		now = time.Now
	}
	return &ProtocolRateLimiter{counts: map[string]*rateWindow{}, now: now, maxEntr: 50_000}
}

// OAuthEndpointClass mirrors endpointClass(req.path).
func OAuthEndpointClass(path string) string {
	switch path {
	case "/oauth/token", "/oauth/token/renew", "/oauth/revoke":
		return "token"
	case "/oauth/authorize/decision", "/oauth/device/decision":
		return "decision"
	case "/oauth/authorize", "/oauth/device", "/oauth/device_authorization":
		return "authorize"
	case "/oauth/userinfo":
		return "userinfo"
	default:
		return "discovery"
	}
}

// Allow records one request and reports whether it is within the class rule.
func (l *ProtocolRateLimiter) Allow(class, scopeKey string) (allowed bool, retryAfterSeconds int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup()
	nowMs := l.now().UnixMilli()
	rule := oauthProtocolRules[class]
	if len(rule) == 0 {
		rule = oauthProtocolRules["discovery"]
	}
	windowSeconds := rule[0].WindowSeconds
	maxRequests := rule[0].MaxRequests
	key := class + ":" + scopeKey
	window, ok := l.counts[key]
	if !ok || nowMs >= window.windowEnd {
		window = &rateWindow{count: 0, windowEnd: nowMs + int64(windowSeconds)*1_000}
		l.counts[key] = window
	}
	window.count++
	if window.count > maxRequests {
		retry := (window.windowEnd - nowMs + 999) / 1_000
		if retry < 1 {
			retry = 1
		}
		return false, int(retry)
	}
	return true, 0
}

func (l *ProtocolRateLimiter) cleanup() {
	if len(l.counts) <= l.maxEntr {
		return
	}
	nowMs := l.now().UnixMilli()
	for key, window := range l.counts {
		if nowMs >= window.windowEnd {
			delete(l.counts, key)
		}
	}
}

// Middleware enforces the class for the current request path. clientIP comes
// from the kernel request context (mirrors getRequestContext()?.clientIp).
func (l *ProtocolRateLimiter) Middleware(class func(r *http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := "unknown"
			if ctx := kernel.Context(r); ctx != nil && ctx.ClientIP != "" {
				ip = ctx.ClientIP
			}
			class := class(r)
			allowed, retryAfter := l.Allow(class, ip)
			if !allowed {
				w.Header().Set("Retry-After", itoa(retryAfter))
				errorCode := "rate_limited"
				if class == "token" {
					errorCode = "slow_down"
				}
				kernel.WriteJSON(w, http.StatusTooManyRequests, oauthError(errorCode, "OAuth 请求过于频繁，请稍后重试"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
