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
	"juhe-ai/backend-go/internal/modules/managementproxies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementAuthPostgresSmoke(t *testing.T) {
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
	sessionToken := "w2-management-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)
	mustChangeSessionToken := "w2-management-must-change-session-token"
	insertW2MustChangeSystemAccountFixture(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth_must_change", "sys_w2_must_change", mustChangeSessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	authContext, err := authenticator.AuthenticateCookie(ctx, "juhe_ai_session="+sessionToken)
	if err != nil {
		t.Fatalf("authenticate management cookie: %v", err)
	}
	if authContext.SystemAccountID != "sys_w2_proxy_options" || authContext.Role != "admin" {
		t.Fatalf("auth context = %+v", authContext)
	}

	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                        slog.Default(),
		ManagementAPIAuthMiddleware:   httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementCurrentUserHandler:  httpapi.NewManagementCurrentUserHandler(authenticator),
		ManagementProxyOptionsHandler: httpapi.NewManagementProxyOptionsHandler(managementproxies.NewService(store)),
	})

	currentUserReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	currentUserReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	currentUserRec := httptest.NewRecorder()
	router.ServeHTTP(currentUserRec, currentUserReq)
	if currentUserRec.Code != http.StatusOK {
		t.Fatalf("current user status = %d, body = %s", currentUserRec.Code, currentUserRec.Body.String())
	}
	var currentUserBody struct {
		Data struct {
			ID                 string `json:"id"`
			Username           string `json:"username"`
			Role               string `json:"role"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(currentUserRec.Body).Decode(&currentUserBody); err != nil {
		t.Fatalf("decode current user response: %v", err)
	}
	if currentUserBody.Data.ID != "sys_w2_proxy_options" || currentUserBody.Data.Username != "w2-proxy-options" || currentUserBody.Data.Role != "admin" || currentUserBody.Data.MustChangePassword {
		t.Fatalf("current user = %+v", currentUserBody.Data)
	}

	mustChangeReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	mustChangeReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeRec, mustChangeReq)
	if mustChangeRec.Code != http.StatusOK {
		t.Fatalf("must change current user status = %d, body = %s", mustChangeRec.Code, mustChangeRec.Body.String())
	}
	var mustChangeBody struct {
		Data struct {
			ID                 string `json:"id"`
			Role               string `json:"role"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mustChangeRec.Body).Decode(&mustChangeBody); err != nil {
		t.Fatalf("decode must change current user response: %v", err)
	}
	if mustChangeBody.Data.ID != "sys_w2_must_change" || mustChangeBody.Data.Role != "user" || !mustChangeBody.Data.MustChangePassword {
		t.Fatalf("must change current user = %+v", mustChangeBody.Data)
	}

	mustChangeProtectedReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	mustChangeProtectedReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeProtectedRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeProtectedRec, mustChangeProtectedReq)
	if mustChangeProtectedRec.Code != http.StatusForbidden {
		t.Fatalf("must change protected status = %d, want 403, body = %s", mustChangeProtectedRec.Code, mustChangeProtectedRec.Body.String())
	}
	var mustChangeProtectedBody map[string]string
	if err := json.NewDecoder(mustChangeProtectedRec.Body).Decode(&mustChangeProtectedBody); err != nil {
		t.Fatalf("decode must change protected response: %v", err)
	}
	if mustChangeProtectedBody["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("must change protected body = %+v", mustChangeProtectedBody)
	}

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?keyword=Al&limit=2", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []managementproxies.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 2 || body.Data[0].ID != "proxy_w2_alpha" || body.Data[1].ID != "proxy_w2_alpine" {
		t.Fatalf("proxy options = %+v", body.Data)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}
}

func insertW2ManagementSessionFixture(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth", "sys_w2_proxy_options", token, now)
}

func insertW2MustChangeSystemAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_must_change', 'w2-must-change', 'W2 Must Change', NULL, 'user', 'active', 'hash',
			true, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 must change system account: %v", err)
	}
}
