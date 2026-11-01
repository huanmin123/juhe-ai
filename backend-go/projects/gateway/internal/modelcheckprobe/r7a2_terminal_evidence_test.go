package modelcheckprobe

// r7a2_terminal_evidence_test.go 钉死 R7-A 落点复核发现的六项探针层缺陷：
// 1 Responses SSE 干净 EOF 但缺 response.completed → Incomplete；
// 2 Responses SSE 带终态事件 → 正常成功；
// 3 chat 以非空 finish_reason 收尾（无 [DONE]）也是完整终态；
// 4 零事件流（空 body / 仅注释 / 仅空 data: 行）不得豁免终态校验；
// 5 chat / anthropic / gemini 终态后的尾部读错误按完整响应判定；
// 6 Responses 非流式 JSON 顶层 status 非 completed → 协议失败；
// 7 Responses reasoning 增量不并入判分 Output。
//
// 上游统一用 httptest 模拟；尾部读错误用"声明的 Content-Length 大于实际
// 写入字节"制造真实连接截断，驱动 read-error 臂。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const r7a2TerminalMissingError = "J3b probe upstream stream ended without terminal event"

func r7a2StreamRequest(t *testing.T, protocol modelcheckprofile.Protocol) Request {
	t.Helper()
	request, err := BuildBasic(protocol, "r7a2-model", "Reply with exactly: OK-MODEL-CHECK", true)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func r7a2SSEBody(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// r7a2Execute 用一次性 httptest 上游执行探针。
func r7a2Execute(t *testing.T, protocol modelcheckprofile.Protocol, handler http.HandlerFunc) (Result, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return Execute(context.Background(), r7a2StreamRequest(t, protocol), Options{Endpoint: server.URL, Timeout: 2 * time.Second})
}

// r7a2SSEHandler 干净 EOF 收尾的 SSE 上游。
func r7a2SSEHandler(body string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, body)
	}
}

// r7a2TailErrorHandler 声明比实际写入更长的 Content-Length 后直接返回：服务
// 端关闭连接，客户端 Body.Read 得到真实的 unexpected EOF，而不是干净 EOF。
func r7a2TailErrorHandler(body string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)+64))
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, body)
	}
}

// 缺陷 1：Responses 协议此前走 default 直接豁免终态校验，无 response.completed
// 的截断流被判成功。修复后与 chat/anthropic/gemini 同口径置 Incomplete。
func TestR7a2ResponsesSSEWithoutCompletedIsIncomplete(t *testing.T) {
	body := r7a2SSEBody(
		`event: response.created`,
		`data: {"type":"response.created","response":{"status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"r7a2 partial answer"}`,
		``,
	)
	result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIResponses, r7a2SSEHandler(body))
	if err != nil {
		t.Fatal(err)
	}
	if result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete {
		t.Fatalf("Responses 无 completed 必须置 Incomplete: %+v", result)
	}
	if result.ErrorMessage != r7a2TerminalMissingError {
		t.Fatalf("errorMessage=%q", result.ErrorMessage)
	}
	if !strings.Contains(result.Output, "r7a2 partial answer") {
		t.Fatalf("部分输出必须保留为排障证据: %q", result.Output)
	}
}

// 缺陷 1 反向卫兵：带 response.completed 的 Responses 流照常成功。
func TestR7a2ResponsesSSEWithCompletedSucceeds(t *testing.T) {
	body := r7a2SSEBody(
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"OK-MODEL-CHECK"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","model":"r7a2-model","output_text":"OK-MODEL-CHECK"}}`,
		``,
	)
	result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIResponses, r7a2SSEHandler(body))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Incomplete || result.ErrorMessage != "" {
		t.Fatalf("带 completed 必须正常成功: %+v", result)
	}
	if result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("output=%q", result.Output)
	}
}

// 缺陷 3：chat 只认 [DONE] 会误判 finish_reason 收尾的完整流；设计 §6 把
// finish_reason 列为 completed 证据。
func TestR7a2ChatFinishReasonWithoutDoneSucceeds(t *testing.T) {
	body := r7a2SSEBody(
		`data: {"model":"r7a2-model","choices":[{"delta":{"content":"OK-MODEL-CHECK"}}]}`,
		``,
		`data: {"model":"r7a2-model","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`,
		``,
	)
	result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIChat, r7a2SSEHandler(body))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Incomplete || result.ErrorMessage != "" {
		t.Fatalf("finish_reason 收尾必须正常成功: %+v", result)
	}
	if result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("output=%q", result.Output)
	}
}

// 缺陷 2：零事件流此前直接 return true，空 body/仅注释/仅空 data: 行冒充
// 完整响应。只有完整可解析 JSON 对象才可豁免。
func TestR7a2ZeroEventStreamIsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "空 body", body: ""},
		{name: "仅注释", body: ": keepalive\n\n"},
		{name: "仅空 data 行", body: "data:\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIChat, r7a2SSEHandler(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !result.Incomplete || result.ErrorMessage != r7a2TerminalMissingError {
				t.Fatalf("零事件流必须置 Incomplete: %+v", result)
			}
		})
	}
}

// 缺陷 4：read-error 臂此前只认 Responses 终态事件，[DONE]/message_stop/
// finishReason 后的尾部读错误被误判 Incomplete；修复后按协议统一终态证据。
func TestR7a2TerminalEvidenceSurvivesTailReadError(t *testing.T) {
	tests := []struct {
		name     string
		protocol modelcheckprofile.Protocol
		body     string
		output   string
	}{
		{
			name:     "chat [DONE]",
			protocol: modelcheckprofile.ProtocolOpenAIChat,
			body: r7a2SSEBody(
				`data: {"model":"r7a2-model","choices":[{"delta":{"content":"OK-MODEL-CHECK"}}]}`,
				``,
				`data: [DONE]`,
				``,
			),
			output: "OK-MODEL-CHECK",
		},
		{
			name:     "anthropic message_stop",
			protocol: modelcheckprofile.ProtocolAnthropic,
			body: r7a2SSEBody(
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","delta":{"text":"OK-MODEL-CHECK"}}`,
				``,
				`event: message_stop`,
				``,
			),
			output: "OK-MODEL-CHECK",
		},
		{
			name:     "gemini finishReason",
			protocol: modelcheckprofile.ProtocolGeminiNative,
			body: r7a2SSEBody(
				`data: {"candidates":[{"content":{"parts":[{"text":"OK-MODEL-CHECK"}],"role":"model"}}]}`,
				``,
				`data: {"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":2}}`,
				``,
			),
			output: "OK-MODEL-CHECK",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := r7a2Execute(t, test.protocol, r7a2TailErrorHandler(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success || result.Incomplete || result.ErrorMessage != "" {
				t.Fatalf("终态已到，尾部读错误不得覆盖语义成功: %+v", result)
			}
			if result.Output != test.output {
				t.Fatalf("output=%q", result.Output)
			}
		})
	}
}

// 缺陷 5：Responses 非流式 JSON 的顶层 status=incomplete 是失败定论，不能
// 因 HTTP 200 与文本非空冒充成功。
func TestR7a2ResponsesDirectJSONIncompleteStatusFails(t *testing.T) {
	handler := func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"r7a2-model","status":"incomplete","output_text":"partial answer","usage":{"total_tokens":2}}`)
	}
	result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIResponses, handler)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Incomplete {
		t.Fatalf("status=incomplete 必须判失败且非传输截断: %+v", result)
	}
	if result.ErrorMessage != "response.status incomplete" {
		t.Fatalf("errorMessage=%q", result.ErrorMessage)
	}
}

// 缺陷 6：Responses reasoning 增量是推理痕迹，不得并入判分 Output；终态事件
// 自身不带 output_text 时 Output 只能来自 response.output_text.* 增量。
func TestR7a2ResponsesReasoningDeltaNotMergedIntoOutput(t *testing.T) {
	body := r7a2SSEBody(
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"R7A2-REASONING-SENTINEL"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"OK-MODEL-CHECK"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","model":"r7a2-model"}}`,
		``,
	)
	result, err := r7a2Execute(t, modelcheckprofile.ProtocolOpenAIResponses, r7a2SSEHandler(body))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Incomplete {
		t.Fatalf("带 completed 的流必须成功: %+v", result)
	}
	if strings.Contains(result.Output, "R7A2-REASONING-SENTINEL") {
		t.Fatalf("reasoning 增量不得并入判分 Output: %q", result.Output)
	}
	if result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("output=%q", result.Output)
	}
	if eventOutput := parseSSEEventOutput(modelcheckprofile.ProtocolOpenAIResponses, map[string]any{"type": "response.reasoning_text.delta", "delta": "R7A2-REASONING-SENTINEL"}); eventOutput != "" {
		t.Fatalf("reasoning 事件输出必须为空: %q", eventOutput)
	}
	if eventOutput := parseSSEEventOutput(modelcheckprofile.ProtocolOpenAIResponses, map[string]any{"type": "response.output_text.delta", "delta": "OK-MODEL-CHECK"}); eventOutput != "OK-MODEL-CHECK" {
		t.Fatalf("output_text 事件输出必须保留: %q", eventOutput)
	}
}
