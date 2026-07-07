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
