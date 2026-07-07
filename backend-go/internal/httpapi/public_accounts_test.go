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

	"juhe-ai/backend-go/internal/modules/publicaccounts"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
)

func TestPublicAccountHandlersAddThroughShellRedactsLogSecrets(t *testing.T) {
	secret := "sk-live-public-account-secret-0123456789abcdef0123456789abcdef"
	service := &publicAccountServiceStub{
		addResponse: publicaccounts.AccountResponse{
			Source:      "stats",
			GeneratedAt: "2026-07-07T10:00:00Z",
			Action:      "created",
			Target:      publicaccounts.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin", GroupID: "grp_1", GroupName: "公开分组"},
			Account: &publicaccounts.AccountSummary{
				ID:                        "acct_1",
				Name:                      "公开账号",
				ProviderCode:              "gpt",
				ProviderProtocolProfileID: "profile_gpt_openai_v1",
				ProtocolCode:              "openai",
				ProtocolVersion:           "v1",
				Type:                      publicaccounts.AccountTypeAPIKey,
				ClientCompatibility:       publicaccounts.DefaultClientCompat,
				Status:                    publicaccounts.StatusPendingTest,
				SupportedModels:           []string{"gpt-5.5"},
				BoundGroupID:              "grp_1",
				BoundGroupName:            "公开分组",
				Schedulable:               false,
			},
		},
	}
	authenticator := newPublicGroupAPIAuthStub()
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, newPublicAccountHandlers(service), time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/add", strings.NewReader(`{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://user:password@api.openai.com/v1","apiKey":"`+secret+`","supportedModels":["gpt-5.5"]}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.scope != publicapi.ScopeAccountAddWrite || limiter.calls != 1 {
		t.Fatalf("auth scope/limiter = %q/%d", authenticator.scope, limiter.calls)
	}
	if service.addCalls != 1 || service.addInput.TargetUsername != "admin" || service.addInput.ProviderProtocolProfileID != "profile_gpt_openai_v1" || service.addInput.APIKey != secret {
		t.Fatalf("add input = calls %d %+v", service.addCalls, service.addInput)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	account := body["data"].(map[string]any)["account"].(map[string]any)
	if _, ok := account["apiKey"]; ok {
		t.Fatalf("response leaked apiKey: %#v", account)
	}
	if _, ok := account["baseUrl"]; ok {
		t.Fatalf("response leaked baseUrl: %#v", account)
	}

	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusCreated || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
	requestBody := log.RequestData["body"].(map[string]any)
	if requestBody["apiKey"] != "[redacted]" {
		t.Fatalf("logged apiKey = %#v, want redacted", requestBody["apiKey"])
	}
	if requestBody["baseUrl"] != "[redacted]" {
		t.Fatalf("logged baseUrl = %#v, want redacted", requestBody["baseUrl"])
	}
	if strings.Contains(fmt.Sprint(log.RequestData), secret) || strings.Contains(fmt.Sprint(log.ResponseData), secret) {
		t.Fatalf("public api log leaked upstream secret: request=%#v response=%#v", log.RequestData, log.ResponseData)
	}
}

func TestPublicAccountHandlersRejectStrictAndNonCoercedFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add unknown credentials field", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","credentials":{"apiKey":"sk-test"}}`},
		{name: "add unsupported type", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"oauth","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`},
		{name: "add string concurrency", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","concurrencyLimit":"20"}`},
		{name: "update empty mutable", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1"}`},
		{name: "delete unknown field", path: "/__aipublic__/account/del", body: `{"accountId":"acct_1","extra":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicAccountServiceStub{}
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicAccountHandlers(service),
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

func TestPublicAccountHandlersCoerceListPaginationAndFilters(t *testing.T) {
	service := &publicAccountServiceStub{listResponse: publicaccounts.AccountListResponse{
		Source:         "stats",
		GeneratedAt:    "2026-07-07T10:00:00Z",
		Target:         publicaccounts.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
		Page:           2,
		PageSize:       10,
		PageUpperBound: 11,
		Items:          []publicaccounts.AccountSummary{},
	}}
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicAccountHandlers(service),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/account/list?targetUsername=admin&targetGroupName=%E5%85%AC%E5%BC%80%E5%88%86%E7%BB%84&providerCode=gpt&providerProtocolProfileId=profile_gpt_openai_v1&keyword=%E5%85%AC%E5%BC%80&type=api_key&status=all&schedulable=enabled&page=2&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.listInput.Page != 2 || service.listInput.PageSize != 10 ||
		service.listInput.TargetGroupName != "公开分组" || service.listInput.ProviderCode != "gpt" ||
		service.listInput.ProviderProtocolProfileID != "profile_gpt_openai_v1" ||
		service.listInput.Keyword != "公开" || service.listInput.Type != publicaccounts.AccountTypeAPIKey ||
		service.listInput.Status != "all" || service.listInput.Schedulable != "enabled" {
		t.Fatalf("list input = %+v", service.listInput)
	}
}

func TestPublicAccountHandlersMapServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		configure  func(*publicAccountServiceStub)
		wantStatus int
		wantMsg    string
	}{
		{
			name: "update not found", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","name":"公开账号"}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.updateErr = publicaccounts.ErrAccountNotFound
			},
			wantStatus: http.StatusNotFound, wantMsg: "账号不存在",
		},
		{
			name: "duplicate", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","name":"公开账号"}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.updateErr = fmt.Errorf("%w: 公开账号", publicaccounts.ErrDuplicateAccountName)
			},
			wantStatus: http.StatusConflict, wantMsg: "账号已存在：公开账号",
		},
		{
			name: "add target not found is bad request", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.addErr = fmt.Errorf("%w: admin", publicaccounts.ErrTargetNotFound)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "list target not found is 404", path: "/__aipublic__/account/list?targetUsername=admin", body: ``,
			configure: func(stub *publicAccountServiceStub) {
				stub.listErr = fmt.Errorf("%w: admin", publicaccounts.ErrTargetNotFound)
			},
			wantStatus: http.StatusNotFound, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "provider disabled", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.addErr = fmt.Errorf("%w: gpt", publicaccounts.ErrProviderDisabled)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "供应商已停用：gpt",
		},
		{
			name: "invalid status transition", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","status":"active"}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.updateErr = fmt.Errorf("%w: pending_test -> active", publicaccounts.ErrInvalidStatusTransition)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "pending_test -> active",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicAccountServiceStub{}
			tt.configure(service)
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicAccountHandlers(service),
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

func TestPublicAccountHandlersTestTokenSkipsService(t *testing.T) {
	authContext := publicAPIShellAuthContext()
	authContext.IsTestToken = true
	service := &publicAccountServiceStub{addErr: errors.New("should not call service")}
	router := newTestPublicAPIShell(
		&publicAPIShellAuthStub{ctx: authContext},
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicAccountHandlers(service),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/add", strings.NewReader(`{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`))
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
	if strings.Contains(rec.Body.String(), "sk-test") || strings.Contains(rec.Body.String(), "baseUrl") {
		t.Fatalf("mock response leaked credentials: %s", rec.Body.String())
	}
}

type publicAccountServiceStub struct {
	listInput   publicaccounts.ListInput
	addInput    publicaccounts.AddInput
	updateInput publicaccounts.UpdateInput
	deleteInput publicaccounts.DeleteInput

	listCalls   int
	addCalls    int
	updateCalls int
	deleteCalls int

	listResponse   publicaccounts.AccountListResponse
	addResponse    publicaccounts.AccountResponse
	updateResponse publicaccounts.AccountResponse
	deleteResponse publicaccounts.AccountResponse

	listErr   error
	addErr    error
	updateErr error
	deleteErr error
}

func (s *publicAccountServiceStub) List(_ *http.Request, input publicaccounts.ListInput) (publicaccounts.AccountListResponse, error) {
	s.listCalls++
	s.listInput = input
	return s.listResponse, s.listErr
}

func (s *publicAccountServiceStub) Add(_ *http.Request, input publicaccounts.AddInput) (publicaccounts.AccountResponse, error) {
	s.addCalls++
	s.addInput = input
	return s.addResponse, s.addErr
}

func (s *publicAccountServiceStub) Update(_ *http.Request, input publicaccounts.UpdateInput) (publicaccounts.AccountResponse, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResponse, s.updateErr
}

func (s *publicAccountServiceStub) Delete(_ *http.Request, input publicaccounts.DeleteInput) (publicaccounts.AccountResponse, error) {
	s.deleteCalls++
	s.deleteInput = input
	return s.deleteResponse, s.deleteErr
}
