//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementAccountOptionsPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2GroupOptionsFixture(t, ctx, db, now)
	insertW2AccountOptionsFixture(t, ctx, db, now)
	insertW2AccountAuthorizationFixture(t, ctx, db, now)
	insertW2AccountTagsFixture(t, ctx, db, now)
	sessionToken := "w2-management-account-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementaccounts.NewService(store)
	adminOptions, err := service.Options(ctx, managementaccounts.OptionListInput{
		IncludeSystemAccountFields: true,
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("admin account options: %v", err)
	}
	if findAccountOption(adminOptions, "acct_w2_other") == nil {
		t.Fatalf("admin all options should include other owner: %+v", adminOptions)
	}
	if findAccountOption(adminOptions, "acct_w2_authorized_other") != nil {
		t.Fatalf("admin all options should not duplicate authorized instances: %+v", adminOptions)
	}

	selfOptions, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("self account options: %v", err)
	}
	if findAccountOption(selfOptions, "acct_w2_other") != nil {
		t.Fatalf("self options leaked other owner: %+v", selfOptions)
	}
	if option := findAccountOption(selfOptions, "acct_w2_alpha"); option == nil || option.SystemAccountID != "" || option.AccessType != "owner" {
		t.Fatalf("self options should hide management owner fields: %+v", selfOptions)
	}
	if option := findAccountOption(selfOptions, "acct_w2_authorized_other"); option == nil ||
		option.SystemAccountID != "" ||
		option.AccessType != "authorized" ||
		option.AccountAuthorizationID != "auth_account_w2_other" ||
		option.AuthorizationStatus != "active" ||
		option.AuthorizationInstanceSourceAccountID != "acct_w2_other" ||
		option.AuthorizationInstanceOwnerSystemAccountID != "sys_w2_group_other" ||
		option.OwnerSystemAccountID != "sys_w2_group_other" ||
		option.OwnerSystemAccountName != "W2 Group Other" ||
		!option.Permissions.CanUse ||
		option.Permissions.CanAuthorize ||
		option.Permissions.CanViewCredentials ||
		option.Permissions.CanBindToAPIKey {
		t.Fatalf("self options missing authorized account or permissions: %+v", selfOptions)
	}

	groupFiltered, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID:            "sys_w2_proxy_options",
		IncludeSystemAccountFields: true,
		ProviderCode:               "openai",
		GroupID:                    "group_w2_default",
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("group filtered account options: %v", err)
	}
	if findAccountOption(groupFiltered, "acct_w2_alpha") == nil || findAccountOption(groupFiltered, "acct_w2_authorized_other") == nil {
		t.Fatalf("group filtered options = %+v", groupFiltered)
	}

	tagFiltered, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID:            "sys_w2_proxy_options",
		IncludeSystemAccountFields: true,
		TagIDs:                     []string{"tag_w2_main"},
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("tag filtered account options: %v", err)
	}
	if findAccountOption(tagFiltered, "acct_w2_alpha") == nil || findAccountOption(tagFiltered, "acct_w2_authorized_other") == nil {
		t.Fatalf("tag filtered options = %+v", tagFiltered)
	}

	tags, err := service.Tags(ctx, managementaccounts.TagListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("account tags: %v", err)
	}
	mainTag := findAccountTag(tags, "tag_w2_main")
	if mainTag == nil || mainTag.AccountCount != 2 {
		t.Fatalf("main tag = %+v, tags = %+v", mainTag, tags)
	}
	if findAccountTag(tags, "tag_w2_other") != nil {
		t.Fatalf("tags leaked other owner: %+v", tags)
	}
	if deleted, err := service.DeleteTag(ctx, managementaccounts.TagDeleteInput{ID: "tag_w2_main", SystemAccountID: "sys_w2_proxy_options"}); !errors.Is(err, managementaccounts.ErrAccountTagInUse) || deleted {
		t.Fatalf("delete bound tag = %v / %v, want in-use error", deleted, err)
	}
	if deleted, err := service.DeleteTag(ctx, managementaccounts.TagDeleteInput{ID: "tag_w2_other", SystemAccountID: "sys_w2_proxy_options"}); err != nil || deleted {
		t.Fatalf("delete other owner tag = %v / %v, want not found", deleted, err)
	}

	updatedAccount, err := service.UpdateTags(ctx, managementaccounts.TagUpdateInput{
		AccountID:       "acct_w2_percent",
		SystemAccountID: "sys_w2_proxy_options",
		Tags:            []string{" 灰度   发布 ", "灰度 发布", ""},
	})
	if err != nil {
		t.Fatalf("update account tags: %v", err)
	}
	if updatedAccount.Account.ID != "acct_w2_percent" || !sameAccountTagNames(updatedAccount.Account.Tags, []string{"灰度 发布"}) {
		t.Fatalf("updated account tags = %+v", updatedAccount.Account.Tags)
	}
	if _, err := service.UpdateTags(ctx, managementaccounts.TagUpdateInput{
		AccountID:       "acct_w2_authorized_revoked",
		SystemAccountID: "sys_w2_proxy_options",
		Tags:            []string{"不可见"},
	}); !errors.Is(err, managementaccounts.ErrAccountNotFound) {
		t.Fatalf("update revoked authorized account err = %v, want not found", err)
	}

	available, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Status:          "active",
		Schedulable:     "enabled",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("available account options: %v", err)
	}
	if findAccountOption(available, "acct_w2_unschedulable") != nil ||
		findAccountOption(available, "acct_w2_cooling") != nil ||
		findAccountOption(available, "acct_w2_authorized_unbound") != nil {
		t.Fatalf("available options included blocked accounts: %+v", available)
	}
	if findAccountOption(available, "acct_w2_alpha") == nil || findAccountOption(available, "acct_w2_authorized_other") == nil {
		t.Fatalf("available options missing active account: %+v", available)
	}

	prefixed, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Keyword:         "Percent%",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("prefixed account options: %v", err)
	}
	if len(prefixed) != 1 || prefixed[0].ID != "acct_w2_percent" {
		t.Fatalf("prefixed options = %+v", prefixed)
	}

	contains, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Keyword:         "cent%",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("contains account options: %v", err)
	}
	if len(contains) != 1 || contains[0].ID != "acct_w2_percent" {
		t.Fatalf("contains options = %+v", contains)
	}

	containsFiltered, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Keyword:         "pha Acc",
		GroupID:         "group_w2_default",
		TagIDs:          []string{"tag_w2_main"},
		Status:          "active",
		Schedulable:     "enabled",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("contains filtered account options: %v", err)
	}
	if len(containsFiltered) != 1 || containsFiltered[0].ID != "acct_w2_alpha" {
		t.Fatalf("contains filtered options = %+v", containsFiltered)
	}

	nonContinuous, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Keyword:         "Aha",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("non-continuous account options: %v", err)
	}
	if len(nonContinuous) != 0 {
		t.Fatalf("non-continuous search should not match terms without document substring: %+v", nonContinuous)
	}

	selfOwnerSearch, err := service.Options(ctx, managementaccounts.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Keyword:         "Owner",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("self owner keyword account options: %v", err)
	}
	if findAccountOption(selfOwnerSearch, "acct_w2_other") != nil {
		t.Fatalf("self contains search leaked other owner: %+v", selfOwnerSearch)
	}
	if findAccountOption(selfOwnerSearch, "acct_w2_authorized_other") == nil {
		t.Fatalf("self contains search should include authorized instance name: %+v", selfOwnerSearch)
	}
	globalOwnerSearch, err := service.Options(ctx, managementaccounts.OptionListInput{
		IncludeSystemAccountFields: true,
		Keyword:                    "Owner",
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("global owner keyword account options: %v", err)
	}
	if findAccountOption(globalOwnerSearch, "acct_w2_other") == nil {
		t.Fatalf("global contains search missing other owner: %+v", globalOwnerSearch)
	}
	if findAccountOption(globalOwnerSearch, "acct_w2_authorized_other") != nil {
		t.Fatalf("global contains search should not duplicate authorized instances: %+v", globalOwnerSearch)
	}

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	operationLogQueue := &w2OperationLogQueueStub{}
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config: config.Config{TrustProxy: "false"},
		Logger: slog.Default(),
		Client: operationLogQueue,
		Now:    func() time.Time { return now },
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                              slog.Default(),
		ManagementAPIAuthMiddleware:         httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAccountOptionsHandler:     httpapi.NewManagementAccountOptionsHandler(service),
		ManagementMyAccountOptionsHandler:   httpapi.NewManagementMyAccountOptionsHandler(service),
		ManagementAccountTagsHandler:        httpapi.NewManagementAccountTagsHandler(service),
		ManagementMyAccountTagsHandler:      httpapi.NewManagementMyAccountTagsHandler(service),
		ManagementAccountTagDeleteHandler:   httpapi.NewManagementAccountTagDeleteHandler(service),
		ManagementMyAccountTagDeleteHandler: httpapi.NewManagementMyAccountTagDeleteHandler(service),
		ManagementAccountTagUpdateHandler:   httpapi.NewManagementAccountTagUpdateHandlerWithOperationLog(service, operationLogOptions),
		ManagementMyAccountTagUpdateHandler: httpapi.NewManagementMyAccountTagUpdateHandlerWithOperationLog(service, operationLogOptions),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=sys_w2_proxy_options&groupId=group_w2_default&tagIds=tag_w2_main", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var adminBody struct {
		Data []managementaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&adminBody); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if option := findAccountOption(adminBody.Data, "acct_w2_alpha"); option == nil || option.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("admin response missing owner-scoped account: %+v", adminBody.Data)
	}
	if option := findAccountOption(adminBody.Data, "acct_w2_authorized_other"); option == nil || option.AccessType != "authorized" || option.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("admin response missing authorized account: %+v", adminBody.Data)
	}

	tagReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=sys_w2_proxy_options", nil)
	tagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	tagRec := httptest.NewRecorder()
	router.ServeHTTP(tagRec, tagReq)
	if tagRec.Code != http.StatusOK {
		t.Fatalf("tag status = %d, body = %s", tagRec.Code, tagRec.Body.String())
	}
	var tagBody struct {
		Data []managementaccounts.Tag `json:"data"`
	}
	if err := json.NewDecoder(tagRec.Body).Decode(&tagBody); err != nil {
		t.Fatalf("decode tag response: %v", err)
	}
	if tag := findAccountTag(tagBody.Data, "tag_w2_main"); tag == nil || tag.AccountCount != 2 {
		t.Fatalf("tag response missing account count: %+v", tagBody.Data)
	}

	deleteBoundTagReq := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/tags/tag_w2_main?systemAccountId=sys_w2_proxy_options", nil)
	deleteBoundTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	deleteBoundTagRec := httptest.NewRecorder()
	router.ServeHTTP(deleteBoundTagRec, deleteBoundTagReq)
	if deleteBoundTagRec.Code != http.StatusBadRequest || !strings.Contains(deleteBoundTagRec.Body.String(), "标签已绑定账户，不能删除") {
		t.Fatalf("delete bound tag status = %d, body = %s", deleteBoundTagRec.Code, deleteBoundTagRec.Body.String())
	}

	deleteOtherTagReq := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/tags/tag_w2_other?systemAccountId=sys_w2_proxy_options", nil)
	deleteOtherTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	deleteOtherTagRec := httptest.NewRecorder()
	router.ServeHTTP(deleteOtherTagRec, deleteOtherTagReq)
	if deleteOtherTagRec.Code != http.StatusNotFound || !strings.Contains(deleteOtherTagRec.Body.String(), "标签不存在") {
		t.Fatalf("delete other owner tag status = %d, body = %s", deleteOtherTagRec.Code, deleteOtherTagRec.Body.String())
	}

	updateAuthorizedTagReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/accounts/acct_w2_authorized_other/tags?systemAccountId=sys_w2_proxy_options", strings.NewReader(`{"tags":["授权标签"]}`))
	updateAuthorizedTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	updateAuthorizedTagRec := httptest.NewRecorder()
	router.ServeHTTP(updateAuthorizedTagRec, updateAuthorizedTagReq)
	if updateAuthorizedTagRec.Code != http.StatusOK {
		t.Fatalf("update authorized tag status = %d, body = %s", updateAuthorizedTagRec.Code, updateAuthorizedTagRec.Body.String())
	}
	var updateAuthorizedTagBody struct {
		Data managementaccounts.AccountSummary `json:"data"`
	}
	if err := json.NewDecoder(updateAuthorizedTagRec.Body).Decode(&updateAuthorizedTagBody); err != nil {
		t.Fatalf("decode update authorized tag response: %v", err)
	}
	if updateAuthorizedTagBody.Data.ID != "acct_w2_authorized_other" ||
		updateAuthorizedTagBody.Data.AccessType != "authorized" ||
		updateAuthorizedTagBody.Data.AuthorizationStatus != "active" ||
		!sameAccountTagNames(updateAuthorizedTagBody.Data.Tags, []string{"授权标签"}) {
		t.Fatalf("update authorized tag response = %+v", updateAuthorizedTagBody.Data)
	}
	updateAuthorizedBindings := readW2AccountTagBindings(t, ctx, db, "acct_w2_authorized_other", "sys_w2_proxy_options")
	if len(updateAuthorizedBindings) != 1 || updateAuthorizedBindings["授权标签"] == "" {
		t.Fatalf("update authorized tag bindings = %+v", updateAuthorizedBindings)
	}

	updateTagReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/accounts/acct_w2_other/tags?systemAccountId=sys_w2_group_other", strings.NewReader(`{"tags":["其他用户标签","跨账户标签","其他用户标签"]}`))
	updateTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	updateTagRec := httptest.NewRecorder()
	router.ServeHTTP(updateTagRec, updateTagReq)
	if updateTagRec.Code != http.StatusOK {
		t.Fatalf("update tag status = %d, body = %s", updateTagRec.Code, updateTagRec.Body.String())
	}
	var updateTagBody struct {
		Data managementaccounts.AccountSummary `json:"data"`
	}
	if err := json.NewDecoder(updateTagRec.Body).Decode(&updateTagBody); err != nil {
		t.Fatalf("decode update tag response: %v", err)
	}
	if updateTagBody.Data.ID != "acct_w2_other" ||
		updateTagBody.Data.SystemAccountID != "sys_w2_group_other" ||
		updateTagBody.Data.AccessType != "owner" ||
		!sameAccountTagNames(updateTagBody.Data.Tags, []string{"其他用户标签", "跨账户标签"}) {
		t.Fatalf("update tag response = %+v", updateTagBody.Data)
	}
	updateBindings := readW2AccountTagBindings(t, ctx, db, "acct_w2_other", "sys_w2_group_other")
	if len(updateBindings) != 2 || updateBindings["其他用户标签"] != "tag_w2_other" || updateBindings["跨账户标签"] == "" {
		t.Fatalf("update tag bindings = %+v", updateBindings)
	}

	deleteEmptyTagReq := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/tags/tag_w2_empty?systemAccountId=sys_w2_proxy_options", nil)
	deleteEmptyTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	deleteEmptyTagRec := httptest.NewRecorder()
	router.ServeHTTP(deleteEmptyTagRec, deleteEmptyTagReq)
	if deleteEmptyTagRec.Code != http.StatusNoContent {
		t.Fatalf("delete empty tag status = %d, body = %s", deleteEmptyTagRec.Code, deleteEmptyTagRec.Body.String())
	}

	deleteMyEmptyTagReq := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-accounts/tags/tag_w2_my_empty?systemAccountId=sys_w2_group_other", nil)
	deleteMyEmptyTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	deleteMyEmptyTagRec := httptest.NewRecorder()
	router.ServeHTTP(deleteMyEmptyTagRec, deleteMyEmptyTagReq)
	if deleteMyEmptyTagRec.Code != http.StatusNoContent {
		t.Fatalf("delete my empty tag status = %d, body = %s", deleteMyEmptyTagRec.Code, deleteMyEmptyTagRec.Body.String())
	}

	myReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/options?systemAccountId=sys_w2_group_other", nil)
	myReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myRec := httptest.NewRecorder()
	router.ServeHTTP(myRec, myReq)
	if myRec.Code != http.StatusOK {
		t.Fatalf("my status = %d, body = %s", myRec.Code, myRec.Body.String())
	}
	var myBody struct {
		Data []managementaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(myRec.Body).Decode(&myBody); err != nil {
		t.Fatalf("decode my response: %v", err)
	}
	if findAccountOption(myBody.Data, "acct_w2_other") != nil {
		t.Fatalf("my response leaked query systemAccountId owner: %+v", myBody.Data)
	}
	if option := findAccountOption(myBody.Data, "acct_w2_alpha"); option == nil || option.SystemAccountID != "" || option.AccessType != "owner" {
		t.Fatalf("my response missing self account or leaked owner fields: %+v", myBody.Data)
	}
	if option := findAccountOption(myBody.Data, "acct_w2_authorized_other"); option == nil ||
		option.SystemAccountID != "" ||
		option.AccessType != "authorized" ||
		option.AccountAuthorizationID != "auth_account_w2_other" ||
		option.OwnerSystemAccountID != "sys_w2_group_other" ||
		option.OwnerSystemAccountName != "W2 Group Other" {
		t.Fatalf("my response missing authorized account or leaked owner fields: %+v", myBody.Data)
	}

	myUpdateTagReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-accounts/acct_w2_percent/tags?systemAccountId=sys_w2_group_other", strings.NewReader(`{"tags":[" 主力 ","主力"]}`))
	myUpdateTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myUpdateTagRec := httptest.NewRecorder()
	router.ServeHTTP(myUpdateTagRec, myUpdateTagReq)
	if myUpdateTagRec.Code != http.StatusOK {
		t.Fatalf("my update tag status = %d, body = %s", myUpdateTagRec.Code, myUpdateTagRec.Body.String())
	}
	var myUpdateTagBody struct {
		Data managementaccounts.AccountSummary `json:"data"`
	}
	if err := json.NewDecoder(myUpdateTagRec.Body).Decode(&myUpdateTagBody); err != nil {
		t.Fatalf("decode my update tag response: %v", err)
	}
	if myUpdateTagBody.Data.ID != "acct_w2_percent" ||
		myUpdateTagBody.Data.SystemAccountID != "sys_w2_proxy_options" ||
		myUpdateTagBody.Data.AccessType != "owner" ||
		!sameAccountTagNames(myUpdateTagBody.Data.Tags, []string{"主力"}) {
		t.Fatalf("my update tag response = %+v", myUpdateTagBody.Data)
	}
	myUpdateBindings := readW2AccountTagBindings(t, ctx, db, "acct_w2_percent", "sys_w2_proxy_options")
	if len(myUpdateBindings) != 1 || myUpdateBindings["主力"] != "tag_w2_main" {
		t.Fatalf("my update tag bindings = %+v", myUpdateBindings)
	}
	myOtherScopeBindings := readW2AccountTagBindings(t, ctx, db, "acct_w2_percent", "sys_w2_group_other")
	if len(myOtherScopeBindings) != 0 {
		t.Fatalf("my update should not write query scope bindings: %+v", myOtherScopeBindings)
	}
	myOtherUpdateTagReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-accounts/acct_w2_other/tags?systemAccountId=sys_w2_group_other", strings.NewReader(`{"tags":["越权"]}`))
	myOtherUpdateTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myOtherUpdateTagRec := httptest.NewRecorder()
	router.ServeHTTP(myOtherUpdateTagRec, myOtherUpdateTagReq)
	if myOtherUpdateTagRec.Code != http.StatusNotFound {
		t.Fatalf("my other owner update tag status = %d, body = %s", myOtherUpdateTagRec.Code, myOtherUpdateTagRec.Body.String())
	}
	if operationLogQueue.decodeErr != nil {
		t.Fatalf("decode operation log payload: %v", operationLogQueue.decodeErr)
	}
	if len(operationLogQueue.logs) != 3 {
		t.Fatalf("operation log count = %d, want 3: %+v", len(operationLogQueue.logs), operationLogQueue.logs)
	}
	assertW2OperationLog(t, operationLogQueue.logs[0], "acct_w2_authorized_other", "sys_w2_proxy_options", "admin")
	assertW2OperationLog(t, operationLogQueue.logs[1], "acct_w2_other", "sys_w2_group_other", "admin")
	assertW2OperationLog(t, operationLogQueue.logs[2], "acct_w2_percent", "sys_w2_proxy_options", "self")
	for index, taskType := range operationLogQueue.taskTypes {
		if taskType != operationlogjob.TaskTypeWrite {
			t.Fatalf("operation log task type[%d] = %q, want %q", index, taskType, operationlogjob.TaskTypeWrite)
		}
		if operationLogQueue.options[index].Queue != operationlogjob.QueueName {
			t.Fatalf("operation log queue[%d] = %q, want %q", index, operationLogQueue.options[index].Queue, operationlogjob.QueueName)
		}
	}

	myTagReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/tags?systemAccountId=sys_w2_group_other", nil)
	myTagReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myTagRec := httptest.NewRecorder()
	router.ServeHTTP(myTagRec, myTagReq)
	if myTagRec.Code != http.StatusOK {
		t.Fatalf("my tag status = %d, body = %s", myTagRec.Code, myTagRec.Body.String())
	}
	var myTagBody struct {
		Data []managementaccounts.Tag `json:"data"`
	}
	if err := json.NewDecoder(myTagRec.Body).Decode(&myTagBody); err != nil {
		t.Fatalf("decode my tag response: %v", err)
	}
	if findAccountTag(myTagBody.Data, "tag_w2_other") != nil || findAccountTag(myTagBody.Data, "tag_w2_main") == nil {
		t.Fatalf("my tag response should force self scope: %+v", myTagBody.Data)
	}
}

func insertW2AccountOptionsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	fixtures := []struct {
		id              string
		systemAccountID string
		name            string
		providerCode    string
		profileID       string
		protocolCode    string
		protocolVersion string
		status          string
		schedulable     bool
		priority        int
		cooldownUntil   *time.Time
	}{
		{id: "acct_w2_alpha", systemAccountID: "sys_w2_proxy_options", name: "Alpha Account", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 1},
		{id: "acct_w2_percent", systemAccountID: "sys_w2_proxy_options", name: "Percent% Literal", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 2},
		{id: "acct_w2_unschedulable", systemAccountID: "sys_w2_proxy_options", name: "Unschedulable Account", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: false, priority: 3},
		{id: "acct_w2_cooling", systemAccountID: "sys_w2_proxy_options", name: "Cooling Account", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 4, cooldownUntil: timePtr(now.Add(time.Hour))},
		{id: "acct_w2_other", systemAccountID: "sys_w2_group_other", name: "Other Owner Account", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 5},
		{id: "acct_w2_unbound_source", systemAccountID: "sys_w2_group_other", name: "Other Unbound Source", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 6},
		{id: "acct_w2_revoked_source", systemAccountID: "sys_w2_group_other", name: "Other Revoked Source", providerCode: "openai", profileID: "profile_openai_openai_v1", protocolCode: "openai", protocolVersion: "v1", status: "active", schedulable: true, priority: 7},
	}
	for index, item := range fixtures {
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id,
				protocol_code, protocol_version, name, type, status, credentials_encrypted,
				credential_fingerprint, credential_mask, concurrency_limit, priority,
				super_priority_enabled, fallback_enabled, client_compatibility, schedulable,
				cooldown_until, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, 'api_key', $8, 'encrypted-fixture',
				NULL, 'sk-test', 20, $9,
				false, false, 'openai_standard', $10,
				$11, $12, $13
			)
		`, item.id, item.systemAccountID, item.providerCode, item.profileID,
			item.protocolCode, item.protocolVersion, item.name, item.status, item.priority,
			item.schedulable, item.cooldownUntil, now.Add(time.Duration(index)*time.Second), now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("insert W2 account fixture %s: %v", item.id, err)
		}
		insertW2AccountNameSearchDocument(t, ctx, db, item.id, item.systemAccountID, item.name, now)
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_name_search_terms (
			account_id, system_account_id, term, created_at
		) VALUES (
			'acct_w2_alpha', 'sys_w2_proxy_options', 'Aha', $1
		)
		ON CONFLICT (account_id, term) DO NOTHING
	`, now)
	if err != nil {
		t.Fatalf("insert W2 account false-positive search term: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, enabled, created_at, updated_at
		) VALUES (
			'sys_w2_proxy_options', 'group_w2_default', 'acct_w2_alpha', true, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 account group binding: %v", err)
	}
}

func insertW2AccountAuthorizationFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, scope, status, effective_source_type,
			activated_at, created_by, created_at, updated_at
		) VALUES
			('auth_account_w2_other', 'account', 'acct_w2_other', 'sys_w2_group_other',
				'sys_w2_proxy_options', 'use', 'active', 'manual', $1, 'sys_w2_group_other', $1, $1),
			('auth_account_w2_unbound', 'account', 'acct_w2_unbound_source', 'sys_w2_group_other',
				'sys_w2_proxy_options', 'use', 'active', 'manual', $1, 'sys_w2_group_other', $1, $1),
			('auth_account_w2_revoked', 'account', 'acct_w2_revoked_source', 'sys_w2_group_other',
				'sys_w2_proxy_options', 'use', 'revoked', 'manual', $1, 'sys_w2_group_other', $1, $1)
	`, now)
	if err != nil {
		t.Fatalf("insert W2 account authorizations: %v", err)
	}

	fixtures := []struct {
		id              string
		name            string
		authorizationID string
		priority        int
	}{
		{id: "acct_w2_authorized_other", name: "Authorized Other Owner Account", authorizationID: "auth_account_w2_other", priority: 6},
		{id: "acct_w2_authorized_unbound", name: "Authorized Unbound Account", authorizationID: "auth_account_w2_unbound", priority: 7},
		{id: "acct_w2_authorized_revoked", name: "Authorized Revoked Account", authorizationID: "auth_account_w2_revoked", priority: 8},
	}
	for index, item := range fixtures {
		_, err = db.ExecContext(ctx, `
			INSERT INTO juhe_business.accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id,
				protocol_code, protocol_version, name, type, status, credentials_encrypted,
				credential_fingerprint, credential_mask, concurrency_limit, priority,
				super_priority_enabled, fallback_enabled, client_compatibility, schedulable,
				authorization_instance_source_account_id, authorization_instance_authorization_id,
				authorization_instance_owner_system_account_id, created_at, updated_at
			) VALUES (
				$1, 'sys_w2_proxy_options', 'openai', 'profile_openai_openai_v1',
				'openai', 'v1', $2, 'api_key', 'active', 'encrypted-fixture',
				NULL, 'sk-test', 20, $3,
				false, false, 'openai_standard', true,
				CASE WHEN $4 = 'auth_account_w2_unbound' THEN 'acct_w2_unbound_source'
					WHEN $4 = 'auth_account_w2_revoked' THEN 'acct_w2_revoked_source'
					ELSE 'acct_w2_other'
				END,
				$4,
				'sys_w2_group_other', $5, $6
			)
		`, item.id, item.name, item.priority, item.authorizationID, now.Add(time.Duration(20+index)*time.Second), now.Add(time.Duration(20+index)*time.Second))
		if err != nil {
			t.Fatalf("insert W2 authorized account fixture %s: %v", item.id, err)
		}
		insertW2AccountNameSearchDocument(t, ctx, db, item.id, "sys_w2_proxy_options", item.name, now)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at
		) VALUES (
			'sys_w2_proxy_options', 'group_w2_default', 'acct_w2_authorized_other',
			'auth_account_w2_other', true, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 authorized account group binding: %v", err)
	}
}

func insertW2AccountTagsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_tags (
			id, system_account_id, name, created_at, updated_at
		) VALUES
			('tag_w2_main', 'sys_w2_proxy_options', '主力', $1, $2),
			('tag_w2_empty', 'sys_w2_proxy_options', '空标签', $1, $2),
			('tag_w2_my_empty', 'sys_w2_proxy_options', '我的空标签', $1, $2),
			('tag_w2_other', 'sys_w2_group_other', '其他用户标签', $1, $2)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 account tags: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_tag_bindings (
			account_id, tag_id, system_account_id, created_at
		) VALUES
			('acct_w2_alpha', 'tag_w2_main', 'sys_w2_proxy_options', $1),
			('acct_w2_authorized_other', 'tag_w2_main', 'sys_w2_proxy_options', $1),
			('acct_w2_other', 'tag_w2_other', 'sys_w2_group_other', $1)
	`, now)
	if err != nil {
		t.Fatalf("insert W2 account tag bindings: %v", err)
	}
}

func insertW2AccountNameSearchDocument(t *testing.T, ctx context.Context, db *sql.DB, accountID string, systemAccountID string, name string, now time.Time) {
	t.Helper()
	normalizedName := w2NormalizeAccountNameSearchText(name)
	if normalizedName == "" {
		return
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_name_search_documents (
			account_id, system_account_id, normalized_name, updated_at
		) VALUES (
			$1, $2, $3, $4
		)
		ON CONFLICT (account_id) DO UPDATE SET
			system_account_id = EXCLUDED.system_account_id,
			normalized_name = EXCLUDED.normalized_name,
			updated_at = EXCLUDED.updated_at
	`, accountID, systemAccountID, normalizedName, now)
	if err != nil {
		t.Fatalf("insert W2 account search document %s: %v", accountID, err)
	}
	for _, term := range w2AccountNameSearchDocumentTerms(normalizedName) {
		_, err = db.ExecContext(ctx, `
			INSERT INTO juhe_business.account_name_search_terms (
				account_id, system_account_id, term, created_at
			) VALUES (
				$1, $2, $3, $4
			)
			ON CONFLICT (account_id, term) DO NOTHING
		`, accountID, systemAccountID, term, now)
		if err != nil {
			t.Fatalf("insert W2 account search term %s/%s: %v", accountID, term, err)
		}
	}
}

func w2NormalizeAccountNameSearchText(value string) string {
	return strings.TrimSpace(norm.NFKC.String(value))
}

func w2AccountNameSearchDocumentTerms(normalizedName string) []string {
	terms := make([]string, 0)
	for length := 1; length <= 3; length++ {
		terms = append(terms, w2AccountNameSearchGrams(normalizedName, length)...)
	}
	return terms
}

func w2AccountNameSearchGrams(value string, gramLength int) []string {
	chars := []rune(value)
	if len(chars) < gramLength {
		return nil
	}
	seen := make(map[string]struct{}, len(chars))
	terms := make([]string, 0, len(chars))
	for index := 0; index+gramLength <= len(chars); index++ {
		term := string(chars[index : index+gramLength])
		if strings.TrimSpace(term) == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

func findAccountOption(options []managementaccounts.Option, id string) *managementaccounts.Option {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}

func findAccountTag(tags []managementaccounts.Tag, id string) *managementaccounts.Tag {
	for index := range tags {
		if tags[index].ID == id {
			return &tags[index]
		}
	}
	return nil
}

func readW2AccountTagBindings(t *testing.T, ctx context.Context, db *sql.DB, accountID string, systemAccountID string) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT account_tags.id, account_tags.name
		FROM juhe_business.account_tag_bindings AS bindings
		INNER JOIN juhe_business.account_tags AS account_tags
			ON account_tags.id = bindings.tag_id
			AND account_tags.system_account_id = bindings.system_account_id
		WHERE bindings.account_id = $1
			AND bindings.system_account_id = $2
	`, accountID, systemAccountID)
	if err != nil {
		t.Fatalf("read W2 account tag bindings: %v", err)
	}
	defer rows.Close()
	bindings := make(map[string]string)
	for rows.Next() {
		var id string
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("scan W2 account tag binding: %v", err)
		}
		bindings[name] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate W2 account tag bindings: %v", err)
	}
	return bindings
}

func sameAccountTagNames(tags []managementaccounts.Tag, want []string) bool {
	if len(tags) != len(want) {
		return false
	}
	seen := make(map[string]int, len(tags))
	for _, tag := range tags {
		seen[tag.Name]++
	}
	for _, name := range want {
		seen[name]--
		if seen[name] < 0 {
			return false
		}
	}
	return true
}

func timePtr(value time.Time) *time.Time {
	return &value
}

type w2OperationLogQueueStub struct {
	taskTypes []string
	options   []queue.EnqueueOptions
	logs      []port.OperationLogInput
	decodeErr error
}

func (s *w2OperationLogQueueStub) Enqueue(_ context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	s.taskTypes = append(s.taskTypes, taskType)
	s.options = append(s.options, opts)
	input, err := operationlogjob.DecodeWriteTaskPayload(payload)
	if err != nil {
		s.decodeErr = err
		return queue.TaskInfo{}, err
	}
	s.logs = append(s.logs, input)
	return queue.TaskInfo{ID: "task_w2_operation_log", Queue: opts.Queue, Type: taskType}, nil
}

func assertW2OperationLog(t *testing.T, logInput port.OperationLogInput, resourceID string, scopeSystemAccountID string, mode string) {
	t.Helper()
	if logInput.OperationKey != "accounts.update_tags" ||
		logInput.Module != "accounts" ||
		logInput.Action != "update_tags" ||
		logInput.ResourceType != "account" ||
		logInput.ResourceID != resourceID ||
		logInput.OperationScopeSystemAccountID != scopeSystemAccountID ||
		logInput.Mode != mode ||
		logInput.Summary == "" {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("operation log status = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 || logInput.Changes[0].Field != "tags" || logInput.Changes[0].Label != "标签" {
		t.Fatalf("operation log changes = %+v", logInput.Changes)
	}
}
