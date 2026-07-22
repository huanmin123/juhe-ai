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

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementresponseinspectionpolicies"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementResponseInspectionPoliciesHandlerCRUDAndBoundedOperationLogs(t *testing.T) {
	policy := responseInspectionPolicyFixture()
	service := &managementResponseInspectionPolicyServiceStub{
		listResult:   managementresponseinspectionpolicies.ListResult{Policies: []port.ResponseInspectionPolicy{policy}},
		createResult: policy,
		updateResult: policy,
		deleteResult: policy,
	}
	queue := &operationLogQueueStub{}
	handler := newManagementResponseInspectionPoliciesHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			NewLogID: func() string { return "oplog_response_policy" },
			Now:      func() time.Time { return time.Date(2026, 7, 22, 10, 11, 12, 0, time.UTC) },
		}),
	)

	get := responseInspectionPolicyRequest(http.MethodGet, "/__aisys__/api/response-inspection-policies", "", "")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK || service.listCalls != 1 || !strings.Contains(getRec.Body.String(), `"policies"`) {
		t.Fatalf("GET status=%d calls=%d body=%s", getRec.Code, service.listCalls, getRec.Body.String())
	}

	secretMatcher := "sensitive-upstream-fragment-must-not-enter-operation-log"
	body := `{"name":" Policy ","enabled":true,"priority":77,"scopeType":"provider","protocolCode":"openai","providerCode":"gpt","match":{"clientProfiles":["codex"],"errorMessageIncludes":["` + secretMatcher + `"],"outputTextExcludes":["do-not-log"]},"action":"retry_next_account","notes":"notes must not enter operation log"}`
	create := responseInspectionPolicyRequest(http.MethodPost, "/__aisys__/api/response-inspection-policies", "", body)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusCreated || service.createCalls != 1 || queue.calls != 1 {
		t.Fatalf("POST status=%d calls=%d logs=%d body=%s", createRec.Code, service.createCalls, queue.calls, createRec.Body.String())
	}
	if service.createInput.ProviderCode == nil || *service.createInput.ProviderCode != "gpt" || service.createInput.Match.ErrorMessageIncludes[0] != secretMatcher {
		t.Fatalf("create input = %+v", service.createInput)
	}
	assertResponseInspectionPolicyOperationLog(t, queue.payload, "create", http.StatusCreated, secretMatcher)

	queue.payload = nil
	update := responseInspectionPolicyRequest(http.MethodPut, "/__aisys__/api/response-inspection-policies/rip-1", "rip-1", body)
	updateRec := httptest.NewRecorder()
	handler.ServeHTTP(updateRec, update)
	if updateRec.Code != http.StatusOK || service.updateCalls != 1 || service.updateID != "rip-1" || queue.calls != 2 {
		t.Fatalf("PUT status=%d calls=%d id=%q logs=%d body=%s", updateRec.Code, service.updateCalls, service.updateID, queue.calls, updateRec.Body.String())
	}
	assertResponseInspectionPolicyOperationLog(t, queue.payload, "update", http.StatusOK, secretMatcher)

	queue.payload = nil
	deleteReq := responseInspectionPolicyRequest(http.MethodDelete, "/__aisys__/api/response-inspection-policies/rip-1", "rip-1", "")
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || service.deleteCalls != 1 || queue.calls != 3 || !strings.Contains(deleteRec.Body.String(), `"deleted":true`) {
		t.Fatalf("DELETE status=%d calls=%d logs=%d body=%s", deleteRec.Code, service.deleteCalls, queue.calls, deleteRec.Body.String())
	}
	assertResponseInspectionPolicyOperationLog(t, queue.payload, "delete", http.StatusOK, secretMatcher)
}

func TestManagementResponseInspectionPoliciesHandlerStrictPayload(t *testing.T) {
	valid := `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: strings.TrimSuffix(valid, "}") + `,"unknown":true}`},
		{name: "trailing json", body: valid + ` {}`},
		{name: "array root", body: `[]`},
		{name: "invalid json", body: `{`},
		{name: "name null", body: `{"name":null,"scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`},
		{name: "enabled null", body: `{"name":"Policy","enabled":null,"scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`},
		{name: "priority null", body: `{"name":"Policy","priority":null,"scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`},
		{name: "match unknown", body: `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"unknown":["x"]},"action":"observe"}`},
		{name: "match list null", body: `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"clientProfiles":null,"errorCodes":["x"]},"action":"observe"}`},
		{name: "provider number", body: `{"name":"Policy","scopeType":"provider","protocolCode":"openai","providerCode":1,"match":{"errorCodes":["x"]},"action":"observe"}`},
		{name: "oversize", body: `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["` + strings.Repeat("x", int(responseInspectionPolicyMaxBodyBytes)) + `"]},"action":"observe"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementResponseInspectionPolicyServiceStub{createResult: responseInspectionPolicyFixture()}
			rec := httptest.NewRecorder()
			newManagementResponseInspectionPoliciesHandler(service, managementOperationLogOptions{}).ServeHTTP(
				rec,
				responseInspectionPolicyRequest(http.MethodPost, "/__aisys__/api/response-inspection-policies", "", test.body),
			)
			want := http.StatusBadRequest
			if test.name == "oversize" {
				want = http.StatusRequestEntityTooLarge
			}
			if rec.Code != want || service.createCalls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", rec.Code, want, service.createCalls, rec.Body.String())
			}
		})
	}
}

func TestManagementResponseInspectionPoliciesHandlerAuthAndErrors(t *testing.T) {
	validBody := `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`
	t.Run("missing auth", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newManagementResponseInspectionPoliciesHandler(&managementResponseInspectionPolicyServiceStub{}, managementOperationLogOptions{}).ServeHTTP(
			rec,
			httptest.NewRequest(http.MethodGet, "/__aisys__/api/response-inspection-policies", nil),
		)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("ordinary user", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/response-inspection-policies", nil)
		req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys-user", Role: "user"})
		newManagementResponseInspectionPoliciesHandler(&managementResponseInspectionPolicyServiceStub{}, managementOperationLogOptions{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "需要管理员权限") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "validation", err: &managementresponseinspectionpolicies.ValidationError{Message: "至少需要填写一个匹配条件"}, wantStatus: http.StatusBadRequest, wantText: "至少需要填写一个匹配条件"},
		{name: "not found", err: &managementresponseinspectionpolicies.NotFoundError{}, wantStatus: http.StatusNotFound, wantText: "响应检查策略不存在"},
		{name: "conflict", err: &managementresponseinspectionpolicies.ConflictError{Message: "响应检查策略写入冲突，请刷新后重试"}, wantStatus: http.StatusConflict, wantText: "写入冲突"},
		{name: "internal", err: errors.New("database password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementResponseInspectionPolicyServiceStub{createErr: test.err}
			rec := httptest.NewRecorder()
			newManagementResponseInspectionPoliciesHandler(service, managementOperationLogOptions{}).ServeHTTP(
				rec,
				responseInspectionPolicyRequest(http.MethodPost, "/__aisys__/api/response-inspection-policies", "", validBody),
			)
			if rec.Code != test.wantStatus || !strings.Contains(rec.Body.String(), test.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestManagementResponseInspectionPoliciesOperationLogFailureDoesNotOverrideCommittedMutation(t *testing.T) {
	queue := &operationLogQueueStub{err: errors.New("queue unavailable")}
	service := &managementResponseInspectionPolicyServiceStub{createResult: responseInspectionPolicyFixture()}
	handler := newManagementResponseInspectionPoliciesHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queue}),
	)
	body := `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, responseInspectionPolicyRequest(http.MethodPost, "/__aisys__/api/response-inspection-policies", "", body))
	if rec.Code != http.StatusCreated || service.createCalls != 1 || queue.calls != 1 {
		t.Fatalf("status=%d calls=%d logs=%d body=%s", rec.Code, service.createCalls, queue.calls, rec.Body.String())
	}
}

func responseInspectionPolicyRequest(method, target, id, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if id != "" {
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	}
	return requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys-admin", Username: "admin", DisplayName: "管理员", Role: "admin",
	})
}

func assertResponseInspectionPolicyOperationLog(t *testing.T, payload []byte, action string, status int, forbidden string) {
	t.Helper()
	input, err := operationlogjob.DecodeWriteTaskPayload(payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if input.Mode != "admin" || input.Module != "response_inspection_policies" || input.Action != action || input.OperationKey != "response_inspection_policies."+action ||
		input.ResourceType != "response_inspection_policy" || input.VisibilityScope != "admin_only" || input.StatusCode == nil || *input.StatusCode != status {
		t.Fatalf("operation log = %#v", input)
	}
	encoded := string(payload)
	for _, value := range []string{forbidden, "do-not-log", "notes must not enter operation log"} {
		if strings.Contains(encoded, value) {
			t.Fatalf("operation log leaked %q: %s", value, encoded)
		}
	}
	if action != "delete" {
		foundSummary := false
		for _, change := range input.Changes {
			if change.Field == "matchSummary" {
				foundSummary = true
				raw, _ := json.Marshal(change.After)
				if len(raw) > 512 {
					t.Fatalf("match summary too large: %d bytes", len(raw))
				}
			}
		}
		if !foundSummary {
			t.Fatalf("matchSummary missing from %#v", input.Changes)
		}
	}
}

func responseInspectionPolicyFixture() port.ResponseInspectionPolicy {
	notes := "stored notes"
	provider := "gpt"
	return port.ResponseInspectionPolicy{
		ID: "rip-1", DefaultRule: false, Editable: true, Name: "Policy", Enabled: true,
		Priority: 77, ScopeType: "provider", ProtocolCode: "openai", ProviderCode: &provider,
		Match:  port.ResponseInspectionPolicyMatch{ClientProfiles: []string{"codex"}, ErrorMessageIncludes: []string{"sensitive-upstream-fragment-must-not-enter-operation-log"}},
		Action: "retry_next_account", Notes: &notes, CreatedAt: "2026-07-22T10:00:00.000Z", UpdatedAt: "2026-07-22T10:00:00.000Z",
	}
}

type managementResponseInspectionPolicyServiceStub struct {
	listCalls    int
	createCalls  int
	updateCalls  int
	deleteCalls  int
	createInput  managementresponseinspectionpolicies.Input
	updateInput  managementresponseinspectionpolicies.Input
	updateID     string
	deleteID     string
	listResult   managementresponseinspectionpolicies.ListResult
	createResult port.ResponseInspectionPolicy
	updateResult port.ResponseInspectionPolicy
	deleteResult port.ResponseInspectionPolicy
	listErr      error
	createErr    error
	updateErr    error
	deleteErr    error
}

func (s *managementResponseInspectionPolicyServiceStub) List(context.Context) (managementresponseinspectionpolicies.ListResult, error) {
	s.listCalls++
	return s.listResult, s.listErr
}

func (s *managementResponseInspectionPolicyServiceStub) Create(_ context.Context, input managementresponseinspectionpolicies.Input) (port.ResponseInspectionPolicy, error) {
	s.createCalls++
	s.createInput = input
	return s.createResult, s.createErr
}

func (s *managementResponseInspectionPolicyServiceStub) Update(_ context.Context, id string, input managementresponseinspectionpolicies.Input) (port.ResponseInspectionPolicy, error) {
	s.updateCalls++
	s.updateID = id
	s.updateInput = input
	return s.updateResult, s.updateErr
}

func (s *managementResponseInspectionPolicyServiceStub) Delete(_ context.Context, id string) (port.ResponseInspectionPolicy, error) {
	s.deleteCalls++
	s.deleteID = id
	return s.deleteResult, s.deleteErr
}
