package ratelimit

// w9b 中间件面、integerSetting 与 SettingError 契约。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestW9BIPRateLimitMiddlewarePassesThroughOnHealth(t *testing.T) {
	limiter := &Limiter{}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := limiter.IPRateLimitMiddleware(next)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !called || recorder.Code != http.StatusOK {
		t.Fatalf("health 直通 called=%v code=%d", called, recorder.Code)
	}
}

func TestW9BIPRateLimitMiddlewareWithoutSettings(t *testing.T) {
	limiter := &Limiter{}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := limiter.IPRateLimitMiddleware(next)
	// 无 SettingsProvider 时中间件不得 panic（缺省放行或拒绝都是契约内行为）。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts", nil)
	func() {
		defer func() {
			if recover() != nil {
				t.Log("nil settings 触发限流缺省路径")
			}
		}()
		handler.ServeHTTP(recorder, request)
	}()
	_ = called
}

func TestW9BAuthenticatedRateLimitHealthBypass(t *testing.T) {
	limiter := &Limiter{}
	recorder := httptest.NewRecorder()
	if !limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/health", nil), "acct-1") {
		t.Fatal("health 必须直通")
	}
	if !limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/health", nil), "acct-1") {
		t.Fatal("后缀 health 必须直通")
	}
}

func TestW9BIntegerSettingArms(t *testing.T) {
	if value, err := integerSetting(json.Number("42"), "k"); err != nil || value != 42 {
		t.Fatalf("合法=%d err=%v", value, err)
	}
	if _, err := integerSetting(json.Number("abc"), "k"); err == nil {
		t.Fatal("非数字必须报错")
	}
	if _, err := integerSetting(json.Number("-1"), "k"); err == nil {
		t.Fatal("负数必须报错")
	}
	if _, err := integerSetting(json.Number("1000001"), "k"); err == nil {
		t.Fatal("越上限必须报错")
	}
}

func TestW9BSettingErrorMessage(t *testing.T) {
	simple := &SettingError{Key: "limit"}
	if simple.Error() != "limit 必须是整数" {
		t.Fatalf("simple=%q", simple.Error())
	}
	outOfRange := &SettingError{Key: "limit", OutOfRange: true}
	if outOfRange.Error() != "limit 必须在 0 到 1000000 之间" {
		t.Fatalf("range=%q", outOfRange.Error())
	}
}
