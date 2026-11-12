package gatewayclientip

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// TestRedisAccountConcurrencyExternal 在真实 Redis 上验证跨实例占槽/拒绝/
// RPM 窗口与释放；需要 JUHE_AI_TEST_REDIS_URL（可选
// JUHE_AI_TEST_REDIS_NAMESPACE，默认 dev-test）指向一次性 namespace。
func TestRedisAccountConcurrencyExternal(t *testing.T) {
	url := os.Getenv("JUHE_AI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("JUHE_AI_TEST_REDIS_URL 未设置，跳过真实 Redis 并发集成测试")
	}
	namespace := os.Getenv("JUHE_AI_TEST_REDIS_NAMESPACE")
	if namespace == "" {
		namespace = "dev-test"
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: url})
	if err != nil {
		t.Fatalf("create redis client: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	name := "rpm-it-" + time.Now().UTC().Format("20060102150405.000000000")
	newTracker := func() *RedisAccountConcurrency {
		tracker, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
			Client:    client,
			Namespace: namespace,
			Name:      name,
		})
		if err != nil {
			t.Fatalf("new tracker: %v", err)
		}
		return tracker
	}
	trackerA := newTracker()
	trackerB := newTracker()
	key := trackerA.accountConcurrencyKey("acc-external")
	t.Cleanup(func() {
		_ = client.Del(context.Background(), key, trackerA.slotsKey("acc-external"), trackerA.laneSlotsKey("acc-external", AccountConcurrencyLaneText)).Err()
	})

	outcome, err := trackerA.AcquireAccountConcurrency(ctx, "acc-external", AccountConcurrencyLaneText, time.Minute.Milliseconds(), 1, 1, 2)
	if err != nil || !outcome.Acquired {
		t.Fatalf("A acquire: outcome=%+v err=%v", outcome, err)
	}
	rejected, err := trackerB.AcquireAccountConcurrency(ctx, "acc-external", AccountConcurrencyLaneText, time.Minute.Milliseconds(), 1, 1, 2)
	if err != nil || rejected.Acquired || rejected.Total != 1 {
		t.Fatalf("B must see A's slot and reject: outcome=%+v err=%v", rejected, err)
	}
	allowed, current, _, err := trackerB.ConsumeRPM(ctx, "acc-external", 2)
	if err != nil || !allowed || current != 2 {
		t.Fatalf("B consume RPM: allowed=%v current=%d err=%v", allowed, current, err)
	}
	if err := trackerA.ReleaseAccountConcurrency(ctx, "acc-external", AccountConcurrencyLaneText, outcome.Token); err != nil {
		t.Fatalf("release: %v", err)
	}
	currentConcurrency, err := trackerB.LoadAccountCurrentConcurrencyByID(ctx, []string{"acc-external"})
	if err != nil || currentConcurrency["acc-external"] != 0 {
		t.Fatalf("release visibility: %v err=%v", currentConcurrency, err)
	}
}
