package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementProviderModelCatalogMappingPreservesCachedImageInputPrice(t *testing.T) {
	row := postgresqueries.ListManagementProviderModelCatalogRow{
		ID: "gpt-image-2", ProviderCode: "gpt", Model: "gpt-image-2", Scope: "built_in", Status: "active",
		SupportedApiProtocolsJson: "[]", SupportedServiceTiersJson: "[]", SupportedReasoningEffortsJson: "[]",
		CodexSupportedReasoningLevelsJson: "[]", ServiceTierPricesJson: "{}", Source: "node-snapshot",
		CachedImageInputUsdPer1m: pgtype.Float8{Float64: 2, Valid: true},
	}
	item, err := managementProviderModelCatalogItemFromRow(row)
	if err != nil {
		t.Fatalf("managementProviderModelCatalogItemFromRow() error = %v", err)
	}
	if item.CachedImageInputUSDPer1M == nil || *item.CachedImageInputUSDPer1M != 2 {
		t.Fatalf("cached image input price = %v, want 2", item.CachedImageInputUSDPer1M)
	}
}

func TestGatewayClientCatalogMappingPreservesCachedImageInputPrice(t *testing.T) {
	row := postgresqueries.ListGatewayClientCatalogModelsRow{
		RequestedProviderCode: "gpt", ProviderCode: "gpt", Model: "gpt-image-2", Scope: "built_in", Status: "active",
		SupportedApiProtocolsJson: "[]", SupportedServiceTiersJson: "[]", CodexSupportedReasoningLevelsJson: "[]",
		ServiceTierPricesJson: "{}", CachedImageInputUsdPer1m: pgtype.Float8{Float64: 2, Valid: true},
	}
	item, err := gatewayClientCatalogModelFromRow(row)
	if err != nil {
		t.Fatalf("gatewayClientCatalogModelFromRow() error = %v", err)
	}
	if item.CachedImageInputUSDPer1M == nil || *item.CachedImageInputUSDPer1M != 2 {
		t.Fatalf("cached image input price = %v, want 2", item.CachedImageInputUSDPer1M)
	}
}

func TestCachedImageInputPriceSQLIsBuiltInOnly(t *testing.T) {
	managementSource, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read management provider model query: %v", err)
	}
	managementSQL := strings.ReplaceAll(string(managementSource), "\r\n", "\n")
	if !strings.Contains(managementSQL, "image_input_usd_per_1m,\n  cached_image_input_usd_per_1m,") ||
		!strings.Contains(managementSQL, "NULL::double precision AS cached_image_input_usd_per_1m") {
		t.Fatal("management catalog must read built-in cached image price and return NULL for custom models")
	}

	gatewaySource, err := os.ReadFile("queries/w8_gateway_client_catalog.sql")
	if err != nil {
		t.Fatalf("read gateway client catalog query: %v", err)
	}
	gatewaySQL := strings.ReplaceAll(string(gatewaySource), "\r\n", "\n")
	for _, want := range []string{
		"built_in.cached_image_input_usd_per_1m",
		"OR built_in.cached_image_input_usd_per_1m IS NOT NULL",
		"NULL::double precision AS cached_image_input_usd_per_1m",
	} {
		if !strings.Contains(gatewaySQL, want) {
			t.Fatalf("gateway client catalog query missing %q", want)
		}
	}
}
