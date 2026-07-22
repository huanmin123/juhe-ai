package managementauditlogs

import (
	"context"
	"errors"
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

func TestHotSearchServiceReturnsDatabaseFailureInsteadOfEmptySuccess(t *testing.T) {
	root := t.TempDir()
	writeHotSearchFile(t, root, "audit-hot-2026072210.ndjson", `{"auditLogId":"audit_1","createdAt":"2026-07-22T10:02:00Z","text":"needle"}`)
	want := errors.New("audit table unavailable")
	service := NewServiceWithOptions(ServiceOptions{
		Store: &auditLogReaderStub{byIDsErr: want}, HotSearchRoot: root,
	})

	result, err := service.HotSearch(context.Background(), HotSearchInput{
		Keywords: []string{"needle"}, Now: time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, want) || len(result.Items) != 0 {
		t.Fatalf("HotSearch() result = %+v, error = %v, want %v", result, err, want)
	}
}

type auditLogReaderStub struct {
	input       port.ManagementAuditLogListInput
	result      port.ManagementAuditLogListResult
	detailID    string
	detail      port.ManagementAuditLogDetail
	detailFound bool
	ids         []string
	byIDs       []port.ManagementAuditLogSummary
	byIDsErr    error
}

func (s *auditLogReaderStub) ListManagementAuditLogsByIDs(_ context.Context, ids []string) ([]port.ManagementAuditLogSummary, error) {
	s.ids = append([]string(nil), ids...)
	return s.byIDs, s.byIDsErr
}

func (s *auditLogReaderStub) ListManagementAuditLogs(_ context.Context, input port.ManagementAuditLogListInput) (port.ManagementAuditLogListResult, error) {
	s.input = input
	if s.result.Items == nil {
		s.result.Items = []port.ManagementAuditLogSummary{}
	}
	return s.result, nil
}

func (s *auditLogReaderStub) GetManagementAuditLog(_ context.Context, id string) (port.ManagementAuditLogDetail, bool, error) {
	s.detailID = id
	return s.detail, s.detailFound, nil
}
