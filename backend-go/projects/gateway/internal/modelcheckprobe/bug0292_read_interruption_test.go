package modelcheckprobe

// BUG-0292 回归：模型检测探针对"HTTP 200 但响应体未完整读取"的截断态必须
// 按传输面请求失败处理——保留已读部分作排障证据、消耗重试阶梯晋级更大预算；
// 末轮仍不完整时判家族终态（题库族 skipped + excludedFromScoring 不扣分）。
// 已读内容语义完整（完整 JSON 或流式终态事件）且无流内失败信封时按完整响应
// 判定。与 accountprobe BUG-0287（probe_budget_timeout_test.go）同构。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// bug0292FailReader 在读取时返回固定错误，驱动真实读取中断分支。
type bug0292FailReader struct{}

func (bug0292FailReader) Read([]byte) (int, error) {
	return 0, errors.New("bug0292 synthetic read failure")
}

// bug0292DeadlineBody 先吐出 head，随后阻塞到请求 ctx 到点并返回 ctx 错误，
// 等价探针本级预算在响应体中途到点截断仍在活跃输出的流。
type bug0292DeadlineBody struct {
	ctx     context.Context
	head    string
	emitted bool
}

func (b *bug0292DeadlineBody) Read(p []byte) (int, error) {
	if !b.emitted {
		b.emitted = true
		return copy(p, b.head), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *bug0292DeadlineBody) Close() error { return nil }

func bug0292BasicRequest(t *testing.T, protocol modelcheckprofile.Protocol) Request {
	t.Helper()
	request, err := BuildBasic(protocol, "bug0292-model", "Reply with exactly: OK-MODEL-CHECK", false)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func bug0292ChatResponse(request *http.Request, content string) (*http.Response, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	payload := `{"model":"bug0292-model","choices":[{"message":{"content":` + string(encoded) + `}}],"usage":{"total_tokens":2}}`
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
}

// 读中断（先吐部分流再报错）：状态码保留、Incomplete 置位、已读部分文本
// 保留为证据、错误按真实读取中断归类。
func TestBug0292ExecuteReadInterruptionKeepsPartialEvidence(t *testing.T) {
	t.Run("SSE 增量中断", func(t *testing.T) {
		partial := strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","delta":"bug0292 partial answer"}`,
			``,
		}, "\n")
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(partial), bug0292FailReader{})), Request: request}, nil
		})
		result, err := Execute(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIResponses), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.ErrorMessage != "J3b upstream response read failed" {
			t.Fatalf("真实读取中断必须保持既有文案: %q", result.ErrorMessage)
		}
		if !strings.Contains(result.Output, "bug0292 partial answer") {
			t.Fatalf("已读部分文本必须保留为排障证据: %q", result.Output)
		}
	})
	t.Run("JSON 中途截断", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"choices":[{"message":{"content":"bug0292 partial ans`), bug0292FailReader{})), Request: request}, nil
		})
		result, err := Execute(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
		if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.ErrorMessage != "J3b upstream response read failed" {
			t.Fatalf("真实读取中断必须保持既有文案: %q", result.ErrorMessage)
		}
		if !strings.Contains(result.Output, "bug0292 partial ans") {
			t.Fatalf("已读部分文本必须保留为排障证据: %q", result.Output)
		}
	})
}

// 本级预算在响应体中途到点：按探针超时归类（保留阶梯晋级证据），Incomplete
// 置位，已读部分保留。
func TestBug0292ExecuteBudgetDeadlineMidBodyTimesOut(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &bug0292DeadlineBody{ctx: request.Context(), head: `{"choices":[{"delta":{"content":"bug0292 partial"`}, Request: request}, nil
	})
	result, err := Execute(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}, Timeout: 20 * time.Millisecond})
	if err != nil || result.HTTPStatus != http.StatusOK || result.Success || !result.Incomplete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.ErrorMessage != "J3b probe timed out" {
		t.Fatalf("预算到点截断必须按探针超时归类: %q", result.ErrorMessage)
	}
	if !strings.Contains(result.Output, "bug0292 partial") {
		t.Fatalf("已读部分文本必须保留为排障证据: %q", result.Output)
	}
}

// 已读体已含流式终态事件但连接报错：尾部传输错误不得覆盖语义成功。
func TestBug0292ExecuteTerminalEventSurvivesReadError(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output_text":"bug0292 final","status":"completed","usage":{"total_tokens":2}}}`,
		``,
	}, "\n")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(stream), bug0292FailReader{})), Request: request}, nil
	})
	result, err := Execute(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIResponses), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil || !result.Success || result.Incomplete || result.ErrorMessage != "" {
		t.Fatalf("终态事件已到必须按完整响应判定: result=%+v err=%v", result, err)
	}
	if result.Output != "bug0292 final" {
		t.Fatalf("语义成功必须取终态输出: %q", result.Output)
	}
}

// 完整 JSON 已到但后续读报错：同样按完整响应判定。
func TestBug0292ExecuteCompleteJSONSurvivesReadError(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"model":"bug0292-model","choices":[{"message":{"content":"OK-MODEL-CHECK"}}],"usage":{"total_tokens":2}}`), bug0292FailReader{})), Request: request}, nil
	})
	result, err := Execute(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("完整 JSON 已到必须按完整响应判定: result=%+v err=%v", result, err)
	}
}

// 重试层：Incomplete 200 首轮截断、次轮完整成功——阶梯晋级治愈慢流。
func TestBug0292RetryEscalatesTruncatedOKToNextBudget(t *testing.T) {
	attempts := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: &bug0292DeadlineBody{ctx: request.Context(), head: `{"choices":[{"delta":{"content":"partial"`}, Request: request}, nil
		}
		return bug0292ChatResponse(request, "OK-MODEL-CHECK")
	})
	result, err := ExecuteWithRetry(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}}, RetryOptions{AttemptTimeouts: []time.Duration{15 * time.Millisecond, time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("首轮截断必须晋级重试: attempts=%d", attempts)
	}
	if !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("次轮完整响应必须成功: %+v", result)
	}
	if len(result.AttemptStatusCodes) != 2 || result.RetryAttemptCount != 1 || result.RetryMaxAttempts != 3 {
		t.Fatalf("retry evidence=%+v", result)
	}
}

// 重试层：三轮全截断——阶梯耗尽后末轮返回，截断 200 判家族终态。
func TestBug0292RetryExhaustionOfTruncatedOKIsFamilyTerminal(t *testing.T) {
	attempts := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"choices":[{"delta":{"content":"partial"`), bug0292FailReader{})), Request: request}, nil
	})
	result, err := ExecuteWithRetry(context.Background(), bug0292BasicRequest(t, modelcheckprofile.ProtocolOpenAIChat), Options{Endpoint: "https://bug0292.example", Client: &http.Client{Transport: transport}}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("三轮全截断必须耗尽阶梯: attempts=%d", attempts)
	}
	if result.Success || !result.Incomplete || result.HTTPStatus != http.StatusOK || result.ErrorMessage != "J3b upstream response read failed" {
		t.Fatalf("末轮必须按截断结果返回: %+v", result)
	}
	if result.RetryAttemptCount != 2 || result.RetryMaxAttempts != 3 || len(result.AttemptStatusCodes) != 3 {
		t.Fatalf("retry evidence=%+v", result)
	}
	if !isTerminalProbeFailure(result) {
		t.Fatalf("末轮截断 200 必须判家族终态: %+v", result)
	}
	// 双绿卫兵：未耗尽阶梯的截断态不判终态；完整 200 语义失败保持非终态。
	if isTerminalProbeFailure(Result{HTTPStatus: http.StatusOK, Incomplete: true, RetryAttemptCount: 0, RetryMaxAttempts: 3, AttemptStatusCodes: []int{http.StatusOK}}) {
		t.Fatalf("未耗尽阶梯的截断态不得判终态")
	}
	if isTerminalProbeFailure(Result{HTTPStatus: http.StatusOK, ErrorMessage: "model_not_found"}) {
		t.Fatalf("完整 200 语义失败必须保持非终态")
	}
}

// bug0292QuizTransport 按请求内容区分题库两跳，并按预置次数返回预算截断流
// （200 + 截断 JSON 头 + 挂起到本级预算到点）。
type bug0292QuizTransport struct {
	answerTruncations int
	gradeTruncations  int
	answerRequests    int
	gradeRequests     int
	gradeOutput       string
}

func (t *bug0292QuizTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	truncatedHead := `{"choices":[{"delta":{"content":"` + quizTestAnswerText
	if strings.Contains(text, "Reference answer:") {
		t.gradeRequests++
		if t.gradeRequests <= t.gradeTruncations {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &bug0292DeadlineBody{ctx: request.Context(), head: truncatedHead}, Request: request}, nil
		}
		return bug0292ChatResponse(request, t.gradeOutput)
	}
	if strings.Contains(text, quizTestQuestionText) {
		t.answerRequests++
		if t.answerRequests <= t.answerTruncations {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &bug0292DeadlineBody{ctx: request.Context(), head: truncatedHead}, Request: request}, nil
		}
		return bug0292ChatResponse(request, quizTestAnswerText)
	}
	return bug0292ChatResponse(request, "OK-MODEL-CHECK")
}

// bug0292QuizSuite 镜像 quizTestSuite 的隔离套件夹具，接受任意 RoundTripper。
func bug0292QuizSuite(transport http.RoundTripper) Suite {
	suite := Suite{
		Endpoint: "https://quiz.example",
		Client:   &http.Client{Transport: transport},
		Model:    "bug0292-model",
		Profile:  "quick",
		Protocol: modelcheckprofile.ProtocolOpenAIChat,
		Retry:    RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}
	return suite
}

// 题库层：作答跳三轮截断 → 家族 terminal → skipped + excludedFromScoring，
// 部分作答文本只作证据留存，绝不进入裁判，也不扣分（与既有扣 31 用例形成
// 红绿对照）。
func TestBug0292QuizAnswerHopTruncationIsTerminalWithoutDeduction(t *testing.T) {
	transport := &bug0292QuizTransport{answerTruncations: 99}
	suite := bug0292QuizSuite(transport)
	suite.QuizQuestions = quizTestQuestions()[:1]
	items, err := RunQuizFamily(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("作答跳终态必须终止家族: %+v", items)
	}
	item := items[0]
	if item.Status != "skipped" || item.MaxScore != 0 || item.Score != 0 {
		t.Fatalf("截断终态项不得计分: %+v", item)
	}
	if item.Evidence["requestFailure"] != true || item.Evidence["excludedFromScoring"] != true || item.Evidence["evidenceInsufficient"] != true || item.Evidence["terminalFailure"] != true {
		t.Fatalf("terminal evidence=%+v", item.Evidence)
	}
	if item.Evidence["httpStatus"] != http.StatusOK || item.Evidence["error"] != "J3b probe timed out" {
		t.Fatalf("truncation evidence=%+v", item.Evidence)
	}
	excerpt, _ := item.Evidence["answerExcerpt"].(string)
	if !strings.Contains(excerpt, quizTestAnswerText) {
		t.Fatalf("部分作答文本必须留存为证据: %+v", item.Evidence)
	}
	if transport.gradeRequests != 0 {
		t.Fatalf("截断的部分作答不得进入裁判: grade=%d", transport.gradeRequests)
	}
	if transport.answerRequests != 3 {
		t.Fatalf("作答跳必须耗尽阶梯: attempts=%d", transport.answerRequests)
	}
	summary := SummarizeChecks(append(quizBaseChecks(), item), false, "full")
	if summary.Score != 100 {
		t.Fatalf("截断终态不得扣分: %+v", summary)
	}
}

// 题库层：作答跳首轮截断、次轮完整——正常进入裁判并判分。
func TestBug0292QuizAnswerHopRecoversOnSecondAttempt(t *testing.T) {
	transport := &bug0292QuizTransport{answerTruncations: 1, gradeOutput: `{"verdict":"pass","reason":"结论一致"}`}
	suite := bug0292QuizSuite(transport)
	suite.QuizQuestions = quizTestQuestions()[:1]
	items, err := RunQuizFamily(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "passed" || items[0].Score != QuizMaxPool || items[0].MaxScore != QuizMaxPool {
		t.Fatalf("首轮截断次轮完整必须正常判分: %+v", items)
	}
	if transport.answerRequests != 2 || transport.gradeRequests != 1 {
		t.Fatalf("answer=%d grade=%d", transport.answerRequests, transport.gradeRequests)
	}
}

// 题库层：裁判跳截断耗尽 → 同作答跳的家族终态口径，不扣分。
func TestBug0292QuizGradeHopTruncationIsTerminalWithoutDeduction(t *testing.T) {
	transport := &bug0292QuizTransport{gradeTruncations: 99, gradeOutput: `{"verdict":"pass","reason":"结论一致"}`}
	suite := bug0292QuizSuite(transport)
	suite.QuizQuestions = quizTestQuestions()[:1]
	items, err := RunQuizFamily(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("裁判跳终态必须终止家族: %+v", items)
	}
	item := items[0]
	if item.Status != "skipped" || item.MaxScore != 0 || item.Score != 0 {
		t.Fatalf("裁判跳截断终态不得计分: %+v", item)
	}
	if item.Evidence["terminalFailure"] != true || item.Evidence["excludedFromScoring"] != true || item.Evidence["error"] != "J3b probe timed out" {
		t.Fatalf("grade hop truncation evidence=%+v", item.Evidence)
	}
	if transport.answerRequests != 1 || transport.gradeRequests != 3 {
		t.Fatalf("answer=%d grade=%d", transport.answerRequests, transport.gradeRequests)
	}
	summary := SummarizeChecks(append(quizBaseChecks(), item), false, "full")
	if summary.Score != 100 {
		t.Fatalf("截断终态不得扣分: %+v", summary)
	}
}

// 五核心探针截断终态（Incomplete 200 耗尽阶梯）必须保持 skipped + 请求失败
// 证据全量保留，不得被 200 分支剥离后按计分 failed 输出（§1.2 终止不得处罚）。
func TestBug0292CoreProbeTruncationTerminalKeepsRequestFailureEvidence(t *testing.T) {
	truncated := Result{
		HTTPStatus:         http.StatusOK,
		Success:            false,
		Incomplete:         true,
		ErrorMessage:       "J3b probe timed out",
		RetryAttemptCount:  2,
		RetryMaxAttempts:   3,
		AttemptStatusCodes: []int{http.StatusOK, http.StatusOK, http.StatusOK},
	}
	if !isTerminalProbeFailure(truncated) {
		t.Fatalf("三轮截断必须是家族终态: %+v", truncated)
	}
	for _, testCase := range []struct {
		name     string
		evaluate func(Result) Evaluation
	}{
		{"protocol_basic", func(result Result) Evaluation { return EvaluateBasic(result, "bug0292-model") }},
		{"tool_calling", func(result Result) Evaluation { return EvaluateTool(result, "bug0292-model") }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			item := testCase.evaluate(truncated)
			if item.Status != "skipped" || item.MaxScore != 0 {
				t.Fatalf("截断终态不得按计分 failed 输出: %+v", item)
			}
			for _, key := range []string{"requestFailure", "excludedFromScoring", "evidenceInsufficient", "terminalFailure"} {
				if item.Evidence[key] != true {
					t.Fatalf("截断终态证据缺 %s: %+v", key, item.Evidence)
				}
			}
			if item.Evidence["httpStatus"] != http.StatusOK || item.Evidence["error"] != "J3b probe timed out" {
				t.Fatalf("截断终态证据不符: %+v", item.Evidence)
			}
		})
	}
}

// SummarizeChecks：中后段核心探针截断终态经 requestFailure+terminalFailure
// 证据触发整轮 unavailable（hasTerminalCoreProbeFailure 既有口径）。
func TestBug0292SummarizeChecksCoreTruncationIsUnavailable(t *testing.T) {
	truncated := Result{
		HTTPStatus:         http.StatusOK,
		Success:            false,
		Incomplete:         true,
		ErrorMessage:       "J3b probe timed out",
		RetryAttemptCount:  2,
		RetryMaxAttempts:   3,
		AttemptStatusCodes: []int{http.StatusOK, http.StatusOK, http.StatusOK},
	}
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
		EvaluateTool(truncated, "bug0292-model"),
	}
	got := SummarizeChecks(checks, false, "quick")
	if got.Level != "unavailable" || got.Message != "目标模型链路不可检测或上游不可用" {
		t.Fatalf("核心探针截断终态必须判整轮不可用: %+v", got)
	}
}

// 对照：完整读取的 200 质量失败（非 modelUnavailable）仍走 failed 计分路径，
// 200 分支收窄不得回归。
func TestBug0292Complete200QualityFailureStaysScoredFailed(t *testing.T) {
	complete := Result{HTTPStatus: http.StatusOK, Success: false, ErrorMessage: "late stream failure"}
	item := EvaluateBasic(complete, "bug0292-model")
	if item.Status != "failed" || item.MaxScore != 10 {
		t.Fatalf("完整 200 质量失败必须保持计分 failed: %+v", item)
	}
	for _, key := range []string{"requestFailure", "excludedFromScoring", "evidenceInsufficient", "terminalFailure"} {
		if item.Evidence[key] == true {
			t.Fatalf("完整 200 质量失败不得携带 %s 证据: %+v", key, item.Evidence)
		}
	}
}
