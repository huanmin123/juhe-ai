package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRouterDoesNotExposeOrRegisterManagementSessionRoutes(t *testing.T) {
	listHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	revokeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementCurrentUserHandler:     listHandler,
	}
	value := reflect.ValueOf(&opts).Elem()
	if field := value.FieldByName("ManagementSessionListHandler"); field.IsValid() {
		field.Set(reflect.ValueOf(listHandler))
	}
	if field := value.FieldByName("ManagementSessionRevokeHandler"); field.IsValid() {
		field.Set(reflect.ValueOf(revokeHandler))
	}

	router := NewRouter(opts)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/__aisys__/api/auth/sessions"},
		{method: http.MethodDelete, path: "/__aisys__/api/auth/sessions/session-id"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}

	typeOfOptions := reflect.TypeOf(RouterOptions{})
	for _, name := range []string{"ManagementSessionListHandler", "ManagementSessionRevokeHandler"} {
		if _, ok := typeOfOptions.FieldByName(name); ok {
			t.Fatalf("RouterOptions still exposes %s", name)
		}
	}
}

func TestManagementAPIAuthMiddlewareInjectsContext(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	var got managementauth.Context
	handler := NewManagementAPIAuthMiddleware(authenticator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		got, ok = ManagementAuthContextFromRequest(r)
		if !ok {
			t.Fatal("management auth context missing")
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	if got.SystemAccountID != "sys_admin" || got.Role != "admin" || got.SessionID != "sess_admin" {
		t.Fatalf("context = %+v", got)
	}
}

func TestManagementAPIAuthMiddlewareWritesAuthErrors(t *testing.T) {
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		err: &managementauth.AuthError{
			StatusCode: http.StatusForbidden,
			Code:       managementauth.ErrorCodeMustChangePassword,
			Message:    "请先修改初始密码",
		},
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "请先修改初始密码" || body["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAPIAuthMiddlewareRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
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

func TestManagementAPIAuthTouchMiddlewareInjectsContext(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handler := NewManagementAPIAuthTouchMiddleware(authenticator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ManagementAuthContextFromRequest(r); !ok {
			t.Fatal("management auth context missing")
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch cookie header = %q", authenticator.touchCookieHeader)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie header = %q, want empty", authenticator.cookieHeader)
	}
}

func TestManagementCurrentUserHandlerReturnsSessionUser(t *testing.T) {
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID:    "sys_user",
			Username:           "user",
			DisplayName:        "用户",
			Role:               "user",
			MustChangePassword: true,
			SessionID:          "sess_user",
		},
	}
	handler := NewManagementCurrentUserHandler(authenticator)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementCurrentUserResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || body.Data.Role != "user" || !body.Data.MustChangePassword {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCurrentUserHandlerWritesAuthErrors(t *testing.T) {
	handler := NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{
		err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"},
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "请先登录" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementCurrentUserHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
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

func TestManagementLogoutHandlerRevokesSessionAndClearsCookie(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	handler := NewManagementLogoutHandler(authenticator, config.Config{
		CookieSecure:   true,
		CookieSameSite: "none",
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementLogoutResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Data.LoggedOut {
		t.Fatalf("body = %+v", body)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"juhe_ai_session=", "Max-Age=0", "Path=/", "HttpOnly", "Secure", "SameSite=None"} {
		if !strings.Contains(setCookie, part) {
			t.Fatalf("Set-Cookie = %q, want contains %q", setCookie, part)
		}
	}
}

func TestManagementLogoutHandlerAllowsMissingCookie(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	handler := NewManagementLogoutHandler(authenticator, config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementLogoutResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Data.LoggedOut {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementLogoutHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := NewManagementLogoutHandler(&managementLogoutAuthenticatorStub{
		err: errors.New("postgres password leaked"),
	}, config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
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
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie = %q, want empty on revoke failure", rec.Header().Get("Set-Cookie"))
	}
}

func TestRouterRegistersManagementCurrentUserWhenEnabled(t *testing.T) {
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(authenticator),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
}

func TestRouterDoesNotRegisterManagementCurrentUserWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func TestRouterRegistersManagementLogoutWhenEnabled(t *testing.T) {
	authenticator := &managementLogoutAuthenticatorStub{}
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLogoutHandler:      NewManagementLogoutHandler(authenticator, config.Config{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
}

func TestRouterAppliesIPWriteRateLimitBeforeManagementLogin(t *testing.T) {
	limiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 5,
		},
	}
	loginCalls := 0
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLoginHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			loginCalls++
			writeData(w, http.StatusOK, map[string]string{"ok": "login"})
		}),
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPWritePerMinute:         1,
				IPWriteBurstPer10Seconds: 1,
			},
		},
		SystemAPIIPRateLimiter: limiter,
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(`{}`))
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s, want 429", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if loginCalls != 0 {
		t.Fatalf("login handler calls = %d, want 0 while IP write limited", loginCalls)
	}
	if limiter.calls != 1 {
		t.Fatalf("system API IP limiter calls = %d, want 1", limiter.calls)
	}
	if limiter.settings.PerMinute != 1 || limiter.settings.BurstPer10Seconds != 1 {
		t.Fatalf("write limit settings = %+v, want write minute and burst values", limiter.settings)
	}
	if limiter.key != systemAPIIPRateLimitKey("203.0.113.10", systemAPIMethodWrite) {
		t.Fatalf("limiter key = %q, want hashed IP write key", limiter.key)
	}
}

func TestRouterUsesTouchMiddlewareOnlyForManagementWriteRoutes(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authContext, ok := ManagementAuthContextFromRequest(r); !ok || authContext.SessionID != "sess_read" {
			t.Fatalf("read route auth context = %+v ok=%v", authContext, ok)
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "read"})
	})
	profileHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authContext, ok := ManagementAuthContextFromRequest(r); !ok || authContext.SessionID != "sess_touch" {
			t.Fatalf("write route auth context = %+v ok=%v", authContext, ok)
		}
		writeData(w, http.StatusOK, map[string]string{"ok": "write"})
	})
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProxyOptionsHandler:    proxyHandler,
		ManagementProfileUpdateHandler:   profileHandler,
	})

	readReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	readReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	readRec := httptest.NewRecorder()
	router.ServeHTTP(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("read route status = %d, body = %s", readRec.Code, readRec.Body.String())
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("read route auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}

	writeReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{}`))
	writeReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	writeRec := httptest.NewRecorder()
	router.ServeHTTP(writeRec, writeReq)
	if writeRec.Code != http.StatusOK {
		t.Fatalf("write route status = %d, body = %s", writeRec.Code, writeRec.Body.String())
	}
	if touchAuthenticator.touchCookieHeader == "" {
		t.Fatal("write route did not use touch middleware")
	}
}

func TestRouterAppliesAuthenticatedUserReadRateLimitAfterManagementAuth(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	limiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 11,
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 300, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:            NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: limiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementProxyOptionsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]string{"ok": "read"})
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s, want 429", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "11" {
		t.Fatalf("Retry-After = %q, want 11", got)
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("read auth cookie header = %q", authenticator.cookieHeader)
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0 while rate limited", handlerCalls)
	}
	if limiter.calls != 1 || limiter.limit != 300 {
		t.Fatalf("limiter calls=%d limit=%d, want one call with read limit 300", limiter.calls, limiter.limit)
	}
	if limiter.key != systemAPIAuthenticatedRateLimitKey("sys_user", systemAPIMethodRead) {
		t.Fatalf("limiter key = %q, want hashed user read key", limiter.key)
	}
}

func TestRouterAppliesAuthenticatedUserWriteRateLimitAfterTouchAuth(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_touch",
		},
	}
	limiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 13,
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 300, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:            NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: limiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProxyTestHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]string{"ok": "write"})
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies/proxy_1/test", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s, want 429", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "13" {
		t.Fatalf("Retry-After = %q, want 13", got)
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch auth cookie header = %q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie header = %q, want empty for write route", readAuthenticator.cookieHeader)
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0 while rate limited", handlerCalls)
	}
	if limiter.calls != 1 || limiter.limit != 120 {
		t.Fatalf("limiter calls=%d limit=%d, want one call with write limit 120", limiter.calls, limiter.limit)
	}
	if limiter.key != systemAPIAuthenticatedRateLimitKey("sys_user", systemAPIMethodWrite) {
		t.Fatalf("limiter key = %q, want hashed user write key", limiter.key)
	}
}

func TestRouterSkipsAuthenticatedUserRateLimitForAuthProfileUpdate(t *testing.T) {
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_touch",
		},
	}
	limiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: 13,
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 1, UserWritePerMinute: 1}},
		SystemAPIIPRateLimiter:            NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: limiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementProfileUpdateHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]string{"ok": "profile"})
		}),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1 for auth profile route", handlerCalls)
	}
	if limiter.calls != 0 {
		t.Fatalf("authenticated limiter calls = %d, want 0 for /auth route", limiter.calls)
	}
}

func TestRouterRequiresAuthenticatedRateLimiterForLimitedBusinessRoutes(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic for business management route without authenticated user limiter")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:      systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:        NewInMemorySystemAPIIPRateLimiter(),
		ManagementAPIAuthMiddleware:   NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementProxyOptionsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
}

func TestInMemorySystemAPIAuthenticatedRateLimiterAppliesMinuteWindow(t *testing.T) {
	limiter := NewInMemorySystemAPIAuthenticatedRateLimiter()

	decision, err := limiter.AllowSystemAPIAuthenticated(context.Background(), "account-key", 1)
	if err != nil {
		t.Fatalf("first AllowSystemAPIAuthenticated() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("first decision = %+v, want allowed", decision)
	}

	decision, err = limiter.AllowSystemAPIAuthenticated(context.Background(), "account-key", 1)
	if err != nil {
		t.Fatalf("second AllowSystemAPIAuthenticated() error = %v", err)
	}
	if decision.Allowed || decision.RetryAfterSeconds <= 0 {
		t.Fatalf("second decision = %+v, want denied with retry-after", decision)
	}
}

func TestRouterRequiresTouchMiddlewareForManagementWriteRoutes(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic for management write route without touch middleware")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
		ManagementProfileUpdateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
}

func TestRouterDoesNotRegisterManagementLogoutWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIAuthMiddleware:  NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementLogoutHandler:      NewManagementLogoutHandler(&managementLogoutAuthenticatorStub{}, config.Config{}),
		ManagementCurrentUserHandler: NewManagementCurrentUserHandler(&managementCurrentUserAuthenticatorStub{}),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementAPIAuthenticatorStub struct {
	cookieHeader      string
	touchCookieHeader string
	context           managementauth.Context
	err               error
}

func (s *managementAPIAuthenticatorStub) AuthenticateCookie(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}

func (s *managementAPIAuthenticatorStub) AuthenticateCookieAndTouch(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.touchCookieHeader = cookieHeader
	return s.context, s.err
}

type managementCurrentUserAuthenticatorStub struct {
	cookieHeader      string
	touchCookieHeader string
	context           managementauth.Context
	err               error
}

func (s *managementCurrentUserAuthenticatorStub) AuthenticateCookieForCurrentUser(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.cookieHeader = cookieHeader
	return s.context, s.err
}

func (s *managementCurrentUserAuthenticatorStub) AuthenticateCookieForCurrentUserAndTouch(_ context.Context, cookieHeader string) (managementauth.Context, error) {
	s.touchCookieHeader = cookieHeader
	return s.context, s.err
}

type managementLogoutAuthenticatorStub struct {
	cookieHeader string
	err          error
}

func (s *managementLogoutAuthenticatorStub) LogoutCookie(_ context.Context, cookieHeader string) error {
	s.cookieHeader = cookieHeader
	return s.err
}

type systemAPIAuthenticatedRateLimiterStub struct {
	decision SystemAPIRateLimitDecision
	err      error
	key      string
	limit    int
	calls    int
}

func (s *systemAPIAuthenticatedRateLimiterStub) AllowSystemAPIAuthenticated(_ context.Context, key string, limit int) (SystemAPIRateLimitDecision, error) {
	s.calls++
	s.key = key
	s.limit = limit
	return s.decision, s.err
}
