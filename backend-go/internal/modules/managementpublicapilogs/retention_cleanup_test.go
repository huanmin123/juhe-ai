package managementpublicapilogs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestPublicAPILogRetentionCleanupUsesOneUTCCutoffAcrossBatches(t *testing.T) {
	now := time.Date(2026, 7, 10, 16, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	store := &publicAPILogRetentionCleanerStub{
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
	if result.Phase != RetentionCleanupPhaseComplete || result.Partial {
		t.Fatalf("completion result = %+v", result)
	}
	wantCutoff := now.UTC().Add(-7 * 24 * time.Hour)
	if !result.CutoffCreatedAt.Equal(wantCutoff) || result.CutoffCreatedAt.Location() != time.UTC {
		t.Fatalf("CutoffCreatedAt = %v, want UTC %v", result.CutoffCreatedAt, wantCutoff)
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

func TestPublicAPILogRetentionCleanupDefaultsAndStopsAtConfiguredMaximum(t *testing.T) {
	store := &publicAPILogRetentionCleanerStub{deleted: []int64{1, 1, 1}}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store, BatchPause: -1})

	result, err := service.Cleanup(context.Background(), RetentionCleanupInput{
		Now:        time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		BatchSize:  1,
		MaxBatches: 2,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RetentionDays != DefaultPublicAPILogRetentionDays || result.Deleted != 2 || result.Batches != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(store.inputs) != 2 {
		t.Fatalf("cleanup calls = %d, want 2", len(store.inputs))
	}
}

func TestPublicAPILogRetentionCleanupReturnsPartialResultWhenSecondBatchFails(t *testing.T) {
	wantErr := errors.New("second batch failed")
	store := &publicAPILogRetentionCleanerStub{
		deleted:       []int64{2},
		cleanupErrors: []error{nil, wantErr},
	}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store, BatchPause: -1})

	result, err := service.Cleanup(context.Background(), RetentionCleanupInput{
		Now:        time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		BatchSize:  2,
		MaxBatches: 3,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Cleanup() error = %v, want %v", err, wantErr)
	}
	if result.Deleted != 2 || result.Batches != 1 || !result.Partial || result.Phase != RetentionCleanupPhasePublicAPILogs {
		t.Fatalf("result = %+v", result)
	}
}

func TestPublicAPILogRetentionCleanupReturnsPartialResultWhenPauseIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &publicAPILogRetentionCleanerStub{deleted: []int64{1}}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{
		Store:      store,
		BatchPause: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	})

	result, err := service.Cleanup(ctx, RetentionCleanupInput{
		Now:        time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		BatchSize:  1,
		MaxBatches: 2,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup() error = %v, want context.Canceled", err)
	}
	if result.Deleted != 1 || result.Batches != 1 || !result.Partial || result.Phase != RetentionCleanupPhasePublicAPILogs {
		t.Fatalf("result = %+v", result)
	}
}

func TestPublicAPILogRetentionCleanupHonorsCancellationBeforeDeleting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &publicAPILogRetentionCleanerStub{retentionDays: 30, found: true, deleted: []int64{1}}
	service := NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store, BatchPause: -1})

	_, err := service.Cleanup(ctx, RetentionCleanupInput{Now: time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup() error = %v, want context.Canceled", err)
	}
	if len(store.inputs) != 0 {
		t.Fatalf("cleanup calls = %d, want 0", len(store.inputs))
	}
}

func TestPublicAPILogRetentionCleanupRejectsInvalidInputs(t *testing.T) {
	service := NewRetentionCleanupService(&publicAPILogRetentionCleanerStub{})
	for _, tc := range []struct {
		name  string
		input RetentionCleanupInput
		want  string
	}{
		{name: "retention days", input: RetentionCleanupInput{RetentionDays: MaxPublicAPILogRetentionDays + 1}, want: "publicApiLogRetentionDays"},
		{name: "negative retention days", input: RetentionCleanupInput{RetentionDays: -1}, want: "publicApiLogRetentionDays"},
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

type publicAPILogRetentionCleanerStub struct {
	retentionDays int
	found         bool
	deleted       []int64
	cleanupErrors []error
	inputs        []port.PublicAPILogCleanupInput
	sleeps        []time.Duration
}

func (s *publicAPILogRetentionCleanerStub) GetPublicAPILogRetentionDays(context.Context) (int, bool, error) {
	return s.retentionDays, s.found, nil
}

func (s *publicAPILogRetentionCleanerStub) CleanupPublicAPILogsBefore(_ context.Context, input port.PublicAPILogCleanupInput) (int64, error) {
	s.inputs = append(s.inputs, input)
	if len(s.cleanupErrors) > 0 {
		err := s.cleanupErrors[0]
		s.cleanupErrors = s.cleanupErrors[1:]
		if err != nil {
			return 0, err
		}
	}
	if len(s.deleted) == 0 {
		return 0, nil
	}
	deleted := s.deleted[0]
	s.deleted = s.deleted[1:]
	return deleted, nil
}
