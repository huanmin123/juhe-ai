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
			TraceID: "trace_audio_meter", SystemAccountID: "sys", ProviderCode: "gpt", Endpoint: endpoint,
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
