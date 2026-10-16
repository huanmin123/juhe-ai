package gatewaymedia

// volcengine_tts_test.go 是火山豆包音频生成 TTS adapter 的契约单测（契约
// §9.2，2026-10-17 迁移批，/api/v3/tts/create 面）：BuildRequest 公共参数
// 映射（model 真实透传 + doubao-tts 占位归一、input→text_prompt、
// voice→speaker、response_format→audio_config.format、speed→speech_rate、
// provider_options deep-merge）与能力边界（词表外格式 / speed 超区间
// [0.2,3.0] / model/input/voice 缺失）；TransformResponse（audio base64
// 解码透传、载荷缺失/非法 base64/空载荷上抛、content-type 按词表推导）。
// 纯逻辑层，不触 chain/HTTP 执行。
import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestVolcengineSpeechAdapterRegistered(t *testing.T) {
	if SpeechAdapterForProvider("volcengine") == nil {
		t.Fatal("volcengine speech adapter must be registered")
	}
	if SpeechAdapterForProvider(" VolcEngine ") == nil {
		t.Fatal("provider key normalization must be case/space insensitive")
	}
}

func TestVolcengineSpeechAdapterBuildRequestContract(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	speed := 1.5
	path, body, err := adapter.BuildRequest(SpeechRequest{
		Model:          "doubao-tts", // 历史占位名：出站前归一映射为现役模型
		Input:          "你好火山",
		Voice:          "BV700_streaming",
		ResponseFormat: "mp3",
		Speed:          &speed,
		ProviderOptions: map[string]any{
			"volcengine": map[string]any{
				"audio_config": map[string]any{"loudness_rate": 10},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != "/api/v3/tts/create" {
		t.Fatalf("path = %q, want /api/v3/tts/create", path)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	// model 真实透传，占位名归一为现役模型（契约 §9.2）。
	if decoded["model"] != VolcengineTTSCurrentModel {
		t.Fatalf("model = %v, want 占位名归一为 %s", decoded["model"], VolcengineTTSCurrentModel)
	}
	if decoded["text_prompt"] != "你好火山" {
		t.Fatalf("text_prompt = %v", decoded)
	}
	if decoded["speaker"] != "BV700_streaming" {
		t.Fatalf("speaker = %v（公共 voice 映射）", decoded)
	}
	audioConfig, _ := decoded["audio_config"].(map[string]any)
	if audioConfig["format"] != "mp3" {
		t.Fatalf("audio_config = %v", audioConfig)
	}
	if audioConfig["speech_rate"] != 1.5 {
		t.Fatalf("speech_rate = %v（公共 speed 映射）", audioConfig)
	}
	// provider_options.volcengine 命中子对象 deep-merge（loudness_rate 补进 audio_config）。
	if audioConfig["loudness_rate"] != float64(10) {
		t.Fatalf("provider_options volcengine 子对象未 deep-merge: %s", body)
	}
	// 旧版报文骨架不得残留（user/req_params/reqid/operation 随旧端点废弃）。
	for _, legacy := range []string{"user", "req_params", "reqid", "operation"} {
		if _, present := decoded[legacy]; present {
			t.Fatalf("legacy key %q must not ride the volcengine body: %s", legacy, body)
		}
	}
}

func TestVolcengineSpeechAdapterModelPassthrough(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	// 真实模型名直传，不做归一改写。
	path, body, err := adapter.BuildRequest(SpeechRequest{Model: "seed-audio-1.0", Input: "hi", Voice: "v"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != "/api/v3/tts/create" {
		t.Fatalf("path = %q", path)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if decoded["model"] != "seed-audio-1.0" {
		t.Fatalf("model = %v, want 直传", decoded["model"])
	}
	// speaker 可选语义由上游裁决，但统一面 OpenAI 语义 voice 必填。
	if _, _, err := adapter.BuildRequest(SpeechRequest{Model: "seed-audio-1.0", Input: "hi"}); err == nil || !strings.Contains(err.Error(), "voice") {
		t.Fatalf("missing voice must fail explicitly, got %v", err)
	}
}

func TestVolcengineSpeechAdapterBuildRequestBoundaries(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	base := SpeechRequest{
		Model: "seed-audio-1.0",
		Input: "hi",
		Voice: "BV700_streaming",
	}
	// response_format 缺省 = OpenAI 默认 mp3 语义（合法）；实测词表四值全过。
	for _, format := range []string{"", "mp3", "wav", "ogg_opus", "opus"} {
		inList := base
		inList.ResponseFormat = format
		if _, _, err := adapter.BuildRequest(inList); err != nil {
			t.Fatalf("format %q must pass (实测词表), got %v", format, err)
		}
	}
	// 词表外格式（pcm 实测上游挂起不采纳；aac/flac 不在火山请求面）→ 400 语义（零转码）。
	for _, format := range []string{"pcm", "aac", "flac"} {
		outOfList := base
		outOfList.ResponseFormat = format
		if _, _, err := adapter.BuildRequest(outOfList); err == nil || !strings.Contains(err.Error(), "response_format") {
			t.Fatalf("format %s must be rejected (零转码), got %v", format, err)
		}
	}
	// speed 超声明区间 [0.2, 3.0] → 拒绝（公共区间差集不静默裁剪）。
	for _, speed := range []float64{0.1, 3.1, 4.0} {
		outOfRange := base
		value := speed
		outOfRange.Speed = &value
		_, _, err := adapter.BuildRequest(outOfRange)
		if err == nil || !strings.Contains(err.Error(), "0.2") {
			t.Fatalf("speed=%v must be rejected with the vendor range, got %v", speed, err)
		}
	}
	if _, _, err := adapter.BuildRequest(SpeechRequest{Input: "hi", Voice: "v"}); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("missing model must fail explicitly, got %v", err)
	}
	if _, _, err := adapter.BuildRequest(SpeechRequest{Model: "seed-audio-1.0", Voice: "v"}); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("missing input must fail explicitly, got %v", err)
	}
}

func TestVolcengineSpeechAdapterTransformResponse(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	request := SpeechRequest{Model: "seed-audio-1.0", Input: "hi", Voice: "v", ResponseFormat: "wav"}

	t.Run("audio base64 解码透传", func(t *testing.T) {
		payload := []byte("RIFFmock-wav-payload")
		body, _ := json.Marshal(map[string]any{
			"audio":             base64.StdEncoding.EncodeToString(payload),
			"duration":          3.28,
			"original_duration": 3.28,
		})
		audio, contentType, err := adapter.TransformResponse(body, request)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if string(audio) != string(payload) {
			t.Fatalf("audio = %q, want decoded payload %q", audio, payload)
		}
		if contentType != "audio/wav" {
			t.Fatalf("contentType = %q, want audio/wav（按请求 response_format 推导）", contentType)
		}
	})

	t.Run("缺 audio 载荷", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"duration": 1.0, "url": "https://example.invalid/x"})
		if _, _, err := adapter.TransformResponse(body, request); err == nil || !strings.Contains(err.Error(), "audio") {
			t.Fatalf("missing audio must fail, got %v", err)
		}
	})

	t.Run("非法 base64", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"audio": "!!!not-base64!!!"})
		if _, _, err := adapter.TransformResponse(body, request); err == nil || !strings.Contains(err.Error(), "base64") {
			t.Fatalf("invalid base64 must fail, got %v", err)
		}
	})

	t.Run("opus 按 OggS 容器推导 audio/ogg", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"audio": base64.StdEncoding.EncodeToString([]byte("OggS-mock")),
		})
		_, contentType, err := adapter.TransformResponse(body, SpeechRequest{Model: "seed-audio-1.0", Input: "i", Voice: "v", ResponseFormat: "opus"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if contentType != "audio/ogg" {
			t.Fatalf("contentType = %q, want audio/ogg（opus 上游返回 OggS 容器）", contentType)
		}
	})

	t.Run("缺省 format 按 mp3 推导", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"audio": base64.StdEncoding.EncodeToString([]byte{0xFF, 0xFB, 0x00}),
		})
		_, contentType, err := adapter.TransformResponse(body, SpeechRequest{Model: "seed-audio-1.0", Input: "i", Voice: "v"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if contentType != "audio/mpeg" {
			t.Fatalf("contentType = %q, want audio/mpeg（OpenAI 默认 mp3 语义）", contentType)
		}
	})
}

func TestSpeechReportedDurationSecondsFromJSON(t *testing.T) {
	// 维度修正批（契约 §9.2/§2.8）：顶层 duration 正值是唯一事实来源。
	if seconds, ok := SpeechReportedDurationSecondsFromJSON(map[string]any{"duration": 3.28}); !ok || seconds != 3.28 {
		t.Fatalf("duration = %v ok=%v want 3.28 true", seconds, ok)
	}
	for name, root := range map[string]any{
		"missing":    map[string]any{"audio": "x"},
		"zero":       map[string]any{"duration": 0.0},
		"negative":   map[string]any{"duration": -1.5},
		"non-number": map[string]any{"duration": "3.28"},
		"non-object": "not-an-object",
		"nil":        nil,
	} {
		if _, ok := SpeechReportedDurationSecondsFromJSON(root); ok {
			t.Fatalf("%s: ok must be false", name)
		}
	}
}
