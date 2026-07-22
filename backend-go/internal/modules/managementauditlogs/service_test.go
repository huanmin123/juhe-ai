package managementauditlogs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestPayloadReadsBoundedPlainWindowAndHeadersOnlyOnFirstWindow(t *testing.T) {
	root := t.TempDir()
	writeAuditPayloadTestFile(t, root, "aa/headers.blob", []byte(`{"x-test":["one","two"]}`))
	writeAuditPayloadTestFile(t, root, "bb/body.blob", []byte("hello payload"))
	store := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
		Summary:     port.ManagementAuditLogPayloadSummary{ID: "payload_1", PartType: "client_request", HasHeaders: true, HasBody: true},
		HeadersBlob: &port.ManagementAuditPayloadBlob{StorageKey: "aa/headers.blob", Compression: "none", RawSizeBytes: 24, CompressedSizeBytes: 24},
		BodyBlob:    &port.ManagementAuditPayloadBlob{StorageKey: "bb/body.blob", Compression: "none", RawSizeBytes: 13, CompressedSizeBytes: 13},
	}}
	service := NewServiceWithOptions(store, Options{PayloadBlobRoot: root})

	first, found, err := service.Payload(context.Background(), "audit_1", "payload_1", PayloadInput{Offset: 0, Limit: 5})
	if err != nil || !found {
		t.Fatalf("Payload() found=%v err=%v", found, err)
	}
	headerValues, headersOK := first.Headers["x-test"].([]string)
	if !first.HeadersIncluded || !headersOK || len(headerValues) != 2 || first.BodyText != "hello" || first.BodyNextOffset == nil || *first.BodyNextOffset != 5 || !first.BodyTruncated {
		t.Fatalf("first payload = %+v", first)
	}
	second, found, err := service.Payload(context.Background(), "audit_1", "payload_1", PayloadInput{Offset: 5, Limit: 1024})
	if err != nil || !found || second.HeadersIncluded || second.Headers != nil || second.HeadersStorageStatus != "not_saved" || second.BodyText != " payload" || second.BodyTruncated {
		t.Fatalf("second payload found=%v err=%v detail=%+v", found, err, second)
	}
}

func TestPayloadStreamsGzipWindowAndEncodesBinary(t *testing.T) {
	root := t.TempDir()
	raw := []byte{0x00, 0xff, 0x01, 0x02, 0x03, 0x04}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeAuditPayloadTestFile(t, root, "cc/body.gz", compressed.Bytes())
	store := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
		Summary:  port.ManagementAuditLogPayloadSummary{ID: "payload_2", PartType: "upstream_response", HasBody: true},
		BodyBlob: &port.ManagementAuditPayloadBlob{StorageKey: "cc/body.gz", Compression: "gzip", RawSizeBytes: int64(len(raw)), CompressedSizeBytes: int64(compressed.Len())},
	}}
	detail, found, err := NewServiceWithOptions(store, Options{PayloadBlobRoot: root}).Payload(context.Background(), "audit_1", "payload_2", PayloadInput{Offset: 1, Limit: 3})
	if err != nil || !found {
		t.Fatalf("Payload() found=%v err=%v", found, err)
	}
	if detail.BodyText != "" || detail.BodyBase64 != base64.StdEncoding.EncodeToString(raw[1:4]) || detail.BodyBytesReturned != 3 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPayloadRejectsTraversalAndOversizedMetadata(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.blob")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, blob := range []port.ManagementAuditPayloadBlob{
		{StorageKey: filepath.Join("..", filepath.Base(filepath.Dir(outside)), filepath.Base(outside)), Compression: "none", RawSizeBytes: 6, CompressedSizeBytes: 6},
		{StorageKey: "body.blob", Compression: "none", RawSizeBytes: maxAuditPayloadBlobRawBytes + 1, CompressedSizeBytes: 1},
	} {
		store := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
			Summary: port.ManagementAuditLogPayloadSummary{ID: "payload_bad", HasBody: true}, BodyBlob: &blob,
		}}
		if _, _, err := NewServiceWithOptions(store, Options{PayloadBlobRoot: root}).Payload(context.Background(), "audit_1", "payload_bad", PayloadInput{}); err == nil {
			t.Fatalf("blob %+v accepted", blob)
		}
	}
}

func TestPayloadReportsMissingMetadataAndMissingFileWithoutReadingUnboundedData(t *testing.T) {
	root := t.TempDir()
	metadataMissing := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
		Summary: port.ManagementAuditLogPayloadSummary{ID: "payload_missing_meta", HasHeaders: true, HasBody: true},
	}}
	detail, found, err := NewServiceWithOptions(metadataMissing, Options{PayloadBlobRoot: root}).Payload(context.Background(), "audit_1", "payload_missing_meta", PayloadInput{})
	if err != nil || !found || detail.HeadersStorageStatus != "metadata_missing" || detail.BodyStorageStatus != "metadata_missing" || !detail.HeadersIncluded {
		t.Fatalf("metadata-missing detail=%+v found=%v err=%v", detail, found, err)
	}

	fileMissing := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
		Summary:  port.ManagementAuditLogPayloadSummary{ID: "payload_missing_file", HasBody: true},
		BodyBlob: &port.ManagementAuditPayloadBlob{StorageKey: "missing/body.blob", Compression: "none", RawSizeBytes: 10, CompressedSizeBytes: 10},
	}}
	detail, found, err = NewServiceWithOptions(fileMissing, Options{PayloadBlobRoot: root}).Payload(context.Background(), "audit_1", "payload_missing_file", PayloadInput{})
	if err != nil || !found || detail.BodyStorageStatus != "file_missing" || detail.BodyTotalBytes != 10 || !detail.BodyTruncated || detail.BodyNextOffset != nil {
		t.Fatalf("file-missing detail=%+v found=%v err=%v", detail, found, err)
	}
}

func TestPayloadRejectsGzipBeyondWriterBoundaryAndHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	writeAuditPayloadTestFile(t, root, "body.blob", []byte("body"))
	store := &auditLogReaderStub{payloadFound: true, payload: port.ManagementAuditLogPayload{
		Summary:  port.ManagementAuditLogPayloadSummary{ID: "payload_cancel", HasBody: true},
		BodyBlob: &port.ManagementAuditPayloadBlob{StorageKey: "body.blob", Compression: "none", RawSizeBytes: 4, CompressedSizeBytes: 4},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := NewServiceWithOptions(store, Options{PayloadBlobRoot: root}).Payload(ctx, "audit_1", "payload_cancel", PayloadInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Payload() error = %v", err)
	}
	store.payload.BodyBlob = &port.ManagementAuditPayloadBlob{StorageKey: "body.gz", Compression: "gzip", RawSizeBytes: maxGzipPayloadBlobRawBytes + 1, CompressedSizeBytes: 1}
	if _, _, err := NewServiceWithOptions(store, Options{PayloadBlobRoot: root}).Payload(context.Background(), "audit_1", "payload_cancel", PayloadInput{}); err == nil {
		t.Fatal("gzip payload beyond writer boundary accepted")
	}
}

func TestOpenAuditPayloadBlobRejectsSymlinkEscapeWhenSupported(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.blob")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.blob")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink is unavailable on this host: %v", err)
	}
	file, found, err := openAuditPayloadBlob(root, "linked.blob")
	if file != nil {
		file.Close()
	}
	if err == nil || found {
		t.Fatalf("symlink escape found=%v err=%v", found, err)
	}
}

func writeAuditPayloadTestFile(t *testing.T, root, key string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

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

type auditLogReaderStub struct {
	input        port.ManagementAuditLogListInput
	result       port.ManagementAuditLogListResult
	detailID     string
	detail       port.ManagementAuditLogDetail
	detailFound  bool
	payload      port.ManagementAuditLogPayload
	payloadFound bool
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

func (s *auditLogReaderStub) GetManagementAuditLogPayload(_ context.Context, auditLogID, payloadID string) (port.ManagementAuditLogPayload, bool, error) {
	return s.payload, s.payloadFound, nil
}
