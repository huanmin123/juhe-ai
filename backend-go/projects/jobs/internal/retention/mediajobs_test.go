package retention

// 2026-11-06 复审 N4：清理单轮必须分批清到空/达到上限，避免积压时每小时
// 只清 500 行导致永久追不上；零/少量超期行保持单批退出。
import (
	"context"
	"testing"
	"time"
)

type fakeMediaJobsStore struct {
	expireRemaining int64
	deleteRemaining int64
	expireCalls     int
	deleteCalls     int
	batchSeen       []int
}

func (s *fakeMediaJobsStore) ExpireNonTerminalBefore(_ context.Context, _ string, limit int) (int64, error) {
	s.expireCalls++
	s.batchSeen = append(s.batchSeen, limit)
	n := minInt64(s.expireRemaining, int64(limit))
	s.expireRemaining -= n
	return n, nil
}

func (s *fakeMediaJobsStore) DeleteExpiredBefore(_ context.Context, _ string, limit int) (int64, error) {
	s.deleteCalls++
	n := minInt64(s.deleteRemaining, int64(limit))
	s.deleteRemaining -= n
	return n, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func TestMediaJobsRetentionDrainsMultipleBatches(t *testing.T) {
	store := &fakeMediaJobsStore{expireRemaining: 1200, deleteRemaining: 1200}
	job := &MediaJobsRetentionJob{
		Store: store, BatchSize: 500,
		Clock: func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) },
	}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExpiredNonTerminal != 1200 || result.DeletedRows != 1200 {
		t.Fatalf("result = %+v, want 1200/1200", result)
	}
	if result.Batches != 3 {
		t.Fatalf("batches = %d, want 3", result.Batches)
	}
	if store.expireCalls != 3 || store.deleteCalls != 3 {
		t.Fatalf("calls = expire:%d delete:%d, want 3/3", store.expireCalls, store.deleteCalls)
	}
}

func TestMediaJobsRetentionStopsOnEmptyBatch(t *testing.T) {
	store := &fakeMediaJobsStore{}
	job := &MediaJobsRetentionJob{Store: store, BatchSize: 500,
		Clock: func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) }}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Batches != 1 || store.expireCalls != 1 || store.deleteCalls != 1 {
		t.Fatalf("empty run = %+v calls expire:%d delete:%d, want single batch", result, store.expireCalls, store.deleteCalls)
	}
}
