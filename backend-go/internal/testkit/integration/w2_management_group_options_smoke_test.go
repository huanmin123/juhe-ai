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
	"juhe-ai/backend-go/internal/modules/managementgroups"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementGroupOptionsPostgresSmoke(t *testing.T) {
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
	insertW2GroupAuthorizationFixture(t, ctx, db, now)
	sessionToken := "w2-management-group-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementgroups.NewService(store)
	adminOptions, err := service.Options(ctx, managementgroups.OptionListInput{
		IncludeSystemAccountFields: true,
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("admin group options: %v", err)
	}
	if findGroupOption(adminOptions, "group_w2_other") == nil {
		t.Fatalf("admin all options should include other owner: %+v", adminOptions)
	}
	if count := countGroupOptions(adminOptions, "group_w2_other"); count != 1 {
		t.Fatalf("admin all options should not duplicate authorized group, count = %d, options = %+v", count, adminOptions)
	}
	if option := findGroupOption(adminOptions, "group_w2_other"); option.AccessType != "owner" || option.GroupAuthorizationID != "" {
		t.Fatalf("admin all options should expose other group as owner row only: %+v", option)
	}

	preferred, err := service.Options(ctx, managementgroups.OptionListInput{
		SystemAccountID:            "sys_w2_proxy_options",
		IncludeSystemAccountFields: true,
		ProviderCode:               "openai",
		PreferDefault:              true,
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("preferred group options: %v", err)
	}
	if len(preferred) == 0 || preferred[0].ID != "group_w2_default" {
		t.Fatalf("preferDefault options = %+v", preferred)
	}

	highConcurrency, err := service.Options(ctx, managementgroups.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		ProviderCode:    "gpt",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("high concurrency group options: %v", err)
	}
	if option := findGroupOption(highConcurrency, "group_w2_high"); option == nil || option.SchedulingPolicy["mode"] != "balanced_fast" {
		t.Fatalf("high concurrency option = %+v", highConcurrency)
	}

	selfOptions, err := service.Options(ctx, managementgroups.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		ProviderCode:    "openai",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("self group options: %v", err)
	}
	if option := findGroupOption(selfOptions, "group_w2_other"); option == nil ||
		option.AccessType != "authorized" ||
		option.GroupAuthorizationID != "auth_group_w2_other" ||
		option.AuthorizationStatus != "active" ||
		option.OwnerSystemAccountID != "sys_w2_group_other" ||
		option.OwnerSystemAccountName != "W2 Group Other" ||
		!option.Permissions.CanBindToAPIKey ||
		option.Permissions.CanAuthorize ||
		option.Permissions.CanManageAccounts {
		t.Fatalf("self options missing authorized group or permissions: %+v", selfOptions)
	}
	if findGroupOption(selfOptions, "group_w2_revoked") != nil {
		t.Fatalf("self options included revoked authorization: %+v", selfOptions)
	}
	manageableOptions, err := service.Options(ctx, managementgroups.OptionListInput{
		SystemAccountID:            "sys_w2_proxy_options",
		ProviderCode:               "openai",
		Limit:                      10,
		ManageableOnly:             true,
		IncludeSystemAccountFields: true,
	})
	if err != nil {
		t.Fatalf("manageable group options: %v", err)
	}
	if findGroupOption(manageableOptions, "group_w2_other") != nil {
		t.Fatalf("manageable options should exclude authorized group: %+v", manageableOptions)
	}
	if option := findGroupOption(selfOptions, "group_w2_disabled"); option == nil || option.SystemAccountID != "" {
		t.Fatalf("self options should include disabled owner group without owner fields: %+v", selfOptions)
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
		Logger:                          slog.Default(),
		ManagementAPIAuthMiddleware:     httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementGroupOptionsHandler:   httpapi.NewManagementGroupOptionsHandler(service),
		ManagementMyGroupOptionsHandler: httpapi.NewManagementMyGroupOptionsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/options?systemAccountId=sys_w2_proxy_options&providerCode=openai&preferDefault=true", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var adminBody struct {
		Data []managementgroups.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&adminBody); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if option := findGroupOption(adminBody.Data, "group_w2_default"); option == nil || option.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("admin response missing owner-scoped default group: %+v", adminBody.Data)
	}

	myReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/options?systemAccountId=sys_w2_other", nil)
	myReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myRec := httptest.NewRecorder()
	router.ServeHTTP(myRec, myReq)
	if myRec.Code != http.StatusOK {
		t.Fatalf("my status = %d, body = %s", myRec.Code, myRec.Body.String())
	}
	var myBody struct {
		Data []managementgroups.Option `json:"data"`
	}
	if err := json.NewDecoder(myRec.Body).Decode(&myBody); err != nil {
		t.Fatalf("decode my response: %v", err)
	}
	if option := findGroupOption(myBody.Data, "group_w2_other"); option == nil || option.AccessType != "authorized" || option.SystemAccountID != "" {
		t.Fatalf("my response should include authorized group without management owner fields: %+v", myBody.Data)
	}
	if option := findGroupOption(myBody.Data, "group_w2_default"); option == nil || option.SystemAccountID != "" || option.AccessType != "owner" {
		t.Fatalf("my response missing self group or leaked owner fields: %+v", myBody.Data)
	}
}

func insertW2GroupOptionsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_group_other', 'w2-group-other', 'W2 Group Other', NULL, 'admin', 'active', 'hash',
			false, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 group other system account: %v", err)
	}

	fixtures := []struct {
		id              string
		systemAccountID string
		name            string
		providerCode    string
		enabled         bool
		isDefault       bool
		groupType       string
		policy          *string
		updatedAt       time.Time
	}{
		{id: "group_w2_default", systemAccountID: "sys_w2_proxy_options", name: "默认分组", providerCode: "openai", enabled: true, isDefault: true, groupType: "personal", updatedAt: now.Add(4 * time.Second)},
		{id: "group_w2_disabled", systemAccountID: "sys_w2_proxy_options", name: "停用分组", providerCode: "openai", enabled: false, groupType: "personal", updatedAt: now.Add(3 * time.Second)},
		{id: "group_w2_high", systemAccountID: "sys_w2_proxy_options", name: "高并发分组", providerCode: "gpt", enabled: true, groupType: "high_concurrency", policy: w2GroupStringPtr(w2HighConcurrencyPolicyJSON()), updatedAt: now.Add(2 * time.Second)},
		{id: "group_w2_other", systemAccountID: "sys_w2_group_other", name: "其他账户分组", providerCode: "openai", enabled: true, groupType: "personal", updatedAt: now.Add(time.Second)},
		{id: "group_w2_revoked", systemAccountID: "sys_w2_group_other", name: "已回收授权分组", providerCode: "openai", enabled: true, groupType: "personal", updatedAt: now},
	}
	for _, item := range fixtures {
		_, err = db.ExecContext(ctx, `
			INSERT INTO juhe_business.groups (
				id, system_account_id, name, provider_code, description, enabled, is_default,
				group_type, scheduling_policy_json, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10
			)
		`, item.id, item.systemAccountID, item.name, item.providerCode, item.enabled, item.isDefault, item.groupType, item.policy, now, item.updatedAt)
		if err != nil {
			t.Fatalf("insert W2 group fixture %s: %v", item.id, err)
		}
	}
}

func insertW2GroupAuthorizationFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
			remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
			revoked_reason, updated_at
		) VALUES
			(
				'auth_group_w2_other', 'group', 'group_w2_other', 'sys_w2_group_other', 'sys_w2_proxy_options',
				'use', 'active', 'manual', NULL, $1, $2,
				NULL, $3, '{"daily":{"limit":100}}', 'sys_w2_group_other', $4, NULL, NULL,
				NULL, $5
			),
			(
				'auth_group_w2_revoked', 'group', 'group_w2_revoked', 'sys_w2_group_other', 'sys_w2_proxy_options',
				'use', 'revoked', 'manual', NULL, $1, $2,
				NULL, NULL, NULL, 'sys_w2_group_other', $4, 'sys_w2_group_other', $5,
				'fixture', $5
			)
	`, now, now, expiresAt, now, now)
	if err != nil {
		t.Fatalf("insert W2 group authorization fixture: %v", err)
	}
}

func findGroupOption(options []managementgroups.Option, id string) *managementgroups.Option {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}

func countGroupOptions(options []managementgroups.Option, id string) int {
	count := 0
	for index := range options {
		if options[index].ID == id {
			count++
		}
	}
	return count
}

func w2HighConcurrencyPolicyJSON() string {
	return `{
		"mode":"balanced_fast",
		"defaultSoftConcurrency":5,
		"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,
		"breakAffinityOnSoftLimit":true,
		"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,
		"firstOutputSlowThresholdMs":15000,
		"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,
		"maxQueueWaitMs":60000,
		"maxQueueSize":1000,
		"perApiKeyQueueLimit":1000,
		"clientIpConcurrencyLimit":0,
		"clientIpConcurrencyOverflowMode":"reject",
		"imageLaneMaxConcurrency":0
	}`
}

func w2GroupStringPtr(value string) *string {
	return &value
}
