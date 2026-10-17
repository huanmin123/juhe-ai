package pricing

// volcengine 落值锚点（2026-10-17 维度修正批 + 国内价口径批）：catalogdata/
// volcengine.json 是唯一数据源（go:embed 运行时加载 + maintenance DB 种子
// 双通道），本测试锁住关键单价——防后续 JSON 编辑静默丢失、占位价静默回
// 归、或国内/国际口径再度混用。口径裁决：账户为国内方舟（账单人民币），
// 单价一律国内明文 ÷ 约定汇率 7.0（与 seedream 图像行同口径）。
import (
	"testing"
)

func floatOf(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

func TestVolcengineCatalogPriceAnchors(t *testing.T) {
	rows := map[string]rawModel{}
	for _, m := range volcengineModelPricingData {
		rows[m.Model] = m
	}

	// seed-audio-1.0：官方后付费 1 元/分钟（按生成原始时长计费、精确至秒）
	// ÷ 60 ÷ 7.0 = $0.00238095/秒；占位字符价（$4.2857/百万字符）必须保持
	// 撤除——字符非官方计费维度（维度修正批）。
	audio, ok := rows["seed-audio-1.0"]
	if !ok {
		t.Fatalf("seed-audio-1.0 row missing")
	}
	if got := floatOf(audio.AudioInputCostPerSecond); got != 0.00238095 {
		t.Fatalf("seed-audio-1.0 AudioInputCostPerSecond = %v, want 0.00238095", got)
	}
	if audio.TtsInputCostPerChar != nil {
		t.Fatalf("seed-audio-1.0 TtsInputCostPerChar must stay nil (dimension fix), got %v", *audio.TtsInputCostPerChar)
	}
	// 历史占位行同规则：请求经 adapter 归一出站均为 seed-audio-1.0。
	if tts, ok := rows["doubao-tts"]; ok && tts.TtsInputCostPerChar != nil {
		t.Fatalf("doubao-tts TtsInputCostPerChar must stay nil, got %v", *tts.TtsInputCostPerChar)
	}

	// volc.seedasr.auc：豆包录音文件识别 2.0 后付费 0.8 元/小时（按时长
	// 计费、精确至秒折算小时/分钟；双声道计费模式按单声道计费）÷ 3600
	// ÷ 7.0。字面量直钉不写表达式（Go 常量算术舍入点与 JSON 解析双精度
	// 不同源，表达式锚点会因位型差异假失败）。
	asr, ok := rows["volc.seedasr.auc"]
	if !ok {
		t.Fatalf("volc.seedasr.auc row missing")
	}
	if got := floatOf(asr.AudioInputCostPerSecond); got != 3.1746031746031745e-05 {
		t.Fatalf("volc.seedasr.auc AudioInputCostPerSecond = %v, want 3.1746031746031745e-05", got)
	}

	// 视频 720p 主档秒价（国内元/秒 ÷ 7.0）：2.5=1.51、2.0=0.99、
	// fast=0.80、mini=0.50；1-0-pro 无官方每秒价 → 不落值不编造。
	for model, want := range map[string]float64{
		"doubao-seedance-2-5-260628":      0.2157,
		"doubao-seedance-2-0-260128":      0.1414,
		"doubao-seedance-2-0-fast-260128": 0.1143,
		"doubao-seedance-2-0-mini-260615": 0.0714,
	} {
		row, ok := rows[model]
		if !ok {
			t.Fatalf("%s row missing", model)
		}
		if got := floatOf(row.VideoOutputCostPerSecond); got != want {
			t.Fatalf("%s VideoOutputCostPerSecond = %v, want %v", model, got, want)
		}
	}
	if pro, ok := rows["doubao-seedance-1-0-pro-250528"]; ok && pro.VideoOutputCostPerSecond != nil {
		t.Fatalf("doubao-seedance-1-0-pro-250528 must keep nil per-second price (no official per-second quote), got %v", *pro.VideoOutputCostPerSecond)
	}
}
