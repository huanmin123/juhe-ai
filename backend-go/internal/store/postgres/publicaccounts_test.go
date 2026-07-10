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

func TestFindPublicAccountProviderProfileQuerySelectsDefaultSupportedModels(t *testing.T) {
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
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("provider profile query missing %q in:\n%s", want, query)
		}
	}
}

func TestUpdatePublicAccountQueryClearsDefaultTestModelOnlyForChangedSupportedModels(t *testing.T) {
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
	for _, want := range []string{
		"default_test_model = CASE",
		"sqlc.arg(supported_models_changed)::boolean",
		"default_test_model IS NOT NULL",
		"default_test_model <> ALL(COALESCE(sqlc.arg(supported_models)::text[], ARRAY[]::text[]))",
		"THEN NULL",
		"ELSE default_test_model",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("public account update query missing %q in:\n%s", want, query)
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
