package gatewaymedia

// volcengine（Seedance）adapter 测试（契约 §9.1 报文，fixture 形态与
// mockupstream volcengineQueryBody 同源派生——Mock 上游按同一契约实现）：
// 视频创建报文构造（prompt→content[].text、input_reference url/base64 双
// 形态直传、size→resolution+ratio 档位换算与词表外 400、seconds→duration
// 数值直传、seed 直传、provider_options 合并）、轮询请求构造与产物定位/
// 取消声明、轮询响应归一（queued/running/succeeded/failed、succeeded 缺
// video_url 的协议违约、未知状态 RawStatus、非 2xx）。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// volcengineCreateAcceptedFixture 对齐 mockupstream 创建响应形态
// （media_volcengine_create_ok，受理凭据 = id 字段，status=queued）。
const volcengineCreateAcceptedFixture = `{"id":"cgt-20261004101122abcdef","status":"queued"}`

// volcenginePollRunningFixture 对齐 running 形态。
const volcenginePollRunningFixture = `{"id":"cgt-20261004101122abcdef","status":"running"}`

// volcenginePollSucceededFixture 对齐 succeeded 形态（content.video_url 为
// 绝对下载 URL）。
const volcenginePollSucceededFixture = `{"id":"cgt-20261004101122abcdef","status":"succeeded","content":{"video_url":"https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks/cgt-20261004101122abcdef/content"}}`

// volcenginePollFailedFixture 对齐 failed 形态（error{code,message}）。
const volcenginePollFailedFixture = `{"id":"cgt-20261004101122abcdef","status":"failed","error":{"code":"InternalServiceError","message":"video generation failed"}}`

func TestVolcengineVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("volcengine")
	if adapter == nil {
		t.Fatal("volcengine（seedance）视频 adapter 应已注册")
	}
	if adapter.Provider() != "volcengine" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" Volcengine ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestVolcengineVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("volcengine").Capabilities()
	// contents/generations/tasks 请求面（契约 §9.1；官方 API 参考 2026-10-04
	// 核实）：content[].image_url（url/base64 双形态）、duration、seed 原生
	// 支持；negative_prompt/audio（generate_audio 仅 2.0/1.5 pro，M3 目录
	// 1.0 pro 不声明）/n 请求面无对应字段 → 忽略 + 回显。
	if !caps.SupportsInputReference || !caps.SupportsSeconds || !caps.SupportsSeed {
		t.Fatalf("volcengine 应支持 input_reference/seconds/seed: %+v", caps)
	}
	if caps.SupportsNegativePrompt || caps.SupportsN || caps.SupportsAudio {
		t.Fatalf("volcengine 请求面无 negative_prompt/n/audio: %+v", caps)
	}
}

func TestVolcengineVideoAdapterArtifactAndCancelDeclarations(t *testing.T) {
	volcengine := VideoAdapterForProvider("volcengine")
	if !volcengine.ContentFromArtifact() {
		t.Fatal("volcengine 产物定位应为绝对 Artifact.ContentURL（content.video_url）")
	}
	// 契约 §9.1 回填面无取消端点（M3 裁决）→ 链上层本地收敛。
	if volcengine.SupportsCancel() {
		t.Fatal("volcengine §9.1 面无上游取消端点，SupportsCancel 应为 false")
	}
}

func TestVolcengineVideoAdapterCreate(t *testing.T) {
	adapter := volcengineVideoAdapter{}
	seconds := 5.0
	seed := int64(42)
	reference := "data:image/png;base64,aGVsbG8="
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "doubao-seedance-1-0-pro-250528",
			Prompt:         "一只猫在弹钢琴",
			Seconds:        &seconds,
			Size:           "1280x720",
			Seed:           &seed,
			InputReference: &reference,
		},
		ProviderOptions: map[string]any{
			"volcengine": map[string]any{"camera_fixed": true},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if output.Method != http.MethodPost || output.Path != "/api/v3/contents/generations/tasks" {
		t.Fatalf("create 出站形态 = %s %s, want POST /api/v3/contents/generations/tasks", output.Method, output.Path)
	}
	var body struct {
		Model       string           `json:"model"`
		Content     []map[string]any `json:"content"`
		Resolution  string           `json:"resolution"`
		Ratio       string           `json:"ratio"`
		Duration    float64          `json:"duration"`
		Seed        float64          `json:"seed"`
		CameraFixed bool             `json:"camera_fixed"`
	}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if body.Model != "doubao-seedance-1-0-pro-250528" {
		t.Fatalf("model = %q", body.Model)
	}
	// prompt → content[0] text 段；input_reference（data URL）→ content[1]
	// image_url 段，字符串直传不剥离 base64 载荷（官方双形态，契约 §9.1）。
	if len(body.Content) != 2 {
		t.Fatalf("content 段数 = %d, want 2（text + image_url）: %s", len(body.Content), output.Body)
	}
	if body.Content[0]["type"] != "text" || body.Content[0]["text"] != "一只猫在弹钢琴" {
		t.Fatalf("content[0] 文本段错误: %v", body.Content[0])
	}
	if body.Content[1]["type"] != "image_url" {
		t.Fatalf("content[1] 类型错误: %v", body.Content[1])
	}
	imageURL, _ := body.Content[1]["image_url"].(map[string]any)
	if imageURL == nil || imageURL["url"] != reference {
		t.Fatalf("content[1].image_url.url = %#v, want 原样直传 data URL", body.Content[1]["image_url"])
	}
	// size 1280x720 → resolution 720p + ratio 16:9（短边档 + 约分精确匹配）。
	if body.Resolution != "720p" || body.Ratio != "16:9" {
		t.Fatalf("size 换算 = %s/%s, want 720p/16:9", body.Resolution, body.Ratio)
	}
	// seconds → duration 数值直传；seed 整数直传。
	if body.Duration != 5 {
		t.Fatalf("duration = %v, want 5", body.Duration)
	}
	if body.Seed != 42 {
		t.Fatalf("seed = %v, want 42", body.Seed)
	}
	// provider_options 命中 volcengine 的子对象 deep-merge。
	if !body.CameraFixed {
		t.Fatalf("camera_fixed 未合并: %s", output.Body)
	}
}

func TestVolcengineVideoAdapterCreatePortraitSizeAndURLReference(t *testing.T) {
	adapter := volcengineVideoAdapter{}
	reference := "https://example.com/frame.png"
	output, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{
			Model:          "doubao-seedance-1-0-pro-250528",
			Prompt:         "竖屏海浪",
			Size:           "1080x1920",
			InputReference: &reference,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body struct {
		Content    []map[string]any `json:"content"`
		Resolution string           `json:"resolution"`
		Ratio      string           `json:"ratio"`
	}
	if err := json.Unmarshal(output.Body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	// 竖屏 1080x1920 → 1080p + 9:16；url 形态 input_reference 同样直传
	//（官方支持公网 URL，与 minimax 仅 base64 的裁决不同）。
	if body.Resolution != "1080p" || body.Ratio != "9:16" {
		t.Fatalf("竖屏 size 换算 = %s/%s, want 1080p/9:16", body.Resolution, body.Ratio)
	}
	if len(body.Content) != 2 {
		t.Fatalf("content 段数 = %d, want 2", len(body.Content))
	}
	imageURL, _ := body.Content[1]["image_url"].(map[string]any)
	if imageURL == nil || imageURL["url"] != reference {
		t.Fatalf("content[1].image_url.url = %#v, want 原样直传公网 URL", body.Content[1]["image_url"])
	}
}

func TestVolcengineVideoAdapterCreateSizeRatioOutOfVocabulary(t *testing.T) {
	adapter := volcengineVideoAdapter{}
	// 词表外宽高比（约分后不等于 16:9/9:16/1:1/4:3/3:4/21:9）→ 参数类 400
	//（契约 §2.4 规则 2：不近似贴合，不猜测）。
	for _, size := range []string{"1279x720", "1000x1000x1"} {
		if _, err := adapter.Create(context.Background(), VideoCreateInput{
			Params: NormalizedVideoParams{Model: "doubao-seedance-1-0-pro-250528", Prompt: "p", Size: size},
		}); err == nil {
			t.Fatalf("size %s 应拒绝", size)
		}
	}
	if _, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: NormalizedVideoParams{Model: "doubao-seedance-1-0-pro-250528", Prompt: "p", Size: "1279x720"},
	}); !errors.Is(err, ErrParamUnsupported) {
		t.Fatalf("词表外 ratio 应为 ErrParamUnsupported: %v", err)
	}
}

func TestVolcengineVideoAdapterPollRequest(t *testing.T) {
	method, path, body := VideoAdapterForProvider("volcengine").BuildPollRequest("cgt-20261004101122abcdef")
	if method != http.MethodGet || path != "/api/v3/contents/generations/tasks/cgt-20261004101122abcdef" {
		t.Fatalf("poll 出站形态 = %s %s", method, path)
	}
	if body != nil {
		t.Fatalf("poll 不得携带请求体: %s", body)
	}
}

func TestVolcengineVideoAdapterParsePollResponse(t *testing.T) {
	adapter := volcengineVideoAdapter{}

	// 创建响应：受理凭据确立，status=queued → queued。
	ir, err := adapter.ParsePollResponse(http.StatusOK, []byte(volcengineCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("create parse: %v", err)
	}
	if ir.UpstreamJobID != "cgt-20261004101122abcdef" || ir.Status != JobStatusQueued {
		t.Fatalf("create parse = %s/%s, want cgt-.../queued", ir.UpstreamJobID, ir.Status)
	}

	// running → in_progress。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(volcenginePollRunningFixture))
	if err != nil {
		t.Fatalf("running parse: %v", err)
	}
	if ir.Status != JobStatusInProgress {
		t.Fatalf("running parse status = %s, want in_progress", ir.Status)
	}

	// succeeded → completed，content.video_url 冻结进 Artifact.ContentURL。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(volcenginePollSucceededFixture))
	if err != nil {
		t.Fatalf("succeeded parse: %v", err)
	}
	if ir.Status != JobStatusCompleted ||
		ir.Artifact.ContentURL != "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks/cgt-20261004101122abcdef/content" {
		t.Fatalf("succeeded parse = %s/%s", ir.Status, ir.Artifact.ContentURL)
	}

	// failed → failed，错误摘要取 error{code,message}。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(volcenginePollFailedFixture))
	if err != nil {
		t.Fatalf("failed parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil ||
		ir.Error.Code != "InternalServiceError" || ir.Error.Message != "video generation failed" {
		t.Fatalf("failed parse = %s/%+v", ir.Status, ir.Error)
	}

	// failed 但 error 对象缺席：以 status 原值兜底，不丢弃终态。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"cgt-x","status":"failed"}`))
	if err != nil {
		t.Fatalf("failed-without-error parse: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Code != "failed" {
		t.Fatalf("failed 无 error 兜底错误: %s/%+v", ir.Status, ir.Error)
	}

	// succeeded 缺 video_url：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"cgt-x","status":"succeeded"}`)); err == nil {
		t.Fatal("succeeded 缺 video_url 应报错（不猜测产物可达性）")
	}

	// 缺受理凭据：协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"status":"running"}`)); err == nil {
		t.Fatal("缺 id 应报错")
	}

	// 未知状态 → in_progress + RawStatus（不猜测失败；官方回调面另有
	// expired 等词，§9.1 轮询面未回填，按契约 §2.6 归一规则处理）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"cgt-x","status":"expired"}`))
	if err != nil {
		t.Fatalf("unknown status parse: %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.RawStatus != "expired" {
		t.Fatalf("unknown status = %s/%s, want in_progress/expired", ir.Status, ir.RawStatus)
	}

	// 非 2xx → UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）。
	var statusErr *UpstreamStatusError
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{}`))
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应为 UpstreamStatusError: %v", err)
	}
	// 未知形态防御：succeeded 带 video_url 空白串同样拒绝。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"cgt-x","status":"succeeded","content":{"video_url":"  "}}`)); err == nil || !strings.Contains(err.Error(), "video_url") {
		t.Fatalf("空白 video_url 应报协议违约: %v", err)
	}
}
