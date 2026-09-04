// ratelimit_test.go covers the protocol fixed-window limiter (ratelimit.go):
// endpoint-class routing, per-class rule boundaries, window rollover and
// Retry-After math with an injected clock, plus the 429 middleware contract
// (error codes rate_limited/slow_down and the verbatim Chinese description).
package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOAuthEndpointClass(t *testing.T) {
	cases := map[string]string{
		"/oauth/token":                      "token",
		"/oauth/token/renew":                "token",
		"/oauth/revoke":                     "token",
		"/oauth/authorize/decision":         "decision",
		"/oauth/device/decision":            "decision",
		"/oauth/authorize":                  "authorize",
		"/oauth/device":                     "authorize",
		"/oauth/device_authorization":       "authorize",
		"/oauth/userinfo":                   "userinfo",
		"/.well-known/openid-configuration": "discovery",
		"/oauth/jwks":                       "discovery",
		"/oauth/somewhere-else":             "discovery",
	}
	for path, want := range cases {
		if got := OAuthEndpointClass(path); got != want {
			t.Fatalf("OAuthEndpointClass(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestProtocolRateLimiterClassBoundaries(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	// oauthProtocolRules from the Node protocolRules table.
	cases := []struct {
		class string
		max   int
	}{
		{"token", 30},
		{"decision", 30},
		{"authorize", 120},
		{"userinfo", 120},
		{"discovery", 240},
		{"delegated", 300},
		{"unknown-class", 240}, // falls back to the discovery rule
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			limiter := NewProtocolRateLimiter(clock.Now)
			for i := 1; i <= tc.max; i++ {
				allowed, retryAfter := limiter.Allow(tc.class, "1.2.3.4")
				if !allowed || retryAfter != 0 {
					t.Fatalf("request %d/%d: allowed=%v retryAfter=%d", i, tc.max, allowed, retryAfter)
				}
			}
			allowed, retryAfter := limiter.Allow(tc.class, "1.2.3.4")
			if allowed {
				t.Fatalf("request %d passed the window", tc.max+1)
			}
			if retryAfter != 60 {
				t.Fatalf("retryAfter at window start = %d, want 60", retryAfter)
			}
		})
	}
}

func TestProtocolRateLimiterScopeIsolationAndRollover(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := NewProtocolRateLimiter(clock.Now)

	// Different scope keys count independently.
	if allowed, _ := limiter.Allow("token", "10.0.0.1"); !allowed {
		t.Fatal("first scope request blocked")
	}
	for i := 0; i < 30; i++ {
		if allowed, _ := limiter.Allow("token", "10.0.0.2"); !allowed {
			t.Fatalf("second scope blocked at request %d", i+1)
		}
	}
	if allowed, _ := limiter.Allow("token", "10.0.0.2"); allowed {
		t.Fatal("second scope should be exhausted")
	}
	if allowed, _ := limiter.Allow("token", "10.0.0.1"); !allowed {
		t.Fatal("first scope must not be affected by the second scope")
	}

	// Retry-After countdown: ceil((windowEnd - nowMs) / 1000), floored at 1.
	_, retry := limiter.Allow("token", "10.0.0.2")
	if retry != 60 {
		t.Fatalf("initial retry = %d", retry)
	}
	clock.Advance(30 * time.Second)
	_, retry = limiter.Allow("token", "10.0.0.2")
	if retry != 30 {
		t.Fatalf("retry after 30s = %d, want 30", retry)
	}
	clock.Advance(29_999 * time.Millisecond)
	_, retry = limiter.Allow("token", "10.0.0.2")
	if retry != 1 {
		t.Fatalf("retry at window end - 1ms = %d, want 1", retry)
	}
	// Crossing the boundary starts a fresh window and resets the count.
	clock.Advance(time.Millisecond)
	if allowed, retry := limiter.Allow("token", "10.0.0.2"); !allowed || retry != 0 {
		t.Fatalf("rolled-over window blocked: allowed=%v retry=%d", allowed, retry)
	}
}

func TestProtocolRateLimiterDefaultClock(t *testing.T) {
	// nil clock falls back to time.Now (smoke test, no panic).
	limiter := NewProtocolRateLimiter(nil)
	if allowed, _ := limiter.Allow("discovery", "x"); !allowed {
		t.Fatal("first request blocked with default clock")
	}
}

func TestProtocolRateLimiterCleanup(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := NewProtocolRateLimiter(clock.Now)
	// Fill beyond maxEntries (50k) with distinct scope keys.
	for i := 0; i <= limiter.maxEntr; i++ {
		limiter.Allow("discovery", "ip-"+itoa(i))
	}
	if got := len(limiter.counts); got <= limiter.maxEntr {
		t.Fatalf("expected more than %d entries, got %d", limiter.maxEntr, got)
	}
	// Everything is expired after a window; the next Allow triggers cleanup.
	clock.Advance(61 * time.Second)
	limiter.Allow("discovery", "fresh-ip")
	if got := len(limiter.counts); got != 1 {
		t.Fatalf("entries after cleanup = %d, want 1", got)
	}
}

func TestProtocolRateLimiterMiddleware(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	newHandler := func(limiter *ProtocolRateLimiter, class string) http.Handler {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return limiter.Middleware(func(r *http.Request) string { return class })(next)
	}

	t.Run("passes through under the limit", func(t *testing.T) {
		limiter := NewProtocolRateLimiter(clock.Now)
		handler := newHandler(limiter, "discovery")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/jwks", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("429 rate_limited body and header", func(t *testing.T) {
		limiter := NewProtocolRateLimiter(clock.Now)
		handler := newHandler(limiter, "decision")
		var rec *httptest.ResponseRecorder
		for i := 0; i <= 30; i++ {
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/oauth/authorize/decision", nil))
		}
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatal("Retry-After header missing")
		}
		wantBody := `{"error":"rate_limited","error_description":"OAuth 请求过于频繁，请稍后重试"}`
		if got := rec.Body.String(); got != wantBody {
			t.Fatalf("body = %q, want %q", got, wantBody)
		}
	})

	t.Run("token class reports slow_down", func(t *testing.T) {
		limiter := NewProtocolRateLimiter(clock.Now)
		handler := newHandler(limiter, "token")
		var rec *httptest.ResponseRecorder
		for i := 0; i <= 30; i++ {
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/oauth/token", nil))
		}
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d", rec.Code)
		}
		wantBody := `{"error":"slow_down","error_description":"OAuth 请求过于频繁，请稍后重试"}`
		if got := rec.Body.String(); got != wantBody {
			t.Fatalf("body = %q, want %q", got, wantBody)
		}
	})

	t.Run("unidentified client IP shares the unknown scope", func(t *testing.T) {
		limiter := NewProtocolRateLimiter(clock.Now)
		handler := newHandler(limiter, "token")
		// Requests without the kernel request context all map to "unknown".
		for i := 0; i < 30; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			req.RemoteAddr = "10.1.1.1:1000"
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("request %d status = %d", i+1, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
		req.RemoteAddr = "10.2.2.2:2000" // different remote, same "unknown" scope
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("different remote shares the unknown scope: status = %d", rec.Code)
		}
	})
}
