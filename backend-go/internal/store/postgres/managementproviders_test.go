package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementProviderOptionFromRowKeepsSystemAndProtocolHealthCheckDefaults(t *testing.T) {
	profilesByProvider, err := managementProviderProfilesByProvider([]postgresqueries.ListManagementProviderOptionProfilesRow{
		{
			ID:                      "profile_gemini_openai_chat_v1beta",
			ProviderCode:            "gemini",
			Name:                    "Gemini / OpenAI Chat",
			Enabled:                 true,
			ProtocolCode:            "openai",
			ProtocolVersion:         "v1",
			BaseUrl:                 "https://generativelanguage.googleapis.com/v1beta/openai",
			DefaultHealthCheckModel: "gemini-3.5-flash",
			AccountTypesJson:        `["api_key"]`,
			CapabilitiesJson:        `["chat"]`,
		},
		{
			ID:                      "profile_gemini_native_v1beta",
			ProviderCode:            "gemini",
			Name:                    "Gemini / Gemini v1beta",
			Enabled:                 true,
			ProtocolCode:            "gemini",
			ProtocolVersion:         "v1beta",
			BaseUrl:                 "https://generativelanguage.googleapis.com",
			DefaultHealthCheckModel: "gemini-3.5-flash",
			AccountTypesJson:        `["api_key"]`,
			CapabilitiesJson:        `["generate_content","models"]`,
		},
	}, map[string][]port.ManagementProviderEndpointFamily{})
	if err != nil {
		t.Fatalf("managementProviderProfilesByProvider() error = %v", err)
	}
	option, err := managementProviderOptionFromRow(managementProviderRow{
		ID:                         "provider_gemini",
		Code:                       "gemini",
		Name:                       "Gemini",
		Description:                pgtype.Text{String: "Google Gemini provider", Valid: true},
		Enabled:                    true,
		DefaultSupportedModelsJson: `["gemini-3.5-flash"]`,
	}, profilesByProvider["gemini"], " gemini-custom ", " gemini-system ")
	if err != nil {
		t.Fatalf("managementProviderOptionFromRow() error = %v", err)
	}

	if option.DefaultProtocolProfileID != "profile_gemini_native_v1beta" {
		t.Fatalf("default profile = %q, want gemini native", option.DefaultProtocolProfileID)
	}
	if option.ProtocolCode != "gemini" || option.DefaultHealthCheckModel != "gemini-custom" {
		t.Fatalf("option = %+v", option)
	}
	if option.SystemDefaultHealthCheckModel != "gemini-system" {
		t.Fatalf("system default health check model = %q, want global default", option.SystemDefaultHealthCheckModel)
	}
	if option.ProtocolProfiles[1].DefaultHealthCheckModel != "gemini-3.5-flash" {
		t.Fatalf("protocol profile default was overwritten: %+v", option.ProtocolProfiles)
	}
}

func TestManagementProviderOptionFromRowFallsBackToSystemHealthCheckDefault(t *testing.T) {
	profiles := []port.ManagementProviderProtocolProfile{
		{
			ID:                      "profile_gpt_openai_v1",
			ProviderCode:            "gpt",
			Enabled:                 true,
			ProtocolCode:            "openai",
			ProtocolVersion:         "v1",
			DefaultHealthCheckModel: "gpt-5-system",
		},
	}

	option, err := managementProviderOptionFromRow(managementProviderRow{
		ID:                         "provider_gpt",
		Code:                       "gpt",
		Name:                       "GPT",
		Enabled:                    true,
		DefaultSupportedModelsJson: `[]`,
	}, profiles, " ", "gpt-5-system")
	if err != nil {
		t.Fatalf("managementProviderOptionFromRow() error = %v", err)
	}

	if option.DefaultHealthCheckModel != "gpt-5-system" || option.SystemDefaultHealthCheckModel != "gpt-5-system" {
		t.Fatalf("option = %+v", option)
	}
	if option.ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5-system" {
		t.Fatalf("protocol profiles = %+v", option.ProtocolProfiles)
	}
}

func TestManagementProviderOptionFromRowFallsBackToProtocolProfileWhenSystemDefaultMissing(t *testing.T) {
	profiles := []port.ManagementProviderProtocolProfile{
		{
			ID:                      "profile_gpt_openai_v1",
			ProviderCode:            "gpt",
			Enabled:                 true,
			DefaultHealthCheckModel: "gpt-profile-default",
		},
	}
	option, err := managementProviderOptionFromRow(managementProviderRow{
		ID:                         "provider_gpt",
		Code:                       "gpt",
		Name:                       "GPT",
		Enabled:                    true,
		DefaultSupportedModelsJson: `[]`,
	}, profiles, "", "")
	if err != nil {
		t.Fatalf("managementProviderOptionFromRow() error = %v", err)
	}
	if option.DefaultHealthCheckModel != "gpt-profile-default" {
		t.Fatalf("effective default = %q, want profile fallback", option.DefaultHealthCheckModel)
	}
	if option.SystemDefaultHealthCheckModel != "" {
		t.Fatalf("system default = %q, want empty explicit global default", option.SystemDefaultHealthCheckModel)
	}
}

func TestManagementProviderSQLKeepsListAndOptionsFiltersSeparate(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_options.sql")
	if err != nil {
		t.Fatalf("read provider option query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	listStart := strings.Index(sql, "-- name: ListManagementProviders :many")
	optionStart := strings.Index(sql, "-- name: ListManagementProviderOptionProviders :many")
	if listStart < 0 || optionStart < 0 || optionStart <= listStart {
		t.Fatalf("provider SQL missing list/options queries")
	}
	listSQL := sql[listStart:optionStart]
	optionSQL := sql[optionStart:]
	for _, want := range []string{
		"FROM juhe_business.providers",
		"ORDER BY name ASC, code ASC",
		"LIMIT 50",
	} {
		if !strings.Contains(listSQL, want) {
			t.Fatalf("provider list SQL missing %q", want)
		}
	}
	if strings.Contains(listSQL, "WHERE enabled = true") {
		t.Fatalf("provider list SQL must include disabled providers: %s", listSQL)
	}
	if !strings.Contains(optionSQL, "WHERE enabled = true") {
		t.Fatalf("provider options SQL must keep enabled filter")
	}
	for _, want := range []string{
		"default_health_check_model",
		"provider_default_health_check_models",
		"ListManagementProviderDefaultHealthCheckModelPreferences",
		"UpsertManagementProviderDefaultHealthCheckModelPreference",
		"provider_system_default_health_check_models",
		"ListManagementProviderSystemDefaultHealthCheckModels",
		"UpsertManagementProviderSystemDefaultHealthCheckModel",
	} {
		if !strings.Contains(optionSQL, want) {
			t.Fatalf("provider options SQL missing health check model contract %q", want)
		}
	}
	for _, legacy := range []string{
		"default_test_model",
		"provider_default_test_models",
		"DefaultTestModel",
	} {
		if strings.Contains(optionSQL, legacy) {
			t.Fatalf("provider options SQL retains legacy contract %q", legacy)
		}
	}
}

func TestManagementProviderMigrationsUseHealthCheckModelColumns(t *testing.T) {
	publicAccountsMigration, err := os.ReadFile("../../../db/migrations/000005_w1b_public_accounts.sql")
	if err != nil {
		t.Fatalf("read public accounts migration: %v", err)
	}
	publicAccountsSQL := string(publicAccountsMigration)
	if !strings.Contains(publicAccountsSQL, "CREATE TABLE IF NOT EXISTS juhe_business.provider_protocol_profiles") ||
		!strings.Contains(publicAccountsSQL, "default_health_check_model text NOT NULL DEFAULT ''") {
		t.Fatalf("provider protocol profile health check model column missing")
	}
	if !strings.Contains(publicAccountsSQL, "CREATE TABLE IF NOT EXISTS juhe_business.accounts") ||
		!strings.Contains(publicAccountsSQL, "health_check_model text NOT NULL") {
		t.Fatalf("accounts.health_check_model column missing")
	}

	providerOptionsMigration, err := os.ReadFile("../../../db/migrations/000008_w2_management_provider_options.sql")
	if err != nil {
		t.Fatalf("read provider options migration: %v", err)
	}
	providerOptionsSQL := string(providerOptionsMigration)
	for _, want := range []string{
		"default_health_check_model text",
		"provider_default_health_check_models",
		"idx_provider_default_health_check_models_model",
	} {
		if !strings.Contains(providerOptionsSQL, want) {
			t.Fatalf("provider options migration missing %q", want)
		}
	}
	for _, legacy := range []string{
		"default_test_model",
		"provider_default_test_models",
		"idx_provider_default_test_models_model",
	} {
		if strings.Contains(providerOptionsSQL, legacy) {
			t.Fatalf("provider options migration retains legacy contract %q", legacy)
		}
	}

	systemDefaultMigration, err := os.ReadFile("../../../db/migrations/000028_w2_provider_system_default_health_check_models.sql")
	if err != nil {
		t.Fatalf("read provider system default health check model migration: %v", err)
	}
	systemDefaultSQL := string(systemDefaultMigration)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.provider_system_default_health_check_models",
		"provider_code text PRIMARY KEY",
		"model text NOT NULL",
		"idx_provider_system_default_health_check_models_model",
	} {
		if !strings.Contains(systemDefaultSQL, want) {
			t.Fatalf("provider system default migration missing %q", want)
		}
	}

	providerModelsMigration, err := os.ReadFile("../../../db/migrations/000027_w2_provider_model_request_capabilities.sql")
	if err != nil {
		t.Fatalf("read provider model migration: %v", err)
	}
	providerModelsSQL := string(providerModelsMigration)
	for _, want := range []string{
		"supported_service_tiers_json text NOT NULL DEFAULT '[]'",
		"supported_reasoning_efforts_json text NOT NULL DEFAULT '[]'",
		"default_reasoning_effort text",
	} {
		if !strings.Contains(providerModelsSQL, want) {
			t.Fatalf("provider model capability migration missing %q", want)
		}
	}
}

func TestDecodeProviderStringArrayTrimsDedupes(t *testing.T) {
	values, err := decodeProviderStringArray(`[" chat ","chat","responses",""]`, "test")
	if err != nil {
		t.Fatalf("decodeProviderStringArray() error = %v", err)
	}
	if len(values) != 2 || values[0] != "chat" || values[1] != "responses" {
		t.Fatalf("values = %#v", values)
	}
}

func TestManagementProviderModelCatalogItemFromRowDecodesOptionalFields(t *testing.T) {
	inputPrice := pgtype.Float8{Float64: 1.25, Valid: true}
	maxInput := pgtype.Int4{Int32: 128000, Valid: true}
	row := postgresqueries.ListManagementProviderModelCatalogRow{
		ID:                            "custom_model_1",
		ProviderCode:                  "gpt",
		Model:                         "gpt-custom",
		Scope:                         "personal",
		SystemAccountID:               pgtype.Text{String: "sys_user", Valid: true},
		Status:                        "active",
		Mode:                          pgtype.Text{String: "chat", Valid: true},
		CatalogOrder:                  pgtype.Int4{Int32: 10, Valid: true},
		SupportedApiProtocolsJson:     `[" chat_completions ","chat_completions","responses"]`,
		SupportedServiceTiersJson:     `[" priority ","priority","flex"]`,
		SupportedReasoningEffortsJson: `["low","high","high"]`,
		DefaultReasoningEffort:        pgtype.Text{String: "high", Valid: true},
		ContextWindowTokens:           maxInput,
		MaxInputTokens:                maxInput,
		InputUsdPer1m:                 inputPrice,
		SupportsPromptCaching:         true,
		CatalogVisible:                true,
		Source:                        "custom-personal",
	}

	item, err := managementProviderModelCatalogItemFromRow(row)
	if err != nil {
		t.Fatalf("managementProviderModelCatalogItemFromRow() error = %v", err)
	}
	if item.SystemAccountID != "sys_user" || item.Mode != "chat" || item.InputUSDPer1M == nil || *item.InputUSDPer1M != 1.25 {
		t.Fatalf("item = %+v", item)
	}
	if item.ContextWindowTokens == nil || *item.ContextWindowTokens != 128000 {
		t.Fatalf("context window = %#v", item.ContextWindowTokens)
	}
	if len(item.SupportedAPIProtocols) != 2 || item.SupportedAPIProtocols[0] != "chat_completions" || item.SupportedAPIProtocols[1] != "responses" {
		t.Fatalf("protocols = %+v", item.SupportedAPIProtocols)
	}
	if len(item.SupportedServiceTiers) != 2 || item.SupportedServiceTiers[0] != "priority" || item.SupportedServiceTiers[1] != "flex" {
		t.Fatalf("service tiers = %+v", item.SupportedServiceTiers)
	}
	if len(item.SupportedReasoningEfforts) != 2 || item.SupportedReasoningEfforts[0] != "low" || item.SupportedReasoningEfforts[1] != "high" || item.DefaultReasoningEffort != "high" {
		t.Fatalf("reasoning capabilities = %+v default=%q", item.SupportedReasoningEfforts, item.DefaultReasoningEffort)
	}
	if !item.SupportsServiceTier {
		t.Fatalf("supports service tier must derive from exact tier array")
	}
}

func TestManagementCustomProviderModelBindingSummaryScopesMappingsByProvider(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	start := strings.Index(sql, "-- name: GetManagementCustomProviderModelBindingSummary :one")
	end := strings.Index(sql, "-- name: ClearManagementProviderDefaultHealthCheckModelIfModel :execrows")
	if start < 0 || end <= start {
		t.Fatalf("provider model SQL missing binding summary query")
	}
	bindingSQL := sql[start:end]
	for _, want := range []string{
		"WHERE asm.provider_code = sqlc.arg(provider_code)",
		"WHERE amm.source_model = sqlc.arg(model)\n    AND amm.provider_code = sqlc.arg(provider_code)",
		"WHERE amm.upstream_model = sqlc.arg(model)\n    AND amm.provider_code = sqlc.arg(provider_code)",
	} {
		if !strings.Contains(bindingSQL, want) {
			t.Fatalf("binding summary SQL missing provider-scoped filter %q in:\n%s", want, bindingSQL)
		}
	}
}
