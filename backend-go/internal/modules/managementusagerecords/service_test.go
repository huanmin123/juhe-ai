package managementusagerecords

import (
	"context"
	"encoding/json"
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
	if detail.RequestSnapshot == nil || detail.ResponseSnapshot == nil ||
		!reflect.DeepEqual(*detail.RequestSnapshot, map[string]any{"authorization": "Bearer raw-token"}) ||
		!reflect.DeepEqual(*detail.ResponseSnapshot, map[string]any{"answer": "raw-response"}) {
		t.Fatalf("snapshots = %#v / %#v", detail.RequestSnapshot, detail.ResponseSnapshot)
	}
	if detail.CostBreakdown == nil || detail.CostBreakdown.AccountChargeUSD == nil || *detail.CostBreakdown.AccountChargeUSD != 0.25 || detail.CostBreakdown.ThinkingTokens == nil || *detail.CostBreakdown.ThinkingTokens != 7 {
		t.Fatalf("cost breakdown = %+v", detail.CostBreakdown)
	}
}

func TestServiceDetailFallsBackToEndpointFromRequestSnapshot(t *testing.T) {
	store := &usageRecordReaderStub{
		detailFound: true,
		detail: port.ManagementUsageRecordDetail{
			ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
				ID:            "usage_1",
				TraceID:       "trace_1",
				TrafficSource: "gateway",
				CreatedAt:     time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
			},
			RequestSnapshotJSON: `{"method":"post","originalUrl":"/v1/responses?stream=true","path":"/ignored"}`,
		},
	}

	detail, found, err := NewService(store).Detail(context.Background(), DetailInput{ID: "usage_1"})
	if err != nil || !found {
		t.Fatalf("Detail = found %v err %v", found, err)
	}
	if detail.Endpoint != "POST /v1/responses" {
		t.Fatalf("endpoint = %q", detail.Endpoint)
	}
}

func TestServiceDetailEndpointFallbackBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		stored       *string
		snapshotJSON string
		want         string
	}{
		{name: "path defaults to GET", snapshotJSON: `{"path":"/v1/chat/completions"}`, want: "GET /v1/chat/completions"},
		{name: "stored endpoint wins", stored: textPointer("PATCH /stored"), snapshotJSON: `{"method":"post","originalUrl":"/ignored"}`, want: "PATCH /stored"},
		{name: "empty original url does not fall through to path", snapshotJSON: `{"originalUrl":"","path":"/ignored"}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &usageRecordReaderStub{
				detailFound: true,
				detail: port.ManagementUsageRecordDetail{
					ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
						ID: "usage_1", TraceID: "trace_1", TrafficSource: "gateway", Endpoint: tt.stored,
						CreatedAt: time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
					},
					RequestSnapshotJSON: tt.snapshotJSON,
				},
			}
			detail, found, err := NewService(store).Detail(context.Background(), DetailInput{ID: "usage_1"})
			if err != nil || !found || detail.Endpoint != tt.want {
				t.Fatalf("endpoint = %q found = %v err = %v", detail.Endpoint, found, err)
			}
		})
	}
}

func TestSummaryJSONIncludesFalseModelMappingApplied(t *testing.T) {
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
}

func TestServiceDetailRejectsNonObjectSnapshots(t *testing.T) {
	for _, tt := range []struct {
		name     string
		request  string
		response string
	}{
		{name: "invalid request json", request: `{`},
		{name: "null request", request: `null`},
		{name: "non ECMAScript whitespace request", request: "\u0085"},
		{name: "array response", request: `{}`, response: `[]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &usageRecordReaderStub{
				detailFound: true,
				detail: port.ManagementUsageRecordDetail{
					ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
						ID: "usage_1", TraceID: "trace_1", TrafficSource: "gateway",
						CreatedAt: time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
					},
					RequestSnapshotJSON:  tt.request,
					ResponseSnapshotJSON: tt.response,
				},
			}
			if _, found, err := NewService(store).Detail(context.Background(), DetailInput{ID: "usage_1"}); err == nil || found {
				t.Fatalf("found = %v err = %v", found, err)
			}
		})
	}
}

func TestServiceDetailTreatsECMAScriptWhitespaceSnapshotsAsMissing(t *testing.T) {
	store := &usageRecordReaderStub{
		detailFound: true,
		detail: port.ManagementUsageRecordDetail{
			ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
				ID: "usage_1", TraceID: "trace_1", TrafficSource: "gateway",
				CreatedAt: time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
			},
			RequestSnapshotJSON:  "\uFEFF",
			ResponseSnapshotJSON: "\u3000\uFEFF",
		},
	}
	detail, found, err := NewService(store).Detail(context.Background(), DetailInput{ID: "usage_1"})
	if err != nil || !found || detail.RequestSnapshot != nil || detail.ResponseSnapshot != nil {
		t.Fatalf("detail = %+v found = %v err = %v", detail, found, err)
	}
}

func TestServiceDetailJSONPreservesEmptySnapshotObjects(t *testing.T) {
	store := &usageRecordReaderStub{
		detailFound: true,
		detail: port.ManagementUsageRecordDetail{
			ManagementUsageRecordSummary: port.ManagementUsageRecordSummary{
				ID: "usage_1", TraceID: "trace_1", TrafficSource: "gateway",
				CreatedAt: time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC),
			},
			RequestSnapshotJSON:  `{}`,
			ResponseSnapshotJSON: `{}`,
		},
	}
	detail, found, err := NewService(store).Detail(context.Background(), DetailInput{ID: "usage_1"})
	if err != nil || !found {
		t.Fatalf("Detail = found %v err %v", found, err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"requestSnapshot", "responseSnapshot"} {
		value, exists := decoded[key]
		object, ok := value.(map[string]any)
		if !exists || !ok || len(object) != 0 {
			t.Fatalf("%s = %#v exists = %v payload = %s", key, value, exists, payload)
		}
	}
}

type usageRecordReaderStub struct {
	timezone    string
	timezoneErr error
	listInput   port.ManagementUsageRecordListInput
	listResult  port.ManagementUsageRecordListResult
	listErr     error
	listCalls   int
	detailInput port.ManagementUsageRecordDetailInput
	detail      port.ManagementUsageRecordDetail
	detailFound bool
	detailErr   error
}

func (s *usageRecordReaderStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.timezone != "", s.timezoneErr
}

func (s *usageRecordReaderStub) ListManagementUsageRecords(_ context.Context, input port.ManagementUsageRecordListInput) (port.ManagementUsageRecordListResult, error) {
	s.listCalls++
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
