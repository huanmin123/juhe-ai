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
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizationoptions"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementAuthorizationGranteeAccountsPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2AuthorizationGranteeAccountsFixture(t, ctx, db, now)
	adminSessionToken := "w2-management-authorization-options-admin-session-token"
	userSessionToken := "w2-management-authorization-options-user-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, adminSessionToken, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_authorization_options_user", "sys_w2_auth_option_user", userSessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementauthorizationoptions.NewService(store)
	all, err := service.GranteeAccounts(ctx, managementauthorizationoptions.PrincipalOptionListInput{Limit: 50})
	if err != nil {
		t.Fatalf("all grantee account options: %v", err)
	}
	if findAuthorizationGranteeAccountOption(all, "sys_w2_auth_option_disabled") == nil {
		t.Fatalf("disabled grantee account should stay visible: %+v", all)
	}

	prefixed, err := service.GranteeAccounts(ctx, managementauthorizationoptions.PrincipalOptionListInput{Keyword: "Readonly", Limit: 10})
	if err != nil {
		t.Fatalf("prefixed grantee account options: %v", err)
	}
	if len(prefixed) != 1 || prefixed[0].ID != "sys_w2_auth_option_readonly" {
		t.Fatalf("prefixed options = %+v", prefixed)
	}

	middle, err := service.GranteeAccounts(ctx, managementauthorizationoptions.PrincipalOptionListInput{Keyword: "nly", Limit: 10})
	if err != nil {
		t.Fatalf("middle keyword grantee account options: %v", err)
	}
	if len(middle) != 0 {
		t.Fatalf("middle keyword should not match prefix-only options: %+v", middle)
	}

	percent, err := service.GranteeAccounts(ctx, managementauthorizationoptions.PrincipalOptionListInput{Keyword: "Percent%", Limit: 10})
	if err != nil {
		t.Fatalf("percent literal grantee account options: %v", err)
	}
	if len(percent) != 1 || percent[0].ID != "sys_w2_auth_option_percent" {
		t.Fatalf("percent literal options = %+v", percent)
	}

	ids, err := service.GranteeAccounts(ctx, managementauthorizationoptions.PrincipalOptionListInput{
		IDs:   []string{"sys_w2_auth_option_disabled", "sys_w2_auth_option_readonly"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ids grantee account options: %v", err)
	}
	if findAuthorizationGranteeAccountOption(ids, "sys_w2_auth_option_disabled") == nil || findAuthorizationGranteeAccountOption(ids, "sys_w2_auth_option_readonly") == nil || findAuthorizationGranteeAccountOption(ids, "sys_w2_auth_option_percent") != nil {
		t.Fatalf("ids options = %+v", ids)
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
		Logger:                      slog.Default(),
		ManagementAPIAuthMiddleware: httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAuthorizationGranteeAccountsHandler:   httpapi.NewManagementAuthorizationGranteeAccountsHandler(service),
		ManagementMyAuthorizationGranteeAccountsHandler: httpapi.NewManagementMyAuthorizationGranteeAccountsHandler(service),
	})

	adminReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts?keyword=Readonly&limit=1", nil)
	adminReq.Header.Set("Cookie", "juhe_ai_session="+adminSessionToken)
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	assertAuthorizationGranteeAccountsResponse(t, adminRec, "sys_w2_auth_option_readonly")

	selfReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-accounts?keyword=Readonly&limit=1", nil)
	selfReq.Header.Set("Cookie", "juhe_ai_session="+userSessionToken)
	selfRec := httptest.NewRecorder()
	router.ServeHTTP(selfRec, selfReq)
	assertAuthorizationGranteeAccountsResponse(t, selfRec, "sys_w2_auth_option_readonly")

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts?limit=1", nil)
	forbiddenReq.Header.Set("Cookie", "juhe_ai_session="+userSessionToken)
	forbiddenRec := httptest.NewRecorder()
	router.ServeHTTP(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("ordinary user admin route status = %d, want 403", forbiddenRec.Code)
	}
}

func TestW2ManagementAuthorizationGranteeGroupsPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2AuthorizationGranteeAccountsFixture(t, ctx, db, now)
	insertW2AuthorizationGranteeGroupsFixture(t, ctx, db, now)
	adminSessionToken := "w2-management-authorization-groups-admin-session-token"
	userSessionToken := "w2-management-authorization-groups-user-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, adminSessionToken, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_authorization_groups_user", "sys_w2_auth_option_user", userSessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementauthorizationoptions.NewService(store)
	all, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID:     "sys_w2_auth_option_user",
		IncludeSystemAccountFields: true,
		Limit:                      50,
		PreferDefault:              true,
	})
	if err != nil {
		t.Fatalf("all grantee group options: %v", err)
	}
	if len(all) == 0 || all[0].ID != "grp_w2_auth_default" {
		t.Fatalf("default group should be first when preferDefault=true: %+v", all)
	}
	if findAuthorizationGranteeGroupOption(all, "grp_w2_auth_disabled") != nil || findAuthorizationGranteeGroupOption(all, "grp_w2_auth_other_user") != nil {
		t.Fatalf("disabled or other owner group leaked: %+v", all)
	}
	if all[0].SystemAccountID != "sys_w2_auth_option_user" || all[0].SystemAccountName == "" || all[0].OwnerSystemAccountName == "" {
		t.Fatalf("admin group fields = %+v", all[0])
	}

	withoutDefaultPreference, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		Limit:                  50,
		PreferDefault:          false,
	})
	if err != nil {
		t.Fatalf("grantee group options without default preference: %v", err)
	}
	if len(withoutDefaultPreference) == 0 || withoutDefaultPreference[0].ID != "grp_w2_auth_regular" {
		t.Fatalf("updated_at order should win when preferDefault=false: %+v", withoutDefaultPreference)
	}

	prefixed, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		Keyword:                "Readonly",
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("prefixed grantee group options: %v", err)
	}
	if len(prefixed) != 1 || prefixed[0].ID != "grp_w2_auth_regular" {
		t.Fatalf("prefixed group options = %+v", prefixed)
	}

	middle, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		Keyword:                "only",
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("middle keyword grantee group options: %v", err)
	}
	if len(middle) != 0 {
		t.Fatalf("middle keyword should not match prefix-only group options: %+v", middle)
	}

	percent, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		Keyword:                "Percent%",
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("percent literal grantee group options: %v", err)
	}
	if len(percent) != 1 || percent[0].ID != "grp_w2_auth_percent" {
		t.Fatalf("percent literal group options = %+v", percent)
	}

	provider, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		ProviderCode:           "gpt",
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("provider grantee group options: %v", err)
	}
	if len(provider) != 1 || provider[0].ID != "grp_w2_auth_gpt" {
		t.Fatalf("provider group options = %+v", provider)
	}

	ids, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_user",
		IDs:                    []string{"grp_w2_auth_regular", "grp_w2_auth_gpt", "grp_w2_auth_other_user"},
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("ids grantee group options: %v", err)
	}
	if findAuthorizationGranteeGroupOption(ids, "grp_w2_auth_regular") == nil || findAuthorizationGranteeGroupOption(ids, "grp_w2_auth_gpt") == nil || findAuthorizationGranteeGroupOption(ids, "grp_w2_auth_other_user") != nil {
		t.Fatalf("ids group options = %+v", ids)
	}

	disabledGrantee, err := service.GranteeGroups(ctx, managementauthorizationoptions.GranteeGroupOptionListInput{
		GranteeSystemAccountID: "sys_w2_auth_option_disabled",
		Limit:                  10,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("disabled grantee group options: %v", err)
	}
	if len(disabledGrantee) != 0 {
		t.Fatalf("disabled grantee should return empty groups: %+v", disabledGrantee)
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
		Logger:                      slog.Default(),
		ManagementAPIAuthMiddleware: httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAuthorizationGranteeGroupsHandler:   httpapi.NewManagementAuthorizationGranteeGroupsHandler(service),
		ManagementMyAuthorizationGranteeGroupsHandler: httpapi.NewManagementMyAuthorizationGranteeGroupsHandler(service),
	})

	adminReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=sys_w2_auth_option_user&keyword=Readonly&providerCode=openai&limit=1", nil)
	adminReq.Header.Set("Cookie", "juhe_ai_session="+adminSessionToken)
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	assertAuthorizationGranteeGroupsResponse(t, adminRec, "grp_w2_auth_regular", true)

	selfReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=sys_w2_auth_option_readonly&keyword=ReadonlyOwner&providerCode=openai&limit=1", nil)
	selfReq.Header.Set("Cookie", "juhe_ai_session="+userSessionToken)
	selfRec := httptest.NewRecorder()
	router.ServeHTTP(selfRec, selfReq)
	assertAuthorizationGranteeGroupsResponse(t, selfRec, "grp_w2_auth_readonly_default", false)

	missingReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-groups?providerCode=openai", nil)
	missingReq.Header.Set("Cookie", "juhe_ai_session="+userSessionToken)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("missing grantee status = %d, want 400", missingRec.Code)
	}

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=sys_w2_auth_option_user&limit=1", nil)
	forbiddenReq.Header.Set("Cookie", "juhe_ai_session="+userSessionToken)
	forbiddenRec := httptest.NewRecorder()
	router.ServeHTTP(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("ordinary user admin route status = %d, want 403", forbiddenRec.Code)
	}
}

func insertW2AuthorizationGranteeAccountsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	fixtures := []struct {
		id          string
		username    string
		displayName string
		status      string
		role        string
	}{
		{id: "sys_w2_auth_option_readonly", username: "readonly-auth-option", displayName: "Readonly Grantee", status: "active", role: "admin"},
		{id: "sys_w2_auth_option_disabled", username: "disabled-auth-option", displayName: "Disabled Grantee", status: "disabled", role: "user"},
		{id: "sys_w2_auth_option_percent", username: "percent-auth-option", displayName: "Percent% Grantee", status: "active", role: "user"},
		{id: "sys_w2_auth_option_user", username: "auth-option-user", displayName: "Authorization Option User", status: "active", role: "user"},
	}
	for index, item := range fixtures {
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.system_accounts (
				id, username, display_name, description, role, status, password_hash,
				must_change_password, image_generation_enabled, created_at, updated_at
			) VALUES (
				$1, $2, $3, NULL, $4, $5, 'hash',
				false, false, $6, $7
			)
		`, item.id, item.username, item.displayName, item.role, item.status, now.Add(time.Duration(index)*time.Second), now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("insert W2 authorization grantee account fixture %s: %v", item.id, err)
		}
	}
}

func insertW2AuthorizationGranteeGroupsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	fixtures := []struct {
		id              string
		systemAccountID string
		name            string
		providerCode    string
		enabled         bool
		isDefault       bool
		updatedAt       time.Time
	}{
		{id: "grp_w2_auth_default", systemAccountID: "sys_w2_auth_option_user", name: "Default Target", providerCode: "openai", enabled: true, isDefault: true, updatedAt: now.Add(1 * time.Second)},
		{id: "grp_w2_auth_regular", systemAccountID: "sys_w2_auth_option_user", name: "Readonly Target", providerCode: "openai", enabled: true, updatedAt: now.Add(5 * time.Second)},
		{id: "grp_w2_auth_percent", systemAccountID: "sys_w2_auth_option_user", name: "Percent% Target", providerCode: "openai", enabled: true, updatedAt: now.Add(4 * time.Second)},
		{id: "grp_w2_auth_gpt", systemAccountID: "sys_w2_auth_option_user", name: "GPT Target", providerCode: "gpt", enabled: true, updatedAt: now.Add(3 * time.Second)},
		{id: "grp_w2_auth_disabled", systemAccountID: "sys_w2_auth_option_user", name: "Disabled Target", providerCode: "openai", enabled: false, updatedAt: now.Add(6 * time.Second)},
		{id: "grp_w2_auth_other_user", systemAccountID: "sys_w2_proxy_options", name: "Other User Target", providerCode: "openai", enabled: true, updatedAt: now.Add(7 * time.Second)},
		{id: "grp_w2_auth_readonly_default", systemAccountID: "sys_w2_auth_option_readonly", name: "ReadonlyOwner Target", providerCode: "openai", enabled: true, isDefault: true, updatedAt: now.Add(8 * time.Second)},
	}
	for _, item := range fixtures {
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.groups (
				id, system_account_id, name, provider_code, description, enabled, is_default,
				group_type, scheduling_policy_json, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, NULL, $5, $6,
				'personal', NULL, $7, $8
			)
		`, item.id, item.systemAccountID, item.name, item.providerCode, item.enabled, item.isDefault, now, item.updatedAt)
		if err != nil {
			t.Fatalf("insert W2 authorization grantee group fixture %s: %v", item.id, err)
		}
	}
}

func insertW2ManagementSessionForAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, sessionID string, accountID string, token string, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_sessions (
			id, system_account_id, token_hash, expires_at, created_at, last_seen_at
		) VALUES (
			$1, $2, $3, $4, $5, $6
		)
	`, sessionID, accountID, managementauth.HashSessionToken(token), now.Add(time.Hour), now, now)
	if err != nil {
		t.Fatalf("insert W2 management session for %s: %v", accountID, err)
	}
}

func assertAuthorizationGranteeGroupsResponse(t *testing.T, rec *httptest.ResponseRecorder, wantID string, wantAdminFields bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string][]map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body["data"]) != 1 {
		t.Fatalf("authorization grantee groups response = %+v", body)
	}
	item := body["data"][0]
	if item["id"] != wantID || item["name"] == "" || item["providerCode"] == "" || item["ownerSystemAccountId"] == "" || item["accessType"] != "owner" {
		t.Fatalf("authorization grantee groups response item = %+v", item)
	}
	permissions, ok := item["permissions"].(map[string]any)
	if !ok || permissions["canUse"] != true || permissions["canEdit"] != false || permissions["canBindToApiKey"] != false {
		t.Fatalf("authorization grantee groups permissions = %+v", item["permissions"])
	}
	if wantAdminFields {
		if item["systemAccountId"] == "" || item["systemAccountName"] == "" || item["ownerSystemAccountName"] == "" {
			t.Fatalf("admin grantee group fields = %+v", item)
		}
		return
	}
	if _, exists := item["systemAccountId"]; exists {
		t.Fatalf("self grantee group leaked systemAccountId: %+v", item)
	}
	if _, exists := item["systemAccountName"]; exists {
		t.Fatalf("self grantee group leaked systemAccountName: %+v", item)
	}
	if _, exists := item["ownerSystemAccountName"]; exists {
		t.Fatalf("self grantee group leaked ownerSystemAccountName: %+v", item)
	}
}

func assertAuthorizationGranteeAccountsResponse(t *testing.T, rec *httptest.ResponseRecorder, wantID string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []managementauthorizationoptions.GranteeAccountOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != wantID || body.Data[0].Username == "" || body.Data[0].DisplayName == "" || body.Data[0].Status != "active" {
		t.Fatalf("authorization grantee accounts response = %+v", body.Data)
	}
}

func findAuthorizationGranteeAccountOption(options []managementauthorizationoptions.GranteeAccountOption, id string) *managementauthorizationoptions.GranteeAccountOption {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}

func findAuthorizationGranteeGroupOption(options []managementauthorizationoptions.GranteeGroupOption, id string) *managementauthorizationoptions.GranteeGroupOption {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}
