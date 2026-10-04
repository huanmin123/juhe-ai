package mockupstream

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doMedia issues a request with full control over method, content type and
// raw body — multipart and binary bodies cannot go through the JSON post
// helper in mockupstream_test.go.
func doMedia(t *testing.T, server *httptest.Server, method, path, scenario, contentType string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if scenario != "" {
		req.Header.Set("X-Mock-Scenario", scenario)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// openaiErrorShape asserts the OpenAI error envelope (error.message/type
// plus an optional code) and returns it.
func openaiErrorShape(t *testing.T, body []byte) (message, errType, code string) {
	t.Helper()
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("not an OpenAI error JSON: %v (%s)", err, body)
	}
	return parsed.Error.Message, parsed.Error.Type, parsed.Error.Code
}

// TestMediaSpeechOKByFormat locks the TTS binary channel: per
// response_format content-type negotiation and magic-byte payloads; an
// absent response_format means the OpenAI default mp3 (contract §4.1).
func TestMediaSpeechOKByFormat(t *testing.T) {
	m := New()
	defer m.Close()

	cases := []struct {
		name      string
		format    string
		wantType  string
		wantMagic []byte
	}{
		{"default is mp3", "", "audio/mpeg", []byte{0xFF, 0xFB}},
		{"mp3", "mp3", "audio/mpeg", []byte{0xFF, 0xFB}},
		{"wav", "wav", "audio/wav", []byte("RIFF")},
		{"pcm", "pcm", "audio/L16;rate=24000", nil},
		{"opus", "opus", "audio/ogg", []byte("OggS")},
		{"aac", "aac", "audio/aac", []byte{0xFF, 0xF1}},
		{"flac", "flac", "audio/flac", []byte("fLaC")},
	}
	for _, tc := range cases {
		code, header, rawStr := post(t, m.Server, "/v1/audio/speech", string(ScenarioMediaTTSOK), `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy","response_format":"`+tc.format+`"}`)
		raw := []byte(rawStr)
		if code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.name, code)
		}
		if got := header.Get("Content-Type"); got != tc.wantType {
			t.Fatalf("%s: content-type = %q, want %q", tc.name, got, tc.wantType)
		}
		if len(raw) == 0 {
			t.Fatalf("%s: empty binary payload", tc.name)
		}
		if tc.wantMagic != nil && !bytes.HasPrefix(raw, tc.wantMagic) {
			t.Fatalf("%s: payload missing magic bytes % X, got % X", tc.name, tc.wantMagic, raw[:min(8, len(raw))])
		}
		if tc.format == "opus" && !strings.Contains(rawStr, "OpusHead") {
			t.Fatal("opus payload missing the OpusHead magic")
		}
	}

	// pcm payload is exactly the shared silent PCM frames (24 kHz / 16-bit /
	// mono / 1 s), all zero amplitude.
	_, _, rawStr := post(t, m.Server, "/v1/audio/speech", string(ScenarioMediaTTSOK), `{"model":"tts-1","input":"hi","voice":"alloy","response_format":"pcm"}`)
	if len(rawStr) != 24000*2 {
		t.Fatalf("pcm payload length = %d, want %d", len(rawStr), 24000*2)
	}
	for i := 0; i < len(rawStr); i++ {
		if rawStr[i] != 0 {
			t.Fatalf("pcm payload byte %d = 0x%02X, want silence", i, rawStr[i])
		}
	}

	// Unknown response_format must 400 like the real upstream instead of
	// guessing a payload.
	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioMediaTTSOK), "application/json", []byte(`{"model":"tts-1","input":"hi","response_format":"wma"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("unknown response_format status = %d, want 400", code)
	}
	if _, _, code := openaiErrorShape(t, raw); code != "invalid_response_format" {
		t.Fatalf("unknown response_format code = %q, want invalid_response_format", code)
	}
}

// TestMediaWAVPayloadStructure parses the built-in WAV payload as a real
// decoder would: RIFF/WAVE/fmt/data chunk structure, PCM format, 24 kHz /
// 16-bit / mono, and chunk size consistency.
func TestMediaWAVPayloadStructure(t *testing.T) {
	m := New()
	defer m.Close()

	code, header, rawStr := post(t, m.Server, "/v1/audio/speech", string(ScenarioMediaTTSOK), `{"model":"tts-1","input":"hello","voice":"alloy","response_format":"wav"}`)
	if code != http.StatusOK {
		t.Fatalf("wav status = %d", code)
	}
	if got := header.Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("wav content-type = %q", got)
	}
	raw := []byte(rawStr)
	const dataLen = 24000 * 2 // 24 kHz * 16-bit * mono * 1 s
	if len(raw) != 44+dataLen {
		t.Fatalf("wav total length = %d, want %d", len(raw), 44+dataLen)
	}
	for _, want := range []struct {
		at   int
		text string
	}{
		{0, "RIFF"}, {8, "WAVE"}, {12, "fmt "}, {36, "data"},
	} {
		if string(raw[want.at:want.at+4]) != want.text {
			t.Fatalf("wav chunk marker at %d = %q, want %q", want.at, raw[want.at:want.at+4], want.text)
		}
	}
	if got := binary.LittleEndian.Uint32(raw[4:8]); got != uint32(len(raw)-8) {
		t.Fatalf("RIFF size = %d, want %d", got, len(raw)-8)
	}
	if got := binary.LittleEndian.Uint16(raw[20:22]); got != 1 {
		t.Fatalf("audio format = %d, want PCM(1)", got)
	}
	if got := binary.LittleEndian.Uint16(raw[22:24]); got != 1 {
		t.Fatalf("channels = %d, want mono", got)
	}
	if got := binary.LittleEndian.Uint32(raw[24:28]); got != 24000 {
		t.Fatalf("sample rate = %d, want 24000", got)
	}
	if got := binary.LittleEndian.Uint32(raw[28:32]); got != 24000*2 {
		t.Fatalf("byte rate = %d, want %d", got, 24000*2)
	}
	if got := binary.LittleEndian.Uint16(raw[32:34]); got != 2 {
		t.Fatalf("block align = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(raw[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(raw[40:44]); got != dataLen {
		t.Fatalf("data size = %d, want %d", got, dataLen)
	}
}

// TestMediaTTSErrorScenarios covers the OpenAI-form TTS errors: the voice
// validation 400 (echoing the requested voice) and the pre-accept 429 for
// account-switch retry cases (contract §3.2.4).
func TestMediaTTSErrorScenarios(t *testing.T) {
	m := New()
	defer m.Close()

	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioMediaTTS400VoiceInvalid), "application/json", []byte(`{"model":"gpt-4o-mini-tts","input":"hi","voice":"not-a-voice"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("voice invalid status = %d, want 400", code)
	}
	message, errType, errCode := openaiErrorShape(t, raw)
	if !strings.Contains(message, "not-a-voice") {
		t.Fatalf("voice invalid message = %q, want the requested voice echoed", message)
	}
	if errType != "invalid_request_error" || errCode != "invalid_voice" {
		t.Fatalf("voice invalid error shape = %q/%q", errType, errCode)
	}

	code, header, raw := doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioMediaTTS429BeforeAccept), "application/json", []byte(`{"model":"tts-1","input":"hi","voice":"alloy"}`))
	if code != http.StatusTooManyRequests {
		t.Fatalf("429 before accept status = %d, want 429", code)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("429 before accept missing Retry-After")
	}
	_, errType, _ = openaiErrorShape(t, raw)
	if errType != "rate_limit_error" {
		t.Fatalf("429 before accept error type = %q", errType)
	}
}

// sttMultipart builds a transcription-style multipart request (file +
// model/response_format fields) per contract §4.2.
func sttMultipart(t *testing.T) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("FAKE-AUDIO-BYTES")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), buf.Bytes()
}

// TestMediaSTTScenarios covers the STT face on transcriptions and the
// structurally identical translations endpoint (contract §4.2).
func TestMediaSTTScenarios(t *testing.T) {
	m := New()
	defer m.Close()

	contentType, body := sttMultipart(t)

	// media_stt_ok: default json shape {"text":"..."}.
	code, header, raw := doMedia(t, m.Server, http.MethodPost, "/v1/audio/transcriptions", string(ScenarioMediaSTTOK), contentType, body)
	if code != http.StatusOK {
		t.Fatalf("stt ok status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "application/json") {
		t.Fatalf("stt ok content-type = %q", header.Get("Content-Type"))
	}
	var plain struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &plain); err != nil {
		t.Fatalf("stt ok body: %v (%s)", err, raw)
	}
	if plain.Text == "" {
		t.Fatal("stt ok text must be non-empty")
	}

	// media_stt_ok_verbose: text/language/duration/segments/usage per §4.2.
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1/audio/transcriptions", string(ScenarioMediaSTTOKVerbose), contentType, body)
	if code != http.StatusOK {
		t.Fatalf("stt verbose status = %d", code)
	}
	var verbose struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Duration any    `json:"duration"`
		Segments []any  `json:"segments"`
		Words    []any  `json:"words"`
		Usage    struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &verbose); err != nil {
		t.Fatalf("stt verbose body: %v (%s)", err, raw)
	}
	if verbose.Text == "" || verbose.Language == "" || verbose.Duration == nil || len(verbose.Segments) == 0 || len(verbose.Words) == 0 {
		t.Fatalf("stt verbose missing contract fields: %s", raw)
	}
	if verbose.Usage.InputTokens <= 0 || verbose.Usage.OutputTokens <= 0 {
		t.Fatalf("stt verbose usage tokens must be positive: %s", raw)
	}

	// media_stt_400_bad_file: OpenAI-form 400.
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1/audio/transcriptions", string(ScenarioMediaSTT400BadFile), contentType, body)
	if code != http.StatusBadRequest {
		t.Fatalf("stt bad file status = %d", code)
	}
	if _, errType, errCode := openaiErrorShape(t, raw); errType != "invalid_request_error" || errCode != "invalid_file" {
		t.Fatalf("stt bad file error shape = %q/%q", errType, errCode)
	}

	// translations is structurally identical (同构).
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1/audio/translations", string(ScenarioMediaSTTOK), contentType, body)
	if code != http.StatusOK || !strings.Contains(string(raw), `"text"`) {
		t.Fatalf("translations stt ok: %d %s", code, raw)
	}

	// Generic status scenarios keep applying to media endpoints.
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioStatus429), "application/json", []byte(`{}`))
	if code != http.StatusTooManyRequests || !strings.Contains(string(raw), "rate_limit_error") {
		t.Fatalf("generic 429 on speech endpoint: %d %s", code, raw)
	}
}

// TestMediaGeminiTTSScenarios covers the Gemini generateContent TTS face
// (contract §5.1): inlineData base64 PCM, usageMetadata, modelVersion echo,
// and the gemini-form 400.
func TestMediaGeminiTTSScenarios(t *testing.T) {
	m := New()
	defer m.Close()

	const model = "gemini-2.5-flash-preview-tts"
	requestBody := `{"contents":[{"parts":[{"text":"Say cheerfully: hello"}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"prebuiltVoiceConfig":{"voiceName":"Kore"}}}}`

	code, header, raw := doMedia(t, m.Server, http.MethodPost, "/v1beta/models/"+model+":generateContent", string(ScenarioMediaGeminiTTSOK), "application/json", []byte(requestBody))
	if code != http.StatusOK {
		t.Fatalf("gemini tts ok status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "application/json") {
		t.Fatalf("gemini tts ok content-type = %q", header.Get("Content-Type"))
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("gemini tts ok body: %v (%s)", err, raw)
	}
	if len(parsed.Candidates) != 1 || len(parsed.Candidates[0].Content.Parts) != 1 {
		t.Fatalf("gemini tts ok candidates shape: %s", raw)
	}
	inline := parsed.Candidates[0].Content.Parts[0].InlineData
	if inline.MimeType != "audio/L16;rate=24000" {
		t.Fatalf("gemini inlineData mimeType = %q, want audio/L16;rate=24000", inline.MimeType)
	}
	pcm, err := base64.StdEncoding.DecodeString(inline.Data)
	if err != nil {
		t.Fatalf("gemini inlineData not base64: %v", err)
	}
	if len(pcm) != 24000*2 {
		t.Fatalf("gemini pcm length = %d, want %d", len(pcm), 24000*2)
	}
	for i, b := range pcm {
		if b != 0 {
			t.Fatalf("gemini pcm byte %d = 0x%02X, want silence", i, b)
		}
	}
	if parsed.ModelVersion != model {
		t.Fatalf("gemini modelVersion = %q, want the URL {model} echo %q", parsed.ModelVersion, model)
	}
	if parsed.UsageMetadata.PromptTokenCount <= 0 || parsed.UsageMetadata.CandidatesTokenCount <= 0 || parsed.UsageMetadata.TotalTokenCount <= 0 {
		t.Fatalf("gemini usageMetadata must carry token counts: %s", raw)
	}

	// voiceName mapping is asserted via the recorded request body — the
	// gemini response schema carries no voice field.
	rec := lastRequest(t, m)
	if rec.Path != "/v1beta/models/"+model+":generateContent" {
		t.Fatalf("recorded gemini path = %q", rec.Path)
	}
	if !strings.Contains(rec.Body, `"voiceName":"Kore"`) {
		t.Fatalf("recorded gemini body missing voiceName: %q", rec.Body)
	}

	// media_gemini_tts_400_format: error.code/message/status envelope.
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1beta/models/"+model+":generateContent", string(ScenarioMediaGeminiTTS400Format), "application/json", []byte(requestBody))
	if code != http.StatusBadRequest {
		t.Fatalf("gemini tts 400 status = %d", code)
	}
	var errParsed struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &errParsed); err != nil {
		t.Fatalf("gemini tts 400 body: %v (%s)", err, raw)
	}
	if errParsed.Error.Code != 400 || errParsed.Error.Message == "" || errParsed.Error.Status != "INVALID_ARGUMENT" {
		t.Fatalf("gemini tts 400 error shape: %+v", errParsed.Error)
	}
}

// TestMediaEndpointWhitelist proves the media endpoint acceptance boundary:
// exact audio paths plus the {model}:generateContent path form with a
// non-empty single-segment model; everything else (including the M3
// families not yet in scope) keeps the 404 behavior. The M2 videos family
// has its own method/path matrix in media_video_test.go.
func TestMediaEndpointWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/audio/speech"},
		{http.MethodPut, "/v1/audio/speech"},
		{http.MethodPost, "/v1/audio/speech/extra"},
		{http.MethodGet, "/v1/audio/transcriptions"},
		{http.MethodPost, "/v1/audio/transcriptions/extra"},
		{http.MethodGet, "/v1beta/models/gemini-x:generateContent"},
		{http.MethodPost, "/v1beta/models/:generateContent"},           // empty model segment
		{http.MethodPost, "/v1beta/models/a/b:generateContent"},        // model segment with slash
		{http.MethodPost, "/v1beta/models/gemini-x:generateContentEx"}, // wrong suffix
		{http.MethodPost, "/v1beta2/models/gemini-x:generateContent"},  // wrong prefix
		{http.MethodPost, "/v1/audio/jobs"},                            // M3, not yet whitelisted
	}
	for _, tc := range bad {
		code, _, _ := doMedia(t, m.Server, tc.method, tc.path, string(ScenarioMediaTTSOK), "application/json", []byte(`{}`))
		if code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, code)
		}
	}

	// Accepted forms default to the family OK response even under an
	// unrelated scenario (chat_ok), mirroring the chat defaulting.
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1/audio/translations", string(ScenarioChatOK), "application/json", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("translations default status = %d, want 200", code)
	}
	if code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1beta/models/any-tts-model:generateContent", string(ScenarioChatOK), "application/json", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("gemini default status = %d, want 200 (%s)", code, raw)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioChatOK), "application/json", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("speech default status = %d, want 200", code)
	}
}

// TestMediaRequestRecording proves Requests() keeps the full request-side
// contract for multipart and binary media bodies: the form fields, the file
// content, and raw non-UTF-8 bytes survive recording verbatim.
func TestMediaRequestRecording(t *testing.T) {
	m := New()
	defer m.Close()

	contentType, body := sttMultipart(t)
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1/audio/transcriptions", string(ScenarioMediaSTTOK), contentType, body); code != http.StatusOK {
		t.Fatal("multipart request failed")
	}
	rec := lastRequest(t, m)
	if rec.Method != http.MethodPost || rec.Path != "/v1/audio/transcriptions" || rec.AuthHeader != "Bearer test-key" {
		t.Fatalf("multipart recording = %+v", rec)
	}
	for _, want := range []string{`name="model"`, "whisper-1", "verbose_json", `name="file"; filename="audio.mp3"`, "FAKE-AUDIO-BYTES"} {
		if !strings.Contains(rec.Body, want) {
			t.Fatalf("multipart recording missing %q", want)
		}
	}

	// Binary request bodies are recorded verbatim regardless of content type.
	rawBody := []byte{0xFF, 0xFB, 0x90, 0xC0, 0x00, 0x01, 0x02}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1/audio/speech", string(ScenarioMediaTTSOK), "application/octet-stream", rawBody); code != http.StatusOK {
		t.Fatal("binary request failed")
	}
	rec = lastRequest(t, m)
	if rec.Body != string(rawBody) {
		t.Fatalf("binary recording = %v, want verbatim %v", []byte(rec.Body), rawBody)
	}
}
