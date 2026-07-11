package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicapikeys"
)

func TestPublicAPIKeyHandlersAddThroughShellRedactsLogSecret(t *testing.T) {
	secret := "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	service := &publicAPIKeyServiceStub{
		addResponse: publicapikeys.APIKeyResponse{
			Source:      "stats",
			GeneratedAt: "2026-07-07T10:00:00Z",
			Action:      "created",
			Target:      publicapikeys.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
			APIKey: &publicapikeys.APIKeySummary{
				ID: "key_1", Name: "公开 Key", KeyPrefix: "sk-01234", KeySuffix: "89abcdef", Key: secret,
				Status: publicapikeys.StatusActive, RouteStrategyID: "rts_1", RouteStrategyName: "公开策略", RouteStrategyMode: "normal", RouteStrategyStatus: publicapikeys.StatusActive,
			},
		},
	}
	authenticator := newPublicGroupAPIAuthStub()
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, newPublicAPIKeyHandlers(service), time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/api-key/add", strings.NewReader(`{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1","quotaLimits":{"daily":{"enabled":true,"limit":10}}}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.scope != publicapi.ScopeAPIKeyAddWrite || limiter.calls != 1 {
		t.Fatalf("auth scope/limiter = %q/%d", authenticator.scope, limiter.calls)
	}
	if service.addCalls != 1 || service.addInput.TargetUsername != "admin" || service.addInput.RouteStrategyID != "rts_1" || !service.addInput.QuotaLimits.Set() {
		t.Fatalf("add input = calls %d %+v", service.addCalls, service.addInput)
	}
	var body struct {
		Data publicapikeys.APIKeyResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.APIKey == nil || body.Data.APIKey.Key != secret {
		t.Fatalf("response api key = %+v", body.Data.APIKey)
	}

	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusCreated || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
	responseBody := log.ResponseData["body"].(map[string]any)
	data := responseBody["data"].(map[string]any)
	apiKey := data["apiKey"].(map[string]any)
	if apiKey["key"] != "[redacted]" {
		t.Fatalf("logged api key secret = %#v, want redacted", apiKey["key"])
	}
	if strings.Contains(fmt.Sprint(log.ResponseData), secret) {
		t.Fatalf("log response leaked secret: %#v", log.ResponseData)
	}
}

func TestPublicAPIKeyHandlersRejectStrictAndNonCoercedFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add unknown field", path: "/__aipublic__/api-key/add", body: `{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1","extra":1}`},
		{name: "quota limits must be object", path: "/__aipublic__/api-key/add", body: `{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1","quotaLimits":[]}`},
		{name: "status must be enum", path: "/__aipublic__/api-key/add", body: `{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1","status":"paused"}`},
		{name: "update empty mutable", path: "/__aipublic__/api-key/update", body: `{"apiKeyId":"key_1"}`},
		{name: "delete unknown field", path: "/__aipublic__/api-key/del", body: `{"apiKeyId":"key_1","extra":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicAPIKeyServiceStub{}
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicAPIKeyHandlers(service),
				time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if service.addCalls != 0 || service.updateCalls != 0 || service.deleteCalls != 0 {
				t.Fatalf("service calls = add %d update %d delete %d", service.addCalls, service.updateCalls, service.deleteCalls)
			}
		})
	}
}

func TestPublicAPIKeyHandlersCoerceListPagination(t *testing.T) {
	service := &publicAPIKeyServiceStub{listResponse: publicapikeys.APIKeyListResponse{
		Source:         "stats",
		GeneratedAt:    "2026-07-07T10:00:00Z",
		Target:         publicapikeys.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
		Page:           2,
		PageSize:       10,
		PageUpperBound: 11,
		Items:          []publicapikeys.APIKeySummary{},
	}}
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicAPIKeyHandlers(service),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/api-key/list?targetUsername=admin&routeStrategyId=rts_1&keyword=%E5%85%AC%E5%BC%80&status=all&page=2&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.listInput.Page != 2 || service.listInput.PageSize != 10 || service.listInput.Status != "all" || service.listInput.RouteStrategyID != "rts_1" || service.listInput.Keyword != "公开" {
		t.Fatalf("list input = %+v", service.listInput)
	}
}

func TestPublicAPIKeyHandlersMapServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		configure  func(*publicAPIKeyServiceStub)
		wantStatus int
		wantMsg    string
	}{
		{
			name: "update not found", path: "/__aipublic__/api-key/update", body: `{"apiKeyId":"key_1","name":"公开 Key"}`,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.updateErr = publicapikeys.ErrAPIKeyNotFound
			},
			wantStatus: http.StatusNotFound, wantMsg: "API Key 不存在",
		},
		{
			name: "add target not found is bad request", path: "/__aipublic__/api-key/add", body: `{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1"}`,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.addErr = fmt.Errorf("%w: admin", publicapikeys.ErrTargetNotFound)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "list target not found is 404", path: "/__aipublic__/api-key/list?targetUsername=admin", body: ``,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.listErr = fmt.Errorf("%w: admin", publicapikeys.ErrTargetNotFound)
			},
			wantStatus: http.StatusNotFound, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "duplicate", path: "/__aipublic__/api-key/update", body: `{"apiKeyId":"key_1","name":"公开 Key"}`,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.updateErr = fmt.Errorf("%w: 公开 Key", publicapikeys.ErrDuplicateAPIKeyName)
			},
			wantStatus: http.StatusConflict, wantMsg: "API Key 名称已存在：公开 Key",
		},
		{
			name: "default delete", path: "/__aipublic__/api-key/del", body: `{"apiKeyId":"key_1"}`,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.deleteErr = publicapikeys.ErrDefaultAPIKeyDelete
			},
			wantStatus: http.StatusBadRequest, wantMsg: "默认 API Key 不允许删除",
		},
		{
			name: "default route change", path: "/__aipublic__/api-key/update", body: `{"apiKeyId":"key_1","routeStrategyId":"rts_2"}`,
			configure: func(stub *publicAPIKeyServiceStub) {
				stub.updateErr = publicapikeys.ErrDefaultAPIKeyRouteStrategyChange
			},
			wantStatus: http.StatusBadRequest, wantMsg: "默认 API Key 不允许更换策略路由",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicAPIKeyServiceStub{}
			tt.configure(service)
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicAPIKeyHandlers(service),
				time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			)

			method := http.MethodPost
			reader := strings.NewReader(tt.body)
			if tt.body == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, tt.path, reader)
			req.Header.Set("Authorization", "Bearer juis_plain")
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertPublicGroupMessageError(t, rec, tt.wantStatus, tt.wantMsg)
		})
	}
}

func TestPublicAPIKeyHandlersDeleteHidesMissingTransactorError(t *testing.T) {
	service := publicapikeys.NewService(publicapikeys.Options{})
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		NewPublicAPIKeyHandlers(service),
		time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/api-key/del", strings.NewReader(`{"apiKeyId":"key_1"}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assertPublicGroupMessageError(t, rec, http.StatusInternalServerError, "服务器内部错误")
}

func TestPublicAPIKeyHandlersTestTokenSkipsService(t *testing.T) {
	authContext := publicAPIShellAuthContext()
	authContext.IsTestToken = true
	service := &publicAPIKeyServiceStub{addErr: errors.New("should not call service")}
	router := newTestPublicAPIShell(
		&publicAPIShellAuthStub{ctx: authContext},
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicAPIKeyHandlers(service),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/api-key/add", strings.NewReader(`{"targetUsername":"admin","name":"公开 Key","routeStrategyId":"rts_1"}`))
	req.Header.Set("Authorization", "Bearer juis_test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.addCalls != 0 {
		t.Fatalf("add calls = %d, want 0", service.addCalls)
	}
}

type publicAPIKeyServiceStub struct {
	listInput   publicapikeys.ListInput
	addInput    publicapikeys.AddInput
	updateInput publicapikeys.UpdateInput
	deleteInput publicapikeys.DeleteInput

	listCalls   int
	addCalls    int
	updateCalls int
	deleteCalls int

	listResponse   publicapikeys.APIKeyListResponse
	addResponse    publicapikeys.APIKeyResponse
	updateResponse publicapikeys.APIKeyResponse
	deleteResponse publicapikeys.APIKeyResponse

	listErr   error
	addErr    error
	updateErr error
	deleteErr error
}

func (s *publicAPIKeyServiceStub) List(_ *http.Request, input publicapikeys.ListInput) (publicapikeys.APIKeyListResponse, error) {
	s.listCalls++
	s.listInput = input
	return s.listResponse, s.listErr
}

func (s *publicAPIKeyServiceStub) Add(_ *http.Request, input publicapikeys.AddInput) (publicapikeys.APIKeyResponse, error) {
	s.addCalls++
	s.addInput = input
	return s.addResponse, s.addErr
}

func (s *publicAPIKeyServiceStub) Update(_ *http.Request, input publicapikeys.UpdateInput) (publicapikeys.APIKeyResponse, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResponse, s.updateErr
}

func (s *publicAPIKeyServiceStub) Delete(_ *http.Request, input publicapikeys.DeleteInput) (publicapikeys.APIKeyResponse, error) {
	s.deleteCalls++
	s.deleteInput = input
	return s.deleteResponse, s.deleteErr
}
