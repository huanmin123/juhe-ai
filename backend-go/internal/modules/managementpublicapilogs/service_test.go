package managementpublicapilogs

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListNormalizesFiltersAndUsesBoundedProgressivePagination(t *testing.T) {
	startAt := time.Date(2026, 7, 14, 12, 0, 0, 987_654_321, time.FixedZone("UTC+8", 8*60*60))
	endAt := startAt.Add(-time.Hour)
	rows := make([]port.ManagementPublicAPILogListItem, 100)
	for index := range rows {
		rows[index] = publicAPILogListItemFixture("publog_probe")
	}
	rows[0] = publicAPILogListItemFixture("publog_1")
	store := &publicAPILogReaderStub{
		listResult: port.ManagementPublicAPILogListResult{Items: rows, HasMore: true},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		TraceID:          "\uFEFF trace_1 \u3000",
		SourceRefID:      " extsrc_1 ",
		Path:             " post /v1/chat/completions?stream=true ",
		Result:           " success ",
		StatusCode:       200,
		ClientIP:         " 203.0.113. ",
		StartAt:          startAt,
		EndAt:            endAt,
		Page:             99,
		PageSize:         1000,
		PageSizeProvided: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	input := store.listInput
	if input.TraceID != "trace_1" || input.SourceRefID != "extsrc_1" || input.Path != "/v1/chat/completions" || input.ClientIP != "203.0.113." {
		t.Fatalf("filters = %+v", input)
	}
	if input.Result != port.ManagementPublicAPILogResultSuccess || input.StatusCode == nil || *input.StatusCode != 200 {
		t.Fatalf("result/status filters = %+v", input)
	}
	if !input.StartAt.Equal(startAt) || !input.EndAt.Equal(endAt) || !input.StartAt.After(input.EndAt) {
		t.Fatalf("reverse range was changed: %s - %s", input.StartAt, input.EndAt)
	}
	if input.Limit != 100 || input.Offset != 900 {
		t.Fatalf("store window = limit %d offset %d, want 100/900", input.Limit, input.Offset)
	}
	if result.Page != 10 || result.PageSize != 100 || result.Total != 1001 || !result.HasMore || len(result.Items) != 100 {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceListPageSizeDefaultsClampsAndTrimsStoreOverflow(t *testing.T) {
	tests := []struct {
		name         string
		input        ListInput
		rowCount     int
		wantPage     int
		wantPageSize int
		wantOffset   int
		wantItems    int
		wantTotal    int
		wantMore     bool
	}{
		{
			name:         "omitted page size uses default",
			input:        ListInput{Page: -3, PageSize: 1},
			wantPage:     1,
			wantPageSize: 50,
		},
		{
			name:         "provided non-positive page size clamps to one",
			input:        ListInput{Page: 5000, PageSize: 0, PageSizeProvided: true},
			rowCount:     2,
			wantPage:     1000,
			wantPageSize: 1,
			wantOffset:   999,
			wantItems:    1,
			wantTotal:    1001,
			wantMore:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := make([]port.ManagementPublicAPILogListItem, test.rowCount)
			for index := range rows {
				rows[index] = publicAPILogListItemFixture("publog_1")
			}
			store := &publicAPILogReaderStub{listResult: port.ManagementPublicAPILogListResult{Items: rows}}
			result, err := NewService(store).List(context.Background(), test.input)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantPageSize || store.listInput.Offset != test.wantOffset || store.listInput.Limit != test.wantPageSize {
				t.Fatalf("pagination = result %+v, input %+v", result, store.listInput)
			}
			if len(result.Items) != test.wantItems || result.Total != test.wantTotal || result.HasMore != test.wantMore || result.Items == nil {
				t.Fatalf("progressive result = %+v", result)
			}
		})
	}
}

func TestServiceListPathNormalizationKeepsSpecialAllValuesExact(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: " all ", want: ""},
		{raw: "ALL", want: "ALL"},
		{raw: "GET all", want: "all"},
		{raw: "all?source=filter", want: "all"},
		{raw: " Patch /v1/messages?stream=true ", want: "/v1/messages"},
		{raw: "OPTIONS \t /health?full=1", want: "/health"},
		{raw: "GET ?only=query", want: ""},
		{raw: "GET\u0085/v1/messages", want: "GET\u0085/v1/messages"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			store := &publicAPILogReaderStub{}
			if _, err := NewService(store).List(context.Background(), ListInput{Path: test.raw}); err != nil {
				t.Fatalf("List: %v", err)
			}
			if store.listInput.Path != test.want {
				t.Fatalf("path = %q, want %q", store.listInput.Path, test.want)
			}
		})
	}
}

func TestServiceListAllSentinelsAndNonECMAScriptWhitespace(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	store := &publicAPILogReaderStub{}
	_, err := NewService(store).List(context.Background(), ListInput{
		TraceID:     " all ",
		SourceRefID: " ALL ",
		ClientIP:    nonECMAScriptWhitespace,
		Result:      "SUCCESS",
		StatusCode:  600,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	input := store.listInput
	if input.TraceID != "all" || input.SourceRefID != "ALL" || input.ClientIP != nonECMAScriptWhitespace {
		t.Fatalf("literal filters = %+v", input)
	}
	if input.Result != port.ManagementPublicAPILogResultAll || input.StatusCode != nil {
		t.Fatalf("invalid result/status = %+v", input)
	}

	if _, err := NewService(store).List(context.Background(), ListInput{SourceRefID: " all ", Result: " failed ", StatusCode: 100}); err != nil {
		t.Fatalf("List exact all: %v", err)
	}
	if store.listInput.SourceRefID != "" || store.listInput.Result != port.ManagementPublicAPILogResultFailed || store.listInput.StatusCode == nil || *store.listInput.StatusCode != 100 {
		t.Fatalf("exact sentinel/result/status = %+v", store.listInput)
	}
}

func TestServiceListMapsLightweightDTOAndUTCMilliseconds(t *testing.T) {
	empty := ""
	traceID := "trace_1"
	statusCode := 204
	durationMs := int64(0)
	row := publicAPILogListItemFixture("publog_1")
	row.TraceID = &traceID
	row.SourceName = &empty
	row.StatusCode = &statusCode
	row.DurationMs = &durationMs
	row.CreatedAt = time.Date(2026, 7, 14, 2, 20, 32, 0, time.UTC)
	store := &publicAPILogReaderStub{listResult: port.ManagementPublicAPILogListResult{Items: []port.ManagementPublicAPILogListItem{row}}}

	result, err := NewService(store).List(context.Background(), ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	item := result.Items[0]
	if item.CreatedAt != "2026-07-14T02:20:32.000Z" {
		t.Fatalf("createdAt = %q", item.CreatedAt)
	}
	if item.SourceName != "" || item.TraceID != traceID || item.StatusCode == nil || item.DurationMs == nil {
		t.Fatalf("mapped item = %+v", item)
	}

	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode mapped item: %v", err)
	}
	for _, omitted := range []string{"sourceRefId", "sourceName", "tokenId", "tokenName", "tokenPrefix", "queryString", "userAgent", "errorCode", "errorMessage", "requestCaptureStatus", "startedAt", "endedAt"} {
		if _, exists := object[omitted]; exists {
			t.Fatalf("field %q must be omitted: %s", omitted, encoded)
		}
	}
	for _, included := range []string{"id", "createdAt", "method", "path", "success", "traceId", "statusCode", "durationMs"} {
		if _, exists := object[included]; !exists {
			t.Fatalf("field %q must be included: %s", included, encoded)
		}
	}

}

func TestServiceDetailParsesOnlyJSONObjectPayloadsAndPreservesID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{name: "object", raw: `{"nested":{"ok":true}}`, want: map[string]any{"nested": map[string]any{"ok": true}}},
		{name: "empty", raw: "", want: map[string]any{}},
		{name: "malformed", raw: "{", want: map[string]any{}},
		{name: "null", raw: "null", want: map[string]any{}},
		{name: "array", raw: "[]", want: map[string]any{}},
		{name: "scalar", raw: `"text"`, want: map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &publicAPILogReaderStub{
				detail: port.ManagementPublicAPILogDetail{
					ManagementPublicAPILogSummary: publicAPILogSummaryFixture("publog_1"),
					RequestDataJSON:               test.raw,
					ResponseDataJSON:              test.raw,
				},
				detailFound: true,
			}
			detail, found, err := NewService(store).Detail(context.Background(), " publog_1 ")
			if err != nil || !found {
				t.Fatalf("Detail = found %v err %v", found, err)
			}
			if store.detailID != " publog_1 " {
				t.Fatalf("detail id = %q, want exact raw id", store.detailID)
			}
			if detail.RequestData == nil || detail.ResponseData == nil || !reflect.DeepEqual(detail.RequestData, test.want) || !reflect.DeepEqual(detail.ResponseData, test.want) {
				t.Fatalf("payloads = %#v / %#v, want %#v", detail.RequestData, detail.ResponseData, test.want)
			}
			encoded, err := json.Marshal(detail)
			if err != nil {
				t.Fatalf("Marshal detail: %v", err)
			}
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("decode detail: %v", err)
			}
			if _, ok := object["requestData"].(map[string]any); !ok {
				t.Fatalf("requestData is not an object: %s", encoded)
			}
			if _, ok := object["responseData"].(map[string]any); !ok {
				t.Fatalf("responseData is not an object: %s", encoded)
			}
		})
	}
}

func TestServicePropagatesErrorsAndNotFound(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	store := &publicAPILogReaderStub{listErr: wantErr, detailErr: wantErr, detailFound: true}
	service := NewService(store)
	if _, err := service.List(context.Background(), ListInput{}); !errors.Is(err, wantErr) {
		t.Fatalf("List error = %v, want %v", err, wantErr)
	}
	if _, found, err := service.Detail(context.Background(), "publog_1"); !errors.Is(err, wantErr) || !found {
		t.Fatalf("Detail error = %v found = %v, want error passthrough and found=true", err, found)
	}

	store.detailErr = nil
	store.detailFound = false
	if detail, found, err := service.Detail(context.Background(), "missing"); err != nil || found || !reflect.DeepEqual(detail, Detail{}) {
		t.Fatalf("not found detail = %#v found = %v err = %v", detail, found, err)
	}
	if _, err := NewService(nil).List(context.Background(), ListInput{}); err == nil {
		t.Fatal("List without store should fail")
	}
	if _, _, err := NewService(nil).Detail(context.Background(), "publog_1"); err == nil {
		t.Fatal("Detail without store should fail")
	}
}

func publicAPILogListItemFixture(id string) port.ManagementPublicAPILogListItem {
	return port.ManagementPublicAPILogListItem{
		ID:        id,
		Method:    "POST",
		Path:      "/v1/responses",
		Success:   true,
		CreatedAt: time.Date(2026, 7, 14, 4, 0, 0, 0, time.UTC),
	}
}

func publicAPILogSummaryFixture(id string) port.ManagementPublicAPILogSummary {
	return port.ManagementPublicAPILogSummary{
		ID:                    id,
		Method:                "POST",
		Path:                  "/v1/chat/completions",
		Success:               true,
		RequestSizeBytes:      12,
		ResponseSizeBytes:     34,
		RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		StartedAt:             time.Date(2026, 7, 14, 2, 20, 30, 0, time.UTC),
		EndedAt:               time.Date(2026, 7, 14, 2, 20, 31, 0, time.UTC),
		CreatedAt:             time.Date(2026, 7, 14, 2, 20, 32, 0, time.UTC),
	}
}

type publicAPILogReaderStub struct {
	listInput   port.ManagementPublicAPILogListInput
	listResult  port.ManagementPublicAPILogListResult
	listErr     error
	detailID    string
	detail      port.ManagementPublicAPILogDetail
	detailFound bool
	detailErr   error
}

func (s *publicAPILogReaderStub) ListManagementPublicAPILogs(_ context.Context, input port.ManagementPublicAPILogListInput) (port.ManagementPublicAPILogListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *publicAPILogReaderStub) GetManagementPublicAPILog(_ context.Context, id string) (port.ManagementPublicAPILogDetail, bool, error) {
	s.detailID = id
	return s.detail, s.detailFound, s.detailErr
}
