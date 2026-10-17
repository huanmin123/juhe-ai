// volcengine_asr_test.go 钉住火山长转写 adapter 的头驱动协议转换（契约
// §9.2 长转写臂）：submit 报文映射（input_url→audio.url、format 后缀推断
// 与 provider_options 覆盖、请求 ID 即任务 ID）、ParsePollResponse 头驱动
// 状态归一（受理空 body / completed 毫秒计量 / failed / 未知码）、query 面
// 专用头三件套。
package gatewaymedia

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestVolcengineASRAdapterCreateMapsSubmitBody(t *testing.T) {
	adapter := volcengineASRAdapter{}
	output, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{
			Model:    "volc.seedasr.auc",
			InputURL: "https://oss.example.com/audio/meeting.mp3?sig=abc",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if output.Method != http.MethodPost || output.Path != VolcASRSubmitPath {
		t.Fatalf("method/path = %s/%s, want POST %s", output.Method, output.Path, VolcASRSubmitPath)
	}
	body := string(output.Body)
	for _, want := range []string{
		`"url":"https://oss.example.com/audio/meeting.mp3?sig=abc"`,
		`"format":"mp3"`, // URL 后缀推断剥 query
		`"model_name":"bigmodel"`,
		`"uid":"juhe-ai-gateway"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("submit body 缺少 %s: %s", want, body)
		}
	}
	if got := output.ExtraHeaders["X-Api-Resource-Id"]; got != VolcASRResourceID {
		t.Fatalf("X-Api-Resource-Id = %q, want %q", got, VolcASRResourceID)
	}
	if got := output.ExtraHeaders["X-Api-Sequence"]; got != "-1" {
		t.Fatalf("X-Api-Sequence = %q, want -1", got)
	}
	requestID := output.ExtraHeaders["X-Api-Request-Id"]
	if len(requestID) != 32 {
		t.Fatalf("X-Api-Request-Id = %q, want 32 字符 hex 任务 ID 载体", requestID)
	}
	// 请求 ID 唯一性：两次 Create 不得复用同一任务 ID。
	again, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{Model: "volc.seedasr.auc", InputURL: "https://oss.example.com/a.wav"},
	})
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if again.ExtraHeaders["X-Api-Request-Id"] == requestID {
		t.Fatal("两次 Create 生成了相同任务 ID")
	}
	if got := string(again.Body); !strings.Contains(got, `"format":"wav"`) {
		t.Fatalf("无后缀 URL 应落缺省 wav: %s", got)
	}
}

func TestVolcengineASRAdapterCreateValidationAndFormatOverride(t *testing.T) {
	adapter := volcengineASRAdapter{}
	if _, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{InputURL: "https://oss.example.com/a.wav"},
	}); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model 应 400 语义报错, got %v", err)
	}
	if _, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params: NormalizedAudioJobParams{Model: "volc.seedasr.auc"},
	}); err == nil || !strings.Contains(err.Error(), "input_url") {
		t.Fatalf("缺 input_url 应 400 语义报错, got %v", err)
	}
	// provider_options.volcengine.audio_format 覆盖后缀推断。
	output, err := adapter.Create(context.Background(), AudioJobCreateInput{
		Params:          NormalizedAudioJobParams{Model: "volc.seedasr.auc", InputURL: "https://oss.example.com/a.wav"},
		ProviderOptions: map[string]any{"volcengine": map[string]any{"audio_format": "ogg"}},
	})
	if err != nil {
		t.Fatalf("Create override: %v", err)
	}
	if !strings.Contains(string(output.Body), `"format":"ogg"`) {
		t.Fatalf("audio_format 覆盖未生效: %s", string(output.Body))
	}
}

func TestVolcengineASRAdapterParsePollResponseArms(t *testing.T) {
	adapter := volcengineASRAdapter{}
	header := func(statusCode, requestID string) http.Header {
		h := http.Header{}
		h.Set("X-Api-Status-Code", statusCode)
		if requestID != "" {
			h.Set("X-Api-Request-Id", requestID)
		}
		return h
	}

	// 受理形态：20000000 + 空 body {} + 回显请求 ID → queued。
	ir, err := adapter.ParsePollResponse(200, header("20000000", "req-1"), []byte(`{}`))
	if err != nil {
		t.Fatalf("受理归一: %v", err)
	}
	if ir.Status != JobStatusQueued || ir.UpstreamJobID != "req-1" || ir.Kind != JobKindAudioTranscription {
		t.Fatalf("受理 ir = %+v", ir)
	}

	// completed：终态 payload（audio_info.duration 毫秒 → ÷1000 秒计量）。
	ir, err = adapter.ParsePollResponse(200, header("20000000", "req-1"), []byte(volcASRCompletedFixtureBody))
	if err != nil {
		t.Fatalf("completed 归一: %v", err)
	}
	if ir.Status != JobStatusCompleted {
		t.Fatalf("completed status = %v", ir.Status)
	}
	if ir.Usage.AudioInputSeconds == nil || *ir.Usage.AudioInputSeconds != 1.118 {
		t.Fatalf("毫秒计量 = %v, want 1.118", ir.Usage.AudioInputSeconds)
	}

	// completed 但无时长计量（duration 缺失）→ 不填计量（链上 usage_missing 兜底）。
	ir, err = adapter.ParsePollResponse(200, header("20000000", "req-1"), []byte(`{"result":{"text":"x"}}`))
	if err != nil || ir.Status != JobStatusCompleted || ir.Usage.AudioInputSeconds != nil {
		t.Fatalf("无计量 completed 归一错误: %+v err=%v", ir, err)
	}

	// 处理中/队列中 → in_progress。
	for _, code := range []string{"20000001", "20000002"} {
		ir, err = adapter.ParsePollResponse(200, header(code, "req-1"), []byte(`{}`))
		if err != nil || ir.Status != JobStatusInProgress || ir.UpstreamJobID != "req-1" {
			t.Fatalf("code %s 归一错误: %+v err=%v", code, ir, err)
		}
	}

	// 450000xx → failed + X-Api-Message 错误信封。
	h := header("45000006", "req-1")
	h.Set("X-Api-Message", "download audio failed")
	ir, err = adapter.ParsePollResponse(200, h, []byte(`{}`))
	if err != nil || ir.Status != JobStatusFailed || ir.Error == nil {
		t.Fatalf("failed 归一错误: %+v err=%v", ir, err)
	}
	if ir.Error.Code != "45000006" || ir.Error.Message != "download audio failed" {
		t.Fatalf("failed error = %+v", ir.Error)
	}

	// 未知码 → in_progress + RawStatus（不猜测失败）。
	ir, err = adapter.ParsePollResponse(200, header("99999999", "req-1"), []byte(`{}`))
	if err != nil || ir.Status != JobStatusInProgress || ir.RawStatus != "99999999" {
		t.Fatalf("未知码归一错误: %+v err=%v", ir, err)
	}

	// 头缺失 → 协议违约报错。
	if _, err = adapter.ParsePollResponse(200, http.Header{}, []byte(`{}`)); err == nil {
		t.Fatal("缺 X-Api-Status-Code 头应报协议违约")
	}

	// HTTP 非 2xx → UpstreamStatusError。
	if _, err = adapter.ParsePollResponse(502, header("20000000", "req-1"), []byte(`{}`)); err == nil {
		t.Fatal("非 2xx 应返回 UpstreamStatusError")
	}
	var statusErr *UpstreamStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 502 {
		t.Fatalf("非 2xx 应为 UpstreamStatusError, got %v", err)
	}
}

func TestVolcengineASRAdapterQueryFaceShape(t *testing.T) {
	adapter := volcengineASRAdapter{}
	method, path, header, body := adapter.BuildPollRequest("req-1")
	if method != http.MethodPost || path != VolcASRQueryPath || string(body) != "{}" {
		t.Fatalf("query 形态 = %s %s %s", method, path, string(body))
	}
	if got := header.Get("X-Api-Request-Id"); got != "req-1" {
		t.Fatalf("query X-Api-Request-Id = %q, want 任务 ID", got)
	}
	if got := header.Get("X-Api-Resource-Id"); got != VolcASRResourceID {
		t.Fatalf("query X-Api-Resource-Id = %q", got)
	}
	if got := header.Get("X-Api-Sequence"); got != "-1" {
		t.Fatalf("query X-Api-Sequence = %q", got)
	}
	// content = 同构 query（火山无产物下载 URL）。
	if adapter.ContentFromArtifact() {
		t.Fatal("ContentFromArtifact 应为 false（content 走同构 query）")
	}
	cMethod, cPath, cHeader, cBody := adapter.BuildContentRequest("req-1")
	if cMethod != method || cPath != path || cHeader.Get("X-Api-Request-Id") != "req-1" || string(cBody) != "{}" {
		t.Fatalf("content 请求应与轮询同构（含空对象 body）: %s %s %v %s", cMethod, cPath, cHeader, string(cBody))
	}
	if adapter.SupportsCancel() {
		t.Fatal("火山长转写面无取消端点，SupportsCancel 应为 false")
	}
}

// volcASRCompletedFixtureBody 与 mock 引擎 volcengineASRSucceededPayload 同
// 源的终态最小形态（真实上游 2026-10-17 实测钉值：duration 毫秒 1118）。
const volcASRCompletedFixtureBody = `{"audio_info":{"duration":1118},"result":{"text":"Carry an oily rag."}}`
