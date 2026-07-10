package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		options: []managementaccounts.Option{
			{
				ID:                     "acct_main",
				SystemAccountID:        "sys_user",
				SystemAccountName:      "用户",
				OwnerSystemAccountID:   "sys_user",
				OwnerSystemAccountName: "用户",
				ProviderCode:           "openai",
				Name:                   "主账号",
				Type:                   "api_key",
				Status:                 "active",
				AccessType:             "owner",
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=sys_user&ids=acct_a,acct_b&ids=acct_a&page=3&limit=500&keyword=%20%E4%B8%BB%20&providerCode=%20openai%20&groupId=group_default&tagIds=tag_a,tag_b&type=api_key&status=active,disabled&schedulable=enabled&sorts=qualityScore:desc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Page != 3 ||
		service.input.Limit != 500 ||
		service.input.Keyword != "主" ||
		service.input.ProviderCode != "openai" ||
		service.input.GroupID != "group_default" ||
		service.input.Type != "api_key" ||
		service.input.Status != "active,disabled" ||
		service.input.Schedulable != "enabled" {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "acct_a" || service.input.IDs[1] != "acct_b" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	if len(service.input.TagIDs) != 2 || service.input.TagIDs[0] != "tag_a" || service.input.TagIDs[1] != "tag_b" {
		t.Fatalf("tagIds = %#v", service.input.TagIDs)
	}
	var body struct {
		Data []managementaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].SystemAccountName != "用户" || body.Data[0].AccessType != "owner" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user on management route")
	}
}

func TestManagementMyAccountOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/options?systemAccountId=sys_admin&limit=20", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" || service.input.IncludeSystemAccountFields {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementAccountOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAccountOptionsHandler(&managementAccountOptionServiceStub{err: errors.New("postgres password leaked")}, managementAccountScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/options", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagsHandlerRequiresAdminAndParsesScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		tags: []managementaccounts.Tag{{ID: "tag_main", Name: "主力", AccountCount: 2}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=sys_user", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !service.tagCalled || service.tagInput.SystemAccountID != "sys_user" {
		t.Fatalf("tag input = %+v, called = %v", service.tagInput, service.tagCalled)
	}
	var body struct {
		Data []managementaccounts.Tag `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "tag_main" || body.Data[0].AccountCount != 2 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagsHandlerUsesAdminSelfScopeForAll(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=all", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.tagInput.SystemAccountID != "sys_admin" {
		t.Fatalf("tag input = %+v, want admin current scope", service.tagInput)
	}
}

func TestManagementAccountTagsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.tagCalled {
		t.Fatal("service should not be called for ordinary user on management tag route")
	}
}

func TestManagementMyAccountTagsHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/tags?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.tagInput.SystemAccountID != "sys_user" {
		t.Fatalf("tag input = %+v", service.tagInput)
	}
}

func TestManagementAccountTagsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAccountTagsHandler(&managementAccountOptionServiceStub{tagErr: errors.New("postgres password leaked")}, managementAccountScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/tags", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagDeleteHandlerRequiresAdminAndParsesScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{deleteTagResult: true}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagDeleteHandler(service, managementAccountScopeAdmin))

	req := managementAccountTagDeleteRequest("/__aisys__/api/accounts/tags/tag_main?systemAccountId=sys_user", "tag_main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if !service.deleteTagCalled || service.deleteTagInput.ID != "tag_main" || service.deleteTagInput.SystemAccountID != "sys_user" {
		t.Fatalf("delete tag input = %+v, called = %v", service.deleteTagInput, service.deleteTagCalled)
	}
}

func TestManagementAccountTagDeleteHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagDeleteHandler(service, managementAccountScopeAdmin))

	req := managementAccountTagDeleteRequest("/__aisys__/api/accounts/tags/tag_main?systemAccountId=sys_admin", "tag_main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.deleteTagCalled {
		t.Fatal("service should not be called for ordinary user on management tag delete route")
	}
}

func TestManagementMyAccountTagDeleteHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{deleteTagResult: true}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagDeleteHandler(service, managementAccountScopeSelf))

	req := managementAccountTagDeleteRequest("/__aisys__/api/my-accounts/tags/tag_main?systemAccountId=sys_admin", "tag_main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if service.deleteTagInput.ID != "tag_main" || service.deleteTagInput.SystemAccountID != "sys_user" {
		t.Fatalf("delete tag input = %+v", service.deleteTagInput)
	}
}

func TestManagementAccountTagDeleteHandlerMapsNotFoundAndInUse(t *testing.T) {
	tests := []struct {
		name       string
		result     bool
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "not found", result: false, wantStatus: http.StatusNotFound, wantMsg: "标签不存在"},
		{name: "in use", err: managementaccounts.ErrAccountTagInUse, wantStatus: http.StatusBadRequest, wantMsg: "标签已绑定账户，不能删除"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountOptionServiceStub{deleteTagResult: tt.result, deleteTagErr: tt.err}
			handler := newManagementAccountTagDeleteHandler(service, managementAccountScopeSelf)
			req := managementAccountTagDeleteRequest("/__aisys__/api/my-accounts/tags/tag_main", "tag_main")
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestManagementAccountTagDeleteHandlerRedactsUnexpectedErrors(t *testing.T) {
	handler := newManagementAccountTagDeleteHandler(&managementAccountOptionServiceStub{deleteTagErr: errors.New("postgres password leaked")}, managementAccountScopeSelf)
	req := managementAccountTagDeleteRequest("/__aisys__/api/my-accounts/tags/tag_main", "tag_main")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagUpdateHandlerRequiresAdminAndParsesScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		updateTagsResult: managementaccounts.TagUpdateResult{
			Account: managementaccounts.TagUpdateAccount{
				ID:   "acct_main",
				Name: "主账号",
				Tags: []managementaccounts.TagUpdateTag{{ID: "tag_main", Name: "主力"}},
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagUpdateHandler(service, managementAccountScopeAdmin))

	req := managementAccountTagUpdateRequest("/__aisys__/api/accounts/acct_main/tags?systemAccountId=sys_user", "acct_main", `{"tags":[" 主力 ","备用"]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !service.updateTagsCalled ||
		service.updateTagsInput.AccountID != "acct_main" ||
		service.updateTagsInput.SystemAccountID != "sys_user" ||
		len(service.updateTagsInput.Tags) != 2 ||
		service.updateTagsInput.Tags[0] != " 主力 " {
		t.Fatalf("update tags input = %+v, called = %v", service.updateTagsInput, service.updateTagsCalled)
	}
	var body struct {
		Data managementaccounts.TagUpdateAccount `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "acct_main" || len(body.Data.Tags) != 1 || body.Data.Tags[0].Name != "主力" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagUpdateHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagUpdateHandler(service, managementAccountScopeAdmin))

	req := managementAccountTagUpdateRequest("/__aisys__/api/accounts/acct_main/tags?systemAccountId=sys_admin", "acct_main", `{"tags":["主力"]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.updateTagsCalled {
		t.Fatal("service should not be called for ordinary user on management tag update route")
	}
}

func TestManagementMyAccountTagUpdateHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		updateTagsResult: managementaccounts.TagUpdateResult{
			Account: managementaccounts.TagUpdateAccount{ID: "acct_main", Name: "主账号"},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagUpdateHandler(service, managementAccountScopeSelf))

	req := managementAccountTagUpdateRequest("/__aisys__/api/my-accounts/acct_main/tags?systemAccountId=sys_admin", "acct_main", `{"tags":["主力"]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.updateTagsInput.SystemAccountID != "sys_user" {
		t.Fatalf("update tags input = %+v", service.updateTagsInput)
	}
}

func TestManagementAccountTagUpdateHandlerEnqueuesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAccountOptionServiceStub{
		updateTagsResult: managementaccounts.TagUpdateResult{
			Account: managementaccounts.TagUpdateAccount{
				ID:                   "acct_main",
				SystemAccountID:      "sys_user",
				OwnerSystemAccountID: "sys_user",
				Name:                 "主账号",
				Tags:                 []managementaccounts.TagUpdateTag{{ID: "tag_new", Name: "主力"}},
			},
			PreviousTags: []managementaccounts.TagUpdateTag{{ID: "tag_old", Name: "旧标签"}},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementAccountTagUpdateHandler(
		service,
		managementAccountScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_fixed" },
		}),
	))

	req := managementAccountTagUpdateRequest("/__aisys__/api/accounts/acct_main/tags?systemAccountId=sys_user", "acct_main", `{"tags":["主力"]}`)
	req.RemoteAddr = "127.0.0.1:12345"
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_tags"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("task type = %q, want %q", queueStub.taskType, operationlogjob.TaskTypeWrite)
	}
	if queueStub.options.Queue != operationlogjob.QueueName {
		t.Fatalf("queue = %q, want %q", queueStub.options.Queue, operationlogjob.QueueName)
	}
	if queueStub.options.MaxRetry == nil || *queueStub.options.MaxRetry != 10 {
		t.Fatalf("max retry = %+v, want 10", queueStub.options.MaxRetry)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_fixed" ||
		logInput.TraceID != "req_tags" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorUsername != "admin" ||
		logInput.ActorDisplayName != "管理员" ||
		logInput.ActorRole != "admin" ||
		logInput.OperationScopeSystemAccountID != "sys_user" ||
		logInput.Mode != "admin" ||
		logInput.Module != "accounts" ||
		logInput.Action != "update_tags" ||
		logInput.OperationKey != "accounts.update_tags" ||
		logInput.ResourceType != "account" ||
		logInput.ResourceID != "acct_main" ||
		logInput.ResourceName != "主账号" ||
		logInput.Summary != "更新账户标签：主账号" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/accounts/acct_main/tags" ||
		logInput.ClientIP != "127.0.0.1" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "tags" ||
		logInput.Changes[0].Label != "标签" {
		t.Fatalf("changes = %+v", logInput.Changes)
	}
	before, beforeOK := logInput.Changes[0].Before.(string)
	after, afterOK := logInput.Changes[0].After.(string)
	if !beforeOK || !afterOK ||
		before != `[{"id":"tag_old","name":"旧标签"}]` ||
		after != `[{"id":"tag_new","name":"主力"}]` {
		t.Fatalf("change values before=%#v after=%#v", logInput.Changes[0].Before, logInput.Changes[0].After)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_user" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" {
		t.Fatalf("viewers = %+v", logInput.Viewers)
	}
}

func TestManagementAccountTagUpdateHandlerKeepsSuccessWhenOperationLogQueueFails(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		updateTagsResult: managementaccounts.TagUpdateResult{
			Account: managementaccounts.TagUpdateAccount{ID: "acct_main", SystemAccountID: "sys_user", Name: "主账号"},
		},
	}
	queueStub := &operationLogQueueStub{err: errors.New("redis down")}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagUpdateHandler(
		service,
		managementAccountScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config: config.Config{TrustProxy: "false"},
			Client: queueStub,
		}),
	))

	req := managementAccountTagUpdateRequest("/__aisys__/api/my-accounts/acct_main/tags", "acct_main", `{"tags":["主力"]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
}

func TestManagementAccountTagUpdateHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "invalid body", body: `{"tags":"主力"}`, wantStatus: http.StatusBadRequest, wantMsg: "账户标签参数无效"},
		{name: "unknown field", body: `{"tags":[],"extra":true}`, wantStatus: http.StatusBadRequest, wantMsg: "账户标签参数无效"},
		{name: "trailing json token", body: `{"tags":[]} true`, wantStatus: http.StatusBadRequest, wantMsg: "账户标签参数无效"},
		{name: "account not found", body: `{"tags":["主力"]}`, err: managementaccounts.ErrAccountNotFound, wantStatus: http.StatusNotFound, wantMsg: "账户不存在"},
		{name: "validation", body: `{"tags":["主力"]}`, err: &managementaccounts.ValidationError{Message: "单个账户最多配置 24 个标签"}, wantStatus: http.StatusBadRequest, wantMsg: "单个账户最多配置 24 个标签"},
		{name: "store error", body: `{"tags":["主力"]}`, err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountOptionServiceStub{updateTagsErr: tt.err}
			handler := newManagementAccountTagUpdateHandler(service, managementAccountScopeSelf)
			req := managementAccountTagUpdateRequest("/__aisys__/api/my-accounts/acct_main/tags", "acct_main", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestRouterRegistersW2ManagementAccountOptions(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		options:          []managementaccounts.Option{{ID: "acct_main", OwnerSystemAccountID: "sys_admin", Name: "主账号", ProviderCode: "openai", Type: "api_key", Status: "active", AccessType: "owner"}},
		tags:             []managementaccounts.Tag{{ID: "tag_main", Name: "主力"}},
		deleteTagResult:  true,
		updateTagsResult: managementaccounts.TagUpdateResult{Account: managementaccounts.TagUpdateAccount{ID: "acct_main", Name: "主账号"}},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	router := NewRouter(RouterOptions{
		Config:                              config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                              slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAccountOptionsHandler:     newManagementAccountOptionsHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountOptionsHandler:   newManagementAccountOptionsHandler(service, managementAccountScopeSelf),
		ManagementAccountTagsHandler:        newManagementAccountTagsHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountTagsHandler:      newManagementAccountTagsHandler(service, managementAccountScopeSelf),
		ManagementAccountTagDeleteHandler:   newManagementAccountTagDeleteHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountTagDeleteHandler: newManagementAccountTagDeleteHandler(service, managementAccountScopeSelf),
		ManagementAccountTagUpdateHandler:   newManagementAccountTagUpdateHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountTagUpdateHandler: newManagementAccountTagUpdateHandler(service, managementAccountScopeSelf),
		ManagementAPIAuthMiddleware:         NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:    NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	for _, path := range []string{"/__aisys__/api/accounts/options", "/__aisys__/api/my-accounts/options", "/__aisys__/api/accounts/tags", "/__aisys__/api/my-accounts/tags"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}

	for _, path := range []string{"/__aisys__/api/accounts/tags/tag_main", "/__aisys__/api/my-accounts/tags/tag_main"} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}

	for _, path := range []string{"/__aisys__/api/accounts/acct_main/tags", "/__aisys__/api/my-accounts/acct_main/tags"} {
		req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"tags":["主力"]}`))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader == "" {
		t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}
}

func TestRouterDoesNotRegisterW2ManagementAccountOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAccountOptionsHandler:   newManagementAccountOptionsHandler(&managementAccountOptionServiceStub{}, managementAccountScopeAdmin),
		ManagementAccountTagsHandler:      newManagementAccountTagsHandler(&managementAccountOptionServiceStub{}, managementAccountScopeAdmin),
		ManagementAccountTagUpdateHandler: newManagementAccountTagUpdateHandler(&managementAccountOptionServiceStub{}, managementAccountScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/accounts/options", "/__aisys__/api/accounts/tags"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/tags/tag_main", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/__aisys__/api/accounts/acct_main/tags", strings.NewReader(`{"tags":["主力"]}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementAccountOptionServiceStub struct {
	called           bool
	input            managementaccounts.OptionListInput
	options          []managementaccounts.Option
	err              error
	tagCalled        bool
	tagInput         managementaccounts.TagListInput
	tags             []managementaccounts.Tag
	tagErr           error
	deleteTagCalled  bool
	deleteTagInput   managementaccounts.TagDeleteInput
	deleteTagResult  bool
	deleteTagErr     error
	updateTagsCalled bool
	updateTagsInput  managementaccounts.TagUpdateInput
	updateTagsResult managementaccounts.TagUpdateResult
	updateTagsErr    error
}

type operationLogQueueStub struct {
	calls    int
	taskType string
	payload  []byte
	options  queue.EnqueueOptions
	err      error
}

func managementAccountTagDeleteRequest(target string, tagID string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("tagId", tagID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func managementAccountTagUpdateRequest(target string, accountID string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", accountID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func (s *managementAccountOptionServiceStub) Options(_ *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *managementAccountOptionServiceStub) Tags(_ *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error) {
	s.tagCalled = true
	s.tagInput = input
	return s.tags, s.tagErr
}

func (s *managementAccountOptionServiceStub) DeleteTag(_ *http.Request, input managementaccounts.TagDeleteInput) (bool, error) {
	s.deleteTagCalled = true
	s.deleteTagInput = input
	return s.deleteTagResult, s.deleteTagErr
}

func (s *managementAccountOptionServiceStub) UpdateTags(_ *http.Request, input managementaccounts.TagUpdateInput) (managementaccounts.TagUpdateResult, error) {
	s.updateTagsCalled = true
	s.updateTagsInput = input
	return s.updateTagsResult, s.updateTagsErr
}

func (s *operationLogQueueStub) Enqueue(_ context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	s.calls++
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	s.options = opts
	return queue.TaskInfo{ID: "task_1", Queue: opts.Queue, Type: taskType}, s.err
}
