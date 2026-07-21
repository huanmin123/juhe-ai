package accountbalancesnapshotcleanup

import (
	"errors"
	"testing"
	"time"
)

func TestPayloadRoundTripPreservesCleanupIdentity(t *testing.T) {
	input := cleanupTaskFixture()
	payload, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := Decode(payload)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != input {
		t.Fatalf("Decode() = %+v, want %+v", got, input)
	}
	if gotKey, wantKey := UniqueKey(got), UniqueKey(input); gotKey == "" || gotKey != wantKey {
		t.Fatalf("UniqueKey() = %q, want stable %q", gotKey, wantKey)
	}
}

func TestDecodeRejectsInvalidPayload(t *testing.T) {
	tests := [][]byte{
		[]byte(`{`),
		[]byte(`{"version":2,"accountId":"acct-1","systemAccountId":"sys-1","updatedBefore":"2026-07-20T08:00:00Z","reason":"multiple_api_keys"}`),
		[]byte(`{"version":1,"systemAccountId":"sys-1","updatedBefore":"2026-07-20T08:00:00Z","reason":"multiple_api_keys"}`),
		[]byte(`{"version":1,"accountId":"acct-1","updatedBefore":"2026-07-20T08:00:00Z","reason":"multiple_api_keys"}`),
		[]byte(`{"version":1,"accountId":"acct-1","systemAccountId":"sys-1","updatedBefore":"not-a-time","reason":"multiple_api_keys"}`),
		[]byte(`{"version":1,"accountId":"acct-1","systemAccountId":"sys-1","updatedBefore":"2026-07-20T08:00:00Z"}`),
	}
	for _, payload := range tests {
		if _, err := Decode(payload); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("Decode(%s) error = %v, want ErrInvalidPayload", payload, err)
		}
	}
}

func cleanupTaskFixture() Task {
	return Task{
		AccountID:       "acct-1",
		SystemAccountID: "sys-1",
		UpdatedBefore:   time.Date(2026, 7, 20, 8, 0, 0, 123, time.UTC),
		Reason:          "multiple_api_keys",
	}
}
