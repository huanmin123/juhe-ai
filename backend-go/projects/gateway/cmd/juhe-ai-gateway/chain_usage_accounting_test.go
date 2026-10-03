package main

// finalize 侧账号模型映射记账回归（2026-10-03 映射收口）：
//
//   - 完成尝试记账必须做与 Node accountUsageModelAccounting 等价的解析：
//     UpstreamModel / PricingModel / ModelMappingApplied / ModelMappingSource /
//     SourceEndpointFamily / UpstreamEndpointFamily 六字段落库（此前全空，
//     生产 142,566 行 model_mapping_applied 全为 0）；
//   - 计价键对齐 Node usageCostCatalogModel：pricingModel ?? upstreamModel ??
//     requestedModel，upstream_response_model 不参与计价；
//   - 失败记账调用点（engine 适配器与三个失败分支）必须携带
//     SourceEndpointFamily，否则服务层映射记账恒不命中；
//   - 审计捕获必须携带 SourceEndpointFamily，审计 attempt 行同样命中映射；
//   - DurationMs 负值夹取为 0（Node Math.max(0, ...)）。
//
// 断言均走真实测试替身：gatewayusage.MemoryUsageRecorder（完成路径）与
// 真实 gatewayusage.Service/FinalizationDispatch（失败路径），不使用生产接口
// 之外的自造形状。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// accountingPricingCatalog 是定价目录替身：记录 ResolvePricingModel 与
// EstimateCost 的入参，供计价键断言。
type accountingPricingCatalog struct {
	// resolvedPricingModel 是 ResolvePricingModel 的应答（空串表示目录未给出
	// 规范计价名）。
	resolvedPricingModel string
	// cost 是 EstimateCost 的应答（nil 表示目录未命中）。
	cost *float64
	// resolveModels 记录 ResolvePricingModel 收到的模型名。
	resolveModels []string
	// inputs 记录 EstimateCost 收到的估算入参。
	inputs []gatewayusage.PricingCostInput
}

func (c *accountingPricingCatalog) ResolvePricingModel(providerCode, systemAccountID, model string) string {
	c.resolveModels = append(c.resolveModels, model)
	return c.resolvedPricingModel
}

func (c *accountingPricingCatalog) EstimateCost(input gatewayusage.PricingCostInput) *float64 {
	c.inputs = append(c.inputs, input)
	return c.cost
}

func (c *accountingPricingCatalog) EstimateCacheReadCost(gatewayusage.PricingCostInput) *float64 {
	return nil
}

func (c *accountingPricingCatalog) EstimateCacheWriteCost(gatewayusage.PricingCostInput) *float64 {
	return nil
}

// accountingMappedSecret 构造带启用的 chat_completions 映射的账号：
// glm-5.3-flash -> deepseek-v4.1-flash（源/目标同族，映射转换支持判定直接
// 通过；provider 身份字段齐备供 providerCode/profileId 断言）。
func accountingMappedSecret() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                          "acc-accounting",
		Name:                        "accounting-account",
		ProviderCode:                "openai",
		ProviderProtocolProfileID:   "openai-default",
		ProtocolCode:                gatewayopenai.ProtocolCode,
		ProtocolVersion:             gatewayopenai.ProtocolVersion,
		AccountOwnerSystemAccountID: "owner-sys",
		AccountAccessType:           gatewayusage.AccountAccessTypeOwner,
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "glm-5.3-flash",
			SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
			UpstreamModel:          "deepseek-v4.1-flash",
			UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
			Enabled:                true,
		}},
	}
}

// accountingCompletedInput 构造映射命中场景的完成尝试输入。
func accountingCompletedInput() gatewayresponse.CompletedAttemptInput {
	return gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:                  "trace-accounting-hit",
			SystemAccountID:          "ctx-sys",
			ProviderCode:             "ctx-provider",
			RequestedServiceTier:     "flex",
			EffectiveServiceTier:     "priority",
			RequestedReasoningEffort: "low",
			EffectiveReasoningEffort: "high",
		},
		Account:        gatewayresponse.OpenAIAccountView{Account: accountingMappedSecret()},
		Success:        true,
		StatusCode:     200,
		RequestedModel: "glm-5.3-flash",
		Usage:          gatewayproto.ParsedUsage{UpstreamResponseModel: "remote-observed-model"},
	}
}

// TestChainFinalizationUsageAccountMappingAccountingHit 覆盖映射命中时完成
// 记录的六字段、provider 身份、语义与层级/档位透传。
func TestChainFinalizationUsageAccountMappingAccountingHit(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	catalog := &accountingPricingCatalog{}
	usage := chainFinalizationUsage{
		recorder:             recorder,
		pricing:              catalog,
		syncPricingAllowed:   true,
		requestedModel:       "glm-5.3-flash",
		sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
		models:               usageModelResolverAdapter{},
	}
	usage.RecordCompletedUpstreamAttempt(accountingCompletedInput())

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Model != "glm-5.3-flash" {
		t.Errorf("Model = %q, want glm-5.3-flash", record.Model)
	}
	if record.UpstreamModel != "deepseek-v4.1-flash" {
		t.Errorf("UpstreamModel = %q, want deepseek-v4.1-flash（映射目标）", record.UpstreamModel)
	}
	if record.ModelMappingApplied == nil || !*record.ModelMappingApplied {
		t.Errorf("ModelMappingApplied = %v, want true", record.ModelMappingApplied)
	}
	if record.ModelMappingSource != "account" {
		t.Errorf("ModelMappingSource = %q, want account", record.ModelMappingSource)
	}
	if record.SourceEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Errorf("SourceEndpointFamily = %q, want chat_completions", record.SourceEndpointFamily)
	}
	if record.UpstreamEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Errorf("UpstreamEndpointFamily = %q, want chat_completions", record.UpstreamEndpointFamily)
	}
	if record.UsageSemantic != "openai" {
		t.Errorf("UsageSemantic = %q, want openai（账号 openai 档案）", record.UsageSemantic)
	}
	if record.ProviderCode != "openai" {
		t.Errorf("ProviderCode = %q, want openai（账号视图优先于 context 的 ctx-provider）", record.ProviderCode)
	}
	if record.ProviderProtocolProfileID != "openai-default" {
		t.Errorf("ProviderProtocolProfileID = %q, want openai-default", record.ProviderProtocolProfileID)
	}
	if record.UpstreamResponseModel != "remote-observed-model" {
		t.Errorf("UpstreamResponseModel = %q, want remote-observed-model（观察事实原样落库）", record.UpstreamResponseModel)
	}
	if record.RequestedServiceTier != "flex" || record.EffectiveServiceTier != "priority" ||
		record.RequestedReasoningEffort != "low" || record.EffectiveReasoningEffort != "high" {
		t.Errorf("tier/effort 透传不符: requested=%q effective=%q reqEffort=%q effEffort=%q",
			record.RequestedServiceTier, record.EffectiveServiceTier,
			record.RequestedReasoningEffort, record.EffectiveReasoningEffort)
	}
	if record.FailureAttribution != "" {
		t.Errorf("成功尝试 FailureAttribution = %q, want empty", record.FailureAttribution)
	}
	if record.DurationMs != nil && *record.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", *record.DurationMs)
	}
}

// TestChainFinalizationUsagePricingKeyIgnoresUpstreamResponseModel 是计价键
// 修复前必红的守卫：请求模型与上游响应观察模型不一致且无映射时，计价目录
// 收到的模型必须是请求模型（Node pricingModel ?? upstreamModel ??
// requestedModel），绝不取 upstream_response_model。
func TestChainFinalizationUsagePricingKeyIgnoresUpstreamResponseModel(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	catalog := &accountingPricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	input := gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:      "trace-pricing-key",
			ProviderCode: "openai",
		},
		Account: gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
			ID: "acc-pricing", ProviderCode: "openai",
		}},
		Success:        true,
		StatusCode:     200,
		RequestedModel: "glm-5.3-flash",
		Usage:          gatewayproto.ParsedUsage{UpstreamResponseModel: "zzz-upstream-observation"},
	}
	usage.RecordCompletedUpstreamAttempt(input)

	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].Model != "glm-5.3-flash" {
		t.Fatalf("计价键 = %q, want glm-5.3-flash（upstream_response_model 不得参与计价）", catalog.inputs[0].Model)
	}
}

// TestChainFinalizationUsagePricingKeyUsesMappingTarget 覆盖映射命中时计价键
// 取映射目标，且 upstreamResponseModel 与映射目标不同也不影响；同时钉住
// pricingModel 优先于 upstreamModel 的 Node 解析顺序。
func TestChainFinalizationUsagePricingKeyUsesMappingTarget(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	catalog := &accountingPricingCatalog{}
	usage := chainFinalizationUsage{
		recorder:             recorder,
		pricing:              catalog,
		syncPricingAllowed:   true,
		requestedModel:       "glm-5.3-flash",
		sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
		models:               usageModelResolverAdapter{},
	}
	input := accountingCompletedInput()
	input.Usage.UpstreamResponseModel = "unrelated-observational-name"
	usage.RecordCompletedUpstreamAttempt(input)

	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].Model != "deepseek-v4.1-flash" {
		t.Fatalf("计价键 = %q, want deepseek-v4.1-flash（映射目标，不受 upstreamResponseModel 影响）", catalog.inputs[0].Model)
	}
	if catalog.inputs[0].ProviderCode != "openai" {
		t.Fatalf("估算 ProviderCode = %q, want openai（账号视图）", catalog.inputs[0].ProviderCode)
	}
	if catalog.inputs[0].SystemAccountID != "owner-sys" {
		t.Fatalf("估算 SystemAccountID = %q, want owner-sys（账号 owner 优先）", catalog.inputs[0].SystemAccountID)
	}

	// pricingModel 优先：目录给出规范计价名时计价键取该名。
	records := recorder.Records()
	if len(records) != 1 || records[0].PricingModel != "" {
		t.Fatalf("目录未给出计价名时 PricingModel 必须保持空（NULL），record = %+v", records)
	}
	recorder2 := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	catalog2 := &accountingPricingCatalog{resolvedPricingModel: "deepseek-v4.1-flash-priced"}
	usage2 := chainFinalizationUsage{
		recorder:             recorder2,
		pricing:              catalog2,
		syncPricingAllowed:   true,
		sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
		models:               usageModelResolverAdapter{},
	}
	usage2.RecordCompletedUpstreamAttempt(accountingCompletedInput())
	if len(catalog2.inputs) != 1 || catalog2.inputs[0].Model != "deepseek-v4.1-flash-priced" {
		t.Fatalf("计价键 = %+v, want deepseek-v4.1-flash-priced（pricingModel 优先）", catalog2.inputs)
	}
	if got := recorder2.Records(); len(got) != 1 || got[0].PricingModel != "deepseek-v4.1-flash-priced" {
		t.Fatalf("PricingModel 字段 = %+v, want deepseek-v4.1-flash-priced", got)
	}
	if len(catalog2.resolveModels) != 1 || catalog2.resolveModels[0] != "deepseek-v4.1-flash" {
		t.Fatalf("ResolvePricingModel 入参 = %v, want [deepseek-v4.1-flash]（解析输入用映射目标）", catalog2.resolveModels)
	}
}

// TestChainFinalizationUsageAccountMappingAccountingMissFallsBack 覆盖未命中：
// 上游模型回落请求模型、applied=false、两端 family 仍写注入值。
func TestChainFinalizationUsageAccountMappingAccountingMissFallsBack(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{
		recorder:             recorder,
		requestedModel:       "glm-5.3-flash",
		sourceEndpointFamily: gatewayopenai.FamilyResponses,
		models:               usageModelResolverAdapter{},
	}
	input := accountingCompletedInput()
	// 账号无映射行：解析必须退化为请求模型原样透传。
	secret := accountingMappedSecret()
	secret.ModelMappings = nil
	input.Account = gatewayresponse.OpenAIAccountView{Account: secret}
	input.UsageContext.RequestedServiceTier = ""
	input.RequestedModel = "glm-5.3-flash"
	usage.RecordCompletedUpstreamAttempt(input)

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.UpstreamModel != "glm-5.3-flash" {
		t.Errorf("UpstreamModel = %q, want glm-5.3-flash（未命中回落请求模型）", record.UpstreamModel)
	}
	if record.ModelMappingApplied == nil || *record.ModelMappingApplied {
		t.Errorf("ModelMappingApplied = %v, want false", record.ModelMappingApplied)
	}
	if record.ModelMappingSource != "" {
		t.Errorf("ModelMappingSource = %q, want empty", record.ModelMappingSource)
	}
	if record.SourceEndpointFamily != gatewayopenai.FamilyResponses ||
		record.UpstreamEndpointFamily != gatewayopenai.FamilyResponses {
		t.Errorf("family = %q/%q, want responses/responses（未命中仍写注入 family）",
			record.SourceEndpointFamily, record.UpstreamEndpointFamily)
	}
}

// TestChainFinalizationUsageCompletedFailureAttribution 覆盖完成路径的失败
// 归因：Success=false 缺省 account_upstream，显式归因原样保留。
func TestChainFinalizationUsageCompletedFailureAttribution(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder}

	defaulted := accountingCompletedInput()
	defaulted.Success = false
	defaulted.UsageContext.TraceID = "trace-attr-default"
	usage.RecordCompletedUpstreamAttempt(defaulted)

	explicit := accountingCompletedInput()
	explicit.Success = false
	explicit.FailureAttribution = gatewayusage.FailureAttributionOpaqueUpstream
	explicit.UsageContext.TraceID = "trace-attr-explicit"
	usage.RecordCompletedUpstreamAttempt(explicit)

	records := recorder.Records()
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].FailureAttribution != gatewayusage.FailureAttributionAccountUpstream {
		t.Errorf("缺省归因 = %q, want account_upstream", records[0].FailureAttribution)
	}
	if records[1].FailureAttribution != gatewayusage.FailureAttributionOpaqueUpstream {
		t.Errorf("显式归因 = %q, want opaque_upstream", records[1].FailureAttribution)
	}
}

// TestChainFinalizationUsageDurationClampsNegative 覆盖 Node
// Math.max(0, completedAt - startedAt)：时钟回拨产生的负总耗时夹取为 0。
func TestChainFinalizationUsageDurationClampsNegative(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder}
	startedAtMs := int64(2_000)
	completedAtMs := int64(1_500)
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext:  gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace-negative-duration"},
		Success:       true,
		StatusCode:    200,
		StartedAtMs:   startedAtMs,
		CompletedAtMs: &completedAtMs,
	})
	records := recorder.Records()
	if len(records) != 1 || records[0].DurationMs == nil {
		t.Fatalf("records = %+v, want 1 条带 DurationMs", records)
	}
	if *records[0].DurationMs != 0 {
		t.Fatalf("DurationMs = %d, want 0（负值夹取）", *records[0].DurationMs)
	}
}

// TestChainFinalizationUsageFailedAttemptMappingAccounting 覆盖
// chainFinalizationUsage 失败路径：请求模型取构造点注入值、六字段与成功
// 路径同源。
func TestChainFinalizationUsageFailedAttemptMappingAccounting(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{
		recorder:             recorder,
		requestedModel:       "glm-5.3-flash",
		sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
		models:               usageModelResolverAdapter{},
	}
	statusCode := 502
	usage.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:      "trace-failed-accounting",
			ProviderCode: "openai",
		},
		Account:      gatewayresponse.OpenAIAccountView{Account: accountingMappedSecret()},
		StatusCode:   &statusCode,
		ErrorMessage: "上游 502",
	})
	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Model != "glm-5.3-flash" {
		t.Errorf("Model = %q, want glm-5.3-flash（注入请求模型）", record.Model)
	}
	if record.UpstreamModel != "deepseek-v4.1-flash" {
		t.Errorf("UpstreamModel = %q, want deepseek-v4.1-flash", record.UpstreamModel)
	}
	if record.ModelMappingApplied == nil || !*record.ModelMappingApplied {
		t.Errorf("ModelMappingApplied = %v, want true", record.ModelMappingApplied)
	}
	if record.ModelMappingSource != "account" {
		t.Errorf("ModelMappingSource = %q, want account", record.ModelMappingSource)
	}
	if record.SourceEndpointFamily != gatewayopenai.FamilyChatCompletions ||
		record.UpstreamEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Errorf("family = %q/%q, want chat_completions/chat_completions",
			record.SourceEndpointFamily, record.UpstreamEndpointFamily)
	}
}

// TestChainFinalizationUsageCostTierUsesBilledFallback：成本估算档位对齐
// Node resolveUsageServiceTiers 的 billedServiceTier（reported ?? effective ??
// requested）：reported 缺失时用 effective，再缺用 requested；jobs 冻结侧
// serviceTierForWrite 同序取 billed，两侧档位价一致。
func TestChainFinalizationUsageCostTierUsesBilledFallback(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	catalog := &accountingPricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}

	effectiveOnly := accountingCompletedInput()
	effectiveOnly.Usage.ServiceTier = ""
	effectiveOnly.UsageContext.EffectiveServiceTier = "priority"
	effectiveOnly.UsageContext.RequestedServiceTier = "flex"
	effectiveOnly.Usage.InputTokens = intPtrOf(10)
	usage.RecordCompletedUpstreamAttempt(effectiveOnly)

	requestedOnly := accountingCompletedInput()
	requestedOnly.Usage.ServiceTier = ""
	requestedOnly.UsageContext.EffectiveServiceTier = ""
	requestedOnly.UsageContext.RequestedServiceTier = "flex"
	requestedOnly.Usage.InputTokens = intPtrOf(10)
	usage.RecordCompletedUpstreamAttempt(requestedOnly)

	reportedWins := accountingCompletedInput()
	reportedWins.Usage.ServiceTier = "reported-tier"
	reportedWins.Usage.InputTokens = intPtrOf(10)
	usage.RecordCompletedUpstreamAttempt(reportedWins)

	if len(catalog.inputs) != 3 {
		t.Fatalf("估算调用次数 = %d, want 3", len(catalog.inputs))
	}
	if catalog.inputs[0].ServiceTier != "priority" {
		t.Errorf("reported 缺失、effective=priority 时 ServiceTier = %q, want priority", catalog.inputs[0].ServiceTier)
	}
	if catalog.inputs[1].ServiceTier != "flex" {
		t.Errorf("effective 也缺失时 ServiceTier = %q, want flex（回落 requested）", catalog.inputs[1].ServiceTier)
	}
	if catalog.inputs[2].ServiceTier != "reported-tier" {
		t.Errorf("reported 存在时 ServiceTier = %q, want reported-tier", catalog.inputs[2].ServiceTier)
	}
}

// TestUsageModelResolverRequiresSourceEndpointFamily 钉住"必须传 family"：
// 带 chat_completions 时适配器命中映射，空 family 退化为未映射。
func TestUsageModelResolverRequiresSourceEndpointFamily(t *testing.T) {
	resolver := usageModelResolverAdapter{}
	account := usageModelAccountOf(gatewaydispatch.AccountCandidate{
		ID:                        "acc-adapter-family",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "openai-default",
		ProtocolCode:              gatewayopenai.ProtocolCode,
		ProtocolVersion:           gatewayopenai.ProtocolVersion,
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "glm-5.3-flash",
			SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
			UpstreamModel:          "deepseek-v4.1-flash",
			UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
			Enabled:                true,
		}},
	})
	hit := resolver.ResolveUsageModel(account, "glm-5.3-flash", gatewayopenai.FamilyChatCompletions)
	if !hit.ModelMappingApplied || hit.UpstreamModel != "deepseek-v4.1-flash" {
		t.Fatalf("带 family 解析 = %+v, want 命中 deepseek-v4.1-flash", hit)
	}
	miss := resolver.ResolveUsageModel(account, "glm-5.3-flash", "")
	if miss.ModelMappingApplied || miss.UpstreamModel != "glm-5.3-flash" {
		t.Fatalf("空 family 解析 = %+v, want 未映射回落请求模型", miss)
	}
}

// TestUsageAttemptRecorderAdapterFailedAttemptCarriesSourceEndpointFamily 是
// engine 失败调用点修复前必红的守卫：适配器必须把请求侧 family 传进
// RecordFailedUpstreamAttemptInput，服务层映射记账才能命中。
func TestUsageAttemptRecorderAdapterFailedAttemptCarriesSourceEndpointFamily(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, nil, 64, 1)
	catalog := &accountingPricingCatalog{}
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithModelResolver(usageModelResolverAdapter{}).
		WithPricingCatalog(catalog)
	adapter := usageAttemptRecorderAdapter{service: service}

	req := modelStreamRequestBody(t, `{"model":"glm-5.3-flash"}`)
	err := adapter.RecordFailedUpstreamAttempt(context.Background(), req,
		gatewaypreauth.GatewayFailureUsageContext{
			TraceID:      "trace-adapter-family",
			ProviderCode: "openai",
		},
		gatewaydispatch.AccountCandidate{
			ID:                        "acc-adapter-family",
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "openai-default",
			ProtocolCode:              gatewayopenai.ProtocolCode,
			ProtocolVersion:           gatewayopenai.ProtocolVersion,
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "glm-5.3-flash",
				SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
				UpstreamModel:          "deepseek-v4.1-flash",
				UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
				Enabled:                true,
			}},
		},
		gatewaydispatch.FailedAttemptRecord{
			UpstreamURL:  "https://upstream.example/v1/chat/completions",
			StartedAt:    1728000000000,
			ErrorMessage: "上游 502",
		})
	if err != nil {
		t.Fatalf("record = %v", err)
	}
	waitForUsageDispatch(t, dispatch)
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "glm-5.3-flash" {
		t.Errorf("Model = %q, want glm-5.3-flash", record.Model)
	}
	if record.UpstreamModel != "deepseek-v4.1-flash" {
		t.Errorf("UpstreamModel = %q, want deepseek-v4.1-flash（family 补齐后命中映射）", record.UpstreamModel)
	}
	if record.ModelMappingApplied == nil || !*record.ModelMappingApplied {
		t.Errorf("ModelMappingApplied = %v, want true", record.ModelMappingApplied)
	}
	if record.ModelMappingSource != "account" {
		t.Errorf("ModelMappingSource = %q, want account", record.ModelMappingSource)
	}
	if record.SourceEndpointFamily != gatewayopenai.FamilyChatCompletions ||
		record.UpstreamEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Errorf("family = %q/%q, want chat_completions/chat_completions",
			record.SourceEndpointFamily, record.UpstreamEndpointFamily)
	}
}

// TestNewAuditCaptureCarriesSourceEndpointFamilyForModelAccounting 是审计
// 捕获修复前必红的守卫：newAuditCapture 必须把请求侧 family 注入
// AuditCaptureInput，审计 attempt 行的模型映射记账才能在记账时命中。
func TestNewAuditCaptureCarriesSourceEndpointFamilyForModelAccounting(t *testing.T) {
	req := mergeRouteRequest("glm-5.3-flash")
	dispatcher := &mergeCapturingAuditDispatcher{}
	chain := &gatewayChain{
		auditSettings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{
			Enabled:           true,
			SuccessSampleRate: 1,
		}},
		auditDispatcher:    dispatcher,
		usageModelResolver: usageModelResolverAdapter{},
	}
	capture := chain.newAuditCapture(req, "trace_audit_accounting", 1728000000000)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })
	concrete := auditCaptureConcrete(capture)
	if concrete == nil {
		t.Fatal("newAuditCapture 必须产出具体 G17 capture")
	}
	if !concrete.IsEnabled() {
		t.Fatal("审计设置 Enabled=true 时 capture 必须启用")
	}

	concrete.StartAttempt(gatewayusage.StartAttemptInput{
		Account: usageModelAccountOf(gatewaydispatch.AccountCandidate{
			ID:                        "acc-audit-family",
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "openai-default",
			ProtocolCode:              gatewayopenai.ProtocolCode,
			ProtocolVersion:           gatewayopenai.ProtocolVersion,
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "glm-5.3-flash",
				SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
				UpstreamModel:          "deepseek-v4.1-flash",
				UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
				Enabled:                true,
			}},
		}),
		AttemptIndex: 0,
		UpstreamURL:  "https://upstream.example/v1/chat/completions",
		Method:       "POST",
	})
	statusCode := 200
	concrete.Finalize(gatewayusage.FinalizeAuditInput{Success: true, StatusCode: &statusCode})

	logs := dispatcher.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计派发日志 = %d, want 1", len(logs))
	}
	if len(logs[0].Attempts) != 1 {
		t.Fatalf("审计 attempt 行 = %d, want 1", len(logs[0].Attempts))
	}
	attempt := logs[0].Attempts[0]
	if attempt.UpstreamModel != "deepseek-v4.1-flash" {
		t.Errorf("attempt UpstreamModel = %q, want deepseek-v4.1-flash（family 注入后命中映射）", attempt.UpstreamModel)
	}
	if attempt.ModelMappingApplied == nil || !*attempt.ModelMappingApplied {
		t.Errorf("attempt ModelMappingApplied = %v, want true", attempt.ModelMappingApplied)
	}
	if attempt.ModelMappingSource != "account" {
		t.Errorf("attempt ModelMappingSource = %q, want account", attempt.ModelMappingSource)
	}
	if attempt.SourceEndpointFamily != gatewayopenai.FamilyChatCompletions ||
		attempt.UpstreamEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Errorf("attempt family = %q/%q, want chat_completions/chat_completions",
			attempt.SourceEndpointFamily, attempt.UpstreamEndpointFamily)
	}
	if logs[0].UpstreamModel != "deepseek-v4.1-flash" {
		t.Errorf("审计日志 UpstreamModel = %q, want deepseek-v4.1-flash", logs[0].UpstreamModel)
	}
}
