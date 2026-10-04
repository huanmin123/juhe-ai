package gatewaymedia

// glm（CogVideoX）视频 adapter 测试（契约 §7.1 报文，fixture 形态与
// mockupstream glmAsyncResultBody 同源派生——Mock 上游按同一契约实现）：
// 创建报文构造（negative_prompt/image_url/size 透传、seconds→duration 档位
// 换算、audio→with_audio、provider_options 合并）、轮询请求构造与产物定位/
// 取消声明、轮询响应归一（task_status 三态、SUCCESS 缺 url 的协议违约、
// 未知状态 RawStatus、非 2xx）。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// glmCreateAcceptedFixture 对齐 mockupstream 创建响应形态
// （media_glm_video_create_ok，受理凭据 = id 字段）。
const glmCreateAcceptedFixture = `{"id":"0123456789ab","request_id":"req-0123456789ab","task_status":"PROCESSING"}`

// glmPollSuccessFixture 对齐 mockupstream SUCCESS 形态
// （media_glm_video_poll_success_url，video_result.url 为绝对下载 URL）。
const glmPollSuccessFixture = `{"id":"0123456789ab","request_id":"req-0123456789ab","task_status":"SUCCESS","video_result":{"url":"https://open.bigmodel.cn/mock/glm%2Fsample.mp4","cover_image_url":""}}`

// glmPollFailFixture 对齐 mockupstream FAIL 形态（media_glm_video_poll_fail）。
const glmPollFailFixture = `{"id":"0123456789ab","request_id":"req-0123456789ab","task_status":"FAIL"}`

func TestGlmVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("glm")
	if adapter == nil {
		t.Fatal("glm（cogvideo）视频 adapter 应已注册")
	}
	if adapter.Provider() != "glm" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" GLM ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestGlmVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("glm").Capabilities()
	// CogVideoX 请求面（契约 §7.1）：negative_prompt、image_url、duration
	//（档位换算）、with_audio 原生支持；n/seed 请求面无对应字段 → 忽略 + 回显。
	if !caps.SupportsNegativePrompt || !caps.SupportsInputReference || !caps.SupportsSeconds || !caps.SupportsAudio {
		t.Fatalf("glm 应支持 negative_prompt/input_reference/seconds/audio: %+v", caps)
	}
	if caps.SupportsN || caps.SupportsSeed {
		t.Fatalf("glm 请求面无 n/seed: %+v", caps)
	}
}

func TestGlmVideoAdapterArtifactAndCancelDeclarations(t *testing.T) {
	glm := VideoAdapterForProvider("glm")
	if !glm.ContentFromArtifact() {
		t.Fatal("glm 产物定位应为绝对 Artifact.ContentURL（video_result.url）")
	}
	if VideoAdapterForProvider("openai").ContentFromArtifact() {
		t.Fatal("openai 产物定位应由 job id 构造（对比项）")
	}
	// glm 无取消 API（契约 §7.1）→ 链上层本地收敛；openai/veo 有取消端点。
	if glm.SupportsCancel() {
		t.Fatal("glm 无上游取消端点，SupportsCancel 应为 false")
	}
	if !VideoAdapterForProvider("openai").SupportsCancel() || !VideoAdapterForProvider("gemini").SupportsCancel() {
		t.Fatal("openai/veo 的 SupportsCancel 应为 true（对比项）")
	}
}

func TestGlmDurationOf(t *testing.T) {
	cases := map[float64]string{
		0.1: "5s", 4.9: "5s", 5: "5s", 6: "5s",
		7.5: "5s", // 等距取小档
		7.6: "10s", 9: "10s", 10: "10s", 30: "10s",
	}
	for seconds, want := range cases {
		if got := glmDurationOf(seconds); got != want {
			t.Fatalf("glmDurationOf(%v) = %q, want %q", seconds, got, want)
		}
	}
}

func TestGlmVideoAdapterCreateRequest(t *testing.T) {
	adapter := VideoAdapterForProvider("glm")
	params, err := ParseVideoParams(map[string]any{
		"model":           "cogvideox-3",
		"prompt":          "一只猫在弹钢琴",
		"negative_prompt": "低清画质",
		"input_reference": "https://example.com/first-frame.png",
		"size":            "1280x720",
		"seconds":         "6",
		"audio":           true,
		// seed 属 ignored（Capabilities 裁决），进 params_ignored。
		"seed": float64(42),
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	if !containsString(params.ParamsIgnored, "seed") {
		t.Fatalf("seed 应进 params_ignored: %v", params.ParamsIgnored)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: params,
		ProviderOptions: map[string]any{
			"glm":    map[string]any{"fps": float64(30), "quality": "quality"},
			"openai": map[string]any{"must_not": float64(1)},
		},
	})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	if out.Method != http.MethodPost || out.Path != "/api/paas/v4/videos/generations" {
		t.Fatalf("method/path = %s %s", out.Method, out.Path)
	}
	var body struct {
		Model          string   `json:"model"`
		Prompt         string   `json:"prompt"`
		NegativePrompt string   `json:"negative_prompt"`
		ImageURL       string   `json:"image_url"`
		Size           string   `json:"size"`
		Duration       string   `json:"duration"`
		WithAudio      bool     `json:"with_audio"`
		FPS            *float64 `json:"fps"`
		Quality        string   `json:"quality"`
	}
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("decode body: %v: %s", err, out.Body)
	}
	if body.Model != "cogvideox-3" || body.Prompt != "一只猫在弹钢琴" {
		t.Fatalf("model/prompt 错误: %s", out.Body)
	}
	if body.NegativePrompt != "低清画质" {
		t.Fatalf("negative_prompt 缺失: %s", out.Body)
	}
	if body.ImageURL != "https://example.com/first-frame.png" {
		t.Fatalf("image_url 应字符串透传: %s", out.Body)
	}
	if body.Size != "1280x720" {
		t.Fatalf("size 应原样直传（glm 即 WxH 形态）: %s", out.Body)
	}
	if body.Duration != "5s" {
		t.Fatalf("seconds=6 应换算最近档 5s: %s", out.Body)
	}
	if !body.WithAudio {
		t.Fatalf("with_audio 缺失: %s", out.Body)
	}
	// L3：命中 glm 的子对象 deep-merge 生效，其余 provider 的子对象忽略。
	if body.FPS == nil || *body.FPS != 30 || body.Quality != "quality" {
		t.Fatalf("provider_options.glm 合并失败: %s", out.Body)
	}
	if strings.Contains(string(out.Body), "must_not") {
		t.Fatalf("未命中 provider 的子对象不得进报文: %s", out.Body)
	}
	if strings.Contains(string(out.Body), `"seed"`) {
		t.Fatalf("创建报文不得含 ignored 键 seed: %s", out.Body)
	}
}

// containsString 是 params 列表包含判定的本地助手（测试内使用）。
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestGlmVideoAdapterPollRequest(t *testing.T) {
	adapter := VideoAdapterForProvider("glm")
	method, path, body := adapter.BuildPollRequest("0123456789ab")
	if method != http.MethodGet || path != "/api/paas/v4/async-result/0123456789ab" || body != nil {
		t.Fatalf("poll request = %s %s %v", method, path, body)
	}
}

func TestGlmVideoAdapterParsePollResponse(t *testing.T) {
	adapter := VideoAdapterForProvider("glm")

	// 创建响应（PROCESSING）→ in_progress + 受理凭据 id。
	ir, err := adapter.ParsePollResponse(http.StatusOK, []byte(glmCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("parse create fixture err = %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.UpstreamJobID != "0123456789ab" {
		t.Fatalf("create fixture 归一 = %s/%s, want in_progress/0123456789ab", ir.Status, ir.UpstreamJobID)
	}
	if ir.RawStatus != "" {
		t.Fatalf("已知状态不得记 RawStatus: %q", ir.RawStatus)
	}

	// SUCCESS → completed + url 冻结。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(glmPollSuccessFixture))
	if err != nil {
		t.Fatalf("parse success fixture err = %v", err)
	}
	if ir.Status != JobStatusCompleted {
		t.Fatalf("SUCCESS 应归一 completed: %s", ir.Status)
	}
	if ir.Artifact.ContentURL != "https://open.bigmodel.cn/mock/glm%2Fsample.mp4" {
		t.Fatalf("video_result.url 应冻结进 Artifact.ContentURL: %q", ir.Artifact.ContentURL)
	}
	// glm 轮询响应无时长回报 → 不填秒计量（契约 §2.8 兜底由链上处理）。
	if ir.Usage.OutputVideoSeconds != nil {
		t.Fatalf("glm 无秒计量基源，不得填 OutputVideoSeconds: %v", *ir.Usage.OutputVideoSeconds)
	}

	// FAIL → failed + task_status 摘要 code。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(glmPollFailFixture))
	if err != nil {
		t.Fatalf("parse fail fixture err = %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Code != "FAIL" {
		t.Fatalf("FAIL 归一 = %s/%+v, want failed/code=FAIL", ir.Status, ir.Error)
	}

	// 未知状态 → in_progress + RawStatus（不猜测失败）。
	ir, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"x1","task_status":"WEIRD_STATE"}`))
	if err != nil {
		t.Fatalf("parse unknown status err = %v", err)
	}
	if ir.Status != JobStatusInProgress || ir.RawStatus != "WEIRD_STATE" {
		t.Fatalf("未知状态归一 = %s/%q, want in_progress/WEIRD_STATE", ir.Status, ir.RawStatus)
	}

	// SUCCESS 缺 url → 协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"id":"x2","task_status":"SUCCESS"}`)); err == nil {
		t.Fatal("SUCCESS 缺 video_result.url 应报协议违约")
	}

	// 缺 id → 协议违约报错。
	if _, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"task_status":"PROCESSING"}`)); err == nil {
		t.Fatal("缺 id（受理凭据）应报协议违约")
	}

	// 非 2xx → *UpstreamStatusError。
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{"error":{"code":"500","message":"boom"}}`))
	var statusErr *UpstreamStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应返回 *UpstreamStatusError: %v", err)
	}
}

// TestGlmProviderOptionKeyIdentity 锁定 provider_options 的 glm 命中语义：
// glm 无子供应商代码，provider_code 与注册键恒等（含大小写归一）；任务书
// 的「alias 补 glm」在恒等映射下无需扩展 providerFamilyAlias——本测试钉住
// 该事实，防止后续误加别名或误破坏。
func TestGlmProviderOptionKeyIdentity(t *testing.T) {
	if !providerOptionKeyMatches("glm", "glm") || !providerOptionKeyMatches("glm", " GLM ") {
		t.Fatal("glm 注册键/账户 provider_code 恒等匹配失败（含归一）")
	}
	if providerOptionKeyMatches("glm", "openai") || providerOptionKeyMatches("glm", "gpt") {
		t.Fatal("glm 不得命中其他 provider 的子对象")
	}
	merged := MergeProviderOptions(map[string]any{"model": "cogvideox-3"}, "glm", map[string]any{
		"glm": map[string]any{"fps": float64(30)},
	})
	if merged["fps"] != float64(30) || merged["model"] != "cogvideox-3" {
		t.Fatalf("glm 子对象应 deep-merge: %#v", merged)
	}
	if keys := AppliedProviderOptionKeys("glm", map[string]any{"glm": map[string]any{"fps": float64(30)}}); len(keys) != 1 || keys[0] != "fps" {
		t.Fatalf("AppliedProviderOptionKeys = %v, want [fps]", keys)
	}
}
