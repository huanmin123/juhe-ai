package operationlog

import (
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestOperationLogWriteTaskPayloadRoundTrip(t *testing.T) {
	input := operationLogTaskFixture()
	payload, err := EncodeWriteTaskPayload(input)
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	got, err := DecodeWriteTaskPayload(payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if got.ID != input.ID ||
		got.OperationKey != "accounts.update_tags" ||
		got.ResourceID != "acct_main" ||
		got.StatusCode == nil ||
		*got.StatusCode != 200 {
		t.Fatalf("decoded payload = %+v", got)
	}
}

func TestOperationLogWriteTaskEnvelopePreservesDistinctCorrelation(t *testing.T) {
	input := operationLogTaskFixture()
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
}

func TestOperationLogWriteTaskEnvelopeAcceptsLegacyV1WithoutCorrelation(t *testing.T) {
	payload, err := EncodeWriteTaskPayload(operationLogTaskFixture())
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

func TestOperationLogWriteTaskEnvelopeReturnsCorrelationWithInvalidLog(t *testing.T) {
	envelope, err := DecodeWriteTaskEnvelope([]byte(`{"version":1,"correlation":{"traceId":"trace-invalid-1","requestId":"request-invalid-1"},"log":{"id":"oplog_invalid_1"}}`))
	if err == nil {
		t.Fatal("DecodeWriteTaskEnvelope() error = nil, want invalid log")
	}
	if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-invalid-1" || envelope.Correlation.RequestID != "request-invalid-1" {
		t.Fatalf("correlation = %+v", envelope.Correlation)
	}
}

func TestOperationLogWriteTaskPayloadRejectsInvalidPayload(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{`),
		[]byte(`{"version":2,"log":{}}`),
		[]byte(`{"version":1,"log":{"id":"oplog_1"}}`),
	} {
		t.Run(string(payload), func(t *testing.T) {
			if _, err := DecodeWriteTaskPayload(payload); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("DecodeWriteTaskPayload() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestEncodeOperationLogWriteTaskRejectsInvalidInput(t *testing.T) {
	input := operationLogTaskFixture()
	input.ID = ""
	if _, err := EncodeWriteTaskPayload(input); err == nil {
		t.Fatal("EncodeWriteTaskPayload() error = nil, want validation error")
	}
}

func operationLogTaskFixture() port.OperationLogInput {
	createdAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	statusCode := 200
	return port.OperationLogInput{
		ID:                   "oplog_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Module:               "accounts",
		Action:               "update_tags",
		OperationKey:         "accounts.update_tags",
		ResourceType:         "account",
		ResourceID:           "acct_main",
		ResourceName:         "主账号",
		Summary:              "更新账户标签：主账号",
		Method:               "PATCH",
		Path:                 "/__aisys__/api/my-accounts/acct_main/tags",
		StatusCode:           &statusCode,
		CreatedAt:            createdAt,
	}
}
