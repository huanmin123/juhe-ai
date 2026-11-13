package gatewayclientip

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// BUG-0361 根治契约：并发槽是带租约的 ZSET 成员，计数 = 清理过期成员后的
// ZCARD；异常路径（进程崩溃/释放丢失/acquire at-most-once）不再产生永久
// 计数漂移，租约到期即自动回收。
func newSlotLeaseTestTracker(t *testing.T, nowMs *int64) (*RedisAccountConcurrency, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client:    client,
		Namespace: "lease-test",
		Clock:     func() int64 { return *nowMs },
	})
	if err != nil {
		t.Fatalf("NewRedisAccountConcurrency: %v", err)
	}
	return instance, client
}

func TestRedisAccountConcurrencySlotLeaseExpiry(t *testing.T) {
	nowMs := int64(1_800_000_000_000)
	instance, _ := newSlotLeaseTestTracker(t, &nowMs)
	ctx := context.Background()

	outcome, err := instance.AcquireAccountConcurrency(ctx, "acc-lease", AccountConcurrencyLaneText, 60_000, 0, 0, 0)
	if err != nil || !outcome.Acquired {
		t.Fatalf("acquire: %+v err %v", outcome, err)
	}
	if got := instance.CurrentAccountConcurrency("acc-lease", ""); got != 1 {
		t.Fatalf("租约内并发=%d want 1", got)
	}

	// 超过租约：槽成员仍在 ZSET，但读面按 now 清理后不再计入。
	nowMs += 61_000
	if got := instance.CurrentAccountConcurrency("acc-lease", ""); got != 0 {
		t.Fatalf("租约过期后并发=%d want 0", got)
	}
	// 过期后的迟到 release 幂等（ZREM 不存在的成员不报错）。
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-lease", AccountConcurrencyLaneText, outcome.Token); err != nil {
		t.Fatalf("迟到 release 必须幂等: %v", err)
	}
}

func TestRedisAccountConcurrencySlotRefreshExtendsLease(t *testing.T) {
	nowMs := int64(1_800_000_000_000)
	instance, _ := newSlotLeaseTestTracker(t, &nowMs)
	ctx := context.Background()

	outcome, err := instance.AcquireAccountConcurrency(ctx, "acc-refresh", AccountConcurrencyLaneText, 60_000, 0, 0, 0)
	if err != nil || !outcome.Acquired {
		t.Fatalf("acquire: %+v err %v", outcome, err)
	}
	nowMs += 50_000
	refreshed, err := instance.RefreshAccountConcurrency(ctx, "acc-refresh", AccountConcurrencyLaneText, outcome.Token, 60_000)
	if err != nil || !refreshed {
		t.Fatalf("refresh: refreshed=%v err=%v", refreshed, err)
	}
	// 原租约到期点已过，但续租后仍应计入。
	nowMs += 50_000
	if got := instance.CurrentAccountConcurrency("acc-refresh", ""); got != 1 {
		t.Fatalf("续租后并发=%d want 1", got)
	}

	// 释放后 refresh 不得复活槽。
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-refresh", AccountConcurrencyLaneText, outcome.Token); err != nil {
		t.Fatalf("release: %v", err)
	}
	refreshed, err = instance.RefreshAccountConcurrency(ctx, "acc-refresh", AccountConcurrencyLaneText, outcome.Token, 60_000)
	if err != nil {
		t.Fatalf("refresh after release: %v", err)
	}
	if refreshed {
		t.Fatal("已释放的槽不能被 refresh 复活")
	}
}

func TestRedisAccountConcurrencyLaneIsolation(t *testing.T) {
	nowMs := int64(1_800_000_000_000)
	instance, _ := newSlotLeaseTestTracker(t, &nowMs)
	ctx := context.Background()

	text, err := instance.AcquireAccountConcurrency(ctx, "acc-lane", AccountConcurrencyLaneText, 60_000, 0, 0, 0)
	if err != nil || !text.Acquired {
		t.Fatalf("text acquire: %+v err %v", text, err)
	}
	if imageOutcome, err := instance.AcquireAccountConcurrency(ctx, "acc-lane", AccountConcurrencyLaneImage, 60_000, 0, 0, 0); err != nil || !imageOutcome.Acquired {
		t.Fatalf("image acquire: %+v err %v", imageOutcome, err)
	}
	if got := instance.CurrentAccountConcurrency("acc-lane", ""); got != 2 {
		t.Fatalf("total=%d want 2", got)
	}
	if got := instance.CurrentAccountConcurrency("acc-lane", AccountConcurrencyLaneText); got != 1 {
		t.Fatalf("text lane=%d want 1", got)
	}
	if got := instance.CurrentAccountConcurrency("acc-lane", AccountConcurrencyLaneImage); got != 1 {
		t.Fatalf("image lane=%d want 1", got)
	}
	if err := instance.ReleaseAccountConcurrency(ctx, "acc-lane", AccountConcurrencyLaneText, text.Token); err != nil {
		t.Fatal(err)
	}
	if got := instance.CurrentAccountConcurrency("acc-lane", AccountConcurrencyLaneImage); got != 1 {
		t.Fatalf("释放 text 后 image lane=%d want 1", got)
	}
	if got := instance.CurrentAccountConcurrency("acc-lane", ""); got != 1 {
		t.Fatalf("释放 text 后 total=%d want 1", got)
	}
}
