package main

// chain_media_video_settlement.go：异步媒体任务终态的统一结算面（非对话媒体
// 统一计费设计 §6.1/§7/§14.4）。
//
// 职责：
//   - 创建时冻结 models.media_pricing_json 模板与生效分辨率档（AC-018）；
//   - 终态按冻结模板跑 gateway/internal/pricing 的 media_unified_v1 分支，
//     产出完整 CostBreakdown + cost_usd（AC-001/002/003/006/007/017/027）；
//   - 历史任务无价格快照时回退当前目录价（并供链上层记
//     media_pricing_snapshot_missing 日志）；
//   - legacy 行（无 mediaPricing）保持既有静态目录/按次/秒价行为不变。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// 终态结算的计量维度（usage 行只落一个主维度，与目录模板 unit 对应）。
const (
	chainMediaJobDimensionVideoSeconds = "video_seconds"
	chainMediaJobDimensionVideoCalls   = "video_calls"
	chainMediaJobDimensionAudioSeconds = "audio_seconds"
)

// chainMediaJobSettlement 是一次终态结算的完整结果：
//   - costUSD / breakdown 是账户扣减金额与网关引擎行项；
//   - snapshot 是落库用的完整 CostBreakdown（PricingSnapshot）；
//   - dimension/quantity/resolutionTier/quantitySource 是 usage 行计量字段；
//   - usageMissing 表示计量缺失或模板无价（0 成本 + usage_missing，不猜测）；
//   - media 标记本次是否走统一媒体模板（false=legacy 行为）。
type chainMediaJobSettlement struct {
	costUSD        float64
	breakdown      *pricing.CostBreakdown
	snapshot       *gatewayusage.CostBreakdown
	usageMissing   bool
	dimension      string
	quantity       float64
	resolutionTier string
	quantitySource string
	media          bool
}

// chainMediaJobPricingRow 读取运行时目录行并转换为定价引擎行。requestedModel
// 优先（目录 public_model 通常是请求侧名称），upstreamModel 兜底；目录 miss
// 返回 nil。查询与 chainUsagePricingCatalog 同源（runtime cache + 1s memo）。
func (c *gatewayChain) chainMediaJobPricingRow(ctx context.Context, vendorCode, systemAccountID, requestedModel, upstreamModel string) *pricing.Pricing {
	if c == nil || c.cache == nil || strings.TrimSpace(vendorCode) == "" {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, chainCatalogLookupTimeout)
	defer cancel()
	catalog, err := c.cache.ListCachedProviderModelCatalogAsync(lookupCtx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    vendorCode,
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return nil
	}
	for _, candidate := range []string{requestedModel, upstreamModel} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if item := findChainCatalogItem(catalog, candidate); item != nil {
			return chainCatalogPricing(item)
		}
	}
	return nil
}

// freezeMediaJobPricing 在创建时把统一媒体模板与生效分辨率档写入请求快照
// （AC-018）。目录无模板时保持 nil：终态继续 legacy 路径。冻结失败不阻断
// 受理（价格缺失由终态 usage_missing 兜底，不回滚已受理任务）。
func (c *gatewayChain) freezeMediaJobPricing(ctx context.Context, req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, record *gatewaymedia.MediaJobRecord) {
	if record == nil {
		return
	}
	requestedModel := ""
	if req != nil {
		if model, ok := gatewaypreauth.RequestModel(req); ok {
			requestedModel = model
		}
	}
	row := c.chainMediaJobPricingRow(ctx, account.VendorCode,
		firstNonEmptyString(account.AccountOwnerSystemAccountID, account.SystemAccountID),
		requestedModel, record.RequestSnapshot.Model)
	if row == nil || row.MediaPricing == nil {
		return
	}
	record.RequestSnapshot.MediaPricing = row.MediaPricing.Clone()
	record.RequestSnapshot.ResolutionTier = chainMediaJobSnapshotResolutionTier(record.Kind, record.RequestSnapshot.Size, row.MediaPricing)
}

// chainMediaJobSnapshotResolutionTier 计算创建时冻结的生效分辨率档：
// 视频按 size 短边归档，音频/按次无档位；size 缺失/auto 时留空由引擎用
// baseTier。终态上游回报不同档位时以终态为准（当前 IR 无尺寸字段，保留
// 冻结节位）。
func chainMediaJobSnapshotResolutionTier(kind gatewaymedia.MediaJobKind, size string, template *mediapricing.Pricing) string {
	if template == nil || template.Unit != mediapricing.UnitSecond || kind != gatewaymedia.JobKindVideo {
		return ""
	}
	width, height, ok := chainMediaJobSizeOf(size)
	if !ok {
		return ""
	}
	tier, ok := mediapricing.TierForDimensions(mediapricing.KindVideo, width, height)
	if !ok {
		return ""
	}
	return tier
}

// mediaJobTerminalPricing 解析终态定价行：冻结模板优先；无冻结时读当前
// 运行时目录；目录 miss 回退静态 legacy 快照（历史任务回退口径，设计 §7.1）。
func (c *gatewayChain) mediaJobTerminalPricing(ctx context.Context, account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord) *pricing.Pricing {
	vendorCode := accountProviderCodeForPricing(account)
	mode := "video"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		mode = "audio"
	}
	ownerSystemAccountID := record.OwnerSystemAccountID
	if account != nil && account.OwnerSystemAccountID != "" {
		ownerSystemAccountID = account.OwnerSystemAccountID
	}
	row := c.chainMediaJobPricingRow(ctx, vendorCode, ownerSystemAccountID, "", record.RequestSnapshot.Model)
	if row == nil {
		row = &pricing.Pricing{
			ProviderCode: vendorCode,
			Model:        record.RequestSnapshot.Model,
			Mode:         mode,
		}
		if legacy := pricing.FindProviderModelPricing(vendorCode, record.RequestSnapshot.Model); legacy != nil {
			row.PriceSet = legacy.PriceSet
			row.MediaPricing = legacy.MediaPricing.Clone()
		}
	}
	if row.Mode == "" {
		row.Mode = mode
	}
	if frozen := record.RequestSnapshot.MediaPricing; frozen != nil {
		row.MediaPricing = frozen.Clone()
	}
	return row
}

// chainMediaJobTerminalSettlement 计算 completed 任务的统一结算。failed/
// cancelled/expired 返回零值（不虚计）。无 mediaPricing 的行保持 legacy
// 静态目录成本路径，行为与改造前一致。
func (c *gatewayChain) chainMediaJobTerminalSettlement(ctx context.Context, account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) chainMediaJobSettlement {
	if record == nil || ir == nil || ir.Status != gatewaymedia.JobStatusCompleted {
		return chainMediaJobSettlement{}
	}
	row := c.mediaJobTerminalPricing(ctx, account, record)
	if row == nil || row.MediaPricing == nil {
		return chainMediaJobLegacySettlement(account, record, ir)
	}
	settlement := chainMediaJobSettlement{media: true}
	settlement.resolutionTier = record.RequestSnapshot.ResolutionTier
	if settlement.resolutionTier == "" && record.Kind == gatewaymedia.JobKindVideo {
		settlement.resolutionTier = chainMediaJobSnapshotResolutionTier(record.Kind, record.RequestSnapshot.Size, row.MediaPricing)
	}
	input := pricing.CostInput{
		ProviderCode:    row.ProviderCode,
		Model:           row.Model,
		ResolutionTier:  settlement.resolutionTier,
		MediaPricing:    record.RequestSnapshot.MediaPricing,
		HasMediaPricing: record.RequestSnapshot.MediaPricing != nil,
	}
	switch row.MediaPricing.Unit {
	case mediapricing.UnitSecond:
		if record.Kind == gatewaymedia.JobKindAudioTranscription {
			if ir.Usage.AudioInputSeconds != nil && *ir.Usage.AudioInputSeconds > 0 {
				settlement.dimension = chainMediaJobDimensionAudioSeconds
				settlement.quantity = *ir.Usage.AudioInputSeconds
				settlement.quantitySource = "upstream"
				input.AudioInputSeconds = ir.Usage.AudioInputSeconds
			}
		} else {
			if seconds, source := chainMediaJobVideoQuantityOf(account, record, ir, true); seconds != nil {
				settlement.dimension = chainMediaJobDimensionVideoSeconds
				settlement.quantity = *seconds
				settlement.quantitySource = source
				input.OutputVideoSeconds = seconds
			} else {
				// P4 兜底（设计 §8.4）：upstream/request/default 都没有秒数时，
				// 只有显式开启的适配器才允许对产物做有界 Range 探测。成功即
				// 把探测秒数回填 IR usage（终态行 usage JSON 可审计），失败
				// 仍走 usage_missing，不重试、不整段下载。
				vendorCode := record.VendorCode
				if account != nil && account.ProviderCode != "" {
					vendorCode = account.ProviderCode
				}
				if gatewaymedia.VideoArtifactDurationProbeForProvider(vendorCode) {
					if seconds, ok := c.chainMediaJobProbeVideoSeconds(ctx, account, record, ir); ok {
						settlement.dimension = chainMediaJobDimensionVideoSeconds
						settlement.quantity = seconds
						settlement.quantitySource = "gateway_parse"
						probed := seconds
						input.OutputVideoSeconds = &probed
						ir.Usage.OutputVideoSeconds = &probed
					}
				}
			}
		}
	case mediapricing.UnitCall:
		settlement.dimension = chainMediaJobDimensionVideoCalls
		settlement.quantity = 1
		settlement.quantitySource = "upstream"
		one := 1.0
		input.VideoCalls = &one
	}
	if settlement.dimension == "" {
		settlement.usageMissing = true
		return settlement
	}
	input.QuantitySource = settlement.quantitySource
	breakdown := pricing.BuildCostBreakdown(row, input)
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		// 模板非法/档位未配置/目录无价：0 成本 + usage_missing，不回落 legacy。
		settlement.usageMissing = true
		return settlement
	}
	settlement.costUSD = *breakdown.AccountChargeUsd
	settlement.breakdown = breakdown
	settlement.snapshot = chainMediaJobPricingSnapshot(breakdown)
	return settlement
}

// chainMediaJobLegacySettlement 保持改造前的 legacy 成本与计量口径：
// 按次价 >0 先按次；否则秒价链；无计量依据 usage_missing。legacy 路径不
// 传 PricingSnapshot，由 usagepricing 按当前目录冻结（既有行为）。
func chainMediaJobLegacySettlement(account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) chainMediaJobSettlement {
	settlement := chainMediaJobSettlement{costUSD: chainMediaJobTerminalCostUsd(account, record, ir)}
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		if ir.Usage.AudioInputSeconds != nil {
			settlement.dimension = chainMediaJobDimensionAudioSeconds
			settlement.quantity = *ir.Usage.AudioInputSeconds
			settlement.quantitySource = "upstream"
		} else {
			settlement.usageMissing = true
		}
		return settlement
	}
	if chainMediaJobVideoPerCallCostUsd(account, record) > 0 {
		settlement.dimension = chainMediaJobDimensionVideoCalls
		settlement.quantity = 1
		settlement.quantitySource = "upstream"
		return settlement
	}
	if seconds, source := chainMediaJobVideoQuantityOf(account, record, ir, false); seconds != nil {
		settlement.dimension = chainMediaJobDimensionVideoSeconds
		settlement.quantity = *seconds
		settlement.quantitySource = source
		return settlement
	}
	settlement.usageMissing = true
	return settlement
}

// chainMediaJobVideoQuantityOf 按优先级解析视频秒数：
//  1. 上游终态 duration → upstream；
//  2. 创建请求生效 seconds → request；
//  3. applyDefault=true 时 adapter 声明默认时长（P1 仅 Veo 3.0/3.1 系 8s）
//     → default；legacy 路径传 false，保持改造前不猜默认值的行为；
//  4. 全部缺失 → nil（调用方 usage_missing，不猜测）。
func chainMediaJobVideoQuantityOf(account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR, applyDefault bool) (*float64, string) {
	if ir != nil && ir.Usage.OutputVideoSeconds != nil && *ir.Usage.OutputVideoSeconds > 0 {
		return ir.Usage.OutputVideoSeconds, "upstream"
	}
	if record.RequestSnapshot.Seconds != nil && *record.RequestSnapshot.Seconds > 0 &&
		slices.Contains(record.ParamsApplied, "seconds") {
		return record.RequestSnapshot.Seconds, "request"
	}
	if !applyDefault {
		return nil, ""
	}
	vendorCode := record.VendorCode
	if account != nil && account.ProviderCode != "" {
		vendorCode = account.ProviderCode
	}
	if seconds, ok := gatewaymedia.VideoDefaultSecondsForModel(vendorCode, record.RequestSnapshot.Model); ok {
		return &seconds, "default"
	}
	return nil, ""
}

// chainMediaJobRequestSnapshotHash 是落库失败告警的请求快照指纹（不含
// prompt 原文的哈希；固定字段 requestSnapshotHash 用于关联上游 job）。
func chainMediaJobRequestSnapshotHash(snapshot gatewaymedia.MediaJobRequestSnapshot) string {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// chainMediaJobPricingSnapshot 把网关 pricing.CostBreakdown 转为落库快照结构
// （usagewriter.CostBreakdown）：行项的 tier/baseUnitPriceUsd/multiplier/
// quantitySource 与金额逐项保留，避免 usagepricing 按当前目录重算（AC-018）。
func chainMediaJobPricingSnapshot(breakdown *pricing.CostBreakdown) *gatewayusage.CostBreakdown {
	if breakdown == nil {
		return nil
	}
	out := &gatewayusage.CostBreakdown{
		Currency:                 breakdown.Currency,
		BillingPolicy:            breakdown.BillingPolicy,
		InputCostUsd:             breakdown.InputCostUsd,
		OutputCostUsd:            breakdown.OutputCostUsd,
		InputUsdPer1M:            breakdown.InputUsdPer1M,
		OutputUsdPer1M:           breakdown.OutputUsdPer1M,
		CacheReadCostUsd:         breakdown.CacheReadCostUsd,
		CacheReadUsdPer1M:        breakdown.CacheReadUsdPer1M,
		CacheWriteCostUsd:        breakdown.CacheWriteCostUsd,
		CacheWriteUsdPer1M:       breakdown.CacheWriteUsdPer1M,
		CacheWrite1hCostUsd:      breakdown.CacheWrite1hCostUsd,
		CacheWrite1hUsdPer1M:     breakdown.CacheWrite1hUsdPer1M,
		ThinkingTokens:           breakdown.ThinkingTokens,
		InputImageCostUsd:        breakdown.InputImageCostUsd,
		OutputImageCostUsd:       breakdown.OutputImageCostUsd,
		InputImageUsdPer1M:       breakdown.InputImageUsdPer1M,
		OutputImageUsdPer1M:      breakdown.OutputImageUsdPer1M,
		InputAudioCostUsd:        breakdown.InputAudioCostUsd,
		OutputAudioCostUsd:       breakdown.OutputAudioCostUsd,
		InputAudioUsdPer1M:       breakdown.InputAudioUsdPer1M,
		OutputAudioUsdPer1M:      breakdown.OutputAudioUsdPer1M,
		OutputImageUnitCostUsd:   breakdown.OutputImageUnitCostUsd,
		OutputUsdPerImage:        breakdown.OutputUsdPerImage,
		AccountChargeUsd:         breakdown.AccountChargeUsd,
		Multiplier:               breakdown.Multiplier,
		ServiceTierPricingSource: string(breakdown.ServiceTierPricingSource),
		ServiceTierMultiplier:    breakdown.ServiceTierMultiplier,
	}
	for _, line := range breakdown.LineItems {
		out.LineItems = append(out.LineItems, gatewayusage.CostLineItem{
			Key:              line.Key,
			Kind:             string(line.Kind),
			Label:            line.Label,
			Quantity:         line.Quantity,
			Unit:             line.Unit,
			UnitSize:         line.UnitSize,
			UnitPriceUsd:     line.UnitPriceUsd,
			CostUsd:          line.CostUsd,
			Tier:             line.Tier,
			BaseUnitPriceUsd: line.BaseUnitPriceUsd,
			Multiplier:       line.Multiplier,
			QuantitySource:   line.QuantitySource,
		})
	}
	return out
}
