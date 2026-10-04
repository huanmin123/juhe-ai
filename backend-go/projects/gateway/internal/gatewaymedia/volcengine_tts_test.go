package gatewaymedia

// volcengine_tts_test.go 是 M6 火山豆包 TTS adapter 的契约单测（契约 §9.2）：
// BuildRequest 公共参数映射（input→req_params.text、voice→speaker、
// response_format→audio_params.format、speed→speed_ratio、reqid=uuid、
// operation=query、user.uid=speech_appid 固定值、provider_options deep-merge）
// 与能力边界（词表外格式 / speed 超区间 [0.2,3.0] / speech_appid 缺失）；
// TransformResponse（code 3000 + data base64 解码透传、code 3001 错误上抛、
// 非法 base64 / 空载荷）。纯逻辑层，不触 chain/HTTP 执行。
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
		Model:          "doubao-tts", // 统一面 model 必填占位：V3 请求面无 model 字段，值不透传
		Input:          "你好火山",
		Voice:          "BV700_streaming",
		ResponseFormat: "mp3",
		Speed:          &speed,
		AccountVendorRefs: map[string]string{
			"speech_appid": "app-123456",
		},
		ProviderOptions: map[string]any{
			"volcengine": map[string]any{
				"req_params": map[string]any{"emotion": "happy"},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != "/api/v3/tts" {
		t.Fatalf("path = %q, want /api/v3/tts", path)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	user, _ := decoded["user"].(map[string]any)
	if user["uid"] != "app-123456" {
		t.Fatalf("user.uid = %v, want speech_appid 固定值 app-123456", user)
	}
	reqParams, _ := decoded["req_params"].(map[string]any)
	if reqParams["text"] != "你好火山" {
		t.Fatalf("req_params.text = %v", reqParams)
	}
	if reqParams["speaker"] != "BV700_streaming" {
		t.Fatalf("req_params.speaker = %v（公共 voice 映射）", reqParams)
	}
	audioParams, _ := reqParams["audio_params"].(map[string]any)
	if audioParams["format"] != "mp3" || audioParams["sample_rate"] != float64(24000) {
		t.Fatalf("audio_params = %v", audioParams)
	}
	if audioParams["speed_ratio"] != 1.5 {
		t.Fatalf("speed_ratio = %v（公共 speed 映射）", audioParams)
	}
	if decoded["operation"] != "query" {
		t.Fatalf("operation = %v, want query", decoded)
	}
	// reqid 网关生成（uuid v4 形状：8-4-4-4-12 hex，版本位 4）。
	reqid, _ := decoded["reqid"].(string)
	if len(reqid) != 36 || reqid[14] != '4' {
		t.Fatalf("reqid = %q, want uuid v4 shape", reqid)
	}
	// model 不透传上游（V3 请求面无 model 字段）。
	if _, present := decoded["model"]; present {
		t.Fatalf("model must not ride the volcengine body: %s", body)
	}
	// provider_options.volcengine 命中子对象 deep-merge（emotion 补进 req_params）。
	if reqParams["emotion"] != "happy" {
		t.Fatalf("provider_options volcengine 子对象未 deep-merge: %s", body)
	}
}

func TestVolcengineSpeechAdapterBuildRequestBoundaries(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	base := SpeechRequest{
		Model: "doubao-tts",
		Input: "hi",
		Voice: "BV700_streaming",
		AccountVendorRefs: map[string]string{
			"speech_appid": "app-123456",
		},
	}
	// response_format 缺省 = OpenAI 默认 mp3 语义（合法）。
	if _, _, err := adapter.BuildRequest(base); err != nil {
		t.Fatalf("default mp3 must pass: %v", err)
	}
	// 词表外格式（opus/aac/flac）→ 400 语义（零转码）。
	for _, format := range []string{"opus", "aac", "flac"} {
		outOfList := base
		outOfList.ResponseFormat = format
		if _, _, err := adapter.BuildRequest(outOfList); err == nil || !strings.Contains(err.Error(), "response_format") {
			t.Fatalf("format %s must be rejected (零转码), got %v", format, err)
		}
	}
	// speed 超厂商区间 [0.2, 3.0] → 拒绝（公共区间差集不静默裁剪）。
	for _, speed := range []float64{0.1, 3.1, 4.0} {
		outOfRange := base
		value := speed
		outOfRange.Speed = &value
		_, _, err := adapter.BuildRequest(outOfRange)
		if err == nil || !strings.Contains(err.Error(), "0.2") {
			t.Fatalf("speed=%v must be rejected with the vendor range, got %v", speed, err)
		}
	}
	// speech_appid 缺失（链上凭据未注入）→ 构造失败（防御位，链上先行校验）。
	noRefs := SpeechRequest{Model: "doubao-tts", Input: "hi", Voice: "BV700_streaming"}
	if _, _, err := adapter.BuildRequest(noRefs); err == nil || !strings.Contains(err.Error(), "speech_appid") {
		t.Fatalf("missing speech_appid must fail explicitly, got %v", err)
	}
}

func TestVolcengineSpeechAdapterTransformResponse(t *testing.T) {
	adapter := SpeechAdapterForProvider("volcengine")
	request := SpeechRequest{Model: "doubao-tts", Input: "hi", Voice: "BV700_streaming", ResponseFormat: "wav"}

	t.Run("code3000 base64 解码透传", func(t *testing.T) {
		payload := []byte("RIFFmock-wav-payload")
		body, _ := json.Marshal(map[string]any{
			"reqid":   "mock-1",
			"code":    3000,
			"message": "success",
			"data":    base64.StdEncoding.EncodeToString(payload),
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

	t.Run("code3001 错误上抛", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"reqid": "mock-2", "code": 3001, "message": "synthesis failed", "data": "",
		})
		_, _, err := adapter.TransformResponse(body, request)
		if err == nil || !strings.Contains(err.Error(), "3001") || !strings.Contains(err.Error(), "synthesis failed") {
			t.Fatalf("code 3001 must surface code+message, got %v", err)
		}
	})

	t.Run("缺 data 载荷", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"reqid": "mock-3", "code": 3000, "message": "success"})
		if _, _, err := adapter.TransformResponse(body, request); err == nil || !strings.Contains(err.Error(), "data") {
			t.Fatalf("missing data must fail, got %v", err)
		}
	})

	t.Run("缺省 format 按 mp3 推导", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"reqid": "mock-4", "code": 3000, "message": "success",
			"data": base64.StdEncoding.EncodeToString([]byte{0xFF, 0xFB, 0x00}),
		})
		_, contentType, err := adapter.TransformResponse(body, SpeechRequest{Model: "m", Input: "i", Voice: "v"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if contentType != "audio/mpeg" {
			t.Fatalf("contentType = %q, want audio/mpeg（OpenAI 默认 mp3 语义）", contentType)
		}
	})
}
