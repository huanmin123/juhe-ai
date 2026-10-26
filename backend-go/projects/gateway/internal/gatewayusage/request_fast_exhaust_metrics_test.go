package gatewayusage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestRequestFastExhaustMetricsFamilies：请求级提前耗尽与负面缓存三计数器
// 接入既有 registry（/__aisys__/metrics 曝光面），实例方法驱动计数、初始
// 不渲染子序列。
func TestRequestFastExhaustMetricsFamilies(t *testing.T) {
	metrics := newPrometheusMetrics()
	// client_golang 的 CounterVec 无子序列时整族不渲染（与既有惰性注册家族
	// 的曝光语义一致）：首个事件前整族缺席。
	baseline := metrics.gatherText()
	for _, name := range []string{
		"juhe_ai_gateway_request_fast_exhausted_total",
		"juhe_ai_gateway_request_negative_cache_hits_total",
		"juhe_ai_gateway_request_negative_cache_capacity_rejected_total",
	} {
		if strings.Contains(baseline, name) {
			t.Fatalf("family %s must stay absent before the first event", name)
		}
	}
	metrics.recordRequestFastExhausted()
	metrics.recordNegativeCacheHit()
	metrics.recordNegativeCacheHit()
	metrics.recordNegativeCacheCapacityRejected()
	text := metrics.gatherText()
	for _, name := range []string{
		"juhe_ai_gateway_request_fast_exhausted_total",
		"juhe_ai_gateway_request_negative_cache_hits_total",
		"juhe_ai_gateway_request_negative_cache_capacity_rejected_total",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("family %s missing from exposition after events", name)
		}
	}
	for _, fragment := range []string{
		`juhe_ai_gateway_request_fast_exhausted_total{service="juhe-ai"} 1`,
		`juhe_ai_gateway_request_negative_cache_hits_total{service="juhe-ai"} 2`,
		`juhe_ai_gateway_request_negative_cache_capacity_rejected_total{service="juhe-ai"} 1`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("exposition missing %q", fragment)
		}
	}
}

// mappedUsageModelResolver 是 ResolveUpstreamModel 的解析夹具：命中映射账户
// 时改写上游模型，空解析回落请求模型。
type mappedUsageModelResolver struct{}

func (mappedUsageModelResolver) ResolveUsageModel(account UsageModelAccount, requestedModel string, _ string) UsageModelResolution {
	if account.ID == "acc_mapped" {
		return UsageModelResolution{UpstreamModel: requestedModel + "-8k"}
	}
	if account.ID == "acc_empty" {
		return UsageModelResolution{UpstreamModel: ""}
	}
	return UsageModelResolution{UpstreamModel: requestedModel}
}

// TestServiceResolveUpstreamModel：导出的上游模型解析与 usage 写库同口径——
// 解析值优先、空解析回落请求模型。
func TestServiceResolveUpstreamModel(t *testing.T) {
	service := NewService(nil, ServiceConfig{}).WithModelResolver(mappedUsageModelResolver{})
	if got := service.ResolveUpstreamModel(UsageModelAccount{ID: "acc_mapped"}, "gpt-test", "chat_completions"); got != "gpt-test-8k" {
		t.Fatalf("mapped resolution = %q want gpt-test-8k", got)
	}
	if got := service.ResolveUpstreamModel(UsageModelAccount{ID: "acc_empty"}, "gpt-test", "chat_completions"); got != "gpt-test" {
		t.Fatalf("empty resolution must fall back to the requested model: %q", got)
	}
	if got := service.ResolveUpstreamModel(UsageModelAccount{ID: "acc_direct"}, "gpt-test", "chat_completions"); got != "gpt-test" {
		t.Fatalf("direct resolution = %q want gpt-test", got)
	}
	if got := NewService(nil, ServiceConfig{}).ResolveUpstreamModel(UsageModelAccount{ID: "acc_any"}, "gpt-test", ""); got != "gpt-test" {
		t.Fatalf("resolver-less service must fall back to the requested model: %q", got)
	}
}

// TestClientRequestBodySha256Accessor：客户端请求体摘要访问器读预计算值，
// 未捕获时返回空串（调用方按缺失自算）。
func TestClientRequestBodySha256Accessor(t *testing.T) {
	context := &AuditCaptureContext{enabled: true, activeCaptureMaxBytes: 1 << 20}
	if got := context.ClientRequestBodySha256(); got != "" {
		t.Fatalf("empty capture must return an empty sha: %q", got)
	}
	context.addPayload(AuditLogPayloadInput{PartType: AuditPartClientRequest, BodySha256: "sha-client-body"})
	if got := context.ClientRequestBodySha256(); got != "sha-client-body" {
		t.Fatalf("accessor = %q want the captured client request sha", got)
	}
}

// TestBodySha256HexMatchesCryptoSha256：L2 自算 body 摘要与标准库 SHA-256
// 逐字节一致（与审计摘要同款实现）。
func TestBodySha256HexMatchesCryptoSha256(t *testing.T) {
	body := []byte(`{"model":"gpt-test","messages":[]}`)
	digest := sha256.Sum256(body)
	if got, want := BodySha256Hex(body), hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("BodySha256Hex = %q want %q", got, want)
	}
	if got := BodySha256Hex(nil); got == "" {
		t.Fatal("empty body must still hash to the sha256 of empty input")
	}
}
