package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAPIKeySecretHandlersResolveAdminAndSelfScopes(t *testing.T) {
	t.Run("admin all owners", func(t *testing.T) {
		service := &managementAPIKeySecretServiceStub{
			revealResult: managementapikeys.SecretResult{Key: "sk-secret"},
		}
		handler := newManagementAPIKeySecretHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{})
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/api-keys/key_1/secret?systemAccountId=%20all%20",
			nil,
		)
		req = requestWithManagementAPIKeyID(req, "key_1")
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if service.revealInput != (managementapikeys.SecretInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
			APIKeyID:             "key_1",
		}) {
			t.Fatalf("input = %+v", service.revealInput)
		}
	})

	t.Run("self ignores forged owner", func(t *testing.T) {
		service := &managementAPIKeySecretServiceStub{
			revealResult: managementapikeys.SecretResult{Key: "sk-secret"},
		}
		handler := newManagementAPIKeySecretHandler(service, managementAPIKeyScopeSelf, managementOperationLogOptions{})
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/my-api-keys/key_self/secret?systemAccountId=sys_forged",
			nil,
		)
		req = requestWithManagementAPIKeyID(req, "key_self")
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_current",
			Role:            "admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if service.revealInput.SystemAccountID != "sys_current" ||
			!service.revealInput.SelfOnly ||
			service.revealInput.ActorSystemAccountID != "sys_current" {
			t.Fatalf("input = %+v", service.revealInput)
		}
	})
}

func TestManagementAPIKeySecretHandlerReturnsNoStoreEnvelopeAndMarkerOnlyOperationLog(t *testing.T) {
	const plaintext = "sk-plaintext-must-never-enter-operation-log"
	queueStub := &managementAPIKeySecretOperationLogQueueStub{}
	service := &managementAPIKeySecretServiceStub{
		revealResult: managementapikeys.SecretResult{
			Key:                  plaintext,
			APIKeyID:             "key_1",
			OwnerSystemAccountID: "sys_owner",
			Name:                 "生产 Key",
			KeyMarker:            "sk-plain...tion-log",
		},
	}
	handler := newManagementAPIKeySecretHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			Now:      func() time.Time { return time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC) },
			NewLogID: func() string { return "oplog_reveal" },
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/api-keys/key_1/secret", nil)
	req = requestWithManagementAPIKeyID(req, "key_1")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("headers = %#v", rec.Header())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data["key"] != plaintext {
		t.Fatalf("response = %#v", envelope.Data)
	}
	logInput := queueStub.requireInput(t)
	if logInput.OperationKey != "api_keys.reveal_secret" ||
		logInput.Action != "reveal_secret" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		len(logInput.Changes) != 1 ||
		logInput.Changes[0].After != "sk-plain...tion-log" {
		t.Fatalf("operation log = %+v", logInput)
	}
	if strings.Contains(string(queueStub.payload), plaintext) {
		t.Fatalf("operation log payload leaked plaintext: %s", queueStub.payload)
	}
}

func TestManagementAPIKeySecretHandlerMapsPermissionsAndFailures(t *testing.T) {
	t.Run("admin route requires admin", func(t *testing.T) {
		service := &managementAPIKeySecretServiceStub{}
		handler := newManagementAPIKeySecretHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{})
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/api-keys/key_1/secret", nil)
		req = requestWithManagementAPIKeyID(req, "key_1")
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden || service.revealCalls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.revealCalls, rec.Body.String())
		}
	})

	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "missing or unauthorized", err: managementapikeys.ErrAPIKeyNotFound, wantStatus: http.StatusNotFound},
		{name: "null ciphertext", err: managementapikeys.ErrAPIKeySecretUnavailable, wantStatus: http.StatusInternalServerError},
		{name: "decrypt failure", err: errors.New("decrypt failed"), wantStatus: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeySecretServiceStub{revealErr: test.err}
			queueStub := &managementAPIKeySecretOperationLogQueueStub{}
			handler := newManagementAPIKeySecretHandler(
				service,
				managementAPIKeyScopeAdmin,
				newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
			)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/api-keys/key_1/secret", nil)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "super_admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if queueStub.calls != 0 {
				t.Fatalf("operation log calls = %d, want 0", queueStub.calls)
			}
		})
	}
}

func TestManagementAPIKeyRefreshHandlerAcceptsOptionalEmptyObjectAndReturnsFullSummary(t *testing.T) {
	for _, body := range []string{"", "{}", " \r\n { } \t"} {
		t.Run(body, func(t *testing.T) {
			queueStub := &managementAPIKeySecretOperationLogQueueStub{}
			service := &managementAPIKeySecretServiceStub{
				refreshResult: managementapikeys.RefreshResult{
					ListItem: managementapikeys.ListItem{
						ID:                  "key_1",
						SystemAccountID:     "sys_owner",
						SystemAccountName:   "所有者",
						Name:                "生产 Key",
						KeyPrefix:           "sk-refre",
						KeySuffix:           "23456789",
						Status:              "active",
						RouteStrategyID:     "route_1",
						RouteStrategyName:   "默认策略",
						RouteStrategyMode:   "normal",
						RouteStrategyStatus: "active",
						QuotaLimits:         port.ManagementRequestQuotaLimits{},
						Usage:               port.ManagementAccountUsageSummary{RequestCount: 3},
					},
					Key:                  "sk-refreshed-secret-0123456789",
					OwnerSystemAccountID: "sys_owner",
					PreviousKeyMarker:    "sk-before...before",
					KeyMarker:            "sk-refre...23456789",
				},
			}
			handler := newManagementAPIKeyRefreshHandler(
				service,
				managementAPIKeyScopeAdmin,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client:   queueStub,
					NewLogID: func() string { return "oplog_refresh" },
				}),
			)
			req := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
				strings.NewReader(body),
			)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Username:        "admin",
				DisplayName:     "管理员",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
				t.Fatalf("headers = %#v", rec.Header())
			}
			if service.refreshInput.APIKeyID != "key_1" ||
				service.refreshInput.SystemAccountID != "sys_owner" ||
				service.refreshInput.SelfOnly {
				t.Fatalf("input = %+v", service.refreshInput)
			}
			var envelope struct {
				Data    map[string]any `json:"data"`
				Message string         `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if envelope.Message != "API Key 密钥已刷新，请立即复制完整密钥" ||
				envelope.Data["id"] != "key_1" ||
				envelope.Data["systemAccountId"] != "sys_owner" ||
				envelope.Data["key"] != "sk-refreshed-secret-0123456789" ||
				envelope.Data["keySecretEncrypted"] != nil {
				t.Fatalf("response = %#v message=%q", envelope.Data, envelope.Message)
			}
			logInput := queueStub.requireInput(t)
			if logInput.OperationKey != "api_keys.refresh_key" ||
				len(logInput.Changes) != 1 ||
				logInput.Changes[0].Before != "sk-before...before" ||
				logInput.Changes[0].After != "sk-refre...23456789" {
				t.Fatalf("operation log = %+v", logInput)
			}
			if strings.Contains(string(queueStub.payload), "sk-refreshed-secret-0123456789") {
				t.Fatalf("operation log leaked refreshed secret: %s", queueStub.payload)
			}
		})
	}
}

func TestManagementAPIKeyRefreshHandlerForcesSelfScopeAndHidesOwnerFields(t *testing.T) {
	service := &managementAPIKeySecretServiceStub{
		refreshResult: managementapikeys.RefreshResult{
			ListItem: managementapikeys.ListItem{
				ID:              "key_self",
				Name:            "个人 Key",
				KeyPrefix:       "sk-self-",
				KeySuffix:       "23456789",
				Status:          "active",
				RouteStrategyID: "route_self",
				QuotaLimits:     port.ManagementRequestQuotaLimits{},
				Usage:           port.ManagementAccountUsageSummary{},
			},
			Key:                  "sk-self-refreshed-0123456789",
			OwnerSystemAccountID: "sys_current",
			PreviousKeyMarker:    "sk-before...before",
			KeyMarker:            "sk-self-...23456789",
		},
	}
	handler := newManagementAPIKeyRefreshHandler(service, managementAPIKeyScopeSelf, managementOperationLogOptions{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/my-api-keys/key_self/refresh-key?systemAccountId=sys_forged",
		strings.NewReader("{}"),
	)
	req = requestWithManagementAPIKeyID(req, "key_self")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_current",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.refreshInput.SystemAccountID != "sys_current" || !service.refreshInput.SelfOnly {
		t.Fatalf("input = %+v", service.refreshInput)
	}
	for _, forbidden := range []string{"systemAccountId", "systemAccountName", "keySecretEncrypted", "keyHash"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("self response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestManagementAPIKeyRefreshHandlerRejectsInvalidBodyBeforeService(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "array", body: "[]", wantStatus: http.StatusBadRequest},
		{name: "nonempty object", body: `{"unexpected":true}`, wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: "{} {}", wantStatus: http.StatusBadRequest},
		{name: "malformed", body: "{", wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"x":"` + strings.Repeat("a", 256<<10) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeySecretServiceStub{}
			handler := newManagementAPIKeyRefreshHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys/key_1/refresh-key", strings.NewReader(test.body))
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus || service.refreshCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.refreshCalls, rec.Body.String())
			}
		})
	}
}

func TestManagementAPIKeyRefreshHandlerMapsNotFoundAndCommittedInvalidationFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "missing or unauthorized", err: managementapikeys.ErrAPIKeyNotFound, wantStatus: http.StatusNotFound},
		{name: "validation invalidation after commit", err: errors.New("validation invalidation failed"), wantStatus: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeySecretServiceStub{refreshErr: test.err}
			queueStub := &managementAPIKeySecretOperationLogQueueStub{}
			handler := newManagementAPIKeyRefreshHandler(
				service,
				managementAPIKeyScopeAdmin,
				newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
			)
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys/key_1/refresh-key", nil)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if queueStub.calls != 0 {
				t.Fatalf("operation log calls = %d, want 0", queueStub.calls)
			}
		})
	}
}

func TestManagementAPIKeyRefreshOperationLogIsBestEffortAfterServiceSuccess(t *testing.T) {
	events := []string{}
	service := &managementAPIKeySecretServiceStub{
		refreshResult: managementapikeys.RefreshResult{
			ListItem: managementapikeys.ListItem{
				ID:              "key_1",
				Name:            "生产 Key",
				KeyPrefix:       "sk-refre",
				KeySuffix:       "23456789",
				Status:          "active",
				RouteStrategyID: "route_1",
				QuotaLimits:     port.ManagementRequestQuotaLimits{},
				Usage:           port.ManagementAccountUsageSummary{},
			},
			Key:                  "sk-refreshed-secret-0123456789",
			OwnerSystemAccountID: "sys_owner",
			KeyMarker:            "sk-refre...23456789",
		},
		events: &events,
	}
	queueStub := &managementAPIKeySecretOperationLogQueueStub{
		err:    errors.New("queue unavailable"),
		events: &events,
	}
	handler := newManagementAPIKeyRefreshHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys/key_1/refresh-key", nil)
	req = requestWithManagementAPIKeyID(req, "key_1")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got, want := strings.Join(events, ","), "refresh,operation_log"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log calls = %d, want 1", queueStub.calls)
	}
}

func TestRouterRegistersManagementAPIKeySecretRoutesWithCorrectAuthBodyAndMutationGuard(t *testing.T) {
	service := &managementAPIKeySecretServiceStub{
		revealResult: managementapikeys.SecretResult{Key: "sk-secret"},
		refreshResult: managementapikeys.RefreshResult{
			ListItem: managementapikeys.ListItem{
				ID:              "key_1",
				Name:            "Key",
				KeyPrefix:       "sk-refre",
				KeySuffix:       "23456789",
				Status:          "active",
				RouteStrategyID: "route_1",
				QuotaLimits:     port.ManagementRequestQuotaLimits{},
				Usage:           port.ManagementAccountUsageSummary{},
			},
			Key: "sk-refreshed-secret",
		},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_read",
		},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_touch",
		},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementAPIKeySecretHandler:    newManagementAPIKeySecretHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{}),
		ManagementMyAPIKeySecretHandler:  newManagementAPIKeySecretHandler(service, managementAPIKeyScopeSelf, managementOperationLogOptions{}),
		ManagementAPIKeyRefreshHandler:   newManagementAPIKeyRefreshHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{}),
		ManagementMyAPIKeyRefreshHandler: newManagementAPIKeyRefreshHandler(service, managementAPIKeyScopeSelf, managementOperationLogOptions{}),
	})

	for _, path := range []string{
		"/__aisys__/api/api-keys/key_1/secret",
		"/__aisys__/api/my-api-keys/key_1/secret?systemAccountId=sys_forged",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("read auth=%q touch auth=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}

	for _, path := range []string{
		"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
		"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_other",
		"/__aisys__/api/my-api-keys/key_1/refresh-key?systemAccountId=sys_forged",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	if touchAuthenticator.touchCookieHeader == "" {
		t.Fatal("refresh routes did not use touch auth")
	}

	duplicate := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/my-api-keys/key_1/refresh-key?systemAccountId=different-forged-owner",
		strings.NewReader("{}"),
	)
	duplicate.Header.Set("Cookie", "juhe_ai_session=session-token")
	duplicateRec := httptest.NewRecorder()
	router.ServeHTTP(duplicateRec, duplicate)
	if duplicateRec.Code != http.StatusConflict {
		t.Fatalf("duplicate self refresh status = %d, body = %s", duplicateRec.Code, duplicateRec.Body.String())
	}
	if service.revealCalls != 2 || service.refreshCalls != 3 {
		t.Fatalf("reveal calls=%d refresh calls=%d", service.revealCalls, service.refreshCalls)
	}
}

func TestRouterDoesNotRegisterManagementAPIKeySecretRoutesWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"key": "sk-secret"})
	})
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIKeySecretHandler:    handler,
		ManagementMyAPIKeySecretHandler:  handler,
		ManagementAPIKeyRefreshHandler:   handler,
		ManagementMyAPIKeyRefreshHandler: handler,
	})
	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/__aisys__/api/api-keys/key_1/secret"},
		{method: http.MethodGet, path: "/__aisys__/api/my-api-keys/key_1/secret"},
		{method: http.MethodPost, path: "/__aisys__/api/api-keys/key_1/refresh-key"},
		{method: http.MethodPost, path: "/__aisys__/api/my-api-keys/key_1/refresh-key"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", request.method, request.path, rec.Code)
		}
	}
}

type managementAPIKeySecretServiceStub struct {
	revealInput   managementapikeys.SecretInput
	refreshInput  managementapikeys.SecretInput
	revealResult  managementapikeys.SecretResult
	refreshResult managementapikeys.RefreshResult
	revealErr     error
	refreshErr    error
	revealCalls   int
	refreshCalls  int
	events        *[]string
}

func requestWithManagementAPIKeyID(req *http.Request, id string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func (s *managementAPIKeySecretServiceStub) Reveal(
	_ *http.Request,
	input managementapikeys.SecretInput,
) (managementapikeys.SecretResult, error) {
	s.revealCalls++
	s.revealInput = input
	return s.revealResult, s.revealErr
}

func (s *managementAPIKeySecretServiceStub) Refresh(
	_ *http.Request,
	input managementapikeys.SecretInput,
) (managementapikeys.RefreshResult, error) {
	s.refreshCalls++
	s.refreshInput = input
	if s.events != nil {
		*s.events = append(*s.events, "refresh")
	}
	return s.refreshResult, s.refreshErr
}

type managementAPIKeySecretOperationLogQueueStub struct {
	calls    int
	taskType string
	payload  []byte
	err      error
	events   *[]string
}

func (s *managementAPIKeySecretOperationLogQueueStub) Enqueue(
	_ context.Context,
	taskType string,
	payload []byte,
	_ queue.EnqueueOptions,
) (queue.TaskInfo, error) {
	s.calls++
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	if s.events != nil {
		*s.events = append(*s.events, "operation_log")
	}
	return queue.TaskInfo{ID: "task_1"}, s.err
}

func (s *managementAPIKeySecretOperationLogQueueStub) requireInput(t *testing.T) port.OperationLogInput {
	t.Helper()
	if s.calls != 1 || s.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("queue calls=%d taskType=%q", s.calls, s.taskType)
	}
	input, err := operationlogjob.DecodeWriteTaskPayload(s.payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	return input
}

var _ managementAPIKeySecretService = (*managementAPIKeySecretServiceStub)(nil)
var _ operationlogjob.EnqueueClient = (*managementAPIKeySecretOperationLogQueueStub)(nil)
