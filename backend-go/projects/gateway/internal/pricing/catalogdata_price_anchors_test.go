package pricing

// 五家落值锚点（2026-10-17 全供应商口径核对批）：catalogdata/*.json 是唯一
// 数据源（go:embed 运行时加载 + maintenance DB 种子双通道），本测试锁住
// 2026-10-17 官方页核对后的关键回正值——防后续 JSON 编辑静默丢失、国际站
// 直落口径静默回归、或官方改价后无人察觉。口径裁决（provider_protocol_
// profiles.base_url 探针证实）：glm（open.bigmodel.cn）、minimax（api.
// minimax.chat）、qwen（dashscope.aliyuncs.com）、deepseek（api.deepseek.com）
// 四家账户均连国内站、账单人民币，单价一律国内明文 ÷ 约定汇率 7.0；xai
// （api.x.ai）为国际站 USD 明文直落。期望值为落值脚本双精度运算的位精确
// 字面量（Go 常量算术舍入点与之不同，不写表达式）。
import (
	"testing"
)

func TestDomesticProviderCatalogPriceAnchors(t *testing.T) {
	rowsFor := func(data []rawModel) map[string]rawModel {
		rows := map[string]rawModel{}
		for _, m := range data {
			rows[m.Model] = m
		}
		return rows
	}

	// ---- minimax（platform.minimax.cn 定价概览，2026-10-17 直取 .md）----
	mm := rowsFor(minimaxModelPricingData)
	// TTS 国内同价 hd 3.50 元/万字符 ÷7.0；turbo 2.00 元/万 ÷7.0。
	for model, want := range map[string]float64{
		"speech-2.8-hd":    5e-05,
		"speech-2.6-hd":    5e-05,
		"speech-2.8-turbo": 2.8571428571428574e-05,
	} {
		row, ok := mm[model]
		if !ok {
			t.Fatalf("minimax %s row missing", model)
		}
		if got := floatOf(row.TtsInputCostPerChar); got != want {
			t.Fatalf("minimax %s TtsInputCostPerChar = %v, want %v", model, got, want)
		}
	}
	// 视频主档：H3 与 H3-Max 均 768P 0.50 元/秒 ÷7.0；Hailuo-2.3 768P-6s
	// 2.00 元/条 ÷7.0（此前 H3-Max 误落 480P 档、Hailuo 落国际明文，本批回正）。
	if h3, ok := mm["MiniMax-H3"]; !ok {
		t.Fatal("minimax MiniMax-H3 row missing")
	} else if got := floatOf(h3.VideoOutputCostPerSecond); got != 0.07142857142857142 {
		t.Fatalf("minimax MiniMax-H3 VideoOutputCostPerSecond = %v, want 0.07142857142857142", got)
	}
	if h3m, ok := mm["MiniMax-H3-Max"]; !ok {
		t.Fatal("minimax MiniMax-H3-Max row missing")
	} else if got := floatOf(h3m.VideoOutputCostPerSecond); got != 0.07142857142857142 {
		t.Fatalf("minimax MiniMax-H3-Max VideoOutputCostPerSecond = %v, want 0.07142857142857142（768P 主档，非 480P）", got)
	}
	if hailuo, ok := mm["MiniMax-Hailuo-2.3"]; !ok {
		t.Fatal("minimax MiniMax-Hailuo-2.3 row missing")
	} else if got := floatOf(hailuo.VideoOutputCostPerCall); got != 0.2857142857142857 {
		t.Fatalf("minimax MiniMax-Hailuo-2.3 VideoOutputCostPerCall = %v, want 0.2857142857142857", got)
	}
	// 缺口补录批（2026-10-17）：Hailuo-2.3-Fast 768P-6s 1.35 元/条 ÷7.0。
	if fast, ok := mm["MiniMax-Hailuo-2.3-Fast"]; !ok {
		t.Fatal("minimax MiniMax-Hailuo-2.3-Fast row missing")
	} else if got := floatOf(fast.VideoOutputCostPerCall); got != 0.19285714285714287 {
		t.Fatalf("minimax MiniMax-Hailuo-2.3-Fast VideoOutputCostPerCall = %v, want 0.19285714285714287", got)
	}
	// M2.7 缓存写 2.625 元/百万 ÷7.0（本批补齐；此前整字段缺失）。
	if m27, ok := mm["MiniMax-M2.7"]; !ok {
		t.Fatal("minimax MiniMax-M2.7 row missing")
	} else if got := floatOf(m27.CacheCreationInputTokenCost); got != 3.75e-07 {
		t.Fatalf("minimax MiniMax-M2.7 CacheCreationInputTokenCost = %v, want 3.75e-07", got)
	}
	// 缺口补录第二批（2026-10-17）：M2 家族历史模型 2.1/8.4 元、缓存读 0.21、
	// 缓存写 2.625 元/百万（highspeed 4.2/16.8）÷7.0；H3-Context-IR 任务价
	// 5.80/23.00 元/百万 ÷7.0；H3-Regeneration 768P→2K 再生成 0.30 元/秒 ÷7.0。
	for model, wants := range map[string][3]float64{
		"MiniMax-M2.5":           {3e-07, 1.2e-06, 3e-08},
		"MiniMax-M2.5-highspeed": {6e-07, 2.4e-06, 3e-08},
		"MiniMax-M2.1":           {3e-07, 1.2e-06, 3e-08},
		"MiniMax-M2.1-highspeed": {6e-07, 2.4e-06, 3e-08},
		"MiniMax-M2":             {3e-07, 1.2e-06, 3e-08},
	} {
		row, ok := mm[model]
		if !ok {
			t.Fatalf("minimax %s row missing", model)
		}
		if got := floatOf(row.InputCostPerToken); got != wants[0] {
			t.Fatalf("minimax %s InputCostPerToken = %v, want %v", model, got, wants[0])
		}
		if got := floatOf(row.OutputCostPerToken); got != wants[1] {
			t.Fatalf("minimax %s OutputCostPerToken = %v, want %v", model, got, wants[1])
		}
		if got := floatOf(row.CacheReadInputTokenCost); got != wants[2] {
			t.Fatalf("minimax %s CacheReadInputTokenCost = %v, want %v", model, got, wants[2])
		}
	}
	if regen, ok := mm["MiniMax-H3-Regeneration"]; !ok {
		t.Fatal("minimax MiniMax-H3-Regeneration row missing")
	} else if got := floatOf(regen.VideoOutputCostPerSecond); got != 0.04285714285714286 {
		t.Fatalf("minimax MiniMax-H3-Regeneration VideoOutputCostPerSecond = %v, want 0.04285714285714286", got)
	}
	// H3-Context-IR 任务价：输入/输出 token 价（无缓存价字段，floatOf 缺失哨兵 -1）。
	if ir, ok := mm["MiniMax-H3-Context-IR"]; !ok {
		t.Fatal("minimax MiniMax-H3-Context-IR row missing")
	} else {
		if got := floatOf(ir.InputCostPerToken); got != 8.285714285714285e-07 {
			t.Fatalf("minimax MiniMax-H3-Context-IR InputCostPerToken = %v, want 8.285714285714285e-07", got)
		}
		if got := floatOf(ir.OutputCostPerToken); got != 3.2857142857142857e-06 {
			t.Fatalf("minimax MiniMax-H3-Context-IR OutputCostPerToken = %v, want 3.2857142857142857e-06", got)
		}
		if got := floatOf(ir.CacheReadInputTokenCost); got != -1 {
			t.Fatalf("minimax MiniMax-H3-Context-IR CacheReadInputTokenCost = %v, want -1（字段缺失）", got)
		}
	}

	// ---- qwen（百炼模型计费页，2026-10-17 浏览器渲染）----
	qw := rowsFor(qwenModelPricingData)
	for model, wants := range map[string][2]float64{
		"qwen3.8-max":   {1.7142857142857143e-06, 5.142857142857143e-06},
		"qwen3.8-flash": {1.142857142857143e-07, 3.8571428571428574e-07},
		"qwen3.7-plus":  {2.857142857142857e-07, 1.1428571428571428e-06},
	} {
		row, ok := qw[model]
		if !ok {
			t.Fatalf("qwen %s row missing", model)
		}
		if got := floatOf(row.InputCostPerToken); got != wants[0] {
			t.Fatalf("qwen %s InputCostPerToken = %v, want %v", model, got, wants[0])
		}
		if got := floatOf(row.OutputCostPerToken); got != wants[1] {
			t.Fatalf("qwen %s OutputCostPerToken = %v, want %v", model, got, wants[1])
		}
	}
	// 语音三件：TTS 0.8 元/万字符、cosyvoice 1 元/万、ASR 0.00022 元/秒（÷7.0）。
	if tts, ok := qw["qwen3-tts-flash"]; !ok {
		t.Fatal("qwen qwen3-tts-flash row missing")
	} else if got := floatOf(tts.TtsInputCostPerChar); got != 1.1428571428571429e-05 {
		t.Fatalf("qwen qwen3-tts-flash TtsInputCostPerChar = %v, want 1.1428571428571429e-05", got)
	}
	if cosy, ok := qw["cosyvoice-v3-flash"]; !ok {
		t.Fatal("qwen cosyvoice-v3-flash row missing")
	} else if got := floatOf(cosy.TtsInputCostPerChar); got != 1.4285714285714287e-05 {
		t.Fatalf("qwen cosyvoice-v3-flash TtsInputCostPerChar = %v, want 1.4285714285714287e-05", got)
	}
	if asr, ok := qw["qwen3-asr-flash"]; !ok {
		t.Fatal("qwen qwen3-asr-flash row missing")
	} else if got := floatOf(asr.AudioInputCostPerSecond); got != 3.142857142857143e-05 {
		t.Fatalf("qwen qwen3-asr-flash AudioInputCostPerSecond = %v, want 3.142857142857143e-05", got)
	}
	// 万相视频 720P 刊例 0.6 元/秒 ÷7.0（限时 7 折留注记不落值）。
	for _, model := range []string{"wan3.0-video", "wan2.7-t2v"} {
		row, ok := qw[model]
		if !ok {
			t.Fatalf("qwen %s row missing", model)
		}
		if got := floatOf(row.VideoOutputCostPerSecond); got != 0.08571428571428572 {
			t.Fatalf("qwen %s VideoOutputCostPerSecond = %v, want 0.08571428571428572", model, got)
		}
	}

	// ---- glm（open.bigmodel.cn/pricing，2026-10-17 web_reader）----
	gl := rowsFor(glmModelPricingData)
	for model, wants := range map[string][2]float64{
		"glm-5.3":       {1.1428571428571428e-06, 4e-06},
		"glm-5.2":       {1.1428571428571428e-06, 4e-06},
		"glm-5.3-flash": {1.142857142857143e-07, 4e-07},
		"glm-4.6v":      {1.4285714285714285e-07, 4.2857142857142857e-07},
	} {
		row, ok := gl[model]
		if !ok {
			t.Fatalf("glm %s row missing", model)
		}
		if got := floatOf(row.InputCostPerToken); got != wants[0] {
			t.Fatalf("glm %s InputCostPerToken = %v, want %v", model, got, wants[0])
		}
		if got := floatOf(row.OutputCostPerToken); got != wants[1] {
			t.Fatalf("glm %s OutputCostPerToken = %v, want %v", model, got, wants[1])
		}
	}
	// TTS 换算公式修正：2 元/万字符 = 2×100÷7.0（此前 2×10÷7.0 少 10 倍）。
	for _, model := range []string{"cogtts", "glm-tts"} {
		row, ok := gl[model]
		if !ok {
			t.Fatalf("glm %s row missing", model)
		}
		if got := floatOf(row.TtsInputCostPerChar); got != 2.8571428571428574e-05 {
			t.Fatalf("glm %s TtsInputCostPerChar = %v, want 2.8571428571428574e-05", model, got)
		}
	}

	// ---- deepseek（api-docs.deepseek.com 高峰时段价 ÷7.0，2026-10-17）----
	ds := rowsFor(deepSeekModelPricingData)
	for model, wants := range map[string][3]float64{
		"deepseek-flash":      {5.714285714285714e-09, 2.857142857142857e-07, 1.1428571428571428e-06},
		"deepseek-v4.1-flash": {5.714285714285714e-09, 2.857142857142857e-07, 1.1428571428571428e-06},
		"deepseek-v4-pro":     {4.285714285714286e-08, 1.2857142857142858e-06, 3.857142857142857e-06},
	} {
		row, ok := ds[model]
		if !ok {
			t.Fatalf("deepseek %s row missing", model)
		}
		if got := floatOf(row.CacheReadInputTokenCost); got != wants[0] {
			t.Fatalf("deepseek %s CacheReadInputTokenCost = %v, want %v", model, got, wants[0])
		}
		if got := floatOf(row.InputCostPerToken); got != wants[1] {
			t.Fatalf("deepseek %s InputCostPerToken = %v, want %v", model, got, wants[1])
		}
		if got := floatOf(row.OutputCostPerToken); got != wants[2] {
			t.Fatalf("deepseek %s OutputCostPerToken = %v, want %v", model, got, wants[2])
		}
	}

	// ---- xai（docs.x.ai/developers/pricing USD 直落，2026-10-17 浏览器渲染）----
	xr := rowsFor(xAIModelPricingData)
	// STS 官方 $0.08/分钟（grok-voice-3.0）→ 折秒 0.08÷60；此前落 $3/小时旧价。
	if agent, ok := xr["grok-voice-agent"]; !ok {
		t.Fatal("xai grok-voice-agent row missing")
	} else if got := floatOf(agent.AudioInputCostPerSecond); got != 0.0013333333333333333 {
		t.Fatalf("xai grok-voice-agent AudioInputCostPerSecond = %v, want 0.0013333333333333333", got)
	}
	// video-1.5 主档 720p $0.14/s 分档锚点。
	if vid, ok := xr["grok-imagine-video-1.5"]; !ok {
		t.Fatal("xai grok-imagine-video-1.5 row missing")
	} else if got := floatOf(vid.VideoOutputCostPerSecond); got != 0.14 {
		t.Fatalf("xai grok-imagine-video-1.5 VideoOutputCostPerSecond = %v, want 0.14", got)
	}
	// 缺口补录批（2026-10-17）：video-1.5-lite 主档 720p $0.03/s（非最低档
	// 主档先例）；grok-tts 占位行 $15/1M chars 折 $1.5e-05/字符（官方 /v1/tts
	// 面无 model 字段，行名仅作路由与计价载体）。
	if lite, ok := xr["grok-imagine-video-1.5-lite"]; !ok {
		t.Fatal("xai grok-imagine-video-1.5-lite row missing")
	} else if got := floatOf(lite.VideoOutputCostPerSecond); got != 0.03 {
		t.Fatalf("xai grok-imagine-video-1.5-lite VideoOutputCostPerSecond = %v, want 0.03", got)
	}
	if tts, ok := xr["grok-tts"]; !ok {
		t.Fatal("xai grok-tts row missing")
	} else if got := floatOf(tts.TtsInputCostPerChar); got != 1.5e-05 {
		t.Fatalf("xai grok-tts TtsInputCostPerChar = %v, want 1.5e-05", got)
	}
	// 缺口补录第二批（2026-10-17）：旧版 grok-imagine-video 主档 720p $0.07/s
	//（480p $0.05 分档留档不落值）。
	if legacy, ok := xr["grok-imagine-video"]; !ok {
		t.Fatal("xai grok-imagine-video row missing")
	} else if got := floatOf(legacy.VideoOutputCostPerSecond); got != 0.07 {
		t.Fatalf("xai grok-imagine-video VideoOutputCostPerSecond = %v, want 0.07", got)
	}
}
