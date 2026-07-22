package runtimelogretention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestCleanupSkipsAllStorageWhenIndexDisabled(t *testing.T) {
	store := &retentionStoreStub{settingErr: errors.New("must not read settings")}
	service := NewService(store)

	result, err := service.Cleanup(context.Background(), CleanupInput{IndexEnabled: false})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.IndexEnabled || result.RuntimeLogs != 0 || result.RuntimeLogFileCursors != 0 {
		t.Fatalf("result = %+v", result)
	}
	if store.settingReads != 0 || len(store.indexInputs) != 0 || len(store.cursorInputs) != 0 {
		t.Fatalf("disabled cleanup touched store: %+v", store)
	}
}

func TestCleanupUsesFixedCutoffAndIndependentBatches(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 34, 56, 987654321, time.UTC)
	store := &retentionStoreStub{
		settingDays:   7,
		settingFound:  true,
		indexDeleted:  []int64{2, 1},
		cursorDeleted: []int64{2, 0},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:      store,
		BatchPause: 25 * time.Millisecond,
		Sleep: func(_ context.Context, duration time.Duration) error {
			store.sleeps = append(store.sleeps, duration)
			return nil
		},
	})

	result, err := service.Cleanup(context.Background(), CleanupInput{
		IndexEnabled: true,
		Now:          now,
		BatchSize:    2,
		MaxBatches:   3,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RetentionDays != 7 || result.RuntimeLogs != 3 || result.RuntimeLogFileCursors != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.RuntimeLogBatches != 2 || result.RuntimeLogFileCursorBatches != 1 {
		t.Fatalf("batch result = %+v", result)
	}
	wantCutoff := "2026-07-15T12:34:56.987Z"
	if result.CutoffISO != wantCutoff {
		t.Fatalf("CutoffISO = %q, want %q", result.CutoffISO, wantCutoff)
	}
	for _, input := range append(append([]port.RuntimeLogRetentionCleanupInput{}, store.indexInputs...), store.cursorInputs...) {
		if input.CutoffISO != wantCutoff || input.Limit != 2 {
			t.Fatalf("cleanup input = %+v", input)
		}
	}
	if len(store.sleeps) != 2 || store.sleeps[0] != 25*time.Millisecond || store.sleeps[1] != 25*time.Millisecond {
		t.Fatalf("sleeps = %v", store.sleeps)
	}
}

func TestCleanupDefaultsAndClampsCatalogSetting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		days  int
		found bool
		want  int
	}{
		{name: "missing", want: DefaultRetentionDays},
		{name: "zero", days: 0, found: true, want: DefaultRetentionDays},
		{name: "below minimum", days: -4, found: true, want: MinRetentionDays},
		{name: "above maximum", days: 120, found: true, want: MaxRetentionDays},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &retentionStoreStub{settingDays: tc.days, settingFound: tc.found}
			service := NewServiceWithOptions(ServiceOptions{Store: store, BatchPause: -1})
			result, err := service.Cleanup(context.Background(), CleanupInput{
				IndexEnabled: true,
				Now:          time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if result.RetentionDays != tc.want || result.BatchSize != DefaultBatchSize || result.MaxBatches != DefaultMaxBatches {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestCleanupOverrideSkipsSettingAndValidatesRange(t *testing.T) {
	store := &retentionStoreStub{settingErr: errors.New("must not read setting")}
	service := NewServiceWithOptions(ServiceOptions{Store: store, BatchPause: -1})
	result, err := service.Cleanup(context.Background(), CleanupInput{
		IndexEnabled:  true,
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RetentionDays != 30 || store.settingReads != 0 {
		t.Fatalf("result=%+v settingReads=%d", result, store.settingReads)
	}

	for _, days := range []int{-1, MaxRetentionDays + 1} {
		_, err := service.Cleanup(context.Background(), CleanupInput{IndexEnabled: true, RetentionDays: days})
		if err == nil || !strings.Contains(err.Error(), "runtimeLogIndexRetentionDays") {
			t.Fatalf("RetentionDays=%d error=%v", days, err)
		}
	}
}

func TestCleanupStopsWhenContextIsCancelledDuringPause(t *testing.T) {
	store := &retentionStoreStub{indexDeleted: []int64{1, 1}}
	ctx, cancel := context.WithCancel(context.Background())
	service := NewServiceWithOptions(ServiceOptions{
		Store:      store,
		BatchPause: 25 * time.Millisecond,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	})
	_, err := service.Cleanup(ctx, CleanupInput{IndexEnabled: true, BatchSize: 1, MaxBatches: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup() error = %v, want context.Canceled", err)
	}
}

type retentionStoreStub struct {
	settingDays   int
	settingFound  bool
	settingErr    error
	settingReads  int
	indexDeleted  []int64
	cursorDeleted []int64
	indexInputs   []port.RuntimeLogRetentionCleanupInput
	cursorInputs  []port.RuntimeLogRetentionCleanupInput
	sleeps        []time.Duration
}

func (s *retentionStoreStub) GetRuntimeLogIndexRetentionDays(context.Context) (int, bool, error) {
	s.settingReads++
	return s.settingDays, s.settingFound, s.settingErr
}

func (s *retentionStoreStub) CleanupRuntimeLogIndexBefore(_ context.Context, input port.RuntimeLogRetentionCleanupInput) (int64, error) {
	s.indexInputs = append(s.indexInputs, input)
	if len(s.indexDeleted) == 0 {
		return 0, nil
	}
	deleted := s.indexDeleted[0]
	s.indexDeleted = s.indexDeleted[1:]
	return deleted, nil
}

func (s *retentionStoreStub) CleanupCompletedRuntimeLogFileCursorsBefore(_ context.Context, input port.RuntimeLogRetentionCleanupInput) (int64, error) {
	s.cursorInputs = append(s.cursorInputs, input)
	if len(s.cursorDeleted) == 0 {
		return 0, nil
	}
	deleted := s.cursorDeleted[0]
	s.cursorDeleted = s.cursorDeleted[1:]
	return deleted, nil
}
