package circuitstore

// w12f_overlay_probe_arms_test.go 覆盖 overlay 只读面与探针状态存储的
// fail-closed 臂：miniredis 关闭后各操作的原始错误传播。

import (
	"context"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestW12fOverlayClosedClientArms(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "w12f"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	server.Close()
	ctx := context.Background()
	if _, err := store.ListDirtyEntries(ctx, 10); err == nil {
		t.Fatal("ListDirtyEntries 必须报错")
	}
	// Acknowledge 空入参在连接层之前返回（幂等语义），不作为错误分支。
	if err := store.Acknowledge(ctx, nil); err != nil {
		t.Fatalf("空入参 Acknowledge 必须幂等: %v", err)
	}
	if _, err := store.LoadSnapshots(ctx, []string{"w12f-acc"}); err == nil {
		t.Fatal("LoadSnapshots 必须报错")
	}
}

func TestW12fOverlayReconcilerDelegates(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	overlay, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "w12f"}, client)
	if err != nil {
		t.Fatal(err)
	}
	repo, db := openListAvailabilityFixture(t)
	t.Cleanup(func() { _ = db.Close() })
	reconciler := NewOverlayReconciler(overlay, repo)
	ctx := context.Background()
	if _, err := reconciler.ListDirtyEntries(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Acknowledge(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.LoadSnapshots(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.ExistingAccountIDs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.UpsertOverlays(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestW12fProbeStateClosedClientArms(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "w12f-space", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probeStore.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	_ = client
	// 直接关闭底层连接后所有读取必须失败。
	if err := probeStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := probeStore.get(context.Background(), "w12f-key"); err == nil {
		t.Fatal("关闭后 get 必须报错")
	}
}
