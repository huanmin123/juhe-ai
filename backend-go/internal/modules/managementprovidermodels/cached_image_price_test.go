package managementprovidermodels

import (
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestCatalogItemFromPortPreservesCachedImageInputPrice(t *testing.T) {
	price := 2.5
	item := catalogItemFromPort(port.ManagementProviderModelCatalogItem{
		ProviderCode:              "gpt",
		Model:                     "gpt-image-1",
		Scope:                     "built_in",
		Mode:                      "image",
		CachedImageInputUSDPer1M:  &price,
		SupportedAPIProtocols:     []string{"images"},
		SupportedServiceTiers:     []string{},
		SupportedReasoningEfforts: []string{},
	})
	if item.CachedImageInputUSDPer1M == nil || *item.CachedImageInputUSDPer1M != price {
		t.Fatalf("cached image input price = %v, want %v", item.CachedImageInputUSDPer1M, price)
	}
	foundDisplayPrice := false
	for _, section := range item.CatalogDisplay {
		for _, displayItem := range section.Items {
			if displayItem.Key == "image_cache_read" {
				foundDisplayPrice = true
			}
		}
	}
	if !foundDisplayPrice {
		t.Fatal("catalog display did not receive cached image input pricing facts")
	}
}
