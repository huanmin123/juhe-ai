package gatewayresponse

// M1 同步音频非流式管道测试：STT 计量三来源（usage token 优先 / duration
// 秒 / 双缺 usage_missing，契约 §2.8）、TTS 字符计量注入，以及 gemini
// speech 响应的 inlineData → PCM 转换（契约 §5.1）。
import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// newAudioInputFixture 构造带请求路径与请求 body 的非流式输入。
func newAudioInputFixture(t *testing.T, method, path string, requestBody []byte, body UpstreamBody, status int, header map[string]string, protocolCode string) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	t.Helper()
	input, recorder := newInputFixture(body, status, header)
	httpReq := httptest.NewRequest(method, path, strings.NewReader(string(requestBody)))
	input.Req = gatewaypreauth.NewGatewayRequest(httpReq)
	if requestBody != nil {
		input.Req.Body = &gatewaybody.Request{RawBody: requestBody}
	}
	if protocolCode != "" && protocolCode != "openai" {
		secret := gatewayAccountFixture
		secret.ProtocolCode = protocolCode
		input.Account = OpenAIAccountView{Account: secret}
	}
	return input, recorder
}

func TestNonStreamSttMeteringDurationSeconds(t *testing.T) {
	// verbose_json 无 usage：duration 落 AudioInputSeconds（whisper 系口径）。
	input, _ := newAudioInputFixture(t, "POST", "/v1/audio/transcriptions", nil,
		NewSliceUpstreamBody([]byte(`{"text":"MOCK-TRANSCRIPT: hello","language":"en","duration":2.5,"segments":[]}`)),
		200, map[string]string{"Content-Type": "application/json"}, "openai")
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Usage.AudioInputSeconds == nil || *result.Usage.AudioInputSeconds != 2.5 {
		t.Fatalf("AudioInputSeconds = %v", result.Usage.AudioInputSeconds)
	}
	if result.Usage.UsageMissing {
		t.Fatalf("duration evidence present, usage_missing must stay false: %+v", result.Usage)
	}
}

func TestNonStreamSttMeteringUsageTokensWin(t *testing.T) {
	// gpt-4o-transcribe 系：usage token 优先，既有 token 字段承载。
	input, _ := newAudioInputFixture(t, "POST", "/v1/audio/transcriptions", nil,
		NewSliceUpstreamBody([]byte(`{"text":"hi","language":"en","duration":9.9,"usage":{"input_tokens":120,"output_tokens":32}}`)),
		200, map[string]string{"Content-Type": "application/json"}, "openai")
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 120 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 32 {
		t.Fatalf("usage tokens = %+v", result.Usage)
	}
	if result.Usage.AudioInputSeconds != nil {
		t.Fatalf("token evidence present, seconds must stay nil: %+v", result.Usage)
	}
}

func TestNonStreamSttMeteringMissingMarked(t *testing.T) {
	// json 默认格式：无 usage 无 duration → 计量 0 + usage_missing。
	input, _ := newAudioInputFixture(t, "POST", "/v1/audio/translations", nil,
		NewSliceUpstreamBody([]byte(`{"text":"MOCK-TRANSCRIPT"}`)),
		200, map[string]string{"Content-Type": "application/json"}, "openai")
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Usage.UsageMissing {
		t.Fatalf("usage_missing must be marked: %+v", result.Usage)
	}
	if result.Usage.AudioInputSeconds != nil {
		t.Fatalf("no evidence, seconds must stay nil: %+v", result.Usage)
	}
}

func TestNonStreamTtsMeteringInputChars(t *testing.T) {
	// openai 直连 TTS：二进制透传 + 网关自算字符计量（UTF-8 rune 数）。
	requestBody := []byte(`{"model":"tts-1","input":"abc你好","voice":"alloy","response_format":"wav"}`)
	input, recorder := newAudioInputFixture(t, "POST", "/v1/audio/speech", requestBody,
		NewSliceUpstreamBody([]byte("RIFFxxxxWAVE")), 200,
		map[string]string{"Content-Type": "audio/wav"}, "openai")
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Usage.TtsInputChars == nil || *result.Usage.TtsInputChars != 5 {
		t.Fatalf("TtsInputChars = %v want 5 (rune count)", result.Usage.TtsInputChars)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "audio/") {
		t.Fatalf("binary passthrough content-type = %q", recorder.Header().Get("Content-Type"))
	}
	if recorder.Body.String() != "RIFFxxxxWAVE" {
		t.Fatalf("client body must be the raw audio bytes, got %q", recorder.Body.String())
	}
}

func TestNonStreamGeminiSpeechTransform(t *testing.T) {
	// gemini 账户 + speech 请求：上游 JSON inlineData → 下游 PCM 字节 +
	// audio/L16;rate=24000（契约 §5.1）。
	pcm := []byte{0x00, 0x01, 0x02, 0x03}
	payload, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role": "model",
				"parts": []any{map[string]any{
					"inlineData": map[string]any{
						"mimeType": "audio/L16;rate=24000",
						"data":     base64.StdEncoding.EncodeToString(pcm),
					},
				}},
			},
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 12, "candidatesTokenCount": 240, "totalTokenCount": 252},
	})
	requestBody := []byte(`{"model":"gemini-2.5-flash-preview-tts","input":"你好 world","voice":"Kore","response_format":"pcm"}`)
	input, recorder := newAudioInputFixture(t, "POST", "/v1/audio/speech", requestBody,
		NewSliceUpstreamBody(payload), 200,
		map[string]string{"Content-Type": "application/json"}, "gemini")
	// 真实链路 input.Driver 为空，由账户协议分派 gemini driver；fixture 显式
	// 注入同一 driver（usageMetadata 抽取走 gemini 语义）。
	input.Driver = NewGeminiResponseDriver()
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Code != 200 {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != string(pcm) {
		t.Fatalf("client body must be raw PCM, got %q", recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "audio/L16;rate=24000" {
		t.Fatalf("content-type = %q", contentType)
	}
	// gemini usageMetadata token 计量沿既有链 + TTS 字符计量注入。
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 12 {
		t.Fatalf("gemini usage tokens = %+v", result.Usage)
	}
	// "你好 world" = 8 个 rune（2 CJK + 空格 + 5 ASCII）。
	if result.Usage.TtsInputChars == nil || *result.Usage.TtsInputChars != 8 {
		t.Fatalf("TtsInputChars = %v want 8", result.Usage.TtsInputChars)
	}
}

func TestNonStreamGeminiSpeechTransformFailure502(t *testing.T) {
	// 上游 2xx 但无 inlineData：502 协议失败，未转换 JSON 不透传。
	requestBody := []byte(`{"model":"gemini-2.5-flash-preview-tts","input":"x","voice":"Kore","response_format":"pcm"}`)
	input, recorder := newAudioInputFixture(t, "POST", "/v1/audio/speech", requestBody,
		NewSliceUpstreamBody([]byte(`{"candidates":[{"content":{"parts":[{"text":"no audio"}]}}]}`)),
		200, map[string]string{"Content-Type": "application/json"}, "gemini")
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	_, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d want 502", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "candidates") {
		t.Fatalf("untransformed upstream JSON must not reach the client: %q", recorder.Body.String())
	}
}

func TestMediaSpeechTransformPlanGates(t *testing.T) {
	// 非 gemini 协议或非 speech 路径恒不激活（现有透传行为零改动）。
	openAIInput, _ := newInputFixture(NewSliceUpstreamBody([]byte(`{"data":[]}`)), 200,
		map[string]string{"Content-Type": "application/json"})
	openAIInput.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/audio/speech", nil))
	if mediaSpeechTransformPlan(openAIInput).active {
		t.Fatal("openai account must not activate the gemini speech transform")
	}
	geminiSecret := gatewayAccountFixture
	geminiSecret.ProtocolCode = "gemini"
	chatInput, _ := newInputFixture(NewSliceUpstreamBody([]byte(`{}`)), 200,
		map[string]string{"Content-Type": "application/json"})
	chatInput.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	chatInput.Account = OpenAIAccountView{Account: geminiSecret}
	if mediaSpeechTransformPlan(chatInput).active {
		t.Fatal("non-speech path must not activate the transform")
	}
	errorInput, _ := newInputFixture(NewSliceUpstreamBody([]byte(`{"error":{}}`)), 400,
		map[string]string{"Content-Type": "application/json"})
	errorInput.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/audio/speech", nil))
	errorInput.Account = OpenAIAccountView{Account: geminiSecret}
	if mediaSpeechTransformPlan(errorInput).active {
		t.Fatal("non-2xx must not activate the transform")
	}
	_ = gatewayruntimecache.GatewaySettings{}
}
