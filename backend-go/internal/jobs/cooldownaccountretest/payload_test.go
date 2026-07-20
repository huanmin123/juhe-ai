package cooldownaccountretest

import (
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestTaskPayloadRoundTripAndUniqueBoundary(t *testing.T) {
	started := time.Date(2026, 7, 20, 9, 0, 0, 123, time.UTC)
	task := port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 7, ObservationStartedAt: &started, MaxPauseMinutes: 30, MaxRecoveryHours: 24}
	payload, err := EncodeTask(task)
	if err != nil {
		t.Fatalf("EncodeTask() error = %v", err)
	}
	decoded, err := DecodeTask(payload)
	if err != nil {
		t.Fatalf("DecodeTask() error = %v", err)
	}
	if decoded.AccountID != task.AccountID || decoded.ConfigRevision != 7 || decoded.ObservationStartedAt == nil || !decoded.ObservationStartedAt.Equal(started) {
		t.Fatalf("decoded = %+v", decoded)
	}
	key := UniqueKey(task)
	changedRevision := task
	changedRevision.ConfigRevision++
	changedObservation := task
	later := started.Add(time.Second)
	changedObservation.ObservationStartedAt = &later
	if key == UniqueKey(changedRevision) || key == UniqueKey(changedObservation) {
		t.Fatal("unique key must include config revision and observation start")
	}
}

func TestDecodeTaskRejectsInvalidPayload(t *testing.T) {
	if _, err := DecodeTask([]byte(`{"version":1,"task":{"accountId":""}}`)); err == nil {
		t.Fatal("expected invalid payload error")
	}
}
