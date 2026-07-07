//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
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
	if len(groupFiltered) != 1 || groupFiltered[0].ID != "acct_w2_alpha" {
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
	if len(tagFiltered) != 1 || tagFiltered[0].ID != "acct_w2_alpha" {
		t.Fatalf("tag filtered options = %+v", tagFiltered)
	}

	tags, err := service.Tags(ctx, managementaccounts.TagListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("account tags: %v", err)
	}
	mainTag := findAccountTag(tags, "tag_w2_main")
	if mainTag == nil || mainTag.AccountCount != 1 {
		t.Fatalf("main tag = %+v, tags = %+v", mainTag, tags)
	}
	if findAccountTag(tags, "tag_w2_other") != nil {
		t.Fatalf("tags leaked other owner: %+v", tags)
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
	if findAccountOption(available, "acct_w2_unschedulable") != nil || findAccountOption(available, "acct_w2_cooling") != nil {
		t.Fatalf("available options included blocked accounts: %+v", available)
	}
	if findAccountOption(available, "acct_w2_alpha") == nil {
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

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                            slog.Default(),
		ManagementAPIAuthMiddleware:       httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAccountOptionsHandler:   httpapi.NewManagementAccountOptionsHandler(service),
		ManagementMyAccountOptionsHandler: httpapi.NewManagementMyAccountOptionsHandler(service),
		ManagementAccountTagsHandler:      httpapi.NewManagementAccountTagsHandler(service),
		ManagementMyAccountTagsHandler:    httpapi.NewManagementMyAccountTagsHandler(service),
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
	if tag := findAccountTag(tagBody.Data, "tag_w2_main"); tag == nil || tag.AccountCount != 1 {
		t.Fatalf("tag response missing account count: %+v", tagBody.Data)
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
	}

	_, err := db.ExecContext(ctx, `
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

func insertW2AccountTagsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_tags (
			id, system_account_id, name, created_at, updated_at
		) VALUES
			('tag_w2_main', 'sys_w2_proxy_options', '主力', $1, $2),
			('tag_w2_empty', 'sys_w2_proxy_options', '空标签', $1, $2),
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
			('acct_w2_other', 'tag_w2_other', 'sys_w2_group_other', $1)
	`, now)
	if err != nil {
		t.Fatalf("insert W2 account tag bindings: %v", err)
	}
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

func timePtr(value time.Time) *time.Time {
	return &value
}
