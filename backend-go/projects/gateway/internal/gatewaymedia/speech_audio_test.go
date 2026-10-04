package gatewaymedia

// M1 同步音频测试：SpeechIR 解析、网关侧计量抽取（契约 §2.8）、Gemini
// TTS adapter 请求构造 / pcm 裁决 / inlineData 响应转换（契约 §5.1，置信度
// A 报文）。adapter 响应面经 httptest 假上游回放 §5.1 报文。
import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSpeechRequestMinimalAndValidation(t *testing.T) {
	ir, err := ParseSpeechRequest(map[string]any{
		"model":           "gemini-2.5-flash-preview-tts",
		"input":           "Say cheerfully: 你好 world",
		"voice":           "Kore",
		"response_format": "pcm",
		"speed":           1.5,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ir.Model != "gemini-2.5-flash-preview-tts" || ir.Voice != "Kore" || ir.ResponseFormat != "pcm" {
		t.Fatalf("ir = %+v", ir)
	}
	if ir.Speed == nil || *ir.Speed != 1.5 {
		t.Fatalf("speed = %v", ir.Speed)
	}
	// UTF-8 rune 数（非字节数）：5 个 ASCII 词字符 + 空格 + 2 个 CJK。
	if ir.InputChars() != int64(len([]rune("Say cheerfully: 你好 world"))) {
		t.Fatalf("InputChars = %d", ir.InputChars())
	}

	if _, err := ParseSpeechRequest(map[string]any{"model": "m"}); err == nil ||
		!strings.Contains(err.Error(), "input") {
		t.Fatalf("missing input must fail: %v", err)
	}
	if _, err := ParseSpeechRequest(map[string]any{"model": "m", "input": "x"}); err == nil ||
		!strings.Contains(err.Error(), "voice") {
		t.Fatalf("missing voice must fail: %v", err)
	}
	if _, err := ParseSpeechRequest(nil); err == nil {
		t.Fatal("nil body must fail")
	}
}

func TestSpeechMeteringPaths(t *testing.T) {
	cases := []struct {
		path     string
		speech   bool
		transcri bool
	}{
		{"/v1/audio/speech", true, false},
		{"/audio/speech", true, false},
		{"/v1/audio/transcriptions", false, true},
		{"/v1/audio/translations?foo=1", false, true},
		{"/v1/chat/completions", false, false},
	}
	for _, c := range cases {
		if got := IsSpeechPath(c.path); got != c.speech {
			t.Fatalf("IsSpeechPath(%q) = %v", c.path, got)
		}
		if got := IsTranscriptionPath(c.path); got != c.transcri {
			t.Fatalf("IsTranscriptionPath(%q) = %v", c.path, got)
		}
	}
}

func TestSpeechInputCharsFromBody(t *testing.T) {
	if got := SpeechInputCharsFromBody(map[string]any{"input": "abc你好"}); got != 5 {
		t.Fatalf("chars = %d want 5", got)
	}
	if got := SpeechInputCharsFromBody(nil); got != 0 {
		t.Fatalf("nil body chars = %d", got)
	}
	if got := SpeechInputCharsFromBody(map[string]any{}); got != 0 {
		t.Fatalf("missing input chars = %d", got)
	}
}

func TestSttDurationSeconds(t *testing.T) {
	if seconds, ok := SttDurationSeconds(map[string]any{"duration": 2.5}); !ok || seconds != 2.5 {
		t.Fatalf("duration = %v %v", seconds, ok)
	}
	if _, ok := SttDurationSeconds(map[string]any{"text": "x"}); ok {
		t.Fatal("missing duration must report !ok")
	}
	if _, ok := SttDurationSeconds(map[string]any{"duration": "2.5"}); ok {
		t.Fatal("non-number duration must report !ok")
	}
	if _, ok := SttDurationSeconds(map[string]any{"duration": -1.0}); ok {
		t.Fatal("negative duration must report !ok")
	}
}

func TestSpeechAdapterForProvider(t *testing.T) {
	if SpeechAdapterForProvider("gemini") == nil {
		t.Fatal("gemini adapter must be registered")
	}
	if SpeechAdapterForProvider("Gemini ") == nil {
		t.Fatal("provider key normalization must be case/space insensitive")
	}
	if SpeechAdapterForProvider("openai") != nil {
		t.Fatal("openai rides the direct passthrough, no adapter expected")
	}
}

func TestGeminiSpeechAdapterBuildRequestContract(t *testing.T) {
	adapter := SpeechAdapterForProvider("gemini")
	path, body, err := adapter.BuildRequest(SpeechRequest{
		Model: "gemini-2.5-flash-preview-tts",
		Input: "Say cheerfully: have a wonderful day",
		Voice: "Kore",
		// response_format 缺省 = OpenAI 默认 mp3 语义 → gemini 400（零转码）。
		ResponseFormat: "pcm",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("path = %q", path)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	contents, _ := decoded["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %v", decoded["contents"])
	}
	content, _ := contents[0].(map[string]any)
	parts, _ := content["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["text"] != "Say cheerfully: have a wonderful day" {
		t.Fatalf("text part = %v", part)
	}
	generationConfig, _ := decoded["generationConfig"].(map[string]any)
	if modalities, _ := generationConfig["responseModalities"].([]any); len(modalities) != 1 || modalities[0] != "AUDIO" {
		t.Fatalf("responseModalities = %v", generationConfig["responseModalities"])
	}
	speechConfig, _ := generationConfig["speechConfig"].(map[string]any)
	prebuilt, _ := speechConfig["prebuiltVoiceConfig"].(map[string]any)
	if prebuilt["voiceName"] != "Kore" {
		t.Fatalf("voiceName = %v", prebuilt)
	}
	// speed 无对应字段：不换算、不猜测（body 中不出现）。
	if _, present := generationConfig["speechSpeed"]; present {
		t.Fatal("speed must not be mapped")
	}
}

func TestGeminiSpeechAdapterBuildRequestFormatGate(t *testing.T) {
	adapter := SpeechAdapterForProvider("gemini")
	for _, format := range []string{"mp3", "wav", ""} {
		_, _, err := adapter.BuildRequest(SpeechRequest{
			Model: "gemini-2.5-flash-preview-tts", Input: "x", Voice: "Kore",
			ResponseFormat: format,
		})
		if err == nil {
			t.Fatalf("format %q must be rejected (pcm-only, zero transcode)", format)
		}
		if !strings.Contains(err.Error(), "pcm") {
			t.Fatalf("error must name the pcm boundary: %v", err)
		}
	}
	if _, _, err := adapter.BuildRequest(SpeechRequest{Model: "m", Input: "x", Voice: "Kore", ResponseFormat: "pcm"}); err != nil {
		t.Fatalf("pcm must pass: %v", err)
	}
}

// TestGeminiSpeechAdapterTransformViaHTTPReplay 用 httptest 假上游回放契约
// §5.1 的 media_gemini_tts_ok 报文（inlineData base64 PCM + usageMetadata），
// 校验转换出的下游载荷与 content-type。
func TestGeminiSpeechAdapterTransformViaHTTPReplay(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x01, 0x00}, 48) // 96 字节静音帧形态
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("upstream call = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		payload := map[string]any{
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
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	response, err := http.Post(server.URL+"/v1beta/models/gemini-2.5-flash-preview-tts:generateContent", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)

	adapter := SpeechAdapterForProvider("gemini")
	audio, contentType, transformErr := adapter.TransformResponse(raw, SpeechRequest{})
	if transformErr != nil {
		t.Fatalf("transform: %v", transformErr)
	}
	if !bytes.Equal(audio, pcm) {
		t.Fatalf("audio bytes mismatch: %d bytes", len(audio))
	}
	if contentType != "audio/L16;rate=24000" {
		t.Fatalf("contentType = %q", contentType)
	}
}

func TestGeminiSpeechAdapterTransformFailures(t *testing.T) {
	adapter := SpeechAdapterForProvider("gemini")
	if _, _, err := adapter.TransformResponse([]byte(`not-json`), SpeechRequest{}); err == nil {
		t.Fatal("invalid json must fail")
	}
	if _, _, err := adapter.TransformResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"no audio"}]}}]}`), SpeechRequest{}); err == nil {
		t.Fatal("missing inlineData must fail")
	}
	if _, _, err := adapter.TransformResponse([]byte(`{"error":{"code":400,"message":"bad voice","status":"INVALID_ARGUMENT"}}`), SpeechRequest{}); err == nil ||
		!strings.Contains(err.Error(), "bad voice") {
		t.Fatalf("upstream error must surface: %v", err)
	}
	if _, _, err := adapter.TransformResponse([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"!!!"}}]}}]}`), SpeechRequest{}); err == nil {
		t.Fatal("invalid base64 must fail")
	}
}

func TestIsTokenMeteredSpeechModel(t *testing.T) {
	// token 口径 TTS 词表（口径文档 §2.5）：gpt-4o-mini-tts / gpt-4o-tts 系。
	for _, model := range []string{
		"gpt-4o-mini-tts", "gpt-4o-mini-tts-preview", "gpt-4o-tts",
	} {
		if !IsTokenMeteredSpeechModel(model) {
			t.Fatalf("%s must classify as token metered", model)
		}
	}
	// 字符口径 TTS、STT 系与 gemini TTS 不命中。
	for _, model := range []string{
		"tts-1", "tts-1-hd", "gpt-4o-transcribe", "gpt-4o-mini-transcribe",
		"whisper-1", "gemini-2.5-flash-preview-tts", "",
	} {
		if IsTokenMeteredSpeechModel(model) {
			t.Fatalf("%s must not classify as token metered", model)
		}
	}
}
