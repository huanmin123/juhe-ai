package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

func TestManagementGroupStatusSnapshotHandlerValidatesAndScopes(t *testing.T) {
	service := &managementGroupStatusSnapshotServiceStub{result: managementgroups.StatusSnapshotResult{Items: []managementgroups.StatusSnapshotItem{}}}
	handler := newManagementGroupStatusSnapshotHandler(service, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/status-snapshot?groupIds=grp_1,%20grp_2,grp_1&systemAccountId=sys_other", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 || service.input.SystemAccountID != "sys_user" || !service.input.SelfOnly || !sameHTTPStrings(service.input.GroupIDs, []string{"grp_1", "grp_2"}) {
		t.Fatalf("service input = %+v calls=%d", service.input, service.calls)
	}
}

func TestManagementGroupStatusSnapshotHandlerRejectsInvalidIDsAndAdminScope(t *testing.T) {
	for _, raw := range []string{"", strings.Join(makeStatusSnapshotIDs(101), ","), strings.Repeat("x", managementgroups.MaxStatusSnapshotQuery+1)} {
		service := &managementGroupStatusSnapshotServiceStub{}
		handler := newManagementGroupStatusSnapshotHandler(service, managementGroupScopeSelf)
		req := httptest.NewRequest(http.MethodGet, "/status-snapshot?groupIds="+raw, nil)
		req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || service.calls != 0 {
			t.Fatalf("raw length=%d status=%d calls=%d body=%s", len(raw), rec.Code, service.calls, rec.Body.String())
		}
	}

	service := &managementGroupStatusSnapshotServiceStub{}
	handler := newManagementGroupStatusSnapshotHandler(service, managementGroupScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/status-snapshot?groupIds=grp_1", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
}

type managementGroupStatusSnapshotServiceStub struct {
	calls  int
	input  managementgroups.StatusSnapshotInput
	result managementgroups.StatusSnapshotResult
	err    error
}

func (s *managementGroupStatusSnapshotServiceStub) StatusSnapshot(_ *http.Request, input managementgroups.StatusSnapshotInput) (managementgroups.StatusSnapshotResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func makeStatusSnapshotIDs(count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = "g" + strings.Repeat("x", index+1)
	}
	return result
}

func sameHTTPStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
