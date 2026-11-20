package main

// 媒体任务终态结算的档位优先级测试（P1-1/P2-4）：创建时冻结档位优先，
// 上游终态回带实际档位时覆盖冻结值；冻结模板的媒体类型以任务 kind 为准。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// TestApplyFrozenMediaJobPricingUsesTaskKindMode 直接覆盖当前目录行 mode
// 已漂移为 audio 的场景：冻结视频模板必须按 kind=video 固定行 mode，不能
// 只依赖 cache=nil 时默认 mode 的路径（旧用例因此假绿）。
func TestApplyFrozenMediaJobPricingUsesTaskKindMode(t *testing.T) {
	frozen := &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.1,
		BaseTier:        mediapricing.VideoTier720P,
		TierMultipliers: mediapricing.DefaultVideoTierMultipliers,
	}
	row := &pricing.Pricing{Mode: "audio", MediaPricingInvalid: true}
	applyFrozenMediaJobPricing(row, "video", frozen)
	if row.Mode != "video" {
		t.Fatalf("mode = %q, want video（冻结模板按任务 kind 固定）", row.Mode)
	}
	if row.MediaPricing == nil || row.MediaPricing.Unit != mediapricing.UnitSecond {
		t.Fatalf("media pricing = %+v, want 冻结视频模板", row.MediaPricing)
	}
	if row.MediaPricingInvalid {
		t.Fatal("冻结模板有效时不得保留 invalid 标记")
	}
}

func TestChainMediaJobSettlementResolutionTierPriority(t *testing.T) {
	record, ir, account := probeTestRecord("")
	record.RequestSnapshot.ResolutionTier = "1080p"
	seconds := 1.0
	ir.Usage.OutputVideoSeconds = &seconds

	settlement := (&gatewayChain{}).chainMediaJobTerminalSettlement(t.Context(), account, record, ir)
	if settlement.breakdown == nil || len(settlement.breakdown.LineItems) != 1 {
		t.Fatalf("settlement = %+v", settlement)
	}
	if tier := settlement.breakdown.LineItems[0].Tier; tier != "1080p" {
		t.Fatalf("冻结档位 = %q, want 1080p", tier)
	}

	// 上游终态回报实际档位（openai size）优先于冻结档位。
	ir.Usage.ResolutionTier = "720p"
	settlement = (&gatewayChain{}).chainMediaJobTerminalSettlement(t.Context(), account, record, ir)
	if settlement.breakdown == nil || len(settlement.breakdown.LineItems) != 1 {
		t.Fatalf("terminal override settlement = %+v", settlement)
	}
	if tier := settlement.breakdown.LineItems[0].Tier; tier != "720p" {
		t.Fatalf("终态档位 = %q, want 720p", tier)
	}
}

// TestChainMediaJobSettlementCallUnitIgnoresTerminalResolutionTier 钉住
// unit=call 视频在终态 IR 带回 size 时也不得注入档位：PriceForTier 对 call
// 携带档位直接拒绝，会把成功任务错误结算成 0 成本 + usage_missing。
func TestChainMediaJobSettlementCallUnitIgnoresTerminalResolutionTier(t *testing.T) {
	record, ir, account := probeTestRecord("")
	record.RequestSnapshot.MediaPricing = &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitCall,
		BaseUsd:         0.342857,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
	ir.Status = gatewaymedia.JobStatusCompleted
	ir.Usage.ResolutionTier = "1080p"
	seconds := 5.0
	ir.Usage.OutputVideoSeconds = &seconds

	settlement := (&gatewayChain{}).chainMediaJobTerminalSettlement(t.Context(), account, record, ir)
	if settlement.usageMissing || settlement.breakdown == nil {
		t.Fatalf("unit=call 应按次结算，不得落 usage_missing: %+v", settlement)
	}
	if settlement.resolutionTier != "" {
		t.Fatalf("resolutionTier = %q, want 空（unit=call 不承载档位）", settlement.resolutionTier)
	}
	if settlement.costUSD != 0.342857 {
		t.Fatalf("cost = %v, want 0.342857", settlement.costUSD)
	}
}

// TestChainMediaJobSettlementAudioIgnoresTerminalResolutionTier 钉住音频任务
// 即使终态 IR 带回分辨率档也不得注入：audio 模板无档位，注入会让
// PriceForTier 拒绝并把成功任务结算成 0 成本 + usage_missing。
func TestChainMediaJobSettlementAudioIgnoresTerminalResolutionTier(t *testing.T) {
	record, ir, account := probeTestRecord("")
	record.Kind = gatewaymedia.JobKindAudioTranscription
	record.RequestSnapshot.Model = "volc.seedasr.auc"
	record.RequestSnapshot.MediaPricing = &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.0002,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
	ir.Status = gatewaymedia.JobStatusCompleted
	ir.Usage.ResolutionTier = "1080p"
	seconds := 12.5
	ir.Usage.AudioInputSeconds = &seconds

	settlement := (&gatewayChain{}).chainMediaJobTerminalSettlement(t.Context(), account, record, ir)
	if settlement.usageMissing || settlement.breakdown == nil || len(settlement.breakdown.LineItems) != 1 {
		t.Fatalf("音频任务应按秒结算，不得落 usage_missing: %+v", settlement)
	}
	if settlement.resolutionTier != "" {
		t.Fatalf("resolutionTier = %q, want 空（音频不承载档位）", settlement.resolutionTier)
	}
	line := settlement.breakdown.LineItems[0]
	if line.Key != "audio_input_seconds" || line.Tier != "" {
		t.Fatalf("line = %s tier=%q, want audio_input_seconds/空", line.Key, line.Tier)
	}
	if settlement.costUSD != 0.0025 {
		t.Fatalf("cost = %v, want 0.0025", settlement.costUSD)
	}
}

func TestChainMediaJobSettlementFrozenTemplateUsesTaskKindMode(t *testing.T) {
	record, ir, account := probeTestRecord("")
	ir.Status = gatewaymedia.JobStatusCompleted
	seconds := 1.0
	ir.Usage.OutputVideoSeconds = &seconds
	// 冻结模板是视频秒价模板；即使当前目录行 mode 已被改成 audio，也必须
	// 按任务 kind=video 校验冻结模板，不能因 ValidateFor(audio) 失败落 0。
	settlement := (&gatewayChain{}).chainMediaJobTerminalSettlement(t.Context(), account, record, ir)
	if settlement.usageMissing || settlement.breakdown == nil {
		t.Fatalf("冻结视频模板应按 video 结算: %+v", settlement)
	}
	if settlement.breakdown.BillingPolicy != "media_unified_v1" {
		t.Fatalf("billing policy = %q", settlement.breakdown.BillingPolicy)
	}
}
