package circuitstore

// w13g5_circuit_redis_arms_test.go 覆盖 Redis 侧残余错误臂：探针运行态
// 结算/提交、overlay 对账、运行态读取的 Eval 失败出口与自建连接 Close。
// 复用 final_edges_2_test.go 的 evalStubClient 注入模式。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func TestW13g5ProbeStateStoreConstructionArms(t *testing.T) {
	if _, err := NewProbeStateStore("  ", "ns", nil); err == nil {
		t.Fatal("缺少 Redis URL 必须报错")
	}
	if _, err := NewProbeStateStore("://w13g5-bad", "ns", nil); err == nil {
		t.Fatal("非法 Redis URL 必须报错")
	}
	// 自建连接 → Close 覆盖 client.Close 分支。
	server := miniredisRunForTest(t)
	store, err := NewProbeStateStore("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// w13g5EvalFailClient 在 evalStubClient 基础上透传 Get（结算路径先 HGet 后
// Eval；嵌入接口为 nil 时直接 Eval 失败即可，无需 Get 代理——此处用独立 stub
// 只覆写 Eval，Get 走 miniredis 内层）。
type w13g5EvalFailClient struct {
	redis.Cmdable
	inner redis.Cmdable
}

func (e *w13g5EvalFailClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmdResult(nil, errW13g5Redis)
}

func (e *w13g5EvalFailClient) Get(ctx context.Context, key string) *redis.StringCmd {
	return e.inner.Get(ctx, key)
}

func (e *w13g5EvalFailClient) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	return e.inner.HGet(ctx, key, field)
}

func (e *w13g5EvalFailClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	return e.inner.Del(ctx, keys...)
}

var errW13g5Redis = errors.New("w13g5 conn refused")

func newW13g5EvalFail(t *testing.T) *w13g5EvalFailClient {
	t.Helper()
	server := miniredisRunForTest(t)
	t.Cleanup(func() { server.Close() })
	inner := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = inner.Close() })
	return &w13g5EvalFailClient{inner: inner}
}

func TestW13g5ProbeStateEvalErrorArms(t *testing.T) {
	ctx := context.Background()
	// store 与播种用的 miniredis 共享同一内层连接。
	server0 := miniredisRunForTest(t)
	t.Cleanup(func() { server0.Close() })
	inner0 := redis.NewClient(&redis.Options{Addr: server0.Addr()})
	t.Cleanup(func() { _ = inner0.Close() })
	stub := &w13g5EvalFailClient{inner: inner0}
	store, err := NewProbeStateStore("", "ns", stub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.acquireGenerationRun(ctx, "w13g5-key", 1, "w13g5-run", 100, 1000); err == nil {
		t.Fatal("acquire Eval 失败必须传播")
	}
	// 播种可结算状态（写入共享 server0）后触发 Eval 失败。
	seedStore, err := NewProbeStateStore("", "ns", inner0)
	if err != nil {
		t.Fatal(err)
	}
	seedState := probeState{RuntimeKey: "w13g5-key", Generation: 1}
	pendingTrue := true
	fences := []string{encodeSourceFence(ProbeSourceFence{})}
	seedState.DispatchPending = &pendingTrue
	seedState.SourceFences = &fences
	seedJSON, err := json.Marshal(seedState)
	if err != nil {
		t.Fatal(err)
	}
	if err := server0.Set(seedStore.stateKey("w13g5-key"), string(seedJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleDispatchedBySourceFence(ctx, "w13g5-key", 1, ProbeSourceFence{}, "success", nil); err == nil {
		t.Fatal("结算 Eval 失败必须传播")
	}
	state := probeState{RuntimeKey: "w13g5-key", Generation: 1}
	stub2 := newW13g5EvalFail(t)
	store2, err := NewProbeStateStore("", "ns", stub2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store2.commitGenerationRun(ctx, state, "w13g5-run", 1000); err == nil {
		t.Fatal("commit Eval 失败必须传播")
	}
}

func TestW13g5OverlayAndRuntimeCloseArms(t *testing.T) {
	// 自建连接 Close 分支。
	server := miniredisRunForTest(t)
	overlay, err := NewOverlayRedisStore(OverlayRedisConfig{URL: "redis://" + server.Addr(), Namespace: "w13g5-ns"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewRuntimeStateReader("redis://"+server.Addr(), "w13g5-ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	// Redis store 自建连接 Close。
	redisStore, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Namespace: "w13g5-ns", Capacity: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := redisStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5OverlayEvalErrorArms(t *testing.T) {
	ctx := context.Background()
	stub := newW13g5EvalFail(t)
	overlay, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "w13g5-ns"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.ListDirtyEntries(ctx, 10); err == nil {
		t.Fatal("ListDirtyEntries Eval 失败必须传播")
	}
	stub2 := newW13g5EvalFail(t)
	overlay2, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "w13g5-ns"}, stub2)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay2.Acknowledge(ctx, []opsjobs.OverlayEntry{{AccountID: "w13g5-acc"}}); err == nil {
		t.Fatal("Acknowledge Eval 失败必须传播")
	}
}
