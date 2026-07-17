//go:build integration

package integration

import (
	"context"
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
	"juhe-ai/backend-go/internal/modules/managementproviders"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementProviderOptionsPostgresSmoke(t *testing.T) {
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
	sessionToken := "w2-management-provider-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementproviders.NewService(store)
	options, err := service.Options(ctx, managementproviders.OptionListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("list provider options: %v", err)
	}
	if len(options) == 0 {
		t.Fatal("provider options are empty")
	}
	gpt := findProviderOption(options, "gpt")
	if gpt == nil {
		t.Fatalf("gpt provider missing: %+v", options)
	}
	if gpt.DefaultProtocolProfileID != "profile_gpt_openai_v1" || len(gpt.ProtocolProfiles) == 0 {
		t.Fatalf("gpt provider = %+v", *gpt)
	}
	if gpt.DefaultHealthCheckModel != "gpt-5.6-sol" ||
		gpt.SystemDefaultHealthCheckModel != "" ||
		gpt.ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("gpt provider health check model contract = %+v", *gpt)
	}
	if !stringSliceContains(gpt.AccountTypes, "oauth") || !stringSliceContains(gpt.AccountTypes, "api_key") {
		t.Fatalf("gpt account types = %+v", gpt.AccountTypes)
	}
	if len(gpt.ProtocolProfiles[0].EndpointFamilies) == 0 {
		t.Fatalf("gpt profile endpoint families missing: %+v", gpt.ProtocolProfiles)
	}
	assertDeepSeekAndHybridProviderOptions(t, options)

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
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementProviderOptionsHandler: httpapi.NewManagementProviderOptionsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options?systemAccountId=sys_w2_proxy_options", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []managementproviders.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if findProviderOption(body.Data, "gpt") == nil {
		t.Fatalf("provider options response missing gpt: %+v", body.Data)
	}
	if provider := findProviderOption(body.Data, "gpt"); provider == nil ||
		provider.DefaultHealthCheckModel != "gpt-5.6-sol" ||
		provider.SystemDefaultHealthCheckModel != "" ||
		provider.ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("provider options response health check model contract = %+v", provider)
	}
	assertDeepSeekAndHybridProviderOptions(t, body.Data)
}

func assertDeepSeekAndHybridProviderOptions(t *testing.T, options []managementproviders.Option) {
	t.Helper()
	deepseek := findProviderOption(options, "deepseek")
	if deepseek == nil || deepseek.DefaultProtocolProfileID != "profile_deepseek_openai_v1" ||
		deepseek.ProtocolCode != "openai" || deepseek.BaseURL != "https://api.deepseek.com" ||
		!reflect.DeepEqual(deepseek.DefaultSupportedModels, []string{"deepseek-v4-flash", "deepseek-v4-pro"}) {
		t.Fatalf("deepseek provider contract = %+v", deepseek)
	}
	hybrid := findProviderOption(options, "hybrid")
	if hybrid == nil || hybrid.DefaultProtocolProfileID != "profile_hybrid_openai_chat_v1" || hybrid.ProtocolCode != "openai" {
		t.Fatalf("hybrid provider contract = %+v", hybrid)
	}
}

func findProviderOption(options []managementproviders.Option, code string) *managementproviders.Option {
	for index := range options {
		if options[index].Code == code {
			return &options[index]
		}
	}
	return nil
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
