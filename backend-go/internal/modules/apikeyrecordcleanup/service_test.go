package apikeyrecordcleanup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceRunOnceDefersBoundedTargetsWhenUsageCleanupContractIsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	store := &apiKeyRecordCleanupStoreStub{result: port.APIKeyRecordCleanupRunResult{
		Attempted: 3,
		Deferred:  3,
	}}
	service := NewService(store)

	result, err := service.RunOnce(context.Background(), RunOnceInput{Now: now, Limit: 3})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Attempted != 3 || result.Deferred != 3 || result.Completed != 0 || result.DeletedRows != 0 {
		t.Fatalf("RunOnce() result = %+v", result)
	}
	if result.Concern != UsageCleanupContractUnavailableConcern {
		t.Fatalf("RunOnce() concern = %q, want %q", result.Concern, UsageCleanupContractUnavailableConcern)
	}
	if len(store.inputs) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.inputs))
	}
	input := store.inputs[0]
	if input.Limit != 3 || !input.AttemptedAt.Equal(now) || input.BlockedReason != UsageCleanupContractUnavailableBlockedReason {
		t.Fatalf("store input = %+v", input)
	}
}

func TestServiceRunOnceUsesBoundedDefaultLimit(t *testing.T) {
	store := &apiKeyRecordCleanupStoreStub{}
	service := NewService(store)

	_, err := service.RunOnce(context.Background(), RunOnceInput{
		Now: time.Date(2026, 7, 22, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60)),
	})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.inputs) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.inputs))
	}
	if store.inputs[0].Limit != DefaultTargetLimit {
		t.Fatalf("store limit = %d, want %d", store.inputs[0].Limit, DefaultTargetLimit)
	}
	if store.inputs[0].AttemptedAt.Location() != time.UTC {
		t.Fatalf("attempted_at location = %v, want UTC", store.inputs[0].AttemptedAt.Location())
	}
}

func TestServiceRunOnceRejectsUnsafeLimitsAndNilStore(t *testing.T) {
	for _, limit := range []int{-1, MaxTargetLimit + 1} {
		service := NewService(&apiKeyRecordCleanupStoreStub{})
		_, err := service.RunOnce(context.Background(), RunOnceInput{Limit: limit})
		if err == nil || !strings.Contains(err.Error(), "清理目标数量") {
			t.Fatalf("RunOnce(limit=%d) error = %v", limit, err)
		}
	}

	_, err := NewService(nil).RunOnce(context.Background(), RunOnceInput{})
	if err == nil || !strings.Contains(err.Error(), "存储不能为空") {
		t.Fatalf("RunOnce(nil store) error = %v", err)
	}
}

func TestServiceRunOnceReturnsStoreFailureWithoutInventingProgress(t *testing.T) {
	wantErr := errors.New("transaction rolled back")
	service := NewService(&apiKeyRecordCleanupStoreStub{err: wantErr})

	result, err := service.RunOnce(context.Background(), RunOnceInput{Limit: 1})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunOnce() error = %v, want %v", err, wantErr)
	}
	if result != (RunOnceResult{}) {
		t.Fatalf("RunOnce() result = %+v, want zero result", result)
	}
}

type apiKeyRecordCleanupStoreStub struct {
	inputs []port.APIKeyRecordCleanupRunInput
	result port.APIKeyRecordCleanupRunResult
	err    error
}

func (s *apiKeyRecordCleanupStoreStub) RunAPIKeyRecordCleanupOnce(
	_ context.Context,
	input port.APIKeyRecordCleanupRunInput,
) (port.APIKeyRecordCleanupRunResult, error) {
	s.inputs = append(s.inputs, input)
	if s.err != nil {
		return port.APIKeyRecordCleanupRunResult{}, s.err
	}
	return s.result, nil
}
