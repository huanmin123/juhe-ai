package main

// M1 同步音频计量注入单测（契约 §2.8/音频设计 §10）：RecordCompletedUpstream
// Attempt 把 ParsedUsage 的 TtsInputChars / AudioInputSeconds / UsageMissing
// 直传 usage 记录，音频两维进入同步定价估算输入（tts_input_chars /
// audio_input_seconds 行项）。nil 保持 NULL、bool 直传，不猜测。
import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func int64PtrOfAudio(value int64) *int64 { return &value }

func audioMeteringAttemptInput(usage gatewayproto.ParsedUsage, endpoint string) gatewayresponse.CompletedAttemptInput {
	return gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID: "trace_audio_meter", SystemAccountID: "sys", ProviderCode: "gpt",
			// 生产 UsageContext.Endpoint 是审计格式 "METHOD path"
			//（gatewaypreauth.RequestEndpoint），分类判定必须兼容该形态。
			Endpoint: "POST " + endpoint,
		},
		Account: gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
			ID: "acc_tts", AccountOwnerSystemAccountID: "sys",
		}},
		Success:        true,
		StatusCode:     200,
		StartedAtMs:    time.Now().UnixMilli() - 100,
		RequestedModel: "gpt-4o-mini-tts",
		Usage:          usage,
	}
}

// TestChainFinalizationUsageTtsInputCharsRecorded：TTS 请求 input 字符数
// （response 管线自算注入）落 usage 记录并进入定价估算输入。
func TestChainFinalizationUsageTtsInputCharsRecorded(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{cost: floatPtrOf(0.0321)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	chars := int64(42)
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		TtsInputChars: &chars,
	}, "/v1/audio/speech"))

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.TtsInputChars == nil || *record.TtsInputChars != 42 {
		t.Fatalf("TtsInputChars = %v, want 42", record.TtsInputChars)
	}
	if record.AudioInputSeconds != nil {
		t.Fatalf("AudioInputSeconds 必须保持 nil: %v", record.AudioInputSeconds)
	}
	if record.UsageMissing {
		t.Fatal("TTS 有字符计量，不得标记 usage_missing")
	}
	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].TtsInputChars == nil || *catalog.inputs[0].TtsInputChars != 42 {
		t.Fatalf("估算输入 TtsInputChars = %v, want 42", catalog.inputs[0].TtsInputChars)
	}
	if catalog.inputs[0].AudioInputSeconds != nil {
		t.Fatalf("估算输入 AudioInputSeconds 必须保持 nil: %v", catalog.inputs[0].AudioInputSeconds)
	}
	if record.CostUsd == nil || *record.CostUsd != 0.0321 {
		t.Fatalf("CostUsd = %v, want 0.0321（音频行项参与估算）", record.CostUsd)
	}
}

// TestChainFinalizationUsageSttSecondsRecorded：STT 音频秒数（上游 usage 缺
// 席时 verbose_json duration 注入）落 usage 记录并进入定价估算输入。
func TestChainFinalizationUsageSttSecondsRecorded(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	seconds := 2.5
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		AudioInputSeconds: &seconds,
		QuantitySource:    "upstream",
	}, "/v1/audio/transcriptions"))

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.AudioInputSeconds == nil || *record.AudioInputSeconds != 2.5 {
		t.Fatalf("AudioInputSeconds = %v, want 2.5", record.AudioInputSeconds)
	}
	if record.TtsInputChars != nil {
		t.Fatalf("TtsInputChars 必须保持 nil: %v", record.TtsInputChars)
	}
	if record.UsageMissing {
		t.Fatal("STT 有 duration 计量，不得标记 usage_missing")
	}
	if len(catalog.inputs) != 1 {
		t.Fatalf("估算调用次数 = %d, want 1", len(catalog.inputs))
	}
	if catalog.inputs[0].AudioInputSeconds == nil || *catalog.inputs[0].AudioInputSeconds != 2.5 {
		t.Fatalf("估算输入 AudioInputSeconds = %v, want 2.5", catalog.inputs[0].AudioInputSeconds)
	}
}

// TestChainFinalizationUsageMissingFlagRecorded：usage 与 duration 皆缺的 STT
// 响应（usage_missing 标记）原样透传到 usage 记录，计量 0 不猜测。
func TestChainFinalizationUsageMissingFlagRecorded(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder, syncPricingAllowed: true}
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		UsageMissing: true,
	}, "/v1/audio/translations"))

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if !record.UsageMissing {
		t.Fatal("UsageMissing 标记必须直传 usage 记录")
	}
	if record.TtsInputChars != nil || record.AudioInputSeconds != nil {
		t.Fatalf("缺失维度必须保持 nil: %+v", record)
	}
}

// audioMeteringAttemptInputFor 覆盖 provider/model，供统一模板与 legacy
// volcengine 判定用例使用。
func audioMeteringAttemptInputFor(usage gatewayproto.ParsedUsage, endpoint, vendor, model string) gatewayresponse.CompletedAttemptInput {
	input := audioMeteringAttemptInput(usage, endpoint)
	input.RequestedModel = model
	input.Account = gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc_tts", AccountOwnerSystemAccountID: "sys", VendorCode: vendor,
	}}
	return input
}

// TestChainFinalizationMediaPricedAudioKeepsAllFacts：统一模板行（有
// media_pricing_json）保留秒数 + 字符/来源，计费输入按秒结算；usage_missing
// 以秒数为准重算。
func TestChainFinalizationMediaPricedAudioKeepsAllFacts(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{hasMediaPricing: true, cost: floatPtrOf(0.02)}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	chars := int64(42)
	seconds := 10.5
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		TtsInputChars:     &chars,
		AudioInputSeconds: &seconds,
		QuantitySource:    "estimated",
	}, "/v1/audio/speech"))

	record := recorder.records[0]
	if record.AudioInputSeconds == nil || *record.AudioInputSeconds != 10.5 ||
		record.TtsInputChars == nil || *record.TtsInputChars != 42 ||
		record.QuantitySource != "estimated" {
		t.Fatalf("mediaPriced 记录必须保留秒/字符/来源: %+v", record)
	}
	if record.UsageMissing {
		t.Fatalf("统一模板行有秒数不得标记 usage_missing: %+v", record)
	}
	input := catalog.inputs[0]
	if input.AudioInputSeconds == nil || *input.AudioInputSeconds != 10.5 ||
		input.TtsInputChars == nil || *input.TtsInputChars != 42 ||
		input.QuantitySource != "estimated" {
		t.Fatalf("mediaPriced 计费输入 = %+v", input)
	}
}

// TestChainFinalizationMediaPricedAudioWithoutSecondsMarksMissing：统一模板
// 音频行以秒为唯一计费维度，只有字符不构成可结算用量。
func TestChainFinalizationMediaPricedAudioWithoutSecondsMarksMissing(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{hasMediaPricing: true}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	chars := int64(42)
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		TtsInputChars: &chars,
	}, "/v1/audio/speech"))

	record := recorder.records[0]
	if !record.UsageMissing {
		t.Fatalf("统一模板音频行无秒数必须 usage_missing: %+v", record)
	}
}

// TestChainFinalizationLegacySttUpstreamSecondsKept：legacy STT 只消费上游
// duration（无 token 计量）——秒数保留、usage_missing 不误置。
func TestChainFinalizationLegacySttUpstreamSecondsKept(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	seconds := 2.5
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		AudioInputSeconds: &seconds,
		QuantitySource:    "upstream",
	}, "/v1/audio/transcriptions"))

	record := recorder.records[0]
	if record.AudioInputSeconds == nil || *record.AudioInputSeconds != 2.5 {
		t.Fatalf("legacy STT upstream duration 必须保留: %+v", record)
	}
	if record.UsageMissing {
		t.Fatalf("legacy STT 有上游 duration 不得 missing: %+v", record)
	}
	if catalog.inputs[0].AudioInputSeconds == nil || *catalog.inputs[0].AudioInputSeconds != 2.5 {
		t.Fatalf("legacy 计费输入秒数 = %v", catalog.inputs[0].AudioInputSeconds)
	}
}

// TestChainFinalizationLegacySttRequestMediaSecondsDropped：legacy STT 不消费
// 请求音频解析秒数（统一模板新增量），无 token/上游 duration 时按旧口径
// 0 计量 + usage_missing。
func TestChainFinalizationLegacySttRequestMediaSecondsDropped(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	seconds := 2.5
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInput(gatewayproto.ParsedUsage{
		AudioInputSeconds: &seconds,
		QuantitySource:    "request_media",
	}, "/v1/audio/transcriptions"))

	record := recorder.records[0]
	if record.AudioInputSeconds != nil {
		t.Fatalf("legacy STT 不得持久化请求解析秒数: %+v", record)
	}
	if !record.UsageMissing {
		t.Fatalf("legacy STT 无可用计量必须 usage_missing: %+v", record)
	}
	if catalog.inputs[0].AudioInputSeconds != nil {
		t.Fatalf("legacy 计费输入不得含请求解析秒数: %v", catalog.inputs[0].AudioInputSeconds)
	}
}

// TestChainFinalizationLegacyVolcengineParsedSecondsRestoreMissing：legacy
// volcengine 同步 TTS 在无上游 duration 时即使网关解析出秒数也不得进入
// legacy 计费；恢复旧口径 0 计量 + usage_missing，且不落字符。
func TestChainFinalizationLegacyVolcengineParsedSecondsRestoreMissing(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	chars := int64(7)
	seconds := 3.28
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInputFor(gatewayproto.ParsedUsage{
		TtsInputChars:     &chars,
		AudioInputSeconds: &seconds,
		QuantitySource:    "gateway_parse",
	}, "/v1/audio/speech", "volcengine", "seed-audio-1.0"))

	record := recorder.records[0]
	if record.AudioInputSeconds != nil || record.TtsInputChars != nil {
		t.Fatalf("legacy volcengine 解析秒数必须清空且不落字符: %+v", record)
	}
	if !record.UsageMissing {
		t.Fatalf("legacy volcengine 无上游 duration 必须 usage_missing: %+v", record)
	}
	if catalog.inputs[0].AudioInputSeconds != nil || catalog.inputs[0].TtsInputChars != nil {
		t.Fatalf("legacy 计费输入不得含解析秒数/字符: %+v", catalog.inputs[0])
	}
}

// volcengine 同步 TTS 按上游原始时长结算时不得同时消费字符维度（防字符+
// 秒双重计费），字符只留在响应层作为审计事实。
func TestChainFinalizationLegacyVolcengineUpstreamSecondsDropsChars(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	catalog := &fakeChainUsagePricingCatalog{}
	usage := chainFinalizationUsage{recorder: recorder, pricing: catalog, syncPricingAllowed: true}
	chars := int64(7)
	seconds := 3.28
	usage.RecordCompletedUpstreamAttempt(audioMeteringAttemptInputFor(gatewayproto.ParsedUsage{
		TtsInputChars:     &chars,
		AudioInputSeconds: &seconds,
		QuantitySource:    "upstream",
	}, "/v1/audio/speech", "volcengine", "seed-audio-1.0"))

	record := recorder.records[0]
	if record.TtsInputChars != nil {
		t.Fatalf("legacy 秒计 TTS 记录不得保留字符维度: %+v", record)
	}
	if record.AudioInputSeconds == nil || *record.AudioInputSeconds != 3.28 {
		t.Fatalf("legacy 秒计 TTS 必须保留上游秒数: %+v", record)
	}
	input := catalog.inputs[0]
	if input.TtsInputChars != nil {
		t.Fatalf("legacy 秒计 TTS 计费输入不得含字符: %+v", input)
	}
	if input.AudioInputSeconds == nil || *input.AudioInputSeconds != 3.28 {
		t.Fatalf("legacy 秒计 TTS 计费输入秒数 = %v", input.AudioInputSeconds)
	}
}
