package main

// DR-28：compact 合成摘要成功路径的 usage 记账。摘要请求在 preflight 内
// 独立派发，不经过主响应管道，原先只 finalize 审计、不写 usage；这里把上游
// 摘要响应的 usage 字段按完成尝试口径写成一条正常 usage 记录（含 token 与
// 同步定价成本），使摘要 token 与普通请求同源可查。

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

type chainCompactUsageRecorder struct {
	recorder           gatewayusage.UsageRecorder
	pricing            gatewayusage.PricingCatalog
	syncPricingAllowed bool
}

func (r chainCompactUsageRecorder) Record(
	input gatewaycodex.CompactPreflightInput,
	account gatewayruntimecache.OpenAIAccountSecret,
	model string,
	usage gatewayproto.ParsedUsage,
) {
	if r.recorder == nil {
		return
	}
	view := gatewayresponse.OpenAIAccountView{Account: account}
	providerCode := account.VendorCode
	catalogSystemAccountID := chainCatalogSystemAccountID(view, input.SystemAccountID)
	record := gatewayusage.UsageRecordInput{
		TraceID:                   input.UsageContext.TraceID,
		TrafficSource:             gatewayusage.OpenAIGatewayTrafficSource(input.UsageContext.TrafficSource),
		ClientIP:                  input.UsageContext.ClientIP,
		SystemAccountID:           input.UsageContext.SystemAccountID,
		APIKeyID:                  input.UsageContext.APIKeyID,
		GroupID:                   input.UsageContext.GroupID,
		RouteGroupID:              input.UsageContext.RouteGroupID,
		CompactionExpected:        true,
		Endpoint:                  input.UsageContext.Endpoint,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		UsageSemantic:             "openai",
		Model:                     model,
		UpstreamModel:             model,
		PricingModel:              model,
		UpstreamResponseModel:     usage.UpstreamResponseModel,
		SourceEndpointFamily:      "chat_completions",
		UpstreamEndpointFamily:    "chat_completions",
		Success:                   true,
		CreatedAt:                 time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		InputTokens:               usage.InputTokens,
		OutputTokens:              usage.OutputTokens,
		CacheReadTokens:           usage.CacheReadTokens,
		CacheWriteTokens:          usage.CacheWriteTokens,
		CacheWrite1hTokens:        usage.CacheWrite1hTokens,
		ThinkingTokens:            usage.ThinkingTokens,
		InputImageTokens:          usage.InputImageTokens,
		OutputImageTokens:         usage.OutputImageTokens,
		InputAudioTokens:          usage.InputAudioTokens,
		OutputAudioTokens:         usage.OutputAudioTokens,
		OutputImageCount:          usage.OutputImageCount,
	}
	stream := false
	statusCode := 200
	record.Stream = &stream
	record.StatusCode = &statusCode
	if usage.ServiceTier != "" {
		record.ReportedServiceTier = usage.ServiceTier
	}
	applyUsageAccountScope(&record, view)
	if r.syncPricingAllowed && r.pricing != nil && model != "" {
		costInput := gatewayusage.PricingCostInput{
			ProviderCode:       providerCode,
			SystemAccountID:    catalogSystemAccountID,
			Model:              model,
			ServiceTier:        firstNonEmptyChainUsage(usage.ServiceTier, input.UsageContext.EffectiveServiceTier, input.UsageContext.RequestedServiceTier),
			InputTokens:        usage.InputTokens,
			OutputTokens:       usage.OutputTokens,
			CacheReadTokens:    usage.CacheReadTokens,
			CacheWriteTokens:   usage.CacheWriteTokens,
			CacheWrite1hTokens: usage.CacheWrite1hTokens,
			ThinkingTokens:     usage.ThinkingTokens,
			InputImageTokens:   usage.InputImageTokens,
			OutputImageTokens:  usage.OutputImageTokens,
			InputAudioTokens:   usage.InputAudioTokens,
			OutputAudioTokens:  usage.OutputAudioTokens,
			OutputImageCount:   usage.OutputImageCount,
		}
		record.CostUsd = r.pricing.EstimateCost(costInput)
		record.CacheReadCostUsd = r.pricing.EstimateCacheReadCost(costInput)
		record.CacheWriteCostUsd = r.pricing.EstimateCacheWriteCost(costInput)
	}
	if err := r.recorder.EnqueueUsageRecord(context.Background(), record); err != nil {
		slog.Error("compact 摘要 usage 入队失败，无法持久投递，已丢弃",
			"event", "gateway_compact_usage_enqueue_failed",
			"traceId", record.TraceID,
			"accountId", account.ID,
			"error", err.Error())
	}
}
