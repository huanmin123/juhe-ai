//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestW2ManagementGroupAccountOptionsPostgresSmoke(t *testing.T) {
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
	insertW2GroupAccountOptionsExtraBindingsFixture(t, ctx, db, now)
	sessionToken := "w2-management-group-account-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementgroups.NewService(store)
	adminOptions, err := service.AccountOptions(ctx, managementgroups.OptionListInput{
		SystemAccountID:            "sys_w2_proxy_options",
		IncludeSystemAccountFields: true,
		ProviderCode:               "openai",
		PreferDefault:              true,
		Limit:                      10,
	})
	if err != nil {
		t.Fatalf("admin group account options: %v", err)
	}
	defaultOption := findGroupAccountOption(adminOptions, "group_w2_default")
	if defaultOption == nil {
		t.Fatalf("admin options missing default group: %+v", adminOptions)
	}
	if defaultOption.SystemAccountID != "sys_w2_proxy_options" || defaultOption.AccessType != "owner" {
		t.Fatalf("admin default option leaked wrong owner fields: %+v", defaultOption)
	}
	if !reflect.DeepEqual(defaultOption.AccountIDs, []string{"acct_w2_alpha"}) {
		t.Fatalf("default accountIds = %#v, want only active owner binding", defaultOption.AccountIDs)
	}

	selfOptions, err := service.AccountOptions(ctx, managementgroups.OptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		ProviderCode:    "openai",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("self group account options: %v", err)
	}
	if findGroupAccountOption(selfOptions, "group_w2_other") != nil {
		t.Fatalf("self options leaked other owner: %+v", selfOptions)
	}
	if option := findGroupAccountOption(selfOptions, "group_w2_default"); option == nil || option.SystemAccountID != "" || len(option.AccountIDs) != 1 {
		t.Fatalf("self options missing account ids or leaked owner fields: %+v", selfOptions)
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
		Logger:                                 slog.Default(),
		ManagementAPIAuthMiddleware:            httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementGroupAccountOptionsHandler:   httpapi.NewManagementGroupAccountOptionsHandler(service),
		ManagementMyGroupAccountOptionsHandler: httpapi.NewManagementMyGroupAccountOptionsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/account-options?systemAccountId=sys_w2_proxy_options&providerCode=openai&preferDefault=true", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var adminBody struct {
		Data []managementgroups.AccountOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&adminBody); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if option := findGroupAccountOption(adminBody.Data, "group_w2_default"); option == nil || !reflect.DeepEqual(option.AccountIDs, []string{"acct_w2_alpha"}) {
		t.Fatalf("admin response missing filtered account ids: %+v", adminBody.Data)
	}

	myReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/account-options?systemAccountId=sys_w2_group_other", nil)
	myReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	myRec := httptest.NewRecorder()
	router.ServeHTTP(myRec, myReq)
	if myRec.Code != http.StatusOK {
		t.Fatalf("my status = %d, body = %s", myRec.Code, myRec.Body.String())
	}
	var myBody struct {
		Data []managementgroups.AccountOption `json:"data"`
	}
	if err := json.NewDecoder(myRec.Body).Decode(&myBody); err != nil {
		t.Fatalf("decode my response: %v", err)
	}
	if findGroupAccountOption(myBody.Data, "group_w2_other") != nil {
		t.Fatalf("my response leaked query systemAccountId owner: %+v", myBody.Data)
	}
	if option := findGroupAccountOption(myBody.Data, "group_w2_default"); option == nil || option.SystemAccountID != "" || !reflect.DeepEqual(option.AccountIDs, []string{"acct_w2_alpha"}) {
		t.Fatalf("my response missing self group account ids or leaked owner fields: %+v", myBody.Data)
	}
}

func insertW2GroupAccountOptionsExtraBindingsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET deleted_at = $1, updated_at = $2
		WHERE id = 'acct_w2_cooling'
	`, now, now)
	if err != nil {
		t.Fatalf("mark W2 group account option deleted account: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at
		) VALUES
			('sys_w2_proxy_options', 'group_w2_default', 'acct_w2_percent', NULL, false, $1, $2),
			('sys_w2_proxy_options', 'group_w2_default', 'acct_w2_cooling', NULL, true, $1, $2),
			('sys_w2_proxy_options', 'group_w2_default', 'acct_w2_unschedulable', 'auth_not_migrated', true, $1, $2)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 group account option extra bindings: %v", err)
	}
}

func findGroupAccountOption(options []managementgroups.AccountOption, id string) *managementgroups.AccountOption {
	for index := range options {
		if options[index].ID == id {
			return &options[index]
		}
	}
	return nil
}
