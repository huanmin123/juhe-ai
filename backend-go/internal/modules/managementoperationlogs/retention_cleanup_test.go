package managementoperationlogs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestRetentionCleanupUsesSettingsCutoffAndBatches(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := &operationLogRetentionCleanerStub{
		retentionDays: 7,
		found:         true,
		deleted:       []int64{2, 1},
	}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{
		Store:      store,
		BatchPause: 25 * time.Millisecond,
		Sleep: func(_ context.Context, duration time.Duration) error {
			store.sleeps = append(store.sleeps, duration)
			return nil
		},
	})

	result, err := service.Cleanup(context.Background(), RetentionCleanupInput{
		Now:        now,
		BatchSize:  2,
		MaxBatches: 3,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.Deleted != 3 || result.Batches != 2 || result.RetentionDays != 7 || result.BatchSize != 2 || result.MaxBatches != 3 {
		t.Fatalf("result = %+v", result)
	}
	wantCutoff := now.Add(-7 * 24 * time.Hour)
	if !result.CutoffCreatedAt.Equal(wantCutoff) {
		t.Fatalf("CutoffCreatedAt = %v, want %v", result.CutoffCreatedAt, wantCutoff)
	}
	if len(store.inputs) != 2 {
		t.Fatalf("cleanup calls = %d, want 2", len(store.inputs))
	}
	for _, input := range store.inputs {
		if input.Limit != 2 || !input.CutoffCreatedAt.Equal(wantCutoff) {
			t.Fatalf("cleanup input = %+v", input)
		}
	}
	if len(store.sleeps) != 1 || store.sleeps[0] != 25*time.Millisecond {
		t.Fatalf("sleeps = %v", store.sleeps)
	}
}

func TestRetentionCleanupDefaultsMissingSetting(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := &operationLogRetentionCleanerStub{deleted: []int64{0}}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store, BatchPause: -1})

	result, err := service.Cleanup(context.Background(), RetentionCleanupInput{Now: now})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RetentionDays != DefaultOperationLogRetentionDays || result.BatchSize != DefaultOperationLogCleanupBatchSize || result.MaxBatches != DefaultOperationLogCleanupMaxBatches {
		t.Fatalf("result defaults = %+v", result)
	}
	if len(store.inputs) != 1 || store.inputs[0].Limit != DefaultOperationLogCleanupBatchSize {
		t.Fatalf("cleanup inputs = %+v", store.inputs)
	}
}

func TestRetentionCleanupOverrideSkipsSettingsRead(t *testing.T) {
	store := &operationLogRetentionCleanerStub{
		retentionErr: errors.New("settings should not be read"),
		deleted:      []int64{0},
	}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store, BatchPause: -1})

	result, err := service.Cleanup(context.Background(), RetentionCleanupInput{
		Now:           time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if store.retentionReads != 0 || result.RetentionDays != 30 {
		t.Fatalf("retentionReads=%d result=%+v", store.retentionReads, result)
	}
}

func TestRetentionCleanupRejectsInvalidInputs(t *testing.T) {
	service := NewRetentionCleanupService(&operationLogRetentionCleanerStub{})
	for _, tc := range []struct {
		name  string
		input RetentionCleanupInput
		want  string
	}{
		{name: "retention days", input: RetentionCleanupInput{RetentionDays: MaxOperationLogRetentionDays + 1}, want: "operationLogRetentionDays"},
		{name: "negative retention days", input: RetentionCleanupInput{RetentionDays: -1}, want: "operationLogRetentionDays"},
		{name: "batch size", input: RetentionCleanupInput{BatchSize: -1}, want: "单批数量"},
		{name: "max batches", input: RetentionCleanupInput{MaxBatches: -1}, want: "单轮批数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Cleanup(context.Background(), tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Cleanup() error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

type operationLogRetentionCleanerStub struct {
	retentionDays  int
	found          bool
	retentionErr   error
	retentionReads int
	deleted        []int64
	inputs         []port.OperationLogCleanupInput
	sleeps         []time.Duration
}

func (s *operationLogRetentionCleanerStub) GetOperationLogRetentionDays(_ context.Context) (int, bool, error) {
	s.retentionReads++
	if s.retentionErr != nil {
		return 0, false, s.retentionErr
	}
	return s.retentionDays, s.found, nil
}

func (s *operationLogRetentionCleanerStub) CleanupOperationLogsBefore(_ context.Context, input port.OperationLogCleanupInput) (int64, error) {
	s.inputs = append(s.inputs, input)
	if len(s.deleted) == 0 {
		return 0, nil
	}
	deleted := s.deleted[0]
	s.deleted = s.deleted[1:]
	return deleted, nil
}
