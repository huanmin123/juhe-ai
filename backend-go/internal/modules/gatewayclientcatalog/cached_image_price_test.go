package gatewayclientcatalog

import (
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestSelectClientModelsTreatsCachedImageInputAsVisiblePrice(t *testing.T) {
	price := 2.0
	items := SelectClientModels([]port.GatewayClientCatalogModel{{
		ProviderCode:             "gpt",
		Model:                    "gpt-image-2",
		Scope:                    "built_in",
		Status:                   "active",
		CatalogVisible:           true,
		CachedImageInputUSDPer1M: &price,
	}})
	if len(items) != 1 || items[0].CachedImageInputUSDPer1M == nil || *items[0].CachedImageInputUSDPer1M != price {
		t.Fatalf("selected items = %+v, want cached-image-only priced model", items)
	}
}
