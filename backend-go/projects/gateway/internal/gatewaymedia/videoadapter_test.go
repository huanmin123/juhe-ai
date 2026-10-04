package gatewaymedia

// openai 视频 adapter 测试（契约 §4.3 报文，fixture 形态与 mockupstream
// videoObject 同源派生——Mock 上游按同一契约实现）：创建报文构造（含
// seconds 字符串形态与 provider_options 合并）、轮询/下载/取消请求构造、
// 轮询响应归一（含未知状态、404/5xx、completed 缺 content 的协议违约）。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// pollQueuedFixture 对齐 mockupstream 创建/首轮响应形态（media_video_ok_poll3
// 创建返回）。
const pollQueuedFixture = `{"id":"video_0123456789abcdef","object":"video","status":"queued","progress":0,"model":"sora-2","prompt":"a cat","seconds_length":4,"size":"1280x720"}`

const pollInProgressFixture = `{"id":"video_0123456789abcdef","object":"video","status":"in_progress","progress":66,"model":"sora-2","prompt":"a cat","seconds_length":4,"size":"1280x720"}`

// pollCompletedFixture 对齐 mockupstream completed 形态（content 数组提供下载
// 定位，media_video_ok_poll3 第三轮）。
const pollCompletedFixture = `{"id":"video_0123456789abcdef","object":"video","status":"completed","progress":100,"model":"sora-2","prompt":"a cat","seconds_length":4,"size":"1280x720","content":[{"status":"completed","type":"video/mp4","url":"https://upstream.test/v1/videos/video_0123456789abcdef/content"}]}`

// pollFailedFixture 对齐 mockupstream failed 形态（media_video_fail_after_accept
// 终轮，error code/message）。
const pollFailedFixture = `{"id":"video_0123456789abcdef","object":"video","status":"failed","progress":0,"model":"sora-2","prompt":"a cat","seconds_length":4,"size":"1280x720","error":{"code":"video_generation_failed","message":"Video generation failed after the request was accepted"}}`

// createError400Fixture 对齐 mockupstream media_video_create_400_bad_size 错误包
//（受理前 4xx，链上层不换账户）。
const createError400Fixture = `{"error":{"message":"Invalid size: value must be one of 1280x720, 720x1280, 960x960","type":"invalid_request_error","code":"invalid_size"}}`

func TestVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")
	if adapter == nil {
		t.Fatal("openai 视频 adapter 应已注册")
	}
	if adapter.Provider() != "openai" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider("OpenAI ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
	// M3 回填池接入后的未注册示例：anthropic 视频面未回填（媒体设计 §9，
	// 未接入返回 nil 不静默回退；xai 已随 M3 回填池注册——见
	// TestXaiVideoAdapterRegistry）。
	if VideoAdapterForProvider("anthropic") != nil {
		t.Fatal("未注册 provider 应返回 nil（回填后才接入，不静默回退）")
	}
}

func TestOpenAIVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("openai").Capabilities()
	// sora 现状（契约 §2.2/§4.3）：seconds/size/n/input_reference 原生支持；
	// negative_prompt/seed/audio 请求面无该参数 → 忽略 + 回显。
	if !caps.SupportsSeconds || !caps.SupportsN || !caps.SupportsInputReference {
		t.Fatalf("sora 应支持 seconds/n/input_reference: %+v", caps)
	}
	if caps.SupportsNegativePrompt || caps.SupportsSeed || caps.SupportsAudio {
		t.Fatalf("sora 请求面无 negative_prompt/seed/audio: %+v", caps)
	}
}

func TestOpenAIVideoAdapterCreateRequest(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")
	reference := "https://example.com/first-frame.png"
	params, err := ParseVideoParams(map[string]any{
		"model":           "sora-2",
		"prompt":          "a cat surfing",
		"seconds":         "8",
		"size":            "720x1280",
		"n":               float64(2),
		"input_reference": reference,
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{Params: params})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	if out.Method != http.MethodPost || out.Path != "/v1/videos" {
		t.Fatalf("method/path = %s %s", out.Method, out.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("body 不是 JSON: %v", err)
	}
	if body["model"] != "sora-2" || body["prompt"] != "a cat surfing" {
		t.Fatalf("L1 字段 = %v", body)
	}
	if got := body["seconds"]; got != "8" {
		t.Fatalf("seconds 应为 openai 原生字符串形态, got %T %v", got, got) // 契约 §4.3 "seconds":"4"
	}
	if body["size"] != "720x1280" || body["n"] != float64(2) {
		t.Fatalf("size/n = %v", body)
	}
	if body["input_reference"] != reference {
		t.Fatalf("input_reference = %v", body["input_reference"])
	}
}

func TestOpenAIVideoAdapterCreateMinimalBody(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")
	params, err := ParseVideoParams(map[string]any{
		"model":  "sora-2",
		"prompt": "a cat",
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{Params: params})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("body 不是 JSON: %v", err)
	}
	if len(body) != 3 {
		t.Fatalf("最小报文应仅 model/prompt/n: %v", body)
	}
	if body["n"] != float64(1) {
		t.Fatalf("n 默认显式携带 1: %v", body["n"])
	}
	// 被判 ignored 的词表外字段不进报文。
	if _, exists := body["negative_prompt"]; exists {
		t.Fatal("negative_prompt 不应进 openai 报文")
	}
}

func TestOpenAIVideoAdapterCreateMergesProviderOptions(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")
	params, err := ParseVideoParams(map[string]any{
		"model":  "sora-2",
		"prompt": "a cat",
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: params,
		ProviderOptions: map[string]any{
			"openai": map[string]any{"prompt": "a vendor cat", "vendor_extra": float64(1)},
			"gemini": map[string]any{"aspectRatio": "16:9"}, // 未命中：忽略
		},
	})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("body 不是 JSON: %v", err)
	}
	if body["prompt"] != "a vendor cat" {
		t.Fatalf("L3 同名覆盖 L2 失败: %v", body["prompt"])
	}
	if body["vendor_extra"] != float64(1) {
		t.Fatalf("未知键应透传: %v", body["vendor_extra"])
	}
	if _, exists := body["aspectRatio"]; exists {
		t.Fatal("未命中 provider 子对象不应进报文")
	}
}

func TestOpenAIVideoAdapterBuildRequests(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")
	const id = "video_0123456789abcdef"

	method, path, body := adapter.BuildPollRequest(id)
	if method != http.MethodGet || path != "/v1/videos/"+id || body != nil {
		t.Fatalf("poll = %s %s %v", method, path, body)
	}
	method, path = adapter.BuildContentRequest(id)
	if method != http.MethodGet || path != "/v1/videos/"+id+"/content" {
		t.Fatalf("content = %s %s", method, path)
	}
	method, path = adapter.BuildCancelRequest(id)
	if method != http.MethodDelete || path != "/v1/videos/"+id {
		t.Fatalf("cancel = %s %s", method, path)
	}
}

func TestOpenAIVideoAdapterParsePollResponse(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")

	cases := []struct {
		name      string
		statusCode int
		body      string
		check     func(*testing.T, *MediaJobIR)
	}{
		{
			name: "queued（创建受理形态）", statusCode: 200, body: pollQueuedFixture,
			check: func(t *testing.T, ir *MediaJobIR) {
				if ir.Status != JobStatusQueued || ir.Kind != JobKindVideo {
					t.Fatalf("status/kind = %s/%s", ir.Status, ir.Kind)
				}
				if ir.UpstreamJobID != "video_0123456789abcdef" {
					t.Fatalf("upstream id = %s", ir.UpstreamJobID)
				}
				if ir.Progress == nil || *ir.Progress != 0 {
					t.Fatalf("progress = %v", ir.Progress)
				}
				if ir.Usage.OutputVideoSeconds != nil {
					t.Fatal("非终态不计量（失败不虚计同源）")
				}
			},
		},
		{
			name: "in_progress（轮询推进形态）", statusCode: 200, body: pollInProgressFixture,
			check: func(t *testing.T, ir *MediaJobIR) {
				if ir.Status != JobStatusInProgress || ir.RawStatus != "" {
					t.Fatalf("status/raw = %s/%q", ir.Status, ir.RawStatus)
				}
				if ir.Progress == nil || *ir.Progress != 66 {
					t.Fatalf("progress = %v", ir.Progress)
				}
			},
		},
		{
			name: "completed（content 定位 + 秒数计量）", statusCode: 200, body: pollCompletedFixture,
			check: func(t *testing.T, ir *MediaJobIR) {
				if ir.Status != JobStatusCompleted {
					t.Fatalf("status = %s", ir.Status)
				}
				want := "https://upstream.test/v1/videos/video_0123456789abcdef/content"
				if ir.Artifact.ContentURL != want {
					t.Fatalf("content url = %s", ir.Artifact.ContentURL)
				}
				if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds != 4 {
					t.Fatalf("seconds = %v", ir.Usage.OutputVideoSeconds)
				}
			},
		},
		{
			name: "failed（错误对象透传）", statusCode: 200, body: pollFailedFixture,
			check: func(t *testing.T, ir *MediaJobIR) {
				if ir.Status != JobStatusFailed {
					t.Fatalf("status = %s", ir.Status)
				}
				if ir.Error == nil || ir.Error.Code != "video_generation_failed" ||
					!strings.Contains(ir.Error.Message, "failed after the request was accepted") {
					t.Fatalf("error = %+v", ir.Error)
				}
				if ir.Usage.OutputVideoSeconds != nil {
					t.Fatal("失败任务不虚计")
				}
			},
		},
		{
			name: "未知状态归一 in_progress 并记录原始值", statusCode: 200,
			body: `{"id":"video_x","object":"video","status":"running","progress":10}`,
			check: func(t *testing.T, ir *MediaJobIR) {
				if ir.Status != JobStatusInProgress || ir.RawStatus != "running" {
					t.Fatalf("status/raw = %s/%q", ir.Status, ir.RawStatus)
				}
			},
		},
	}
	for _, c := range cases {
		ir, err := adapter.ParsePollResponse(c.statusCode, []byte(c.body))
		if err != nil {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		c.check(t, ir)
	}
}

func TestOpenAIVideoAdapterParsePollResponseErrors(t *testing.T) {
	adapter := VideoAdapterForProvider("openai")

	// 非 2xx：UpstreamStatusError 保留状态码与响应体（受理后 404 收敛 / 5xx
	// 故障由链上层裁决，不在此伪造终态——契约 §2.6）。
	for _, statusCode := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		_, err := adapter.ParsePollResponse(statusCode, []byte(createError400Fixture))
		if err == nil {
			t.Fatalf("%d 应报错", statusCode)
		}
		var statusErr *UpstreamStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != statusCode {
			t.Fatalf("%d: 错误应为 *UpstreamStatusError, got %T %v", statusCode, err, err)
		}
		if !strings.Contains(statusErr.Body, "invalid_size") {
			t.Fatalf("响应体证据应保留: %s", statusErr.Body)
		}
	}

	// 坏 JSON / 缺 id。
	if _, err := adapter.ParsePollResponse(200, []byte(`{not-json`)); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := adapter.ParsePollResponse(200, []byte(`{"object":"video","status":"queued"}`)); err == nil ||
		!strings.Contains(err.Error(), "id") {
		t.Fatalf("缺受理凭据 id 应报错: %v", err)
	}
	// completed 但缺 content 定位：上游协议违约，不猜产物可达性。
	noContent := `{"id":"video_x","object":"video","status":"completed","progress":100}`
	if _, err := adapter.ParsePollResponse(200, []byte(noContent)); err == nil ||
		!strings.Contains(err.Error(), "content") {
		t.Fatalf("completed 缺 content 应报错: %v", err)
	}
}
