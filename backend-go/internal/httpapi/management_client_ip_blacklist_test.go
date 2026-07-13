package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientippolicies"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementClientIPBlacklistHandlerAcceptsNodeEquivalentJSONIntegers(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMinutes *int
		wantDays    *int
	}{
		{name: "integer", body: `{"durationMinutes":1}`, wantMinutes: clientIPPolicyInt(1)},
		{name: "decimal integer", body: `{"durationMinutes":1.0}`, wantMinutes: clientIPPolicyInt(1)},
		{name: "exponent integer", body: `{"durationMinutes":1e0}`, wantMinutes: clientIPPolicyInt(1)},
		{name: "node rounded integer", body: `{"durationMinutes":1.0000000000000001}`, wantMinutes: clientIPPolicyInt(1)},
		{name: "days", body: `{"durationDays":2.0}`, wantDays: clientIPPolicyInt(2)},
		{name: "permanent", body: `{}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementClientIPPolicyHTTPServiceStub{
				blacklistResult: managementclientippolicies.PolicySummary{ID: "ip_policy_blacklist"},
			}
			handler := newManagementClientIPPolicyHandler(
				service,
				managementClientIPPolicyActionBlacklist,
				managementOperationLogOptions{},
			)
			auth := &managementauth.Context{
				SystemAccountID: "sys_super_admin",
				Role:            "super_admin",
			}
			req := managementClientIPPolicyRequest(
				managementClientIPPolicyActionBlacklist,
				managementClientIPPolicyTestHash,
				test.body,
				auth,
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if service.blacklistCalls != 1 ||
				service.blacklistInput.IPHash != managementClientIPPolicyTestHash ||
				service.blacklistInput.ActorSystemAccountID != "sys_super_admin" {
				t.Fatalf("calls=%d input=%+v", service.blacklistCalls, service.blacklistInput)
			}
			assertClientIPPolicyIntPointer(t, "durationMinutes", service.blacklistInput.DurationMinutes, test.wantMinutes)
			assertClientIPPolicyIntPointer(t, "durationDays", service.blacklistInput.DurationDays, test.wantDays)
		})
	}
}

func TestManagementClientIPBlacklistHandlerRejectsFractionsAndPreservesServiceValidationMessages(t *testing.T) {
	service := managementclientippolicies.NewService(nil)
	handler := NewManagementClientIPBlacklistHandlerWithOperationLog(
		service,
		ManagementOperationLogOptions{},
	)
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{name: "fraction", body: `{"durationMinutes":1.5}`, wantMessage: "IP 策略参数无效"},
		{name: "minutes below range", body: `{"durationMinutes":0}`, wantMessage: "封禁分钟数不能小于 1"},
		{name: "minutes above range", body: `{"durationMinutes":525601}`, wantMessage: "封禁分钟数不能超过 525600"},
		{name: "large node integer reaches service", body: `{"durationMinutes":1e20}`, wantMessage: "封禁分钟数不能超过 525600"},
		{name: "node underflow reaches service", body: `{"durationMinutes":1e-324}`, wantMessage: "封禁分钟数不能小于 1"},
		{name: "days below range", body: `{"durationDays":0}`, wantMessage: "封禁天数不能小于 1"},
		{name: "days above range", body: `{"durationDays":3651}`, wantMessage: "封禁天数不能超过 3650"},
		{name: "duration mutually exclusive", body: `{"durationMinutes":1,"durationDays":1}`, wantMessage: "封禁时长只能选择一种"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := managementClientIPPolicyRequest(
				managementClientIPPolicyActionBlacklist,
				strings.ToLower(managementClientIPPolicyTestHash),
				test.body,
				adminAuthContext(),
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementClientIPPolicyMessage(t, rec, http.StatusBadRequest, test.wantMessage)
		})
	}
}

func TestRouterManagementClientIPBlacklistStrictJSONBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		action      string
		body        string
		wantMessage string
	}{
		{name: "top level null", action: "blacklist", body: `null`, wantMessage: "请求体无效"},
		{name: "top level string", action: "blacklist", body: `"value"`, wantMessage: "请求体无效"},
		{name: "top level boolean", action: "blacklist", body: `true`, wantMessage: "请求体无效"},
		{name: "top level array reaches policy schema", action: "blacklist", body: `[]`, wantMessage: "IP 策略参数无效"},
		{name: "unknown field", action: "blacklist", body: `{"unknown":true}`, wantMessage: "IP 策略参数包含未知字段"},
		{name: "trailing json", action: "blacklist", body: `{} {}`, wantMessage: "请求体无效"},
		{name: "reason null", action: "blacklist", body: `{"reason":null}`, wantMessage: "IP 策略参数无效"},
		{name: "duration null", action: "blacklist", body: `{"durationMinutes":null}`, wantMessage: "IP 策略参数无效"},
		{name: "duration string", action: "blacklist", body: `{"durationMinutes":"1"}`, wantMessage: "IP 策略参数无效"},
		{name: "duration boolean", action: "blacklist", body: `{"durationMinutes":true}`, wantMessage: "IP 策略参数无效"},
		{name: "duration array", action: "blacklist", body: `{"durationMinutes":[1]}`, wantMessage: "IP 策略参数无效"},
		{name: "unblock rejects duration", action: "unblock", body: `{"durationMinutes":1}`, wantMessage: "IP 策略参数包含未知字段"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementClientIPPolicyHTTPServiceStub{}
			router := newManagementClientIPBlacklistRouter(service, "admin")
			hash := strings.ToLower(managementClientIPPolicyTestHash)
			req := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/ip-stats/"+hash+"/"+test.action,
				strings.NewReader(test.body),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertManagementClientIPPolicyMessage(t, rec, http.StatusBadRequest, test.wantMessage)
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache control=%q", rec.Header().Get("Cache-Control"))
			}
			if service.blacklistCalls != 0 || service.unblockCalls != 0 {
				t.Fatalf("service calls blacklist=%d unblock=%d", service.blacklistCalls, service.unblockCalls)
			}
		})
	}
}

func TestRouterManagementClientIPBlacklistEnforces256KiBBodyLimit(t *testing.T) {
	service := &managementClientIPPolicyHTTPServiceStub{}
	router := newManagementClientIPBlacklistRouter(service, "admin")
	hash := strings.ToLower(managementClientIPPolicyTestHash)
	body := `{"reason":"` + strings.Repeat("x", managementClientIPPolicyMaxBodyBytes) + `"}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/ip-stats/"+hash+"/blacklist",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assertManagementClientIPPolicyMessage(t, rec, http.StatusRequestEntityTooLarge, "请求体过大")
	if rec.Header().Get("Cache-Control") != "no-store" || service.blacklistCalls != 0 {
		t.Fatalf("cache=%q calls=%d", rec.Header().Get("Cache-Control"), service.blacklistCalls)
	}
}

func TestManagementClientIPBlacklistOperationLogMatchesNodePayload(t *testing.T) {
	expiresMinute := "2026-07-14T02:03:04.567Z"
	expiresDay := "2026-07-15T02:03:04.567Z"
	tests := []struct {
		name              string
		body              string
		expiresAt         *string
		wantLabel         string
		wantDurationKey   string
		wantDurationValue float64
	}{
		{name: "permanent", body: `{"reason":" 永久封禁 "}`, wantLabel: "永久"},
		{name: "minutes", body: `{"reason":" 临时封禁 ","durationMinutes":5.0}`, expiresAt: &expiresMinute, wantLabel: "5 分钟", wantDurationKey: "durationMinutes", wantDurationValue: 5},
		{name: "days", body: `{"reason":" 临时封禁 ","durationDays":1e0}`, expiresAt: &expiresDay, wantLabel: "1 天", wantDurationKey: "durationDays", wantDurationValue: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := &operationLogQueueStub{}
			service := &managementClientIPPolicyHTTPServiceStub{
				blacklistResult: managementclientippolicies.PolicySummary{
					ID:         "ip_policy_blacklist",
					PolicyType: "blacklist",
					Status:     "active",
					ExpiresAt:  test.expiresAt,
				},
			}
			handler := newManagementClientIPPolicyHandler(
				service,
				managementClientIPPolicyActionBlacklist,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client: queue,
					Now: func() time.Time {
						return time.Date(2026, 7, 14, 1, 2, 3, 456000000, time.UTC)
					},
					NewLogID: func() string { return "oplog_blacklist" },
				}),
			)
			req := managementClientIPPolicyRequest(
				managementClientIPPolicyActionBlacklist,
				managementClientIPPolicyTestHash,
				test.body,
				adminAuthContext(),
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || queue.calls != 1 {
				t.Fatalf("status=%d queue=%d body=%s", rec.Code, queue.calls, rec.Body.String())
			}
			logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
			if err != nil {
				t.Fatalf("decode operation log: %v", err)
			}
			assertClientIPBlacklistLogCommon(t, logInput, "blacklist", "封禁 IP：AAAAAAAAAAAA", 4)
			if logInput.Changes[0].Field != "reason" ||
				logInput.Changes[1].Field != "policyType" || logInput.Changes[1].After != "blacklist" ||
				logInput.Changes[2].Field != "duration" || logInput.Changes[2].Label != "封禁时长" || logInput.Changes[2].After != test.wantLabel ||
				logInput.Changes[3].Field != "expiresAt" {
				t.Fatalf("changes=%+v", logInput.Changes)
			}
			wantReason := "永久封禁"
			if test.name != "permanent" {
				wantReason = "临时封禁"
			}
			if logInput.Changes[0].After != wantReason ||
				logInput.Metadata["ipHash"] != managementClientIPPolicyTestHash ||
				logInput.Metadata["policyId"] != "ip_policy_blacklist" ||
				logInput.Metadata["policyType"] != "blacklist" ||
				logInput.Metadata["durationLabel"] != test.wantLabel ||
				logInput.Metadata["reason"] != wantReason {
				t.Fatalf("metadata=%+v changes=%+v", logInput.Metadata, logInput.Changes)
			}
			if test.expiresAt == nil {
				if logInput.Changes[3].After != nil {
					t.Fatalf("expires change=%#v", logInput.Changes[3].After)
				}
				if _, exists := logInput.Metadata["expiresAt"]; exists {
					t.Fatalf("permanent metadata=%+v", logInput.Metadata)
				}
			} else if logInput.Changes[3].After != *test.expiresAt || logInput.Metadata["expiresAt"] != *test.expiresAt {
				t.Fatalf("expires metadata=%+v changes=%+v", logInput.Metadata, logInput.Changes)
			}
			if test.wantDurationKey != "" && logInput.Metadata[test.wantDurationKey] != test.wantDurationValue {
				t.Fatalf("duration metadata=%+v", logInput.Metadata)
			}
		})
	}
}

func TestManagementClientIPUnblockZeroRowsStillLogsSuccess(t *testing.T) {
	queue := &operationLogQueueStub{}
	service := &managementClientIPPolicyHTTPServiceStub{
		unblockResult: managementclientippolicies.UnblockResult{DisabledCount: 0},
	}
	handler := newManagementClientIPPolicyHandler(
		service,
		managementClientIPPolicyActionUnblock,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			Now:      func() time.Time { return time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC) },
			NewLogID: func() string { return "oplog_unblock" },
		}),
	)
	req := managementClientIPPolicyRequest(
		managementClientIPPolicyActionUnblock,
		managementClientIPPolicyTestHash,
		`{"reason":" 解除误封 "}`,
		adminAuthContext(),
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "{\"data\":{\"disabledCount\":0}}\n" || queue.calls != 1 {
		t.Fatalf("status=%d queue=%d body=%s", rec.Code, queue.calls, rec.Body.String())
	}
	if service.unblockCalls != 1 || service.unblockInput.Reason == nil || *service.unblockInput.Reason != "解除误封" {
		t.Fatalf("calls=%d input=%+v", service.unblockCalls, service.unblockInput)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	assertClientIPBlacklistLogCommon(t, logInput, "unblock", "解除 IP 封禁：AAAAAAAAAAAA", 3)
	if logInput.Changes[0].Field != "disabledCount" || logInput.Changes[0].After != float64(0) ||
		logInput.Changes[1].Field != "policyType" || logInput.Changes[1].Before != "blacklist" || logInput.Changes[1].After != nil ||
		logInput.Changes[2].Field != "reason" || logInput.Changes[2].After != "解除误封" ||
		logInput.Metadata["ipHash"] != managementClientIPPolicyTestHash ||
		logInput.Metadata["policyType"] != "blacklist" ||
		logInput.Metadata["disabledCount"] != float64(0) ||
		logInput.Metadata["reason"] != "解除误封" {
		t.Fatalf("operation log=%+v", logInput)
	}
}

func TestRouterManagementClientIPBlacklistGuardNormalizesNodeEquivalentNumbers(t *testing.T) {
	service := &managementClientIPPolicyHTTPServiceStub{
		blacklistResult: managementclientippolicies.PolicySummary{ID: "ip_policy_blacklist"},
		unblockResult:   managementclientippolicies.UnblockResult{},
	}
	router := newManagementClientIPBlacklistRouter(service, "")
	hash := strings.ToLower(managementClientIPPolicyTestHash)
	postAtHash := func(role string, pathHash string, action string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/__aisys__/api/ip-stats/"+pathHash+"/"+action,
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Role", role)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	post := func(role string, action string, body string) *httptest.ResponseRecorder {
		return postAtHash(role, hash, action, body)
	}

	for range 2 {
		rec := post("user", "blacklist", `{"reason":"x","durationMinutes":1}`)
		assertManagementClientIPPolicyMessage(t, rec, http.StatusForbidden, "需要管理员权限")
	}
	if service.blacklistCalls != 0 {
		t.Fatalf("user request reached service %d times", service.blacklistCalls)
	}

	first := post("admin", "blacklist", `{"reason":"x","durationMinutes":1}`)
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first status=%d cache=%q body=%s", first.Code, first.Header().Get("Cache-Control"), first.Body.String())
	}
	for _, body := range []string{
		`{"reason":"x","durationMinutes":1.0}`,
		`{"reason":"x","durationMinutes":1e0}`,
		`{"reason":"x","durationMinutes":1.0000000000000001}`,
	} {
		rec := post("admin", "blacklist", body)
		assertManagementClientIPPolicyMessage(t, rec, http.StatusConflict, "该操作刚刚已处理，请刷新列表查看结果")
	}
	for _, body := range []string{
		`{"reason":"x","durationMinutes":2}`,
		`{"reason":"x","durationDays":1}`,
		`{"reason":"other","durationMinutes":1}`,
		`{"reason":" x ","durationMinutes":1}`,
	} {
		rec := post("admin", "blacklist", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("distinct fingerprint status=%d body=%s request=%s", rec.Code, rec.Body.String(), body)
		}
	}
	uppercaseHash := postAtHash("admin", strings.ToUpper(hash), "blacklist", `{"reason":"x","durationMinutes":1}`)
	if uppercaseHash.Code != http.StatusOK {
		t.Fatalf("original hash fingerprint status=%d body=%s", uppercaseHash.Code, uppercaseHash.Body.String())
	}
	permanent := post("admin", "blacklist", `{"reason":"overflow"}`)
	if permanent.Code != http.StatusOK {
		t.Fatalf("permanent status=%d body=%s", permanent.Code, permanent.Body.String())
	}
	for _, body := range []string{
		`{"reason":"overflow","durationMinutes":1e400}`,
		`{"reason":"overflow","durationMinutes":-1e400}`,
		`{"reason":"overflow","durationMinutes":1e401}`,
		`{"reason":"overflow","durationDays":1e400}`,
	} {
		rec := post("admin", "blacklist", body)
		assertManagementClientIPPolicyMessage(t, rec, http.StatusConflict, "该操作刚刚已处理，请刷新列表查看结果")
	}
	if service.blacklistCalls != 7 {
		t.Fatalf("blacklist calls=%d want=7", service.blacklistCalls)
	}

	unblock := post("admin", "unblock", `{"reason":"x"}`)
	if unblock.Code != http.StatusOK || service.unblockCalls != 1 {
		t.Fatalf("unblock status=%d calls=%d body=%s", unblock.Code, service.unblockCalls, unblock.Body.String())
	}
	duplicateUnblock := post("admin", "unblock", `{"reason":"x"}`)
	assertManagementClientIPPolicyMessage(t, duplicateUnblock, http.StatusConflict, "该操作刚刚已处理，请刷新列表查看结果")
}

func TestRouterManagementClientIPBlacklistWritePipelineUsesTouchAndBothLimiters(t *testing.T) {
	tests := []struct {
		action string
		body   string
	}{
		{action: "blacklist", body: `{"durationMinutes":1}`},
		{action: "unblock", body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
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
				ManagementClientIPBlacklistHandler: newManagementClientIPPolicyHandler(
					service,
					managementClientIPPolicyActionBlacklist,
					managementOperationLogOptions{},
				),
				ManagementClientIPUnblockHandler: newManagementClientIPPolicyHandler(
					service,
					managementClientIPPolicyActionUnblock,
					managementOperationLogOptions{},
				),
			})
			hash := strings.ToLower(managementClientIPPolicyTestHash)
			malformed := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/ip-stats/"+hash+"/"+test.action,
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

			req := httptest.NewRequest(
				http.MethodPost,
				"/__aisys__/api/ip-stats/"+hash+"/"+test.action,
				strings.NewReader(test.body),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
			}
			if strings.Join(events, ",") != "ip_limit,touch_auth,user_limit,"+test.action {
				t.Fatalf("events=%v", events)
			}
			if authenticator.readCalls != 0 || authenticator.touchCalls != 1 || ipLimiter.calls != 2 || userLimiter.calls != 1 {
				t.Fatalf("read=%d touch=%d ip=%d user=%d", authenticator.readCalls, authenticator.touchCalls, ipLimiter.calls, userLimiter.calls)
			}
		})
	}
}

func TestRouterClassifiesClientIPBlacklistActionsAsRequiredManagementWrites(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, opts := range []RouterOptions{
		{ManagementClientIPBlacklistHandler: handler},
		{ManagementClientIPUnblockHandler: handler},
	} {
		if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
			t.Fatalf("client IP action was not classified as management business write: %+v", opts)
		}
	}

	defer func() {
		recovered := recover()
		if recovered != "ManagementAPIAuthTouchMiddleware is required for Go management write routes" {
			t.Fatalf("panic=%#v", recovered)
		}
	}()
	_ = NewRouter(RouterOptions{
		Config:                             config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:        func(next http.Handler) http.Handler { return next },
		ManagementClientIPBlacklistHandler: handler,
	})
}

func TestManagementClientIPBlacklistHandlersMapServiceAndStoreErrorsTo400(t *testing.T) {
	tests := []struct {
		action string
	}{
		{action: managementClientIPPolicyActionBlacklist},
		{action: managementClientIPPolicyActionUnblock},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			queue := &operationLogQueueStub{}
			service := &managementClientIPPolicyHTTPServiceStub{}
			if test.action == managementClientIPPolicyActionBlacklist {
				service.blacklistErr = errors.New("存储写入失败")
			} else {
				service.unblockErr = errors.New("存储写入失败")
			}
			handler := newManagementClientIPPolicyHandler(
				service,
				test.action,
				newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queue}),
			)
			req := managementClientIPPolicyRequest(
				test.action,
				strings.ToLower(managementClientIPPolicyTestHash),
				`{}`,
				adminAuthContext(),
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementClientIPPolicyMessage(t, rec, http.StatusBadRequest, "存储写入失败")
			if queue.calls != 0 {
				t.Fatalf("failed mutation logged success %d times", queue.calls)
			}
		})
	}
}

func TestManagementClientIPBlacklistHandlersDefensivelyRequireAdmin(t *testing.T) {
	service := &managementClientIPPolicyHTTPServiceStub{}
	tests := []struct {
		action string
		body   string
	}{
		{action: "blacklist", body: `{}`},
		{action: "unblock", body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			handler := newManagementClientIPPolicyHandler(
				service,
				test.action,
				managementOperationLogOptions{},
			)
			req := managementClientIPPolicyRequest(
				test.action,
				strings.ToLower(managementClientIPPolicyTestHash),
				test.body,
				&managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementClientIPPolicyMessage(t, rec, http.StatusForbidden, "需要管理员权限")
		})
	}
	if service.blacklistCalls != 0 || service.unblockCalls != 0 {
		t.Fatalf("service calls blacklist=%d unblock=%d", service.blacklistCalls, service.unblockCalls)
	}
}

func newManagementClientIPBlacklistRouter(
	service *managementClientIPPolicyHTTPServiceStub,
	defaultRole string,
) http.Handler {
	writeAuthMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := r.Header.Get("X-Test-Role")
			if role == "" {
				role = defaultRole
			}
			r = requestWithManagementAuthContext(r, managementauth.Context{
				SystemAccountID: "sys_" + role,
				Role:            role,
			})
			next.ServeHTTP(w, r)
		})
	}
	return NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: writeAuthMiddleware,
		ManagementClientIPBlacklistHandler: newManagementClientIPPolicyHandler(
			service,
			managementClientIPPolicyActionBlacklist,
			managementOperationLogOptions{},
		),
		ManagementClientIPUnblockHandler: newManagementClientIPPolicyHandler(
			service,
			managementClientIPPolicyActionUnblock,
			managementOperationLogOptions{},
		),
	})
}

func assertClientIPBlacklistLogCommon(
	t *testing.T,
	logInput port.OperationLogInput,
	action string,
	summary string,
	wantChanges int,
) {
	t.Helper()
	if logInput.Module != "client_ip_stats" ||
		logInput.Action != action ||
		logInput.OperationKey != "client_ip_stats."+action ||
		logInput.ResourceType != "client_ip" ||
		logInput.ResourceID != managementClientIPPolicyTestHash ||
		logInput.ResourceName != managementClientIPPolicyTestHash[:12] ||
		logInput.Summary != summary ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "admin_only" ||
		logInput.OperationScopeSystemAccountID != "" ||
		logInput.Mode != "admin" ||
		logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK ||
		len(logInput.Changes) != wantChanges {
		t.Fatalf("operation log=%+v", logInput)
	}
}

func assertClientIPPolicyIntPointer(t *testing.T, name string, got *int, want *int) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("%s=%v want=%v", name, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s=%d want=%d", name, *got, *want)
	}
}

func clientIPPolicyInt(value int) *int {
	return &value
}
