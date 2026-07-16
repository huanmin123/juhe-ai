package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
)

func TestManagementClientIPStatsHandlerParsesQueryAndReturnsEnvelope(test *testing.T) {
	result := managementclientipstats.ListResult{
		Items:          []managementclientipstats.ListItem{},
		PageUpperBound: 201,
		HasMore:        true,
		Page:           2,
		PageSize:       100,
		Range: managementclientipstats.UsageRange{
			StartDate: "2026-06-15",
			EndDate:   "2026-07-14",
			Days:      30,
			MaxDays:   31,
		},
		RangeReady: true,
	}
	service := &managementClientIPStatsServiceStub{result: result}
	handler := newManagementClientIPStatsHandler(service)
	query := url.Values{
		"page":                  {"\uFEFF0x2\u2029"},
		"pageSize":              {"\u20031e2\u2028"},
		"keyword":               {"\u00A0 203.0.113 \u3000"},
		"status":                {"blacklisted"},
		"startDate":             {"\u16802026-06-15\u200A"},
		"endDate":               {"\u202F2026-07-14\u205F"},
		"lastUsedStartDate":     {"\u000B2026-07-01\u000C"},
		"lastUsedEndDate":       {"\u00092026-07-14\u000D"},
		"sortField":             {"totalCost"},
		"sortOrder":             {"asc"},
		"lastUsedSortScope":     {"range"},
		"unknownRepeatedOption": {"first", "second"},
	}
	request := managementClientIPStatsRequest("?"+query.Encode(), managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		test.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if service.calls != 1 {
		test.Fatalf("service calls = %d, want 1", service.calls)
	}
	wantInput := managementclientipstats.ListInput{
		Page:              2,
		PageSize:          100,
		Keyword:           "203.0.113",
		Status:            "blacklisted",
		StartDate:         "2026-06-15",
		EndDate:           "2026-07-14",
		LastUsedStartDate: "2026-07-01",
		LastUsedEndDate:   "2026-07-14",
		SortField:         "totalCost",
		SortOrder:         "asc",
	}
	if service.input != wantInput {
		test.Fatalf("service input = %+v, want %+v", service.input, wantInput)
	}
	var envelope DataResponse
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		test.Fatalf("decode response: %v", err)
	}
	encodedData, err := json.Marshal(envelope.Data)
	if err != nil {
		test.Fatalf("encode response data: %v", err)
	}
	var responseResult managementclientipstats.ListResult
	if err := json.Unmarshal(encodedData, &responseResult); err != nil {
		test.Fatalf("decode response data: %v", err)
	}
	if responseResult.Page != result.Page ||
		responseResult.PageSize != result.PageSize ||
		responseResult.PageUpperBound != result.PageUpperBound ||
		!responseResult.HasMore ||
		!responseResult.RangeReady ||
		responseResult.Items == nil {
		test.Fatalf("response data = %+v", responseResult)
	}
}

func TestManagementClientIPStatsHandlerPreservesNonECMAScriptWhitespaceAndLooseDates(test *testing.T) {
	service := &managementClientIPStatsServiceStub{
		result: managementclientipstats.ListResult{Items: []managementclientipstats.ListItem{}},
	}
	handler := newManagementClientIPStatsHandler(service)
	query := url.Values{
		"keyword":           {"\u0085needle\u0085"},
		"startDate":         {"\u0085not-a-date\u0085"},
		"endDate":           {"2026-99-99"},
		"lastUsedStartDate": {"also-not-a-date"},
		"lastUsedEndDate":   {""},
	}
	request := managementClientIPStatsRequest("?"+query.Encode(), managementauth.Context{
		SystemAccountID: "sys_super_admin",
		Role:            "super_admin",
	})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		test.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if service.calls != 1 {
		test.Fatalf("service calls = %d, want 1", service.calls)
	}
	if service.input.Keyword != "\u0085needle\u0085" ||
		service.input.StartDate != "\u0085not-a-date\u0085" ||
		service.input.EndDate != "2026-99-99" ||
		service.input.LastUsedStartDate != "also-not-a-date" ||
		service.input.LastUsedEndDate != "" {
		test.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementClientIPStatsHandlerRejectsInvalidQueryWithoutCallingService(test *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "empty page", query: "page="},
		{name: "invalid page", query: "page=bad"},
		{name: "zero page", query: "page=0"},
		{name: "negative page", query: "page=-1"},
		{name: "fractional page", query: "page=1.5"},
		{name: "infinite page", query: "page=1e309"},
		{name: "non ECMAScript whitespace page", query: url.Values{"page": {"\u00852\u0085"}}.Encode()},
		{name: "empty page size", query: "pageSize="},
		{name: "zero page size", query: "pageSize=0"},
		{name: "page size above maximum", query: "pageSize=101"},
		{name: "fractional page size", query: "pageSize=99.5"},
		{name: "invalid status", query: "status=blocked"},
		{name: "trimmed status is invalid", query: "status=%20normal%20"},
		{name: "invalid sort field", query: "sortField=duration"},
		{name: "trimmed sort field is invalid", query: "sortField=%20requestCount%20"},
		{name: "invalid sort order", query: "sortOrder=ascending"},
		{name: "trimmed sort order is invalid", query: "sortOrder=%20asc%20"},
		{name: "duplicate page", query: "page=1&page=2"},
		{name: "duplicate page size", query: "pageSize=20&pageSize=100"},
		{name: "duplicate keyword", query: "keyword=first&keyword=second"},
		{name: "duplicate status", query: "status=all&status=normal"},
		{name: "duplicate start date", query: "startDate=2026-07-01&startDate=2026-07-02"},
		{name: "duplicate end date", query: "endDate=2026-07-01&endDate=2026-07-02"},
		{name: "duplicate last used start date", query: "lastUsedStartDate=2026-07-01&lastUsedStartDate=2026-07-02"},
		{name: "duplicate last used end date", query: "lastUsedEndDate=2026-07-01&lastUsedEndDate=2026-07-02"},
		{name: "duplicate sort field", query: "sortField=requestCount&sortField=errorCount"},
		{name: "duplicate sort order", query: "sortOrder=asc&sortOrder=desc"},
	}

	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			service := &managementClientIPStatsServiceStub{}
			handler := newManagementClientIPStatsHandler(service)
			request := managementClientIPStatsRequest("?"+testCase.query, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertManagementClientIPStatsMessage(test, recorder, http.StatusBadRequest, "IP 统计参数无效")
			if service.calls != 0 {
				test.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementClientIPStatsHandlerAcceptsNumericAndDefaultBoundaries(test *testing.T) {
	tests := []struct {
		name         string
		query        string
		wantPage     int
		wantPageSize int
	}{
		{name: "defaults", query: "unknown=first&unknown=second"},
		{name: "minimums", query: "page=1&pageSize=1", wantPage: 1, wantPageSize: 1},
		{name: "maximum page size", query: "page=0b10&pageSize=0o144", wantPage: 2, wantPageSize: 100},
		{name: "large finite page", query: "page=1e308&pageSize=0x64", wantPage: int(^uint(0) >> 1), wantPageSize: 100},
	}

	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			service := &managementClientIPStatsServiceStub{
				result: managementclientipstats.ListResult{Items: []managementclientipstats.ListItem{}},
			}
			handler := newManagementClientIPStatsHandler(service)
			request := managementClientIPStatsRequest("?"+testCase.query, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				test.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
			}
			if service.calls != 1 || service.input.Page != testCase.wantPage || service.input.PageSize != testCase.wantPageSize {
				test.Fatalf("service calls = %d, input = %+v", service.calls, service.input)
			}
		})
	}
}

func TestManagementClientIPStatsHandlerRequiresAuthContextAndAdministratorRole(test *testing.T) {
	tests := []struct {
		name        string
		authContext *managementauth.Context
		wantStatus  int
		wantMessage string
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
	}

	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			service := &managementClientIPStatsServiceStub{}
			handler := newManagementClientIPStatsHandler(service)
			request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats", nil)
			if testCase.authContext != nil {
				request = requestWithManagementAuthContext(request, *testCase.authContext)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertManagementClientIPStatsMessage(test, recorder, testCase.wantStatus, testCase.wantMessage)
			if service.calls != 0 {
				test.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementClientIPStatsHandlerHandlesNilServiceAndRedactsErrors(test *testing.T) {
	test.Run("nil service", func(test *testing.T) {
		handler := NewManagementClientIPStatsHandler(nil)
		request := managementClientIPStatsRequest("", managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
		})
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		assertManagementClientIPStatsMessage(test, recorder, http.StatusInternalServerError, "服务器内部错误")
	})

	test.Run("service error", func(test *testing.T) {
		service := &managementClientIPStatsServiceStub{err: errors.New("postgres password leaked")}
		handler := newManagementClientIPStatsHandler(service)
		request := managementClientIPStatsRequest("", managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
		})
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		assertManagementClientIPStatsMessage(test, recorder, http.StatusInternalServerError, "服务器内部错误")
		if service.calls != 1 {
			test.Fatalf("service calls = %d, want 1", service.calls)
		}
		if strings.Contains(recorder.Body.String(), "postgres") || strings.Contains(recorder.Body.String(), "password") {
			test.Fatalf("response leaked service error: %s", recorder.Body.String())
		}
	})
}

func managementClientIPStatsRequest(query string, authContext managementauth.Context) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats"+query, nil)
	return requestWithManagementAuthContext(request, authContext)
}

func assertManagementClientIPStatsMessage(
	test *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	test.Helper()
	if recorder.Code != wantStatus {
		test.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		test.Fatalf("decode response: %v", err)
	}
	if body["message"] != wantMessage {
		test.Fatalf("message = %q, want %q", body["message"], wantMessage)
	}
}

type managementClientIPStatsServiceStub struct {
	calls  int
	input  managementclientipstats.ListInput
	result managementclientipstats.ListResult
	err    error
}

func (service *managementClientIPStatsServiceStub) List(
	_ *http.Request,
	input managementclientipstats.ListInput,
) (managementclientipstats.ListResult, error) {
	service.calls++
	service.input = input
	return service.result, service.err
}

var _ managementClientIPStatsService = (*managementClientIPStatsServiceStub)(nil)
