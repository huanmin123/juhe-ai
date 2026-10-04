package gatewaymedia

// qwen（万相 wan 系）adapter 测试（契约 §10.1 报文，fixture 形态与
// mockupstream qwenTaskBody 同源派生——Mock 上游按同一契约实现）：视频创建
// 报文构造（prompt→input.prompt、negative_prompt→input.negative_prompt、
// input_reference url/base64 双形态直传 input.img_url、size WxH→W*H 星号
// 格式转换、seconds→parameters.duration 数值直传、seed 直传、
// provider_options 嵌套合并）、异步头声明（X-DashScope-Async）、轮询请求
// 构造与产物定位/取消声明、轮询响应归一（PENDING/RUNNING/SUCCEEDED/
// FAILED、usage JSON 字符串双形态解析、succeeded 缺 video_url 的协议违约、
// 未知状态 RawStatus、非 2xx）。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// qwenCreateAcceptedFixture 对齐 mockupstream 创建响应形态
// （media_qwen_create_pending，受理凭据 = output.task_id，
// output.task_status=PENDING）。
const qwenCreateAcceptedFixture = `{"output":{"task_id":"0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c","task_status":"PENDING"},"request_id":"req-0385dc79"}`

// qwenPollRunningFixture 对齐 RUNNING 形态。
const qwenPollRunningFixture = `{"output":{"task_id":"0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c","task_status":"RUNNING"},"request_id":"req-0385dc79"}`

// qwenPollSucceededFixture 对齐 SUCCEEDED 形态（output.video_url 为绝对下载
// URL；usage 为 DashScope 的 JSON 字符串形态——字符串内再嵌一层 JSON，
// wan2.5 及以下字段族 video_duration/video_count）。
const qwenPollSucceededFixture = `{"output":{"task_id":"0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c","task_status":"SUCCEEDED","video_url":"https://dashscope-result-oss.oss-cn-wulanchabu.aliyuncs.com/wan2.2-t2v-plus/xxx.mp4?Expires=1760000000"},"usage":"{\"video_duration\":5,\"video_count\":1}","request_id":"req-0385dc79"}`

// qwenPollFailedFixture 对齐 FAILED 形态（顶层 code/message）。
const qwenPollFailedFixture = `{"output":{"task_id":"0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c","task_status":"FAILED"},"code":"InternalError","message":"wanx video generation failed","request_id":"req-0385dc79"}`

func TestQwenVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("qwen")
	if adapter == nil {
		t.Fatal("qwen（万相）视频 adapter 应已注册")
	}
	if adapter.Provider() != "qwen" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" Qwen ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestQwenVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("qwen").Capabilities()
	// video-synthesis 请求面（契约 §10.1；官方 legacy 万相 API 参考
	// 2026-10-04 核实）：input.negative_prompt（官方示例章节"所有模型"）、
	// parameters.seed、parameters.duration、input.img_url（图生视频形态，
	// url/base64 双形态）原生支持；请求面无"生成音频"开关与多段参数
	//（input.audio_url 是输入素材字段非开关，经 L3 传递）→ audio/n 忽略 +
	// 回显。
	if !caps.SupportsInputReference || !caps.SupportsSeconds || !caps.SupportsSeed || !caps.SupportsNegativePrompt {
		t.Fatalf("qwen 应支持 input_reference/seconds/seed/negative_prompt: %+v", caps)
	}
	if caps.SupportsN || caps.SupportsAudio {
		t.Fatalf("qwen 请求面无 n/audio 开关: %+v", caps)
	}
}

func TestQwenVideoAdapterArtifactAndCancelDeclarations(t *testing.T) {
	qwen := VideoAdapterForProvider("qwen")
	if !qwen.ContentFromArtifact() {
		t.Fatal("qwen 产物定位应为绝对 Artifact.ContentURL（output.video_url）")
	}
	// 契约 §10.1 回填面无取消端点（M3 裁决）→ 链上层本地收敛。
	if qwen.SupportsCancel() {
		t.Fatal("qwen §10.1 面无上游取消端点，SupportsCancel 应为 false")
	}
}

func TestQwenVideoAdapterCreate(t *testing.T) {
	adapter := qwenVideoAdapter{}
	seconds := 5.0
	seed := int64(42)
	reference := "https://example.com/frame.png"
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "wan2.2-t2v-plus",
			Prompt:         "一只猫在弹钢琴",
			Seconds:        &seconds,
			Size:           "1920x1080",
			Seed:           &seed,
			NegativePrompt: "模糊",
			InputReference: &reference,
		},
		ProviderOptions: map[string]any{
			"qwen": map[string]any{"parameters": map[string]any{"prompt_extend": true}},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if output.Method != http.MethodPost || output.Path != "/api/v1/services/aigc/video-generation/video-synthesis" {
		t.Fatalf("create 出站形态 = %s %s, want POST /api/v1/services/aigc/video-generation/video-synthesis", output.Method, output.Path)
	}
	// DashScope 异步任务约定（契约 §10.1）：创建请求必带 X-DashScope-Async。
	if output.ExtraHeaders["X-DashScope-Async"] != "enable" {
		t.Fatalf("create ExtraHeaders = %v, want X-DashScope-Async: enable", output.ExtraHeaders)
	}
	var body struct {
		Model string `json:"model"`
		Input struct {
			Prompt         string `json:"prompt"`
			NegativePrompt string `json:"negative_prompt"`
			ImgURL         string `json:"img_url"`
		} `json:"input"`
		Parameters struct {
			Size         string  `json:"size"`
			Duration     float64 `json:"duration"`
			Seed         float64 `json:"seed"`
			PromptExtend bool    `json:"prompt_extend"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body.Model != "wan2.2-t2v-plus" {
		t.Fatalf("model = %q", body.Model)
	}
	if body.Input.Prompt != "一只猫在弹钢琴" {
		t.Fatalf("input.prompt = %q", body.Input.Prompt)
	}
	if body.Input.NegativePrompt != "模糊" {
		t.Fatalf("input.negative_prompt = %q", body.Input.NegativePrompt)
	}
	// input_reference（公网 url 形态）→ input.img_url 原样直传（官方双形态，
	// 网关不代为下载）。
	if body.Input.ImgURL != reference {
		t.Fatalf("input.img_url = %q, want 原样直传公网 URL", body.Input.ImgURL)
	}
	// size 1920x1080 → parameters.size "1920*1080"（x → * 星号格式转换，
	// 档位合法性由上游裁决）。
	if body.Parameters.Size != "1920*1080" {
		t.Fatalf("parameters.size = %q, want 1920*1080", body.Parameters.Size)
	}
	// seconds → parameters.duration 数值直传；seed 整数直传。
	if body.Parameters.Duration != 5 {
		t.Fatalf("parameters.duration = %v, want 5", body.Parameters.Duration)
	}
	if body.Parameters.Seed != 42 {
		t.Fatalf("parameters.seed = %v, want 42", body.Parameters.Seed)
	}
	// provider_options 命中 qwen 的子对象嵌套 deep-merge（parameters 键内
	// 追加 prompt_extend，不整体替换既有 parameters 键）。
	if !body.Parameters.PromptExtend {
		t.Fatalf("parameters.prompt_extend 未合并: %s", output.Body)
	}
}

func TestQwenVideoAdapterCreateBase64ReferenceAndOmittedOptional(t *testing.T) {
	adapter := qwenVideoAdapter{}
	reference := "data:image/png;base64,aGVsbG8="
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "wan2.2-t2v-plus",
			Prompt:         "首帧图生视频",
			InputReference: &reference,
			Size:           "832x480",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body struct {
		Input      map[string]any `json:"input"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	// base64 data URL 形态 input.img_url 同样直传（官方图生视频双形态）。
	if body.Input["img_url"] != reference {
		t.Fatalf("input.img_url = %#v, want 原样直传 data URL", body.Input["img_url"])
	}
	// negative_prompt 未传不进报文；size 832x480 → "832*480"。
	if _, present := body.Input["negative_prompt"]; present {
		t.Fatalf("negative_prompt 未传不得进报文: %s", output.Body)
	}
	if body.Parameters["size"] != "832*480" {
		t.Fatalf("parameters.size = %#v, want 832*480", body.Parameters["size"])
	}
	// seconds/seed 未传 → parameters 无对应键。
	if _, present := body.Parameters["duration"]; present {
		t.Fatalf("seconds 未传不得进报文: %s", output.Body)
	}
	if _, present := body.Parameters["seed"]; present {
		t.Fatalf("seed 未传不得进报文: %s", output.Body)
	}
}

func TestQwenVideoAdapterCreateSizeFormValidation(t *testing.T) {
	adapter := qwenVideoAdapter{}
	// 公共 size 非 WxH 形态（归一层漏检防御）→ 参数类 400（契约 §2.4
	// 规则 2）。
	for _, size := range []string{"16:9", "1080p", "1280x720x1", "0x720", "ax720"} {
		if _, err := adapter.Create(context.Background(), VideoCreateInput{
			Params: NormalizedVideoParams{Model: "wan2.2-t2v-plus", Prompt: "p", Size: size},
		}); err == nil {
			t.Fatalf("size %s 应拒绝", size)
		}
	}
	if _, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "wan2.2-t2v-plus", Prompt: "p", Size: "16:9"},
	}); !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("非 WxH size 应为 ErrParamUnsupported: %v", err)
	}
	// input_reference 空白串 → 参数类 400。
	blank := "  "
	if _, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "wan2.2-t2v-plus", Prompt: "p", InputReference: &blank},
	}); !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("空白 input_reference 应为 ErrParamUnsupported: %v", err)
	}
}

func TestQwenVideoAdapterPollRequest(t *testing.T) {
	method, path, body := VideoAdapterForProvider("qwen").BuildPollRequest("0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c")
	if method != http.MethodGet || path != "/api/v1/tasks/0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c" {
		t.Fatalf("poll 出站形态 = %s %s", method, path)
	}
	if body != nil {
		t.Fatalf("poll 不得携带请求体: %s", body)
	}
}

func TestQwenVideoAdapterParsePollResponse(t *testing.T) {
	adapter := qwenVideoAdapter{}

	// 创建响应：受理凭据确立，output.task_status=PENDING → queued。
	ir, err := adapter.ParsePollResponse(http.StatusOK, []byte(qwenCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("create parse: %v", err)
	}
	if ir.UpstreamJobID != "0385dc79-5ff8-4d82-bcb6-0f5f9d2e1a7c" || ir.Status != JobStatusQueued {
		t.Fatalf("create parse = %s/%s, want 0385dc79-.../queued", ir.UpstreamJobID, ir.Status)
	}
	// 创建响应（受理面）无 usage —— 不产生秒计量。
	if ir.Usage.OutputVideoSeconds != nil {
		t.Fatalf("create parse 不得产生秒计量: %+v", ir.Usage)
	}

	// RUNNING → in_progress。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(qwenPollRunningFixture))
	if err != nil {
		t.Fatalf("running parse: %v", err)
	}
	if ir.Status != JobStatusInProgress {
		t.Fatalf("running parse status = %s, want in_progress", ir.Status)
	}

	// SUCCEEDED → completed：output.video_url 冻结进 Artifact.ContentURL；
	// usage JSON 字符串解析出 video_duration=5 进 OutputVideoSeconds。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(qwenPollSucceededFixture))
	if err != nil {
		t.Fatalf("succeeded parse: %v", err)
	}
	if ir.Status != JobStatusCompleted ||
		!strings.HasPrefix(ir.Artifact.ContentURL, "https://dashscope-result-oss.") {
		t.Fatalf("succeeded parse = %s/%s", ir.Status, ir.Artifact.ContentURL)
	}
	if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds != 5 {
		t.Fatalf("usage 解析 = %+v, want video_duration 5", ir.Usage.OutputVideoSeconds)
	}

	// usage 对象形态（官方文档示例展示的另一种呈现）同样可解析（双形态
	// 兼容，wan2.6 字段族 output_video_duration 优先于 duration）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-1","task_status":"SUCCEEDED","video_url":"https://oss/x.mp4"},"usage":{"duration":10,"output_video_duration":8,"SR":720}}`))
	if err != nil {
		t.Fatalf("usage object-form parse: %v", err)
	}
	if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds != 8 {
		t.Fatalf("usage object-form 解析 = %+v, want output_video_duration 8", ir.Usage.OutputVideoSeconds)
	}

	// usage 解析失败不失败（无正值时长字段 → nil，链上走 §2.8 兜底）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-2","task_status":"SUCCEEDED","video_url":"https://oss/y.mp4"},"usage":"{\"video_count\":1}"}`))
	if err != nil {
		t.Fatalf("usage unparsable parse: %v", err)
	}
	if ir.Status != JobStatusCompleted || ir.Usage.OutputVideoSeconds != nil {
		t.Fatalf("usage 无时长字段应不填秒: %s/%+v", ir.Status, ir.Usage.OutputVideoSeconds)
	}

	// FAILED → failed，错误摘要取顶层 code/message。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(qwenPollFailedFixture))
	if err != nil {
		t.Fatalf("failed parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil ||
		ir.Error.Code != "InternalError" || ir.Error.Message != "wanx video generation failed" {
		t.Fatalf("failed parse = %s/%+v", ir.Status, ir.Error)
	}

	// FAILED 但 code/message 缺席：以 status 原值兜底，不丢弃终态。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-3","task_status":"FAILED"}}`))
	if err != nil {
		t.Fatalf("failed-without-code parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Code != "FAILED" {
		t.Fatalf("failed 无 code 兜底错误: %s/%+v", ir.Status, ir.Error)
	}

	// SUCCEEDED 缺 video_url：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-4","task_status":"SUCCEEDED"}}`)); err == nil {
		t.Fatal("SUCCEEDED 缺 video_url 应报错（不猜测产物可达性）")
	}

	// 缺受理凭据：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_status":"RUNNING"}}`)); err == nil {
		t.Fatal("缺 output.task_id 应报错")
	}

	// 未知状态（官方词表另有 CANCELED/UNKNOWN，轮询面未在契约 §2.6 回填）
	// → in_progress + RawStatus（契约 §2.6 归一规则：不猜测失败；同
	// volcengine expired 先例）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-5","task_status":"CANCELED"}}`))
	if err != nil {
		t.Fatalf("unknown status parse: %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.RawStatus != "CANCELED" {
		t.Fatalf("unknown status = %s/%s, want in_progress/CANCELED", ir.Status, ir.RawStatus)
	}

	// 非 2xx → UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）。
	var statusErr *UpstreamStatusError
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{}`))
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应为 UpstreamStatusError: %v", err)
	}
	// 未知形态防御：SUCCEEDED 带 video_url 空白串同样拒绝。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"output":{"task_id":"t-6","task_status":"SUCCEEDED","video_url":"  "}}`)); err == nil || !strings.Contains(err.Error(), "video_url") {
		t.Fatalf("空白 video_url 应报协议违约: %v", err)
	}
}
