package managementusagerecords

import (
	"context"
	"reflect"
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

func TestServiceDetailPreservesSnapshotsAndBuildsFallbackCostBreakdown(t *testing.T) {
	store := &usageRecordReaderStub{
		detailFound: true,
		detail: port.ManagementUsageRecordDetail{
			ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
				ID:             "usage_1",
				TraceID:        "trace_1",
				TrafficSource:  "gateway",
				Success:        true,
				CostUSD:        floatPointer(0.25),
				ThinkingTokens: int64Pointer(7),
				CreatedAt:      time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
			},
			RequestSnapshotJSON:  `{"authorization":"Bearer raw-token"}`,
			ResponseSnapshotJSON: `{"answer":"raw-response"}`,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: "usage_1", ScopeSystemAccountID: "sys_user"})
	if err != nil || !found {
		t.Fatalf("Detail = found %v err %v", found, err)
	}
	if store.detailInput.ID != "usage_1" || store.detailInput.SystemAccountID != "sys_user" {
		t.Fatalf("detail input = %+v", store.detailInput)
	}
	if !reflect.DeepEqual(detail.RequestSnapshot, map[string]any{"authorization": "Bearer raw-token"}) ||
		!reflect.DeepEqual(detail.ResponseSnapshot, map[string]any{"answer": "raw-response"}) {
		t.Fatalf("snapshots = %#v / %#v", detail.RequestSnapshot, detail.ResponseSnapshot)
	}
	if detail.CostBreakdown == nil || detail.CostBreakdown.AccountChargeUSD == nil || *detail.CostBreakdown.AccountChargeUSD != 0.25 || detail.CostBreakdown.ThinkingTokens == nil || *detail.CostBreakdown.ThinkingTokens != 7 {
		t.Fatalf("cost breakdown = %+v", detail.CostBreakdown)
	}
}

type usageRecordReaderStub struct {
	timezone    string
	timezoneErr error
	listInput   port.ManagementUsageRecordListInput
	listResult  port.ManagementUsageRecordListResult
	listErr     error
	detailInput port.ManagementUsageRecordDetailInput
	detail      port.ManagementUsageRecordDetail
	detailFound bool
	detailErr   error
}

func (s *usageRecordReaderStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.timezone != "", s.timezoneErr
}

func (s *usageRecordReaderStub) ListManagementUsageRecords(_ context.Context, input port.ManagementUsageRecordListInput) (port.ManagementUsageRecordListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *usageRecordReaderStub) GetManagementUsageRecord(_ context.Context, input port.ManagementUsageRecordDetailInput) (port.ManagementUsageRecordDetail, bool, error) {
	s.detailInput = input
	return s.detail, s.detailFound, s.detailErr
}

func textPointer(value string) *string    { return &value }
func floatPointer(value float64) *float64 { return &value }
func int64Pointer(value int64) *int64     { return &value }
