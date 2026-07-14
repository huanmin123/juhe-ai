package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
				HealthCheckEndpointFamily: "responses",
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
	assertPublicAccountStringListValue(t, service.addInput.SupportedModels, true, []string{"gpt-5.5"})

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
	if _, ok := account["healthCheckModel"]; ok {
		t.Fatalf("response exposed internal healthCheckModel: %#v", account)
	}
	if _, ok := account["healthCheckEndpointFamily"]; ok {
		t.Fatalf("response exposed internal healthCheckEndpointFamily: %#v", account)
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

func TestParsePublicAccountAddBodyPreservesSupportedModelsPresence(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantSet    bool
		wantModels []string
	}{
		{
			name:       "omitted",
			body:       `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`,
			wantSet:    false,
			wantModels: nil,
		},
		{
			name:       "explicit empty array",
			body:       `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":[]}`,
			wantSet:    true,
			wantModels: nil,
		},
		{
			name:       "non-empty array",
			body:       `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":[" gpt-5.5 ","gpt-5.5-mini"]}`,
			wantSet:    true,
			wantModels: []string{"gpt-5.5", "gpt-5.5-mini"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newPublicAccountParserRequest(t, tt.body)
			input, err := parsePublicAccountAddBody(req)
			if err != nil {
				t.Fatalf("parse add body: %v", err)
			}
			assertPublicAccountStringListValue(t, input.SupportedModels, tt.wantSet, tt.wantModels)
		})
	}
}

func TestParsePublicAccountUpdateBodyPreservesSupportedModelsPresence(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantSet    bool
		wantModels []string
	}{
		{
			name:       "omitted",
			body:       `{"accountId":"acct_1","name":"公开账号更新"}`,
			wantSet:    false,
			wantModels: nil,
		},
		{
			name:       "explicit empty array",
			body:       `{"accountId":"acct_1","supportedModels":[]}`,
			wantSet:    true,
			wantModels: nil,
		},
		{
			name:       "non-empty array",
			body:       `{"accountId":"acct_1","supportedModels":[" gpt-5.5 ","gpt-5.5-mini"]}`,
			wantSet:    true,
			wantModels: []string{"gpt-5.5", "gpt-5.5-mini"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newPublicAccountParserRequest(t, tt.body)
			input, err := parsePublicAccountUpdateBody(req)
			if err != nil {
				t.Fatalf("parse update body: %v", err)
			}
			assertPublicAccountStringListValue(t, input.SupportedModels, tt.wantSet, tt.wantModels)
		})
	}
}

func TestPublicAccountHandlersUpdateTypeIsValidationOnly(t *testing.T) {
	t.Run("type without mutable field", func(t *testing.T) {
		service := &publicAccountServiceStub{}
		router := newTestPublicAPIShell(
			newPublicGroupAPIAuthStub(),
			&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
			&publicAPIShellLogQueueStub{},
			newPublicAccountHandlers(service),
			time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		)

		req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/update", strings.NewReader(`{"accountId":"acct_1","type":"api_key"}`))
		req.Header.Set("Authorization", "Bearer juis_plain")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assertPublicGroupMessageError(t, rec, http.StatusBadRequest, "账号修改至少提供一个要修改的字段")
		if service.updateCalls != 0 {
			t.Fatalf("update calls = %d, want 0", service.updateCalls)
		}
	})

	t.Run("type with mutable field", func(t *testing.T) {
		service := &publicAccountServiceStub{}
		router := newTestPublicAPIShell(
			newPublicGroupAPIAuthStub(),
			&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
			&publicAPIShellLogQueueStub{},
			newPublicAccountHandlers(service),
			time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		)

		req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/update", strings.NewReader(`{"accountId":"acct_1","type":"api_key","name":"公开账号更新"}`))
		req.Header.Set("Authorization", "Bearer juis_plain")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if service.updateCalls != 1 {
			t.Fatalf("update calls = %d, want 1", service.updateCalls)
		}
		if service.updateInput.AccountID != "acct_1" || service.updateInput.Type == nil || *service.updateInput.Type != publicaccounts.AccountTypeAPIKey ||
			service.updateInput.Name == nil || *service.updateInput.Name != "公开账号更新" {
			t.Fatalf("update input = %+v", service.updateInput)
		}
	})

	t.Run("unsupported type with mutable field", func(t *testing.T) {
		service := &publicAccountServiceStub{}
		router := newTestPublicAPIShell(
			newPublicGroupAPIAuthStub(),
			&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
			&publicAPIShellLogQueueStub{},
			newPublicAccountHandlers(service),
			time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		)

		req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/update", strings.NewReader(`{"accountId":"acct_1","type":"oauth","name":"公开账号更新"}`))
		req.Header.Set("Authorization", "Bearer juis_plain")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
		}
		if service.updateCalls != 0 {
			t.Fatalf("update calls = %d, want 0", service.updateCalls)
		}
	})
}

func TestParsePublicAccountBodiesRejectInternalHealthCheckFields(t *testing.T) {
	tests := []struct {
		name      string
		parse     func(*http.Request) error
		body      string
		fieldName string
	}{
		{
			name: "add health check model",
			parse: func(req *http.Request) error {
				_, err := parsePublicAccountAddBody(req)
				return err
			},
			body:      `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","healthCheckModel":"gpt-5.5"}`,
			fieldName: "healthCheckModel",
		},
		{
			name: "update health check model",
			parse: func(req *http.Request) error {
				_, err := parsePublicAccountUpdateBody(req)
				return err
			},
			body:      `{"accountId":"acct_1","healthCheckModel":"gpt-5.5"}`,
			fieldName: "healthCheckModel",
		},
		{
			name: "add health check endpoint family",
			parse: func(req *http.Request) error {
				_, err := parsePublicAccountAddBody(req)
				return err
			},
			body:      `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","healthCheckEndpointFamily":"responses"}`,
			fieldName: "healthCheckEndpointFamily",
		},
		{
			name: "update health check endpoint family",
			parse: func(req *http.Request) error {
				_, err := parsePublicAccountUpdateBody(req)
				return err
			},
			body:      `{"accountId":"acct_1","healthCheckEndpointFamily":"responses"}`,
			fieldName: "healthCheckEndpointFamily",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse(newPublicAccountParserRequest(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), "请求体包含未知字段："+tt.fieldName) {
				t.Fatalf("parse error = %v, want unknown %s field", err, tt.fieldName)
			}
		})
	}
}

func TestPublicAccountHandlersAddPreservesSupportedModelsPresence(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSet bool
	}{
		{
			name:    "omitted",
			body:    `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`,
			wantSet: false,
		},
		{
			name:    "explicit empty array",
			body:    `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":[]}`,
			wantSet: true,
		},
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

			req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/add", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if service.addCalls != 1 {
				t.Fatalf("add calls = %d, want 1", service.addCalls)
			}
			assertPublicAccountStringListValue(t, service.addInput.SupportedModels, tt.wantSet, nil)
		})
	}
}

func TestPublicAccountHandlersRejectNullNonNullableOptionalFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add target display name", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetDisplayName":null,"targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`},
		{name: "add supported models", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":null}`},
		{name: "add status", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","status":null}`},
		{name: "add concurrency limit", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","concurrencyLimit":null}`},
		{name: "add priority", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","priority":null}`},
		{name: "add notes", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","notes":null}`},
		{name: "update target username", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","targetUsername":null,"name":"公开账号"}`},
		{name: "update target group name", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","targetGroupName":null,"name":"公开账号"}`},
		{name: "update provider code", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","providerCode":null,"name":"公开账号"}`},
		{name: "update provider profile", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","providerProtocolProfileId":null,"name":"公开账号"}`},
		{name: "update name", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","name":null}`},
		{name: "update type", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","type":null,"name":"公开账号"}`},
		{name: "update base url", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","baseUrl":null}`},
		{name: "update api key", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","apiKey":null}`},
		{name: "update supported models", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","supportedModels":null}`},
		{name: "update status", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","status":null}`},
		{name: "update concurrency limit", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","concurrencyLimit":null}`},
		{name: "update priority", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","priority":null}`},
		{name: "update notes", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","notes":null}`},
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
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.addCalls != 0 || service.updateCalls != 0 {
				t.Fatalf("service calls = add %d update %d, want 0", service.addCalls, service.updateCalls)
			}
		})
	}
}

func TestPublicAccountHandlersAcceptNullableScheduleAndEmptyNotes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		assert     func(*testing.T, *publicAccountServiceStub)
	}{
		{
			name:       "add empty notes",
			path:       "/__aipublic__/account/add",
			body:       `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","notes":"   "}`,
			wantStatus: http.StatusCreated,
			assert: func(t *testing.T, service *publicAccountServiceStub) {
				t.Helper()
				if service.addCalls != 1 || service.updateCalls != 0 || service.addInput.Notes == nil || *service.addInput.Notes != "" {
					t.Fatalf("service calls/input = add %d update %d notes %#v", service.addCalls, service.updateCalls, service.addInput.Notes)
				}
			},
		},
		{
			name:       "add null availability schedule",
			path:       "/__aipublic__/account/add",
			body:       `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","availabilitySchedule":null}`,
			wantStatus: http.StatusCreated,
			assert: func(t *testing.T, service *publicAccountServiceStub) {
				t.Helper()
				if service.addCalls != 1 || service.updateCalls != 0 || !service.addInput.AvailabilitySchedule.Set() || service.addInput.AvailabilitySchedule.Value() != nil {
					t.Fatalf("service calls/schedule = add %d update %d set %v value %#v", service.addCalls, service.updateCalls, service.addInput.AvailabilitySchedule.Set(), service.addInput.AvailabilitySchedule.Value())
				}
			},
		},
		{
			name:       "update empty notes",
			path:       "/__aipublic__/account/update",
			body:       `{"accountId":"acct_1","notes":"   "}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, service *publicAccountServiceStub) {
				t.Helper()
				notes := service.updateInput.Notes.Value()
				if service.addCalls != 0 || service.updateCalls != 1 || !service.updateInput.Notes.Set() || notes == nil || *notes != "" {
					t.Fatalf("service calls/notes = add %d update %d set %v value %#v", service.addCalls, service.updateCalls, service.updateInput.Notes.Set(), notes)
				}
			},
		},
		{
			name:       "update null availability schedule",
			path:       "/__aipublic__/account/update",
			body:       `{"accountId":"acct_1","availabilitySchedule":null}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, service *publicAccountServiceStub) {
				t.Helper()
				if service.addCalls != 0 || service.updateCalls != 1 || !service.updateInput.AvailabilitySchedule.Set() || service.updateInput.AvailabilitySchedule.Value() != nil {
					t.Fatalf("service calls/schedule = add %d update %d set %v value %#v", service.addCalls, service.updateCalls, service.updateInput.AvailabilitySchedule.Set(), service.updateInput.AvailabilitySchedule.Value())
				}
			},
		},
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

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			tt.assert(t, service)
		})
	}
}

func TestPublicAccountHandlersAddReturnsSupportedModelsRequiredMessage(t *testing.T) {
	service := &publicAccountServiceStub{
		addErr: fmt.Errorf(
			"%w: 账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型",
			publicaccounts.ErrInvalidSupportedModels,
		),
	}
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicAccountHandlers(service),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/__aipublic__/account/add",
		strings.NewReader(`{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":[]}`),
	)
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	const want = "{\"message\":\"账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestPublicAccountHandlersReturnUnsupportedSupportedModelsMessage(t *testing.T) {
	const (
		wantMessage    = "账户支持模型不在供应商模型目录中：gpt-missing"
		wantBody       = "{\"message\":\"" + wantMessage + "\"}\n"
		internalPrefix = "public account invalid supported models"
	)
	serviceErr := fmt.Errorf("%w: %s", publicaccounts.ErrInvalidSupportedModels, wantMessage)
	tests := []struct {
		name      string
		path      string
		body      string
		configure func(*publicAccountServiceStub)
	}{
		{
			name: "add",
			path: "/__aipublic__/account/add",
			body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","supportedModels":["gpt-missing"]}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.addErr = serviceErr
			},
		},
		{
			name: "update",
			path: "/__aipublic__/account/update",
			body: `{"accountId":"acct_1","supportedModels":["gpt-missing"]}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.updateErr = serviceErr
			},
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

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != wantBody {
				t.Fatalf("body = %q, want %q", got, wantBody)
			}
			if strings.Contains(rec.Body.String(), internalPrefix) {
				t.Fatalf("body leaked internal error prefix: %q", rec.Body.String())
			}
		})
	}
}

func TestPublicAccountHandlersRejectStrictAndNonCoercedFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add unknown credentials field", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","credentials":{"apiKey":"sk-test"}}`},
		{name: "add health check endpoint family", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","healthCheckEndpointFamily":"responses"}`},
		{name: "add unsupported type", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"oauth","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test"}`},
		{name: "add string concurrency", path: "/__aipublic__/account/add", body: `{"targetUsername":"admin","targetGroupName":"公开分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"sk-test","concurrencyLimit":"20"}`},
		{name: "update empty mutable", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1"}`},
		{name: "update health check endpoint family", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","healthCheckEndpointFamily":"responses"}`},
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
		{
			name: "health check model removed", path: "/__aipublic__/account/update", body: `{"accountId":"acct_1","supportedModels":["gpt-5.5"]}`,
			configure: func(stub *publicAccountServiceStub) {
				stub.updateErr = fmt.Errorf("%w: 账户检查模型必须属于账户支持模型", publicaccounts.ErrInvalidHealthCheckModel)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "账户检查模型必须属于账户支持模型",
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
	if strings.Contains(rec.Body.String(), "sk-test") ||
		strings.Contains(rec.Body.String(), "baseUrl") ||
		strings.Contains(rec.Body.String(), "healthCheckModel") {
		t.Fatalf("mock response leaked credentials or internal health model: %s", rec.Body.String())
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

func assertPublicAccountStringListValue(t *testing.T, value publicaccounts.StringListValue, wantSet bool, want []string) {
	t.Helper()
	if value.Set() != wantSet {
		t.Fatalf("supportedModels Set() = %v, want %v", value.Set(), wantSet)
	}
	if got := value.Value(); !slices.Equal(got, want) {
		t.Fatalf("supportedModels Value() = %#v, want %#v", got, want)
	}
}

func newPublicAccountParserRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	var requestBody map[string]any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		t.Fatalf("decode parser request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/add", nil)
	return req.WithContext(context.WithValue(req.Context(), publicAPIRequestBodyKey, requestBody))
}
