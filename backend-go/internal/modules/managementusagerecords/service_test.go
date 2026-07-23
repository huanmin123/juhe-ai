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
