package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
)

func TestManagementPublicAPILogsHandlerParsesListQueryAndReturnsList(t *testing.T) {
	service := &managementPublicAPILogServiceStub{
		listResult: managementpublicapilogs.ListResult{
			Items:    []managementpublicapilogs.Summary{{ID: "publog_1", Method: http.MethodPost, Path: "/v1/chat/completions"}},
			Total:    21,
			HasMore:  true,
			Page:     2,
			PageSize: 20,
		},
	}
	handler := newManagementPublicAPILogsHandler(service)
	query := url.Values{
		"page":        {"\uFEFF0x2\u2029", "9"},
		"pageSize":    {"2e1", "99"},
		"traceId":     {"\uFEFFtrace_1\u3000", "ignored"},
		"sourceRefId": {"\u00A0extsrc_1\u202F", "ignored"},
		"path":        {"\u1680POST /v1/chat/completions?stream=true\u205F", "ignored"},
		"result":      {"\u2000success\u200A", "failed"},
		"statusCode":  {"2e2", "500"},
		"clientIp":    {"\uFEFF203.0.113.\u2029", "ignored"},
		"startAt":     {"\uFEFF2026-07-14T10:00:00.123Z\u3000", "ignored"},
		"endAt":       {"2026-07-14T09:00:00Z", "ignored"},
	}
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs?"+query.Encode(), nil)
	req = withManagementPublicAPILogAuth(req, "super_admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	input := service.listInput
	if input.Page != 2 || input.PageSize != 20 || !input.PageSizeProvided ||
		input.TraceID != "trace_1" || input.SourceRefID != "extsrc_1" ||
		input.Path != "POST /v1/chat/completions?stream=true" || input.Result != "success" ||
		input.StatusCode != 200 || input.ClientIP != "203.0.113." {
		t.Fatalf("input = %+v", input)
	}
	wantStart := time.Date(2026, 7, 14, 10, 0, 0, 123_000_000, time.UTC)
	wantEnd := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	if !input.StartAt.Equal(wantStart) || !input.EndAt.Equal(wantEnd) || !input.StartAt.After(input.EndAt) {
		t.Fatalf("date range = %s - %s, want unchanged reverse range", input.StartAt, input.EndAt)
	}
	if service.deadlineSet {
		t.Fatal("public API log handler must not add a request deadline")
	}
	var body struct {
		Data managementpublicapilogs.ListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Total != 21 || !body.Data.HasMore || len(body.Data.Items) != 1 || body.Data.Items[0].ID != "publog_1" {
		t.Fatalf("response data = %+v", body.Data)
	}
}

func TestManagementPublicAPILogsHandlerUsesExactListQueryValidation(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	tests := []struct {
		name             string
		query            url.Values
		wantPageSize     int
		wantPageProvided bool
		wantResult       string
		wantStatus       int
		wantStart        time.Time
		wantEnd          time.Time
	}{
		{
			name: "allowed failed and exact millisecond time",
			query: url.Values{
				"pageSize":   {"0"},
				"result":     {"failed"},
				"statusCode": {"0x257"},
				"startAt":    {"2024-02-29T23:59:59.999Z"},
			},
			wantPageProvided: true,
			wantResult:       "failed",
			wantStatus:       599,
			wantStart:        time.Date(2024, 2, 29, 23, 59, 59, 999_000_000, time.UTC),
		},
		{
			name: "allowed all and second precision time",
			query: url.Values{
				"result":   {"all"},
				"endAt":    {"2026-07-14T10:00:00Z"},
				"pageSize": {"2.5", "20"},
			},
			wantResult: "all",
			wantEnd:    time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		},
		{
			name: "invalid values are ignored",
			query: url.Values{
				"result":     {"SUCCESS", "success"},
				"statusCode": {"600", "200"},
				"startAt":    {"2026-02-29T10:00:00Z"},
				"endAt":      {"2026-07-14T10:00:00.12Z"},
				"pageSize":   {"\uFEFF\u3000"},
			},
		},
		{
			name: "offset lowercase and non ECMAScript whitespace times are ignored",
			query: url.Values{
				"startAt": {"2026-07-14T10:00:00+00:00"},
				"endAt":   {nonECMAScriptWhitespace + "2026-07-14T10:00:00z"},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &managementPublicAPILogServiceStub{}
			handler := newManagementPublicAPILogsHandler(service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs?"+testCase.query.Encode(), nil)
			req = withManagementPublicAPILogAuth(req, "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			input := service.listInput
			if input.PageSize != testCase.wantPageSize || input.PageSizeProvided != testCase.wantPageProvided ||
				input.Result != testCase.wantResult || input.StatusCode != testCase.wantStatus ||
				!input.StartAt.Equal(testCase.wantStart) || !input.EndAt.Equal(testCase.wantEnd) {
				t.Fatalf("input = %+v", input)
			}
		})
	}
}

func TestManagementPublicAPILogsHandlerPermissions(t *testing.T) {
	tests := []struct {
		name        string
		authContext *managementauth.Context
		wantStatus  int
		wantMessage string
		wantCalled  bool
	}{
		{name: "missing auth context", wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
		{
			name:        "empty system account",
			authContext: &managementauth.Context{Role: "admin"},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
		{
			name:        "ordinary user",
			authContext: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus:  http.StatusForbidden,
			wantMessage: "需要管理员权限",
		},
		{
			name:        "admin",
			authContext: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			wantStatus:  http.StatusOK,
			wantCalled:  true,
		},
		{
			name:        "super admin",
			authContext: &managementauth.Context{SystemAccountID: "sys_super", Role: "super_admin"},
			wantStatus:  http.StatusOK,
			wantCalled:  true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &managementPublicAPILogServiceStub{}
			handler := newManagementPublicAPILogsHandler(service)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs", nil)
			if testCase.authContext != nil {
				req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, *testCase.authContext))
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, testCase.wantStatus, rec.Body.String())
			}
			if service.listCalled != testCase.wantCalled || service.detailCalled {
				t.Fatalf("service calls = list %v detail %v", service.listCalled, service.detailCalled)
			}
			if testCase.wantMessage != "" && !strings.Contains(rec.Body.String(), testCase.wantMessage) {
				t.Fatalf("body = %s, want message %q", rec.Body.String(), testCase.wantMessage)
			}
		})
	}
}

func TestManagementPublicAPILogsHandlerReturnsDetailAndPreservesID(t *testing.T) {
	id := " \uFEFFpublog_1\u3000 "
	service := &managementPublicAPILogServiceStub{
		detail: managementpublicapilogs.Detail{
			Summary:      managementpublicapilogs.Summary{ID: id, Method: http.MethodGet, Path: "/health"},
			RequestData:  map[string]any{"probe": true},
			ResponseData: map[string]any{"ok": true},
		},
		detailFound: true,
	}
	handler := newManagementPublicAPILogsHandler(service)
	req := withManagementPublicAPILogAuth(managementPublicAPILogDetailRequest(id), "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.detailCalled || service.listCalled || service.detailID != id {
		t.Fatalf("service calls = detail %v list %v id %q", service.detailCalled, service.listCalled, service.detailID)
	}
	if service.deadlineSet {
		t.Fatal("public API log detail handler must not add a request deadline")
	}
	if !strings.Contains(rec.Body.String(), `"probe":true`) || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("detail body = %s", rec.Body.String())
	}
}

func TestManagementPublicAPILogsHandlerDetailNotFound(t *testing.T) {
	service := &managementPublicAPILogServiceStub{}
	handler := newManagementPublicAPILogsHandler(service)
	req := withManagementPublicAPILogAuth(managementPublicAPILogDetailRequest("publog_missing"), "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "公开接口日志不存在") {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestManagementPublicAPILogsHandlerRedactsInternalErrors(t *testing.T) {
	tests := []struct {
		name    string
		service managementPublicAPILogService
		detail  bool
	}{
		{
			name:    "nil service",
			service: nil,
		},
		{
			name:    "list error",
			service: &managementPublicAPILogServiceStub{listErr: errors.New("postgres password leaked")},
		},
		{
			name:    "detail error",
			service: &managementPublicAPILogServiceStub{detailErr: errors.New("postgres password leaked")},
			detail:  true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := newManagementPublicAPILogsHandler(testCase.service)
			var req *http.Request
			if testCase.detail {
				req = managementPublicAPILogDetailRequest("publog_1")
			} else {
				req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs", nil)
			}
			req = withManagementPublicAPILogAuth(req, "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "服务器内部错误") {
				t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "postgres password leaked") {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestRouterRegistersManagementPublicAPILogsOnlyWhenEnabled(t *testing.T) {
	service := &managementPublicAPILogServiceStub{
		detail:      managementpublicapilogs.Detail{Summary: managementpublicapilogs.Summary{ID: "publog_1"}},
		detailFound: true,
	}
	newRouter := func(enabled bool) http.Handler {
		return NewRouter(RouterOptions{
			Config:                         config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: enabled},
			Logger:                         slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
			ManagementPublicAPILogsHandler: newManagementPublicAPILogsHandler(service),
			ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(managementPublicAPILogAuthenticatorStub{
				authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			}),
		})
	}
	for _, path := range []string{
		"/__aisys__/api/public-api-logs",
		"/__aisys__/api/public-api-logs/publog_1",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		newRouter(true).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("enabled %s status = %d, want 200; body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("enabled %s Cache-Control = %q, want no-store", path, got)
		}
	}

	for _, path := range []string{
		"/__aisys__/api/public-api-logs",
		"/__aisys__/api/public-api-logs/publog_1",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		newRouter(false).ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled %s status = %d, want 404", path, rec.Code)
		}
	}
}

func withManagementPublicAPILogAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            role,
	}))
}

func managementPublicAPILogDetailRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs/detail", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

type managementPublicAPILogServiceStub struct {
	listCalled   bool
	listInput    managementpublicapilogs.ListInput
	listResult   managementpublicapilogs.ListResult
	listErr      error
	detailCalled bool
	detailID     string
	detail       managementpublicapilogs.Detail
	detailFound  bool
	detailErr    error
	deadlineSet  bool
}

func (s *managementPublicAPILogServiceStub) List(
	r *http.Request,
	input managementpublicapilogs.ListInput,
) (managementpublicapilogs.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	_, s.deadlineSet = r.Context().Deadline()
	return s.listResult, s.listErr
}

func (s *managementPublicAPILogServiceStub) Detail(
	r *http.Request,
	id string,
) (managementpublicapilogs.Detail, bool, error) {
	s.detailCalled = true
	s.detailID = id
	_, s.deadlineSet = r.Context().Deadline()
	return s.detail, s.detailFound, s.detailErr
}

type managementPublicAPILogAuthenticatorStub struct {
	authContext managementauth.Context
}

func (s managementPublicAPILogAuthenticatorStub) AuthenticateCookie(
	_ context.Context,
	_ string,
) (managementauth.Context, error) {
	return s.authContext, nil
}
