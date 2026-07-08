//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementSystemAccountOptionsPostgresSmoke(t *testing.T) {
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
	insertW2SystemAccountOptionsFixture(t, ctx, db, now)
	sessionToken := "w2-management-system-account-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementsystemaccounts.NewService(store)
	all, err := service.Options(ctx, managementsystemaccounts.OptionListInput{Limit: 50})
	if err != nil {
		t.Fatalf("all system account options: %v", err)
	}
	if findSystemAccountOption(all, "sys_w2_option_disabled") == nil {
		t.Fatalf("disabled system account should stay visible: %+v", all)
	}

	prefixed, err := service.Options(ctx, managementsystemaccounts.OptionListInput{Keyword: "Readonly", Limit: 10})
	if err != nil {
		t.Fatalf("prefixed system account options: %v", err)
	}
	if len(prefixed) != 1 || prefixed[0].ID != "sys_w2_option_readonly" {
		t.Fatalf("prefixed options = %+v", prefixed)
	}

	middle, err := service.Options(ctx, managementsystemaccounts.OptionListInput{Keyword: "nly", Limit: 10})
	if err != nil {
		t.Fatalf("middle keyword system account options: %v", err)
	}
	if len(middle) != 0 {
		t.Fatalf("middle keyword should not match prefix-only options: %+v", middle)
	}

	percent, err := service.Options(ctx, managementsystemaccounts.OptionListInput{Keyword: "Percent%", Limit: 10})
	if err != nil {
		t.Fatalf("percent literal system account options: %v", err)
	}
	if len(percent) != 1 || percent[0].ID != "sys_w2_option_percent" {
		t.Fatalf("percent literal options = %+v", percent)
	}

	ids, err := service.Options(ctx, managementsystemaccounts.OptionListInput{
		IDs:   []string{"sys_w2_option_disabled", "sys_w2_option_readonly"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ids system account options: %v", err)
	}
	if findSystemAccountOption(ids, "sys_w2_option_disabled") == nil || findSystemAccountOption(ids, "sys_w2_option_readonly") == nil || findSystemAccountOption(ids, "sys_w2_option_percent") != nil {
		t.Fatalf("ids options = %+v", ids)
	}

	listResult, err := service.List(ctx, managementsystemaccounts.ListInput{Keyword: "READONLY", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("list system accounts: %v", err)
	}
	if len(listResult.Items) != 1 || listResult.Items[0].ID != "sys_w2_option_readonly" || listResult.Items[0].Username != "readonly" || listResult.Items[0].Role != "admin" {
		t.Fatalf("list result = %+v", listResult)
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
		Logger:                                slog.Default(),
		ManagementAPIAuthMiddleware:           httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementSystemAccountsHandler:       httpapi.NewManagementSystemAccountsHandler(service),
		ManagementSystemAccountOptionsHandler: httpapi.NewManagementSystemAccountOptionsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts?keyword=readonly&page=1&pageSize=1", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hash") {
		t.Fatalf("system account list leaked password hash: %s", rec.Body.String())
	}
	var listBody struct {
		Data managementsystemaccounts.ListResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.Data.Items) != 1 || listBody.Data.Items[0].ID != "sys_w2_option_readonly" || listBody.Data.Items[0].DisplayName == "" || listBody.Data.Page != 1 || listBody.Data.PageSize != 1 {
		t.Fatalf("system account list response = %+v", listBody.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/system-accounts/options?keyword=Readonly&limit=1", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("options status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []managementsystemaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "sys_w2_option_readonly" || body.Data[0].Username == "" || body.Data[0].DisplayName == "" || body.Data[0].Status != "active" {
		t.Fatalf("system account options response = %+v", body.Data)
	}
}

func insertW2SystemAccountOptionsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	fixtures := []struct {
		id          string
		username    string
		displayName string
		status      string
		role        string
	}{
		{id: "sys_w2_option_readonly", username: "readonly", displayName: "Readonly Admin", status: "active", role: "admin"},
		{id: "sys_w2_option_disabled", username: "disabled-user", displayName: "Disabled User", status: "disabled", role: "user"},
		{id: "sys_w2_option_percent", username: "percent-user", displayName: "Percent% Literal", status: "active", role: "user"},
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
			t.Fatalf("insert W2 system account option fixture %s: %v", item.id, err)
		}
	}
}

func findSystemAccountOption(options []managementsystemaccounts.Option, id string) *managementsystemaccounts.Option {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}
