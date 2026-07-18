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
	"juhe-ai/backend-go/internal/modules/managementruntimelogs"
)

func TestManagementRuntimeLogsHandlerParsesNodeCompatibleQueryAndMetadata(t *testing.T) {
	service := &managementRuntimeLogServiceStub{
		listResult: managementruntimelogs.ListResult{
			Items: []managementruntimelogs.Summary{{
				ID:        "runtime_1",
				Time:      "2026-07-14T10:00:00.000Z",
				Level:     "warn",
				TraceID:   "trace_1",
				Message:   "slow request",
				CreatedAt: "2026-07-14T10:00:01.000Z",
			}},
			Total:    22,
			HasMore:  true,
			Page:     2,
			PageSize: 20,
		},
	}
	clockValues := []time.Time{
		time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 10, 0, 0, 1_600_000, time.UTC),
	}
	clockIndex := 0
	handler := newManagementRuntimeLogsHandler(service, func() time.Time {
		value := clockValues[min(clockIndex, len(clockValues)-1)]
		clockIndex++
		return value
	})
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs?page=2e0&pageSize=20&traceId=trace_&traceId=ignored&level=WARN&event=gateway&keyword=slow&startAt=2026-07-14T12:00:00Z&endAt=2026-07-14T10:00:00Z", nil)
	req = withManagementRuntimeLogAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	input := service.listInput
	if input.Page != 2 || input.PageSize != 20 || !input.PageSizeProvided || input.TraceID != "trace_" || input.Level != "WARN" || input.Event != "gateway" || input.Keyword != "slow" {
		t.Fatalf("input = %+v", input)
	}
	if input.StartAt.IsZero() || input.EndAt.IsZero() || !input.StartAt.Before(input.EndAt) {
		t.Fatalf("date range = %s - %s", input.StartAt, input.EndAt)
	}
	if !service.deadlineSet {
		t.Fatal("runtime log request must have a bounded deadline")
	}

	var body struct {
		Data struct {
			Items                         []map[string]any `json:"items"`
			Total                         int              `json:"total"`
			HasMore                       bool             `json:"hasMore"`
			Page                          int              `json:"page"`
			PageSize                      int              `json:"pageSize"`
			ElapsedMS                     int64            `json:"elapsedMs"`
			RetentionDays                 *int             `json:"retentionDays"`
			RetentionDaysSource           string           `json:"retentionDaysSource"`
			RuntimeAvailable              bool             `json:"runtimeAvailable"`
			WorkerSnapshotAvailable       bool             `json:"workerSnapshotAvailable"`
			RuntimeLogIndexQueueAvailable bool             `json:"runtimeLogIndexQueueAvailable"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Total != 22 || !body.Data.HasMore || body.Data.Page != 2 || body.Data.PageSize != 20 || body.Data.ElapsedMS != 2 {
		t.Fatalf("data = %+v", body.Data)
	}
	if body.Data.RetentionDays != nil || body.Data.RetentionDaysSource != "unavailable" || body.Data.RuntimeAvailable || body.Data.WorkerSnapshotAvailable || body.Data.RuntimeLogIndexQueueAvailable {
		t.Fatalf("runtime metadata = %+v", body.Data)
	}
	if len(body.Data.Items) != 1 {
		t.Fatalf("items = %+v", body.Data.Items)
	}
	if _, exists := body.Data.Items[0]["rawJson"]; exists {
		t.Fatalf("list item exposed rawJson: %+v", body.Data.Items[0])
	}
}

func TestManagementRuntimeLogsHandlerRequiresAdmin(t *testing.T) {
	service := &managementRuntimeLogServiceStub{}
	handler := newManagementRuntimeLogsHandler(service, time.Now)
	req := withManagementRuntimeLogAuth(httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs", nil), "user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if service.listCalled || service.detailCalled {
		t.Fatal("service should not be called for an ordinary user")
	}
}

func TestParseManagementRuntimeLogQueryUsesECMAScriptWhitespaceAndJSIntegers(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	input := parseManagementRuntimeLogListQuery(url.Values{
		"page":     {"\uFEFF0x2\u2029"},
		"pageSize": {"2.5"},
		"traceId":  {"\uFEFFtrace_\u2029", "ignored"},
		"keyword":  {nonECMAScriptWhitespace},
		"startAt":  {nonECMAScriptWhitespace + "2026-07-14T10:00:00Z"},
	})
	if input.Page != 2 || input.PageSize != 0 || input.PageSizeProvided {
		t.Fatalf("numeric query = page %d pageSize %d provided %v", input.Page, input.PageSize, input.PageSizeProvided)
	}
	if input.TraceID != "trace_" || input.Keyword != nonECMAScriptWhitespace {
		t.Fatalf("text query = trace %q keyword %q", input.TraceID, input.Keyword)
	}
	if !input.StartAt.IsZero() {
		t.Fatalf("non-ECMAScript whitespace must not be removed from date input: %s", input.StartAt)
	}
}

func TestManagementRuntimeLogDateTimeQueryAcceptsCurrentNodeDateForms(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("runtime-log-test-local", 8*60*60)
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{
			name: "year only is UTC",
			raw:  "2026",
			want: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "year month is UTC",
			raw:  "2026-07",
			want: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "offset ISO",
			raw:  "2026-07-14T10:00:00.123+08:00",
			want: time.Date(2026, 7, 14, 2, 0, 0, 123_000_000, time.UTC),
		},
		{
			name: "offset ISO without colon",
			raw:  "2026-07-14T10:00:00+0800",
			want: time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "lowercase ISO separators",
			raw:  "2026-07-14t10:00:00.123z",
			want: time.Date(2026, 7, 14, 10, 0, 0, 123_000_000, time.UTC),
		},
		{
			name: "RFC1123",
			raw:  "Tue, 14 Jul 2026 10:00:00 GMT",
			want: time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		},
		{
			name: "date only is UTC",
			raw:  "2026-07-14",
			want: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "local time without seconds",
			raw:  "2026-07-14T10:00",
			want: time.Date(2026, 7, 14, 10, 0, 0, 0, time.Local).UTC(),
		},
		{
			name: "space separated local time",
			raw:  "2026-07-14 10:00:00",
			want: time.Date(2026, 7, 14, 10, 0, 0, 0, time.Local).UTC(),
		},
		{
			name: "ANSIC is local time",
			raw:  "Tue Jul 14 10:00:00 2026",
			want: time.Date(2026, 7, 14, 10, 0, 0, 0, time.Local).UTC(),
		},
		{
			name: "invalid value is ignored",
			raw:  "not-a-date",
			want: time.Time{},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := managementRuntimeLogDateTimeQueryValue(testCase.raw)
			if !got.Equal(testCase.want) {
				t.Fatalf("parse %q = %s, want %s", testCase.raw, got, testCase.want)
			}
		})
	}
}

func TestManagementRuntimeLogsHandlerDetailAndRedactedErrors(t *testing.T) {
	tests := []struct {
		name       string
		detail     managementruntimelogs.Detail
		found      bool
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "detail",
			detail: managementruntimelogs.Detail{
				Summary: managementruntimelogs.Summary{ID: "runtime_1", Time: "2026-07-14T10:00:00.000Z", Level: "error", CreatedAt: "2026-07-14T10:00:01.000Z"},
				RawJSON: `{"password":"redacted"}`,
			},
			found:      true,
			wantStatus: http.StatusOK,
			wantBody:   `"rawJson":"{\"password\":\"redacted\"}"`,
		},
		{name: "not found", wantStatus: http.StatusNotFound, wantBody: "运行日志不存在"},
		{name: "store error", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantBody: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRuntimeLogServiceStub{detail: tt.detail, detailFound: tt.found, detailErr: tt.err}
			handler := newManagementRuntimeLogsHandler(service, time.Now)
			req := managementRuntimeLogDetailRequest("runtime_1")
			req = withManagementRuntimeLogAuth(req, "admin")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "postgres password leaked") {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementRuntimeLogsHandlerPreservesNonECMAScriptWhitespaceDetailID(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	service := &managementRuntimeLogServiceStub{}
	handler := newManagementRuntimeLogsHandler(service, time.Now)
	req := withManagementRuntimeLogAuth(managementRuntimeLogDetailRequest(nonECMAScriptWhitespace), "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	if !service.detailCalled || service.listCalled || service.detailID != nonECMAScriptWhitespace {
		t.Fatalf("service calls = detail %v list %v id %q", service.detailCalled, service.listCalled, service.detailID)
	}
}

func TestManagementRuntimeLogsHandlerRejectsTrimmedEmptyDetailIDWithoutListing(t *testing.T) {
	service := &managementRuntimeLogServiceStub{}
	handler := newManagementRuntimeLogsHandler(service, time.Now)
	req := withManagementRuntimeLogAuth(managementRuntimeLogDetailRequest(" \uFEFF "), "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "运行日志不存在") {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	if service.listCalled || service.detailCalled {
		t.Fatal("trimmed-empty detail id must not fall through to list or query detail")
	}
}

func TestRouterRegistersW6ManagementRuntimeLogsOnlyWhenEnabled(t *testing.T) {
	service := &managementRuntimeLogServiceStub{
		detail: managementruntimelogs.Detail{
			Summary: managementruntimelogs.Summary{ID: "rtlog_runtime_1", Time: "2026-07-14T10:00:00.000Z", Level: "info", CreatedAt: "2026-07-14T10:00:01.000Z"},
			RawJSON: `{}`,
		},
		detailFound: true,
	}
	newRouter := func(enabled bool) http.Handler {
		return NewRouter(RouterOptions{
			Config:                       config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: enabled},
			Logger:                       slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
			ManagementRuntimeLogsHandler: newManagementRuntimeLogsHandler(service, time.Now),
			ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			}),
		})
	}
	for _, testCase := range []struct {
		path         string
		wantDetailID string
	}{
		{path: "/__aisys__/api/runtime-logs"},
		{path: "/__aisys__/api/runtime-logs/rtlog_runtime_1", wantDetailID: "rtlog_runtime_1"},
		{path: "/__aisys__/api/runtime-logs/node-writer-custom-id", wantDetailID: "node-writer-custom-id"},
	} {
		req := httptest.NewRequest(http.MethodGet, testCase.path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		newRouter(true).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("enabled %s status = %d, want 200; body = %s", testCase.path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("enabled %s missing no-store", testCase.path)
		}
		if testCase.wantDetailID != "" && service.detailID != testCase.wantDetailID {
			t.Fatalf("enabled %s detail id = %q, want %q", testCase.path, service.detailID, testCase.wantDetailID)
		}
	}
	service.listCalled = false
	service.detailCalled = false
	for _, path := range []string{"/__aisys__/api/runtime-logs/facets", "/__aisys__/api/runtime-logs/grep"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		newRouter(true).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("reserved Node path %s status = %d, want 404 from Go router", path, rec.Code)
		}
	}
	if service.listCalled || service.detailCalled {
		t.Fatal("facets and grep must not be captured by the Go runtime log handler")
	}

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	newRouter(false).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d, want 404", rec.Code)
	}
}

func withManagementRuntimeLogAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            role,
	}))
}

func managementRuntimeLogDetailRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs/"+url.PathEscape(id), nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

type managementRuntimeLogServiceStub struct {
	listCalled   bool
	listInput    managementruntimelogs.ListInput
	listResult   managementruntimelogs.ListResult
	listErr      error
	detailCalled bool
	detailID     string
	detail       managementruntimelogs.Detail
	detailFound  bool
	detailErr    error
	deadlineSet  bool
}

func (s *managementRuntimeLogServiceStub) List(r *http.Request, input managementruntimelogs.ListInput) (managementruntimelogs.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	_, s.deadlineSet = r.Context().Deadline()
	return s.listResult, s.listErr
}

func (s *managementRuntimeLogServiceStub) Detail(r *http.Request, id string) (managementruntimelogs.Detail, bool, error) {
	s.detailCalled = true
	s.detailID = id
	_, s.deadlineSet = r.Context().Deadline()
	return s.detail, s.detailFound, s.detailErr
}
