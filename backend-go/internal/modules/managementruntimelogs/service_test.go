package managementruntimelogs

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListUsesBoundedProgressiveWindowAndKeywordDefaultRange(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 30, 45, 987_654_321, time.UTC)
	items := make([]port.ManagementRuntimeLogSummary, 100)
	for index := range items {
		items[index] = port.ManagementRuntimeLogSummary{
			ID:        "runtime_probe",
			Time:      "2026-07-14T11:00:00.000Z",
			Level:     "info",
			CreatedAt: "2026-07-14T11:00:01.000Z",
		}
	}
	items[0] = port.ManagementRuntimeLogSummary{
		ID:           "runtime_1",
		Time:         "2026-07-14T12:00:00.000Z",
		Level:        "warn",
		TraceID:      stringPointer("trace_1"),
		Event:        stringPointer("gateway"),
		Message:      stringPointer("slow request"),
		ErrorMessage: stringPointer("timeout"),
		CreatedAt:    "2026-07-14T12:00:01.000Z",
	}
	store := &managementRuntimeLogReaderStub{
		listResult: port.ManagementRuntimeLogListResult{
			Items:   items,
			HasMore: true,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	result, err := service.List(context.Background(), ListInput{
		Page:             99,
		PageSize:         100,
		PageSizeProvided: true,
		TraceID:          " trace_ ",
		Level:            " WARN ",
		Event:            " gateway ",
		Keyword:          " slow ",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	input := store.listInput
	if input.Limit != 100 || input.Offset != 900 {
		t.Fatalf("store window = limit %d offset %d, want 100/900", input.Limit, input.Offset)
	}
	if input.TraceID != "trace_" || input.Level != "warn" || input.Event != "gateway" || input.Keyword != "slow" {
		t.Fatalf("store filters = %+v", input)
	}
	if input.StartAt != "2026-07-14T06:30:45.987Z" || input.EndAt != "" {
		t.Fatalf("keyword range = %q - %q", input.StartAt, input.EndAt)
	}
	if result.Page != 10 || result.PageSize != 100 || result.Total != 1001 || !result.HasMore {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Items) != 100 || result.Items[0].TraceID != "trace_1" || result.Items[0].ErrorMessage != "timeout" {
		t.Fatalf("items = %+v", result.Items)
	}
}

func TestServiceListPreservesExplicitRangeAndIgnoresInvalidLevel(t *testing.T) {
	store := &managementRuntimeLogReaderStub{listResult: port.ManagementRuntimeLogListResult{Items: []port.ManagementRuntimeLogSummary{}}}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now: func() time.Time {
			t.Fatal("clock should not be read when an explicit range exists")
			return time.Time{}
		},
	})
	startAt := time.Date(2026, 7, 14, 10, 0, 0, 123_999_999, time.FixedZone("UTC+8", 8*60*60))
	endAt := time.Date(2026, 7, 14, 11, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	result, err := service.List(context.Background(), ListInput{
		Page:             -1,
		PageSize:         -5,
		PageSizeProvided: true,
		Level:            "verbose",
		Keyword:          "needle",
		StartAt:          startAt,
		EndAt:            endAt,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	input := store.listInput
	if input.StartAt != "2026-07-14T02:00:00.123Z" || input.EndAt != "2026-07-14T03:00:00.000Z" {
		t.Fatalf("explicit range = %q - %q", input.StartAt, input.EndAt)
	}
	if input.Level != "" || input.Limit != 1 || input.Offset != 0 {
		t.Fatalf("store input = %+v", input)
	}
	if result.Items == nil || result.Page != 1 || result.PageSize != 1 || result.Total != 0 || result.HasMore {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceListPreservesNonECMAScriptWhitespaceFilter(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := &managementRuntimeLogReaderStub{listResult: port.ManagementRuntimeLogListResult{Items: []port.ManagementRuntimeLogSummary{}}}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	_, err := service.List(context.Background(), ListInput{
		TraceID: nonECMAScriptWhitespace,
		Event:   nonECMAScriptWhitespace,
		Keyword: nonECMAScriptWhitespace,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	input := store.listInput
	if input.TraceID != nonECMAScriptWhitespace || input.Event != nonECMAScriptWhitespace || input.Keyword != nonECMAScriptWhitespace {
		t.Fatalf("non-ECMAScript whitespace filters were broadened: %+v", input)
	}
	if input.StartAt != "2026-07-14T06:00:00.000Z" {
		t.Fatalf("keyword range start = %q", input.StartAt)
	}
}

func TestServiceDetailAndDependencyErrors(t *testing.T) {
	store := &managementRuntimeLogReaderStub{
		detail: port.ManagementRuntimeLog{
			ManagementRuntimeLogSummary: port.ManagementRuntimeLogSummary{
				ID:        "runtime_1",
				Time:      "2026-07-14T10:00:00.000Z",
				Level:     "error",
				CreatedAt: "2026-07-14T10:00:01.000Z",
			},
			RawJSON: `{"event":"gateway.error"}`,
		},
		detailFound: true,
	}
	service := NewService(store)
	detail, found, err := service.Detail(context.Background(), " runtime_1 ")
	if err != nil || !found {
		t.Fatalf("Detail = found %v err %v", found, err)
	}
	if store.detailID != "runtime_1" || detail.ID != "runtime_1" || detail.RawJSON != `{"event":"gateway.error"}` {
		t.Fatalf("detail = %+v; store id = %q", detail, store.detailID)
	}

	if _, err := NewService(nil).List(context.Background(), ListInput{}); err == nil {
		t.Fatal("List without store should fail")
	}
	if _, _, err := NewService(nil).Detail(context.Background(), "runtime_1"); err == nil {
		t.Fatal("Detail without store should fail")
	}
	if _, err := NewService(nil).Facets(context.Background()); err == nil {
		t.Fatal("Facets without store should fail")
	}
}

func TestServicePropagatesStoreErrors(t *testing.T) {
	want := errors.New("postgres unavailable")
	store := &managementRuntimeLogReaderStub{listErr: want, detailErr: want}
	service := NewService(store)
	if _, err := service.List(context.Background(), ListInput{}); !errors.Is(err, want) {
		t.Fatalf("List error = %v, want %v", err, want)
	}
	if _, _, err := service.Detail(context.Background(), "runtime_1"); !errors.Is(err, want) {
		t.Fatalf("Detail error = %v, want %v", err, want)
	}
}

type managementRuntimeLogReaderStub struct {
	listInput   port.ManagementRuntimeLogListInput
	listResult  port.ManagementRuntimeLogListResult
	listErr     error
	detailID    string
	detail      port.ManagementRuntimeLog
	detailFound bool
	detailErr   error
	facets      port.ManagementRuntimeLogFacets
	facetsErr   error
}

func (s *managementRuntimeLogReaderStub) ListManagementRuntimeLogs(_ context.Context, input port.ManagementRuntimeLogListInput) (port.ManagementRuntimeLogListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementRuntimeLogReaderStub) GetManagementRuntimeLog(_ context.Context, id string) (port.ManagementRuntimeLog, bool, error) {
	s.detailID = id
	return s.detail, s.detailFound, s.detailErr
}

func (s *managementRuntimeLogReaderStub) ManagementRuntimeLogFacets(context.Context) (port.ManagementRuntimeLogFacets, error) {
	return s.facets, s.facetsErr
}

func stringPointer(value string) *string {
	return &value
}
