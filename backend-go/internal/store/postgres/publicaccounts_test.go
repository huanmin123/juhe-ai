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
		"profiles.capabilities_json",
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

func TestUpdatePublicAccountQueryWritesAndReturnsHealthCheckModel(t *testing.T) {
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
	if !strings.Contains(query[:whereIndex], "health_check_model = sqlc.arg(health_check_model)") {
		t.Fatalf("public account update query must write health_check_model:\n%s", query)
	}
	if !strings.Contains(query[whereIndex:], "health_check_model") {
		t.Fatalf("public account update query must return health_check_model:\n%s", query)
	}
}

func TestUpdatePublicAccountQueryIncrementsConfigRevisionInMainUpdate(t *testing.T) {
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
	if !strings.Contains(query[:whereIndex], "config_revision = config_revision + 1") {
		t.Fatalf("public account main update must increment config_revision:\n%s", query)
	}
}

func TestPublicAccountStoreOnlyUpdatesGroupDispatchWhenExplicitlyChanged(t *testing.T) {
	source, err := os.ReadFile("publicaccounts.go")
	if err != nil {
		t.Fatalf("read public account store: %v", err)
	}
	store := string(source)
	guard := "if input.GroupDispatchChanged {"
	guardIndex := strings.Index(store, guard)
	dispatchIndex := strings.Index(store, "q.UpdatePublicAccountGroupBindingDispatch")
	if guardIndex < 0 || dispatchIndex <= guardIndex {
		t.Fatalf("public account group dispatch update must be guarded by %q", guard)
	}
	blockEnd := strings.Index(store[guardIndex:], "\n\t}")
	if blockEnd < 0 || dispatchIndex >= guardIndex+blockEnd {
		t.Fatalf("public account group dispatch call must remain inside %q", guard)
	}
}

func TestUpdatePublicAccountQuerySeparatesFailureResetAndHealthCheckScheduling(t *testing.T) {
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
		"sqlc.arg(reset_failure_state)::boolean",
		"sqlc.arg(status)::text = 'pending_test'",
		"sqlc.arg(schedule_health_check)::boolean",
		"sqlc.arg(reset_health_diagnostics)::boolean",
		"cooldown_until = CASE",
		"last_error_code = CASE",
		"last_error_message = CASE",
		"账户配置已保存，等待后台检查",
		"next_health_check_at = CASE",
		"health_check_failure_count = CASE",
		"health_check_failure_started_at = CASE",
		"last_health_check_status_code = CASE",
		"last_health_check_error_code = CASE",
		"last_health_check_error_message = CASE",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("public account update query missing %q in:\n%s", want, query)
		}
	}
	for _, removed := range []string{"configuration_changed", "health_check_model_changed"} {
		if strings.Contains(query, removed) {
			t.Fatalf("public account update query still contains removed flag %q in:\n%s", removed, query)
		}
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
		CapabilitiesJson:           `["responses","stream_responses","chat","models","messages","generate_content","interactions","passthrough","bridge"]`,
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
	wantModes := []string{
		"responses_json", "responses_sse",
		"chat_json", "chat_sse",
		"messages_json", "messages_sse",
		"generate_content_json", "generate_content_sse",
		"interactions_json", "interactions_sse",
	}
	if !slices.Equal(profile.EnabledEndpointModes, wantModes) {
		t.Fatalf("enabled endpoint modes = %#v, want %#v", profile.EnabledEndpointModes, wantModes)
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

func TestPublicAccountProviderProfileFromRowRejectsMalformedCapabilities(t *testing.T) {
	_, err := publicAccountProviderProfileFromRow(postgresqueries.FindPublicAccountProviderProfileRow{
		DefaultSupportedModelsJson: `[]`,
		CapabilitiesJson:           `{"chat":true}`,
	})
	if err == nil || !strings.Contains(err.Error(), "provider profile capabilities_json") {
		t.Fatalf("publicAccountProviderProfileFromRow() error = %v, want capabilities decode error", err)
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

func TestPublicAccountGoFreshSeedIncludesProviderAuthAndInteractionsProfiles(t *testing.T) {
	baseline, err := os.ReadFile("../../../db/migrations/000008_w2_management_provider_options.sql")
	if err != nil {
		t.Fatalf("read Go fresh provider baseline: %v", err)
	}
	catchUp, err := os.ReadFile("../../../db/migrations/000060_w2_provider_auth_protocol_schema_20260718.sql")
	if err != nil {
		t.Fatalf("read Go provider auth catch-up: %v", err)
	}
	sql := string(baseline) + "\n" + string(catchUp)
	for _, required := range []string{
		"'xai', 'xai', 'xAI / Grok', 'openai'",
		"('profile_xai_openai_v1', 'xai'",
		"('profile_anthropic_anthropic_v1', 'anthropic'",
		"'[\"api_key\"]'",
		"'[\"api_key\",\"google_oauth\"]'",
		"'gemini_v1beta_interactions'",
		"'profile_gemini_native_v1beta', 'interactions'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Go fresh provider seed missing %q", required)
		}
	}
	if strings.Contains(sql, "workload_identity") {
		t.Fatal("Go fresh provider seed must not expose workload_identity")
	}
}

func TestPublicAccountSchemaAllowsCurrentAccountTypesAndInteractionsHealthModes(t *testing.T) {
	baseline, err := os.ReadFile("../../../db/migrations/000005_w1b_public_accounts.sql")
	if err != nil {
		t.Fatalf("read Go public account baseline: %v", err)
	}
	catchUp, err := os.ReadFile("../../../db/migrations/000060_w2_provider_auth_protocol_schema_20260718.sql")
	if err != nil {
		t.Fatalf("read Go public account catch-up: %v", err)
	}
	sql := string(baseline) + "\n" + string(catchUp)
	for _, required := range []string{
		"CHECK (type IN ('api_key', 'oauth', 'google_oauth'))",
		"'interactions_json', 'interactions_sse'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Go public account schema missing %q", required)
		}
	}
	if strings.Contains(sql, "workload_identity") {
		t.Fatal("Go public account schema must not allow workload_identity")
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
