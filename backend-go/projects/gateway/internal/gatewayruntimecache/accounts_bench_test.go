package gatewayruntimecache

import (
	"context"
	"fmt"
	"testing"
)

// 审计 profile 批次（streamread/双快照/账户克隆/OAuth body）：账户读路径
// 「克隆 + 并发叠加」的分配画像。ListCachedOpenAIAccountsForGroup 命中缓存
// 条目后仍执行 applyConcurrencyOverlay（可用性过滤 + 逐账户深克隆 +
// CurrentConcurrency 注入），这是 /v1 每请求的固定成本；基准用 fake models
// 排除加载器与网络，只度量覆盖层本身。
func benchmarkAccountsReadOverlay(b *testing.B, accountCount int) {
	models := newFakeModels()
	accounts := make([]OpenAIAccountSecret, 0, accountCount)
	for i := 0; i < accountCount; i++ {
		account := testAccount(fmt.Sprintf("acc-%03d", i), "sys")
		account.SupportedModels = []string{"gpt-a", "gpt-b", "gpt-c"}
		account.ModelMappings = []AccountModelMapping{{SourceModel: "gpt-a", UpstreamModel: "gpt-b", Enabled: true}}
		account.Credentials = map[string]any{"apiKey": "upstream-secret", "baseUrl": "https://upstream.test/v1"}
		accounts = append(accounts, account)
	}
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: accounts}
	svc, err := New(models, Options{Clock: newManualClock()})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(svc.Close)
	ctx := context.Background()
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimecacheAccountsReadOverlay10(b *testing.B) { benchmarkAccountsReadOverlay(b, 10) }
func BenchmarkRuntimecacheAccountsReadOverlay50(b *testing.B) { benchmarkAccountsReadOverlay(b, 50) }

// 覆盖层隔离基准：直接度量 applyConcurrencyOverlay（可用性过滤 + 深克隆 +
// CurrentConcurrency 注入），排除读装配与并发加载器（生产加载器为
// Redis/内存实现，成本独立于本层）。
func BenchmarkRuntimecacheApplyConcurrencyOverlay50(b *testing.B) {
	accounts := make([]OpenAIAccountSecret, 0, 50)
	concurrency := map[string]int{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("acc-%03d", i)
		account := testAccount(id, "sys")
		account.SupportedModels = []string{"gpt-a", "gpt-b", "gpt-c"}
		account.ModelMappings = []AccountModelMapping{{SourceModel: "gpt-a", UpstreamModel: "gpt-b", Enabled: true}}
		account.Credentials = map[string]any{"apiKey": "upstream-secret", "baseUrl": "https://upstream.test/v1"}
		accounts = append(accounts, account)
		concurrency[id] = i % 3
	}
	svc, err := New(newFakeModels(), Options{Clock: newManualClock()})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(svc.Close)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.applyConcurrencyOverlay(accounts, concurrency); err != nil {
			b.Fatal(err)
		}
	}
}

// 单账户深克隆微基准（CloneStaticOpenAIAccountSecret 字段级克隆成本）。
func BenchmarkRuntimecacheCloneStaticOpenAIAccountSecret(b *testing.B) {
	account := testAccount("acc-000", "sys")
	account.SupportedModels = []string{"gpt-a", "gpt-b", "gpt-c"}
	account.ModelMappings = []AccountModelMapping{{SourceModel: "gpt-a", UpstreamModel: "gpt-b", Enabled: true}}
	account.Credentials = map[string]any{"apiKey": "upstream-secret", "baseUrl": "https://upstream.test/v1"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = CloneStaticOpenAIAccountSecret(account)
	}
}
