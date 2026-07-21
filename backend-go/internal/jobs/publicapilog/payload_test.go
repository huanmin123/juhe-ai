package publicapilog

import (
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestEncodeDecodeWriteTaskPayload(t *testing.T) {
	input := publicAPILogFixture()
	payload, err := EncodeWriteTaskPayload(input)
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	decoded, err := DecodeWriteTaskPayload(payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if decoded.ID != input.ID || decoded.TraceID != input.TraceID || decoded.Path != input.Path {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.StatusCode == nil || *decoded.StatusCode != 200 {
		t.Fatalf("status code = %v", decoded.StatusCode)
	}
	if decoded.DurationMs == nil || *decoded.DurationMs != 12 {
		t.Fatalf("duration = %v", decoded.DurationMs)
	}
	if !decoded.StartedAt.Equal(input.StartedAt) || !decoded.EndedAt.Equal(input.EndedAt) {
		t.Fatalf("times = %v/%v, want %v/%v", decoded.StartedAt, decoded.EndedAt, input.StartedAt, input.EndedAt)
	}
}

func TestEncodeDecodeWriteTaskEnvelopePreservesDistinctCorrelation(t *testing.T) {
	input := publicAPILogFixture()
	payload, err := EncodeWriteTaskPayloadWithCorrelation(input, TaskCorrelation{
		TraceID:   "trace-envelope-1",
		RequestID: "request-envelope-1",
	})
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayloadWithCorrelation() error = %v", err)
	}
	envelope, err := DecodeWriteTaskEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskEnvelope() error = %v", err)
	}
	if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-envelope-1" || envelope.Correlation.RequestID != "request-envelope-1" {
		t.Fatalf("correlation = %+v", envelope.Correlation)
	}
	if envelope.Log.ID != input.ID {
		t.Fatalf("log = %+v", envelope.Log)
	}
}

func TestDecodeWriteTaskEnvelopeAcceptsLegacyV1WithoutCorrelation(t *testing.T) {
	payload, err := EncodeWriteTaskPayload(publicAPILogFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	envelope, err := DecodeWriteTaskEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskEnvelope() error = %v", err)
	}
	if envelope.Correlation != nil {
		t.Fatalf("legacy correlation = %+v, want nil", envelope.Correlation)
	}
}

func TestDecodeWriteTaskEnvelopeReturnsCorrelationWithInvalidLog(t *testing.T) {
	envelope, err := DecodeWriteTaskEnvelope([]byte(`{"version":1,"correlation":{"traceId":"trace-invalid-1","requestId":"request-invalid-1"},"log":{"id":"publog_invalid_1"}}`))
	if err == nil {
		t.Fatal("DecodeWriteTaskEnvelope() error = nil, want invalid log")
	}
	if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-invalid-1" || envelope.Correlation.RequestID != "request-invalid-1" {
		t.Fatalf("correlation = %+v", envelope.Correlation)
	}
}

func TestDecodeWriteTaskPayloadRejectsBadInput(t *testing.T) {
	if _, err := DecodeWriteTaskPayload([]byte(`{bad json`)); err == nil {
		t.Fatal("DecodeWriteTaskPayload() error = nil, want json error")
	}
	if _, err := DecodeWriteTaskPayload([]byte(`{"version":2,"log":{"id":"publog_1"}}`)); err == nil {
		t.Fatal("DecodeWriteTaskPayload() error = nil, want version error")
	}
	if _, err := DecodeWriteTaskPayload([]byte(`{"version":1,"log":{"path":"/__aipublic__/group/list"}}`)); err == nil {
		t.Fatal("DecodeWriteTaskPayload() error = nil, want missing id error")
	}
}

func TestEncodeWriteTaskPayloadRejectsMissingStableFields(t *testing.T) {
	input := publicAPILogFixture()
	input.ID = ""
	if _, err := EncodeWriteTaskPayload(input); err == nil {
		t.Fatal("EncodeWriteTaskPayload() error = nil, want missing id error")
	}
	input = publicAPILogFixture()
	input.StartedAt = time.Time{}
	if _, err := EncodeWriteTaskPayload(input); err == nil {
		t.Fatal("EncodeWriteTaskPayload() error = nil, want missing started_at error")
	}
}

func publicAPILogFixture() port.PublicAPILogInput {
	statusCode := 200
	durationMs := int64(12)
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	return port.PublicAPILogInput{
		ID:                    "publog_1",
		TraceID:               "trace_1",
		SourceRefID:           "source_1",
		SourceName:            "Source",
		TokenID:               "token_1",
		TokenName:             "Token",
		TokenPrefix:           "juis_abc",
		Method:                "GET",
		Path:                  "/__aipublic__/group/list",
		StatusCode:            &statusCode,
		Success:               true,
		DurationMs:            &durationMs,
		RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		RequestData:           map[string]any{"query": map[string]any{"targetUsername": "admin"}},
		ResponseData:          map[string]any{"body": map[string]any{"ok": true}},
		StartedAt:             startedAt,
		EndedAt:               startedAt.Add(time.Millisecond),
		CreatedAt:             startedAt.Add(time.Millisecond),
	}
}
