package accounthealth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// D 任务④：loadDirectSchedule 短 TTL 缓存的单测（fake clock + 计数 fetch）。
// 真实 fetch（只读事务查 system_settings）路径由 PG 集成测试覆盖；这里验证
// 缓存语义本身：TTL 内命中不重读、过期重读、失败不缓存。

// TestDirectScheduleCacheTTLLifecycle：miss 查询一次；TTL 内命中不再查询；
// 过期后重读并回填。
func TestDirectScheduleCacheTTLLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	fetches := 0
	schedule := Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond)}
	timezone := time.UTC
	cache := &directScheduleCache{ttl: directScheduleCacheTTL, now: clock}
	fetch := func(context.Context) (Schedule, *time.Location, error) {
		fetches++
		return schedule, timezone, nil
	}

	gotSchedule, gotTZ, err := cache.load(context.Background(), fetch)
	if err != nil || fetches != 1 || gotSchedule.HealthIntervalMS != schedule.HealthIntervalMS || gotTZ != timezone {
		t.Fatalf("first load: fetches=%d schedule=%v tz=%v err=%v", fetches, gotSchedule, gotTZ, err)
	}
	now = now.Add(30 * time.Second)
	if _, _, err = cache.load(context.Background(), fetch); err != nil || fetches != 1 {
		t.Fatalf("load within ttl must hit cache: fetches=%d err=%v", fetches, err)
	}
	now = now.Add(31 * time.Second)
	gotSchedule, _, err = cache.load(context.Background(), fetch)
	if err != nil || fetches != 2 || gotSchedule.HealthIntervalMS != schedule.HealthIntervalMS {
		t.Fatalf("load after ttl must refetch: fetches=%d schedule=%v err=%v", fetches, gotSchedule, err)
	}
	// 回填后的快照再次进入命中窗口。
	now = now.Add(10 * time.Second)
	if _, _, err = cache.load(context.Background(), fetch); err != nil || fetches != 2 {
		t.Fatalf("refetched entry must be cached again: fetches=%d err=%v", fetches, err)
	}
}

// TestDirectScheduleCacheFetchFailureNotCached：fetch 失败原样返回错误且不
// 缓存——下一次读取立即重试真实查询（不把失败固化成 TTL 窗口内的空转）。
func TestDirectScheduleCacheFetchFailureNotCached(t *testing.T) {
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	fetches := 0
	fetchErr := errors.New("system_settings unavailable")
	cache := &directScheduleCache{ttl: directScheduleCacheTTL, now: clock}
	fetch := func(context.Context) (Schedule, *time.Location, error) {
		fetches++
		if fetches == 1 {
			return Schedule{}, nil, fetchErr
		}
		return Schedule{HealthIntervalMS: 1}, time.UTC, nil
	}

	if _, _, err := cache.load(context.Background(), fetch); !errors.Is(err, fetchErr) {
		t.Fatalf("fetch error must surface verbatim: %v", err)
	}
	now = now.Add(2 * directScheduleCacheTTL)
	schedule, _, err := cache.load(context.Background(), fetch)
	if err != nil || fetches != 2 || schedule.HealthIntervalMS != 1 {
		t.Fatalf("failed fetch must not be cached: fetches=%d schedule=%v err=%v", fetches, schedule, err)
	}
}
