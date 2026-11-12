package gatewaymedia

// minimax（Hailuo/t2a_v2）adapter 测试（契约 §8 报文，fixture 形态与
// mockupstream minimaxQueryBody/serveMinimaxTTS 同源派生——Mock 上游按同一
// 契约实现）：视频创建报文构造（duration 数值直传、first_frame_image
// base64 直传与 url 拒绝、provider_options 合并）、轮询请求构造与产物
// 定位/取消声明、轮询响应归一（Preparing/Queueing/Processing/Success/Fail、
// 受理前 base_resp 错误、Success 缺 url 的协议违约、未知状态 RawStatus、
// 非 2xx）；TTS 报文构造（input→text、voice→voice_setting.voice_id、
// speed 区间裁决、response_format 词表、provider_options deep-merge）与
// 响应 hex 解码转换（content-type 按请求 format、base_resp 错误、非 hex
// 拒绝）、extra_info 字符计量抽取。
import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// minimaxCreateAcceptedFixture 对齐 mockupstream 创建响应形态
// （media_minimax_create_ok，受理凭据 = task_id 字段，base_resp 0）。
const minimaxCreateAcceptedFixture = `{"task_id":"0123456789abcdef","base_resp":{"status_code":0,"status_msg":""}}`

// minimaxPollQueuedFixture 对齐 Preparing/Queueing 形态。
const minimaxPollQueuedFixture = `{"task_id":"0123456789abcdef","status":"Queueing"}`

// minimaxPollProcessingFixture 对齐 Processing 形态。
const minimaxPollProcessingFixture = `{"task_id":"0123456789abcdef","status":"Processing"}`

// minimaxPollSuccessFixture 对齐 Success 形态（file_download_url 为绝对
// 下载 URL）。
const minimaxPollSuccessFixture = `{"task_id":"0123456789abcdef","status":"Success","file_id":"0123456789abcdef","file_download_url":"https://api.minimax.chat/mock/files/sample.mp4"}`

// minimaxPollFailFixture 对齐 Fail 形态（base_resp 非零，错误摘要来源）。
const minimaxPollFailFixture = `{"task_id":"0123456789abcdef","status":"Fail","base_resp":{"status_code":1004,"status_msg":"content policy violation"}}`

// minimaxCreateRejectedFixture 对齐创建面受理前错误形态（200 + 非零
// base_resp 且无 Fail 终态标记）。
const minimaxCreateRejectedFixture = `{"base_resp":{"status_code":1004,"status_msg":"invalid prompt"}}`

func TestMinimaxVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("minimax")
	if adapter == nil {
		t.Fatal("minimax（hailuo）视频 adapter 应已注册")
	}
	if adapter.Provider() != "minimax" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" MiniMax ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestMinimaxVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("minimax").Capabilities()
	// video_generation 请求面（契约 §8.1）：duration（数值秒）、
	// first_frame_image（base64）原生支持；negative_prompt/n/seed/audio
	// 请求面无对应字段 → 忽略 + 回显。
	if !caps.SupportsInputReference || !caps.SupportsSeconds {
		t.Fatalf("minimax 应支持 input_reference/seconds: %+v", caps)
	}
	if caps.SupportsNegativePrompt || caps.SupportsN || caps.SupportsSeed || caps.SupportsAudio {
		t.Fatalf("minimax 请求面无 negative_prompt/n/seed/audio: %+v", caps)
	}
}

func TestMinimaxVideoAdapterArtifactAndCancelDeclarations(t *testing.T) {
	minimax := VideoAdapterForProvider("minimax")
	if !minimax.ContentFromArtifact() {
		t.Fatal("minimax 产物定位应为绝对 Artifact.ContentURL（file_download_url）")
	}
	// minimax 无取消 API（契约 §8.1）→ 链上层本地收敛。
	if minimax.SupportsCancel() {
		t.Fatal("minimax 无上游取消端点，SupportsCancel 应为 false")
	}
}

func TestMinimaxVideoAdapterCreate(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	seconds := 6.0
	reference := "data:image/png;base64,aGVsbG8="
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "MiniMax-Hailuo-2.3",
			Prompt:         "一只猫在弹钢琴",
			Seconds:        &seconds,
			InputReference: &reference,
		},
		ProviderOptions: map[string]any{
			"minimax": map[string]any{"prompt_optimizer": true},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 动机（v2 直拼契约）：opPath 不含版本段，/v1 由 base_url 版本根承载。
	if output.Method != http.MethodPost || output.Path != "/video_generation" {
		t.Fatalf("create 出站形态 = %s %s, want POST /video_generation", output.Method, output.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body["model"] != "MiniMax-Hailuo-2.3" || body["prompt"] != "一只猫在弹钢琴" {
		t.Fatalf("create model/prompt 错误: %v", body)
	}
	// seconds → duration 数值直传（Hailuo 数值形态，不做字符串/档位换算）。
	if duration, ok := body["duration"].(float64); !ok || duration != 6 {
		t.Fatalf("duration = %#v, want 数值 6", body["duration"])
	}
	// 未带 size → 官方默认 768P，显式发送使产出与计费档一致。
	if body["resolution"] != "768P" {
		t.Fatalf("resolution = %#v, want 768P", body["resolution"])
	}
	// input_reference → first_frame_image：data URL 剥离为裸 base64 载荷。
	if body["first_frame_image"] != "aGVsbG8=" {
		t.Fatalf("first_frame_image = %#v, want 剥离后的 base64 载荷", body["first_frame_image"])
	}
	// provider_options 命中 minimax 的子对象 deep-merge。
	if optimizer, ok := body["prompt_optimizer"].(bool); !ok || !optimizer {
		t.Fatalf("prompt_optimizer 未合并: %#v", body["prompt_optimizer"])
	}
}

func TestMinimaxVideoAdapterCreateInputReferenceURLRejected(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	reference := "https://example.com/frame.png"
	_, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "MiniMax-Hailuo-2.3",
			Prompt:         "一只猫在弹钢琴",
			InputReference: &reference,
		},
	})
	if !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("url 形态 input_reference 应 400（网关不代为下载）: %v", err)
	}
}

func TestMinimaxVideoAdapterPollRequest(t *testing.T) {
	method, path, body := VideoAdapterForProvider("minimax").BuildPollRequest("0123456789abcdef")
	// 动机（v2 直拼契约）：opPath 不含版本段，/v1 由 base_url 版本根承载。
	if method != http.MethodGet || path != "/query/video_generation?task_id=0123456789abcdef" {
		t.Fatalf("poll 出站形态 = %s %s", method, path)
	}
	if body != nil {
		t.Fatalf("poll 不得携带请求体: %s", body)
	}
}

func TestMinimaxVideoAdapterParsePollResponse(t *testing.T) {
	adapter := minimaxVideoAdapter{}

	// 创建响应：受理凭据确立，status 缺席 → queued。
	ir, err := adapter.ParsePollResponse(http.StatusOK, []byte(minimaxCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("create parse: %v", err)
	}
	if ir.UpstreamJobID != "0123456789abcdef" || ir.Status != JobStatusQueued {
		t.Fatalf("create parse = %s/%s, want 0123456789abcdef/queued", ir.UpstreamJobID, ir.Status)
	}

	// Preparing/Queueing → queued；Processing → in_progress。
	for _, fixture := range []string{minimaxPollQueuedFixture, minimaxPollProcessingFixture} {
		ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(fixture))
		if err != nil {
			t.Fatalf("poll parse: %v", err)
		}
		want := JobStatusQueued
		if fixture == minimaxPollProcessingFixture {
			want = JobStatusInProgress
		}
		if ir.Status != want {
			t.Fatalf("poll parse status = %s, want %s", ir.Status, want)
		}
	}

	// Success → completed，file_download_url 冻结进 Artifact.ContentURL。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(minimaxPollSuccessFixture))
	if err != nil {
		t.Fatalf("success parse: %v", err)
	}
	if ir.Status != JobStatusCompleted || ir.Artifact.ContentURL != "https://api.minimax.chat/mock/files/sample.mp4" {
		t.Fatalf("success parse = %s/%s", ir.Status, ir.Artifact.ContentURL)
	}

	// Fail → failed，错误摘要取 base_resp（code=status_code 数字串）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(minimaxPollFailFixture))
	if err != nil {
		t.Fatalf("fail parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Code != "1004" ||
		ir.Error.Message != "content policy violation" {
		t.Fatalf("fail parse = %s/%+v", ir.Status, ir.Error)
	}

	// 受理前错误：200 + 非零 base_resp（无 Fail 标记）→ 错误上抛（不落任务）。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(minimaxCreateRejectedFixture)); err == nil ||
		!strings.Contains(err.Error(), "1004") {
		t.Fatalf("受理前 base_resp 错误应上抛: %v", err)
	}

	// Success 缺 file_download_url：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"task_id":"0123456789abcdef","status":"Success"}`)); err == nil {
		t.Fatal("Success 缺 url 应报错（不猜测产物可达性）")
	}

	// 缺受理凭据：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"Processing"}`)); err == nil {
		t.Fatal("缺 task_id 应报错")
	}

	// 未知状态 → in_progress + RawStatus（不猜测失败）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"task_id":"0123456789abcdef","status":"Whatever"}`))
	if err != nil {
		t.Fatalf("unknown status parse: %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.RawStatus != "Whatever" {
		t.Fatalf("unknown status = %s/%s, want in_progress/Whatever", ir.Status, ir.RawStatus)
	}

	// 非 2xx → UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）。
	var statusErr *UpstreamStatusError
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{}`))
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应为 UpstreamStatusError: %v", err)
	}
}

// ---------------------------------------------------------------------------
// minimax TTS adapter（契约 §8.2）
// ---------------------------------------------------------------------------

func TestMinimaxSpeechAdapterRegistry(t *testing.T) {
	adapter := SpeechAdapterForProvider("minimax")
	if adapter == nil {
		t.Fatal("minimax TTS adapter 应已注册")
	}
	if adapter.Provider() != "minimax" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
}

func TestMinimaxSpeechAdapterBuildRequest(t *testing.T) {
	adapter := minimaxSpeechAdapter{}
	speed := 1.5
	path, body, err := adapter.BuildRequest(SpeechRequest{
		Model:          "speech-02-turbo",
		Input:          "你好 minimax",
		Voice:          "male-qn-qingse",
		Speed:          &speed,
		ResponseFormat: "wav",
		ProviderOptions: map[string]any{
			"minimax": map[string]any{"voice_setting": map[string]any{"vol": 2}},
		},
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// 动机（v2 直拼契约）：opPath 不含版本段，/v1 由 base_url 版本根承载。
	if path != "/t2a_v2" {
		t.Fatalf("path = %q, want /t2a_v2", path)
	}
	var parsed struct {
		Model        string `json:"model"`
		Text         string `json:"text"`
		Stream       bool   `json:"stream"`
		VoiceSetting struct {
			VoiceID string  `json:"voice_id"`
			Speed   float64 `json:"speed"`
			Vol     float64 `json:"vol"`
		} `json:"voice_setting"`
		AudioSetting struct {
			SampleRate int    `json:"sample_rate"`
			Format     string `json:"format"`
		} `json:"audio_setting"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	// 公共参数映射：input→text、voice→voice_setting.voice_id、
	// speed→voice_setting.speed、response_format→audio_setting.format。
	if parsed.Model != "speech-02-turbo" || parsed.Text != "你好 minimax" ||
		parsed.VoiceSetting.VoiceID != "male-qn-qingse" ||
		parsed.VoiceSetting.Speed != 1.5 || parsed.AudioSetting.Format != "wav" {
		t.Fatalf("公共参数映射错误: %s", body)
	}
	if parsed.Stream {
		t.Fatalf("同步 TTS 请求 stream 必须为 false: %s", body)
	}
	// provider_options deep-merge：vol 进 voice_setting。
	if parsed.VoiceSetting.Vol != 2 {
		t.Fatalf("provider_options vol 未合并: %s", body)
	}
}

func TestMinimaxSpeechAdapterBuildRequestBoundaries(t *testing.T) {
	adapter := minimaxSpeechAdapter{}
	over := 3.0
	// speed 超厂商区间 0.5–2.0 → 本地 400（契约 §2.4 规则 2）。
	if _, _, err := adapter.BuildRequest(SpeechRequest{
		Model: "speech-02-turbo", Input: "hi", Voice: "v", Speed: &over,
	}); err == nil || !strings.Contains(err.Error(), "0.5") {
		t.Fatalf("speed 超区间应拒绝: %v", err)
	}
	// response_format 词表外（opus 等 OpenAI 值）→ 拒绝（零转码）。
	if _, _, err := adapter.BuildRequest(SpeechRequest{
		Model: "speech-02-turbo", Input: "hi", Voice: "v", ResponseFormat: "opus",
	}); err == nil {
		t.Fatal("response_format=opus 应拒绝（minimax 词表 mp3/pcm/flac/wav）")
	}
	// 空 response_format 默认 mp3 语义（OpenAI 默认）。
	path, body, err := adapter.BuildRequest(SpeechRequest{
		Model: "speech-02-turbo", Input: "hi", Voice: "v",
	})
	if err != nil || path == "" || !strings.Contains(string(body), `"format":"mp3"`) {
		t.Fatalf("空 response_format 应按 mp3 构造: %v %s", err, body)
	}
}

func TestMinimaxSpeechAdapterTransformResponse(t *testing.T) {
	adapter := minimaxSpeechAdapter{}
	// data.audio hex 解码：mp3 magic bytes（frame sync 0xFF 0xFB）。
	payload := []byte{0xFF, 0xFB, 0x90, 0xC0, 0x00, 0x01}
	upstream := `{"data":{"audio":"` + hex.EncodeToString(payload) + `","status":2},"extra_info":{"usage_characters":5},"trace_id":"t"}`
	audio, contentType, err := adapter.TransformResponse([]byte(upstream), SpeechRequest{ResponseFormat: "mp3"})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if len(audio) != len(payload) || audio[0] != 0xFF || audio[1] != 0xFB {
		t.Fatalf("hex 解码结果错误: % x", audio)
	}
	if contentType != "audio/mpeg" {
		t.Fatalf("contentType = %q, want audio/mpeg", contentType)
	}
	// content-type 按请求 format 推导（响应不回显 mime）。
	if _, contentType, err = adapter.TransformResponse([]byte(upstream), SpeechRequest{ResponseFormat: "wav"}); err != nil || contentType != "audio/wav" {
		t.Fatalf("wav format content-type = %q err=%v", contentType, err)
	}
	// base_resp 非零 → 上游错误上抛。
	if _, _, err = adapter.TransformResponse([]byte(`{"base_resp":{"status_code":1004,"status_msg":"bad voice"}}`), SpeechRequest{}); err == nil ||
		!strings.Contains(err.Error(), "1004") {
		t.Fatalf("base_resp 错误应上抛: %v", err)
	}
	// 非 hex 载荷拒绝（不得把 hex 字符串当音频下发）。
	if _, _, err = adapter.TransformResponse([]byte(`{"data":{"audio":"zz!!"}}`), SpeechRequest{}); err == nil {
		t.Fatal("非 hex data.audio 应拒绝")
	}
	// 缺 data.audio 拒绝。
	if _, _, err = adapter.TransformResponse([]byte(`{"data":{"status":2}}`), SpeechRequest{}); err == nil {
		t.Fatal("缺 data.audio 应拒绝")
	}
}

func TestSpeechReportedCharsFromJSON(t *testing.T) {
	// minimax 形态：extra_info.usage_characters 在场 → 回报字符数。
	var root any
	if err := json.Unmarshal([]byte(`{"extra_info":{"usage_characters":161}}`), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := SpeechReportedCharsFromJSON(root); got != 161 {
		t.Fatalf("reported chars = %d, want 161", got)
	}
	// 非 minimax 形态（gemini 响应/缺失/非正值）恒 0（回落请求字符自算）。
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"data":"AAAA"}}]}}]}`), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := SpeechReportedCharsFromJSON(root); got != 0 {
		t.Fatalf("gemini 形态应返回 0, got %d", got)
	}
	if got := SpeechReportedCharsFromJSON(nil); got != 0 {
		t.Fatalf("nil root 应返回 0, got %d", got)
	}
	if err := json.Unmarshal([]byte(`{"extra_info":{"usage_characters":-1}}`), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := SpeechReportedCharsFromJSON(root); got != 0 {
		t.Fatalf("非正值应返回 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// minimax H3 v2（2026-11-13 协议/分辨率档批）
// ---------------------------------------------------------------------------

func TestMinimaxH3CreateTextToVideo(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	seconds := 5.0
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:   "MiniMax-H3",
			Prompt:  "一只猫在弹钢琴",
			Seconds: &seconds,
			Size:    "2560x1440",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if output.Method != http.MethodPost || output.Path != "/v2/video_generation" {
		t.Fatalf("H3 create 出站形态 = %s %s, want POST /v2/video_generation", output.Method, output.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body["model"] != "MiniMax-H3" || body["duration"] != float64(5) ||
		body["resolution"] != "2K" || body["ratio"] != "16:9" {
		t.Fatalf("H3 create 字段错误: %v", body)
	}
	content, ok := body["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want 单 text item", body["content"])
	}
	item, _ := content[0].(map[string]any)
	if item["type"] != "text" || item["text"] != "一只猫在弹钢琴" {
		t.Fatalf("text item = %#v", item)
	}
	if _, exists := body["prompt"]; exists {
		t.Fatalf("H3 v2 不得携带 v1 prompt 字段: %v", body)
	}
}

func TestMinimaxH3CreateImageToVideoAndDefaults(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	reference := "data:image/png;base64,aGVsbG8="
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "MiniMax-H3-Max",
			Prompt:         "让画面动起来",
			Size:           "640x480",
			InputReference: &reference,
		},
		ProviderOptions: map[string]any{"minimax": map[string]any{"prompt_optimizer": true}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body["duration"] != float64(6) {
		t.Fatalf("未传 seconds 必须发默认 6 秒（计费基源一致）: %v", body["duration"])
	}
	if body["resolution"] != "480P" {
		t.Fatalf("640x480 短边 480 应落 480P: %v", body["resolution"])
	}
	if _, exists := body["ratio"]; exists {
		t.Fatalf("图生视频 ratio 必须省略（跟随首帧）: %v", body)
	}
	content, _ := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v, want text + first_frame", body["content"])
	}
	imageItem, _ := content[1].(map[string]any)
	imageURL, _ := imageItem["image_url"].(map[string]any)
	if imageItem["type"] != "image_url" || imageItem["role"] != "first_frame" ||
		imageURL["url"] != reference {
		t.Fatalf("first_frame item = %#v", imageItem)
	}
	if body["prompt_optimizer"] != true {
		t.Fatalf("provider_options 未合并: %v", body["prompt_optimizer"])
	}
}

func TestMinimaxH3CreateRejectsBareBase64Reference(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	reference := "aGVsbG8="
	_, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "MiniMax-H3",
			Prompt:         "p",
			InputReference: &reference,
		},
	})
	if !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("H3 裸 base64 首帧应 400（无法表达 mime）: %v", err)
	}
}

func TestMinimaxH3BillingResolution(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	cases := []struct {
		model, size, want string
	}{
		{"MiniMax-H3", "", "768P"},
		{"MiniMax-H3", "1280x720", "768P"},
		{"MiniMax-H3", "2560x1440", "2K"},
		{"MiniMax-H3-Max", "", "768P"},
		{"MiniMax-H3-Max", "640x480", "480P"},
		{"MiniMax-H3-Max", "1280x720", "768P"},
		{"MiniMax-Hailuo-2.3", "", "768P"},
		{"MiniMax-Hailuo-2.3", "1280x720", "768P"},
		{"MiniMax-Hailuo-2.3", "1920x1080", "1080P"},
		{"MiniMax-Hailuo-2.3-Fast", "2560x1440", "1080P"},
	}
	for _, testCase := range cases {
		got := adapter.BillingResolution(NormalizedVideoParams{Model: testCase.model, Size: testCase.size})
		if got != testCase.want {
			t.Fatalf("BillingResolution(%s,%s) = %q, want %q", testCase.model, testCase.size, got, testCase.want)
		}
	}
}

func TestMinimaxH3PollRequestAndParse(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	method, path, body := adapter.BuildPollRequestForModel("MiniMax-H3", "0123456789abcdef")
	if method != http.MethodGet || path != "/v2/query/video_generation/0123456789abcdef" || body != nil {
		t.Fatalf("H3 poll = %s %s %s", method, path, body)
	}
	// Hailuo 仍走 v1 query 形态。
	if _, v1Path, _ := adapter.BuildPollRequestForModel("MiniMax-Hailuo-2.3", "t"); v1Path != "/query/video_generation?task_id=t" {
		t.Fatalf("Hailuo poll path = %q", v1Path)
	}

	ir, err := adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK, []byte(`{"task_id":"0123456789abcdef"}`))
	if err != nil || ir.UpstreamJobID != "0123456789abcdef" || ir.Status != JobStatusQueued {
		t.Fatalf("H3 create parse = %+v/%v", ir, err)
	}
	ir, err = adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"processing"}}`))
	if err != nil || ir.Status != JobStatusInProgress {
		t.Fatalf("H3 processing parse = %+v/%v", ir, err)
	}
	ir, err = adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"succeeded","content":{"url":"https://example.com/h3.mp4"}}}`))
	if err != nil || ir.Status != JobStatusCompleted || ir.Artifact.ContentURL != "https://example.com/h3.mp4" {
		t.Fatalf("H3 success parse = %+v/%v", ir, err)
	}
	ir, err = adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"failed","error":{"code":1004,"message":"content policy"}}}`))
	if err != nil || ir.Status != JobStatusFailed || ir.Error == nil ||
		ir.Error.Code != "1004" || ir.Error.Message != "content policy" {
		t.Fatalf("H3 fail parse = %+v/%v", ir, err)
	}
	ir, err = adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"cancelled","error":"canceled by user"}}`))
	if err != nil || ir.Status != JobStatusCancelled || ir.Error == nil || ir.Error.Message != "canceled by user" {
		t.Fatalf("H3 cancelled parse = %+v/%v", ir, err)
	}
	ir, err = adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"mystery"}}`))
	if err != nil || ir.Status != JobStatusInProgress || ir.RawStatus != "mystery" {
		t.Fatalf("H3 unknown parse = %+v/%v", ir, err)
	}
	if _, err := adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusOK,
		[]byte(`{"task":{"status":"succeeded"}}`)); err == nil {
		t.Fatal("H3 succeeded 缺 content.url 应报错")
	}
	if _, err := adapter.ParsePollResponseForModel("MiniMax-H3", http.StatusBadGateway,
		[]byte(`{}`)); err == nil {
		t.Fatal("H3 非 2xx 应报错")
	}
	// Hailuo 模型仍按 v1 解析。
	if _, err := adapter.ParsePollResponseForModel("MiniMax-Hailuo-2.3", http.StatusOK,
		[]byte(minimaxPollSuccessFixture)); err != nil {
		t.Fatalf("Hailuo v1 parse: %v", err)
	}
}

func TestMinimaxH3CreateRejectsUnknownRatio(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	_, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:  "MiniMax-H3",
			Prompt: "p",
			Size:   "1000x333",
		},
	})
	if !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("词表外宽高比应 400（不近似贴合）: %v", err)
	}
}

func TestMinimaxHailuoCreate1080P(t *testing.T) {
	adapter := minimaxVideoAdapter{}
	seconds := 6.0
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:   "MiniMax-Hailuo-2.3",
			Prompt:  "p",
			Seconds: &seconds,
			Size:    "1920x1080",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["resolution"] != "1080P" {
		t.Fatalf("1080p size 应发送 resolution=1080P: %v", body)
	}
}
