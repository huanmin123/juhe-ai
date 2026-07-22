package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

type pageDataChangeConfirmServiceStub struct {
	input  PageDataChangeConfirmInput
	result redisplatform.PageDataConfirmResult
	err    error
	calls  int
}

func (s *pageDataChangeConfirmServiceStub) Confirm(_ context.Context, input PageDataChangeConfirmInput) (redisplatform.PageDataConfirmResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func TestPageDataChangeConfirmHandlerAllowsAuthenticatedSelfAndReturnsData(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{result: redisplatform.PageDataConfirmResult{
		ServerTime: "2026-07-22T00:00:00.000Z",
		Domains: map[string]redisplatform.PageDataConfirmDomainResult{
			"accounts.runtime": {Action: redisplatform.PageDataConfirmActionReload},
		},
	}}
	rec := servePageDataChangeConfirm(t, NewPageDataChangeConfirmHandler(service), managementauth.Context{
		SystemAccountID: "sys-user", Role: "user",
	}, `{"viewScope":"self","domains":{"accounts.runtime":null}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 || service.input.ViewerSystemAccountID != "sys-user" || service.input.ViewScope != redisplatform.PageDataViewScopeSelf || service.input.TargetSystemAccountID != "" {
		t.Fatalf("confirm input = %#v, calls = %d", service.input, service.calls)
	}
	if token, ok := service.input.Domains["accounts.runtime"]; !ok || token != nil {
		t.Fatalf("domains = %#v", service.input.Domains)
	}
	var payload struct {
		Data redisplatform.PageDataConfirmResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload.Data.ServerTime != service.result.ServerTime {
		t.Fatalf("response = %s, err = %v", rec.Body.String(), err)
	}
}

func TestPageDataChangeConfirmHandlerEnforcesViewScopeAuthorization(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{}
	tests := []struct {
		name   string
		auth   managementauth.Context
		body   string
		status int
	}{
		{name: "self target", auth: managementauth.Context{SystemAccountID: "sys-user", Role: "user"}, body: `{"viewScope":"self","targetSystemAccountId":"sys-other","domains":{}}`, status: http.StatusBadRequest},
		{name: "user admin", auth: managementauth.Context{SystemAccountID: "sys-user", Role: "user"}, body: `{"viewScope":"admin","targetSystemAccountId":"sys-other","domains":{}}`, status: http.StatusForbidden},
		{name: "admin target", auth: managementauth.Context{SystemAccountID: "sys-admin", Role: "admin"}, body: `{"viewScope":"admin","targetSystemAccountId":" sys-other ","domains":{}}`, status: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service.calls = 0
			rec := servePageDataChangeConfirm(t, NewPageDataChangeConfirmHandler(service), test.auth, test.body)
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, test.status, rec.Body.String())
			}
			if test.status == http.StatusOK && (service.input.TargetSystemAccountID != "sys-other" || service.input.ViewScope != redisplatform.PageDataViewScopeAdmin) {
				t.Fatalf("confirm input = %#v", service.input)
			}
		})
	}
}

func TestPageDataChangeConfirmHandlerRejectsStrictInvalidBodies(t *testing.T) {
	tooMany := make([]string, 0, redisplatform.PageDataMaxConfirmDomains+1)
	for index := 0; index <= redisplatform.PageDataMaxConfirmDomains; index++ {
		tooMany = append(tooMany, fmt.Sprintf(`"accounts.runtime.%d":null`, index))
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown top level field", body: `{"viewScope":"self","domains":{},"extra":true}`},
		{name: "trailing json", body: `{"viewScope":"self","domains":{}} {}`},
		{name: "invalid view scope", body: `{"viewScope":"owner","domains":{}}`},
		{name: "missing domains", body: `{"viewScope":"self"}`},
		{name: "null domains", body: `{"viewScope":"self","domains":null}`},
		{name: "array domains", body: `{"viewScope":"self","domains":[]}`},
		{name: "unsupported domain", body: `{"viewScope":"self","domains":{"unknown.domain":null}}`},
		{name: "too many domains", body: `{"viewScope":"self","domains":{` + strings.Join(tooMany, ",") + `}}`},
		{name: "unknown token field", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"e","scope":"s","domain":"accounts.runtime","sequence":0,"resetSequence":0,"extra":true}}}`},
		{name: "missing token field", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"e","scope":"s","domain":"accounts.runtime","sequence":0}}}`},
		{name: "blank epoch", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":" ","scope":"s","domain":"accounts.runtime","sequence":0,"resetSequence":0}}}`},
		{name: "fractional sequence", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"e","scope":"s","domain":"accounts.runtime","sequence":1.5,"resetSequence":0}}}`},
		{name: "negative sequence", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"e","scope":"s","domain":"accounts.runtime","sequence":-1,"resetSequence":0}}}`},
		{name: "unsafe reset sequence", body: `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"e","scope":"s","domain":"accounts.runtime","sequence":0,"resetSequence":9007199254740992}}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &pageDataChangeConfirmServiceStub{}
			rec := servePageDataChangeConfirm(t, NewPageDataChangeConfirmHandler(service), managementauth.Context{SystemAccountID: "sys-user", Role: "user"}, test.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if service.calls != 0 {
				t.Fatalf("service calls = %d", service.calls)
			}
		})
	}
}

func TestPageDataChangeConfirmHandlerPassesValidatedToken(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{}
	body := `{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":1e0,"epoch":" old-epoch ","scope":" scope-1 ","domain":" accounts.runtime ","sequence":12,"resetSequence":3}}}`
	rec := servePageDataChangeConfirm(t, NewPageDataChangeConfirmHandler(service), managementauth.Context{SystemAccountID: "sys-user", Role: "user"}, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	token := service.input.Domains["accounts.runtime"]
	if token == nil || token.ProtocolVersion != 1 || token.Epoch != "old-epoch" || token.Scope != "scope-1" || token.Domain != "accounts.runtime" || token.Sequence != 12 || token.ResetSequence != 3 {
		t.Fatalf("token = %#v", token)
	}
}

func TestPageDataChangeConfirmHandlerReturnsRetryableUnavailable(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{err: errors.New("redis unavailable")}
	rec := servePageDataChangeConfirm(t, NewPageDataChangeConfirmHandler(service), managementauth.Context{SystemAccountID: "sys-user", Role: "user"}, `{"viewScope":"self","domains":{"accounts.runtime":null}}`)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("status = %d, retry-after = %q, body = %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestPageDataChangeConfirmHandlerRequiresJSONContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/xml"} {
		t.Run(contentType, func(t *testing.T) {
			service := &pageDataChangeConfirmServiceStub{}
			req := httptest.NewRequest(http.MethodPost, "/data-changes/confirm", strings.NewReader(`{"viewScope":"self","domains":{}}`))
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys-user", Role: "user"}))
			rec := httptest.NewRecorder()
			NewPageDataChangeConfirmHandler(service).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || service.calls != 0 {
				t.Fatalf("content-type=%q status=%d calls=%d body=%s", contentType, rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestPageDataChangeConfirmHandlerRejectsBodyOver256KiB(t *testing.T) {
	for _, body := range []string{
		`{"viewScope":"self","domains":{"accounts.runtime":{"protocolVersion":2,"epoch":"` + strings.Repeat("e", 256<<10) + `","scope":"s","domain":"accounts.runtime","sequence":0,"resetSequence":0}}}`,
		`{"viewScope":"self","domains":{}}` + strings.Repeat(" ", 256<<10),
	} {
		service := &pageDataChangeConfirmServiceStub{}
		req := httptest.NewRequest(http.MethodPost, "/data-changes/confirm", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys-user", Role: "user"}))
		rec := httptest.NewRecorder()
		NewPageDataChangeConfirmHandler(service).ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge || service.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
		}
	}
}

func TestPageDataChangeConfirmRouterUsesReadAuthWithoutTouchAndNoStore(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{}
	readCalls := 0
	touchCalls := 0
	readAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			readCalls++
			ctx := context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys-user", Role: "user"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	touchAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			touchCalls++
			next.ServeHTTP(w, r)
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      readAuth,
		ManagementAPIAuthTouchMiddleware: touchAuth,
		ManagementPageDataConfirmHandler: NewPageDataChangeConfirmHandler(service),
	})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/data-changes/confirm", strings.NewReader(`{"viewScope":"self","domains":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || readCalls != 1 || touchCalls != 0 || service.calls != 1 {
		t.Fatalf("status=%d read=%d touch=%d service=%d body=%s", rec.Code, readCalls, touchCalls, service.calls, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestPageDataChangeConfirmRouterDoesNotRequireTouchMiddleware(t *testing.T) {
	service := &pageDataChangeConfirmServiceStub{}
	readAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys-user", Role: "user"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      readAuth,
		ManagementPageDataConfirmHandler: NewPageDataChangeConfirmHandler(service),
	})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/data-changes/confirm", strings.NewReader(`{"viewScope":"self","domains":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
}

func servePageDataChangeConfirm(t *testing.T, handler http.Handler, authContext managementauth.Context, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/data-changes/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
