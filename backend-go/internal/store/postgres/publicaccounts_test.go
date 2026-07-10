package postgres

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestFindPublicAccountProviderProfileQuerySelectsEffectiveHealthCheckModel(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_accounts.sql")
	if err != nil {
		t.Fatalf("read public account query: %v", err)
	}
	sql := string(source)
	start := strings.Index(sql, "-- name: FindPublicAccountProviderProfile :one")
	end := strings.Index(sql, "-- name: FindExistingPublicAccountGroupByName :one")
	if start < 0 || end <= start {
		t.Fatal("public account SQL missing provider profile query")
	}
	query := sql[start:end]
	for _, want := range []string{
		"providers.default_supported_models_json",
		"JOIN juhe_business.providers AS providers",
		"ON providers.code = profiles.provider_code",
		"LEFT JOIN juhe_business.system_accounts AS target_system_account",
		"target_system_account.role NOT IN ('admin', 'super_admin')",
		"LEFT JOIN juhe_business.provider_default_health_check_models AS personal_health_check_defaults",
		"personal_health_check_defaults.system_account_id = target_system_account.id",
		"LEFT JOIN juhe_business.provider_system_default_health_check_models AS system_health_check_defaults",
		"system_health_check_defaults.provider_code = profiles.provider_code",
		"personal_health_check_defaults.model,",
		"system_health_check_defaults.model,",
		"profiles.default_health_check_model",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("provider profile query missing %q in:\n%s", want, query)
		}
	}
}

func TestUpdatePublicAccountQueryPreservesHealthCheckModel(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_accounts.sql")
	if err != nil {
		t.Fatalf("read public account query: %v", err)
	}
	sql := string(source)
	start := strings.Index(sql, "-- name: UpdatePublicAccountAllFields :one")
	end := strings.Index(sql, "-- name: UpdatePublicAccountGroupBindingDispatch :exec")
	if start < 0 || end <= start {
		t.Fatal("public account SQL missing update query")
	}
	query := sql[start:end]
	whereIndex := strings.Index(query, "\nWHERE ")
	if whereIndex < 0 {
		t.Fatalf("public account update query missing WHERE clause:\n%s", query)
	}
	if strings.Contains(query[:whereIndex], "health_check_model") {
		t.Fatalf("public account update query must not directly modify health_check_model:\n%s", query)
	}
	if !strings.Contains(query[whereIndex:], "health_check_model") {
		t.Fatalf("public account update query must return health_check_model:\n%s", query)
	}
}

func TestPublicAccountQueriesReadAndInsertHealthCheckModel(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_accounts.sql")
	if err != nil {
		t.Fatalf("read public account query: %v", err)
	}
	sql := string(source)
	for _, queryName := range []string{
		"ListPublicAccounts",
		"FindPublicAccountByID",
		"FindPublicAccountByIDForUpdate",
		"FindExistingPublicAccountByNameInGroup",
	} {
		start := strings.Index(sql, "-- name: "+queryName+" ")
		if start < 0 {
			t.Fatalf("public account SQL missing %s", queryName)
		}
		next := strings.Index(sql[start+1:], "\n-- name: ")
		end := len(sql)
		if next >= 0 {
			end = start + 1 + next
		}
		if query := sql[start:end]; !strings.Contains(query, "accounts.health_check_model") {
			t.Fatalf("%s query missing accounts.health_check_model:\n%s", queryName, query)
		}
	}

	start := strings.Index(sql, "-- name: InsertPublicAccount :one")
	end := strings.Index(sql, "-- name: InsertPublicAccountGroupBinding :exec")
	if start < 0 || end <= start {
		t.Fatal("public account SQL missing insert query")
	}
	insertQuery := sql[start:end]
	for _, want := range []string{
		"health_check_model,",
		"sqlc.arg(health_check_model)",
	} {
		if !strings.Contains(insertQuery, want) {
			t.Fatalf("public account insert query missing %q in:\n%s", want, insertQuery)
		}
	}
}

func TestPublicAccountProviderProfileFromRowDecodesDefaultSupportedModels(t *testing.T) {
	profile, err := publicAccountProviderProfileFromRow(postgresqueries.FindPublicAccountProviderProfileRow{
		ID:                         "profile_gpt_openai_v1",
		ProviderCode:               "gpt",
		Name:                       "GPT / OpenAI v1",
		ProfileEnabled:             true,
		ProviderEnabled:            true,
		DefaultSupportedModelsJson: `[" gpt-5.6-sol ","gpt-5.6-terra","gpt-5.6-sol","gpt-5.6-luna",""]`,
		DefaultHealthCheckModel:    " gpt-5.6-sol ",
		ProtocolCode:               "openai",
		ProtocolVersion:            "v1",
		AccountTypesJson:           `["oauth","api_key"]`,
	})
	if err != nil {
		t.Fatalf("publicAccountProviderProfileFromRow() error = %v", err)
	}
	wantModels := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	if !slices.Equal(profile.DefaultSupportedModels, wantModels) {
		t.Fatalf("default supported models = %#v, want %#v", profile.DefaultSupportedModels, wantModels)
	}
	if profile.ID != "profile_gpt_openai_v1" || profile.ProviderCode != "gpt" || profile.AccountTypesJSON != `["oauth","api_key"]` {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.DefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("default health check model = %q, want gpt-5.6-sol", profile.DefaultHealthCheckModel)
	}
}

func TestPublicAccountProviderProfileFromRowRejectsMalformedDefaultSupportedModels(t *testing.T) {
	_, err := publicAccountProviderProfileFromRow(postgresqueries.FindPublicAccountProviderProfileRow{
		DefaultSupportedModelsJson: `{"model":"gpt-5.6-sol"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "provider default_supported_models_json") {
		t.Fatalf("publicAccountProviderProfileFromRow() error = %v, want default model decode error", err)
	}
}

func TestPublicAccountGoFreshSeedIncludesGPT56DefaultModels(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000008_w2_management_provider_options.sql")
	if err != nil {
		t.Fatalf("read Go fresh provider seed: %v", err)
	}
	sql := string(source)
	wantModels := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	for _, providerCode := range []string{"gpt", "openai"} {
		models := publicAccountSeededProviderModels(t, sql, providerCode)
		for _, model := range wantModels {
			if !slices.Contains(models, model) {
				t.Fatalf("provider %q default models = %#v, missing %q", providerCode, models, model)
			}
		}
	}
}

func publicAccountSeededProviderModels(t *testing.T, sql string, providerCode string) []string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)\('` + regexp.QuoteMeta(providerCode) + `'\s*,\s*'` + regexp.QuoteMeta(providerCode) + `'.*?\btrue\s*,\s*'(\[[^']*\])'`)
	matches := pattern.FindStringSubmatch(sql)
	if len(matches) != 2 {
		t.Fatalf("Go fresh provider seed missing default models for %q", providerCode)
	}
	var models []string
	if err := json.Unmarshal([]byte(matches[1]), &models); err != nil {
		t.Fatalf("decode Go fresh provider %q default models: %v", providerCode, err)
	}
	return models
}
