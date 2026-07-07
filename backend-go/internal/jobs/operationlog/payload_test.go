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
