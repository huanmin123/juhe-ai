package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementCaptchaHandlerReturnsChallenge(t *testing.T) {
	issuer := &managementCaptchaIssuerStub{
		challenge: managementauth.CaptchaChallenge{
			CaptchaID: "captcha-id",
			Image:     "data:image/png;base64,test",
			ExpiresAt: "2026-07-08T12:05:00.000Z",
		},
	}
	handler := NewManagementCaptchaHandler(issuer, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if issuer.clientIP != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want remote address IP", issuer.clientIP)
	}
	var body struct {
		Data managementauth.CaptchaChallenge `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.CaptchaID != "captcha-id" || body.Data.Image == "" || body.Data.ExpiresAt == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCaptchaHandlerReturnsNotRequiredWhenDisabled(t *testing.T) {
	issuer := &managementCaptchaIssuerStub{}
	handler := NewManagementCaptchaHandler(issuer, config.Config{AuthCaptchaDisabled: true})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Required bool `json:"required"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Required {
		t.Fatal("required = true, want false")
	}
	if issuer.clientIP != "" {
		t.Fatalf("disabled captcha should not issue challenge, clientIP = %q", issuer.clientIP)
	}
}

func TestManagementCaptchaHandlerUsesTrustProxyClientIP(t *testing.T) {
	issuer := &managementCaptchaIssuerStub{
		challenge: managementauth.CaptchaChallenge{CaptchaID: "captcha-id"},
	}
	handler := NewManagementCaptchaHandler(issuer, config.Config{TrustProxy: "true"})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if issuer.clientIP != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want trusted forwarded IP", issuer.clientIP)
	}
}

func TestManagementCaptchaHandlerWritesRateLimitError(t *testing.T) {
	handler := NewManagementCaptchaHandler(&managementCaptchaIssuerStub{
		err: &managementauth.CaptchaIssueLimitError{RetryAfterSeconds: 9},
	}, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "9" {
		t.Fatalf("Retry-After = %q, want 9", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "验证码请求过于频繁，请稍后再试" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCaptchaHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementCaptchaHandler(&managementCaptchaIssuerStub{
		err: errors.New("redis password leaked"),
	}, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestRouterRegistersManagementCaptchaWhenEnabled(t *testing.T) {
	issuer := &managementCaptchaIssuerStub{
		challenge: managementauth.CaptchaChallenge{CaptchaID: "captcha-id"},
	}
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCaptchaHandler:    NewManagementCaptchaHandler(issuer, config.Config{}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterDoesNotRegisterManagementCaptchaWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCaptchaHandler:    NewManagementCaptchaHandler(&managementCaptchaIssuerStub{}, config.Config{}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func TestRouterAppliesIPReadRateLimitForManagementCaptcha(t *testing.T) {
	issuer := &managementCaptchaIssuerStub{
		challenge: managementauth.CaptchaChallenge{CaptchaID: "captcha-id"},
	}
	limiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 7,
		},
	}
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCaptchaHandler:    NewManagementCaptchaHandler(issuer, config.Config{}),
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 1, IPReadBurstPer10Seconds: 1},
		},
		SystemAPIIPRateLimiter: limiter,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s, want 429", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	if limiter.calls != 1 {
		t.Fatalf("system API limiter calls = %d, want 1 for captcha GET", limiter.calls)
	}
}

type managementCaptchaIssuerStub struct {
	clientIP  string
	challenge managementauth.CaptchaChallenge
	err       error
}

func (s *managementCaptchaIssuerStub) IssueChallenge(_ context.Context, clientIP string) (managementauth.CaptchaChallenge, error) {
	s.clientIP = clientIP
	return s.challenge, s.err
}
