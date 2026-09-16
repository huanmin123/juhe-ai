package keymodelrecovery

// w12b 波次 RedisStore 分支覆盖测试：基于 miniredis 与按命令错误注入。
//
// 不可达语句登记（无法通过任何输入触达）：
//   - redis_store.go Acquire 的 `len(result) != 2` 臂：内置 Lua 脚本恒返回
//     两元素表，miniredis 无法改写脚本返回形状。
//   - redis_store.go Acquire 的 applied 分支 json.Unmarshal 错误臂：applied
//     返回值是 Lua cjson 对已存储 state 的重编码，且 acquire 前置校验已排除
//     非 Go State 兼容的 JSON 类型，Go 侧反序列化恒成功。
//   - redis_store.go Commit 的 json.Marshal(next) 错误臂：State 编码恒成功。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
)

// w12bRedisFailCmd 对指定 Redis 命令注入错误，返回恢复函数。
func w12bRedisFailCmd(m *miniredis.Miniredis, command, message string) (restore func()) {
	srv := m.Server()
	target := strings.ToUpper(command)
	srv.SetPreHook(func(peer *server.Peer, cmd string, _ ...string) bool {
		if cmd == target {
			peer.WriteError(message)
			return true
		}
		return false
	})
	return func() { srv.SetPreHook(nil) }
}

func TestW12bRedisStoreErrorArms(t *testing.T) {
	ctx := context.Background()
	store, mini, client, keys := newWoRedisStore(t)
	now := time.Unix(30_000, 0).UTC()

	// ServerNow 错误臂。
	restore := w12bRedisFailCmd(mini, "time", "w12b time failure")
	if _, err := store.ServerNow(ctx); err == nil {
		t.Fatal("TIME 注入错误应冒泡")
	}
	restore()

	// ListDue：limit 非法、ZRANGEBYSCORE 注入错误。
	if _, err := store.ListDue(ctx, now, 0); err == nil {
		t.Fatal("limit<1 应报错")
	}
	restore = w12bRedisFailCmd(mini, "zrangebyscore", "w12b zrange failure")
	if _, err := store.ListDue(ctx, now, 10); err == nil {
		t.Fatal("ZRANGEBYSCORE 注入错误应冒泡")
	}
	restore()

	// ListDue：state GET 注入错误（due 集合已含成员，ZRANGEBYSCORE 正常放行）。
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: 1, Member: "w12b-due-get"}).Err(); err != nil {
		t.Fatal(err)
	}
	restore = w12bRedisFailCmd(mini, "get", "w12b get failure")
	if _, err := store.ListDue(ctx, now, 10); err == nil {
		t.Fatal("state GET 注入错误应冒泡")
	}
	restore()
	if err := client.Del(ctx, keys.Due()).Err(); err != nil {
		t.Fatal(err)
	}

	// ListDue：state 完整性校验失败（非法 JSON 与 hash 不匹配走同一错误臂）。
	if err := client.Set(ctx, keys.State("w12b-bad"), "not-json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: 1, Member: "w12b-bad"}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, now, 10); err == nil {
		t.Fatal("非法 state JSON 应触发完整性错误")
	}
	mismatched := w12bDueCandidate(t, now)
	mismatched.CapabilityHash = "w12b-other-hash"
	validButMismatched, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, keys.State("w12b-bad"), validButMismatched, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDue(ctx, now, 10); err == nil {
		t.Fatal("hash 不匹配应触发完整性错误")
	}
	if err := client.Del(ctx, keys.State("w12b-bad"), keys.Due()).Err(); err != nil {
		t.Fatal(err)
	}

	// Acquire / CleanClosed / Renew 注入错误臂。
	restore = w12bRedisFailCmd(mini, "eval", "w12b eval failure")
	candidate := w12bDueCandidate(t, now)
	if _, _, err := store.Acquire(ctx, candidate, "w12b-lease", false, false); err == nil {
		t.Fatal("EVAL 注入错误应冒泡")
	}
	if _, err := store.CleanClosed(ctx, 1000); err == nil {
		t.Fatal("EVAL 注入错误应冒泡")
	}
	if _, err := store.Renew(ctx, candidate, "w12b-lease"); err == nil {
		t.Fatal("Renew 注入错误应冒泡")
	}
	restore()

	// CleanClosed limit 边界。
	if _, err := store.CleanClosed(ctx, 0); err == nil {
		t.Fatal("CleanClosed limit<1 应报错")
	}
	if _, err := store.CleanClosed(ctx, 1001); err == nil {
		t.Fatal("CleanClosed limit>1000 应报错")
	}

	// OpenRedisStore 错误臂：未启用与非法 URL。
	if _, err := OpenRedisStore(RedisConfig{}); err == nil {
		t.Fatal("未启用应报错")
	}
	if _, err := OpenRedisStore(RedisConfig{Enabled: true, URL: "not-a-redis-url", Namespace: "w12b-space"}); err == nil {
		t.Fatal("非法 URL 应报错")
	}
}

func TestW12bRedisListDueSortsSamePhaseByRetryAt(t *testing.T) {
	ctx := context.Background()
	store, mini, client, keys := newWoRedisStore(t)
	now := time.Unix(30_000, 0).UTC()

	// 两个同源 OPEN state（不同 hash、RetryAt 相差 1s）：验证同相位排序比较臂按 RetryAt 升序。
	base := w12bDueCandidate(t, now)
	later := base
	base.CapabilityHash = "w12b-sort-early"
	later.CapabilityHash = "w12b-sort-later"
	later.RetryAt = base.RetryAt.Add(time.Second)
	for _, state := range []State{base, later} {
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Set(ctx, keys.State(state.CapabilityHash), raw, 0).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(ctx, keys.Due(), redis.Z{Score: float64(state.RetryAt.UnixMilli()), Member: state.CapabilityHash}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	states, err := store.ListDue(ctx, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("应读取两个 due state: %d", len(states))
	}
	if states[0].CapabilityHash != base.CapabilityHash || states[1].CapabilityHash != later.CapabilityHash {
		t.Fatalf("应按 RetryAt 升序返回: %s %s", states[0].CapabilityHash, states[1].CapabilityHash)
	}
	_ = mini
}

func TestW12bRedisConfigArms(t *testing.T) {
	if _, err := LoadRedisConfig(nil); err == nil {
		t.Fatal("nil getenv 应报错")
	}
	// 未启用：URL 为空时直接透传。
	cfg, err := LoadRedisConfig(func(key string) string {
		if key == "JUHE_AI_REDIS_NAMESPACE" {
			return "w12b-space"
		}
		return "  "
	})
	if err != nil || cfg.Enabled {
		t.Fatalf("空 URL 应保持未启用: %#v %v", cfg, err)
	}
	// 启用但 namespace 非法。
	if _, err := LoadRedisConfig(func(key string) string {
		if key == "JUHE_AI_REDIS_STATE_URL" {
			return "redis://127.0.0.1:6379/9"
		}
		return "illegal namespace!"
	}); err == nil {
		t.Fatal("非法 namespace 应报错")
	}
	// 启用且合法。
	cfg, err = LoadRedisConfig(func(key string) string {
		if key == "JUHE_AI_REDIS_STATE_URL" {
			return "redis://127.0.0.1:6379/9"
		}
		return "w12b-space"
	})
	if err != nil || !cfg.Enabled {
		t.Fatalf("合法配置应启用: %#v %v", cfg, err)
	}

	// RedisKeys namespace 校验与键族。
	if _, err := NewRedisKeys("bad space!"); err == nil {
		t.Fatal("非法 namespace 应报错")
	}
	keys, err := NewRedisKeys("w12b-space")
	if err != nil {
		t.Fatal(err)
	}
	if keys.State("h") == "" || keys.Lease("h") == "" || keys.Due() == "" || keys.Closed() == "" || keys.GlobalProbes() == "" || keys.SourceProbes("src") == "" {
		t.Fatal("键族不应为空")
	}

	// Close 空安全臂。
	var nilStore *RedisStore
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close 应为 nil: %v", err)
	}
}
