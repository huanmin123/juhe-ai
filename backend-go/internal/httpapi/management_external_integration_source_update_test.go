package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceUpdateHandlerSuccessAndOperationLog(t *testing.T) {
	result := managementExternalIntegrationSourceUpdateResultFixture()
	service := &managementExternalIntegrationSourceUpdateServiceStub{result: result}
	queueStub := &operationLogQueueStub{}
	handler := newManagementExternalIntegrationSourceUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_external_source_update" },
			Now:      func() time.Time { return time.Date(2026, 7, 16, 4, 5, 6, 0, time.UTC) },
		}),
	)
	body := `{"name":" 新来源 ","status":"disabled","scopes":["juhe_ai_public:group_list:read"],"rateLimits":[{"windowSeconds":60,"maxRequests":10}],"expiresAt":null,"notes":null}`
	req := managementExternalIntegrationSourceUpdateRequest("source_1", body)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.calls != 1 || queueStub.calls != 1 {
		t.Fatalf("status=%d service=%d logs=%d body=%s", rec.Code, service.calls, queueStub.calls, rec.Body.String())
	}
	input := service.input
	if input.SourceID != "source_1" || !input.HasName || input.Name != " 新来源 " ||
		!input.HasStatus || input.Status != "disabled" || !input.HasScopes ||
		!input.HasRateLimits || !input.HasExpiresAt || input.ExpiresAt != nil ||
		!input.HasNotes || input.Notes != nil {
		t.Fatalf("service input = %#v", input)
	}
	var response struct {
		Data managementexternalintegrationsources.Detail `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil || !reflect.DeepEqual(response.Data, result.After) {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_external_source_update" || logInput.Module != "external_integration_sources" ||
		logInput.Action != "update" || logInput.OperationKey != "external_integration_sources.update" ||
		logInput.ResourceType != "external_integration_source" || logInput.ResourceID != "source_1" ||
		logInput.ResourceName != "新来源" || logInput.Summary != "更新外部来源系统：新来源" ||
		logInput.DetailLevel != "full" || logInput.VisibilityScope != "admin_only" ||
		logInput.Mode != "self" || logInput.OperationScopeSystemAccountID != "" || len(logInput.Changes) != 4 {
		t.Fatalf("operation log = %#v", logInput)
	}
	wantFields := []string{"name", "status", "expiresAt", "rateLimits"}
	for index, field := range wantFields {
		if logInput.Changes[index].Field != field {
			t.Fatalf("operation log changes = %#v", logInput.Changes)
		}
	}
}

func TestManagementExternalIntegrationSourceUpdateHandlerChecksAdminBeforeBody(t *testing.T) {
	service := &managementExternalIntegrationSourceUpdateServiceStub{}
	handler := newManagementExternalIntegrationSourceUpdateHandler(service, managementOperationLogOptions{})
	req := managementExternalIntegrationSourceUpdateRequest("source_1", `{`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || service.calls != 0 || !strings.Contains(rec.Body.String(), "需要管理员权限") {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
}

func TestManagementExternalIntegrationSourceUpdateHandlerStrictPayload(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty object allowed", body: `{}`, wantStatus: http.StatusOK},
		{name: "unknown field", body: `{"unknown":true}`, wantStatus: http.StatusBadRequest},
		{name: "array root", body: `[]`, wantStatus: http.StatusBadRequest},
		{name: "trailing JSON", body: `{} {}`, wantStatus: http.StatusBadRequest},
		{name: "name null", body: `{"name":null}`, wantStatus: http.StatusBadRequest},
		{name: "status null", body: `{"status":null}`, wantStatus: http.StatusBadRequest},
		{name: "scopes null", body: `{"scopes":null}`, wantStatus: http.StatusBadRequest},
		{name: "rate limits null", body: `{"rateLimits":null}`, wantStatus: http.StatusBadRequest},
		{name: "expires number", body: `{"expiresAt":1}`, wantStatus: http.StatusBadRequest},
		{name: "notes number", body: `{"notes":1}`, wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"notes":"` + strings.Repeat("a", (256<<10)+1) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceUpdateServiceStub{result: managementExternalIntegrationSourceUpdateResultFixture()}
			handler := newManagementExternalIntegrationSourceUpdateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceUpdateRequest("source_1", test.body)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			wantCalls := 0
			if test.wantStatus == http.StatusOK {
				wantCalls = 1
			}
			if service.calls != wantCalls {
				t.Fatalf("service calls=%d want=%d", service.calls, wantCalls)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceUpdateHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "not found", err: managementexternalintegrationsources.ErrNotFound, wantStatus: http.StatusNotFound, wantText: "来源系统不存在"},
		{name: "built in", err: managementexternalintegrationsources.ErrBuiltInUpdateRestricted, wantStatus: http.StatusBadRequest, wantText: "只支持启用或停用"},
		{name: "name exists", err: managementexternalintegrationsources.ErrNameExists, wantStatus: http.StatusBadRequest, wantText: "来源系统名称已存在"},
		{name: "validation", err: testExternalSourceUpdateValidationError(), wantStatus: http.StatusBadRequest, wantText: "来源系统名称不能为空"},
		{name: "internal", err: errors.New("database down"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceUpdateServiceStub{err: test.err}
			handler := newManagementExternalIntegrationSourceUpdateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceUpdateRequest("source_1", `{}`)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus || !strings.Contains(rec.Body.String(), test.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceUpdateMutationGuardMatchesNodeFingerprint(t *testing.T) {
	config := managementExternalIntegrationSourceUpdateMutationGuardConfig()
	body := `{"name":" 新来源 ","status":"disabled","scopes":["ignored"],"rateLimits":[{"windowSeconds":60,"maxRequests":10}],"expiresAt":null,"notes":"ignored"}`
	req := managementExternalIntegrationSourceUpdateRequest("source_1", body)
	rec := httptest.NewRecorder()

	fingerprint, err := config.fingerprint(rec, req)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	got, ok := fingerprint.(map[string]any)
	if !ok {
		t.Fatalf("fingerprint type = %T", fingerprint)
	}
	if config.operationKey != "external_integration_sources.update" || len(got) != 5 ||
		got["id"] != "source_1" || got["name"] != " 新来源 " || got["status"] != "disabled" ||
		got["expiresAt"] != nil {
		t.Fatalf("config=%+v fingerprint=%#v", config, got)
	}
	if _, exists := got["scopes"]; exists {
		t.Fatalf("scopes must not participate in the Node-compatible fingerprint: %#v", got)
	}
	if _, exists := got["notes"]; exists {
		t.Fatalf("notes must not participate in the Node-compatible fingerprint: %#v", got)
	}
	downstreamBody, err := io.ReadAll(req.Body)
	if err != nil || string(downstreamBody) != body {
		t.Fatalf("downstream body = %q err=%v", downstreamBody, err)
	}

	var numberHashes []string
	for _, numeric := range []string{"1", "1.0", "1e0"} {
		req := managementExternalIntegrationSourceUpdateRequest(
			"source_1",
			`{"rateLimits":[{"windowSeconds":`+numeric+`,"maxRequests":`+numeric+`}]}`,
		)
		fingerprint, err := config.fingerprint(httptest.NewRecorder(), req)
		if err != nil {
			t.Fatalf("numeric fingerprint %s: %v", numeric, err)
		}
		numberHashes = append(numberHashes, hashMutationStableValue(fingerprint))
	}
	if numberHashes[0] != numberHashes[1] || numberHashes[0] != numberHashes[2] {
		t.Fatalf("Node-equivalent JSON numbers produced different fingerprints: %v", numberHashes)
	}
}

func TestRouterRegistersManagementExternalIntegrationSourceUpdatePatch(t *testing.T) {
	touchCalls := 0
	updateCalls := 0
	touchMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			touchCalls++
			r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			next.ServeHTTP(w, r)
		})
	}
	opts := RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: touchMiddleware,
		ManagementExternalIntegrationSourceUpdateHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateCalls++
			if chi.URLParam(r, "id") != "source_1" {
				t.Fatalf("source id = %q", chi.URLParam(r, "id"))
			}
			w.WriteHeader(http.StatusNoContent)
		}),
		ManagementExternalIntegrationSourceScopesHandler:  http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ManagementExternalIntegrationSourceAPIDocsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	router := NewRouter(opts)

	for attempt, wantStatus := range []int{http.StatusNoContent, http.StatusConflict} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/external-integration-sources/source_1", strings.NewReader(`{"status":"disabled"}`))
		router.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("attempt %d status=%d want=%d body=%s", attempt+1, rec.Code, wantStatus, rec.Body.String())
		}
	}
	if touchCalls != 2 || updateCalls != 1 {
		t.Fatalf("touch calls=%d update calls=%d", touchCalls, updateCalls)
	}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("external source update must be classified as a management business write route")
	}

	for _, path := range []string{
		"/__aisys__/api/external-integration-sources/scopes",
		"/__aisys__/api/external-integration-sources/api-docs",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("PATCH %s status=%d Allow=%q body=%s", path, rec.Code, rec.Header().Get("Allow"), rec.Body.String())
		}
	}
	if updateCalls != 1 {
		t.Fatalf("static catalog paths reached update handler: calls=%d", updateCalls)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, "/__aisys__/api/external-integration-sources/source_1", strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s detail status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestRouterManagementExternalIntegrationSourceUpdateChecksAdminBeforeMutationGuard(t *testing.T) {
	updateCalls := 0
	opts := RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = requestWithManagementExternalIntegrationSourceAuthContext(r, managementauth.Context{
					SystemAccountID: "sys_user",
					Role:            "user",
				})
				next.ServeHTTP(w, r)
			})
		},
		ManagementExternalIntegrationSourceUpdateHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			updateCalls++
		}),
	}
	router := NewRouter(opts)
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(
			http.MethodPatch,
			"/__aisys__/api/external-integration-sources/source_1",
			strings.NewReader(`{"status":"disabled"}`),
		))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status=%d want=403 body=%s", attempt, rec.Code, rec.Body.String())
		}
	}
	if updateCalls != 0 {
		t.Fatalf("non-admin reached update handler: calls=%d", updateCalls)
	}
}

func managementExternalIntegrationSourceUpdateRequest(sourceID string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func requestWithManagementExternalIntegrationSourceAuthContext(
	req *http.Request,
	authContext managementauth.Context,
) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

func managementExternalIntegrationSourceUpdateResultFixture() managementexternalintegrationsources.UpdateResult {
	before := managementexternalintegrationsources.Detail{Source: managementexternalintegrationsources.Source{
		ID:         "source_1",
		Name:       "旧来源",
		Status:     "active",
		RateLimits: []managementexternalintegrationsources.RateLimitRule{{WindowSeconds: 60, MaxRequests: 5}},
		ExpiresAt:  stringPointerHTTP("2026-08-01T00:00:00.000Z"),
	}}
	after := managementexternalintegrationsources.Detail{Source: managementexternalintegrationsources.Source{
		ID:         "source_1",
		Name:       "新来源",
		Status:     "disabled",
		RateLimits: []managementexternalintegrationsources.RateLimitRule{{WindowSeconds: 30, MaxRequests: 7}},
	}}
	return managementexternalintegrationsources.UpdateResult{Before: before, After: after, Committed: true}
}

func testExternalSourceUpdateValidationError() error {
	service := managementexternalintegrationsources.NewUpdateService(&managementExternalIntegrationSourceUpdatePortStub{})
	_, err := service.Update(context.Background(), managementexternalintegrationsources.UpdateInput{
		SourceID: "source_1",
		HasName:  true,
		Name:     "",
	})
	return err
}

type managementExternalIntegrationSourceUpdateServiceStub struct {
	input  managementexternalintegrationsources.UpdateInput
	result managementexternalintegrationsources.UpdateResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceUpdateServiceStub) Update(
	_ context.Context,
	input managementexternalintegrationsources.UpdateInput,
) (managementexternalintegrationsources.UpdateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type managementExternalIntegrationSourceUpdatePortStub struct{}

func (s *managementExternalIntegrationSourceUpdatePortStub) UpdateManagementExternalIntegrationSource(
	context.Context,
	port.ManagementExternalIntegrationSourceUpdateInput,
	func(port.ManagementExternalIntegrationSourceUpdateResult) error,
) (port.ManagementExternalIntegrationSourceUpdateResult, error) {
	return port.ManagementExternalIntegrationSourceUpdateResult{}, nil
}

func stringPointerHTTP(value string) *string { return &value }
