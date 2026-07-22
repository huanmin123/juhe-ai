package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestGatewayClientCatalogQueryIsScopedAndBounded(t *testing.T) {
	source, err := os.ReadFile("queries/w8_gateway_client_catalog.sql")
	if err != nil {
		t.Fatalf("read gateway client catalog query: %v", err)
	}
	sql := strings.ToLower(string(source))
	for _, fragment := range []string{
		"limit 50",
		"profiles.enabled = true",
		"providers.enabled = true",
		"partition by protocol_code, protocol_version",
		"protocol_rank <= 50",
		"protocol_code = 'openai'",
		"protocol_version = 'v1'",
		"protocol_code = 'anthropic'",
		"protocol_code = 'gemini'",
		"protocol_version = 'v1beta'",
		"catalog_visible = true",
		"scope = 'global'",
		"scope = 'personal'",
		"system_account_id = sqlc.arg(system_account_id)",
		"service_tier_prices_json",
		"supported_api_protocols_json",
		"supported_service_tiers_json",
		"codex_supported_reasoning_levels_json",
		"codex_default_reasoning_level",
		"codex_multi_agent_version",
		"context_window_tokens",
		"max_input_tokens",
		"max_output_tokens",
		"input_usd_per_1m",
		"output_usd_per_1m",
		"cached_input_usd_per_1m",
		"cache_write_usd_per_1m",
		"cache_write_1h_usd_per_1m",
		"image_input_usd_per_1m",
		"image_output_usd_per_1m",
		"audio_input_usd_per_1m",
		"audio_output_usd_per_1m",
		"output_usd_per_image",
		"limit 20001",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("query missing %q", fragment)
		}
	}
	if strings.Contains(sql, "catalog_visible = 1") {
		t.Fatal("PostgreSQL boolean catalog_visible must not be compared with integer 1")
	}
}

func TestListGatewayClientCatalogProvidersMapsRows(t *testing.T) {
	q := &gatewayClientCatalogQueriesStub{providers: []postgresqueries.ListGatewayClientCatalogProvidersRow{
		{Code: "gpt", Enabled: true},
		{Code: "glm", Enabled: false},
	}}
	got, err := listGatewayClientCatalogProviders(context.Background(), q)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	want := []port.GatewayClientCatalogProvider{{Code: "gpt", Enabled: true}, {Code: "glm", Enabled: false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %#v, want %#v", got, want)
	}
}

func TestListGatewayClientCatalogModelsNormalizesScopeAndDecodesFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	q := &gatewayClientCatalogQueriesStub{models: []postgresqueries.ListGatewayClientCatalogModelsRow{{
		RequestedProviderCode:             "openai",
		ProviderCode:                      "gpt",
		Model:                             "gpt-5.6",
		Scope:                             "personal",
		SystemAccountID:                   pgtype.Text{String: "owner-1", Valid: true},
		Status:                            "active",
		CatalogVisible:                    true,
		ReleaseDate:                       pgtype.Text{String: "2026-07-01", Valid: true},
		CreatedAt:                         pgtype.Timestamptz{Time: createdAt, Valid: true},
		SupportedApiProtocolsJson:         `["responses","chat_completions"]`,
		SupportedServiceTiersJson:         `["priority"]`,
		CodexSupportedReasoningLevelsJson: `["high","xhigh"]`,
		CodexDefaultReasoningLevel:        pgtype.Text{String: "high", Valid: true},
		CodexMultiAgentVersion:            pgtype.Text{String: "v2", Valid: true},
		ContextWindowTokens:               pgtype.Int4{Int32: 272000, Valid: true},
		MaxInputTokens:                    pgtype.Int4{Int32: 250000, Valid: true},
		MaxOutputTokens:                   pgtype.Int4{Int32: 22000, Valid: true},
		InputUsdPer1m:                     pgtype.Float8{Float64: 1.25, Valid: true},
		ServiceTierPricesJson:             `{"priority":{"outputUsdPer1M":9.5}}`,
		PricingNotes:                      pgtype.Text{String: "price", Valid: true},
		CapabilityNotes:                   pgtype.Text{String: "capability", Valid: true},
		Notes:                             pgtype.Text{String: "notes", Valid: true},
	}}}

	got, err := listGatewayClientCatalogModels(context.Background(), q, port.GatewayClientCatalogModelListInput{
		LogicalProviderCodes: []string{" GPT ", "openai", "gpt"},
		SystemAccountID:      " owner-1 ",
	})
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	wantParams := []postgresqueries.ListGatewayClientCatalogModelsParams{{
		LogicalProviderCodes: []string{"gpt", "openai"},
		SystemAccountID:      "owner-1",
	}}
	if !reflect.DeepEqual(q.modelInputs, wantParams) {
		t.Fatalf("model params = %#v, want %#v", q.modelInputs, wantParams)
	}
	if len(got) != 1 {
		t.Fatalf("models = %#v", got)
	}
	item := got[0]
	if item.RequestedProviderCode != "openai" || item.ProviderCode != "gpt" || item.SystemAccountID != "owner-1" {
		t.Fatalf("scope fields = %#v", item)
	}
	if !reflect.DeepEqual(item.SupportedAPIProtocols, []string{"responses", "chat_completions"}) ||
		!reflect.DeepEqual(item.SupportedServiceTiers, []string{"priority"}) ||
		!reflect.DeepEqual(item.CodexSupportedReasoningLevels, []string{"high", "xhigh"}) {
		t.Fatalf("decoded arrays = %#v", item)
	}
	if item.ContextWindowTokens == nil || *item.ContextWindowTokens != 272000 ||
		item.InputUSDPer1M == nil || *item.InputUSDPer1M != 1.25 ||
		item.ServiceTierPrices["priority"].OutputUSDPer1M == nil || *item.ServiceTierPrices["priority"].OutputUSDPer1M != 9.5 {
		t.Fatalf("decoded prices/capacity = %#v", item)
	}
	if !item.CreatedAt.Equal(createdAt) || item.PricingNotes != "price" || item.CapabilityNotes != "capability" || item.Notes != "notes" {
		t.Fatalf("metadata = %#v", item)
	}
}

func TestListGatewayClientCatalogModelsRejectsOversizedScope(t *testing.T) {
	codes := make([]string, 51)
	for index := range codes {
		codes[index] = "provider-" + string(rune('a'+index))
	}
	q := &gatewayClientCatalogQueriesStub{}
	_, err := listGatewayClientCatalogModels(context.Background(), q, port.GatewayClientCatalogModelListInput{LogicalProviderCodes: codes})
	if err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("error = %v, want provider bound", err)
	}
	if len(q.modelInputs) != 0 {
		t.Fatalf("query called with %#v", q.modelInputs)
	}
}

func TestListGatewayClientCatalogModelsRejectsTruncatedAndInvalidRows(t *testing.T) {
	tooMany := make([]postgresqueries.ListGatewayClientCatalogModelsRow, maxGatewayClientCatalogModels+1)
	q := &gatewayClientCatalogQueriesStub{models: tooMany}
	_, err := listGatewayClientCatalogModels(context.Background(), q, port.GatewayClientCatalogModelListInput{LogicalProviderCodes: []string{"gpt"}})
	if err == nil || !strings.Contains(err.Error(), "exceeds 20000") {
		t.Fatalf("oversized result error = %v", err)
	}

	q = &gatewayClientCatalogQueriesStub{models: []postgresqueries.ListGatewayClientCatalogModelsRow{{
		SupportedApiProtocolsJson:         `["responses"]`,
		SupportedServiceTiersJson:         `[]`,
		CodexSupportedReasoningLevelsJson: `[]`,
		ServiceTierPricesJson:             `{invalid`,
	}}}
	_, err = listGatewayClientCatalogModels(context.Background(), q, port.GatewayClientCatalogModelListInput{LogicalProviderCodes: []string{"gpt"}})
	if err == nil || !strings.Contains(err.Error(), "service_tier_prices_json") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func TestGatewayClientCatalogReaderPropagatesQueryErrors(t *testing.T) {
	providerErr := errors.New("provider query failed")
	if _, err := listGatewayClientCatalogProviders(context.Background(), &gatewayClientCatalogQueriesStub{providersErr: providerErr}); !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v", err)
	}
	modelErr := errors.New("model query failed")
	if _, err := listGatewayClientCatalogModels(context.Background(), &gatewayClientCatalogQueriesStub{modelsErr: modelErr}, port.GatewayClientCatalogModelListInput{LogicalProviderCodes: []string{"gpt"}}); !errors.Is(err, modelErr) {
		t.Fatalf("model error = %v", err)
	}
}

type gatewayClientCatalogQueriesStub struct {
	providers    []postgresqueries.ListGatewayClientCatalogProvidersRow
	models       []postgresqueries.ListGatewayClientCatalogModelsRow
	modelInputs  []postgresqueries.ListGatewayClientCatalogModelsParams
	providersErr error
	modelsErr    error
}

func (s *gatewayClientCatalogQueriesStub) ListGatewayClientCatalogProviders(context.Context) ([]postgresqueries.ListGatewayClientCatalogProvidersRow, error) {
	return s.providers, s.providersErr
}

func (s *gatewayClientCatalogQueriesStub) ListGatewayClientCatalogModels(_ context.Context, input postgresqueries.ListGatewayClientCatalogModelsParams) ([]postgresqueries.ListGatewayClientCatalogModelsRow, error) {
	s.modelInputs = append(s.modelInputs, input)
	return s.models, s.modelsErr
}
