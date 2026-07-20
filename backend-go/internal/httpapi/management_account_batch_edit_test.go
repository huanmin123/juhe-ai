package httpapi

import "testing"

func TestNormalizeBatchUpdatesUsesExplicitSupportedFields(t *testing.T) {
	updates := normalizeBatchUpdates(map[string]any{
		"priority":             map[string]any{"enabled": true, "value": float64(9)},
		"availabilitySchedule": map[string]any{"enabled": true, "value": map[string]any{"timezone": "Asia/Shanghai"}},
		"tags":                 map[string]any{"enabled": true, "value": []any{"production"}},
	})
	if updates["priority"] != float64(9) {
		t.Fatalf("priority mapping missing: %#v", updates)
	}
	if updates["availability_schedule_json"] != `{"timezone":"Asia/Shanghai"}` {
		t.Fatalf("schedule mapping invalid: %#v", updates)
	}
	if _, ok := updates["tags"]; ok {
		t.Fatalf("unsupported tags must not become a SQL field: %#v", updates)
	}
}
