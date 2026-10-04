package mockupstream

// Media scenario family for the M1 audio chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2/§4/§5.1:
//
//   - OpenAI TTS  POST /v1/audio/speech — binary audio negotiated by
//     response_format (contract §4.1);
//   - OpenAI STT  POST /v1/audio/transcriptions and /v1/audio/translations
//     (同构, contract §4.2) — json / verbose_json transcripts;
//   - Gemini TTS  POST /v1beta/models/{model}:generateContent (contract
//     §5.1) — inlineData base64 PCM + usageMetadata.
//   - OpenAI videos async job face (video.go, contract §3.2/§4.3) — M2.
//
// Scenario selection reuses the X-Mock-Scenario / ?scenario= mechanism.
// Like the chat face (unknown scenarios default to chat_ok), an unknown
// scenario on a media endpoint defaults to that endpoint family's OK
// response; the generic status/fault scenarios above the media dispatch
// keep applying to every accepted endpoint (e.g. status_429 on
// /v1/audio/speech, slow_first_byte composing with the binary channel).

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Media scenario names (contract §3.2.4).
const (
	ScenarioMediaTTSOK              Scenario = "media_tts_ok"
	ScenarioMediaTTS400VoiceInvalid Scenario = "media_tts_400_voice_invalid"
	ScenarioMediaTTS429BeforeAccept Scenario = "media_tts_429_before_accept"
	ScenarioMediaSTTOK              Scenario = "media_stt_ok"
	ScenarioMediaSTTOKVerbose       Scenario = "media_stt_ok_verbose"
	ScenarioMediaSTT400BadFile      Scenario = "media_stt_400_bad_file"
	ScenarioMediaGeminiTTSOK        Scenario = "media_gemini_tts_ok"
	ScenarioMediaGeminiTTS400Format Scenario = "media_gemini_tts_400_format"
)

// mediaEndpoints are the exact media method+path pairs added to the engine
// whitelist (contract §3.2.1).
var mediaEndpoints = map[endpoint]bool{
	{http.MethodPost, "/v1/audio/speech"}:         true,
	{http.MethodPost, "/v1/audio/transcriptions"}: true,
	{http.MethodPost, "/v1/audio/translations"}:   true,
}

// acceptsMediaEndpoint matches the exact audio paths, the videos family
// (video.go), the Gemini Veo operation family (video_gemini.go, M3), the GLM
// CogVideoX task family (video_glm.go, M3), the MiniMax video/TTS family
// (video_minimax.go, M3), the Volcengine Seedance task family
// (video_volcengine.go, M3), and the Gemini TTS path form
// POST /v1beta/models/{model}:generateContent, where {model} is a non-empty
// single path segment.
func acceptsMediaEndpoint(method, path string) bool {
	if mediaEndpoints[endpoint{method, path}] {
		return true
	}
	if acceptsVideoEndpoint(method, path) {
		return true
	}
	if acceptsGeminiVeoEndpoint(method, path) {
		return true
	}
	if acceptsGlmVideoEndpoint(method, path) {
		return true
	}
	if acceptsMinimaxVideoEndpoint(method, path) {
		return true
	}
	if acceptsVolcengineVideoEndpoint(method, path) {
		return true
	}
	return method == http.MethodPost && geminiGenerateContentModel(path) != ""
}

// isMediaPath reports whether the path belongs to a media endpoint family.
// It runs after the method-aware whitelist check, so the POST-keyed map
// lookup is only reached by requests that already passed as POST.
func isMediaPath(path string) bool {
	if mediaEndpoints[endpoint{http.MethodPost, path}] || isVideoPath(path) || isGeminiVeoPath(path) || isGlmVideoPath(path) || isMinimaxVideoPath(path) || isVolcengineVideoPath(path) {
		return true
	}
	return geminiGenerateContentModel(path) != ""
}

// geminiGenerateContentModel returns the {model} segment when path matches
// the Gemini TTS form /v1beta/models/{model}:generateContent with a
// non-empty single-segment model name, "" otherwise.
func geminiGenerateContentModel(path string) string {
	const prefix, suffix = "/v1beta/models/", ":generateContent"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	model := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if model == "" || strings.Contains(model, "/") {
		return ""
	}
	return model
}

// serveMedia dispatches a whitelisted media endpoint to its family handler.
func (m *Server) serveMedia(w http.ResponseWriter, r *http.Request, idx int, scenario Scenario) {
	if isVideoPath(r.URL.Path) {
		m.serveVideo(w, r, idx, scenario)
		return
	}
	if isGeminiVeoPath(r.URL.Path) {
		m.serveGeminiVeo(w, r, scenario)
		return
	}
	if isGlmVideoPath(r.URL.Path) {
		m.serveGlmVideo(w, r, scenario)
		return
	}
	if isMinimaxVideoPath(r.URL.Path) {
		m.serveMinimax(w, r, idx, scenario)
		return
	}
	if isVolcengineVideoPath(r.URL.Path) {
		m.serveVolcengineVideo(w, r, scenario)
		return
	}
	if model := geminiGenerateContentModel(r.URL.Path); model != "" {
		m.serveGeminiTTS(w, scenario, model)
		return
	}
	if r.URL.Path == "/v1/audio/speech" {
		m.serveSpeech(w, idx, scenario)
		return
	}
	serveSTT(w, scenario) // transcriptions + translations 同构 (contract §4.2)
}

// serveSpeech implements the OpenAI TTS face (contract §4.1).
func (m *Server) serveSpeech(w http.ResponseWriter, idx int, scenario Scenario) {
	// Read the recorded body under the Server mutex: concurrent requests
	// append to m.seenReqs and an unlocked read races the writer.
	m.mu.Lock()
	requestBody := m.seenReqs[idx].Body
	m.mu.Unlock()
	var req struct {
		Voice          string `json:"voice"`
		ResponseFormat string `json:"response_format"`
	}
	_ = json.Unmarshal([]byte(requestBody), &req)

	switch scenario {
	case ScenarioMediaTTS400VoiceInvalid:
		// OpenAI 形态错误：error.message/type/code，message 回显请求 voice
		// 供断言（缺失时用 "(missing)" 占位）。
		voice := req.Voice
		if voice == "" {
			voice = "(missing)"
		}
		message, _ := json.Marshal(fmt.Sprintf("Invalid voice: %s not found", voice))
		writeJSONStatus(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":%s,"type":"invalid_request_error","code":"invalid_voice"}}`, message))
	case ScenarioMediaTTS429BeforeAccept:
		// 受理前限流：换账户重试用例（contract §3.2.4）。
		w.Header().Set("Retry-After", FormatRetryAfter(30))
		writeJSONStatus(w, http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached before accepting request","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	default:
		// media_tts_ok（未知场景同样默认 OK，镜像 chat 默认哲学）：按请求
		// response_format 协商 content-type 与内置载荷；response_format 缺省
		// 为 OpenAI 默认 mp3。
		contentType, payload, ok := audioResponseFor(req.ResponseFormat)
		if !ok {
			message, _ := json.Marshal(fmt.Sprintf("Invalid response_format: %s", req.ResponseFormat))
			writeJSONStatus(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":%s,"type":"invalid_request_error","code":"invalid_response_format"}}`, message))
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}
}

// serveSTT implements the OpenAI STT face for transcriptions and
// translations (同构, contract §4.2).
func serveSTT(w http.ResponseWriter, scenario Scenario) {
	switch scenario {
	case ScenarioMediaSTT400BadFile:
		writeJSONStatus(w, http.StatusBadRequest, `{"error":{"message":"Invalid audio file: unable to decode","type":"invalid_request_error","code":"invalid_file"}}`)
	case ScenarioMediaSTTOKVerbose:
		// verbose_json：text/language/duration/segments/words + usage 的
		// gpt-4o-transcribe 系 input_tokens/output_tokens 形态（§4.2）。
		writeJSONStatus(w, http.StatusOK, `{"text":"MOCK-TRANSCRIPT: hello from mock upstream","language":"en","duration":2.5,"segments":[{"id":0,"start":0.0,"end":2.5,"text":"MOCK-TRANSCRIPT: hello from mock upstream"}],"words":[{"word":"MOCK-TRANSCRIPT","start":0.0,"end":0.9},{"word":"hello","start":1.0,"end":1.4}],"usage":{"input_tokens":120,"output_tokens":32}}`)
	default:
		// media_stt_ok（json 默认格式）。
		writeJSONStatus(w, http.StatusOK, `{"text":"MOCK-TRANSCRIPT: hello from mock upstream"}`)
	}
}

// serveGeminiTTS implements the Gemini TTS generateContent face (contract
// §5.1).
func (m *Server) serveGeminiTTS(w http.ResponseWriter, scenario Scenario, model string) {
	if scenario == ScenarioMediaGeminiTTS400Format {
		// gemini 错误形态：error.code/message/status。
		writeJSONStatus(w, http.StatusBadRequest, `{"error":{"code":400,"message":"Unsupported response format for this model; only pcm audio output is supported","status":"INVALID_ARGUMENT"}}`)
		return
	}
	// media_gemini_tts_ok：candidates[0].content.parts[0].inlineData.data =
	// base64 裸 PCM（24kHz/16bit/mono，无 WAV 头），mimeType
	// audio/L16;rate=24000，附 usageMetadata token 计量；modelVersion 回显
	// URL {model} 段供路由断言。voiceName 映射经 Requests() 请求体录制断言
	// ——gemini 响应 schema 无 voice 字段，不私造契约外字段。
	data := base64.StdEncoding.EncodeToString(silencePCM())
	body := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":%q}}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":240,"totalTokenCount":252},"modelVersion":%q}`, data, model)
	writeJSONStatus(w, http.StatusOK, body)
}
