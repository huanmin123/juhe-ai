// w12b_runner_suite_test.go 覆盖编排层：RunBasicProbe/RunBasicProbeWithRetry/
// RunStructuredProbe/RunToolProbe 的协议分支与 transport 错误臂、
// ExecuteWithRetry 的重试状态序列与注入错误、RunTargetEvidenceExtensions
// 全臂、evaluateSuiteBasic 非 Responses 协议分支、emitSuiteItem panic 防护。
// 不可达清单（覆盖率登记）：
//   - retry.go/transport.go/identity.go/tokenprobe.go 的 rand.Read 失败回退
//     分支：crypto/rand 无测试注入点（newTraceID 回退 "model-check"、
//     identityNonce/tokenNonce 回退 "jobs-token"）。
//   - request.go/suite.go 各 json.Marshal 错误臂：载荷均为可序列化固定结构。
//   - transport.go NewRequestWithContext 错误臂：URL 已由 buildProbeURL 校验。
//   - transport.go 读取错误消息臂（readErr 且无解析错误）：需要服务端截断
//     连接且协议解析为零值，httptest 无稳定注入点。
//   - suite.go RunSuite 各构建错误臂经 RunBasic/BuildStructured/BuildTool
//     直测等价覆盖；runBatch/behavior 的 RunProbe 错误臂由探针单测直测。
package modelcheckprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// w12b_runner_suite_test.go 覆盖编排层：RunBasicProbe/RunBasicProbeWithRetry/
// RunStructuredProbe/RunToolProbe 的协议分支与 transport 错误臂、
// ExecuteWithRetry 的重试状态序列与注入错误、RunTargetEvidenceExtensions
// 全臂、evaluateSuiteBasic 非 Responses 协议分支、emitSuiteItem panic 防护。
// 不可达清单：retry.go/transport.go/identity.go/tokenprobe.go 中 rand.Read
// 失败回退分支（crypto/rand 无测试注入点）。

func w12bOKBody(protocol modelcheckprofile.Protocol) string {
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIChat:
		return `{"model":"m1","choices":[{"message":{"role":"assistant","content":"OK-MODEL-CHECK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	case modelcheckprofile.ProtocolAnthropic:
		return `{"model":"m1","content":[{"type":"text","text":"OK-MODEL-CHECK"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	case modelcheckprofile.ProtocolGeminiNative:
		return `{"modelVersion":"m1","candidates":[{"content":{"parts":[{"text":"OK-MODEL-CHECK"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`
	default:
		return `{"model":"m1","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1,"output_tokens":1}}`
	}
}

func w12bEchoModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		model, _ := payload["model"].(string)
		_, _ = w.Write([]byte(`{"model":"` + model + `","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
}

func TestW12bRunBasicProbeProtocolVariants(t *testing.T) {
	protocols := []modelcheckprofile.Protocol{
		modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat,
		modelcheckprofile.ProtocolAnthropic, modelcheckprofile.ProtocolGeminiNative,
	}
	for _, protocol := range protocols {
		for _, stream := range []bool{false, true} {
			server := w12bEchoModelServer(t)
			input := BasicProbeInput{
				Endpoint: server.URL, Protocol: protocol, Model: "m1",
				Prompt: "Reply with exactly: OK-MODEL-CHECK", Stream: stream, MaxOutputTokens: 16,
			}
			item, err := RunBasicProbe(context.Background(), input)
			if err != nil {
				t.Fatalf("protocol=%s stream=%v err=%v", protocol, stream, err)
			}
			wantType := "responses_basic"
			switch protocol {
			case modelcheckprofile.ProtocolOpenAIChat:
				wantType = "chat_basic"
			case modelcheckprofile.ProtocolAnthropic:
				wantType = "anthropic_basic"
			case modelcheckprofile.ProtocolGeminiNative:
				wantType = "gemini_basic"
			}
			if stream {
				wantType += "_stream"
			}
			if item.ItemType != wantType {
				t.Fatalf("protocol=%s stream=%v itemType=%s want=%s", protocol, stream, item.ItemType, wantType)
			}
			server.Close()
		}
	}
}

func TestW12bRunBasicProbeErrors(t *testing.T) {
	// 构建错误：非法模型。
	if _, err := RunBasicProbe(context.Background(), BasicProbeInput{Protocol: modelcheckprofile.ProtocolOpenAIResponses}); err == nil {
		t.Fatal("非法模型应构建失败")
	}
	// transport 错误臂：endpoint 非法 → evaluateBasicResult 填充错误信息。
	item, err := RunBasicProbe(context.Background(), BasicProbeInput{
		Endpoint: "httx://w12b-invalid", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "m1", Prompt: "p", MaxOutputTokens: 16,
	})
	if err != nil {
		t.Fatalf("transport 失败应转证据: %v", err)
	}
	if !strings.Contains(item.ErrorMessage, "模型检测") {
		t.Fatalf("应保留 transport 失败语义: %q", item.ErrorMessage)
	}
}

func TestW12bRunBasicProbeWithRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(w12bOKBody(modelcheckprofile.ProtocolOpenAIResponses)))
	}))
	defer server.Close()
	item, err := RunBasicProbeWithRetry(context.Background(), BasicProbeInput{
		Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "m1", Prompt: "Reply with exactly: OK-MODEL-CHECK", MaxOutputTokens: 16,
	}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "passed" {
		t.Fatalf("应通过: %+v", item)
	}
	// 构建错误臂。
	if _, err := RunBasicProbeWithRetry(context.Background(), BasicProbeInput{Protocol: modelcheckprofile.ProtocolOpenAIResponses}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}}); err == nil {
		t.Fatal("非法模型应构建失败")
	}
}

func TestW12bRunStructuredAndToolProbes(t *testing.T) {
	server := w12bEchoModelServer(t)
	defer server.Close()
	base := BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m1"}
	retry := RetryOptions{AttemptTimeouts: []time.Duration{time.Second}}

	structured, err := RunStructuredProbe(context.Background(), base, retry)
	if err != nil {
		t.Fatal(err)
	}
	if structured.ItemType != "structured_output" {
		t.Fatalf("structured itemType 不符: %s", structured.ItemType)
	}
	tool, err := RunToolProbe(context.Background(), base, retry)
	if err != nil {
		t.Fatal(err)
	}
	if tool.ItemType != "tool_calling" {
		t.Fatalf("tool itemType 不符: %s", tool.ItemType)
	}

	// transport 错误臂：endpoint 非法 → 错误消息填充分支。
	bad := BasicProbeInput{Endpoint: "httx://w12b-invalid", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m1"}
	structured, err = RunStructuredProbe(context.Background(), bad, retry)
	if err != nil || !strings.Contains(structured.ErrorMessage, "模型检测") {
		t.Fatalf("structured transport 错误臂: %+v %v", structured, err)
	}
	tool, err = RunToolProbe(context.Background(), bad, retry)
	if err != nil || !strings.Contains(tool.ErrorMessage, "模型检测") {
		t.Fatalf("tool transport 错误臂: %+v %v", tool, err)
	}
	// 构建错误臂。
	if _, err := RunStructuredProbe(context.Background(), BasicProbeInput{Protocol: modelcheckprofile.ProtocolOpenAIResponses}, retry); err == nil {
		t.Fatal("structured 构建错误应返回")
	}
	if _, err := RunToolProbe(context.Background(), BasicProbeInput{Protocol: modelcheckprofile.ProtocolOpenAIResponses}, retry); err == nil {
		t.Fatal("tool 构建错误应返回")
	}
}

func TestW12bExecuteWithRetryArms(t *testing.T) {
	ctx := context.Background()

	// 非正 timeout。
	if _, err := ExecuteWithRetry(ctx, mustRequest(), TransportOptions{}, RetryOptions{AttemptTimeouts: []time.Duration{0}}); err == nil {
		t.Fatal("非正 timeout 应报错")
	}
	// 首次尝试即 ExecuteRequest 错误（endpoint 非法）+ 空 attempts。
	if _, err := ExecuteWithRetry(ctx, mustRequest(), TransportOptions{Endpoint: "httx://w12b-invalid"}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}}); err == nil {
		t.Fatal("endpoint 非法应报错")
	}
	// delay 注入失败：第一次重试前中止。
	delayErr := context.Canceled
	if _, err := ExecuteWithRetry(ctx, mustRequest(), TransportOptions{Endpoint: "httx://w12b-invalid"}, RetryOptions{
		AttemptTimeouts: []time.Duration{time.Second, time.Second},
		Delay:           func(context.Context) error { return delayErr },
	}); err == nil {
		t.Fatal("delay 失败应报错")
	}
	// 首次 500 → 重试 → 200：多 attempt 状态序列。
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(w12bOKBody(modelcheckprofile.ProtocolOpenAIResponses)))
	}))
	defer server.Close()
	result, err := ExecuteWithRetry(ctx, mustRequest(), TransportOptions{Endpoint: server.URL}, RetryOptions{
		AttemptTimeouts: []time.Duration{time.Second, time.Second},
		Delay:           func(context.Context) error { return nil },
	})
	if err != nil || result.HTTPStatusCode != 200 || result.RetryAttemptCount != 1 || result.RetryMaxAttempts != 2 {
		t.Fatalf("重试结果不符: %+v %v", result, err)
	}
	if evidence := RetryEvidence(result); evidence == nil || evidence["retryAttemptCount"] != 1 {
		t.Fatalf("RetryEvidence 不符: %#v", evidence)
	}
	// 单次 200：无重试证据。
	result, err = ExecuteWithRetry(ctx, mustRequest(), TransportOptions{Endpoint: server.URL}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || result.RetryAttemptCount != 0 || RetryEvidence(result) != nil {
		t.Fatalf("单次成功不应有重试证据: %+v %v", result, err)
	}
	// 单 timeout 内非 200：最后一次尝试即返回（语义失败不作网络重试）。
	only500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer only500.Close()
	result, err = ExecuteWithRetry(ctx, mustRequest(), TransportOptions{Endpoint: only500.URL}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || result.HTTPStatusCode != 502 {
		t.Fatalf("末次尝试应返回证据: %+v %v", result, err)
	}
}

// TestW12bDefaultRetryDelay 覆盖缺省退避：取消立即返回；真实计时分支单次执行。
func TestW12bDefaultRetryDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := defaultRetryDelay(ctx); err == nil {
		t.Fatal("取消后应返回 ctx 错误")
	}
	if err := defaultRetryDelay(context.Background()); err != nil {
		t.Fatalf("真实退避应正常返回: %v", err)
	}
}

// TestW12bRunTargetEvidenceExtensions 覆盖证据扩展：非 Responses 协议短路、
// tokenizer 缺失跳过、完整 token 探针、身份观察终态判定。
// w12bTokenServer 返回 usage.input_tokens = 提示词空格数 + 1：
// 配合空格计数 tokenizer（" x" 恰为 1 token）使差分斜率≈1；
// +1 偏移让非零分桶值不落在 64 倍数上，避免 bucket_rounding 误报。
func w12bTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		prompt := w12bResponsesPrompt(payload)
		_, _ = w.Write([]byte(`{"model":"m1","output_text":"OK","usage":{"input_tokens":` +
			strconv.Itoa(strings.Count(prompt, " ")+1) + `}}`))
	}))
}

func w12bResponsesPrompt(payload map[string]any) string {
	input, _ := payload["input"].([]any)
	if len(input) == 0 {
		return ""
	}
	entry, _ := input[0].(map[string]any)
	content, _ := entry["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

func TestW12bRunTargetEvidenceExtensions(t *testing.T) {
	ctx := context.Background()
	base := BasicProbeInput{Endpoint: "placeholder", Protocol: modelcheckprofile.ProtocolOpenAIChat, Model: "m1"}
	// 非 Responses 协议：短路。
	if items, terminal, err := RunTargetEvidenceExtensions(ctx, base, "", RetryOptions{}, nil); err != nil || items != nil || terminal {
		t.Fatalf("非 Responses 协议应短路: %#v %v %v", items, terminal, err)
	}

	// tokenizer 缺失：token 项 skipped，身份项照常执行（约束不符即 failed，非终态）。
	server := w12bTokenServer(t)
	defer server.Close()
	responses := BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m1"}
	items, terminal, err := RunTargetEvidenceExtensions(ctx, responses, "target", RetryOptions{AttemptTimeouts: []time.Duration{time.Second}}, nil)
	if err != nil || terminal || len(items) != 2 {
		t.Fatalf("tokenizer 缺失臂: %#v %v %v", items, terminal, err)
	}
	if items[0].ItemType != "token_integrity" || items[0].Status != "skipped" {
		t.Fatalf("token 项应 skipped: %+v", items[0])
	}
	if items[1].ItemType != "identity_observation" {
		t.Fatalf("身份项类型不符: %+v", items[1])
	}

	// 带 tokenizer（空格计数）：token 项差分一致 → passed。
	withTokens := responses
	withTokens.CountTokens = func(value string) int { return strings.Count(value, " ") }
	items, terminal, err = RunTargetEvidenceExtensions(ctx, withTokens, "", RetryOptions{AttemptTimeouts: []time.Duration{time.Second}}, nil)
	if err != nil || terminal || len(items) != 2 || items[0].Status != "passed" {
		t.Fatalf("token 完整臂: %#v %v %v", items, terminal, err)
	}

	// 身份终态：上游 502 直至重试耗尽 → requestFailure。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusBadGateway)
	}))
	defer dead.Close()
	failing := BasicProbeInput{Endpoint: dead.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m1"}
	items, terminal, err = RunTargetEvidenceExtensions(ctx, failing, "", RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }}, nil)
	if err != nil || !terminal || len(items) != 2 {
		t.Fatalf("终态臂: %#v %v %v", items, terminal, err)
	}
	if !isTerminalProbeItem(items[1]) || isTerminalProbeItem(EvaluationItem{}) {
		t.Fatal("isTerminalProbeItem 判定不符")
	}

	// token 探针错误臂：非法 endpoint + 有 tokenizer。
	broken := BasicProbeInput{Endpoint: "httx://w12b-invalid", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m1", CountTokens: withTokens.CountTokens}
	if _, _, err := RunTargetEvidenceExtensions(ctx, broken, "", RetryOptions{}, nil); err == nil {
		t.Fatal("token 探针错误应返回")
	}
}

func TestW12bEvaluateSuiteBasicProtocolArm(t *testing.T) {
	server := w12bEchoModelServer(t)
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{
		Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIChat, Model: "m1", MaxOutputTokens: 16,
	}, SuiteOptions{IncludeStream: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 2 {
		t.Fatalf("chat 套件: %#v %v", items, err)
	}
	if items[0].ItemKey != "target.protocol_basic" || items[1].ItemKey != "target.protocol_stream" {
		t.Fatalf("chat 套件 item key 不符: %#v", items)
	}
}

// TestW12bEmitSuiteItemRecovers panic 回调不应中断套件。
func TestW12bEmitSuiteItemRecovers(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("panic 应被吞掉: %v", recovered)
		}
	}()
	emitSuiteItem(func(EvaluationItem) { panic("w12b 观察者故障") }, EvaluationItem{})
	emitSuiteItem(nil, EvaluationItem{})
}

func mustRequest() Request {
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "m1", "Reply with exactly: OK-MODEL-CHECK", BasicOptions{MaxOutputTokens: 16})
	if err != nil {
		panic(err)
	}
	return request
}
