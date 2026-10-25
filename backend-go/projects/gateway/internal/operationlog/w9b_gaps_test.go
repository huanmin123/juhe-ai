package operationlog

// w9b 剩余可达臂补充：OpenStore 打开失败、inode/别名守卫、SQLite 写路径错误、
// 配置校验与 normalize 矩阵的分支。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW9BNormalizedViewersArms(t *testing.T) {
	// 非法 reason。
	if _, err := normalizedViewers([]Viewer{{SystemAccountID: "v", VisibilityReason: "weird"}}, "full"); err == nil || !strings.Contains(err.Error(), "viewer is invalid") {
		t.Fatalf("非法 reason=%v", err)
	}
	// 非法 detail level。
	if _, err := normalizedViewers([]Viewer{{SystemAccountID: "v", VisibilityReason: "actor_self", DetailLevel: "weird"}}, "full"); err == nil || !strings.Contains(err.Error(), "detail level is invalid") {
		t.Fatalf("非法 detail=%v", err)
	}
	// 空 ID 跳过、默认 level、去重。
	out, err := normalizedViewers([]Viewer{
		{SystemAccountID: " ", VisibilityReason: "actor_self", DetailLevel: "full"},
		{SystemAccountID: "v", VisibilityReason: "actor_self"},
		{SystemAccountID: "v", VisibilityReason: "actor_self", DetailLevel: "summary"},
	}, "full")
	if err != nil || len(out) != 2 || out[0].DetailLevel != "full" {
		t.Fatalf("跳过空 ID/默认 level/去重=%+v err=%v", out, err)
	}
}

// 2026-09-30 C4 收编：JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS /
// MAX_IDLE_CONNS 收编为常量后 pool 值不再可配置，
// TestW9BLoadConfigPostgresPoolLimitsArm（非法池上限 fail 臂）随之删除。

func TestW9BProducerNilSafetyAndUnmarshalFallback(t *testing.T) {
	// nil 接收者与 nil store 都是 no-op。
	var nilProducer *Producer
	nilProducer.Record(Input{})
	(&Producer{}).Record(Input{})
	// 无法 JSON 序列化的值回退空串（normalizeSafeValue default 分支）。
	if got := normalizeSafeValue(make(chan int)); got != "" {
		t.Fatalf("不可序列化值=%v", got)
	}
}

type w9bAcquireOKStore struct{ w9bFakeStore }

func (s *w9bAcquireOKStore) AcquireOwnerLease(_ context.Context, owner string, _ time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{OwnerID: owner, FenceToken: 1}, true, nil
}

func TestW9BStartLeaseKeeperDefaultTTLAndTransientRenew(t *testing.T) {
	// ttl<=0 走默认值。
	store := &w9bAcquireOKStore{}
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "w9b-ttl", 0, nil)
	if err != nil || !ok {
		t.Fatalf("默认 TTL 启动=%v err=%v", ok, err)
	}
	if keeper.TTL() != defaultOwnerLease {
		t.Fatalf("TTL=%v", keeper.TTL())
	}
	keeper.Close()
	// Acquire 失败 → 不启动。
	failing := &w9bAcquireFailingStore{}
	if keeper, ok, err := StartLeaseKeeper(context.Background(), failing, "w9b-fail", time.Minute, nil); err == nil || ok || keeper != nil {
		t.Fatalf("Acquire 失败应拒绝：ok=%v err=%v", ok, err)
	}
}

type w9bAcquireFailingStore struct{ w9bFakeStore }

func (s *w9bAcquireFailingStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, false, w9bErrBoom
}

func TestW9BRunOwnerDefaultLogger(t *testing.T) {
	// logger 为 nil 时使用 slog.Default()，仍能优雅退出。
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	store := &w9bFakeStore{retentionDays: 1}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunOwner(runCtx, store, keeper, Config{RetentionInterval: time.Millisecond}, nil) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("默认 logger=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未退出")
	}
}
