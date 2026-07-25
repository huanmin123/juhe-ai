package managementusagerecords

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListDefaultsToTodayAndScopesSelf(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	store := &usageRecordReaderStub{
		timezone: "Asia/Shanghai",
		listResult: port.ManagementUsageRecordListResult{
			Items: []port.ManagementUsageRecordSummary{{
				ID:              "usage_1",
				SystemAccountID: textPointer("sys_user"),
				TraceID:         "trace_1",
				TrafficSource:   "gateway",
				Success:         true,
				CreatedAt:       now,
			}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	result, err := service.List(context.Background(), ListInput{
		ScopeSystemAccountID: "sys_user",
		IncludeSystemAccount: false,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantStart := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)
	if store.listInput.SystemAccountID != "sys_user" || !store.listInput.StartAt.Equal(wantStart) || !store.listInput.EndAt.Equal(wantEnd) {
		t.Fatalf("store input = %+v", store.listInput)
	}
	if result.Page != 1 || result.PageSize != 50 || result.Total != 1 || result.HasMore || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].SystemAccountID != "" || result.Items[0].CreatedAt != "2026-07-21T03:04:05.000Z" {
		t.Fatalf("item = %+v", result.Items[0])
	}
}

func TestServiceListInvalidNonEmptyDateSuppressesDefaultRange(t *testing.T) {
	store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC) },
	})

	if _, err := service.List(context.Background(), ListInput{StartDate: "2026-02-31"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !store.listInput.StartAt.IsZero() || !store.listInput.EndAt.IsZero() {
		t.Fatalf("invalid non-empty date must suppress default range: %+v", store.listInput)
	}
}

func TestSummaryJSONIncludesFalseModelMappingAppliedAndExcludesSnapshots(t *testing.T) {
	payload, err := json.Marshal(Summary{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	value, exists := decoded["modelMappingApplied"]
	if !exists || value != false {
		t.Fatalf("modelMappingApplied = %#v exists = %v payload = %s", value, exists, payload)
	}
	for _, key := range []string{"requestSnapshot", "responseSnapshot"} {
		if _, exists := decoded[key]; exists {
			t.Fatalf("list summary must not contain %s: %s", key, payload)
		}
	}
}

func TestCostBreakdownSnapshotIncludesProviderBillingMetadataAndLegacyFields(t *testing.T) {
	snapshot := `{
		"currency":"USD",
		"billingPolicy":"gemini",
		"lineItems":[{
			"key":"cache_storage",
			"kind":"other",
			"label":"缓存存储",
			"quantity":1500000.5,
			"unit":"token_hour",
			"unitSize":1000000,
			"unitPriceUsd":1,
			"costUsd":1.5000005
		}],
		"inputCostUsd":0.25,
		"cacheReadUsdPer1M":0.1,
		"accountChargeUsd":1.7500005,
		"multiplier":1,
		"serviceTierPricingSource":"default"
	}`
	breakdown := costBreakdown(port.ManagementUsageRecordSummary{
		CostBreakdownSnapshotJSON: textPointer(snapshot),
	})
	if breakdown.Currency != "USD" || breakdown.BillingPolicy != "gemini" {
		t.Fatalf("provider billing metadata = %+v", breakdown)
	}
	if breakdown.LineItems == nil || len(*breakdown.LineItems) != 1 {
		t.Fatalf("lineItems = %+v", breakdown.LineItems)
	}
	line := (*breakdown.LineItems)[0]
	if line.Key != "cache_storage" || line.Kind != "other" || line.Label != "缓存存储" ||
		line.Quantity != 1500000.5 || line.Unit != "token_hour" || line.UnitSize != 1000000 ||
		line.UnitPriceUSD != 1 || line.CostUSD != 1.5000005 {
		t.Fatalf("line item = %+v", line)
	}
	if breakdown.InputCostUSD == nil || *breakdown.InputCostUSD != 0.25 ||
		breakdown.CacheReadUSDPer1M == nil || *breakdown.CacheReadUSDPer1M != 0.1 ||
		breakdown.AccountChargeUSD == nil || *breakdown.AccountChargeUSD != 1.7500005 ||
		breakdown.Multiplier != 1 || breakdown.ServiceTierPricingSource != "default" {
		t.Fatalf("legacy fields = %+v", breakdown)
	}

	payload, err := json.Marshal(breakdown)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"currency", "billingPolicy", "lineItems", "inputCostUsd", "cacheReadUsdPer1M", "accountChargeUsd", "multiplier", "serviceTierPricingSource"} {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("cost breakdown must contain %s: %s", key, payload)
		}
	}
}

func TestCostBreakdownSnapshotPreservesPresentEmptyLineItems(t *testing.T) {
	snapshot := `{"lineItems":[],"multiplier":1,"serviceTierPricingSource":"default"}`
	breakdown := costBreakdown(port.ManagementUsageRecordSummary{CostBreakdownSnapshotJSON: textPointer(snapshot)})
	payload, err := json.Marshal(breakdown)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	lineItems, exists := decoded["lineItems"]
	if !exists {
		t.Fatalf("present empty lineItems must be preserved: %s", payload)
	}
	if items, ok := lineItems.([]any); !ok || len(items) != 0 {
		t.Fatalf("lineItems = %#v", lineItems)
	}
}

func TestCostBreakdownLegacySnapshotDoesNotInventProviderBillingMetadata(t *testing.T) {
	snapshot := `{"accountChargeUsd":0.5,"multiplier":1,"serviceTierPricingSource":"unknown"}`
	breakdown := costBreakdown(port.ManagementUsageRecordSummary{CostBreakdownSnapshotJSON: textPointer(snapshot)})
	payload, err := json.Marshal(breakdown)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"currency", "billingPolicy", "lineItems"} {
		if _, exists := decoded[key]; exists {
			t.Fatalf("legacy snapshot must not invent %s: %s", key, payload)
		}
	}
	if decoded["accountChargeUsd"] != 0.5 {
		t.Fatalf("legacy accountChargeUsd = %#v", decoded["accountChargeUsd"])
	}
}

type usageRecordReaderStub struct {
	timezone    string
	timezoneErr error
	listInput   port.ManagementUsageRecordListInput
	listResult  port.ManagementUsageRecordListResult
	listErr     error
}

func (s *usageRecordReaderStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.timezone != "", s.timezoneErr
}

func (s *usageRecordReaderStub) ListManagementUsageRecords(_ context.Context, input port.ManagementUsageRecordListInput) (port.ManagementUsageRecordListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func textPointer(value string) *string { return &value }
