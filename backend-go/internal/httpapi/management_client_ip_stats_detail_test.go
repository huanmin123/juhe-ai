package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
)

func TestManagementClientIPStatsDetailHandlerParsesPathQueryAndReturnsEnvelope(t *testing.T) {
	hash := strings.Repeat("A", 64)
	result := managementclientipstats.DetailResult{
		IPHash:         strings.ToLower(hash),
		AggregateIPKey: "198.18.20",
		Items:          []managementclientipstats.AccountUsageItem{},
		PageUpperBound: 3,
		HasMore:        true,
		Page:           2,
		PageSize:       2,
		Range: managementclientipstats.UsageRange{
			StartDate: "2026-07-13",
			EndDate:   "2026-07-14",
			Days:      2,
			MaxDays:   31,
		},
		RangeReady: true,
	}
	service := &managementClientIPStatsDetailServiceStub{result: result}
	handler := newManagementClientIPStatsDetailHandler(service)
	query := url.Values{
		"page":            {"\uFEFF0x2\u2029"},
		"pageSize":        {"\u20032\u2028"},
		"startDate":       {"\u16802026-07-13\u200A"},
		"endDate":         {"\u202F2026-07-14\u205F"},
		"sortField":       {"lastUsedAt"},
		"sortOrder":       {"asc"},
		"unknownRepeated": {"first", "second"},
	}
	request := managementClientIPStatsDetailRequest(
		"\u00A0"+hash+"\u3000",
		"?"+query.Encode(),
		managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	wantInput := managementclientipstats.DetailInput{
		IPHash:    hash,
		Page:      2,
		PageSize:  2,
		StartDate: "2026-07-13",
		EndDate:   "2026-07-14",
		SortField: "lastUsedAt",
		SortOrder: "asc",
	}
	if service.calls != 1 || service.input != wantInput {
		t.Fatalf("service calls/input = %d / %+v, want 1 / %+v", service.calls, service.input, wantInput)
	}
	var envelope DataResponse
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	var response managementclientipstats.DetailResult
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response data: %v", err)
	}
	if response.IPHash != result.IPHash || response.AggregateIPKey != result.AggregateIPKey || response.Items == nil || response.PageUpperBound != 3 || !response.HasMore || !response.RangeReady {
		t.Fatalf("response = %+v", response)
	}
}

func TestManagementClientIPStatsDetailHandlerPreservesSortOrderWithoutSortField(t *testing.T) {
	service := &managementClientIPStatsDetailServiceStub{result: managementclientipstats.DetailResult{Items: []managementclientipstats.AccountUsageItem{}}}
	handler := newManagementClientIPStatsDetailHandler(service)
	request := managementClientIPStatsDetailRequest(
		strings.Repeat("b", 64),
		"?sortOrder=asc",
		managementauth.Context{SystemAccountID: "sys_admin", Role: "super_admin"},
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || service.calls != 1 || service.input.SortField != "" || service.input.SortOrder != "asc" {
		t.Fatalf("status/calls/input = %d / %d / %+v; body = %s", recorder.Code, service.calls, service.input, recorder.Body.String())
	}
}

func TestManagementClientIPStatsDetailHandlerRejectsInvalidPathAndQuery(t *testing.T) {
	tests := []struct {
		name        string
		hash        string
		query       string
		wantMessage string
	}{
		{name: "invalid hash", hash: "not-a-hash", wantMessage: "IP 标识无效"},
		{name: "page zero", hash: strings.Repeat("c", 64), query: "?page=0", wantMessage: "IP 详情参数无效"},
		{name: "page size over max", hash: strings.Repeat("c", 64), query: "?pageSize=101", wantMessage: "IP 详情参数无效"},
		{name: "invalid sort field", hash: strings.Repeat("c", 64), query: "?sortField=unknown", wantMessage: "IP 详情参数无效"},
		{name: "invalid sort order", hash: strings.Repeat("c", 64), query: "?sortOrder=up", wantMessage: "IP 详情参数无效"},
		{name: "duplicate page", hash: strings.Repeat("c", 64), query: "?page=1&page=2", wantMessage: "IP 详情参数无效"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &managementClientIPStatsDetailServiceStub{}
			handler := newManagementClientIPStatsDetailHandler(service)
			request := managementClientIPStatsDetailRequest(
				testCase.hash,
				testCase.query,
				managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertManagementClientIPStatsMessage(t, recorder, http.StatusBadRequest, testCase.wantMessage)
			if service.calls != 0 {
				t.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementClientIPStatsDetailHandlerMapsAuthNotFoundAndInternalErrors(t *testing.T) {
	hash := strings.Repeat("d", 64)
	tests := []struct {
		name       string
		auth       *managementauth.Context
		service    managementClientIPStatsDetailService
		wantStatus int
		wantText   string
		wantCalls  int
	}{
		{name: "missing auth", service: &managementClientIPStatsDetailServiceStub{}, wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
		{name: "non admin", auth: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, service: &managementClientIPStatsDetailServiceStub{}, wantStatus: http.StatusForbidden, wantText: "需要管理员权限"},
		{name: "nil service", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
		{name: "not found", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, service: &managementClientIPStatsDetailServiceStub{err: managementclientipstats.ErrIPNotFound}, wantStatus: http.StatusNotFound, wantText: "IP 不存在", wantCalls: 1},
		{name: "internal", auth: &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, service: &managementClientIPStatsDetailServiceStub{err: errors.New("postgres password leaked")}, wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误", wantCalls: 1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := newManagementClientIPStatsDetailHandler(testCase.service)
			request := managementClientIPStatsDetailRequest(hash, "", managementauth.Context{})
			if testCase.auth == nil {
				request = managementClientIPStatsDetailRequestWithoutAuth(hash, "")
			} else {
				request = managementClientIPStatsDetailRequest(hash, "", *testCase.auth)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assertManagementClientIPStatsMessage(t, recorder, testCase.wantStatus, testCase.wantText)
			if stub, ok := testCase.service.(*managementClientIPStatsDetailServiceStub); ok && stub.calls != testCase.wantCalls {
				t.Fatalf("service calls = %d, want %d", stub.calls, testCase.wantCalls)
			}
			if strings.Contains(recorder.Body.String(), "postgres") || strings.Contains(recorder.Body.String(), "password") {
				t.Fatalf("response leaked internal error: %s", recorder.Body.String())
			}
		})
	}
}

func managementClientIPStatsDetailRequest(
	ipHash string,
	query string,
	authContext managementauth.Context,
) *http.Request {
	return requestWithManagementAuthContext(
		managementClientIPStatsDetailRequestWithoutAuth(ipHash, query),
		authContext,
	)
}

func managementClientIPStatsDetailRequestWithoutAuth(ipHash string, query string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats/value/detail"+query, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("ipHash", ipHash)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

type managementClientIPStatsDetailServiceStub struct {
	calls  int
	input  managementclientipstats.DetailInput
	result managementclientipstats.DetailResult
	err    error
}

func (service *managementClientIPStatsDetailServiceStub) Detail(
	_ *http.Request,
	input managementclientipstats.DetailInput,
) (managementclientipstats.DetailResult, error) {
	service.calls++
	service.input = input
	return service.result, service.err
}

var _ managementClientIPStatsDetailService = (*managementClientIPStatsDetailServiceStub)(nil)
