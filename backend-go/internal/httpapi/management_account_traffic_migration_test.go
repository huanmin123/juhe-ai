package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounttrafficmigration"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountTrafficMigrationAdminAndSelfScopes(t *testing.T) {
	for _, test := range []struct {
		name         string
		scope        managementAccountTrafficMigrationScope
		path         string
		role         string
		wantSystemID string
		wantSelf     bool
	}{
		{name: "admin", scope: managementAccountTrafficMigrationScopeAdmin, path: "/__aisys__/api/accounts/source/traffic-migration?systemAccountId=sys_owner", role: "admin", wantSystemID: "sys_owner"},
		{name: "self", scope: managementAccountTrafficMigrationScopeSelf, path: "/__aisys__/api/my-accounts/source/traffic-migration?systemAccountId=sys_other", role: "user", wantSelf: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &trafficMigrationServiceStub{result: trafficMigrationHTTPResult()}
			handler := newManagementAccountTrafficMigrationHandler(service, test.scope)
			req := trafficMigrationRequest(test.path, `{"targetAccountId":" target ","sourceStatus":"unchanged"}`)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_actor", Role: test.role}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if service.input.SystemAccountID != test.wantSystemID || service.input.SelfOnly != test.wantSelf || service.input.TargetAccountID != " target " {
				t.Fatalf("input=%+v", service.input)
			}
			var body struct {
				Data managementaccounttrafficmigration.Result `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Data.SourceAccount.ID != "source" || body.Data.MigratedSessionCount != 1 {
				t.Fatalf("body=%s err=%v", rec.Body.String(), err)
			}
		})
	}
}

func TestManagementAccountTrafficMigrationValidation(t *testing.T) {
	service := &trafficMigrationServiceStub{}
	handler := newManagementAccountTrafficMigrationHandler(service, managementAccountTrafficMigrationScopeAdmin)
	req := trafficMigrationRequest("/__aisys__/api/accounts/source/traffic-migration", `{"targetAccountId":"target","extra":true}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_actor", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "迁移流量参数无效") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type trafficMigrationServiceStub struct {
	input  managementaccounttrafficmigration.Input
	result managementaccounttrafficmigration.Result
	err    error
}

func (s *trafficMigrationServiceStub) Migrate(_ *http.Request, input managementaccounttrafficmigration.Input) (managementaccounttrafficmigration.Result, error) {
	s.input = input
	return s.result, s.err
}

func trafficMigrationRequest(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "source")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func trafficMigrationHTTPResult() managementaccounttrafficmigration.Result {
	return managementaccounttrafficmigration.Result{SourceAccount: managementaccounttrafficmigration.Account{ID: "source"}, TargetAccount: managementaccounttrafficmigration.Account{ID: "target"}, SourceStatus: managementaccounttrafficmigration.SourceStatusUnchanged, MigratedSessionCount: 1}
}
