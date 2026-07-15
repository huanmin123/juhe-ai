package postgres

import (
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestMarshalManagementProviderModelPriceMapNormalizesNilToObject(t *testing.T) {
	encoded, err := marshalManagementProviderModelPriceMap(nil)
	if err != nil {
		t.Fatalf("marshalManagementProviderModelPriceMap(nil) error = %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("marshalManagementProviderModelPriceMap(nil) = %s, want {}", encoded)
	}

	price := 1.25
	encoded, err = marshalManagementProviderModelPriceMap(map[string]port.ManagementProviderModelPriceSet{
		"priority": {InputUSDPer1M: &price},
	})
	if err != nil {
		t.Fatalf("marshalManagementProviderModelPriceMap(non-nil) error = %v", err)
	}
	if string(encoded) != `{"priority":{"inputUsdPer1M":1.25}}` {
		t.Fatalf("marshalManagementProviderModelPriceMap(non-nil) = %s", encoded)
	}
}
