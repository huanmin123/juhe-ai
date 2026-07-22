package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementResponseInspectionPoliciesPath = "/__aisys__/api/response-inspection-policies"

func TestManagementResponseInspectionPolicyMutationGuardsHashFullPayloadAndRestoreBody(t *testing.T) {
	body := `{"name":"Policy","scopeType":"provider","protocolCode":"openai","providerCode":"gpt","match":{"errorMessageIncludes":["sensitive-fragment"]},"action":"retry_next_account"}`
	tests := []struct {
		name      string
		config    mutationGuardConfig
		method    string
		id        string
		operation string
	}{
		{name: "create", config: managementResponseInspectionPolicyCreateMutationGuardConfig(), method: http.MethodPost, operation: "response_inspection_policies.create"},
		{name: "update", config: managementResponseInspectionPolicyUpdateMutationGuardConfig(), method: http.MethodPut, id: "rip-1", operation: "response_inspection_policies.update"},
		{name: "delete", config: managementResponseInspectionPolicyDeleteMutationGuardConfig(), method: http.MethodDelete, id: "rip-1", operation: "response_inspection_policies.delete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, managementResponseInspectionPoliciesPath, strings.NewReader(body))
			if test.id != "" {
				req = responseInspectionPolicyRequest(test.method, managementResponseInspectionPoliciesPath+"/"+test.id, test.id, body)
			}
			fingerprint, err := test.config.fingerprint(httptest.NewRecorder(), req)
			if err != nil || test.config.operationKey != test.operation {
				t.Fatalf("config=%+v fingerprint=%#v error=%v", test.config, fingerprint, err)
			}
			encoded := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(toJSON(fingerprint)), "\\u003c", "<"), "\\u003e", ">"))
			if strings.Contains(encoded, "sensitive-fragment") {
				t.Fatalf("fingerprint leaked matcher: %s", encoded)
			}
			if test.method != http.MethodDelete {
				downstream, readErr := io.ReadAll(req.Body)
				if readErr != nil || string(downstream) != body {
					t.Fatalf("restored body=%q error=%v", downstream, readErr)
				}
			}
		})
	}
}

func TestRouterRegistersManagementResponseInspectionPolicyCRUDWithMutationGuards(t *testing.T) {
	readAuthCalls := 0
	writeAuthCalls := 0
	handlerCalls := map[string]int{}
	opts := RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readAuthCalls++
				r = requestWithManagementAuthContext(r, managementauth.Context{SystemAccountID: "sys-admin", Role: "admin"})
				next.ServeHTTP(w, r)
			})
		},
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeAuthCalls++
				r = requestWithManagementAuthContext(r, managementauth.Context{SystemAccountID: "sys-admin", Role: "admin"})
				next.ServeHTTP(w, r)
			})
		},
		ManagementResponseInspectionPoliciesHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalls[r.Method]++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	router := NewRouter(opts)
	validBody := `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`

	requests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: managementResponseInspectionPoliciesPath},
		{method: http.MethodPost, path: managementResponseInspectionPoliciesPath, body: validBody},
		{method: http.MethodPut, path: managementResponseInspectionPoliciesPath + "/rip-1", body: validBody},
		{method: http.MethodDelete, path: managementResponseInspectionPoliciesPath + "/rip-1"},
	}
	for _, request := range requests {
		for attempt, want := range []int{http.StatusNoContent, http.StatusConflict} {
			if request.method == http.MethodGet && attempt == 1 {
				break
			}
			var body io.Reader
			if request.body != "" {
				body = strings.NewReader(request.body)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, body))
			if rec.Code != want {
				t.Fatalf("%s attempt=%d status=%d want=%d body=%s", request.method, attempt+1, rec.Code, want, rec.Body.String())
			}
		}
	}
	if readAuthCalls != 1 || writeAuthCalls != 6 {
		t.Fatalf("read auth=%d write auth=%d", readAuthCalls, writeAuthCalls)
	}
	if handlerCalls[http.MethodGet] != 1 || handlerCalls[http.MethodPost] != 1 || handlerCalls[http.MethodPut] != 1 || handlerCalls[http.MethodDelete] != 1 {
		t.Fatalf("handler calls=%#v", handlerCalls)
	}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("response inspection policy CRUD must be classified as management business/write routes")
	}
}

func TestRouterResponseInspectionPolicyAdminBoundaryRunsBeforeMutationGuard(t *testing.T) {
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = requestWithManagementAuthContext(r, managementauth.Context{SystemAccountID: "sys-user", Role: "user"})
				next.ServeHTTP(w, r)
			})
		},
		ManagementResponseInspectionPoliciesHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalls++ }),
	})
	body := `{"name":"Policy","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`
	for attempt := 0; attempt < 2; attempt++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, managementResponseInspectionPoliciesPath, strings.NewReader(body)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt=%d status=%d body=%s", attempt+1, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls=%d", handlerCalls)
	}
}

func TestRouterDoesNotRegisterManagementResponseInspectionPoliciesWhenDisabledOrHandlerMissing(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tests := []RouterOptions{
		{Config: config.Config{Host: "127.0.0.1", Port: 3000}, ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next }, ManagementResponseInspectionPoliciesHandler: handler},
		{Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true}, ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler { return next }, ManagementCaptchaHandler: handler},
	}
	for index, opts := range tests {
		rec := httptest.NewRecorder()
		NewRouter(opts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, managementResponseInspectionPoliciesPath, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("case=%d status=%d body=%s", index, rec.Code, rec.Body.String())
		}
	}
}

func toJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
