package modelcheckprobe

// r7a_probe_fixes_test.go 钉死 R7-A 深挖复审五条修复的回归契约：
// P-H1 三条 SSE 臂（chat_sse / messages_sse / generate_content_sse）干净 EOF
// 收尾的流必须出现协议终态事件才算完整（chat [DONE] / anthropic
// message_stop / gemini finishReason 终帧），未见终态按传输面请求失败口径置
// Incomplete；P-H2 流式 chat reasoning_content 不并入判分 Output；P-H3 通用
// responses 解析器对 response.completed 的非 completed status 保持失败口径；
// P-H5 quiz 作答跳非终态失败（200 错误信封/空 choices/空 content）不送裁判、
// 不扣分；W-H1 对比账户自身的 modelMismatch 不把目标 run 拉成 suspicious。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func r7aStreamRequest(t *testing.T, protocol modelcheckprofile.Protocol) Request {
	t.Helper()
	request, err := BuildBasic(protocol, "r7a-model", "Reply with exactly: OK-MODEL-CHECK", true)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func r7aSSEBody(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// P-H1：干净 EOF 的流缺终态事件 → Incomplete/Success=false（与传输错误臂同
// 口径），部分输出仅作证据保留；带终态事件 → 正常成功。
func TestR7aSSEStreamWithoutTerminalEventIsIncomplete(t *testing.T) {
	const r7aTerminalMissingError = "J3b probe upstream stream ended without terminal event"
	t.Run("chat_sse 无 [DONE] 干净 EOF", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"model":"r7a-model","choices":[{"delta":{"content":"partial ans"}}]}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete {
			t.Fatalf("无终态干净 EOF 必须置 Incomplete: result=%+v err=%v", result, err)
		}
		if result.ErrorMessage != r7aTerminalMissingError {
			t.Fatalf("errorMessage=%q", result.ErrorMessage)
		}
		if !strings.Contains(result.Output, "partial ans") {
			t.Fatalf("部分输出必须保留为排障证据: %q", result.Output)
		}
	})
	t.Run("chat_sse 带 [DONE] 正常成功", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"model":"r7a-model","choices":[{"delta":{"content":"OK-MODEL-CHECK"}}]}`,
			``,
			`data: [DONE]`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
			t.Fatalf("带 [DONE] 必须正常成功: result=%+v err=%v", result, err)
		}
	})
	t.Run("messages_sse 无 message_stop 干净 EOF", func(t *testing.T) {
		body := r7aSSEBody(
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"text":"partial ans"}}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolAnthropic), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete || result.ErrorMessage != r7aTerminalMissingError {
			t.Fatalf("无 message_stop 干净 EOF 必须置 Incomplete: result=%+v err=%v", result, err)
		}
	})
	t.Run("messages_sse 纯 event 行 message_stop 正常成功", func(t *testing.T) {
		body := r7aSSEBody(
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"text":"OK-MODEL-CHECK"}}`,
			``,
			`event: message_stop`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolAnthropic), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
			t.Fatalf("无 data 的 message_stop 必须识别为终态: result=%+v err=%v", result, err)
		}
	})
	t.Run("messages_sse data 载荷 message_stop 正常成功", func(t *testing.T) {
		body := r7aSSEBody(
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"text":"OK-MODEL-CHECK"}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolAnthropic), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
			t.Fatalf("载荷 message_stop 必须识别为终态: result=%+v err=%v", result, err)
		}
	})
	t.Run("generate_content_sse 无 finishReason 干净 EOF", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"candidates":[{"content":{"parts":[{"text":"partial ans"}],"role":"model"}}]}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolGeminiNative), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete || result.ErrorMessage != r7aTerminalMissingError {
			t.Fatalf("无 finishReason 干净 EOF 必须置 Incomplete: result=%+v err=%v", result, err)
		}
	})
	t.Run("generate_content_sse finishReason 终帧正常成功", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"candidates":[{"content":{"parts":[{"text":"OK-MODEL-CHECK"}],"role":"model"}}]}`,
			``,
			`data: {"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":2}}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolGeminiNative), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
			t.Fatalf("finishReason 终帧必须正常成功: result=%+v err=%v", result, err)
		}
	})
	t.Run("generate_content_sse MAX_TOKENS 终帧同为完整", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"candidates":[{"content":{"parts":[{"text":"OK-MODEL-CHECK"}],"role":"model"}}]}`,
			``,
			`data: {"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"MAX_TOKENS"}]}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolGeminiNative), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete {
			t.Fatalf("finishReason 非空即终态（不限 STOP）: result=%+v err=%v", result, err)
		}
	})
	t.Run("非 SSE JSON 响应不受终态校验约束", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			payload := `{"model":"r7a-model","choices":[{"message":{"content":"OK-MODEL-CHECK"}}],"usage":{"total_tokens":2}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
			t.Fatalf("完整 JSON 响应必须照常成功: result=%+v err=%v", result, err)
		}
	})
	t.Run("流内失败信封优先不叠加传输不完整", func(t *testing.T) {
		body := r7aSSEBody(
			`data: {"error":{"message":"r7a upstream exploded"}}`,
			``,
		)
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.Success || result.Incomplete {
			t.Fatalf("流内失败信封是失败定论不是传输截断: result=%+v err=%v", result, err)
		}
		if !strings.Contains(result.ErrorMessage, "r7a upstream exploded") {
			t.Fatalf("错误信封原文必须保留: %q", result.ErrorMessage)
		}
	})
}

// P-H2：流式 chat reasoning_content 是推理痕迹，即使携带哨兵而 content 为空
// 也不并入判分 Output（Output 为空即不可能被判对）。
func TestR7aStreamingReasoningContentNeverEntersJudgedOutput(t *testing.T) {
	body := r7aSSEBody(
		`data: {"model":"r7a-model","choices":[{"delta":{"reasoning_content":"R7A-REASONING-SENTINEL"}}]}`,
		``,
		`data: [DONE]`,
		``,
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "R7A-REASONING-SENTINEL") {
		t.Fatalf("reasoning_content 不得并入判分 Output: %q", result.Output)
	}
	if strings.TrimSpace(result.Output) != "" {
		t.Fatalf("content 为空时 Output 必须为空: %q", result.Output)
	}
}

// P-H3：通用 responses 解析器对 response.completed 携带非 completed status
// （incomplete/failed）保持失败口径（与 Codex 臂同构），完整 200 质量失败
// 不置 Incomplete。
func TestR7aResponsesCompletedWithNonCompletedStatusFails(t *testing.T) {
	body := r7aSSEBody(
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output_text":"partial answer","status":"incomplete"}}`,
		``,
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	result, err := Execute(context.Background(), r7aStreamRequest(t, modelcheckprofile.ProtocolOpenAIResponses), Options{Endpoint: "https://r7a.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil || result.Success || result.Incomplete {
		t.Fatalf("status=incomplete 的 completed 必须失败且非传输截断: result=%+v err=%v", result, err)
	}
	if result.ErrorMessage != "response.completed status incomplete" {
		t.Fatalf("errorMessage=%q", result.ErrorMessage)
	}
	_, _, _, _, parseErrorMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
	if parseErrorMessage != "response.completed status incomplete" {
		t.Fatalf("parseResponseDetailed errorMessage=%q", parseErrorMessage)
	}
}

// r7aQuizAnswerEnvelopeTransport 作答跳恒返回 200 错误信封，裁判跳返回 fail
// 判定：若门失效（错误信封文本冒充作答送裁判），题项会判 failed 扣满 31 分，
// 与修复后的 skipped 不扣分形成红绿对照。
type r7aQuizAnswerEnvelopeTransport struct {
	envelope   string
	answerReqs int
	gradeReqs  int
}

func (t *r7aQuizAnswerEnvelopeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	if strings.Contains(text, "Reference answer:") {
		t.gradeReqs++
		return r7aChatOKResponse(request, `{"verdict":"fail","reason":"答案不匹配"}`)
	}
	if strings.Contains(text, quizTestQuestionText) {
		t.answerReqs++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(t.envelope)), Request: request}, nil
	}
	return r7aChatOKResponse(request, "OK-MODEL-CHECK")
}

func r7aChatOKResponse(request *http.Request, content string) (*http.Response, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	payload := `{"model":"r7a-model","choices":[{"message":{"content":` + string(encoded) + `}}],"usage":{"total_tokens":2}}`
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
}

// P-H5：quiz 作答跳 200 错误信封/空输出是非终态失败——不送裁判、按既有请求
// 失败路径记 error 证据、不扣分不入分母，家族继续后续题目；诊断链路同口径
// 转 request_failed 正常结果。
func TestR7aQuizAnswerHopNonTerminalFailureSkipsGrader(t *testing.T) {
	t.Run("家族：200 错误信封不送裁判不扣分", func(t *testing.T) {
		transport := &r7aQuizAnswerEnvelopeTransport{envelope: `{"error":{"message":"r7a answer hop upstream failure"}}`}
		suite := quizTestSuite("quick", nil)
		suite.Client = &http.Client{Transport: transport}
		suite.QuizQuestions = quizTestQuestions()
		items, err := RunQuizFamily(context.Background(), suite, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		quizItems := findQuizItems(items)
		if len(quizItems) != 2 {
			t.Fatalf("非终态失败不终止家族，两题各自留证: %+v", items)
		}
		for _, item := range quizItems {
			if item.Status != "skipped" || item.Score != 0 {
				t.Fatalf("非终态失败必须 skipped 不计分: %+v", item)
			}
			for _, key := range []string{"requestFailure", "excludedFromScoring", "evidenceInsufficient"} {
				if item.Evidence[key] != true {
					t.Fatalf("非终态失败证据缺 %s: %+v", key, item.Evidence)
				}
			}
			if item.Evidence["error"] != "r7a answer hop upstream failure" || item.Evidence["httpStatus"] != http.StatusOK {
				t.Fatalf("错误信封原文必须留证: %+v", item.Evidence)
			}
			if item.Evidence["terminalFailure"] == true {
				t.Fatalf("非终态失败不得携带 terminalFailure: %+v", item.Evidence)
			}
		}
		if transport.gradeReqs != 0 {
			t.Fatalf("错误信封文本不得冒充作答送裁判: grade=%d", transport.gradeReqs)
		}
		if transport.answerReqs != 2 {
			t.Fatalf("两题各作答一次: answer=%d", transport.answerReqs)
		}
		summary := SummarizeChecks(append(quizBaseChecks(), quizItems...), false, "full")
		if summary.Score != 100 {
			t.Fatalf("非终态失败不得扣分: %+v", summary)
		}
	})
	t.Run("家族：空 choices 作答不送裁判", func(t *testing.T) {
		transport := &r7aQuizAnswerEnvelopeTransport{envelope: `{"model":"r7a-model","choices":[],"usage":{"total_tokens":0}}`}
		suite := quizTestSuite("quick", nil)
		suite.Client = &http.Client{Transport: transport}
		suite.QuizQuestions = quizTestQuestions()[:1]
		items, err := RunQuizFamily(context.Background(), suite, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		quizItems := findQuizItems(items)
		if len(quizItems) != 1 || quizItems[0].Status != "skipped" {
			t.Fatalf("空 choices 作答必须按非终态失败留证: %+v", items)
		}
		if quizItems[0].Evidence["error"] != "J3b quiz answer hop produced no usable output" {
			t.Fatalf("空输出必须带说明性 error 证据: %+v", quizItems[0].Evidence)
		}
		if transport.gradeReqs != 0 {
			t.Fatalf("空作答不得送裁判: grade=%d", transport.gradeReqs)
		}
	})
	t.Run("诊断：错误信封转 request_failed 正常结果", func(t *testing.T) {
		transport := &r7aQuizAnswerEnvelopeTransport{envelope: `{"error":{"message":"r7a answer hop upstream failure"}}`}
		suite := quizDiagnosisTestSuite(nil)
		suite.Client = &http.Client{Transport: transport}
		diagnosis, err := RunQuizDiagnosis(context.Background(), suite, time.Second)
		if err != nil {
			t.Fatalf("非终态失败必须是正常结果: %v", err)
		}
		if diagnosis.Verdict != "request_failed" || diagnosis.HTTPStatus != http.StatusOK {
			t.Fatalf("diagnosis=%+v", diagnosis)
		}
		if !strings.Contains(diagnosis.ErrorMessage, "r7a answer hop upstream failure") {
			t.Fatalf("errorMessage=%q", diagnosis.ErrorMessage)
		}
		if transport.gradeReqs != 0 {
			t.Fatalf("错误信封文本不得送诊断裁判: grade=%d", transport.gradeReqs)
		}
	})
}

// W-H1：对比账户自身 modelMismatch 不得把目标 run 拉成 suspicious（对比项
// 异常走 comparison 聚合路径）；目标自身 mismatch 仍照常 suspicious。
func TestR7aComparisonMismatchDoesNotPoisonTargetSummary(t *testing.T) {
	targetOK := []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}}
	comparisonMismatch := []Evaluation{{Kind: "trusted_comparison.protocol_basic", Status: "failed", Score: 0, MaxScore: 10, Evidence: map[string]any{"success": true, "modelMismatch": true}}}
	got := SummarizeChecks(append(targetOK, comparisonMismatch...), true, "quick")
	if got.Level != "likely" {
		t.Fatalf("对比项自身 mismatch 不得拉 suspicious: %+v", got)
	}
	if got.Score != 100 {
		t.Fatalf("对比项不得进入目标分母或扣分: %+v", got)
	}
	targetMismatch := []Evaluation{{Kind: "protocol_basic", Status: "failed", Score: 0, MaxScore: 10, Evidence: map[string]any{"success": true, "modelMismatch": true}}}
	poisoned := SummarizeChecks(append(targetMismatch, comparisonMismatch...), true, "quick")
	if poisoned.Level != "suspicious" {
		t.Fatalf("目标自身 mismatch 必须照常 suspicious: %+v", poisoned)
	}
}
