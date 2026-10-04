package gatewaymedia

// xai（Grok Imagine Video）adapter 测试（契约 §6.1 报文，fixture 形态与
// mockupstream xaiTaskBodyLocked 同源派生——Mock 上游按同一契约实现）：
// 视频创建报文构造（duration 整数化、size→aspect_ratio+resolution 七值词表
// 换算与词表外 400、image 首帧直传、generate_audio 直传、零值字段省略、
// provider_options 合并）、轮询请求构造与产物定位/取消声明、轮询响应归一
//（pending/done/failed/expired 四状态 + duration 秒计量、request_id 缺失
// 受理失败、未知状态 RawStatus、非 2xx）。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// xaiCreateAcceptedFixture 对齐 mockupstream 创建响应形态
// （media_xai_video_create_request_id，受理凭据 = request_id 字段，无 status）。
const xaiCreateAcceptedFixture = `{"request_id":"0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74"}`

// xaiPollPendingFixture 对齐 pending 形态。
const xaiPollPendingFixture = `{"request_id":"0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74","status":"pending"}`

// xaiPollDoneFixture 对齐 done 形态（video.url 绝对下载 URL + duration 秒）。
const xaiPollDoneFixture = `{"request_id":"0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74","status":"done","video":{"url":"https://vidgen.x.ai/v/0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74.mp4","duration":6,"respect_moderation":true}}`

// xaiPollFailedFixture 对齐 failed 形态（error{code,message}，code 词表
// invalid_argument/permission_denied/failed_precondition/service_unavailable/
// internal_error）。
const xaiPollFailedFixture = `{"request_id":"0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74","status":"failed","error":{"code":"invalid_argument","message":"duration must be between 1 and 15 seconds"}}`

// xaiPollExpiredFixture 对齐 expired 形态（上游原生终态）。
const xaiPollExpiredFixture = `{"request_id":"0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74","status":"expired"}`

func TestXaiVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("xai")
	if adapter == nil {
		t.Fatal("xai（grok imagine video）视频 adapter 应已注册")
	}
	if adapter.Provider() != "xai" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" XAI ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestXaiVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("xai").Capabilities()
	// /v1/videos/generations 请求面（契约 §6.1；官方文档 2026-10-04 核实）：
	// image（首帧，url/base64 双形态）、duration、generate_audio 原生支持；
	// negative_prompt/seed/n 请求面无对应字段 → 忽略 + 回显。
	if !caps.SupportsInputReference || !caps.SupportsSeconds || !caps.SupportsAudio {
		t.Fatalf("xai 应支持 input_reference/seconds/audio: %+v", caps)
	}
	if caps.SupportsNegativePrompt || caps.SupportsN || caps.SupportsSeed {
		t.Fatalf("xai 请求面无 negative_prompt/n/seed: %+v", caps)
	}
}

func TestXaiVideoAdapterArtifactAndCancelDeclarations(t *testing.T) {
	xai := VideoAdapterForProvider("xai")
	if !xai.ContentFromArtifact() {
		t.Fatal("xai 产物定位应为绝对 Artifact.ContentURL（video.url 临时直连 URL）")
	}
	// 契约 §6.1 面无取消端点 → 链上层本地收敛。
	if xai.SupportsCancel() {
		t.Fatal("xai §6.1 面无上游取消端点，SupportsCancel 应为 false")
	}
}

func TestXaiVideoAdapterCreate(t *testing.T) {
	adapter := xaiVideoAdapter{}
	seconds := 5.0
	reference := "data:image/png;base64,aGVsbG8="
	audio := false
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "grok-imagine-video-1.5",
			Prompt:         "一只猫在弹钢琴",
			Seconds:        &seconds,
			Size:           "1280x720",
			InputReference: &reference,
			Audio:          &audio,
		},
		ProviderOptions: map[string]any{
			"xai": map[string]any{"reference_audios": []any{"voice-1"}},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if output.Method != http.MethodPost || output.Path != "/v1/videos/generations" {
		t.Fatalf("create 出站形态 = %s %s, want POST /v1/videos/generations", output.Method, output.Path)
	}
	var body struct {
		Model         string          `json:"model"`
		Prompt        string          `json:"prompt"`
		Duration      float64         `json:"duration"`
		AspectRatio   string          `json:"aspect_ratio"`
		Resolution    string          `json:"resolution"`
		Image         string          `json:"image"`
		GenerateAudio bool            `json:"generate_audio"`
		Other         map[string]any `json:"-"`
	}
	var raw map[string]any
	if err := json.Unmarshal(output.Body, &raw); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body.Model != "grok-imagine-video-1.5" || body.Prompt != "一只猫在弹钢琴" {
		t.Fatalf("model/prompt = %q/%q", body.Model, body.Prompt)
	}
	// seconds 5 → duration 整数直传；size 1280x720 → aspect_ratio 16:9 +
	// resolution 720p（短边档 + 约分精确匹配）。
	if body.Duration != 5 {
		t.Fatalf("duration = %v, want 5", body.Duration)
	}
	if body.AspectRatio != "16:9" || body.Resolution != "720p" {
		t.Fatalf("size 换算 = %s/%s, want 16:9/720p", body.AspectRatio, body.Resolution)
	}
	// input_reference（data URL）→ image 字符串直传不剥离 base64 载荷；
	// audio=false → generate_audio 布尔直传。
	if body.Image != reference {
		t.Fatalf("image = %#v, want 原样直传 data URL", body.Image)
	}
	if body.GenerateAudio {
		t.Fatalf("generate_audio = true, want false（布尔直传）")
	}
	// provider_options 命中 xai 的子对象 deep-merge（契约 §6.1 范围外的
	// reference_audios 经 L3 通道可达）。
	if _, ok := raw["reference_audios"]; !ok {
		t.Fatalf("reference_audios 未合并: %s", output.Body)
	}
}

func TestXaiVideoAdapterCreateDurationRoundingAndOmission(t *testing.T) {
	adapter := xaiVideoAdapter{}
	// 零值省略面：仅 model+prompt 时 body 不携带 duration/aspect_ratio/
	// resolution/image/generate_audio（上游默认 16:9/480p/generate_audio=true）。
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "grok-imagine-video-1.5", Prompt: "p"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	for _, key := range []string{"duration", "aspect_ratio", "resolution", "image", "generate_audio"} {
		if _, present := body[key]; present {
			t.Fatalf("零值字段 %s 应省略: %s", key, output.Body)
		}
	}
	// Audio 为 nil（未传）同样省略 generate_audio。
	audio := true
	output, err = adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "grok-imagine-video-1.5", Prompt: "p", Audio: &audio},
	})
	if err != nil {
		t.Fatalf("create with audio: %v", err)
	}
	body = map[string]any{}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create with audio body: %v", err)
	}
	if body["generate_audio"] != true {
		t.Fatalf("generate_audio = %#v, want true", body["generate_audio"])
	}
	// seconds 小数形态 → duration 四舍五入到最近整数秒（区间 1–15 由上游裁决）。
	fractional := 5.6
	output, err = adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "grok-imagine-video-1.5", Prompt: "p", Seconds: &fractional},
	})
	if err != nil {
		t.Fatalf("create fractional: %v", err)
	}
	body = map[string]any{}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create fractional body: %v", err)
	}
	if body["duration"] != float64(6) {
		t.Fatalf("duration = %#v, want 6（5.6 四舍五入）", body["duration"])
	}
}

func TestXaiVideoAspectRatioVocabulary(t *testing.T) {
	adapter := xaiVideoAdapter{}
	// 七值词表正例（约分精确匹配，契约 §6.1 aspect_ratio 词表；resolution
	// 按短边最近档 ≥900→1080p / ≥600→720p / 其余→480p）。
	positive := []struct {
		size, ratio, resolution string
	}{
		{"1280x720", "16:9", "720p"},
		{"720x1280", "9:16", "720p"},
		{"960x960", "1:1", "1080p"},
		{"1280x960", "4:3", "1080p"},
		{"960x1280", "3:4", "1080p"},
		{"960x640", "3:2", "720p"},
		{"640x960", "2:3", "720p"},
		{"600x800", "3:4", "720p"},
		{"599x800", "599:800", "-"},  // 反例：词表外宽高比
		{"480x640", "3:4", "480p"},   // 短边 480 → 480p 下限档
		{"1920x1080", "16:9", "1080p"},
	}
	for _, testCase := range positive {
		aspectRatio, resolution, err := xaiSizeToAspectRatioResolution(testCase.size)
		if testCase.resolution == "-" {
			if err == nil {
				t.Fatalf("size %s 应拒绝（词表外宽高比）", testCase.size)
			}
			if !errors.Is(err, ErrParamUnsupported) {
				t.Fatalf("size %s 应为 ErrParamUnsupported: %v", testCase.size, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("size %s 换算失败: %v", testCase.size, err)
		}
		if aspectRatio != testCase.ratio || resolution != testCase.resolution {
			t.Fatalf("size %s = %s/%s, want %s/%s", testCase.size, aspectRatio, resolution, testCase.ratio, testCase.resolution)
		}
	}
	// 词表外宽高比在 Create 面同样本地 400（不近似贴合，契约 §2.4 规则 2）。
	if _, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "grok-imagine-video-1.5", Prompt: "p", Size: "1279x720"},
	}); !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("词表外 aspect_ratio 应为 ErrParamUnsupported: %v", err)
	}
	// 5:4 等词表外常见比同样拒绝（xai 七值词表无 5:4；336x144 约分 7:3
	// 亦非词表值——词表匹配以约分后元组精确相等为准，21:9 元组属
	// volcengine 词表）。
	if _, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "grok-imagine-video-1.5", Prompt: "p", Size: "1000x800"},
	}); !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("5:4 非 xai 词表值应拒绝: %v", err)
	}
}

func TestXaiVideoAdapterPollRequest(t *testing.T) {
	method, path, body := VideoAdapterForProvider("xai").BuildPollRequest("0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74")
	if method != http.MethodGet || path != "/v1/videos/0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74" {
		t.Fatalf("poll 出站形态 = %s %s", method, path)
	}
	if body != nil {
		t.Fatalf("poll 不得携带请求体: %s", body)
	}
}

func TestXaiVideoAdapterParsePollResponse(t *testing.T) {
	adapter := xaiVideoAdapter{}

	// 创建响应：受理凭据确立（无 status 字段）→ in_progress（pending 同义）。
	ir, err := adapter.ParsePollResponse(http.StatusOK, []byte(xaiCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("create parse: %v", err)
	}
	if ir.UpstreamJobID != "0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74" || ir.Status != JobStatusInProgress {
		t.Fatalf("create parse = %s/%s, want uuid/in_progress", ir.UpstreamJobID, ir.Status)
	}

	// pending → in_progress。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(xaiPollPendingFixture))
	if err != nil {
		t.Fatalf("pending parse: %v", err)
	}
	if ir.Status != JobStatusInProgress {
		t.Fatalf("pending parse status = %s, want in_progress", ir.Status)
	}

	// done → completed：video.url 冻结进 Artifact.ContentURL，video.duration
	// 抽入 OutputVideoSeconds（契约 §6.1 时长计量基源）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(xaiPollDoneFixture))
	if err != nil {
		t.Fatalf("done parse: %v", err)
	}
	if ir.Status != JobStatusCompleted ||
		ir.Artifact.ContentURL != "https://vidgen.x.ai/v/0b9f4d3a-7c2e-4f61-9a88-2c5d1e6b0f74.mp4" {
		t.Fatalf("done parse = %s/%s", ir.Status, ir.Artifact.ContentURL)
	}
	if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds != 6 {
		t.Fatalf("done duration 计量 = %#v, want 6", ir.Usage.OutputVideoSeconds)
	}

	// done 缺 video.url：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"done"}`)); err == nil {
		t.Fatal("done 缺 video.url 应报错（不猜测产物可达性）")
	}

	// done 无 duration：状态照常 completed，计量留空由链上 §2.8 兜底
	//（usage_missing，不猜测）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/v/x.mp4"}}`))
	if err != nil {
		t.Fatalf("done without duration: %v", err)
	}
	if ir.Status != JobStatusCompleted || ir.Usage.OutputVideoSeconds != nil {
		t.Fatalf("done without duration = %s/%#v", ir.Status, ir.Usage.OutputVideoSeconds)
	}

	// failed → failed，错误摘要取 error{code,message}。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(xaiPollFailedFixture))
	if err != nil {
		t.Fatalf("failed parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil ||
		ir.Error.Code != "invalid_argument" || ir.Error.Message != "duration must be between 1 and 15 seconds" {
		t.Fatalf("failed parse = %s/%+v", ir.Status, ir.Error)
	}

	// failed 但 error 对象缺席：以 status 原值兜底，不丢弃终态。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"failed"}`))
	if err != nil {
		t.Fatalf("failed-without-error parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Code != "failed" {
		t.Fatalf("failed 无 error 兜底错误: %s/%+v", ir.Status, ir.Error)
	}

	// expired → expired（上游原生终态直接收敛，契约 §6.1/§2.6；终态语义
	// Terminal() 覆盖，不产生 usage 行）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(xaiPollExpiredFixture))
	if err != nil {
		t.Fatalf("expired parse: %v", err)
	}
	if ir.Status != JobStatusExpired || !ir.Status.Terminal() {
		t.Fatalf("expired parse status = %s, want expired 终态", ir.Status)
	}

	// 创建响应缺 request_id（status 也缺席）：受理失败协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{}`)); err == nil {
		t.Fatal("缺 request_id 应报受理失败")
	}

	// 未知状态 → in_progress + RawStatus（不猜测失败，契约 §2.6 归一规则）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"canceled"}`))
	if err != nil {
		t.Fatalf("unknown status parse: %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.RawStatus != "canceled" {
		t.Fatalf("unknown status = %s/%s, want in_progress/canceled", ir.Status, ir.RawStatus)
	}

	// 非 2xx → UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）。
	var statusErr *UpstreamStatusError
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{}`))
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应为 UpstreamStatusError: %v", err)
	}
	// 未知形态防御：done 带 video.url 空白串同样拒绝。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"done","video":{"url":"  "}}`)); err == nil || !strings.Contains(err.Error(), "video.url") {
		t.Fatalf("空白 video.url 应报协议违约: %v", err)
	}
}
