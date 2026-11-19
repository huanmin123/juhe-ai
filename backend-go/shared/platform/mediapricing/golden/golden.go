// Package golden 提供统一媒体计费两个引擎共用的黄金用例
// （非对话媒体统一计费设计 §6.1/AC-015）。
//
// 本包只被 gateway/internal/pricing 与 shared/platform/usagepricing 的测试
// 引用：同一组模板、输入与预期行项由两边分别执行，防止两侧字面量各自漂移。
package golden

import "github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"

// Want 是两引擎必须逐项一致的预期。
type Want struct {
	// Nil 表示引擎必须返回 nil（价格数据非法/档位不支持）。
	Nil bool
	// LineCount 为 0 表示返回非 nil 但无行项（数量缺失，0 成本）。
	LineCount        int
	BillingPolicy    string
	Key              string
	Kind             string
	Unit             string
	Tier             string
	Multiplier       *float64
	BaseUnitPriceUsd *float64
	CostUsd          *float64
	AccountChargeUsd *float64
	QuantitySource   string
}

// Case 是一组引擎输入与预期输出。
type Case struct {
	Name               string
	Mode               string
	Template           *mediapricing.Pricing
	OutputVideoSeconds *float64
	VideoCalls         *float64
	AudioInputSeconds  *float64
	OutputImageCount   *int
	ResolutionTier     string
	QuantitySource     string
	Want               Want
}

func Float(value float64) *float64 { return &value }
func Int(value int) *int           { return &value }

// Cases 返回两引擎共用的黄金用例。
func Cases() []Case {
	return []Case{
		{
			Name:               "video_seconds_1080p",
			Mode:               "video",
			Template:           videoTemplate(),
			OutputVideoSeconds: Float(10),
			ResolutionTier:     "1080p",
			QuantitySource:     "upstream",
			Want: Want{
				LineCount:        1,
				BillingPolicy:    "media_unified_v1",
				Key:              "video_output_seconds",
				Kind:             "video_output_seconds",
				Unit:             "second",
				Tier:             "1080p",
				Multiplier:       Float(2),
				BaseUnitPriceUsd: Float(0.05),
				CostUsd:          Float(1),
				AccountChargeUsd: Float(1),
				QuantitySource:   "upstream",
			},
		},
		{
			Name:           "video_call",
			Mode:           "video",
			Template:       callTemplate(),
			VideoCalls:     Float(1),
			QuantitySource: "upstream",
			Want: Want{
				LineCount:        1,
				BillingPolicy:    "media_unified_v1",
				Key:              "video_output_calls",
				Kind:             "video_output_calls",
				Unit:             "call",
				Tier:             "",
				Multiplier:       Float(1),
				BaseUnitPriceUsd: Float(0.3429),
				CostUsd:          Float(0.3429),
				AccountChargeUsd: Float(0.3429),
				QuantitySource:   "upstream",
			},
		},
		{
			Name:              "audio_seconds",
			Mode:              "audio",
			Template:          audioTemplate(),
			AudioInputSeconds: Float(12.5),
			QuantitySource:    "gateway_parse",
			Want: Want{
				LineCount:        1,
				BillingPolicy:    "media_unified_v1",
				Key:              "audio_input_seconds",
				Kind:             "audio_input_seconds",
				Unit:             "second",
				Tier:             "",
				Multiplier:       Float(1),
				BaseUnitPriceUsd: Float(0.0002),
				CostUsd:          Float(0.0025),
				AccountChargeUsd: Float(0.0025),
				QuantitySource:   "gateway_parse",
			},
		},
		{
			Name:             "image_2k_two_images",
			Mode:             "image_generation",
			Template:         imageTemplate(),
			OutputImageCount: Int(2),
			ResolutionTier:   "2k",
			QuantitySource:   "upstream",
			Want: Want{
				LineCount:        1,
				BillingPolicy:    "media_unified_v1",
				Key:              "image_output_unit",
				Kind:             "image_output_unit",
				Unit:             "image",
				Tier:             "2k",
				Multiplier:       Float(1.6),
				BaseUnitPriceUsd: Float(0.04),
				CostUsd:          Float(0.128),
				AccountChargeUsd: Float(0.128),
				QuantitySource:   "upstream",
			},
		},
		{
			Name:              "audio_free_zero_price",
			Mode:              "audio",
			Template:          freeAudioTemplate(),
			AudioInputSeconds: Float(3),
			Want: Want{
				LineCount:        1,
				BillingPolicy:    "media_unified_v1",
				Key:              "audio_input_seconds",
				Kind:             "audio_input_seconds",
				Unit:             "second",
				Tier:             "",
				Multiplier:       Float(1),
				BaseUnitPriceUsd: Float(0),
				CostUsd:          Float(0),
				AccountChargeUsd: Float(0),
			},
		},
		{
			Name:           "video_missing_quantity",
			Mode:           "video",
			Template:       videoTemplate(),
			ResolutionTier: "1080p",
			Want: Want{
				LineCount:        0,
				BillingPolicy:    "media_unified_v1",
				AccountChargeUsd: Float(0),
			},
		},
		{
			Name:               "video_invalid_tier",
			Mode:               "video",
			Template:           videoTemplate(),
			OutputVideoSeconds: Float(10),
			ResolutionTier:     "800p",
			Want:               Want{Nil: true},
		},
	}
}

func videoTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:  1,
		Unit:     mediapricing.UnitSecond,
		BaseUsd:  0.05,
		BaseTier: mediapricing.VideoTier720P,
		TierMultipliers: map[string]float64{
			"480p": 0.5, "720p": 1.0, "1080p": 2.0, "1440p": 3.0, "2160p": 4.0,
		},
	}
}

func imageTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:  1,
		Unit:     mediapricing.UnitImage,
		BaseUsd:  0.04,
		BaseTier: mediapricing.ImageTier1K,
		TierMultipliers: map[string]float64{
			"1k": 1.0, "2k": 1.6, "4k": 3.0,
		},
	}
}

func audioTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.0002,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}

func freeAudioTemplate() *mediapricing.Pricing {
	template := audioTemplate()
	template.BaseUsd = 0
	return template
}

func callTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitCall,
		BaseUsd:         0.3429,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}
