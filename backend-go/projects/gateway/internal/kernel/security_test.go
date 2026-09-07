package kernel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCORSPolicyIsOriginAllowed pins the Node isCorsOriginAllowed judgment
// (http-security.ts:18-22): an absent origin is always allowed, allow-any
// reflects everything, otherwise the exact allowlist decides (strict string
// comparison — no normalization at judgment time).
func TestCORSPolicyIsOriginAllowed(t *testing.T) {
	policy := CORSPolicy{AllowedOrigins: []string{"https://admin.example.com"}}
	if !policy.IsOriginAllowed("") {
		t.Fatal("a request without an Origin header must be allowed")
	}
	if !policy.IsOriginAllowed("https://admin.example.com") {
		t.Fatal("allowlisted origin must be allowed")
	}
	if policy.IsOriginAllowed("https://evil.example.com") {
		t.Fatal("unknown origin must be denied")
	}
	if policy.IsOriginAllowed("https://admin.example.com/") {
		t.Fatal("origin comparison is a strict string match (no trailing-slash tolerance)")
	}
	if policy.IsOriginAllowed("HTTPS://admin.example.com") {
		t.Fatal("origin comparison is case-sensitive like the Node allowlist includes()")
	}
	any := CORSPolicy{AllowAnyOrigin: true}
	if !any.IsOriginAllowed("https://anything.example.com") {
		t.Fatal("allow-any origin must be allowed")
	}
	if !any.IsOriginAllowed("") {
		t.Fatal("allow-any origin without an Origin header must be allowed")
	}
}

func corsTestHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestCORSMiddlewareHeaderContract ports the Node CORS middleware response
// behavior: allowed origins get the reflected Access-Control-Allow-Origin
// with credentials and Vary, disallowed/absent origins get no CORS headers
// and the request always continues (the middleware itself never rejects).
func TestCORSMiddlewareHeaderContract(t *testing.T) {
	policy := CORSPolicy{AllowedOrigins: []string{"https://admin.example.com"}}

	// 白名单 Origin：反射 ACAO + credentials + Vary，请求继续。
	called := false
	handler := CORSMiddleware(policy)(corsTestHandler(&called))
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil)
	request.Header.Set("Origin", "https://admin.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatal("allowed origin request must reach the handler")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://admin.example.com" {
		t.Fatalf("Access-Control-Allow-Origin must reflect the request origin, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials must be true, got %q", got)
	}
	if !strings.Contains(strings.Join(recorder.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("Vary must include Origin, got %v", recorder.Header().Values("Vary"))
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed origin request must keep the handler status, got %d", recorder.Code)
	}

	// 异源：不写任何 CORS 头，请求继续（cors delegate callback(null, false) → next()）。
	called = false
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatal("a disallowed origin must not be rejected by the middleware itself")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" || recorder.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("disallowed origin must not receive CORS headers: %#v", recorder.Header())
	}

	// 无 Origin：放行且零 CORS 头（同源与非浏览器客户端不受影响）。
	called = false
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatal("a request without Origin must reach the handler")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("a request without Origin must not receive CORS headers")
	}

	// 允许的预检：204 终止 + 默认 methods + 回显 request headers，不进路由。
	called = false
	request = httptest.NewRequest(http.MethodOptions, "/__aisys__/api/accounts", nil)
	request.Header.Set("Origin", "https://admin.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Session-Id")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if called {
		t.Fatal("an allowed preflight must terminate before the router")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight must answer 204, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != corsPreflightAllowMethods {
		t.Fatalf("preflight allow-methods wrong: %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Session-Id" {
		t.Fatalf("preflight allow-headers must echo the request, got %q", got)
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://admin.example.com" {
		t.Fatal("allowed preflight must carry the reflected origin")
	}

	// 被拒绝的预检：落到路由（404 JSON 契约），无 CORS 头。
	called = false
	request = httptest.NewRequest(http.MethodOptions, "/__aisys__/api/accounts", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatal("a disallowed preflight must fall through to the router")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed preflight must not receive CORS headers")
	}
}

// TestCORSMiddlewarePrefixScope pins the mount scoping: only the configured
// prefixes run the CORS judgment (mountPathMatch semantics), everything else
// — including the /v1 gateway chain — stays byte-for-byte unchanged.
func TestCORSMiddlewarePrefixScope(t *testing.T) {
	policy := CORSPolicy{AllowedOrigins: []string{"https://admin.example.com"}}
	called := false
	handler := CORSMiddleware(policy, "/__aisys__", "/__aipublic__", "/__aidelegated__/v1")(corsTestHandler(&called))

	// /v1 网关链在 CORS 作用域之外：即使 Origin 在白名单也不写头。
	request := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	request.Header.Set("Origin", "https://admin.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatal("out-of-scope requests must reach the handler")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("the /v1 gateway chain must stay outside the CORS surface")
	}

	// 管理面前缀命中（含更深层路径）。
	called = false
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil)
	request.Header.Set("Origin", "https://admin.example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://admin.example.com" {
		t.Fatal("management prefix requests must receive CORS headers")
	}

	// 相邻前缀不命中（Express app.use 语义）。
	called = false
	request = httptest.NewRequest(http.MethodGet, "/__aidelegated__/v1x", nil)
	request.Header.Set("Origin", "https://admin.example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("an adjacent prefix must not match the CORS surface")
	}

	// allow-any 策略（非生产缺省）：任意 Origin 反射。
	anyHandler := CORSMiddleware(CORSPolicy{AllowAnyOrigin: true})(corsTestHandler(&called))
	request = httptest.NewRequest(http.MethodGet, "/__aipublic__/integrations", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	recorder = httptest.NewRecorder()
	anyHandler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("allow-any must reflect the request origin, got %q", got)
	}
}
