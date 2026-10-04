package gatewaymedia

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// M3f qwen paraformer 长转写 adapter 单测（契约 §10.2）：创建报文映射
//（input_url → input.file_urls 单元素数组、language → parameters.
// language_hints 数组、provider_options deep-merge、无 X-DashScope-Async
// 异步头）、轮询响应归一（PENDING/RUNNING/SUCCEEDED/FAILED 同万相词表、
// transcription_url 冻结、usage JSON 字符串/对象双形态时长解析、output.
// message 错误摘要、受理凭据缺失与产物缺失协议违约、非 2xx
// UpstreamStatusError）、取消面声明（SupportsCancel=false 本地收敛）。

func TestQwenASRAdapterCreateBodyMapping(t *testing.T) {
	adapter := qwenASRAdapter{}
	url := "https://oss.example.com/audio/meeting.mp3"
	out, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{
			Model:    "paraformer-v2",
			InputURL: url,
			Language: "zh",
		},
		ProviderOptions: map[string]any{
			"qwen": map[string]any{"parameters": map[string]any{"diarization_enabled": true}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Method != http.MethodPost || out.Path != "/api/v1/services/audio/asr/transcription" {
		t.Fatalf("出站请求形态错误: %s %s", out.Method, out.Path)
	}
	if out.ExtraHeaders != nil {
		t.Fatalf("paraformer 创建不得携带厂商私有头（天然异步，无 X-DashScope-Async）: %v", out.ExtraHeaders)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("创建报文不是 JSON: %v", err)
	}
	if body["model"] != "paraformer-v2" {
		t.Fatalf("model = %v, want paraformer-v2", body["model"])
	}
	input, _ := body["input"].(map[string]any)
	fileURLs, _ := input["file_urls"].([]any)
	if len(fileURLs) != 1 || fileURLs[0] != url {
		t.Fatalf("input.file_urls = %v, want [%s]（公网 URL 单元素数组）", input["file_urls"], url)
	}
	parameters, _ := body["parameters"].(map[string]any)
	hints, _ := parameters["language_hints"].([]any)
	if len(hints) != 1 || hints[0] != "zh" {
		t.Fatalf("parameters.language_hints = %v, want [zh]（language 单字符串映射单元素数组）", parameters["language_hints"])
	}
	if parameters["diarization_enabled"] != true {
		t.Fatalf("provider_options.qwen.parameters.diarization_enabled 未 deep-merge: %v", body)
	}
}

func TestQwenASRAdapterCreateWithoutLanguageOmitsParameters(t *testing.T) {
	out, err := (qwenASRAdapter{}).Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{Model: "paraformer-v2", InputURL: "https://a.example.com/x.wav"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("创建报文不是 JSON: %v", err)
	}
	if _, present := body["parameters"]; present {
		t.Fatalf("未传 language 不得携带 parameters 键: %s", out.Body)
	}
}

func TestQwenASRAdapterCreateRequiredFields(t *testing.T) {
	if _, err := (qwenASRAdapter{}).Create(context.Background(), AudioJobCreateInput{Params: NormalizedAudioJobParams{InputURL: "https://a/b.mp3"}}); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model 应报错, got %v", err)
	}
	if _, err := (qwenASRAdapter{}).Create(context.Background(), AudioJobCreateInput{Params: NormalizedAudioJobParams{Model: "paraformer-v2"}}); err == nil || !strings.Contains(err.Error(), "input_url") {
		t.Fatalf("缺 input_url 应报错, got %v", err)
	}
}

func TestQwenASRAdapterParsePollResponseLifecycle(t *testing.T) {
	adapter := qwenASRAdapter{}
	// 创建/首轮 PENDING → queued。
	ir, err := adapter.ParsePollResponse(200, []byte(`{"output":{"task_id":"asr-1","task_status":"PENDING"},"request_id":"r"}`))
	if err != nil || ir.Status != JobStatusQueued || ir.UpstreamJobID != "asr-1" || ir.Kind != JobKindAudioTranscription {
		t.Fatalf("PENDING 归一错误: %+v err=%v", ir, err)
	}
	// RUNNING → in_progress。
	ir, err = adapter.ParsePollResponse(200, []byte(`{"output":{"task_id":"asr-1","task_status":"RUNNING"}}`))
	if err != nil || ir.Status != JobStatusInProgress {
		t.Fatalf("RUNNING 归一错误: %+v err=%v", ir, err)
	}
	// SUCCEEDED + transcription_url + usage JSON 字符串（duration=62.5）。
	body := `{"output":{"task_id":"asr-1","task_status":"SUCCEEDED","results":[{"file_url":"u","transcription_url":"https://oss/result.json"}]},"usage":"{\"duration\":62.5}"}`
	ir, err = adapter.ParsePollResponse(200, []byte(body))
	if err != nil {
		t.Fatalf("SUCCEEDED 解析失败: %v", err)
	}
	if ir.Status != JobStatusCompleted || ir.Artifact.ContentURL != "https://oss/result.json" {
		t.Fatalf("SUCCEEDED 归一错误: %+v", ir)
	}
	if ir.Usage.AudioInputSeconds == nil || *ir.Usage.AudioInputSeconds != 62.5 {
		t.Fatalf("usage duration 时长计量照抽失败: %+v", ir.Usage)
	}
	// FAILED + output.message（契约 §10.2 钉住）→ code 兜底 FAILED。
	ir, err = adapter.ParsePollResponse(200, []byte(`{"output":{"task_id":"asr-1","task_status":"FAILED","message":"audio file download failed"}}`))
	if err != nil {
		t.Fatalf("FAILED 解析失败: %v", err)
	}
	if ir.Status != JobStatusFailed || ir.Error == nil || ir.Error.Message != "audio file download failed" || ir.Error.Code != "FAILED" {
		t.Fatalf("FAILED 错误摘要错误: %+v", ir)
	}
}

func TestQwenASRAdapterUsageDualForm(t *testing.T) {
	// usage 对象形态兼容（官方文档示例亦展示对象形态，沿万相双形态先例）。
	ir, err := (qwenASRAdapter{}).ParsePollResponse(200, []byte(
		`{"output":{"task_id":"asr-2","task_status":"SUCCEEDED","results":[{"transcription_url":"https://x/1.json"}]},"usage":{"duration":12.5}}`))
	if err != nil {
		t.Fatalf("对象形态 usage 解析失败: %v", err)
	}
	if ir.Usage.AudioInputSeconds == nil || *ir.Usage.AudioInputSeconds != 12.5 {
		t.Fatalf("对象形态 duration = %+v, want 12.5", ir.Usage.AudioInputSeconds)
	}
	// usage 缺失 / 非正值 / 解析失败 → 无计量（nil，链上 usage_missing 兜底）。
	for name, usage := range map[string]string{
		"missing":   `{"output":{"task_id":"asr-3","task_status":"SUCCEEDED","results":[{"transcription_url":"u"}]}}`,
		"zero":      `{"output":{"task_id":"asr-3","task_status":"SUCCEEDED","results":[{"transcription_url":"u"}]},"usage":"{\"duration\":0}"}`,
		"unparsed":  `{"output":{"task_id":"asr-3","task_status":"SUCCEEDED","results":[{"transcription_url":"u"}]},"usage":"not-json"}`,
		"nokeyword": `{"output":{"task_id":"asr-3","task_status":"SUCCEEDED","results":[{"transcription_url":"u"}]},"usage":"{\"video_duration\":5}"}`,
	} {
		ir, err := (qwenASRAdapter{}).ParsePollResponse(200, []byte(usage))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ir.Usage.AudioInputSeconds != nil {
			t.Fatalf("%s: 不应抽到时长计量: %+v", name, ir.Usage)
		}
	}
}

func TestQwenASRAdapterParsePollResponseProtocolViolations(t *testing.T) {
	adapter := qwenASRAdapter{}
	// 受理凭据缺失。
	if _, err := adapter.ParsePollResponse(200, []byte(`{"output":{"task_status":"PENDING"}}`)); err == nil || !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("缺 task_id 应报协议违约, got %v", err)
	}
	// SUCCEEDED 但 results 全缺 transcription_url。
	body := `{"output":{"task_id":"asr-4","task_status":"SUCCEEDED","results":[{"file_url":"only"}]}}`
	if _, err := adapter.ParsePollResponse(200, []byte(body)); err == nil || !strings.Contains(err.Error(), "transcription_url") {
		t.Fatalf("缺产物定位应报协议违约, got %v", err)
	}
	// 非 2xx → *UpstreamStatusError（链上层裁决，不伪造终态）。
	var statusErr *UpstreamStatusError
	_, err := adapter.ParsePollResponse(500, []byte(`{"code":"InternalError"}`))
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 500 {
		t.Fatalf("非 2xx 应返回 UpstreamStatusError, got %v", err)
	}
	// 未知状态值 → in_progress + RawStatus（同万相 §2.6 归一规则）。
	ir, err := adapter.ParsePollResponse(200, []byte(`{"output":{"task_id":"asr-5","task_status":"SUSPENDED"}}`))
	if err != nil || ir.Status != JobStatusInProgress || ir.RawStatus != "SUSPENDED" {
		t.Fatalf("未知状态归一错误: %+v err=%v", ir, err)
	}
	// 非 JSON。
	if _, err := adapter.ParsePollResponse(200, []byte(`not-json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

func TestQwenASRAdapterTaskPlaneDeclarations(t *testing.T) {
	adapter := qwenASRAdapter{}
	if !adapter.ContentFromArtifact() {
		t.Fatal("paraformer 产物是 transcription_url 绝对 URL，ContentFromArtifact 应为 true")
	}
	if adapter.SupportsCancel() {
		t.Fatal("契约 §10.2 面无取消端点，SupportsCancel 应为 false（本地收敛 cancelled）")
	}
	if method, path, _ := adapter.BuildPollRequest("task-9"); method != http.MethodGet || path != "/api/v1/tasks/task-9" {
		t.Fatalf("轮询路径错误: %s %s（应与万相同一 DashScope 任务接口）", method, path)
	}
	if got := AudioJobAdapterForProvider("QWEN"); got == nil || got.Provider() != "qwen" {
		t.Fatalf("AudioJobAdapterForProvider 注册表解析错误: %+v", got)
	}
	if AudioJobAdapterForProvider("openai") != nil {
		t.Fatal("openai 无长转写 adapter，应返回 nil（能力缺失不静默回退）")
	}
}

func TestParseAudioJobParams(t *testing.T) {
	caps := AudioJobCapabilities{SupportsLanguage: true}
	params, err := ParseAudioJobParams(map[string]any{
		"model":     "paraformer-v2",
		"input_url": "https://oss.example.com/a/meeting.mp3",
		"language":  "zh",
	}, caps)
	if err != nil {
		t.Fatalf("ParseAudioJobParams: %v", err)
	}
	if params.Model != "paraformer-v2" || params.InputURL != "https://oss.example.com/a/meeting.mp3" || params.Language != "zh" {
		t.Fatalf("归一参数错误: %+v", params)
	}
	applied := strings.Join(params.ParamsApplied, ",")
	if applied != "model,input_url,language" {
		t.Fatalf("params_applied = %q", applied)
	}
	if len(params.ParamsIgnored) != 0 {
		t.Fatalf("params_ignored = %v, want 空", params.ParamsIgnored)
	}

	// 缺省/形态错误。
	if _, err := ParseAudioJobParams(map[string]any{"input_url": "https://a/b"}, caps); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model: %v", err)
	}
	if _, err := ParseAudioJobParams(map[string]any{"model": "paraformer-v2"}, caps); err == nil || !strings.Contains(err.Error(), "input_url") {
		t.Fatalf("缺 input_url: %v", err)
	}
	if _, err := ParseAudioJobParams(map[string]any{"model": "paraformer-v2", "input_url": "ftp://example.com/a.mp3"}, caps); err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("非 http(s) input_url: %v", err)
	}
	if _, err := ParseAudioJobParams(map[string]any{"model": "paraformer-v2", "input_url": "https://a/b.mp3", "language": "  "}, caps); err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("空 language: %v", err)
	}

	// 能力裁决：无 language 字段的厂商 → ignored 回显。
	noLangCaps := AudioJobCapabilities{SupportsLanguage: false}
	params, err = ParseAudioJobParams(map[string]any{
		"model": "x-asr", "input_url": "https://a/b.mp3", "language": "zh",
	}, noLangCaps)
	if err != nil {
		t.Fatalf("ParseAudioJobParams(no language caps): %v", err)
	}
	if len(params.ParamsIgnored) != 1 || params.ParamsIgnored[0] != "language" {
		t.Fatalf("language 应进 params_ignored: %v", params.ParamsIgnored)
	}
	if strings.Contains(strings.Join(params.ParamsApplied, ","), "language") {
		t.Fatalf("language 未生效不得进 applied: %v", params.ParamsApplied)
	}
}
