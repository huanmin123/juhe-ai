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
		ManagementProxyOptionsHandler: httpapi.NewManagementProxyOptionsHandler(managementproxies.NewService(store)),
	})

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
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_sessions (
			id, system_account_id, token_hash, expires_at, created_at, last_seen_at
		) VALUES (
			'sess_w2_management_auth', 'sys_w2_proxy_options', $1, $2, $3, $4
		)
	`, managementauth.HashSessionToken(token), now.Add(time.Hour), now, now)
	if err != nil {
		t.Fatalf("insert W2 management session: %v", err)
	}
}
