package main

// 完成尝试 usage 行三成本字段（costUsd / cacheReadCostUsd /
// cacheWriteCostUsd）的同步定价估算验证（对齐 Node recordCompletedUpstreamAttempt
// 记录时同步估算语义）：
//
//   - gate 开（syncPricingAllowed + pricing 非 nil）且目录命中 → 三字段落
//     mock 返回值，估算输入（ProviderCode / SystemAccountID / Model /
//     ServiceTier / token 指针）按契约取法；
//   - gate 关 → 不触碰目录，三字段 NULL；
//   - 目录未命中（估算器返回 nil）→ 三字段 NULL，不写 0；
//   - 成本估算与显式 CompletedAtMs 的 DurationMs 口径互不干扰（回归总耗时
//     兜底契约）。

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// fakeChainUsagePricingCatalog 是测试用 gatewayusage.PricingCatalog mock：
// 按固定值应答三个估算方法并记录收到的 PricingCostInput，供断言估算输入取法。
type fakeChainUsagePricingCatalog struct {
	cost           *float64
	cacheReadCost  *float64
	cacheWriteCost *float64
	// inputs 记录 EstimateCost 收到的输入（三个方法入参同构，记录一份即可）。
	inputs []gatewayusage.PricingCostInput
}

func (f *fakeChainUsagePricingCatalog) ResolvePricingModel(providerCode, systemAccountID, model string) string {
	return ""
}

func (f *fakeChainUsagePricingCatalog) EstimateCost(input gatewayusage.PricingCostInput) *float64 {
	f.inputs = append(f.inputs, input)
	return f.cost
}

func (f *fakeChainUsagePricingCatalog) EstimateCacheReadCost(input gatewayusage.PricingCostInput) *float64 {
	return f.cacheReadCost
}

func (f *fakeChainUsagePricingCatalog) EstimateCacheWriteCost(input gatewayusage.PricingCostInput) *float64 {
	return f.cacheWriteCost
}

func floatPtrOf(value float64) *float64 { return &value }

// intPtrOf 复用 chain_wiring_w2c_test.go 的同名包内 helper。

// completedAttemptCostInput 构造带完整 usage 证据的完成尝试输入。
func completedAttemptCostInput() gatewayresponse.CompletedAttemptInput {
	return gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:         "trace_cost_1",
			SystemAccountID: "ctx_sys_acc",
			ProviderCode:    "openai",
			Endpoint:        "/v1/chat/completions",
		},
		Account: gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
			ID:                          "acc_1",
			AccountOwnerSystemAccountID: "owner_sys_acc",
		}},
		Success:        true,
		StatusCode:     200,
		Stream:         true,
		StartedAtMs:    time.Now().UnixMilli() - 1_000,
		RequestedModel: "gpt-request-alias",
		Usage: gatewayproto.ParsedUsage{
			UpstreamResponseModel: "gpt-upstream-real",
			ServiceTier:           "priority",
			InputTokens:           intPtrOf(100),
			OutputTokens:          intPtrOf(50),
			CacheReadTokens:       intPtrOf(10),
			CacheWriteTokens:      intPtrOf(20),
		},
	}
}

// TestChainFinalizationUsageCostEstimatedWhenGateOpen：(a) gate 开 + 目录命中
// → 三成本字段落 mock 返回值；估算输入的 SystemAccountID 取账号视图 owner、
// Model 取计价目录模型（pricingModel ?? upstreamModel ?? requestedModel；未
// 映射时即请求模型；upstream_response_model 是响应观察事实，不参与计价）、
// ServiceTier 与 token 指针全量透传。
func TestChainFinalizationUsageCostEstimatedWhenGateOpen(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{
		cost:           floatPtrOf(0.0123),
		cacheReadCost:  floatPtrOf(0.0001),
		cacheWriteCost: floatPtrOf(0.0002),
	}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	usage.RecordCompletedUpstreamAttempt(completedAttemptCostInput())

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.CostUsd == nil || *record.CostUsd != 0.0123 {
		t.Fatalf("CostUsd = %v, want 0.0123", record.CostUsd)
	}
	if record.CacheReadCostUsd == nil || *record.CacheReadCostUsd != 0.0001 {
		t.Fatalf("CacheReadCostUsd = %v, want 0.0001", record.CacheReadCostUsd)
	}
	if record.CacheWriteCostUsd == nil || *record.CacheWriteCostUsd != 0.0002 {
		t.Fatalf("CacheWriteCostUsd = %v, want 0.0002", record.CacheWriteCostUsd)
	}

	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	input := catalog.inputs[0]
	if input.ProviderCode != "openai" {
		t.Errorf("ProviderCode = %q, want openai", input.ProviderCode)
	}
	if input.SystemAccountID != "owner_sys_acc" {
		t.Errorf("SystemAccountID = %q, want owner_sys_acc（账号视图 owner 优先）", input.SystemAccountID)
	}
	if input.Model != "gpt-request-alias" {
		t.Errorf("Model = %q, want gpt-request-alias（未映射时计价键取请求模型；上游响应观察模型不参与计价）", input.Model)
	}
	if input.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", input.ServiceTier)
	}
	if input.InputTokens == nil || *input.InputTokens != 100 ||
		input.OutputTokens == nil || *input.OutputTokens != 50 ||
		input.CacheReadTokens == nil || *input.CacheReadTokens != 10 ||
		input.CacheWriteTokens == nil || *input.CacheWriteTokens != 20 {
		t.Fatalf("token 指针未按 usage 透传: %+v", input)
	}
	if input.ThinkingTokens != nil || input.InputImageTokens != nil || input.OutputImageTokens != nil ||
		input.InputAudioTokens != nil || input.OutputAudioTokens != nil || input.OutputImageCount != nil {
		t.Fatalf("缺失维度必须保持 nil: %+v", input)
	}
}

// TestChainFinalizationUsageCostModelFallbackToRequestedModel：未映射时计价键
// 恒取请求模型；上游响应模型为空与否都不改变计价键（Node
// pricingModel ?? upstreamModel ?? requestedModel）。
func TestChainFinalizationUsageCostModelFallbackToRequestedModel(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.5)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	input := completedAttemptCostInput()
	input.Usage.UpstreamResponseModel = ""
	usage.RecordCompletedUpstreamAttempt(input)

	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].Model != "gpt-request-alias" {
		t.Fatalf("Model = %q, want gpt-request-alias（回落请求模型）", catalog.inputs[0].Model)
	}
}

// TestChainFinalizationUsageCostSystemAccountFallbackToContext：账号视图缺
// owner 系统账号（或非 OpenAIAccountView 实现）时回退 usage context 的
// SystemAccountID（对齐 audit_capture.go catalogSystemAccountID 取法）。
func TestChainFinalizationUsageCostSystemAccountFallbackToContext(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.5)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	input := completedAttemptCostInput()
	input.Account = gatewayresponse.OpenAIAccountView{}
	usage.RecordCompletedUpstreamAttempt(input)

	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].SystemAccountID != "ctx_sys_acc" {
		t.Fatalf("SystemAccountID = %q, want ctx_sys_acc（回落 usage context）", catalog.inputs[0].SystemAccountID)
	}
}

// TestChainFinalizationUsageCostNullWhenGateClosed：(b) gate 关 → 不触碰目录，
// 三成本字段保持 NULL；gate 开但 catalog 为 nil（组合测试形态）同样保持 NULL。
func TestChainFinalizationUsageCostNullWhenGateClosed(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.9)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: false}
	usage.RecordCompletedUpstreamAttempt(completedAttemptCostInput())

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	if len(catalog.inputs) != 0 {
		t.Fatalf("gate 关闭时不得触碰定价目录，实际调用 %d 次", len(catalog.inputs))
	}
	record := recorder.records[0]
	if record.CostUsd != nil || record.CacheReadCostUsd != nil || record.CacheWriteCostUsd != nil {
		t.Fatalf("gate 关闭时三成本字段必须为 NULL: %+v", record)
	}

	// gate 开但 catalog 缺席（零值链/组合测试）：同样不估算。
	nilCatalogRecorder := &capturingUsageRecorder{}
	bare := chainFinalizationUsage{recorder: nilCatalogRecorder, syncPricingAllowed: true}
	bare.RecordCompletedUpstreamAttempt(completedAttemptCostInput())
	if len(nilCatalogRecorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(nilCatalogRecorder.records))
	}
	bareRecord := nilCatalogRecorder.records[0]
	if bareRecord.CostUsd != nil || bareRecord.CacheReadCostUsd != nil || bareRecord.CacheWriteCostUsd != nil {
		t.Fatalf("catalog 为 nil 时三成本字段必须为 NULL: %+v", bareRecord)
	}
}

// TestChainFinalizationUsageCostNullOnCatalogMiss：(c) 目录未命中（估算器返
// 回 nil）→ 三字段保持 NULL，不写 0。
func TestChainFinalizationUsageCostNullOnCatalogMiss(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	usage.RecordCompletedUpstreamAttempt(completedAttemptCostInput())

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.CostUsd != nil || record.CacheReadCostUsd != nil || record.CacheWriteCostUsd != nil {
		t.Fatalf("目录未命中时三成本字段必须保持 NULL（不写 0）: %+v", record)
	}
}

// TestChainFinalizationUsageCostDoesNotDisturbDurationMs：(d) 成本估算与
// CompletedAtMs / DurationMs 口径互不干扰——gate 开 + 成本落值时，显式
// CompletedAtMs 仍精确透传（completedAt - startedAt）。
func TestChainFinalizationUsageCostDoesNotDisturbDurationMs(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{
		cost:           floatPtrOf(0.01),
		cacheReadCost:  floatPtrOf(0.001),
		cacheWriteCost: floatPtrOf(0.002),
	}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	startedAtMs := time.Now().UnixMilli() - 1_000
	completedAtMs := startedAtMs + 500
	input := completedAttemptCostInput()
	input.StartedAtMs = startedAtMs
	input.CompletedAtMs = &completedAtMs
	usage.RecordCompletedUpstreamAttempt(input)

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.DurationMs == nil || *record.DurationMs != 500 {
		t.Fatalf("DurationMs = %v, want 500（显式 CompletedAtMs 口径不受成本估算影响）", record.DurationMs)
	}
	if record.CostUsd == nil || *record.CostUsd != 0.01 {
		t.Fatalf("CostUsd = %v, want 0.01", record.CostUsd)
	}
	if record.CacheReadCostUsd == nil || *record.CacheReadCostUsd != 0.001 {
		t.Fatalf("CacheReadCostUsd = %v, want 0.001", record.CacheReadCostUsd)
	}
	if record.CacheWriteCostUsd == nil || *record.CacheWriteCostUsd != 0.002 {
		t.Fatalf("CacheWriteCostUsd = %v, want 0.002", record.CacheWriteCostUsd)
	}
}

// TestChainFinalizationUsageCostSkippedWithoutPricingModel：定价模型名为空
// （上游响应模型与请求模型均缺失）时不调用目录、三字段保持 NULL。
func TestChainFinalizationUsageCostSkippedWithoutPricingModel(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.7)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	input := completedAttemptCostInput()
	input.Usage.UpstreamResponseModel = ""
	input.RequestedModel = ""
	usage.RecordCompletedUpstreamAttempt(input)

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	if len(catalog.inputs) != 0 {
		t.Fatalf("定价模型名为空时不得调用目录，实际调用 %d 次", len(catalog.inputs))
	}
	record := recorder.records[0]
	if record.CostUsd != nil || record.CacheReadCostUsd != nil || record.CacheWriteCostUsd != nil {
		t.Fatalf("定价模型名为空时三成本字段必须为 NULL: %+v", record)
	}
}
