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
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementRouteStrategiesPostgresSmoke(t *testing.T) {
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
	insertW2RouteStrategyOptionsFixture(t, ctx, db, now)
	sessionToken := "w2-management-route-strategy-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementroutestrategies.NewService(store)
	adminOptions, err := service.Options(ctx, managementroutestrategies.OptionListInput{
		IncludeSystemAccountFields: true,
		ActiveOnly:                 false,
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("admin route strategy options: %v", err)
	}
	if findRouteStrategyOption(adminOptions, "route_w2_other") == nil {
		t.Fatalf("admin all options should include other owner: %+v", adminOptions)
	}
	if findRouteStrategyOption(adminOptions, "route_w2_disabled") == nil {
		t.Fatalf("activeOnly=false should include disabled strategy: %+v", adminOptions)
	}

	selfOptions, err := service.Options(ctx, managementroutestrategies.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		ActiveOnly:      true,
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("self route strategy options: %v", err)
	}
	if findRouteStrategyOption(selfOptions, "route_w2_disabled") != nil || findRouteStrategyOption(selfOptions, "route_w2_other") != nil {
		t.Fatalf("self active options leaked disabled or other owner: %+v", selfOptions)
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
		Logger:                                  slog.Default(),
		ManagementAPIAuthMiddleware:             httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementRouteStrategyOptionsHandler:   httpapi.NewManagementRouteStrategyOptionsHandler(service),
		ManagementMyRouteStrategyOptionsHandler: httpapi.NewManagementMyRouteStrategyOptionsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies/options?systemAccountId=sys_w2_proxy_options&activeOnly=false", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var adminBody struct {
		Data []managementroutestrategies.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&adminBody); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if option := findRouteStrategyOption(adminBody.Data, "route_w2_disabled"); option == nil || option.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("admin response missing owner-scoped disabled strategy: %+v", adminBody.Data)
	}

	myReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-route-strategies/options?systemAccountId=sys_w2_other", nil)
	myReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myRec := httptest.NewRecorder()
	router.ServeHTTP(myRec, myReq)
	if myRec.Code != http.StatusOK {
		t.Fatalf("my status = %d, body = %s", myRec.Code, myRec.Body.String())
	}
	var myBody struct {
		Data []managementroutestrategies.Option `json:"data"`
	}
	if err := json.NewDecoder(myRec.Body).Decode(&myBody); err != nil {
		t.Fatalf("decode my response: %v", err)
	}
	if findRouteStrategyOption(myBody.Data, "route_w2_other") != nil {
		t.Fatalf("my response leaked query systemAccountId owner: %+v", myBody.Data)
	}
	if option := findRouteStrategyOption(myBody.Data, "route_w2_default"); option == nil || option.SystemAccountID != "" {
		t.Fatalf("my response missing self strategy or leaked owner fields: %+v", myBody.Data)
	}
}

func insertW2RouteStrategyOptionsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_other', 'w2-other', 'W2 Other', NULL, 'admin', 'active', 'hash',
			false, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 other system account: %v", err)
	}

	fixtures := []struct {
		id              string
		systemAccountID string
		name            string
		status          string
		isDefault       bool
		updatedAt       time.Time
	}{
		{id: "route_w2_default", systemAccountID: "sys_w2_proxy_options", name: "默认路由", status: "active", isDefault: true, updatedAt: now.Add(3 * time.Second)},
		{id: "route_w2_disabled", systemAccountID: "sys_w2_proxy_options", name: "停用路由", status: "disabled", updatedAt: now.Add(2 * time.Second)},
		{id: "route_w2_other", systemAccountID: "sys_w2_other", name: "其他账户路由", status: "active", updatedAt: now.Add(time.Second)},
	}
	for _, item := range fixtures {
		_, err = db.ExecContext(ctx, `
			INSERT INTO juhe_business.route_strategies (
				id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
			) VALUES (
				$1, $2, $3, NULL, 'normal', $4, $5, NULL, $6, $7
			)
		`, item.id, item.systemAccountID, item.name, item.status, item.isDefault, now, item.updatedAt)
		if err != nil {
			t.Fatalf("insert W2 route strategy fixture %s: %v", item.id, err)
		}
	}
}

func findRouteStrategyOption(options []managementroutestrategies.Option, id string) *managementroutestrategies.Option {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}
