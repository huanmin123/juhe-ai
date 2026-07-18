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
	"juhe-ai/backend-go/internal/store/port"
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
	assertW2ProviderModelCatalogSnapshot(t, ctx, db)
	priorityFlexTiers := []string{"priority", "flex"}
	assertW2ProviderModelRequestCapabilitiesRow(t, ctx, db, "gpt-5.6-sol", priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "low", "v2")
	assertW2ProviderModelTierPricingRow(t, ctx, db, "gpt-5.6-sol")
	assertW2ProviderModelRequestCapabilitiesRow(t, ctx, db, "gpt-5.6-terra", priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "medium", "v2")
	assertW2ProviderModelRequestCapabilitiesRow(t, ctx, db, "gpt-5.6-luna", priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max"}, "medium", "")
	assertW2ProviderModelRequestCapabilitiesRow(t, ctx, db, "o3", priorityFlexTiers, []string{}, "", []string{}, "", "")
	assertW2ProviderModelRequestCapabilitiesRow(t, ctx, db, "o4-mini", priorityFlexTiers, []string{}, "", []string{}, "", "")

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
	if findW2ProviderModel(models, "gpt-5.6-sol") == nil {
		t.Fatalf("seeded gpt-5.6-sol model missing: %+v", models)
	}
	builtInModel := findW2ProviderModel(models, "gpt-5.6-sol")
	if builtInModel.ID == "" || builtInModel.InputUSDPer1M == nil || builtInModel.OutputUSDPer1M == nil {
		t.Fatalf("seeded gpt-5.6-sol model missing provider_model_catalog ID: %+v", builtInModel)
	}
	originalInputPrice := *builtInModel.InputUSDPer1M
	originalOutputPrice := *builtInModel.OutputUSDPer1M
	inputPrice := 41.0
	_, found, err := store.UpdateManagementBuiltInProviderModelPrices(ctx, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: builtInModel.ID, ProviderCode: builtInModel.ProviderCode,
		InputUSDPer1M: port.ManagementProviderModelOptionalFloat{Present: true, Value: &inputPrice},
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error { return nil })
	if err != nil || !found {
		t.Fatalf("update built-in input price: found=%v err=%v", found, err)
	}
	outputPrice := 42.0
	_, found, err = store.UpdateManagementBuiltInProviderModelPrices(ctx, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: builtInModel.ID, ProviderCode: builtInModel.ProviderCode,
		OutputUSDPer1M: port.ManagementProviderModelOptionalFloat{Present: true, Value: &outputPrice},
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error { return nil })
	if err != nil || !found {
		t.Fatalf("update built-in output price: found=%v err=%v", found, err)
	}
	var actualInputPrice sql.NullFloat64
	var actualOutputPrice sql.NullFloat64
	if err := db.QueryRowContext(ctx, `
		SELECT input_usd_per_1m, output_usd_per_1m
		FROM juhe_business.provider_model_catalog
		WHERE id = $1
	`, builtInModel.ID).Scan(&actualInputPrice, &actualOutputPrice); err != nil {
		t.Fatalf("query sequential built-in prices: %v", err)
	}
	if !actualInputPrice.Valid || actualInputPrice.Float64 != inputPrice || !actualOutputPrice.Valid || actualOutputPrice.Float64 != outputPrice {
		t.Fatalf("sequential built-in prices = input:%+v output:%+v", actualInputPrice, actualOutputPrice)
	}
	_, found, err = store.UpdateManagementBuiltInProviderModelPrices(ctx, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: builtInModel.ID, ProviderCode: builtInModel.ProviderCode,
		InputUSDPer1M: port.ManagementProviderModelOptionalFloat{Present: true},
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error { return nil })
	if err != nil || !found {
		t.Fatalf("clear built-in input price: found=%v err=%v", found, err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT input_usd_per_1m, output_usd_per_1m
		FROM juhe_business.provider_model_catalog
		WHERE id = $1
	`, builtInModel.ID).Scan(&actualInputPrice, &actualOutputPrice); err != nil {
		t.Fatalf("query cleared built-in price: %v", err)
	}
	if actualInputPrice.Valid || !actualOutputPrice.Valid || actualOutputPrice.Float64 != outputPrice {
		t.Fatalf("cleared built-in prices = input:%+v output:%+v", actualInputPrice, actualOutputPrice)
	}
	restored, found, err := store.UpdateManagementBuiltInProviderModelPrices(ctx, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: builtInModel.ID, ProviderCode: builtInModel.ProviderCode,
		InputUSDPer1M:  port.ManagementProviderModelOptionalFloat{Present: true, Value: &originalInputPrice},
		OutputUSDPer1M: port.ManagementProviderModelOptionalFloat{Present: true, Value: &originalOutputPrice},
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error { return nil })
	if err != nil || !found {
		t.Fatalf("restore built-in prices: found=%v err=%v", found, err)
	}
	if restored.Before.InputUSDPer1M != nil ||
		restored.Before.OutputUSDPer1M == nil || *restored.Before.OutputUSDPer1M != outputPrice {
		t.Fatalf("pre-restore RETURNING prices = %+v", restored.Before)
	}
	if restored.After.InputUSDPer1M == nil || *restored.After.InputUSDPer1M != originalInputPrice ||
		restored.After.OutputUSDPer1M == nil || *restored.After.OutputUSDPer1M != originalOutputPrice {
		t.Fatalf("restored RETURNING prices = %+v", restored)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT input_usd_per_1m, output_usd_per_1m
		FROM juhe_business.provider_model_catalog
		WHERE id = $1
	`, builtInModel.ID).Scan(&actualInputPrice, &actualOutputPrice); err != nil {
		t.Fatalf("query restored built-in prices: %v", err)
	}
	if !actualInputPrice.Valid || actualInputPrice.Float64 != originalInputPrice ||
		!actualOutputPrice.Valid || actualOutputPrice.Float64 != originalOutputPrice {
		t.Fatalf("restored built-in prices = input:%+v output:%+v", actualInputPrice, actualOutputPrice)
	}
	assertW2ProviderModelPricing(t, findW2ProviderModel(models, "gpt-5.6-sol"), "2026-06-26", 922000, 0.5, 6.25)
	assertW2ProviderModelTierPricing(t, findW2ProviderModel(models, "gpt-5.6-sol"))
	assertW2ProviderModelPricing(t, findW2ProviderModel(models, "gpt-5.6-terra"), "2026-06-26", 922000, 0.25, 3.125)
	assertW2ProviderModelPricing(t, findW2ProviderModel(models, "gpt-5.6-luna"), "2026-06-26", 922000, 0.1, 1.25)
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "gpt-5.6-sol"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "low", "v2")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "gpt-5.6-terra"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "medium", "v2")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "gpt-5.6-luna"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max"}, "medium", "")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "o3"), priorityFlexTiers, []string{}, "", []string{}, "", "")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "o4-mini"), priorityFlexTiers, []string{}, "", []string{}, "", "")
	if personal := findW2ProviderModel(models, "w2-personal-model"); personal == nil || personal.Scope != "personal" || personal.SystemAccountID != "sys_w2_proxy_options" {
		t.Fatalf("personal model missing or wrong scope: %+v", personal)
	}
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(models, "w2-personal-model"), []string{"priority", "flex"}, []string{"low", "high"}, "high", []string{}, "", "")

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
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "gpt-5.6-sol"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "gpt-5.6-terra"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "gpt-5.6-luna"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "o3"), priorityFlexTiers, []string{}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "o4-mini"), priorityFlexTiers, []string{}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(options, "gpt", "w2-personal-model"), []string{"priority", "flex"}, []string{"low", "high"}, "high")

	saved, err := service.SetDefaultHealthCheckModel(ctx, managementprovidermodels.DefaultHealthCheckModelInput{
		ProviderCode:         "gpt",
		ActorSystemAccountID: "sys_w2_proxy_options",
		ActorRole:            "user",
		Model:                "w2-personal-model",
	})
	if err != nil {
		t.Fatalf("set default health check model: %v", err)
	}
	if saved.ProviderCode != "gpt" || saved.DefaultHealthCheckModel != "w2-personal-model" {
		t.Fatalf("saved default health check model = %+v", saved)
	}
	providerOptions, err := providerService.Options(ctx, managementproviders.OptionListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("list provider options after default model set: %v", err)
	}
	if provider := findProviderOption(providerOptions, "gpt"); provider == nil ||
		provider.DefaultHealthCheckModel != "w2-personal-model" ||
		provider.SystemDefaultHealthCheckModel != "" {
		t.Fatalf("provider default health check model preference missing: %+v", provider)
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
		Logger:                                           slog.Default(),
		ManagementAPIAuthMiddleware:                      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:                 httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProviderModelOptionsHandler:            httpapi.NewManagementProviderModelOptionsHandler(service),
		ManagementProviderModelsHandler:                  httpapi.NewManagementProviderModelsHandler(service),
		ManagementProviderDefaultHealthCheckModelHandler: httpapi.NewManagementProviderDefaultHealthCheckModelHandler(service),
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
	modelsResponseBody := append([]byte(nil), rec.Body.Bytes()...)
	if err := json.Unmarshal(modelsResponseBody, &modelsBody); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if findW2ProviderModel(modelsBody.Data, "w2-personal-model") == nil {
		t.Fatalf("HTTP provider models response missing personal model: %+v", modelsBody.Data)
	}
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(modelsBody.Data, "gpt-5.6-sol"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "low", "v2")
	assertW2ProviderModelTierPricing(t, findW2ProviderModel(modelsBody.Data, "gpt-5.6-sol"))
	assertW2ProviderModelWireTierPricing(t, modelsResponseBody, "gpt-5.6-sol")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(modelsBody.Data, "gpt-5.6-terra"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "medium", "v2")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(modelsBody.Data, "gpt-5.6-luna"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "", []string{"low", "medium", "high", "xhigh", "max"}, "medium", "")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(modelsBody.Data, "o3"), priorityFlexTiers, []string{}, "", []string{}, "", "")
	assertW2ProviderModelRequestCapabilities(t, findW2ProviderModel(modelsBody.Data, "o4-mini"), priorityFlexTiers, []string{}, "", []string{}, "", "")

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
	optionsResponseBody := optionsRec.Body.Bytes()
	if err := json.Unmarshal(optionsResponseBody, &optionsBody); err != nil {
		t.Fatalf("decode model options response: %v", err)
	}
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "gpt-5.6-sol"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "gpt-5.6-terra"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "gpt-5.6-luna"), priorityFlexTiers, []string{"none", "low", "medium", "high", "xhigh", "max"}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "o3"), priorityFlexTiers, []string{}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "o4-mini"), priorityFlexTiers, []string{}, "")
	assertW2ProviderModelOptionRequestCapabilities(t, findW2ProviderModelOption(optionsBody.Data, "gpt", "w2-personal-model"), []string{"priority", "flex"}, []string{"low", "high"}, "high")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "gpt-5.6-sol", "supportedServiceTiers", "supportedReasoningEfforts")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "gpt-5.6-terra", "supportedServiceTiers", "supportedReasoningEfforts")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "gpt-5.6-luna", "supportedServiceTiers", "supportedReasoningEfforts")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "o3", "supportedServiceTiers")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "o4-mini", "supportedServiceTiers")
	assertW2ProviderModelOptionWireFields(t, optionsResponseBody, "gpt", "w2-personal-model", "supportedServiceTiers", "supportedReasoningEfforts", "defaultReasoningEffort")

	putReq := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?systemAccountId=sys_w2_proxy_options", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	putReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("default health check model status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	var putBody struct {
		Data managementprovidermodels.DefaultHealthCheckModelResult `json:"data"`
	}
	if err := json.NewDecoder(putRec.Body).Decode(&putBody); err != nil {
		t.Fatalf("decode default health check model response: %v", err)
	}
	if putBody.Data.DefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("default health check model response = %+v", putBody.Data)
	}
	providerOptions, err = providerService.Options(ctx, managementproviders.OptionListInput{SystemAccountID: "sys_w2_proxy_options"})
	if err != nil {
		t.Fatalf("list provider options after HTTP default model set: %v", err)
	}
	if provider := findProviderOption(providerOptions, "gpt"); provider == nil ||
		provider.DefaultHealthCheckModel != "w2-personal-model" ||
		provider.SystemDefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("personal preference must override HTTP-updated system default: %+v", provider)
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
		id                     string
		model                  string
		scope                  string
		systemAccountID        *string
		status                 string
		price                  *float64
		serviceTiers           string
		reasoningEfforts       string
		defaultReasoningEffort *string
	}{
		{id: "custom_model_w2_global", model: "w2-global-model", scope: "global", status: "active", price: float64Ptr(2)},
		{id: "custom_model_w2_personal", model: "w2-personal-model", scope: "personal", systemAccountID: stringPtr("sys_w2_proxy_options"), status: "active", price: float64Ptr(3), serviceTiers: `["priority","flex"]`, reasoningEfforts: `["low","high"]`, defaultReasoningEffort: stringPtr("high")},
		{id: "custom_model_w2_unpriced", model: "w2-unpriced-model", scope: "personal", systemAccountID: stringPtr("sys_w2_proxy_options"), status: "active"},
	}
	for _, item := range fixtures {
		serviceTiers := item.serviceTiers
		if serviceTiers == "" {
			serviceTiers = "[]"
		}
		reasoningEfforts := item.reasoningEfforts
		if reasoningEfforts == "" {
			reasoningEfforts = "[]"
		}
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.custom_provider_models (
				id, provider_code, model, scope, system_account_id, status, mode,
				supported_api_protocols_json, supported_service_tiers_json,
				supported_reasoning_efforts_json, default_reasoning_effort,
				input_usd_per_1m, currency, created_by,
				created_at, updated_at
			) VALUES ($1, 'gpt', $2, $3, $4, $5, 'text', '["chat_completions"]', $6, $7, $8, $9, 'USD', 'sys_w2_proxy_options', $10, $11)
		`, item.id, item.model, item.scope, item.systemAccountID, item.status, serviceTiers, reasoningEfforts, item.defaultReasoningEffort, item.price, now, now)
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

func assertW2ProviderModelPricing(t *testing.T, item *managementprovidermodels.ModelCatalogItem, releaseDate string, maxInputTokens int, cachedInputUSDPer1M float64, cacheWriteUSDPer1M float64) {
	t.Helper()
	if item == nil {
		t.Fatalf("provider model missing")
	}
	if item.ReleaseDate != releaseDate {
		t.Fatalf("%s release date = %q, want %q", item.Model, item.ReleaseDate, releaseDate)
	}
	if item.MaxInputTokens == nil || *item.MaxInputTokens != maxInputTokens {
		t.Fatalf("%s max input tokens = %v, want %d", item.Model, item.MaxInputTokens, maxInputTokens)
	}
	if item.CachedInputUSDPer1M == nil || *item.CachedInputUSDPer1M != cachedInputUSDPer1M {
		t.Fatalf("%s cached input price = %v, want %v", item.Model, item.CachedInputUSDPer1M, cachedInputUSDPer1M)
	}
	if item.CacheWriteUSDPer1M == nil || *item.CacheWriteUSDPer1M != cacheWriteUSDPer1M {
		t.Fatalf("%s cache write price = %v, want %v", item.Model, item.CacheWriteUSDPer1M, cacheWriteUSDPer1M)
	}
}

func assertW2ProviderModelTierPricing(t *testing.T, item *managementprovidermodels.ModelCatalogItem) {
	t.Helper()
	if item == nil {
		t.Fatalf("provider model missing")
	}
	priority := item.ServiceTierPrices["priority"]
	flex := item.ServiceTierPrices["flex"]
	if priority.InputUSDPer1M == nil || *priority.InputUSDPer1M != 10 ||
		priority.OutputUSDPer1M == nil || *priority.OutputUSDPer1M != 60 ||
		priority.CachedInputUSDPer1M == nil || *priority.CachedInputUSDPer1M != 1 ||
		priority.CacheWriteUSDPer1M == nil || *priority.CacheWriteUSDPer1M != 12.5 ||
		priority.CacheWrite1hUSDPer1M != nil ||
		flex.InputUSDPer1M == nil || *flex.InputUSDPer1M != 2.5 ||
		flex.OutputUSDPer1M == nil || *flex.OutputUSDPer1M != 15 ||
		flex.CachedInputUSDPer1M == nil || *flex.CachedInputUSDPer1M != 0.25 ||
		flex.CacheWriteUSDPer1M == nil || *flex.CacheWriteUSDPer1M != 3.125 ||
		flex.CacheWrite1hUSDPer1M != nil {
		t.Fatalf("%s tier pricing metadata = %+v", item.Model, item)
	}
	if item.LongContextInputTokenThreshold == nil || *item.LongContextInputTokenThreshold != 272000 ||
		item.LongContextInputCostMultiplier == nil || *item.LongContextInputCostMultiplier != 2 ||
		item.LongContextOutputCostMultiplier == nil || *item.LongContextOutputCostMultiplier != 1.5 {
		t.Fatalf("%s long-context metadata = %+v", item.Model, item)
	}
}

func assertW2ProviderModelWireTierPricing(t *testing.T, body []byte, model string) {
	t.Helper()
	var payload struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode provider model wire payload: %v", err)
	}
	for _, item := range payload.Data {
		var actualModel string
		if err := json.Unmarshal(item["model"], &actualModel); err != nil || actualModel != model {
			continue
		}
		var prices map[string]port.ManagementProviderModelPriceSet
		if err := json.Unmarshal(item["serviceTierPrices"], &prices); err != nil {
			t.Fatalf("decode %s wire tier prices: %v", model, err)
		}
		priority := prices["priority"]
		flex := prices["flex"]
		if priority.InputUSDPer1M == nil || *priority.InputUSDPer1M != 10 ||
			priority.OutputUSDPer1M == nil || *priority.OutputUSDPer1M != 60 ||
			priority.CachedInputUSDPer1M == nil || *priority.CachedInputUSDPer1M != 1 ||
			priority.CacheWriteUSDPer1M == nil || *priority.CacheWriteUSDPer1M != 12.5 ||
			priority.CacheWrite1hUSDPer1M != nil ||
			flex.InputUSDPer1M == nil || *flex.InputUSDPer1M != 2.5 ||
			flex.OutputUSDPer1M == nil || *flex.OutputUSDPer1M != 15 ||
			flex.CachedInputUSDPer1M == nil || *flex.CachedInputUSDPer1M != 0.25 ||
			flex.CacheWriteUSDPer1M == nil || *flex.CacheWriteUSDPer1M != 3.125 ||
			flex.CacheWrite1hUSDPer1M != nil {
			t.Fatalf("%s wire tier pricing = %+v", model, prices)
		}
		for field, want := range map[string]string{
			"longContextInputTokenThreshold":  "272000",
			"longContextInputCostMultiplier":  "2",
			"longContextOutputCostMultiplier": "1.5",
		} {
			if string(item[field]) != want {
				t.Fatalf("%s wire field %s = %s, want %s", model, field, item[field], want)
			}
		}
		for _, field := range []string{"priorityInputUsdPer1M", "priorityCacheWrite1hUsdPer1M", "flexInputUsdPer1M", "flexCacheWrite1hUsdPer1M"} {
			if _, ok := item[field]; ok {
				t.Fatalf("%s wire field %s must be omitted from unified pricing payload", model, field)
			}
		}
		return
	}
	t.Fatalf("provider model %s missing from wire payload", model)
}

func assertW2ProviderModelRequestCapabilities(
	t *testing.T,
	item *managementprovidermodels.ModelCatalogItem,
	serviceTiers []string,
	reasoningEfforts []string,
	defaultReasoningEffort string,
	codexReasoningLevels []string,
	codexDefaultReasoningLevel string,
	codexMultiAgentVersion string,
) {
	t.Helper()
	if item == nil {
		t.Fatalf("provider model missing")
	}
	if strings.Join(item.SupportedServiceTiers, ",") != strings.Join(serviceTiers, ",") {
		t.Fatalf("%s service tiers = %v, want %v", item.Model, item.SupportedServiceTiers, serviceTiers)
	}
	if strings.Join(item.SupportedReasoningEfforts, ",") != strings.Join(reasoningEfforts, ",") {
		t.Fatalf("%s reasoning efforts = %v, want %v", item.Model, item.SupportedReasoningEfforts, reasoningEfforts)
	}
	if item.DefaultReasoningEffort != defaultReasoningEffort {
		t.Fatalf("%s default reasoning effort = %q, want %q", item.Model, item.DefaultReasoningEffort, defaultReasoningEffort)
	}
	if item.SupportsServiceTier != (len(serviceTiers) > 0) {
		t.Fatalf("%s supportsServiceTier = %v, want %v", item.Model, item.SupportsServiceTier, len(serviceTiers) > 0)
	}
	if strings.Join(item.CodexSupportedReasoningLevels, ",") != strings.Join(codexReasoningLevels, ",") {
		t.Fatalf("%s Codex reasoning levels = %v, want %v", item.Model, item.CodexSupportedReasoningLevels, codexReasoningLevels)
	}
	if item.CodexDefaultReasoningLevel != codexDefaultReasoningLevel {
		t.Fatalf("%s Codex default reasoning level = %q, want %q", item.Model, item.CodexDefaultReasoningLevel, codexDefaultReasoningLevel)
	}
	if item.CodexMultiAgentVersion != codexMultiAgentVersion {
		t.Fatalf("%s Codex multi-agent version = %q, want %q", item.Model, item.CodexMultiAgentVersion, codexMultiAgentVersion)
	}
}

func assertW2ProviderModelOptionRequestCapabilities(
	t *testing.T,
	item *managementprovidermodels.ModelOption,
	serviceTiers []string,
	reasoningEfforts []string,
	defaultReasoningEffort string,
) {
	t.Helper()
	if item == nil {
		t.Fatalf("provider model option missing")
	}
	if strings.Join(item.SupportedServiceTiers, ",") != strings.Join(serviceTiers, ",") {
		t.Fatalf("%s option service tiers = %v, want %v", item.Model, item.SupportedServiceTiers, serviceTiers)
	}
	if strings.Join(item.SupportedReasoningEfforts, ",") != strings.Join(reasoningEfforts, ",") {
		t.Fatalf("%s option reasoning efforts = %v, want %v", item.Model, item.SupportedReasoningEfforts, reasoningEfforts)
	}
	if item.DefaultReasoningEffort != defaultReasoningEffort {
		t.Fatalf("%s option default reasoning effort = %q, want %q", item.Model, item.DefaultReasoningEffort, defaultReasoningEffort)
	}
}

func assertW2ProviderModelOptionWireFields(t *testing.T, body []byte, providerCode string, model string, fields ...string) {
	t.Helper()
	var payload struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode provider model option wire payload: %v", err)
	}
	for _, item := range payload.Data {
		var actualProviderCode string
		var actualModel string
		if err := json.Unmarshal(item["providerCode"], &actualProviderCode); err != nil {
			continue
		}
		if err := json.Unmarshal(item["model"], &actualModel); err != nil {
			continue
		}
		if actualProviderCode != providerCode || actualModel != model {
			continue
		}
		for _, field := range fields {
			if _, ok := item[field]; !ok {
				t.Fatalf("%s/%s option wire field %q missing: %s", providerCode, model, field, string(body))
			}
		}
		return
	}
	t.Fatalf("%s/%s option missing from wire payload: %s", providerCode, model, string(body))
}

func assertW2ProviderModelCatalogSnapshot(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	type counts struct {
		total   int64
		visible int64
	}
	// Total includes historical disabled rows retained by sync migrations; visible follows the current snapshot.
	wantCounts := map[string]counts{
		"gpt":       {total: 81, visible: 81},
		"anthropic": {total: 43, visible: 17},
		"gemini":    {total: 10, visible: 9},
		"deepseek":  {total: 6, visible: 4},
		"glm":       {total: 18, visible: 17},
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			provider_code,
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE catalog_visible)::bigint
		FROM juhe_business.provider_model_catalog
		WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm')
		GROUP BY provider_code
	`)
	if err != nil {
		t.Fatalf("query provider model catalog snapshot counts: %v", err)
	}
	defer rows.Close()

	actualCounts := make(map[string]counts, len(wantCounts))
	for rows.Next() {
		var providerCode string
		var actual counts
		if err := rows.Scan(&providerCode, &actual.total, &actual.visible); err != nil {
			t.Fatalf("scan provider model catalog snapshot counts: %v", err)
		}
		actualCounts[providerCode] = actual
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate provider model catalog snapshot counts: %v", err)
	}
	if len(actualCounts) != len(wantCounts) {
		t.Fatalf("provider model catalog snapshot provider count = %d, want %d: %+v", len(actualCounts), len(wantCounts), actualCounts)
	}
	for providerCode, want := range wantCounts {
		if actualCounts[providerCode] != want {
			t.Fatalf("%s provider model catalog counts = %+v, want %+v", providerCode, actualCounts[providerCode], want)
		}
	}

	wantModels := []struct {
		providerCode string
		model        string
	}{
		{providerCode: "gpt", model: "gpt-4.1"},
		{providerCode: "gpt", model: "o3"},
		{providerCode: "anthropic", model: "claude-sonnet-4-6"},
		{providerCode: "gemini", model: "gemini-2.5-flash-lite"},
		{providerCode: "deepseek", model: "deepseek-chat"},
		{providerCode: "glm", model: "glm-4.7"},
	}
	for _, want := range wantModels {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM juhe_business.provider_model_catalog
				WHERE provider_code = $1 AND model = $2
			)
		`, want.providerCode, want.model).Scan(&exists); err != nil {
			t.Fatalf("query provider model %s/%s: %v", want.providerCode, want.model, err)
		}
		if !exists {
			t.Fatalf("provider model %s/%s missing after migrations", want.providerCode, want.model)
		}
	}
}

func assertW2ProviderModelRequestCapabilitiesRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	model string,
	serviceTiers []string,
	reasoningEfforts []string,
	defaultReasoningEffort string,
	codexReasoningLevels []string,
	codexDefaultReasoningLevel string,
	codexMultiAgentVersion string,
) {
	t.Helper()
	var serviceTiersJSON string
	var reasoningEffortsJSON string
	var defaultEffort sql.NullString
	var codexReasoningLevelsJSON string
	var codexDefaultLevel sql.NullString
	var codexMultiAgent sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT
			supported_service_tiers_json,
			supported_reasoning_efforts_json,
			default_reasoning_effort,
			codex_supported_reasoning_levels_json,
			codex_default_reasoning_level,
			codex_multi_agent_version
		FROM juhe_business.provider_model_catalog
		WHERE provider_code = 'gpt' AND model = $1
	`, model).Scan(
		&serviceTiersJSON,
		&reasoningEffortsJSON,
		&defaultEffort,
		&codexReasoningLevelsJSON,
		&codexDefaultLevel,
		&codexMultiAgent,
	); err != nil {
		t.Fatalf("query %s request capabilities: %v", model, err)
	}

	var actualServiceTiers []string
	if err := json.Unmarshal([]byte(serviceTiersJSON), &actualServiceTiers); err != nil {
		t.Fatalf("decode %s service tiers: %v", model, err)
	}
	var actualReasoningEfforts []string
	if err := json.Unmarshal([]byte(reasoningEffortsJSON), &actualReasoningEfforts); err != nil {
		t.Fatalf("decode %s reasoning efforts: %v", model, err)
	}
	var actualCodexReasoningLevels []string
	if err := json.Unmarshal([]byte(codexReasoningLevelsJSON), &actualCodexReasoningLevels); err != nil {
		t.Fatalf("decode %s Codex reasoning levels: %v", model, err)
	}

	if strings.Join(actualServiceTiers, ",") != strings.Join(serviceTiers, ",") {
		t.Fatalf("%s PG service tiers = %v, want %v", model, actualServiceTiers, serviceTiers)
	}
	if strings.Join(actualReasoningEfforts, ",") != strings.Join(reasoningEfforts, ",") {
		t.Fatalf("%s PG reasoning efforts = %v, want %v", model, actualReasoningEfforts, reasoningEfforts)
	}
	if defaultEffort.String != defaultReasoningEffort {
		t.Fatalf("%s PG default reasoning effort = %q, want %q", model, defaultEffort.String, defaultReasoningEffort)
	}
	if strings.Join(actualCodexReasoningLevels, ",") != strings.Join(codexReasoningLevels, ",") {
		t.Fatalf("%s PG Codex reasoning levels = %v, want %v", model, actualCodexReasoningLevels, codexReasoningLevels)
	}
	if codexDefaultLevel.String != codexDefaultReasoningLevel {
		t.Fatalf("%s PG Codex default reasoning level = %q, want %q", model, codexDefaultLevel.String, codexDefaultReasoningLevel)
	}
	if codexMultiAgent.String != codexMultiAgentVersion {
		t.Fatalf("%s PG Codex multi-agent version = %q, want %q", model, codexMultiAgent.String, codexMultiAgentVersion)
	}
}

func assertW2ProviderModelTierPricingRow(t *testing.T, ctx context.Context, db *sql.DB, model string) {
	t.Helper()
	var serviceTierPricesJSON string
	var longContextThreshold sql.NullInt64
	var longContextInputMultiplier, longContextOutputMultiplier sql.NullFloat64
	if err := db.QueryRowContext(ctx, `
		SELECT
			service_tier_prices_json,
			long_context_input_token_threshold,
			long_context_input_cost_multiplier,
			long_context_output_cost_multiplier
		FROM juhe_business.provider_model_catalog
		WHERE provider_code = 'gpt' AND model = $1
	`, model).Scan(
		&serviceTierPricesJSON,
		&longContextThreshold,
		&longContextInputMultiplier,
		&longContextOutputMultiplier,
	); err != nil {
		t.Fatalf("query %s tier pricing metadata: %v", model, err)
	}
	var serviceTierPrices map[string]port.ManagementProviderModelPriceSet
	if err := json.Unmarshal([]byte(serviceTierPricesJSON), &serviceTierPrices); err != nil {
		t.Fatalf("decode %s tier pricing metadata: %v", model, err)
	}
	priority, priorityOK := serviceTierPrices["priority"]
	flex, flexOK := serviceTierPrices["flex"]
	if !priorityOK || !flexOK ||
		priority.InputUSDPer1M == nil || *priority.InputUSDPer1M != 10 ||
		priority.OutputUSDPer1M == nil || *priority.OutputUSDPer1M != 60 ||
		priority.CachedInputUSDPer1M == nil || *priority.CachedInputUSDPer1M != 1 ||
		priority.CacheWriteUSDPer1M == nil || *priority.CacheWriteUSDPer1M != 12.5 ||
		priority.CacheWrite1hUSDPer1M != nil ||
		flex.InputUSDPer1M == nil || *flex.InputUSDPer1M != 2.5 ||
		flex.OutputUSDPer1M == nil || *flex.OutputUSDPer1M != 15 ||
		flex.CachedInputUSDPer1M == nil || *flex.CachedInputUSDPer1M != 0.25 ||
		flex.CacheWriteUSDPer1M == nil || *flex.CacheWriteUSDPer1M != 3.125 ||
		flex.CacheWrite1hUSDPer1M != nil ||
		longContextThreshold.Int64 != 272000 ||
		longContextInputMultiplier.Float64 != 2 ||
		longContextOutputMultiplier.Float64 != 1.5 {
		t.Fatalf("%s PG tier pricing metadata drifted from Node values", model)
	}
}

func stringPtr(value string) *string {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}
