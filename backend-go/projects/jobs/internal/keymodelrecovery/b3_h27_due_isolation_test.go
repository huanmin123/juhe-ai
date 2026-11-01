package keymodelrecovery

// B3-H27 回归：ListDue 必须逐项隔离脏 state（JSON 损坏 / capabilityHash
// 不符），移除其 due 成员并继续返回其余正常项；只有 Redis 基础设施错误
// 才让整轮扫描失败。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestB3H27ListDueIsolatesCorruptStates(t *testing.T) {
	store, _, client, keys := newWoRedisStore(t)
	ctx := context.Background()
	now := time.Unix(60_000, 0).UTC()

	early := stateFixture(t, "b3h27-good-early")
	early.Phase = Recovering
	early.RetryAt = now.Add(-3 * time.Second)
	later := stateFixture(t, "b3h27-good-later")
	later.Phase = Open
	later.RetryAt = now.Add(-2 * time.Second)
	for _, state := range []State{early, later} {
		seedState(t, ctx, client, keys, state)
		if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(state.RetryAt.UnixMilli()), Member: state.CapabilityHash}).Err(); err != nil {
			t.Fatal(err)
		}
	}

	corruptHash := "b3h27-corrupt"
	if err := client.Set(ctx, keys.State(corruptHash), "{broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	mismatchHash := "b3h27-mismatch"
	mismatched := stateFixture(t, mismatchHash)
	mismatched.CapabilityHash = "b3h27-other-hash"
	encoded, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, keys.State(mismatchHash), encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
	ghostHash := "b3h27-ghost"
	for _, hash := range []string{corruptHash, mismatchHash, ghostHash} {
		if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: hash}).Err(); err != nil {
			t.Fatal(err)
		}
	}

	states, err := store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("单条脏 state 不得阻塞扫描: %v", err)
	}
	if len(states) != 2 || states[0].CapabilityHash != early.CapabilityHash || states[1].CapabilityHash != later.CapabilityHash {
		t.Fatalf("正常 due 项必须继续返回: %#v", states)
	}
	for _, hash := range []string{corruptHash, mismatchHash, ghostHash} {
		if _, err := client.ZScore(ctx, keys.Due(), hash).Result(); err != redis.Nil {
			t.Fatalf("坏 due 成员 %q 必须被 ZREM: %v", hash, err)
		}
	}
	// 坏 state 的原始 key 不被本函数删除（只摘除 due 成员）。
	if exists, err := client.Exists(ctx, keys.State(corruptHash)).Result(); err != nil || exists != 1 {
		t.Fatalf("坏 state key 不应被删除: %d %v", exists, err)
	}
}
