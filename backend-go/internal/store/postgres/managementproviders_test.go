package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestManagementProviderOptionFromRowUsesDeterministicDefaultProfileByProviderCode(t *testing.T) {
	tests := []struct {
		name          string
		providerCode  string
		profiles      []port.ManagementProviderProtocolProfile
		wantProfileID string
		wantProtocol  string
		wantBaseURL   string
	}{
		{
			name:         "deepseek",
			providerCode: "deepseek",
			profiles: []port.ManagementProviderProtocolProfile{
				{
					ID:              "profile_deepseek_anthropic_v1",
					ProviderCode:    "deepseek",
					Enabled:         true,
					ProtocolCode:    "anthropic",
					ProtocolVersion: "v1",
					BaseURL:         "https://api.deepseek.com/anthropic",
				},
				{
					ID:              "profile_deepseek_openai_v1",
					ProviderCode:    "deepseek",
					Enabled:         true,
					ProtocolCode:    "openai",
					ProtocolVersion: "v1",
					BaseURL:         "https://api.deepseek.com",
				},
			},
			wantProfileID: "profile_deepseek_openai_v1",
			wantProtocol:  "openai",
			wantBaseURL:   "https://api.deepseek.com",
		},
		{
			name:         "hybrid",
			providerCode: "hybrid",
			profiles: []port.ManagementProviderProtocolProfile{
				{
					ID:              "profile_hybrid_anthropic_messages_v1",
					ProviderCode:    "hybrid",
					Enabled:         true,
					ProtocolCode:    "anthropic",
					ProtocolVersion: "v1",
				},
				{
					ID:              "profile_hybrid_openai_chat_v1",
					ProviderCode:    "hybrid",
					Enabled:         true,
					ProtocolCode:    "openai",
					ProtocolVersion: "v1",
				},
			},
			wantProfileID: "profile_hybrid_openai_chat_v1",
			wantProtocol:  "openai",
			wantBaseURL:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			option, err := managementProviderOptionFromRow(managementProviderRow{
				ID:                         "provider_" + tt.providerCode,
				Code:                       tt.providerCode,
				Name:                       tt.providerCode,
				Enabled:                    true,
				DefaultSupportedModelsJson: `[]`,
			}, tt.profiles, "", "")
			if err != nil {
				t.Fatalf("managementProviderOptionFromRow() error = %v", err)
			}
			if option.DefaultProtocolProfileID != tt.wantProfileID || option.ProtocolCode != tt.wantProtocol || option.BaseURL != tt.wantBaseURL {
				t.Fatalf("option = %+v, want profile=%q protocol=%q baseURL=%q", option, tt.wantProfileID, tt.wantProtocol, tt.wantBaseURL)
			}
		})
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
	longContextThreshold := pgtype.Int4{Int32: 272000, Valid: true}
	longContextInputMultiplier := pgtype.Float8{Float64: 2, Valid: true}
	longContextOutputMultiplier := pgtype.Float8{Float64: 1.5, Valid: true}
	maxInput := pgtype.Int4{Int32: 128000, Valid: true}
	row := postgresqueries.ListManagementProviderModelCatalogRow{
		ID:                              "custom_model_1",
		ProviderCode:                    "gpt",
		Model:                           "gpt-custom",
		Scope:                           "personal",
		SystemAccountID:                 pgtype.Text{String: "sys_user", Valid: true},
		Status:                          "active",
		Mode:                            pgtype.Text{String: "chat", Valid: true},
		CatalogOrder:                    pgtype.Int4{Int32: 10, Valid: true},
		SupportedApiProtocolsJson:       `[" chat_completions ","chat_completions","responses"]`,
		SupportedServiceTiersJson:       `[" priority ","priority","flex"]`,
		SupportedReasoningEffortsJson:   `["low","high","high"]`,
		DefaultReasoningEffort:          pgtype.Text{String: "high", Valid: true},
		ContextWindowTokens:             maxInput,
		MaxInputTokens:                  maxInput,
		InputUsdPer1m:                   inputPrice,
		ServiceTierPricesJson:           `{"priority":{"inputUsdPer1M":2.5},"flex":{"inputUsdPer1M":0.625}}`,
		LongContextInputTokenThreshold:  longContextThreshold,
		LongContextInputCostMultiplier:  longContextInputMultiplier,
		LongContextOutputCostMultiplier: longContextOutputMultiplier,
		SupportsPromptCaching:           true,
		CatalogVisible:                  true,
		Source:                          "custom-personal",
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
	if item.ServiceTierPrices["priority"].InputUSDPer1M == nil || *item.ServiceTierPrices["priority"].InputUSDPer1M != 2.5 ||
		item.ServiceTierPrices["flex"].InputUSDPer1M == nil || *item.ServiceTierPrices["flex"].InputUSDPer1M != 0.625 {
		t.Fatalf("tier prices = %+v", item.ServiceTierPrices)
	}
	if item.LongContextInputTokenThreshold == nil || *item.LongContextInputTokenThreshold != 272000 ||
		item.LongContextInputCostMultiplier == nil || *item.LongContextInputCostMultiplier != 2 ||
		item.LongContextOutputCostMultiplier == nil || *item.LongContextOutputCostMultiplier != 1.5 {
		t.Fatalf("long-context metadata = threshold:%v input:%v output:%v",
			item.LongContextInputTokenThreshold,
			item.LongContextInputCostMultiplier,
			item.LongContextOutputCostMultiplier,
		)
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

func TestManagementProviderModelCatalogSQLLocksThenFullyUpdatesBuiltInConfiguration(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	listStart := strings.Index(sql, "-- name: ListManagementProviderModelCatalog :many")
	lockStart := strings.Index(sql, "-- name: LockManagementBuiltInProviderModelConfiguration :one")
	updateStart := strings.Index(sql, "-- name: UpdateManagementBuiltInProviderModelConfiguration :one")
	findStart := strings.Index(sql, "-- name: FindManagementCustomProviderModel :one")
	if listStart < 0 || lockStart <= listStart || updateStart <= lockStart || findStart <= updateStart {
		t.Fatalf("provider model SQL query boundaries missing")
	}
	listSQL := sql[listStart:lockStart]
	if !strings.Contains(listSQL, "SELECT\n  id,\n  provider_code") || strings.Contains(listSQL, "''::text AS id") {
		t.Fatalf("catalog list must return provider_model_catalog.id:\n%s", listSQL)
	}

	lockSQL := sql[lockStart:updateStart]
	updateSQL := sql[updateStart:findStart]
	if !strings.Contains(lockSQL, "FOR UPDATE") || strings.Contains(updateSQL, "CASE WHEN") || strings.Contains(updateSQL, "WITH locked") {
		t.Fatalf("built-in update must use a separate row lock and full update:\nlock=%s\nupdate=%s", lockSQL, updateSQL)
	}
	for _, column := range []string{"status", "mode", "supported_reasoning_efforts_json", "default_reasoning_effort", "service_tier_prices_json", "updated_at"} {
		if !regexp.MustCompile(regexp.QuoteMeta(column) + `\s*=\s*sqlc\.(?:n?arg)\(`).MatchString(updateSQL) {
			t.Fatalf("full built-in update missing %q assignment:\n%s", column, updateSQL)
		}
	}
}

func TestUpdateManagementBuiltInProviderModelPricesMapsSparsePresenceToSQLC(t *testing.T) {
	outputPrice := 9.5
	persistedOutputPrice := 17.5
	tierInputPrice := 3.0
	updatedAt := time.Date(2026, 7, 15, 8, 9, 10, 0, time.UTC)
	q := &managementBuiltInProviderModelPriceUpdateQueriesStub{
		locked: postgresqueries.LockManagementBuiltInProviderModelConfigurationRow{
			ID: "provider_model_gpt_real", ProviderCode: "gpt", Status: "disabled", Mode: pgtype.Text{String: "text", Valid: true},
			SupportedApiProtocolsJson: `["chat_completions"]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `[]`, ServiceTierPricesJson: `{}`,
			InputUsdPer1m: pgtype.Float8{Float64: 2.5, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: updatedAt.Add(-time.Hour), Valid: true},
		},
		updated: postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow{
			ID: "provider_model_gpt_real", ProviderCode: "gpt", Status: "disabled", Mode: pgtype.Text{String: "text", Valid: true},
			SupportedApiProtocolsJson: `["chat_completions"]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `[]`,
			ServiceTierPricesJson: `{"priority":{"inputUsdPer1M":3}}`, OutputUsdPer1m: pgtype.Float8{Float64: persistedOutputPrice, Valid: true},
			UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
		},
	}

	validateCalls := 0
	result, found, err := updateManagementBuiltInProviderModelPricesTx(context.Background(), q, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID:           "provider_model_gpt_real",
		ProviderCode: "gpt",
		InputUSDPer1M: port.ManagementProviderModelOptionalFloat{
			Present: true,
		},
		OutputUSDPer1M: port.ManagementProviderModelOptionalFloat{
			Present: true,
			Value:   &outputPrice,
		},
		ServiceTierPrices: port.ManagementProviderModelOptionalPriceMap{
			Present: true,
			Value: map[string]port.ManagementProviderModelPriceSet{
				"priority": {InputUSDPer1M: &tierInputPrice},
			},
		},
	}, func(got port.ManagementBuiltInProviderModelPriceUpdateResult) error {
		validateCalls++
		if got.Before.InputUSDPer1M == nil || *got.Before.InputUSDPer1M != 2.5 || got.After.InputUSDPer1M != nil ||
			got.After.OutputUSDPer1M == nil || *got.After.OutputUSDPer1M != outputPrice {
			t.Fatalf("validate snapshots = %+v", got)
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("updateManagementBuiltInProviderModelPrices() found=%v error=%v", found, err)
	}
	input := q.updateInput
	if input.ID != "provider_model_gpt_real" || input.ProviderCode != "gpt" ||
		input.InputUsdPer1m.Valid || !input.OutputUsdPer1m.Valid || input.OutputUsdPer1m.Float64 != outputPrice ||
		input.CachedInputUsdPer1m.Valid || input.ServiceTierPricesJson != `{"priority":{"inputUsdPer1M":3}}` {
		t.Fatalf("sqlc input = %+v", input)
	}
	priority := result.After.ServiceTierPrices["priority"]
	if result.After.ID != "provider_model_gpt_real" || result.After.ProviderCode != "gpt" || result.After.InputUSDPer1M != nil ||
		result.After.OutputUSDPer1M == nil || *result.After.OutputUSDPer1M != persistedOutputPrice ||
		priority.InputUSDPer1M == nil || *priority.InputUSDPer1M != tierInputPrice || !result.After.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("persisted result = %+v", result)
	}
	if result.Before.Status != "disabled" || result.Before.InputUSDPer1M == nil || *result.Before.InputUSDPer1M != 2.5 ||
		result.Before.Mode != "text" || len(result.Before.SupportedAPIProtocols) != 1 || result.Before.SupportedAPIProtocols[0] != "chat_completions" ||
		result.Before.ContextWindowTokens != nil || result.After.Status != "disabled" || validateCalls != 1 {
		t.Fatalf("atomic snapshots = before=%+v after=%+v", result.Before, result.After)
	}
}

func TestUpdateManagementBuiltInProviderModelPricesMapsNoRowsToNotFound(t *testing.T) {
	q := &managementBuiltInProviderModelPriceUpdateQueriesStub{lockErr: pgx.ErrNoRows}
	result, found, err := updateManagementBuiltInProviderModelPricesTx(context.Background(), q, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: "missing", ProviderCode: "gpt",
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error { return nil })
	if err != nil || found || result.Before.ID != "" || result.After.ID != "" {
		t.Fatalf("result=%+v found=%v err=%v, want not found", result, found, err)
	}
}

func TestUpdateManagementBuiltInProviderModelPricesRejectsMismatchedReturningIdentity(t *testing.T) {
	q := &managementBuiltInProviderModelPriceUpdateQueriesStub{
		locked: postgresqueries.LockManagementBuiltInProviderModelConfigurationRow{
			ID: "provider_model_gpt_real", ProviderCode: "gpt", Status: "active",
			SupportedApiProtocolsJson: `[]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `[]`, ServiceTierPricesJson: `{}`,
		},
		updated: postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow{
			ID: "provider_model_other", ProviderCode: "gpt", Status: "active",
			SupportedApiProtocolsJson: `[]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `[]`, ServiceTierPricesJson: `{}`,
		},
	}
	validateCalls := 0
	_, found, err := updateManagementBuiltInProviderModelPricesTx(context.Background(), q, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: "provider_model_gpt_real", ProviderCode: "gpt",
	}, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error {
		validateCalls++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "identity") || found || validateCalls != 1 {
		t.Fatalf("err/found/validate = %v/%t/%d, want identity error/false/1", err, found, validateCalls)
	}
}

func TestUpdateManagementBuiltInProviderModelPricesTransactionValidationAndCommit(t *testing.T) {
	locked := postgresqueries.LockManagementBuiltInProviderModelConfigurationRow{
		ID: "provider_model_gpt_real", ProviderCode: "gpt", Status: "active", Mode: pgtype.Text{String: "text", Valid: true},
		SupportedApiProtocolsJson: `[]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `["low"]`,
		DefaultReasoningEffort: pgtype.Text{String: "low", Valid: true}, ServiceTierPricesJson: `{}`,
		UpdatedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}
	updated := postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow{
		ID: locked.ID, ProviderCode: locked.ProviderCode, Status: locked.Status, Mode: locked.Mode,
		SupportedApiProtocolsJson: `[]`, SupportedServiceTiersJson: `[]`, SupportedReasoningEffortsJson: `["high"]`,
		DefaultReasoningEffort: pgtype.Text{String: "high", Valid: true}, ServiceTierPricesJson: `{}`,
		UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	input := port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: locked.ID, ProviderCode: locked.ProviderCode,
		SupportedReasoningEfforts: port.ManagementProviderModelOptionalStringList{Present: true, Value: []string{"high"}},
		DefaultReasoningEffort:    port.ManagementProviderModelOptionalString{Present: true, Value: "high"},
	}

	t.Run("validation failure stops before update and rolls back with independent context", func(t *testing.T) {
		tx := &managementBuiltInProviderModelUpdateTxStub{rows: []pgx.Row{managementProviderModelLockRow(locked), managementProviderModelUpdateRow(updated)}}
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), managementProviderModelRollbackContextKey{}, "rollback-value"))
		validateErr := errors.New("invalid final configuration")
		validateCalls := 0
		_, found, err := updateManagementBuiltInProviderModelPricesInTx(ctx, func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil }, input, func(result port.ManagementBuiltInProviderModelPriceUpdateResult) error {
			validateCalls++
			cancel()
			if result.Before.DefaultReasoningEffort != "low" || result.After.DefaultReasoningEffort != "high" {
				t.Fatalf("snapshots = %+v", result)
			}
			return validateErr
		})
		if !errors.Is(err, validateErr) || found || validateCalls != 1 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
			t.Fatalf("err/found/validate/commit/rollback = %v/%t/%d/%d/%d", err, found, validateCalls, tx.commitCalls, tx.rollbackCalls)
		}
		if !reflect.DeepEqual(tx.calls, []string{"lock"}) || len(tx.rows) != 1 {
			t.Fatalf("validation failure transaction calls/remaining rows = %v/%d, want lock/1", tx.calls, len(tx.rows))
		}
		if tx.rollbackContextErr != nil || tx.rollbackContextValue != "rollback-value" || !tx.rollbackHasDeadline {
			t.Fatalf("rollback context = err:%v value:%v deadline:%t", tx.rollbackContextErr, tx.rollbackContextValue, tx.rollbackHasDeadline)
		}
		if remaining := time.Until(tx.rollbackDeadline); remaining <= 0 || remaining > 5*time.Second {
			t.Fatalf("rollback deadline remaining = %s", remaining)
		}
	})

	t.Run("valid result commits after one validation", func(t *testing.T) {
		tx := &managementBuiltInProviderModelUpdateTxStub{rows: []pgx.Row{managementProviderModelLockRow(locked), managementProviderModelUpdateRow(updated)}}
		validateCalls := 0
		result, found, err := updateManagementBuiltInProviderModelPricesInTx(context.Background(), func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil }, input, func(port.ManagementBuiltInProviderModelPriceUpdateResult) error {
			validateCalls++
			if tx.commitCalls != 0 {
				t.Fatal("validation ran after commit")
			}
			return nil
		})
		if err != nil || !found || validateCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
			t.Fatalf("result/err/found/validate/commit/rollback = %+v/%v/%t/%d/%d/%d", result, err, found, validateCalls, tx.commitCalls, tx.rollbackCalls)
		}
		if !reflect.DeepEqual(tx.calls, []string{"lock", "update", "commit"}) {
			t.Fatalf("transaction order = %v", tx.calls)
		}
	})
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

type managementBuiltInProviderModelPriceUpdateQueriesStub struct {
	lockInput   postgresqueries.LockManagementBuiltInProviderModelConfigurationParams
	locked      postgresqueries.LockManagementBuiltInProviderModelConfigurationRow
	lockErr     error
	updateInput postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams
	updated     postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow
	updateErr   error
}

func (s *managementBuiltInProviderModelPriceUpdateQueriesStub) LockManagementBuiltInProviderModelConfiguration(
	_ context.Context,
	input postgresqueries.LockManagementBuiltInProviderModelConfigurationParams,
) (postgresqueries.LockManagementBuiltInProviderModelConfigurationRow, error) {
	s.lockInput = input
	return s.locked, s.lockErr
}

func (s *managementBuiltInProviderModelPriceUpdateQueriesStub) UpdateManagementBuiltInProviderModelConfiguration(
	_ context.Context,
	input postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams,
) (postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow, error) {
	s.updateInput = input
	return s.updated, s.updateErr
}

type managementProviderModelRollbackContextKey struct{}

type managementBuiltInProviderModelUpdateTxStub struct {
	pgx.Tx
	rows                 []pgx.Row
	calls                []string
	commitCalls          int
	rollbackCalls        int
	rollbackContextErr   error
	rollbackContextValue any
	rollbackHasDeadline  bool
	rollbackDeadline     time.Time
}

func (s *managementBuiltInProviderModelUpdateTxStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FOR UPDATE"):
		s.calls = append(s.calls, "lock")
	case strings.Contains(sql, "UPDATE juhe_business.provider_model_catalog"):
		s.calls = append(s.calls, "update")
	default:
		return managementProviderModelStaticRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
	}
	if len(s.rows) == 0 {
		return managementProviderModelStaticRow{err: errors.New("missing stub row")}
	}
	row := s.rows[0]
	s.rows = s.rows[1:]
	return row
}

func (s *managementBuiltInProviderModelUpdateTxStub) Commit(context.Context) error {
	s.calls = append(s.calls, "commit")
	s.commitCalls++
	return nil
}

func (s *managementBuiltInProviderModelUpdateTxStub) Rollback(ctx context.Context) error {
	s.rollbackCalls++
	s.rollbackContextErr = ctx.Err()
	s.rollbackContextValue = ctx.Value(managementProviderModelRollbackContextKey{})
	s.rollbackDeadline, s.rollbackHasDeadline = ctx.Deadline()
	return nil
}

type managementProviderModelStaticRow struct {
	values []any
	err    error
}

func (r managementProviderModelStaticRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destinations = %d, values = %d", len(dest), len(r.values))
	}
	for index := range dest {
		reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(r.values[index]))
	}
	return nil
}

func managementProviderModelLockRow(row postgresqueries.LockManagementBuiltInProviderModelConfigurationRow) pgx.Row {
	return managementProviderModelStaticRow{values: managementProviderModelConfigurationValues(row.ID, row.ProviderCode, row.Status, row.CatalogVisible, row.Mode, row.SupportedApiProtocolsJson, row.SupportedServiceTiersJson, row.SupportedReasoningEffortsJson, row.DefaultReasoningEffort, row.ReleaseDate, row.ShutdownDate, row.ContextWindowTokens, row.MaxInputTokens, row.MaxOutputTokens, row.InputUsdPer1m, row.OutputUsdPer1m, row.CachedInputUsdPer1m, row.CacheWriteUsdPer1m, row.CacheWrite1hUsdPer1m, row.ServiceTierPricesJson, row.ImageInputUsdPer1m, row.ImageOutputUsdPer1m, row.AudioInputUsdPer1m, row.AudioOutputUsdPer1m, row.OutputUsdPerImage, row.UpdatedAt)}
}

func managementProviderModelUpdateRow(row postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow) pgx.Row {
	return managementProviderModelStaticRow{values: managementProviderModelConfigurationValues(row.ID, row.ProviderCode, row.Status, row.CatalogVisible, row.Mode, row.SupportedApiProtocolsJson, row.SupportedServiceTiersJson, row.SupportedReasoningEffortsJson, row.DefaultReasoningEffort, row.ReleaseDate, row.ShutdownDate, row.ContextWindowTokens, row.MaxInputTokens, row.MaxOutputTokens, row.InputUsdPer1m, row.OutputUsdPer1m, row.CachedInputUsdPer1m, row.CacheWriteUsdPer1m, row.CacheWrite1hUsdPer1m, row.ServiceTierPricesJson, row.ImageInputUsdPer1m, row.ImageOutputUsdPer1m, row.AudioInputUsdPer1m, row.AudioOutputUsdPer1m, row.OutputUsdPerImage, row.UpdatedAt)}
}

func managementProviderModelConfigurationValues(values ...any) []any { return values }

var _ pgx.Tx = (*managementBuiltInProviderModelUpdateTxStub)(nil)
var _ pgx.Row = managementProviderModelStaticRow{}
