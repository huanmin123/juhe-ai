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

func TestManagementAPIKeySecretHandlersValidateAdminScopeQueryAndIgnoreSelfQuery(t *testing.T) {
	for _, endpoint := range []struct {
		name   string
		method string
		path   string
		call   func(http.Handler, http.ResponseWriter, *http.Request)
		new    func(*managementAPIKeySecretServiceStub, managementAPIKeyScope) http.Handler
		calls  func(*managementAPIKeySecretServiceStub) int
		input  func(*managementAPIKeySecretServiceStub) managementapikeys.SecretInput
	}{
		{
			name:   "reveal",
			method: http.MethodGet,
			path:   "/__aisys__/api/api-keys/key_1/secret",
			call:   func(handler http.Handler, w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) },
			new: func(service *managementAPIKeySecretServiceStub, scope managementAPIKeyScope) http.Handler {
				return newManagementAPIKeySecretHandler(service, scope, managementOperationLogOptions{})
			},
			calls: func(service *managementAPIKeySecretServiceStub) int { return service.revealCalls },
			input: func(service *managementAPIKeySecretServiceStub) managementapikeys.SecretInput {
				return service.revealInput
			},
		},
		{
			name:   "refresh",
			method: http.MethodPost,
			path:   "/__aisys__/api/api-keys/key_1/refresh-key",
			call:   func(handler http.Handler, w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) },
			new: func(service *managementAPIKeySecretServiceStub, scope managementAPIKeyScope) http.Handler {
				return newManagementAPIKeyRefreshHandler(service, scope, managementOperationLogOptions{})
			},
			calls: func(service *managementAPIKeySecretServiceStub) int { return service.refreshCalls },
			input: func(service *managementAPIKeySecretServiceStub) managementapikeys.SecretInput {
				return service.refreshInput
			},
		},
	} {
		t.Run(endpoint.name+" admin query", func(t *testing.T) {
			for _, test := range []struct {
				name             string
				rawQuery         string
				wantStatus       int
				wantSystemAcctID string
				wantMessage      string
			}{
				{name: "absent", wantStatus: http.StatusOK},
				{name: "all", rawQuery: "systemAccountId=%20all%20", wantStatus: http.StatusOK},
				{name: "single trimmed", rawQuery: "systemAccountId=%20sys_owner%20", wantStatus: http.StatusOK, wantSystemAcctID: "sys_owner"},
				{name: "empty", rawQuery: "systemAccountId=", wantStatus: http.StatusBadRequest, wantMessage: "系统账号 ID 不能为空"},
				{name: "blank", rawQuery: "systemAccountId=%20%20", wantStatus: http.StatusBadRequest, wantMessage: "系统账号 ID 不能为空"},
				{name: "repeated", rawQuery: "systemAccountId=sys_a&systemAccountId=sys_b", wantStatus: http.StatusBadRequest, wantMessage: "Expected string, received array"},
			} {
				t.Run(test.name, func(t *testing.T) {
					service := &managementAPIKeySecretServiceStub{
						revealResult:  managementapikeys.SecretResult{Key: "sk-secret"},
						refreshResult: managementapikeys.RefreshResult{Key: "sk-secret"},
					}
					handler := endpoint.new(service, managementAPIKeyScopeAdmin)
					target := endpoint.path
					if test.rawQuery != "" {
						target += "?" + test.rawQuery
					}
					req := httptest.NewRequest(endpoint.method, target, nil)
					req = requestWithManagementAPIKeyID(req, "key_1")
					req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
						SystemAccountID: "sys_admin",
						Role:            "admin",
					})
					rec := httptest.NewRecorder()

					endpoint.call(handler, rec, req)

					if rec.Code != test.wantStatus {
						t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
					}
					wantCalls := 0
					if test.wantStatus == http.StatusOK {
						wantCalls = 1
					}
					if got := endpoint.calls(service); got != wantCalls {
						t.Fatalf("service calls = %d, want %d", got, wantCalls)
					}
					if test.wantMessage != "" {
						var body map[string]string
						if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
							t.Fatalf("decode response: %v", err)
						}
						if body["message"] != test.wantMessage {
							t.Fatalf("message = %q, want %q", body["message"], test.wantMessage)
						}
					}
					if wantCalls == 1 && endpoint.input(service).SystemAccountID != test.wantSystemAcctID {
						t.Fatalf("input = %+v, want systemAccountId %q", endpoint.input(service), test.wantSystemAcctID)
					}
				})
			}
		})

		t.Run(endpoint.name+" self query ignored", func(t *testing.T) {
			service := &managementAPIKeySecretServiceStub{
				revealResult:  managementapikeys.SecretResult{Key: "sk-secret"},
				refreshResult: managementapikeys.RefreshResult{Key: "sk-secret"},
			}
			handler := endpoint.new(service, managementAPIKeyScopeSelf)
			req := httptest.NewRequest(
				endpoint.method,
				strings.Replace(endpoint.path, "/api-keys/", "/my-api-keys/", 1)+"?systemAccountId=&systemAccountId=sys_forged",
				nil,
			)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_current",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			endpoint.call(handler, rec, req)

			if rec.Code != http.StatusOK || endpoint.calls(service) != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, endpoint.calls(service), rec.Body.String())
			}
			input := endpoint.input(service)
			if input.SystemAccountID != "sys_current" || !input.SelfOnly {
				t.Fatalf("input = %+v", input)
			}
		})
	}
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

func TestManagementAPIKeyRefreshHandlerDoesNotApplyBodySchemaAndReturnsFullSummary(t *testing.T) {
	for _, body := range []string{"", "{}", `{"unexpected":true}`, "[]"} {
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

func TestRouterManagementAPIKeyRefreshMatchesExpressJSONAndMiddlewareOrder(t *testing.T) {
	t.Run("accepted JSON shapes reach auth limiter and handler in order", func(t *testing.T) {
		for _, test := range []struct {
			name string
			body string
		}{
			{name: "empty body"},
			{name: "empty object", body: "{}"},
			{name: "object with fields", body: `{"unexpected":true}`},
			{name: "array", body: `["accepted"]`},
		} {
			t.Run(test.name, func(t *testing.T) {
				events := []string{}
				fixture := newManagementAPIKeyRefreshRouterFixture(t, "admin", &events)
				req := httptest.NewRequest(
					http.MethodPost,
					"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
					strings.NewReader(test.body),
				)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Cookie", "juhe_ai_session=session-token")
				rec := httptest.NewRecorder()

				fixture.router.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
				if got, want := strings.Join(events, ","), "ip_limit,touch_auth,user_limit,refresh"; got != want {
					t.Fatalf("events = %q, want %q", got, want)
				}
				if fixture.service.refreshCalls != 1 {
					t.Fatalf("refresh calls = %d, want 1", fixture.service.refreshCalls)
				}
				assertManagementAPIKeyNoStore(t, rec)
			})
		}
	})

	t.Run("strict JSON rejection happens before auth user limit and mutation claim", func(t *testing.T) {
		for _, test := range []struct {
			name       string
			body       string
			wantStatus int
		}{
			{name: "null", body: "null", wantStatus: http.StatusBadRequest},
			{name: "number", body: "1", wantStatus: http.StatusBadRequest},
			{name: "string", body: `"text"`, wantStatus: http.StatusBadRequest},
			{name: "malformed", body: "{", wantStatus: http.StatusBadRequest},
			{name: "trailing JSON", body: "{} {}", wantStatus: http.StatusBadRequest},
			{
				name:       "oversized",
				body:       `{"value":"` + strings.Repeat("x", managementAPIKeyRefreshMaxBodyBytes) + `"}`,
				wantStatus: http.StatusRequestEntityTooLarge,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				events := []string{}
				fixture := newManagementAPIKeyRefreshRouterFixture(t, "admin", &events)
				req := httptest.NewRequest(
					http.MethodPost,
					"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
					strings.NewReader(test.body),
				)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Cookie", "juhe_ai_session=session-token")
				rec := httptest.NewRecorder()

				fixture.router.ServeHTTP(rec, req)

				if rec.Code != test.wantStatus {
					t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
				}
				if fixture.auth.touchCalls != 0 || fixture.userLimiter.calls != 0 || fixture.service.refreshCalls != 0 {
					t.Fatalf(
						"touch=%d userLimit=%d refresh=%d, want parser rejection before all three",
						fixture.auth.touchCalls,
						fixture.userLimiter.calls,
						fixture.service.refreshCalls,
					)
				}
				if fixture.ipLimiter.calls != 1 || strings.Join(events, ",") != "ip_limit" {
					t.Fatalf("IP calls=%d events=%q", fixture.ipLimiter.calls, strings.Join(events, ","))
				}
				assertManagementAPIKeyNoStore(t, rec)

				validReq := httptest.NewRequest(
					http.MethodPost,
					"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
					strings.NewReader("{}"),
				)
				validReq.Header.Set("Content-Type", "application/json")
				validReq.Header.Set("Cookie", "juhe_ai_session=session-token")
				validRec := httptest.NewRecorder()
				fixture.router.ServeHTTP(validRec, validReq)
				if validRec.Code != http.StatusOK || fixture.service.refreshCalls != 1 {
					t.Fatalf(
						"valid retry status=%d refresh=%d body=%s; parser rejection must not claim mutation",
						validRec.Code,
						fixture.service.refreshCalls,
						validRec.Body.String(),
					)
				}
			})
		}
	})

	t.Run("non JSON content type is skipped", func(t *testing.T) {
		for _, contentType := range []string{"text/plain", "application/problem+json"} {
			t.Run(contentType, func(t *testing.T) {
				events := []string{}
				fixture := newManagementAPIKeyRefreshRouterFixture(t, "admin", &events)
				req := httptest.NewRequest(
					http.MethodPost,
					"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
					strings.NewReader("not-json"),
				)
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Cookie", "juhe_ai_session=session-token")
				rec := httptest.NewRecorder()

				fixture.router.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK || fixture.service.refreshCalls != 1 {
					t.Fatalf("status=%d refresh=%d body=%s", rec.Code, fixture.service.refreshCalls, rec.Body.String())
				}
				if got, want := strings.Join(events, ","), "ip_limit,touch_auth,user_limit,refresh"; got != want {
					t.Fatalf("events = %q, want %q", got, want)
				}
			})
		}
	})

	t.Run("non admin authenticates and is limited before role rejection without mutation claim", func(t *testing.T) {
		events := []string{}
		fixture := newManagementAPIKeyRefreshRouterFixture(t, "user", &events)
		for attempt := 1; attempt <= 2; attempt++ {
			req := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=sys_owner",
				strings.NewReader("{}"),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()

			fixture.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("attempt %d status = %d, want 403; body = %s", attempt, rec.Code, rec.Body.String())
			}
			assertManagementAPIKeyNoStore(t, rec)
		}
		if fixture.auth.touchCalls != 2 || fixture.userLimiter.calls != 2 || fixture.service.refreshCalls != 0 {
			t.Fatalf(
				"touch=%d userLimit=%d refresh=%d",
				fixture.auth.touchCalls,
				fixture.userLimiter.calls,
				fixture.service.refreshCalls,
			)
		}
	})

	t.Run("admin query validation remains after auth limiter and mutation guard", func(t *testing.T) {
		events := []string{}
		fixture := newManagementAPIKeyRefreshRouterFixture(t, "admin", &events)
		for attempt, wantStatus := range []int{http.StatusBadRequest, http.StatusConflict} {
			req := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/api-keys/key_1/refresh-key?systemAccountId=",
				strings.NewReader("{}"),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()

			fixture.router.ServeHTTP(rec, req)

			if rec.Code != wantStatus {
				t.Fatalf("attempt %d status = %d, want %d; body = %s", attempt+1, rec.Code, wantStatus, rec.Body.String())
			}
		}
		if fixture.auth.touchCalls != 2 || fixture.userLimiter.calls != 2 || fixture.service.refreshCalls != 0 {
			t.Fatalf(
				"touch=%d userLimit=%d refresh=%d",
				fixture.auth.touchCalls,
				fixture.userLimiter.calls,
				fixture.service.refreshCalls,
			)
		}
	})
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

type managementAPIKeyRefreshRouterFixture struct {
	router      http.Handler
	service     *managementAPIKeySecretServiceStub
	auth        *managementAPIKeyRefreshAuthStub
	ipLimiter   *managementAPIKeyRefreshIPLimiterStub
	userLimiter *managementAPIKeyRefreshUserLimiterStub
}

func newManagementAPIKeyRefreshRouterFixture(
	t *testing.T,
	role string,
	events *[]string,
) managementAPIKeyRefreshRouterFixture {
	t.Helper()
	service := &managementAPIKeySecretServiceStub{
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
		events: events,
	}
	auth := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_actor",
			Username:        "actor",
			Role:            role,
			SessionID:       "sess_actor",
		},
		events: events,
	}
	ipLimiter := &managementAPIKeyRefreshIPLimiterStub{events: events}
	userLimiter := &managementAPIKeyRefreshUserLimiterStub{events: events}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(auth),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(auth),
		ManagementAPIKeyRefreshHandler:    newManagementAPIKeyRefreshHandler(service, managementAPIKeyScopeAdmin, managementOperationLogOptions{}),
	})
	return managementAPIKeyRefreshRouterFixture{
		router:      router,
		service:     service,
		auth:        auth,
		ipLimiter:   ipLimiter,
		userLimiter: userLimiter,
	}
}

func assertManagementAPIKeyNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

type managementAPIKeyRefreshAuthStub struct {
	context    managementauth.Context
	events     *[]string
	readCalls  int
	touchCalls int
}

func (s *managementAPIKeyRefreshAuthStub) AuthenticateCookie(
	context.Context,
	string,
) (managementauth.Context, error) {
	s.readCalls++
	if s.events != nil {
		*s.events = append(*s.events, "read_auth")
	}
	return s.context, nil
}

func (s *managementAPIKeyRefreshAuthStub) AuthenticateCookieAndTouch(
	context.Context,
	string,
) (managementauth.Context, error) {
	s.touchCalls++
	if s.events != nil {
		*s.events = append(*s.events, "touch_auth")
	}
	return s.context, nil
}

type managementAPIKeyRefreshIPLimiterStub struct {
	events *[]string
	calls  int
}

func (s *managementAPIKeyRefreshIPLimiterStub) AllowSystemAPIIP(
	context.Context,
	string,
	SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	s.calls++
	if s.events != nil {
		*s.events = append(*s.events, "ip_limit")
	}
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type managementAPIKeyRefreshUserLimiterStub struct {
	events *[]string
	calls  int
}

func (s *managementAPIKeyRefreshUserLimiterStub) AllowSystemAPIAuthenticated(
	context.Context,
	string,
	int,
) (SystemAPIRateLimitDecision, error) {
	s.calls++
	if s.events != nil {
		*s.events = append(*s.events, "user_limit")
	}
	return SystemAPIRateLimitDecision{Allowed: true}, nil
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
