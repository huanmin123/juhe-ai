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
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceListHandlerReturnsDataForAdminRoles(t *testing.T) {
	wantResult := managementexternalintegrationsources.ListResult{
		Items:          []managementexternalintegrationsources.ListItem{},
		Page:           1,
		PageSize:       10,
		PageUpperBound: 0,
		HasMore:        false,
	}
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{result: wantResult}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := httptest.NewRequest(
				http.MethodGet,
				"/__aisys__/api/external-integration-sources?page=1.0&pageSize=1e1&keyword=%20MiXeD%20&status=active&ignored=value",
				nil,
			)
			req = withManagementExternalIntegrationSourceAuth(req, role)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if len(service.calls) != 1 {
				t.Fatalf("service calls = %d, want 1", len(service.calls))
			}
			wantInput := managementexternalintegrationsources.ListInput{
				Page:             1,
				PageSize:         10,
				PageSizeProvided: true,
				Keyword:          "MiXeD",
				Status:           "active",
			}
			if got := service.calls[0]; !reflect.DeepEqual(got, wantInput) {
				t.Fatalf("service input = %#v, want %#v", got, wantInput)
			}
			var body struct {
				Data managementexternalintegrationsources.ListResult `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !reflect.DeepEqual(body.Data, wantResult) {
				t.Fatalf("data = %#v, want %#v", body.Data, wantResult)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerPreservesDefaultsAndSingleQSArrays(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantInput managementexternalintegrationsources.ListInput
	}{
		{
			name:   "defaults and unknown fields",
			target: "/__aisys__/api/external-integration-sources?unknown=value",
		},
		{
			name:   "single qs arrays",
			target: "/__aisys__/api/external-integration-sources?page%5B%5D=2&pageSize%5B%5D=5",
			wantInput: managementexternalintegrationsources.ListInput{
				Page:             2,
				PageSize:         5,
				PageSizeProvided: true,
			},
		},
		{
			name:   "single indexed and nested qs arrays",
			target: "/__aisys__/api/external-integration-sources?page%5B0%5D=3&pageSize%5B%5D%5B0%5D=7",
			wantInput: managementexternalintegrationsources.ListInput{
				Page:             3,
				PageSize:         7,
				PageSizeProvided: true,
			},
		},
		{
			name:   "qs maximum depth array",
			target: "/__aisys__/api/external-integration-sources?page%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D=4",
			wantInput: managementexternalintegrationsources.ListInput{
				Page: 4,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{
				result: managementexternalintegrationsources.ListResult{Items: []managementexternalintegrationsources.ListItem{}},
			}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := withManagementExternalIntegrationSourceAuth(httptest.NewRequest(http.MethodGet, test.target, nil), "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if len(service.calls) != 1 || !reflect.DeepEqual(service.calls[0], test.wantInput) {
				t.Fatalf("service calls = %#v, want %#v", service.calls, test.wantInput)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerRejectsInvalidZodQueriesInFieldOrder(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantMessage string
	}{
		{name: "page empty", query: "page=", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page NaN", query: "page=NaN", wantMessage: "Expected number, received nan"},
		{name: "page non-number", query: "page=nope", wantMessage: "Expected number, received nan"},
		{name: "page non-integer", query: "page=1.5", wantMessage: "Expected integer, received float"},
		{name: "page lower bound", query: "page=0", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page repeated", query: "page=1&page=2", wantMessage: "Expected number, received nan"},
		{name: "page direct and bracket array", query: "page=1&page%5B%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page object bracket", query: "page%5Bfoo%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page nested object bracket", query: "page%5B0%5D%5Bfoo%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page index outside qs array limit", query: "page%5B20%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page beyond qs maximum depth", query: "page%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D%5B%5D=2", wantMessage: "Expected number, received nan"},
		{name: "page size empty", query: "pageSize=", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page size NaN", query: "pageSize=NaN", wantMessage: "Expected number, received nan"},
		{name: "page size non-integer", query: "pageSize=1.5", wantMessage: "Expected integer, received float"},
		{name: "page size lower bound", query: "pageSize=0", wantMessage: "Number must be greater than or equal to 1"},
		{name: "page size upper bound", query: "pageSize=101", wantMessage: "Number must be less than or equal to 100"},
		{name: "page size repeated", query: "pageSize=10&pageSize=20", wantMessage: "Expected number, received nan"},
		{name: "keyword repeated", query: "keyword=first&keyword=second", wantMessage: "Expected string, received array"},
		{name: "keyword bracket array", query: "keyword%5B%5D=first", wantMessage: "Expected string, received array"},
		{name: "keyword indexed bracket array", query: "keyword%5B0%5D=first", wantMessage: "Expected string, received array"},
		{name: "keyword object bracket", query: "keyword%5Bfoo%5D=first", wantMessage: "Expected string, received object"},
		{name: "keyword direct and bracket array", query: "keyword=first&keyword%5B%5D=second", wantMessage: "Expected string, received array"},
		{name: "keyword direct and object bracket", query: "keyword=first&keyword%5Bfoo%5D=second", wantMessage: "Expected string, received array"},
		{name: "status repeated", query: "status=active&status=disabled", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status bracket array", query: "status%5B%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status indexed bracket array", query: "status%5B0%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status object bracket", query: "status%5Bfoo%5D=active", wantMessage: "Expected 'all' | 'active' | 'disabled', received object"},
		{name: "status direct and bracket array", query: "status=active&status%5B%5D=disabled", wantMessage: "Expected 'all' | 'active' | 'disabled', received array"},
		{name: "status is not trimmed", query: "status=%20active%20", wantMessage: "Invalid enum value. Expected 'all' | 'active' | 'disabled', received ' active '"},
		{
			name:        "page is first issue",
			query:       "page=0&pageSize=101&keyword=first&keyword=second&status=bad",
			wantMessage: "Number must be greater than or equal to 1",
		},
		{
			name:        "page size precedes keyword and status",
			query:       "pageSize=101&keyword=first&keyword=second&status=bad",
			wantMessage: "Number must be less than or equal to 100",
		},
		{
			name:        "keyword precedes status",
			query:       "keyword=first&keyword=second&status=bad",
			wantMessage: "Expected string, received array",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceListServiceStub{}
			handler := newManagementExternalIntegrationSourceListHandler(service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources?"+test.query, nil)
			req = withManagementExternalIntegrationSourceAuth(req, "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			assertManagementExternalIntegrationSourceMessage(t, rec, test.wantMessage)
			if len(service.calls) != 0 {
				t.Fatalf("service calls = %d, want 0", len(service.calls))
			}
		})
	}
}

func TestManagementExternalIntegrationSourceListHandlerAuthAndServiceFailures(t *testing.T) {
	tests := []struct {
		name        string
		role        *string
		service     managementExternalIntegrationSourceListService
		wantStatus  int
		wantMessage string
	}{
		{name: "missing auth context", service: &managementExternalIntegrationSourceListServiceStub{}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "ordinary user", role: managementExternalIntegrationSourceRolePointer("user"), service: &managementExternalIntegrationSourceListServiceStub{}, wantStatus: http.StatusForbidden, wantMessage: "需要管理员权限"},
		{name: "nil service", role: managementExternalIntegrationSourceRolePointer("admin"), wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "store error", role: managementExternalIntegrationSourceRolePointer("admin"), service: &managementExternalIntegrationSourceListServiceStub{err: errors.New("postgres unavailable")}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{name: "stored JSON error", role: managementExternalIntegrationSourceRolePointer("admin"), service: &managementExternalIntegrationSourceListServiceStub{err: errors.New("decode scopes JSON")}, wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newManagementExternalIntegrationSourceListHandler(test.service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources", nil)
			if test.role != nil {
				req = withManagementExternalIntegrationSourceAuth(req, *test.role)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			assertManagementExternalIntegrationSourceMessage(t, rec, test.wantMessage)
		})
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerReturnsDataForAdminRoles(t *testing.T) {
	wantDetail := &managementexternalintegrationsources.Detail{
		Source: managementexternalintegrationsources.Source{
			ID:               "source_1",
			Name:             "来源一",
			Status:           "active",
			Scopes:           []string{publicapi.ScopeAccountListRead},
			RateLimits:       []managementexternalintegrationsources.RateLimitRule{},
			CreatedAt:        "2026-07-15T00:00:00.000Z",
			UpdatedAt:        "2026-07-15T00:00:00.000Z",
			TokenCount:       1,
			ActiveTokenCount: 1,
		},
		Tokens: []managementexternalintegrationsources.Token{{
			ID:          "token_1",
			Name:        "生产 Token",
			TokenPrefix: "jis_live_",
			TokenSuffix: "tail",
			Status:      publicapi.TokenStatusActive,
			Scopes:      []string{publicapi.ScopeAccountListRead},
			CreatedAt:   "2026-07-15T00:00:00.000Z",
			UpdatedAt:   "2026-07-15T00:00:00.000Z",
		}},
	}
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementExternalIntegrationSourceDetailServiceStub{detail: wantDetail}
			handler := newManagementExternalIntegrationSourceDetailHandler(service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/source_1", nil)
			req = withManagementExternalIntegrationSourceDetailID(req, " \t source_1 \r\n")
			req = withManagementExternalIntegrationSourceAuth(req, role)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if !reflect.DeepEqual(service.calls, []string{"source_1"}) {
				t.Fatalf("service calls = %#v, want [source_1]", service.calls)
			}
			var body struct {
				Data managementexternalintegrationsources.Detail `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !reflect.DeepEqual(body.Data, *wantDetail) {
				t.Fatalf("data = %#v, want %#v", body.Data, *wantDetail)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerRejectsBlankID(t *testing.T) {
	service := &managementExternalIntegrationSourceDetailServiceStub{}
	handler := newManagementExternalIntegrationSourceDetailHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/blank", nil)
	req = withManagementExternalIntegrationSourceDetailID(req, " \t\r\n")
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	assertManagementExternalIntegrationSourceMessage(t, rec, "来源系统不存在")
	if len(service.calls) != 0 {
		t.Fatalf("service calls = %#v, want none", service.calls)
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerPreservesNonECMAScriptWhitespaceID(t *testing.T) {
	const sourceID = "\u0085"
	service := &managementExternalIntegrationSourceDetailServiceStub{}
	handler := newManagementExternalIntegrationSourceDetailHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/non-ecmascript-space", nil)
	req = withManagementExternalIntegrationSourceDetailID(req, sourceID)
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	assertManagementExternalIntegrationSourceMessage(t, rec, "来源系统不存在")
	if !reflect.DeepEqual(service.calls, []string{sourceID}) {
		t.Fatalf("service calls = %#v, want %#v", service.calls, []string{sourceID})
	}
}

func TestManagementExternalIntegrationSourceDetailRouterAndServicePreserveNonECMAScriptWhitespaceID(t *testing.T) {
	const sourceID = "\u0085extsrc_1\u0085"
	reader := &managementExternalIntegrationSourceExactDetailReaderStub{foundID: "extsrc_1"}
	service := managementexternalintegrationsources.NewServiceWithOptions(
		managementexternalintegrationsources.ServiceOptions{DetailReader: reader},
	)
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, withManagementExternalIntegrationSourceAuth(r, "admin"))
			})
		},
		ManagementExternalIntegrationSourceDetailHandler: NewManagementExternalIntegrationSourceDetailHandler(service),
	})
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/external-integration-sources/%C2%85extsrc_1%C2%85",
		nil,
	)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	assertManagementExternalIntegrationSourceMessage(t, rec, "来源系统不存在")
	if !reflect.DeepEqual(reader.calls, []string{sourceID}) {
		t.Fatalf("detail reader calls = %#v, want %#v", reader.calls, []string{sourceID})
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerRequiresAdmin(t *testing.T) {
	service := &managementExternalIntegrationSourceDetailServiceStub{}
	handler := newManagementExternalIntegrationSourceDetailHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/source_1", nil)
	req = withManagementExternalIntegrationSourceDetailID(req, "source_1")
	req = withManagementExternalIntegrationSourceAuth(req, "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	assertManagementExternalIntegrationSourceMessage(t, rec, "需要管理员权限")
	if len(service.calls) != 0 {
		t.Fatalf("service calls = %#v, want none", service.calls)
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerMapsNotFoundAndInternalErrors(t *testing.T) {
	tests := []struct {
		name        string
		role        *string
		service     managementExternalIntegrationSourceDetailService
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "missing auth context",
			service:     &managementExternalIntegrationSourceDetailServiceStub{},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
		{
			name:        "nil dependency",
			role:        managementExternalIntegrationSourceRolePointer("admin"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
		{
			name:        "not found",
			role:        managementExternalIntegrationSourceRolePointer("admin"),
			service:     &managementExternalIntegrationSourceDetailServiceStub{},
			wantStatus:  http.StatusNotFound,
			wantMessage: "来源系统不存在",
		},
		{
			name:        "service error is redacted",
			role:        managementExternalIntegrationSourceRolePointer("admin"),
			service:     &managementExternalIntegrationSourceDetailServiceStub{err: errors.New("postgres password leaked")},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var handler http.Handler
			if test.name == "nil dependency" {
				handler = NewManagementExternalIntegrationSourceDetailHandler(nil)
			} else {
				handler = newManagementExternalIntegrationSourceDetailHandler(test.service)
			}
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/source_1", nil)
			req = withManagementExternalIntegrationSourceDetailID(req, "source_1")
			if test.role != nil {
				req = withManagementExternalIntegrationSourceAuth(req, *test.role)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			assertManagementExternalIntegrationSourceMessage(t, rec, test.wantMessage)
			if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked service error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceDetailHandlerKeepsSafeShapeAndEmptyTokensArray(t *testing.T) {
	detail := &managementexternalintegrationsources.Detail{
		Source: managementexternalintegrationsources.Source{
			ID:         "source_empty",
			Name:       "空 Token 来源",
			Status:     "active",
			Scopes:     []string{},
			RateLimits: []managementexternalintegrationsources.RateLimitRule{},
			CreatedAt:  "2026-07-15T00:00:00.000Z",
			UpdatedAt:  "2026-07-15T00:00:00.000Z",
		},
		Tokens: []managementexternalintegrationsources.Token{},
	}
	handler := newManagementExternalIntegrationSourceDetailHandler(
		&managementExternalIntegrationSourceDetailServiceStub{detail: detail},
	)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/source_empty", nil)
	req = withManagementExternalIntegrationSourceDetailID(req, "source_empty")
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	bodyText := rec.Body.String()
	for _, forbidden := range []string{
		"token_hash",
		"tokenHash",
		"token_secret_encrypted",
		"tokenSecretEncrypted",
		`"token":`,
		`"primaryToken"`,
	} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("response leaked forbidden field %q: %s", forbidden, bodyText)
		}
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := string(body.Data["tokens"]); got != "[]" {
		t.Fatalf("tokens JSON = %s, want []; body = %s", got, bodyText)
	}
	allowedFields := map[string]struct{}{
		"id": {}, "name": {}, "status": {}, "scopes": {}, "rateLimits": {},
		"expiresAt": {}, "notes": {}, "lastUsedAt": {}, "createdAt": {}, "updatedAt": {},
		"tokenCount": {}, "activeTokenCount": {}, "tokens": {}, "isBuiltIn": {},
	}
	for field := range body.Data {
		if _, allowed := allowedFields[field]; !allowed {
			t.Fatalf("response contains unexpected field %q: %s", field, bodyText)
		}
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerReturnsCatalog(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Data []publicapi.ScopeOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := len(body.Data), 16; got != want {
		t.Fatalf("scope options = %d, want %d", got, want)
	}
	if want := publicapi.ScopeOptions(); !reflect.DeepEqual(body.Data, want) {
		t.Fatalf("scope options = %+v, want %+v", body.Data, want)
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerRequiresAdmin(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("message = %q, want 需要管理员权限", body["message"])
	}
}

func TestManagementExternalIntegrationSourceScopesHandlerRejectsMissingAuthContext(t *testing.T) {
	handler := newManagementExternalIntegrationSourceScopesHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/scopes", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("message = %q, want 服务器内部错误", body["message"])
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerReturnsCatalog(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var gotCatalog any
	if err := json.Unmarshal(body.Data, &gotCatalog); err != nil {
		t.Fatalf("decode response catalog: %v", err)
	}
	var wantCatalog any
	if err := json.Unmarshal(publicapi.APIDocsCatalog(), &wantCatalog); err != nil {
		t.Fatalf("decode expected catalog: %v", err)
	}
	if !reflect.DeepEqual(gotCatalog, wantCatalog) {
		t.Fatalf("api docs catalog = %#v, want %#v", gotCatalog, wantCatalog)
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerRequiresAdmin(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	req = withManagementExternalIntegrationSourceAuth(req, "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("message = %q, want 需要管理员权限", body["message"])
	}
}

func TestManagementExternalIntegrationSourceAPIDocsHandlerRejectsMissingAuthContext(t *testing.T) {
	handler := newManagementExternalIntegrationSourceAPIDocsHandler()
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/api-docs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("message = %q, want 服务器内部错误", body["message"])
	}
}

type managementExternalIntegrationSourceListServiceStub struct {
	result managementexternalintegrationsources.ListResult
	err    error
	calls  []managementexternalintegrationsources.ListInput
}

func (s *managementExternalIntegrationSourceListServiceStub) List(
	_ context.Context,
	input managementexternalintegrationsources.ListInput,
) (managementexternalintegrationsources.ListResult, error) {
	s.calls = append(s.calls, input)
	return s.result, s.err
}

type managementExternalIntegrationSourceDetailServiceStub struct {
	detail *managementexternalintegrationsources.Detail
	err    error
	calls  []string
}

type managementExternalIntegrationSourceExactDetailReaderStub struct {
	foundID string
	calls   []string
}

func (s *managementExternalIntegrationSourceExactDetailReaderStub) FindManagementExternalIntegrationSource(
	_ context.Context,
	sourceID string,
) (port.ManagementExternalIntegrationSourceListRow, bool, error) {
	s.calls = append(s.calls, sourceID)
	if sourceID != s.foundID {
		return port.ManagementExternalIntegrationSourceListRow{}, false, nil
	}
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	return port.ManagementExternalIntegrationSourceListRow{
		ID:             sourceID,
		Name:           "Source One",
		Status:         publicapi.SourceStatusActive,
		ScopesJSON:     `[]`,
		RateLimitsJSON: `[]`,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, true, nil
}

func (*managementExternalIntegrationSourceExactDetailReaderStub) ListManagementExternalIntegrationSourceTokens(
	context.Context,
	string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	return []port.ManagementExternalIntegrationSourcePrimaryTokenRow{}, nil
}

func (s *managementExternalIntegrationSourceDetailServiceStub) Get(
	_ context.Context,
	id string,
) (*managementexternalintegrationsources.Detail, error) {
	s.calls = append(s.calls, id)
	return s.detail, s.err
}

func assertManagementExternalIntegrationSourceMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	want string,
) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != want {
		t.Fatalf("message = %q, want %q", body["message"], want)
	}
}

func managementExternalIntegrationSourceRolePointer(role string) *string {
	return &role
}

func withManagementExternalIntegrationSourceDetailID(req *http.Request, sourceID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func withManagementExternalIntegrationSourceAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            role,
	}))
}
