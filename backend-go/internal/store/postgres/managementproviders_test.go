package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementProviderOptionFromRowUsesPreferredDefaultProfile(t *testing.T) {
	profilesByProvider, err := managementProviderProfilesByProvider([]postgresqueries.ListManagementProviderOptionProfilesRow{
		{
			ID:               "profile_gemini_openai_chat_v1beta",
			ProviderCode:     "gemini",
			Name:             "Gemini / OpenAI Chat",
			Enabled:          true,
			ProtocolCode:     "openai",
			ProtocolVersion:  "v1",
			BaseUrl:          "https://generativelanguage.googleapis.com/v1beta/openai",
			DefaultTestModel: "gemini-3.5-flash",
			AccountTypesJson: `["api_key"]`,
			CapabilitiesJson: `["chat"]`,
		},
		{
			ID:               "profile_gemini_native_v1beta",
			ProviderCode:     "gemini",
			Name:             "Gemini / Gemini v1beta",
			Enabled:          true,
			ProtocolCode:     "gemini",
			ProtocolVersion:  "v1beta",
			BaseUrl:          "https://generativelanguage.googleapis.com",
			DefaultTestModel: "gemini-3.5-flash",
			AccountTypesJson: `["api_key"]`,
			CapabilitiesJson: `["generate_content","models"]`,
		},
	}, map[string][]port.ManagementProviderEndpointFamily{})
	if err != nil {
		t.Fatalf("managementProviderProfilesByProvider() error = %v", err)
	}
	option, err := managementProviderOptionFromRow(postgresqueries.ListManagementProviderOptionProvidersRow{
		ID:                         "provider_gemini",
		Code:                       "gemini",
		Name:                       "Gemini",
		Description:                pgtype.Text{String: "Google Gemini provider", Valid: true},
		Enabled:                    true,
		DefaultSupportedModelsJson: `["gemini-3.5-flash"]`,
	}, profilesByProvider["gemini"], "gemini-custom")
	if err != nil {
		t.Fatalf("managementProviderOptionFromRow() error = %v", err)
	}

	if option.DefaultProtocolProfileID != "profile_gemini_native_v1beta" {
		t.Fatalf("default profile = %q, want gemini native", option.DefaultProtocolProfileID)
	}
	if option.ProtocolCode != "gemini" || option.DefaultTestModel != "gemini-custom" {
		t.Fatalf("option = %+v", option)
	}
	if option.ProtocolProfiles[1].DefaultTestModel != "gemini-custom" {
		t.Fatalf("default profile preference was not applied to profile: %+v", option.ProtocolProfiles)
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
		ID:                        "custom_model_1",
		ProviderCode:              "gpt",
		Model:                     "gpt-custom",
		Scope:                     "personal",
		SystemAccountID:           pgtype.Text{String: "sys_user", Valid: true},
		Status:                    "active",
		Mode:                      pgtype.Text{String: "chat", Valid: true},
		CatalogOrder:              pgtype.Int4{Int32: 10, Valid: true},
		SupportedApiProtocolsJson: `[" chat_completions ","chat_completions","responses"]`,
		ContextWindowTokens:       maxInput,
		MaxInputTokens:            maxInput,
		InputUsdPer1m:             inputPrice,
		SupportsPromptCaching:     true,
		CatalogVisible:            true,
		Source:                    "custom-personal",
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
}
