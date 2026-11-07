package gatewayquota

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// TestRedisInflightQuotaExternal 在真实 Redis 上验证多 scope 原子预占/
// 拒绝/释放；需要 JUHE_AI_TEST_REDIS_URL 与可选
// JUHE_AI_TEST_REDIS_NAMESPACE（默认 dev-test）。
func TestRedisInflightQuotaExternal(t *testing.T) {
	url := os.Getenv("JUHE_AI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("JUHE_AI_TEST_REDIS_URL 未设置，跳过真实 Redis 额度集成测试")
	}
	namespace := os.Getenv("JUHE_AI_TEST_REDIS_NAMESPACE")
	if namespace == "" {
		namespace = "dev-test"
	}
	// 每次运行使用独立子 namespace，清理只影响本用例，避免并行用例互相
	// 清空状态，也避免误配到现网 namespace 时删除共享在途预留。
	namespace = fmt.Sprintf("%s-it-%d", namespace, time.Now().UnixNano())
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: url})
	if err != nil {
		t.Fatalf("create redis client: %v", err)
	}
	defer client.Close()
	service, err := NewInflightQuotaService(InflightQuotaConfig{
		Redis:          client,
		RedisNamespace: namespace,
		Timer:          func(time.Duration, func()) func() { return func() {} },
	})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	ctx := context.Background()
	t.Cleanup(func() { service.ClearForTest() })
	limits10 := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 10}}
	limits100 := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 100}}
	input := ScopeReserveInput{
		Scopes: []InflightScope{
			{ScopeKey: "it:api-key", Limits: limits10, CurrentCosts: RequestQuotaCosts{Total: 9}},
			{ScopeKey: "it:auth", Limits: limits100, CurrentCosts: RequestQuotaCosts{}},
		},
		EstimatedCostUsd: 2,
	}
	decision, releases, err := service.remote.reserveScopes(ctx, input)
	if err != nil || decision.Allowed || len(releases) != 0 {
		t.Fatalf("API key scope must deny: decision=%+v releases=%d err=%v", decision, len(releases), err)
	}
	input.Scopes[0].CurrentCosts = RequestQuotaCosts{}
	decision, releases, err = service.remote.reserveScopes(ctx, input)
	if err != nil || !decision.Allowed || len(releases) != 2 {
		t.Fatalf("reserve: decision=%+v releases=%d err=%v", decision, len(releases), err)
	}
	// 第二个服务实例共享同一 Redis：它必须看到第一个实例写入的在途成本并
	// 拒绝会超过 API Key 限额的新预留。
	otherService, err := NewInflightQuotaService(InflightQuotaConfig{
		Redis:          client,
		RedisNamespace: namespace,
		Timer:          func(time.Duration, func()) func() { return func() {} },
	})
	if err != nil {
		t.Fatalf("second service: %v", err)
	}
	deniedInput := ScopeReserveInput{
		Scopes:           []InflightScope{{ScopeKey: "it:api-key", Limits: limits10, CurrentCosts: RequestQuotaCosts{Total: 9}}},
		EstimatedCostUsd: 0.5,
	}
	denied, _, err := otherService.remote.reserveScopes(ctx, deniedInput)
	if err != nil || denied.Allowed {
		t.Fatalf("second instance must see reserved cost and deny: decision=%+v err=%v", denied, err)
	}
	if err := service.remote.release(ctx, releases); err != nil {
		t.Fatalf("release: %v", err)
	}
	states, err := service.remote.snapshot(ctx)
	if err != nil || len(states) != 0 {
		t.Fatalf("snapshot after release: states=%+v err=%v", states, err)
	}
}
