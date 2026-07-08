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
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/modules/managementproviders"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementProviderModelsPostgresSmoke(t *testing.T) {
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
	insertW2ProviderModelFixture(t, ctx, db, now)
	sessionToken := "w2-management-provider-model-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementprovidermodels.NewService(store)
	providerService := managementproviders.NewService(store)
	models, err := service.Models(ctx, managementprovidermodels.ModelListInput{
		ProviderCode:    "gpt",
		SystemAccountID: "sys_w2_proxy_options",
		IncludeInactive: true,
		IncludeUnpriced: true,
	})
	if err != nil {
		t.Fatalf("list provider models: %v", err)
	}
	if findW2ProviderModel(models, "gpt-5.5") == nil {
		t.Fatalf("seeded gpt-5.5 model missing: %+v", models)
	}
	if personal := findW2ProviderModel(models, "w2-personal-model"); personal == nil || personal.Scope != "personal" || personal.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("personal model missing or wrong scope: %+v", personal)
	}

	defaultModels, err := service.Models(ctx, managementprovidermodels.ModelListInput{
		ProviderCode:    "gpt",
		SystemAccountID: "sys_w2_proxy_options",
	})
	if err != nil {
		t.Fatalf("list default provider models: %v", err)
	}
	if findW2ProviderModel(defaultModels, "w2-unpriced-model") != nil {
		t.Fatalf("unpriced model should be hidden by default: %+v", defaultModels)
	}

	options, err := service.ModelOptions(ctx, managementprovidermodels.ModelOptionListInput{
		SystemAccountID: "sys_w2_proxy_options",
		Protocol:        "openai",
	})
	if err != nil {
		t.Fatalf("list provider model options: %v", err)
	}
	if findW2ProviderModelOption(options, "gpt", "gpt-5.5") == nil {
		t.Fatalf("gpt-5.5 model option missing: %+v", options)
	}

	saved, err := service.SetDefaultTestModel(ctx, managementprovidermodels.DefaultTestModelInput{
		ProviderCode:    "gpt",
		SystemAccountID: "sys_w2_proxy_options",
		Model:           "w2-personal-model",
	})
	if err != nil {
		t.Fatalf("set default test model: %v", err)
	}
	if saved.ProviderCode != "gpt" || saved.DefaultTestModel != "w2-personal-model" {
		t.Fatalf("saved default test model = %+v", saved)
	}
	providerOptions, err := providerService.Options(ctx, managementproviders.OptionListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("list provider options after default model set: %v", err)
	}
	if provider := findProviderOption(providerOptions, "gpt"); provider == nil || provider.DefaultTestModel != "w2-personal-model" {
		t.Fatalf("provider default model preference missing: %+v", provider)
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
		Logger:                                    slog.Default(),
		ManagementAPIAuthMiddleware:               httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:          httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProviderModelOptionsHandler:     httpapi.NewManagementProviderModelOptionsHandler(service),
		ManagementProviderModelsHandler:           httpapi.NewManagementProviderModelsHandler(service),
		ManagementProviderDefaultTestModelHandler: httpapi.NewManagementProviderDefaultTestModelHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_w2_proxy_options&includeInactive=true&includeUnpriced=true", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var modelsBody struct {
		Data []managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&modelsBody); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if findW2ProviderModel(modelsBody.Data, "w2-personal-model") == nil {
		t.Fatalf("HTTP provider models response missing personal model: %+v", modelsBody.Data)
	}

	optionsReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/models/options?protocol=openai&systemAccountId=sys_w2_proxy_options", nil)
	optionsReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	optionsRec := httptest.NewRecorder()
	router.ServeHTTP(optionsRec, optionsReq)
	if optionsRec.Code != http.StatusOK {
		t.Fatalf("options status = %d, body = %s", optionsRec.Code, optionsRec.Body.String())
	}
	var optionsBody struct {
		Data []managementprovidermodels.ModelOption `json:"data"`
	}
	if err := json.NewDecoder(optionsRec.Body).Decode(&optionsBody); err != nil {
		t.Fatalf("decode model options response: %v", err)
	}
	if findW2ProviderModelOption(optionsBody.Data, "gpt", "w2-personal-model") == nil {
		t.Fatalf("HTTP provider model options response missing personal model: %+v", optionsBody.Data)
	}

	putReq := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model?systemAccountId=sys_w2_proxy_options", strings.NewReader(`{"model":"gpt-5.5"}`))
	putReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("default test model status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	var putBody struct {
		Data managementprovidermodels.DefaultTestModelResult `json:"data"`
	}
	if err := json.NewDecoder(putRec.Body).Decode(&putBody); err != nil {
		t.Fatalf("decode default test model response: %v", err)
	}
	if putBody.Data.DefaultTestModel != "gpt-5.5" {
		t.Fatalf("default test model response = %+v", putBody.Data)
	}
	providerOptions, err = providerService.Options(ctx, managementproviders.OptionListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("list provider options after HTTP default model set: %v", err)
	}
	if provider := findProviderOption(providerOptions, "gpt"); provider == nil || provider.DefaultTestModel != "gpt-5.5" {
		t.Fatalf("provider default model preference was not updated by HTTP PUT: %+v", provider)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/gpt/models", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}
}

func insertW2ProviderModelFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	fixtures := []struct {
		id              string
		model           string
		scope           string
		systemAccountID *string
		status          string
		price           *float64
	}{
		{id: "custom_model_w2_global", model: "w2-global-model", scope: "global", status: "active", price: float64Ptr(2)},
		{id: "custom_model_w2_personal", model: "w2-personal-model", scope: "personal", systemAccountID: stringPtr("sys_w2_proxy_options"), status: "active", price: float64Ptr(3)},
		{id: "custom_model_w2_unpriced", model: "w2-unpriced-model", scope: "personal", systemAccountID: stringPtr("sys_w2_proxy_options"), status: "active"},
	}
	for _, item := range fixtures {
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.custom_provider_models (
				id, provider_code, model, scope, system_account_id, status, mode,
				supported_api_protocols_json, input_usd_per_1m, currency, created_by,
				created_at, updated_at
			) VALUES ($1, 'gpt', $2, $3, $4, $5, 'chat', '["chat_completions"]', $6, 'USD', 'sys_w2_proxy_options', $7, $8)
		`, item.id, item.model, item.scope, item.systemAccountID, item.status, item.price, now, now)
		if err != nil {
			t.Fatalf("insert provider model fixture %s: %v", item.id, err)
		}
	}
}

func findW2ProviderModel(items []managementprovidermodels.ModelCatalogItem, model string) *managementprovidermodels.ModelCatalogItem {
	for index := range items {
		if items[index].Model == model {
			return &items[index]
		}
	}
	return nil
}

func findW2ProviderModelOption(items []managementprovidermodels.ModelOption, providerCode string, model string) *managementprovidermodels.ModelOption {
	for index := range items {
		if items[index].ProviderCode == providerCode && items[index].Model == model {
			return &items[index]
		}
	}
	return nil
}

func stringPtr(value string) *string {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}
