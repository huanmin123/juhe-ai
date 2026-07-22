package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
)

func TestManagementSystemTeamListUsesNodeCompactDTO(t *testing.T) {
	service := &managementSystemTeamServiceStub{
		listResult: managementsystemteams.ListResult{
			Items: []managementsystemteams.ListItem{{
				ID: "team_ops", Name: "运维团队", Description: "负责稳定性", Status: "active",
				MemberCount: 1, CreatedAt: "2026-07-09T10:00:00Z",
			}},
			Page: 1, PageSize: 20,
		},
	}
	handler := newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin)
	req := managementCompactContractRequest(http.MethodGet, "/__aisys__/api/system-teams", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	item := responseDataListItem(t, rec, http.StatusOK)
	assertExactJSONKeys(t, item, "createdAt", "description", "id", "memberCount", "name", "status")
}

func TestManagementSystemTeamDetailAndWritesUseNodeCompactDTO(t *testing.T) {
	detail := richSystemTeamDetailForCompactContract()
	tests := []struct {
		name    string
		handler http.Handler
		request *http.Request
		status  int
		members int
	}{
		{
			name: "detail",
			handler: newManagementSystemTeamsHandler(&managementSystemTeamServiceStub{
				detailFound: true, detailResult: detail,
			}, managementSystemTeamScopeAdmin),
			request: withSystemTeamRouteParam(managementCompactContractRequest(http.MethodGet, "/__aisys__/api/system-teams/team_ops", nil), "team_ops"),
			status:  http.StatusOK,
			members: 1,
		},
		{
			name: "create",
			handler: newManagementSystemTeamCreateHandler(&managementSystemTeamServiceStub{
				result: detail.Summary,
			}, managementOperationLogOptions{}),
			request: managementCompactContractRequest(http.MethodPost, "/__aisys__/api/system-teams", strings.NewReader(`{"name":"运维团队"}`)),
			status:  http.StatusCreated,
			members: 0,
		},
		{
			name: "update",
			handler: newManagementSystemTeamPatchHandler(&managementSystemTeamServiceStub{
				updateFound: true, updateResult: managementsystemteams.UpdateResult{Team: detail},
			}, managementOperationLogOptions{}),
			request: withSystemTeamRouteParam(managementCompactContractRequest(http.MethodPatch, "/__aisys__/api/system-teams/team_ops", strings.NewReader(`{"status":"active"}`)), "team_ops"),
			status:  http.StatusOK,
			members: 1,
		},
		{
			name: "add members",
			handler: newManagementSystemTeamMembersAddHandler(&managementSystemTeamServiceStub{
				addFound: true, addResult: managementsystemteams.AddMembersResult{Team: detail},
			}, managementOperationLogOptions{}),
			request: withSystemTeamRouteParam(managementCompactContractRequest(http.MethodPost, "/__aisys__/api/system-teams/team_ops/members", strings.NewReader(`{"systemAccountIds":["sys_member"]}`)), "team_ops"),
			status:  http.StatusOK,
			members: 1,
		},
		{
			name: "remove member",
			handler: newManagementSystemTeamMemberDeleteHandler(&managementSystemTeamServiceStub{
				removeFound: true, removeResult: managementsystemteams.RemoveMemberResult{Team: detail},
			}, managementOperationLogOptions{}),
			request: withSystemTeamMemberRouteParam(managementCompactContractRequest(http.MethodDelete, "/__aisys__/api/system-teams/team_ops/members/teammem_1", nil), "team_ops", "teammem_1"),
			status:  http.StatusOK,
			members: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			test.handler.ServeHTTP(rec, test.request)
			data := responseDataObject(t, rec, test.status)
			assertExactJSONKeys(t, data, "createdAt", "description", "id", "memberCount", "members", "name", "status")
			members, ok := data["members"].([]any)
			if !ok || len(members) != test.members {
				t.Fatalf("members = %#v, want %d compact members", data["members"], test.members)
			}
			if test.members == 0 {
				return
			}
			member, ok := members[0].(map[string]any)
			if !ok {
				t.Fatalf("member = %#v", members[0])
			}
			assertExactJSONKeys(t, member, "id", "joinedAt", "systemAccountId", "systemAccountName")
		})
	}
}

func TestManagementAuthorizationListUsesNodeCompactDTO(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	service := &managementAuthorizationCreateServiceStub{
		listResult: managementauthorizations.ListResult{
			Items: []managementauthorizations.ListItem{{
				ID:                             "rauthgrant_main",
				ResourceType:                   "account",
				ResourceID:                     "acct_main",
				ResourceName:                   "主账号",
				ResourceOwnerSystemAccountID:   "sys_owner",
				ResourceOwnerSystemAccountName: "资源所有者",
				GranteeType:                    "system_account",
				GranteeSystemAccountID:         "sys_grantee",
				GranteeSystemAccountName:       "被授权人",
				GranteeUsername:                "grantee",
				Status:                         "active",
				Remark:                         "列表备注",
				ExpiresAt:                      &createdAt,
				EffectiveSourceType:            "manual",
				CreatedAt:                      createdAt,
				Permissions:                    managementauthorizations.Permissions{CanEdit: true, CanAuthorize: true},
				SourceSummary: managementauthorizations.SourceSummary{
					ActiveSourceCount: 1,
					HasManual:         true,
					TeamSources:       []managementauthorizations.TeamSourceItem{},
				},
			}},
			Page: 1, PageSize: 50,
		},
	}
	handler := newManagementAuthorizationListHandler(service, managementAuthorizationScopeAdmin)
	req := managementCompactContractRequest(http.MethodGet, "/__aisys__/api/authorizations", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	item := responseDataListItem(t, rec, http.StatusOK)
	assertExactJSONKeys(t, item,
		"createdAt", "effectiveSourceType", "expiresAt", "granteeSystemAccountId", "granteeSystemAccountName",
		"granteeType", "granteeUsername", "id", "permissions", "remark", "resourceId",
		"resourceName", "resourceOwnerSystemAccountId", "resourceOwnerSystemAccountName",
		"resourceType", "sourceSummary", "status",
	)
	permissions, ok := item["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v", item["permissions"])
	}
	assertExactJSONKeys(t, permissions, "canAuthorize", "canEdit")
	sourceSummary, ok := item["sourceSummary"].(map[string]any)
	if !ok {
		t.Fatalf("sourceSummary = %#v", item["sourceSummary"])
	}
	assertExactJSONKeys(t, sourceSummary, "activeSourceCount", "hasManual", "hasTeam", "teamSources")
}

func richSystemTeamSummaryForCompactContract() managementsystemteams.Summary {
	return managementsystemteams.Summary{
		ID: "team_ops", Name: "运维团队", Description: "负责稳定性", Status: "active",
		MemberCount: 1, ActiveMemberCount: 1, CreatedBy: "sys_admin",
		CreatedAt: "2026-07-09T10:00:00Z", UpdatedAt: "2026-07-09T11:00:00Z",
	}
}

func richSystemTeamDetailForCompactContract() managementsystemteams.Detail {
	return managementsystemteams.Detail{
		Summary: richSystemTeamSummaryForCompactContract(),
		Members: []managementsystemteams.MemberSummary{{
			ID: "teammem_1", TeamID: "team_ops", SystemAccountID: "sys_member",
			SystemAccountName: "成员", Username: "member", MemberRole: "member", Status: "active",
			JoinedAt: "2026-07-09T10:05:00Z", CreatedAt: "2026-07-09T10:05:00Z", UpdatedAt: "2026-07-09T10:05:00Z",
		}},
	}
}

func managementCompactContractRequest(method string, target string, body *strings.Reader) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin",
	}))
}

func responseDataListItem(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) map[string]any {
	t.Helper()
	data := responseDataObject(t, rec, wantStatus)
	items, ok := data["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one item", data["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %#v", items[0])
	}
	return item
}

func responseDataObject(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) map[string]any {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope.Data
}

func assertExactJSONKeys(t *testing.T, value map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("json keys = %v, want %v", got, want)
	}
}
