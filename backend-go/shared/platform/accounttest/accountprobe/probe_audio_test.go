package accountprobe

// M1 同步音频探针测试（音频设计 §11.9）：speech payload 报文、STT
// multipart（内置小 WAV 样本结构断言）与两类判活证据。
import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestModeAudioVocabulary(t *testing.T) {
	if !ModeAudioSpeech.audio() || !ModeAudioTranscriptionJSON.audio() {
		t.Fatal("audio modes must classify as audio")
	}
	if ModeAudioSpeech.streaming() || ModeAudioTranscriptionJSON.streaming() {
		t.Fatal("audio probes are non-streaming")
	}
	if ModeChatJSON.audio() {
		t.Fatal("chat mode must not classify as audio")
	}
}

func TestBuildSpeechPayloadContract(t *testing.T) {
	body, err := buildSpeechPayload("tts-1", "alloy")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if decoded["model"] != "tts-1" || decoded["input"] != "ok" ||
		decoded["voice"] != "alloy" || decoded["response_format"] != "wav" {
		t.Fatalf("speech payload = %v", decoded)
	}
}

func TestBuildTestRequestAudioModes(t *testing.T) {
	view := &View{Type: "api_key"}
	speech, err := buildTestRequest(view, ModeAudioSpeech, "tts-1", OutputChallenge{})
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	if speech.path != "/v1/audio/speech" || speech.model != "tts-1" {
		t.Fatalf("speech request = %+v", speech)
	}
	if contentType := speech.headers["content-type"]; contentType != "application/json" {
		t.Fatalf("speech content-type = %q", contentType)
	}

	stt, err := buildTestRequest(view, ModeAudioTranscriptionJSON, "whisper-1", OutputChallenge{})
	if err != nil {
		t.Fatalf("stt: %v", err)
	}
	if stt.path != "/v1/audio/transcriptions" {
		t.Fatalf("stt path = %q", stt.path)
	}
	contentType := stt.headers["content-type"]
	if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
		t.Fatalf("stt content-type = %q", contentType)
	}
	boundary := strings.TrimPrefix(contentType, "multipart/form-data; boundary=")
	if !bytes.Contains(stt.body, []byte("--"+boundary)) {
		t.Fatalf("body missing multipart boundary")
	}
	if !bytes.Contains(stt.body, []byte(`name="model"`)) || !bytes.Contains(stt.body, []byte("whisper-1")) {
		t.Fatalf("multipart missing model field: %q", stt.body)
	}
	if !bytes.Contains(stt.body, []byte(`name="file"; filename="probe.wav"`)) {
		t.Fatalf("multipart missing file field: %q", stt.body)
	}
}

func TestProbeWAVPayloadStructure(t *testing.T) {
	wav := probeWAVPayload()
	if len(wav) < 44 {
		t.Fatalf("wav too short: %d bytes", len(wav))
	}
	if !bytes.Equal(wav[0:4], []byte("RIFF")) || !bytes.Equal(wav[8:12], []byte("WAVE")) ||
		!bytes.Equal(wav[12:16], []byte("fmt ")) || !bytes.Equal(wav[36:40], []byte("data")) {
		t.Fatalf("wav headers invalid: % x", wav[:44])
	}
	riffSize := uint32(wav[4]) | uint32(wav[5])<<8 | uint32(wav[6])<<16 | uint32(wav[7])<<24
	if int(riffSize) != len(wav)-8 {
		t.Fatalf("riff size = %d, file = %d", riffSize, len(wav))
	}
	dataSize := uint32(wav[40]) | uint32(wav[41])<<8 | uint32(wav[42])<<16 | uint32(wav[43])<<24
	if int(dataSize) != len(wav)-44 {
		t.Fatalf("data size = %d, payload = %d", dataSize, len(wav)-44)
	}
}

func TestHasAudioProbeSuccessEvidence(t *testing.T) {
	// speech（openai）：2xx content-type audio/* 判活。
	if !hasAudioProbeSuccessEvidence(ModeAudioSpeech, ProtocolOpenAI, responseContext{}, map[string]string{"content-type": "audio/wav"}) {
		t.Fatal("audio/wav must pass the speech envelope")
	}
	if hasAudioProbeSuccessEvidence(ModeAudioSpeech, ProtocolOpenAI, responseContext{}, map[string]string{"content-type": "application/json"}) {
		t.Fatal("json content-type must fail the speech envelope")
	}
	// STT：JSON 信封含 text 字段即判活（静音样本真实上游可能返回空 text）。
	record := map[string]any{"text": "MOCK-TRANSCRIPT"}
	if !hasAudioProbeSuccessEvidence(ModeAudioTranscriptionJSON, ProtocolOpenAI, responseContext{record: record}, nil) {
		t.Fatal("non-empty text must pass the stt envelope")
	}
	if !hasAudioProbeSuccessEvidence(ModeAudioTranscriptionJSON, ProtocolOpenAI, responseContext{record: map[string]any{"text": ""}}, nil) {
		t.Fatal("empty text field must pass the stt envelope (silence sample)")
	}
	if hasAudioProbeSuccessEvidence(ModeAudioTranscriptionJSON, ProtocolOpenAI, responseContext{record: map[string]any{"duration": 1.5}}, nil) {
		t.Fatal("missing text field must fail")
	}
	if hasAudioProbeSuccessEvidence(ModeAudioTranscriptionJSON, ProtocolOpenAI, responseContext{}, nil) {
		t.Fatal("missing record must fail")
	}
	// speech（gemini）：candidates[].content.parts[].inlineData 判活（§5.1）。
	geminiOK := parseResponseContext(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"cGNt"}}]}}]}`)
	if !hasAudioProbeSuccessEvidence(ModeAudioSpeech, ProtocolGemini, geminiOK, map[string]string{"content-type": "application/json"}) {
		t.Fatal("gemini inlineData must pass the speech envelope")
	}
	geminiTextOnly := parseResponseContext(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`)
	if hasAudioProbeSuccessEvidence(ModeAudioSpeech, ProtocolGemini, geminiTextOnly, map[string]string{"content-type": "application/json"}) {
		t.Fatal("gemini text-only response must fail the speech envelope")
	}
}

func TestClassifyResponseAudioModes(t *testing.T) {
	view := &View{Type: "api_key", ProtocolCode: "openai"}
	// speech 成功：2xx + audio/*。
	observation := classifyResponse(view, ProtocolOpenAI, ModeAudioSpeech, "",
		map[string]string{"content-type": "audio/wav"}, http.StatusOK, 10, 20, OutputChallenge{}, false)
	if !observation.Result.Success {
		t.Fatalf("speech success misclassified: %+v", observation.Result)
	}
	// speech 失败：2xx 但 json（错误体）。
	observation = classifyResponse(view, ProtocolOpenAI, ModeAudioSpeech,
		`{"error":{"message":"Invalid voice"}}`, map[string]string{"content-type": "application/json"},
		http.StatusBadRequest, 10, 20, OutputChallenge{}, false)
	if observation.Result.Success {
		t.Fatalf("speech error misclassified: %+v", observation.Result)
	}
	if !strings.Contains(observation.Result.Message, "Audio API") {
		t.Fatalf("failure message = %q", observation.Result.Message)
	}
	// STT 成功：2xx + JSON text。
	observation = classifyResponse(view, ProtocolOpenAI, ModeAudioTranscriptionJSON,
		`{"text":"hello"}`, map[string]string{"content-type": "application/json"},
		http.StatusOK, 10, 20, OutputChallenge{}, false)
	if !observation.Result.Success {
		t.Fatalf("stt success misclassified: %+v", observation.Result)
	}
}

func TestEndpointModeOrdersIncludeAudio(t *testing.T) {
	foundSpeech, foundStt := false, false
	for _, mode := range EndpointModeOrderOpenAI() {
		if mode == ModeAudioSpeech {
			foundSpeech = true
		}
		if mode == ModeAudioTranscriptionJSON {
			foundStt = true
		}
	}
	if !foundSpeech || !foundStt {
		t.Fatalf("openai order missing audio modes: %v", EndpointModeOrderOpenAI())
	}
	foundSpeech = false
	for _, mode := range EndpointModeOrderGemini() {
		if mode == ModeAudioSpeech {
			foundSpeech = true
		}
		if mode == ModeAudioTranscriptionJSON {
			t.Fatal("gemini order must not carry the transcription mode")
		}
	}
	if !foundSpeech {
		t.Fatalf("gemini order missing audio_speech: %v", EndpointModeOrderGemini())
	}
}

// gemini 协议 audio_speech 探针（契约 §5.1）：gemini 上游无 /v1/audio/speech
// 端点，请求按 generateContent 形态构造（contents 文本 + responseModalities
// AUDIO + voiceName=Kore），判活验 candidates 含 inlineData。
func TestBuildTestRequestGeminiAudioSpeech(t *testing.T) {
	view := &View{Type: "api_key", ProtocolCode: "gemini"}
	request, err := buildTestRequest(view, ModeAudioSpeech, "gemini-2.5-flash-preview-tts", OutputChallenge{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if request.path != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("gemini speech path = %q", request.path)
	}
	if contentType := request.headers["content-type"]; contentType != "application/json" {
		t.Fatalf("gemini speech content-type = %q", contentType)
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.body, &decoded); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	contents, _ := decoded["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %v", decoded["contents"])
	}
	firstContent, _ := contents[0].(map[string]any)
	parts, _ := firstContent["parts"].([]any)
	firstPart, _ := parts[0].(map[string]any)
	if firstPart["text"] != "ok" {
		t.Fatalf("contents text = %v", firstPart["text"])
	}
	generationConfig, _ := decoded["generationConfig"].(map[string]any)
	modalities, _ := generationConfig["responseModalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "AUDIO" {
		t.Fatalf("responseModalities = %v", generationConfig["responseModalities"])
	}
	speechConfig, _ := generationConfig["speechConfig"].(map[string]any)
	prebuilt, _ := speechConfig["prebuiltVoiceConfig"].(map[string]any)
	if prebuilt["voiceName"] != "Kore" {
		t.Fatalf("voiceName = %v", prebuilt["voiceName"])
	}
}

func TestBuildUpstreamURLGeminiAudioSpeech(t *testing.T) {
	view := &View{Type: "api_key", ProtocolCode: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta"}
	upstream, err := buildUpstreamURL(view, "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if upstream != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("upstream url = %q", upstream)
	}
}

func TestClassifyResponseGeminiAudioSpeech(t *testing.T) {
	view := &View{Type: "api_key", ProtocolCode: "gemini"}
	// 成功：2xx + candidates[].content.parts[].inlineData（JSON 信封，非 audio/* content-type）。
	observation := classifyResponse(view, ProtocolGemini, ModeAudioSpeech,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"cGNt"}}]}}]}`,
		map[string]string{"content-type": "application/json"}, http.StatusOK, 10, 20, OutputChallenge{}, false)
	if !observation.Result.Success {
		t.Fatalf("gemini speech success misclassified: %+v", observation.Result)
	}
	// 失败：2xx 但无 inlineData（错误/空响应）。
	observation = classifyResponse(view, ProtocolGemini, ModeAudioSpeech,
		`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
		map[string]string{"content-type": "application/json"}, http.StatusOK, 10, 20, OutputChallenge{}, false)
	if observation.Result.Success {
		t.Fatalf("gemini speech missing inlineData misclassified: %+v", observation.Result)
	}
}

func TestProbeGeminiAudioSpeechAgainstMockUpstream(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("body not json: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"cGNt"}}]}}]}`))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.ProviderCode = "gemini"
	view.ProtocolCode = "gemini"
	view.HealthCheckModel = "gemini-2.5-flash-preview-tts"
	view.SupportedModels = []string{"gemini-2.5-flash-preview-tts"}
	view.HealthCheckEndpointMode = string(ModeAudioSpeech)
	view.NormalizeEndpointModes = map[EndpointMode]bool{ModeAudioSpeech: true}
	service := newTestService(t, &fakeSource{view: view})
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
	if err != nil {
		t.Fatalf("probe err = %v", err)
	}
	if !observation.Result.Success {
		t.Fatalf("gemini audio_speech probe failed: %+v", observation.Result)
	}
	if gotPath != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	generationConfig, _ := gotBody["generationConfig"].(map[string]any)
	if generationConfig == nil {
		t.Fatalf("body missing generationConfig: %v", gotBody)
	}
	modalities, _ := generationConfig["responseModalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "AUDIO" {
		t.Fatalf("responseModalities = %v", generationConfig["responseModalities"])
	}
	speechConfig, _ := generationConfig["speechConfig"].(map[string]any)
	prebuilt, _ := speechConfig["prebuiltVoiceConfig"].(map[string]any)
	if prebuilt["voiceName"] != "Kore" {
		t.Fatalf("voiceName = %v", prebuilt["voiceName"])
	}
}
