package managementauditlogs

import (
	"context"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesNodeCompatibleFiltersAndProgressiveWindow(t *testing.T) {
	status := 503
	store := &auditLogReaderStub{result: port.ManagementAuditLogListResult{
		Items: []port.ManagementAuditLogSummary{{
			ID: "audit_1", TraceID: "trace_1", TrafficSource: "gateway", Method: "POST", Path: "/v1/responses",
			AuditOutcome: "upstream_failed", FinalStatusCode: &status, SampleBucket: 3, SampleReason: "problem",
			AttemptCount: 2, PayloadCount: 1, RawPayloadBytes: 100, CompressedPayloadBytes: 60,
			CompressionSavedBytes: 40, CaptureStatus: "complete", StartedAt: "2026-07-21T01:02:03.004Z",
			EndedAt: "2026-07-21T01:02:04.004Z", CreatedAt: "2026-07-21T01:02:05.004Z",
		}}, HasMore: true,
	}}
	service := NewService(store)
	result, err := service.List(context.Background(), ListInput{
		TraceID: " trace_", ErrorGroupID: " err_1 ", Outcome: "upstream_failed", StatusCode: 503,
		Path: " POST /v1/responses?stream=true ", Model: " gpt-5 ", SystemAccountID: " sys_1 ",
		APIKeyID: " key_1 ", GroupID: " group_1 ", AccountID: " account_1 ", ClientIP: " 203.0.113. ",
		StartAt: "2026-07-21T00:00:00.000Z", EndAt: "2026-07-21T23:59:59.000Z", TrafficSource: "gateway",
		Page: 20, PageSize: 100, PageSizeProvided: true,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.input.Path != "/v1/responses" || store.input.Model != "gpt-5" || store.input.Offset != 900 || store.input.Limit != 100 {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.Page != 10 || result.PageSize != 100 || result.Total != 902 || !result.HasMore || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	item := result.Items[0]
	if item.ID != "audit_1" || item.FinalStatusCode == nil || *item.FinalStatusCode != 503 || item.CreatedAt != "2026-07-21T01:02:05.004Z" {
		t.Fatalf("item = %+v", item)
	}
}

func TestListIgnoresInvalidEnumsAndStatus(t *testing.T) {
	store := &auditLogReaderStub{}
	service := NewService(store)
	_, err := service.List(context.Background(), ListInput{Outcome: "unknown", StatusCode: 99, TrafficSource: "unknown"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.input.Outcome != "" || store.input.StatusCode != nil || store.input.TrafficSource != "" {
		t.Fatalf("invalid filters reached store: %+v", store.input)
	}
}

func TestListErrorGroupsNormalizesFiltersMapsOptionalFieldsAndProgressiveWindow(t *testing.T) {
	status := 503
	value := func(text string) *string { return &text }
	group := port.ManagementAuditErrorGroup{
		ID: "err_1", Fingerprint: "fingerprint", WindowStartedAt: "2026-07-21T00:00:00.000Z", WindowEndedAt: "2026-07-21T01:00:00.000Z",
		SystemAccountID: value("sys_1"), SystemAccountName: value("System account"), APIKeyID: value("key_1"), APIKeyName: value("API key"),
		GroupID: value("group_1"), GroupName: value("Group"), AccountID: value("account_1"), AccountName: value("Account"), ProviderCode: value("openai"),
		Path: value("/v1/responses"), Model: value("gpt-5"), StatusCode: &status, ErrorPhase: value("upstream"), ErrorCode: value("rate_limit"), ErrorType: value("rate_limit_error"),
		RequestFingerprint: value("request_fingerprint"), ErrorFingerprint: value("error_fingerprint"), Count: 2,
		FirstEventID: value("event_1"), LastEventID: value("event_2"), SampleEventID: value("event_1"), LastMessage: value("rate limit reached"),
		CreatedAt: "2026-07-21T00:00:00.000Z", UpdatedAt: "2026-07-21T01:00:00.000Z",
	}
	store := &auditLogReaderStub{errorGroupResult: port.ManagementAuditErrorGroupListResult{
		Items: []port.ManagementAuditErrorGroup{group}, HasMore: true,
	}}
	result, err := NewService(store).ListErrorGroups(context.Background(), ErrorGroupListInput{
		Path: "\uFEFF POST /v1/responses?stream=true \uFEFF", Model: "\uFEFF\u0085gpt-5\u0085\uFEFF", SystemAccountID: " sys_1 ",
		APIKeyID: " key_1 ", GroupID: " group_1 ", AccountID: " account_1 ", StatusCode: 503,
		Page: 20, PageSize: 999, PageSizeProvided: true,
	})
	if err != nil {
		t.Fatalf("ListErrorGroups() error = %v", err)
	}
	if store.errorGroupInput.Path != "/v1/responses" || store.errorGroupInput.Model != "\u0085gpt-5\u0085" || store.errorGroupInput.SystemAccountID != "sys_1" ||
		store.errorGroupInput.APIKeyID != "key_1" || store.errorGroupInput.GroupID != "group_1" || store.errorGroupInput.AccountID != "account_1" ||
		store.errorGroupInput.StatusCode == nil || *store.errorGroupInput.StatusCode != 503 || store.errorGroupInput.Limit != 100 || store.errorGroupInput.Offset != 900 {
		t.Fatalf("store input = %+v", store.errorGroupInput)
	}
	want := ErrorGroup{
		ID: group.ID, Fingerprint: group.Fingerprint, WindowStartedAt: group.WindowStartedAt, WindowEndedAt: group.WindowEndedAt,
		SystemAccountID: *group.SystemAccountID, SystemAccountName: *group.SystemAccountName, APIKeyID: *group.APIKeyID, APIKeyName: *group.APIKeyName,
		GroupID: *group.GroupID, GroupName: *group.GroupName, AccountID: *group.AccountID, AccountName: *group.AccountName, ProviderCode: *group.ProviderCode,
		Path: *group.Path, Model: *group.Model, StatusCode: group.StatusCode, ErrorPhase: *group.ErrorPhase, ErrorCode: *group.ErrorCode, ErrorType: *group.ErrorType,
		RequestFingerprint: *group.RequestFingerprint, ErrorFingerprint: *group.ErrorFingerprint, Count: group.Count,
		FirstEventID: *group.FirstEventID, LastEventID: *group.LastEventID, SampleEventID: *group.SampleEventID, LastMessage: *group.LastMessage,
		CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
	}
	if result.Page != 10 || result.PageSize != 100 || result.Total != 902 || !result.HasMore || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	actual := result.Items[0]
	if actual.StatusCode == nil || want.StatusCode == nil || *actual.StatusCode != *want.StatusCode {
		t.Fatalf("statusCode = %v, want %v", actual.StatusCode, want.StatusCode)
	}
	actual.StatusCode = nil
	want.StatusCode = nil
	if actual != want {
		t.Fatalf("item = %+v, want %+v", actual, want)
	}
}

func TestListErrorGroupsDefaultsPageSizeAndReturnsNonNilItems(t *testing.T) {
	store := &auditLogReaderStub{}
	result, err := NewService(store).ListErrorGroups(context.Background(), ErrorGroupListInput{})
	if err != nil {
		t.Fatalf("ListErrorGroups() error = %v", err)
	}
	if store.errorGroupInput.Limit != 100 || store.errorGroupInput.Offset != 0 || result.Page != 1 || result.PageSize != 100 || result.Items == nil || result.Total != 0 || result.HasMore {
		t.Fatalf("store input = %+v, result = %+v", store.errorGroupInput, result)
	}
}

func TestListErrorGroupEventsOverridesQueryGroupAndPreservesListFilters(t *testing.T) {
	status := 503
	store := &auditLogReaderStub{result: port.ManagementAuditLogListResult{
		Items: []port.ManagementAuditLogSummary{{
			ID: "audit_1", TraceID: "trace_1", TrafficSource: "gateway", Method: "POST", Path: "/v1/responses",
			AuditOutcome: "upstream_failed", FinalStatusCode: &status, StartedAt: "2026-07-21T01:02:03.004Z",
			EndedAt: "2026-07-21T01:02:04.004Z", CreatedAt: "2026-07-21T01:02:05.004Z",
		}}, HasMore: true,
	}}

	result, err := NewService(store).ListErrorGroupEvents(context.Background(), " route_group ", ListInput{
		TraceID: " trace_1 ", ErrorGroupID: "query_group", Outcome: "upstream_failed", StatusCode: 503,
		Path: " POST /v1/responses?stream=true ", Model: " gpt-5 ", SystemAccountID: " sys_1 ",
		APIKeyID: " key_1 ", GroupID: " group_1 ", AccountID: " account_1 ", ClientIP: " 203.0.113.1 ",
		StartAt: " 2026-07-21T00:00:00.000Z ", EndAt: " 2026-07-21T23:59:59.000Z ", TrafficSource: "gateway",
		Page: 20, PageSize: 100, PageSizeProvided: true,
	})
	if err != nil {
		t.Fatalf("ListErrorGroupEvents() error = %v", err)
	}
	if store.listCalls != 1 || store.input.ErrorGroupID != "route_group" || store.input.TraceID != "trace_1" ||
		store.input.Outcome != "upstream_failed" || store.input.StatusCode == nil || *store.input.StatusCode != 503 ||
		store.input.Path != "/v1/responses" || store.input.Model != "gpt-5" || store.input.SystemAccountID != "sys_1" ||
		store.input.APIKeyID != "key_1" || store.input.GroupID != "group_1" || store.input.AccountID != "account_1" ||
		store.input.ClientIP != "203.0.113.1" || store.input.StartAt != "2026-07-21T00:00:00.000Z" ||
		store.input.EndAt != "2026-07-21T23:59:59.000Z" || store.input.TrafficSource != "gateway" ||
		store.input.Limit != 100 || store.input.Offset != 900 {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.Page != 10 || result.PageSize != 100 || result.Total != 902 || !result.HasMore || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestListErrorGroupEventsReturnsNonNilEmptyListWithoutDetailLookup(t *testing.T) {
	store := &auditLogReaderStub{}
	result, err := NewService(store).ListErrorGroupEvents(context.Background(), "missing_group", ListInput{})
	if err != nil {
		t.Fatalf("ListErrorGroupEvents() error = %v", err)
	}
	if store.listCalls != 1 || store.input.ErrorGroupID != "missing_group" || store.detailCalls != 0 || store.detailID != "" ||
		result.Items == nil || result.Page != 1 || result.PageSize != 100 || result.Total != 0 || result.HasMore {
		t.Fatalf("store input = %+v, result = %+v", store.input, result)
	}
}

func TestListErrorGroupEventsRejectsECMAScriptWhitespaceOnlyRouteID(t *testing.T) {
	store := &auditLogReaderStub{}
	_, err := NewService(store).ListErrorGroupEvents(context.Background(), "\uFEFF \t\n\uFEFF", ListInput{})
	if err == nil || err.Error() != "error group id is required" {
		t.Fatalf("ListErrorGroupEvents() error = %v", err)
	}
	if store.listCalls != 0 {
		t.Fatalf("ListManagementAuditLogs() calls = %d, want 0", store.listCalls)
	}
}

func TestDetailMapsAttemptsPayloadMetadataAndErrorGroup(t *testing.T) {
	proxyURL := "  http://proxy.internal:8080  "
	store := &auditLogReaderStub{
		detailFound: true,
		detail: port.ManagementAuditLogDetail{
			ManagementAuditLogSummary: port.ManagementAuditLogSummary{
				ID: "audit_1", TraceID: "trace_1", TrafficSource: "gateway", Method: "POST", Path: "/v1/responses",
				AuditOutcome: "upstream_failed", SampleBucket: 3, SampleReason: "problem", CaptureStatus: "complete",
				StartedAt: "2026-07-21T01:02:03.004Z", EndedAt: "2026-07-21T01:02:04.004Z", CreatedAt: "2026-07-21T01:02:05.004Z",
			},
			Attempts: []port.ManagementAuditLogAttempt{{
				ID: "attempt_1", AttemptIndex: 0, ProxyURL: &proxyURL, UpstreamMethod: "POST", UpstreamURL: " https://user:pass@example.com/v1/responses ",
				Success: false, StartedAt: "2026-07-21T01:02:03.100Z",
			}},
			ErrorGroup: &port.ManagementAuditErrorGroup{
				ID: "err_1", Fingerprint: "fingerprint", WindowStartedAt: "2026-07-21T00:00:00.000Z",
				WindowEndedAt: "2026-07-21T01:00:00.000Z", Count: 2, CreatedAt: "2026-07-21T00:00:00.000Z", UpdatedAt: "2026-07-21T01:00:00.000Z",
			},
			Payloads: []port.ManagementAuditLogPayloadSummary{{
				ID: "payload_1", PartType: "upstream_response", SequenceIndex: 2, SizeBytes: 100,
				CompressedSizeBytes: 60, CaptureStatus: "complete", CreatedAt: "2026-07-21T01:02:04.000Z", HasBody: true,
			}},
		},
	}
	detail, found, err := NewService(store).Detail(context.Background(), "audit_1")
	if err != nil || !found {
		t.Fatalf("Detail() found=%v err=%v", found, err)
	}
	if store.detailID != "audit_1" || len(detail.Attempts) != 1 || len(detail.Payloads) != 1 || detail.ErrorGroup == nil {
		t.Fatalf("detail = %+v store id = %q", detail, store.detailID)
	}
	if detail.Attempts[0].UpstreamURL != "https://user:pass@example.com/v1/responses" || detail.Attempts[0].ProxyURL != "http://proxy.internal:8080" || !detail.Payloads[0].HasBody {
		t.Fatalf("detail children = %+v / %+v", detail.Attempts, detail.Payloads)
	}
}

func TestDetailRejectsUnknownTrafficSourceLikeNodeMapper(t *testing.T) {
	store := &auditLogReaderStub{detailFound: true, detail: port.ManagementAuditLogDetail{
		ManagementAuditLogSummary: port.ManagementAuditLogSummary{ID: "audit_1", TrafficSource: "legacy_source"},
	}}
	_, found, err := NewService(store).Detail(context.Background(), "audit_1")
	if found || err == nil {
		t.Fatalf("Detail() found=%v err=%v, want mapper error", found, err)
	}
}

func TestDetailPreservesWhitespaceOnlyRequiredUpstreamURL(t *testing.T) {
	store := &auditLogReaderStub{detailFound: true, detail: port.ManagementAuditLogDetail{
		ManagementAuditLogSummary: port.ManagementAuditLogSummary{ID: "audit_1", TrafficSource: "gateway"},
		Attempts: []port.ManagementAuditLogAttempt{{
			ID: "attempt_1", UpstreamMethod: "POST", UpstreamURL: " \t ", StartedAt: "2026-07-21T01:02:03.100Z",
		}},
	}}
	detail, found, err := NewService(store).Detail(context.Background(), "audit_1")
	if err != nil || !found || len(detail.Attempts) != 1 {
		t.Fatalf("Detail() found=%v err=%v detail=%+v", found, err, detail)
	}
	if detail.Attempts[0].UpstreamURL != " \t " {
		t.Fatalf("upstreamUrl=%q, want stored required value", detail.Attempts[0].UpstreamURL)
	}
}

func TestHotSearchServiceRestoresDatabaseSummariesAndPreservesSearchOrder(t *testing.T) {
	root := t.TempDir()
	writeHotSearchFile(t, root, "audit-hot-2026072210.ndjson", strings.Join([]string{
		`{"auditLogId":"audit_new","createdAt":"2026-07-22T10:02:00Z","text":"needle"}`,
		`{"auditLogId":"audit_old","createdAt":"2026-07-22T10:01:00Z","text":"needle"}`,
	}, "\n"))
	store := &auditLogReaderStub{byIDs: []port.ManagementAuditLogSummary{
		{ID: "audit_old", TraceID: "old", TrafficSource: "gateway", AuditOutcome: "success", CreatedAt: "2026-07-22T10:01:00Z"},
		{ID: "audit_new", TraceID: "new", TrafficSource: "gateway", AuditOutcome: "success", CreatedAt: "2026-07-22T10:02:00Z"},
	}}
	service := NewServiceWithOptions(ServiceOptions{Store: store, HotSearchRoot: root})
	result, err := service.HotSearch(context.Background(), HotSearchInput{Keywords: []string{"needle"}, Limit: 10, Now: time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "audit_new" || result.Items[1].ID != "audit_old" || len(store.ids) != 2 {
		t.Fatalf("result = %+v ids = %+v", result, store.ids)
	}
}

type auditLogReaderStub struct {
	input            port.ManagementAuditLogListInput
	result           port.ManagementAuditLogListResult
	errorGroupInput  port.ManagementAuditErrorGroupListInput
	errorGroupResult port.ManagementAuditErrorGroupListResult
	listCalls        int
	detailID         string
	detailCalls      int
	detail           port.ManagementAuditLogDetail
	detailFound      bool
	ids              []string
	byIDs            []port.ManagementAuditLogSummary
}

func (s *auditLogReaderStub) ListManagementAuditLogsByIDs(_ context.Context, ids []string) ([]port.ManagementAuditLogSummary, error) {
	s.ids = append([]string(nil), ids...)
	return s.byIDs, nil
}

func (s *auditLogReaderStub) ListManagementAuditLogs(_ context.Context, input port.ManagementAuditLogListInput) (port.ManagementAuditLogListResult, error) {
	s.listCalls++
	s.input = input
	if s.result.Items == nil {
		s.result.Items = []port.ManagementAuditLogSummary{}
	}
	return s.result, nil
}

func (s *auditLogReaderStub) ListManagementAuditErrorGroups(_ context.Context, input port.ManagementAuditErrorGroupListInput) (port.ManagementAuditErrorGroupListResult, error) {
	s.errorGroupInput = input
	return s.errorGroupResult, nil
}

func (s *auditLogReaderStub) GetManagementAuditLog(_ context.Context, id string) (port.ManagementAuditLogDetail, bool, error) {
	s.detailCalls++
	s.detailID = id
	return s.detail, s.detailFound, nil
}
