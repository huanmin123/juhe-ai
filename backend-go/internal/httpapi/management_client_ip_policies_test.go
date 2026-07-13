package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientippolicies"
	"juhe-ai/backend-go/internal/store/port"
)

const managementClientIPPolicyTestHash = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestManagementClientIPPolicyHandlersReturnNodeCompatibleResponsesAndLogs(t *testing.T) {
	createdAt := time.Date(2026, 7, 14, 1, 2, 3, 456000000, time.UTC)
	reason := "可信来源"
	tests := []struct {
		name         string
		action       string
		body         string
		service      *managementClientIPPolicyHTTPServiceStub
		wantResponse string
		wantAction   string
		wantSummary  string
		wantChanges  int
	}{
		{
			name:   "allowlist",
			action: "allowlist",
			body:   `{"reason":"\u3000可信来源\u3000"}`,
			service: &managementClientIPPolicyHTTPServiceStub{
				allowlistResult: managementclientippolicies.PolicySummary{
					ID:                       "ip_policy_1",
					IPHash:                   strings.ToLower(managementClientIPPolicyTestHash),
					PolicyType:               "allowlist",
					Status:                   "active",
					Reason:                   &reason,
					CreatedBySystemAccountID: "sys_admin",
					CreatedAt:                createdAt.Format(time.RFC3339Nano),
					UpdatedAt:                createdAt.Format(time.RFC3339Nano),
				},
			},
			wantResponse: `{"data":{"id":"ip_policy_1","ipHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policyType":"allowlist","status":"active","reason":"可信来源","createdBySystemAccountId":"sys_admin","createdAt":"2026-07-14T01:02:03.456Z","updatedAt":"2026-07-14T01:02:03.456Z"}}`,
			wantAction:   "allowlist",
			wantSummary:  "加入 IP 白名单：AAAAAAAAAAAA",
			wantChanges:  4,
		},
		{
			name:   "unallowlist zero rows remains success",
			action: "unallowlist",
			body:   `{"reason":"\t可信来源\t"}`,
			service: &managementClientIPPolicyHTTPServiceStub{
				unallowlistResult: managementclientippolicies.UnallowlistResult{DisabledCount: 0},
			},
			wantResponse: `{"data":{"disabledCount":0}}`,
			wantAction:   "unallowlist",
			wantSummary:  "移出 IP 白名单：AAAAAAAAAAAA",
			wantChanges:  3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := &operationLogQueueStub{}
			handler := newManagementClientIPPolicyHandler(
				test.service,
				test.action,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client: queue,
					Now:    func() time.Time { return createdAt },
					NewLogID: func() string {
						return "oplog_ip_policy"
					},
				}),
			)
			req := managementClientIPPolicyRequest(
				test.action,
				managementClientIPPolicyTestHash,
				test.body,
				&managementauth.Context{
					SystemAccountID: "sys_admin",
					Username:        "admin",
					DisplayName:     "管理员",
					Role:            "admin",
				},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertManagementClientIPPolicyJSONEqual(t, rec.Body.String(), test.wantResponse)
			if test.service.allowlistCalls+test.service.unallowlistCalls != 1 {
				t.Fatalf("service calls allow=%d unallow=%d", test.service.allowlistCalls, test.service.unallowlistCalls)
			}
			if test.action == "allowlist" {
				if test.service.allowlistInput.IPHash != managementClientIPPolicyTestHash ||
					test.service.allowlistInput.ActorSystemAccountID != "sys_admin" ||
					test.service.allowlistInput.Reason == nil ||
					*test.service.allowlistInput.Reason != reason {
					t.Fatalf("allowlist input=%+v", test.service.allowlistInput)
				}
			} else if test.service.unallowlistInput.Reason == nil ||
				*test.service.unallowlistInput.Reason != reason {
				t.Fatalf("unallowlist input=%+v", test.service.unallowlistInput)
			}
			if queue.calls != 1 {
				t.Fatalf("operation log calls=%d", queue.calls)
			}
			logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
			if err != nil {
				t.Fatalf("decode operation log: %v", err)
			}
			if logInput.Module != "client_ip_stats" ||
				logInput.Action != test.wantAction ||
				logInput.OperationKey != "client_ip_stats."+test.wantAction ||
				logInput.ResourceType != "client_ip" ||
				logInput.ResourceID != managementClientIPPolicyTestHash ||
				logInput.ResourceName != managementClientIPPolicyTestHash[:12] ||
				logInput.Summary != test.wantSummary ||
				logInput.DetailLevel != "full" ||
				logInput.VisibilityScope != "admin_only" ||
				logInput.OperationScopeSystemAccountID != "" ||
				logInput.Mode != "admin" ||
				logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK ||
				len(logInput.Changes) != test.wantChanges {
				t.Fatalf("operation log=%+v", logInput)
			}
			if test.action == "unallowlist" && logInput.Metadata["disabledCount"] != float64(0) {
				t.Fatalf("disabledCount metadata=%#v", logInput.Metadata["disabledCount"])
			}
		})
	}
}

func TestManagementClientIPPolicyHandlersRejectInvalidInputsAndMapServiceErrorsTo400(t *testing.T) {
	longReason, err := json.Marshal(strings.Repeat("🙂", 251))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		ipHash      string
		body        string
		auth        *managementauth.Context
		serviceErr  error
		wantStatus  int
		wantMessage string
		wantCalls   int
	}{
		{name: "invalid hash", ipHash: "not-a-hash", body: `{}`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "IP 标识无效"},
		{name: "unknown body field", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{"unknown":true}`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "IP 策略参数包含未知字段"},
		{name: "reason null", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{"reason":null}`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "IP 策略参数无效"},
		{name: "reason wrong type", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{"reason":1}`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "IP 策略参数无效"},
		{name: "reason over javascript limit", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{"reason":` + string(longReason) + `}`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "原因不能超过 500 个字符"},
		{name: "top level array", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `[]`, auth: adminAuthContext(), wantStatus: 400, wantMessage: "IP 策略参数无效"},
		{name: "missing auth", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{}`, wantStatus: 401, wantMessage: "请先登录"},
		{name: "non admin", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{}`, auth: &managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, wantStatus: 403, wantMessage: "需要管理员权限"},
		{name: "service validation", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{}`, auth: adminAuthContext(), serviceErr: &managementclientippolicies.ValidationError{Message: "IP 不存在"}, wantStatus: 400, wantMessage: "IP 不存在", wantCalls: 1},
		{name: "store error remains node compatible bad request", ipHash: strings.ToLower(managementClientIPPolicyTestHash), body: `{}`, auth: adminAuthContext(), serviceErr: errors.New("存储写入失败"), wantStatus: 400, wantMessage: "存储写入失败", wantCalls: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementClientIPPolicyHTTPServiceStub{allowlistErr: test.serviceErr}
			handler := newManagementClientIPPolicyHandler(service, "allowlist", managementOperationLogOptions{})
			req := managementClientIPPolicyRequest("allowlist", test.ipHash, test.body, test.auth)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementClientIPPolicyMessage(t, rec, test.wantStatus, test.wantMessage)
			if service.allowlistCalls != test.wantCalls {
				t.Fatalf("service calls=%d want=%d", service.allowlistCalls, test.wantCalls)
			}
		})
	}
}

func TestManagementClientIPPolicyHandlerEnforces256KiBBodyLimit(t *testing.T) {
	service := &managementClientIPPolicyHTTPServiceStub{}
	handler := newManagementClientIPPolicyHandler(service, "allowlist", managementOperationLogOptions{})
	body := `{"reason":"` + strings.Repeat("x", (256<<10)+1) + `"}`
	req := managementClientIPPolicyRequest(
		"allowlist",
		strings.ToLower(managementClientIPPolicyTestHash),
		body,
		adminAuthContext(),
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertManagementClientIPPolicyMessage(t, rec, http.StatusRequestEntityTooLarge, "请求体过大")
	if service.allowlistCalls != 0 {
		t.Fatalf("service calls=%d", service.allowlistCalls)
	}
}

func TestRouterRegistersManagementClientIPPolicyWritesWithAdminGuardAndDeduplication(t *testing.T) {
	writeAuthCalls := 0
	authMiddleware := func(next http.Handler) http.Handler { return next }
	writeAuthMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeAuthCalls++
			role := r.Header.Get("X-Test-Role")
			if role == "" {
				role = "admin"
			}
			r = requestWithManagementAuthContext(r, managementauth.Context{
				SystemAccountID: "sys_" + role,
				Role:            role,
			})
			next.ServeHTTP(w, r)
		})
	}
	service := &managementClientIPPolicyHTTPServiceStub{}
	router := NewRouter(RouterOptions{
		Config:                           configForManagementClientIPPolicyRouterTest(),
		ManagementAPIAuthMiddleware:      authMiddleware,
		ManagementAPIAuthTouchMiddleware: writeAuthMiddleware,
		ManagementClientIPAllowlistHandler: newManagementClientIPPolicyHandler(
			service,
			managementClientIPPolicyActionAllowlist,
			managementOperationLogOptions{},
		),
		ManagementClientIPUnallowlistHandler: newManagementClientIPPolicyHandler(
			service,
			managementClientIPPolicyActionUnallowlist,
			managementOperationLogOptions{},
		),
	})
	hash := strings.ToLower(managementClientIPPolicyTestHash)

	malformed := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/allowlist", strings.NewReader(`{"reason":`))
	malformed.Header.Set("Content-Type", "application/json")
	malformedRec := httptest.NewRecorder()
	router.ServeHTTP(malformedRec, malformed)
	assertManagementClientIPPolicyMessage(t, malformedRec, http.StatusBadRequest, "请求体无效")
	if writeAuthCalls != 0 {
		t.Fatalf("malformed request reached auth %d times", writeAuthCalls)
	}
	arrayReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/allowlist", strings.NewReader(`[]`))
	arrayReq.Header.Set("Content-Type", "application/json")
	arrayRec := httptest.NewRecorder()
	router.ServeHTTP(arrayRec, arrayReq)
	assertManagementClientIPPolicyMessage(t, arrayRec, http.StatusBadRequest, "IP 策略参数无效")
	if service.allowlistCalls != 0 {
		t.Fatalf("array request reached service %d times", service.allowlistCalls)
	}

	for range 2 {
		userReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/allowlist", strings.NewReader(`{"reason":"x"}`))
		userReq.Header.Set("Content-Type", "application/json")
		userReq.Header.Set("X-Test-Role", "user")
		userRec := httptest.NewRecorder()
		router.ServeHTTP(userRec, userReq)
		assertManagementClientIPPolicyMessage(t, userRec, http.StatusForbidden, "需要管理员权限")
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/allowlist", strings.NewReader(`{"reason":"x"}`))
	adminReq.Header.Set("Content-Type", "application/json")
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK || service.allowlistCalls != 1 {
		t.Fatalf("admin status=%d service calls=%d body=%s", adminRec.Code, service.allowlistCalls, adminRec.Body.String())
	}
	if adminRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control=%q", adminRec.Header().Get("Cache-Control"))
	}

	duplicateReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/allowlist", strings.NewReader(`{"reason":"x"}`))
	duplicateReq.Header.Set("Content-Type", "application/json")
	duplicateRec := httptest.NewRecorder()
	router.ServeHTTP(duplicateRec, duplicateReq)
	assertManagementClientIPPolicyMessage(t, duplicateRec, http.StatusConflict, "该操作刚刚已处理，请刷新列表查看结果")
	if service.allowlistCalls != 1 {
		t.Fatalf("duplicate reached service, calls=%d", service.allowlistCalls)
	}

	unallowlistReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+hash+"/unallowlist", strings.NewReader(`{}`))
	unallowlistReq.Header.Set("Content-Type", "application/json")
	unallowlistRec := httptest.NewRecorder()
	router.ServeHTTP(unallowlistRec, unallowlistReq)
	if unallowlistRec.Code != http.StatusOK || service.unallowlistCalls != 1 {
		t.Fatalf("unallowlist status=%d service calls=%d body=%s", unallowlistRec.Code, service.unallowlistCalls, unallowlistRec.Body.String())
	}
}

func TestRouterManagementClientIPPolicyWritePipelineUsesIPAndUserLimits(t *testing.T) {
	events := []string{}
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
		events: &events,
	}
	ipLimiter := &managementAPIKeyRefreshIPLimiterStub{events: &events}
	userLimiter := &managementAPIKeyRefreshUserLimiterStub{events: &events}
	service := &managementClientIPPolicyHTTPServiceStub{events: &events}
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{
			IPWritePerMinute:         180,
			IPWriteBurstPer10Seconds: 40,
			UserWritePerMinute:       120,
		}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementClientIPAllowlistHandler: newManagementClientIPPolicyHandler(
			service,
			managementClientIPPolicyActionAllowlist,
			managementOperationLogOptions{},
		),
	})
	hash := strings.ToLower(managementClientIPPolicyTestHash)

	malformed := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/ip-stats/"+hash+"/allowlist",
		strings.NewReader(`{"reason":`),
	)
	malformed.Header.Set("Content-Type", "application/json")
	malformed.Header.Set("Cookie", "juhe_ai_session=session-token")
	malformedRec := httptest.NewRecorder()
	router.ServeHTTP(malformedRec, malformed)
	assertManagementClientIPPolicyMessage(t, malformedRec, http.StatusBadRequest, "请求体无效")
	if strings.Join(events, ",") != "ip_limit" {
		t.Fatalf("malformed events=%v", events)
	}

	events = events[:0]
	valid := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/ip-stats/"+hash+"/allowlist",
		strings.NewReader(`{"reason":"可信"}`),
	)
	valid.Header.Set("Content-Type", "application/json")
	valid.Header.Set("Cookie", "juhe_ai_session=session-token")
	validRec := httptest.NewRecorder()
	router.ServeHTTP(validRec, valid)
	if validRec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", validRec.Code, validRec.Body.String())
	}
	if strings.Join(events, ",") != "ip_limit,touch_auth,user_limit,allowlist" {
		t.Fatalf("valid events=%v", events)
	}
	if authenticator.readCalls != 0 ||
		authenticator.touchCalls != 1 ||
		ipLimiter.calls != 2 ||
		userLimiter.calls != 1 ||
		service.allowlistCalls != 1 {
		t.Fatalf(
			"read=%d touch=%d ip=%d user=%d service=%d",
			authenticator.readCalls,
			authenticator.touchCalls,
			ipLimiter.calls,
			userLimiter.calls,
			service.allowlistCalls,
		)
	}
}

func managementClientIPPolicyRequest(
	action string,
	ipHash string,
	body string,
	auth *managementauth.Context,
) *http.Request {
	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/ip-stats/"+ipHash+"/"+action,
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("ipHash", ipHash)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	if auth != nil {
		req = requestWithManagementAuthContext(req, *auth)
	}
	return req
}

func adminAuthContext() *managementauth.Context {
	return &managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin"}
}

func configForManagementClientIPPolicyRouterTest() config.Config {
	return config.Config{ManagementAPIEnabled: true}
}

func assertManagementClientIPPolicyJSONEqual(t *testing.T, got string, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v; body=%s", err, got)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v; body=%s", err, want)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON=%s want=%s", gotJSON, wantJSON)
	}
}

func assertManagementClientIPPolicyMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if body["message"] != wantMessage {
		t.Fatalf("message=%#v want=%q body=%s", body["message"], wantMessage, rec.Body.String())
	}
}

type managementClientIPPolicyHTTPServiceStub struct {
	allowlistCalls    int
	allowlistInput    managementclientippolicies.AllowlistInput
	allowlistResult   managementclientippolicies.PolicySummary
	allowlistErr      error
	unallowlistCalls  int
	unallowlistInput  managementclientippolicies.UnallowlistInput
	unallowlistResult managementclientippolicies.UnallowlistResult
	unallowlistErr    error
	events            *[]string
}

func (s *managementClientIPPolicyHTTPServiceStub) Allowlist(
	_ *http.Request,
	input managementclientippolicies.AllowlistInput,
) (managementclientippolicies.PolicySummary, error) {
	s.allowlistCalls++
	s.allowlistInput = input
	if s.events != nil {
		*s.events = append(*s.events, "allowlist")
	}
	return s.allowlistResult, s.allowlistErr
}

func (s *managementClientIPPolicyHTTPServiceStub) Unallowlist(
	_ *http.Request,
	input managementclientippolicies.UnallowlistInput,
) (managementclientippolicies.UnallowlistResult, error) {
	s.unallowlistCalls++
	s.unallowlistInput = input
	if s.events != nil {
		*s.events = append(*s.events, "unallowlist")
	}
	return s.unallowlistResult, s.unallowlistErr
}
