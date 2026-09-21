package modelcheckprobe

// quiz_test.go 覆盖题库测试家族：31 分池拆分、quick/full 家族位置、
// 顶层门禁、判分、两跳上下文隔离与 SummarizeChecks 扣分扩展。

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

const (
	quizTestQuestionText  = "quiz-question-求半径为-r-的圆面积"
	quizTestReferenceText = "quiz-reference-面积等于-pi-乘-r-平方"
	quizTestKeyPoint      = "quiz-keypoint-必须提到圆周率"
	quizTestAnswerText    = "quiz-answer-圆的面积是-pi-r-平方"
)

func quizTestQuestions() []QuizQuestion {
	return []QuizQuestion{
		{ID: "quiz-1", Title: "题目一", Question: quizTestQuestionText + "-one", ReferenceAnswer: quizTestReferenceText + "-one", KeyPoints: []string{quizTestKeyPoint + "-one"}},
		{ID: "quiz-2", Title: "题目二", Question: quizTestQuestionText + "-two", ReferenceAnswer: quizTestReferenceText + "-two"},
	}
}

// quizTransport 按 body 特征分发：含 "Reference answer:" 的是对比跳，含题面
// 特征的是作答跳，其余是核心探针。响应格式按请求 path 区分 chat/responses。
type quizTransport struct {
	bodies                 []string
	gradeOutput            string
	gradeStatus            int
	failAnswerWhenContains string
	answerRequests         int
	gradeRequests          int
}

func (t *quizTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	t.bodies = append(t.bodies, text)
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	output, status := "OK-MODEL-CHECK", http.StatusOK
	switch {
	case strings.Contains(text, "Reference answer:"):
		t.gradeRequests++
		output, status = t.gradeOutput, t.gradeStatus
	case strings.Contains(text, quizTestQuestionText):
		t.answerRequests++
		output = quizTestAnswerText
		if t.failAnswerWhenContains != "" && strings.Contains(text, t.failAnswerWhenContains) {
			status = http.StatusServiceUnavailable
			output = ""
		}
	case strings.Contains(text, "CROSS-MODEL-OK"):
		output = "CROSS-MODEL-OK"
	}
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stage failure"}}`)), Request: request}, nil
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	if strings.Contains(request.URL.Path, "/responses") {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"` + payload.Model + `","output_text":` + string(encoded) + `,"usage":{"total_tokens":2}}`)), Request: request}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"` + payload.Model + `","choices":[{"message":{"content":` + string(encoded) + `}}],"usage":{"total_tokens":2}}`)), Request: request}, nil
}

func quizTestSuite(profile string, transport *quizTransport) Suite {
	suite := Suite{
		Endpoint: "https://quiz.example",
		Client:   &http.Client{Transport: transport},
		Model:    "quiz-test-model",
		Profile:  profile,
		Protocol: modelcheckprofile.ProtocolOpenAIChat,
		Retry:    RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}
	if profile == "full" {
		suite.Tokenizer = deterministicTokenizer{}
		suite.ModelLimits = deterministicLimits{}
	}
	return suite
}

func findQuizItems(items []Evaluation) []Evaluation {
	result := make([]Evaluation, 0, 3)
	for _, item := range items {
		if unscopedKind(item.Kind) == "custom_quiz" {
			result = append(result, item)
		}
	}
	return result
}

func TestQuizWeightsSplit(t *testing.T) {
	cases := map[int][]int{
		1: {31},
		2: {16, 15},
		3: {11, 10, 10},
	}
	for n, want := range cases {
		got := quizWeights(n)
		total := 0
		for _, weight := range got {
			total += weight
		}
		if len(got) != n || total != QuizMaxPool {
			t.Fatalf("quizWeights(%d)=%v total=%d", n, got, total)
		}
		for index, weight := range want {
			if got[index] != weight {
				t.Fatalf("quizWeights(%d)=%v want=%v", n, got, want)
			}
		}
	}
}

func TestRunSuiteQuizFamilyRunsLastInQuick(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	suite.QuizRequested = true
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quizItems := findQuizItems(items)
	if len(quizItems) != 2 {
		t.Fatalf("quiz items=%+v all=%+v", quizItems, items)
	}
	if items[len(items)-1].Kind != "custom_quiz" || items[len(items)-2].Kind != "custom_quiz" || unscopedKind(items[len(items)-3].Kind) != "usage_shape" {
		t.Fatalf("quiz family must run last: kinds=%v", evaluationKinds(items))
	}
	for _, item := range quizItems {
		if item.Status != "passed" || item.Score != item.MaxScore {
			t.Fatalf("expected full credit quiz item=%+v", item)
		}
	}
}

func TestRunSuiteQuizFamilyRunsLastInFull(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("full", transport)
	suite.QuizRequested = true
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quizItems := findQuizItems(items)
	if len(quizItems) != 2 {
		t.Fatalf("quiz items=%+v all=%+v", quizItems, items)
	}
	if items[len(items)-1].Kind != "custom_quiz" || items[len(items)-2].Kind != "custom_quiz" || unscopedKind(items[len(items)-3].Kind) != "usage_shape" {
		t.Fatalf("quiz family must run last in full: kinds=%v", evaluationKinds(items))
	}
}

func evaluationKinds(items []Evaluation) []string {
	kinds := make([]string, 0, len(items))
	for _, item := range items {
		kinds = append(kinds, item.Kind)
	}
	return kinds
}

func TestRunSuiteQuizNotRunForNestedTrustedComparison(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	// 模拟可信对比递归调用的嵌套套件形态：Prefix 非空且带着题目配置。
	suite.Prefix = "trusted_comparison"
	suite.QuizRequested = true
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if quizItems := findQuizItems(items); len(quizItems) != 0 {
		t.Fatalf("nested suite must not run quiz family: %+v", quizItems)
	}
	if transport.answerRequests != 0 || transport.gradeRequests != 0 {
		t.Fatalf("nested suite must not issue quiz requests: answer=%d grade=%d", transport.answerRequests, transport.gradeRequests)
	}
}

func TestRunSuiteQuizNotRunWhenNotRequested(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	// 题目已解析但配置未启用题库环节：不得产生任何题库项或请求。
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if quizItems := findQuizItems(items); len(quizItems) != 0 {
		t.Fatalf("unrequested quiz family must stay silent: %+v", quizItems)
	}
	if transport.answerRequests != 0 || transport.gradeRequests != 0 {
		t.Fatalf("unrequested quiz family must not issue requests: answer=%d grade=%d", transport.answerRequests, transport.gradeRequests)
	}
}

func TestRunSuiteQuizSkipsWhenQuestionsUnavailable(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	suite.QuizRequested = true
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quizItems := findQuizItems(items)
	if len(quizItems) != 1 {
		t.Fatalf("expected one unavailable quiz item: %+v", quizItems)
	}
	item := quizItems[0]
	if item.Status != "skipped" || item.MaxScore != 0 || item.Score != 0 {
		t.Fatalf("unavailable quiz item=%+v", item)
	}
	if item.Evidence["excludedFromScoring"] != true || item.Evidence["evidenceInsufficient"] != true || item.Evidence["reason"] != "quiz_questions_unavailable" {
		t.Fatalf("unavailable quiz evidence=%+v", item.Evidence)
	}
	if transport.answerRequests != 0 || transport.gradeRequests != 0 {
		t.Fatalf("unavailable quiz family must not issue requests")
	}
}

func TestRunQuizFamilyGradesVerdicts(t *testing.T) {
	transport := &quizTransport{
		gradeOutput: `{"verdict":"fail","reason":"答案不符"}`,
	}
	// 单题家族：分别验证 pass / fail / invalid 三种判定。pass 题先跑。
	passTransport := &quizTransport{gradeOutput: `前置说明 {"verdict":"PASS","reason":"结论一致"}`}
	questions := []QuizQuestion{{ID: "quiz-1", Title: "题目一", Question: quizTestQuestionText, ReferenceAnswer: quizTestReferenceText}}
	passItems, err := RunQuizFamily(context.Background(), func() Suite {
		suite := quizTestSuite("quick", passTransport)
		suite.QuizQuestions = questions
		return suite
	}(), time.Second)
	if err != nil || len(passItems) != 1 {
		t.Fatalf("items=%+v err=%v", passItems, err)
	}
	if passItems[0].Status != "passed" || passItems[0].Score != QuizMaxPool || passItems[0].MaxScore != QuizMaxPool {
		t.Fatalf("pass item=%+v", passItems[0])
	}
	if passItems[0].Evidence["verdict"] != "pass" || passItems[0].Evidence["reason"] != "结论一致" {
		t.Fatalf("pass evidence=%+v", passItems[0].Evidence)
	}

	failItems, err := RunQuizFamily(context.Background(), func() Suite {
		suite := quizTestSuite("quick", transport)
		suite.QuizQuestions = questions
		return suite
	}(), time.Second)
	if err != nil || len(failItems) != 1 {
		t.Fatalf("items=%+v err=%v", failItems, err)
	}
	if failItems[0].Status != "failed" || failItems[0].Score != 0 || failItems[0].MaxScore != QuizMaxPool {
		t.Fatalf("fail item=%+v", failItems[0])
	}
	if failItems[0].Evidence["verdict"] != "fail" || failItems[0].Evidence["reason"] != "答案不符" {
		t.Fatalf("fail evidence=%+v", failItems[0].Evidence)
	}

	invalidTransport := &quizTransport{gradeOutput: `模型拒绝输出结构化判定`}
	invalidItems, err := RunQuizFamily(context.Background(), func() Suite {
		suite := quizTestSuite("quick", invalidTransport)
		suite.QuizQuestions = questions
		return suite
	}(), time.Second)
	if err != nil || len(invalidItems) != 1 {
		t.Fatalf("items=%+v err=%v", invalidItems, err)
	}
	if invalidItems[0].Status != "failed" || invalidItems[0].Score != 0 {
		t.Fatalf("invalid item=%+v", invalidItems[0])
	}
	if invalidItems[0].Evidence["verdict"] != "invalid" || invalidItems[0].Evidence["reason"] != "模型未能输出有效对比判定" {
		t.Fatalf("invalid evidence=%+v", invalidItems[0].Evidence)
	}
}

func TestRunQuizFamilyTerminalStopsFamily(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`, failAnswerWhenContains: "-two"}
	suite := quizTestSuite("quick", transport)
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunQuizFamily(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("terminal family must stop after the failing question: %+v", items)
	}
	first := items[0]
	if first.Status != "warning" {
		t.Fatalf("completed pass item must degrade to warning: %+v", first)
	}
	if first.Evidence["requestFailure"] != true || first.Evidence["excludedFromScoring"] != true || first.Evidence["terminalFailure"] != true {
		t.Fatalf("terminal first item evidence=%+v", first.Evidence)
	}
	second := items[1]
	if second.Status != "skipped" {
		t.Fatalf("failing question must stay skipped: %+v", second)
	}
	if second.Evidence["requestFailure"] != true || second.Evidence["excludedFromScoring"] != true || second.Evidence["terminalFailure"] != true || second.Evidence["httpStatus"] != http.StatusServiceUnavailable {
		t.Fatalf("failing question evidence=%+v", second.Evidence)
	}
	// 第 1 题两跳 + 第 2 题作答跳 3 次重试；第 2 题不得再有对比跳。
	if transport.gradeRequests != 1 {
		t.Fatalf("grade requests=%d want=1", transport.gradeRequests)
	}
}

func TestRunQuizFamilyKeepsHopsIsolated(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	suite.QuizQuestions = quizTestQuestions()[:1]
	if _, err := RunQuizFamily(context.Background(), suite, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(transport.bodies) != 2 {
		t.Fatalf("expected exactly two hop requests: %d", len(transport.bodies))
	}
	answerBody, gradeBody := transport.bodies[0], transport.bodies[1]
	if !strings.Contains(answerBody, quizTestQuestionText) {
		t.Fatalf("answer hop must carry the raw question: %s", answerBody)
	}
	for _, forbidden := range []string{quizTestReferenceText, quizTestKeyPoint, "Reference answer:", "Grading key points:", quizGradingInstructions, "verdict"} {
		if strings.Contains(answerBody, forbidden) {
			t.Fatalf("answer hop must not carry reference material %q: %s", forbidden, answerBody)
		}
	}
	for _, required := range []string{quizTestQuestionText, quizTestAnswerText, quizTestReferenceText, quizTestKeyPoint, "Reference answer:", "Grading key points:"} {
		if !strings.Contains(gradeBody, required) {
			t.Fatalf("grade hop must carry %q: %s", required, gradeBody)
		}
	}
	var gradePayload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(gradeBody), &gradePayload); err != nil {
		t.Fatal(err)
	}
	// 全新独立上下文：仅 system + user 两条消息，无作答跳的消息结构。
	if len(gradePayload.Messages) != 2 || gradePayload.Messages[0].Role != "system" || gradePayload.Messages[1].Role != "user" {
		t.Fatalf("grade hop messages=%+v", gradePayload.Messages)
	}
	if gradePayload.Messages[0].Content != quizGradingInstructions {
		t.Fatalf("grade hop system instructions=%q", gradePayload.Messages[0].Content)
	}
}

func TestApplyQuizGradingInstructionsPerProtocol(t *testing.T) {
	cases := []struct {
		protocol modelcheckprofile.Protocol
		check    func(payload map[string]any) bool
	}{
		{modelcheckprofile.ProtocolOpenAIResponses, func(payload map[string]any) bool { return payload["instructions"] == quizGradingInstructions }},
		{modelcheckprofile.ProtocolOpenAIChat, func(payload map[string]any) bool {
			messages, _ := payload["messages"].([]any)
			if len(messages) != 2 {
				return false
			}
			system, _ := messages[0].(map[string]any)
			return system["role"] == "system" && system["content"] == quizGradingInstructions
		}},
		{modelcheckprofile.ProtocolAnthropic, func(payload map[string]any) bool { return payload["system"] == quizGradingInstructions }},
		{modelcheckprofile.ProtocolGeminiNative, func(payload map[string]any) bool {
			instruction, _ := payload["systemInstruction"].(map[string]any)
			parts, _ := instruction["parts"].([]any)
			if len(parts) != 1 {
				return false
			}
			part, _ := parts[0].(map[string]any)
			return part["text"] == quizGradingInstructions
		}},
	}
	for _, testCase := range cases {
		request, err := tunedBasicTestRequest(testCase.protocol)
		if err != nil {
			t.Fatal(err)
		}
		request, err = applyQuizGradingInstructions(request, testCase.protocol, quizGradingInstructions)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if !testCase.check(payload) {
			t.Fatalf("protocol %q payload=%+v", testCase.protocol, payload)
		}
	}
}

func tunedBasicTestRequest(protocol modelcheckprofile.Protocol) (Request, error) {
	suite := Suite{Model: "quiz-test-model", Protocol: protocol}
	return suite.tunedBasic("quiz-test-model", "quiz prompt", modelcheckprofile.EndpointModeForProtocol(protocol, false), false, quizMaxOutputTokens, 0)
}

func TestRunQuizFamilyUsesBoundedOutputBudget(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	suite.QuizQuestions = quizTestQuestions()[:1]
	if _, err := RunQuizFamily(context.Background(), suite, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, body := range transport.bodies {
		if !strings.Contains(body, `"max_tokens":1024`) {
			t.Fatalf("quiz hop must request %d output tokens: %s", quizMaxOutputTokens, body)
		}
	}
}

func TestRunQuizFamilyEvidenceNeverCarriesReferenceMaterial(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","reason":"对"}`}
	suite := quizTestSuite("quick", transport)
	suite.QuizQuestions = quizTestQuestions()
	items, err := RunQuizFamily(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		encoded, err := json.Marshal(item.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		if strings.Contains(text, quizTestReferenceText) || strings.Contains(text, quizTestKeyPoint) {
			t.Fatalf("quiz evidence leaked reference material: %s", text)
		}
		excerpt, _ := item.Evidence["answerExcerpt"].(string)
		if len([]rune(excerpt)) > answerExcerptLimit {
			t.Fatalf("answer excerpt too long: %d", len([]rune(excerpt)))
		}
		if item.Evidence["questionId"] == "" || item.Evidence["questionTitle"] == "" {
			t.Fatalf("quiz evidence missing question identity: %+v", item.Evidence)
		}
	}
}

func quizBaseChecks() []Evaluation {
	return []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "cross_model", Status: "passed", Score: 10, MaxScore: 10},
	}
}

func TestSummarizeChecksQuizDeductionSingleFailedQuestion(t *testing.T) {
	checks := append(quizBaseChecks(), Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 31, Evidence: map[string]any{"verdict": "fail"}})
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 100-QuizMaxPool {
		t.Fatalf("score=%d want=%d", got.Score, 100-QuizMaxPool)
	}
}

func TestSummarizeChecksQuizDeductionThreeFailedQuestions(t *testing.T) {
	checks := quizBaseChecks()
	checks = append(checks,
		Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 11, Evidence: map[string]any{"verdict": "fail"}},
		Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 10, Evidence: map[string]any{"verdict": "fail"}},
		Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 10, Evidence: map[string]any{"verdict": "fail"}},
	)
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 100-QuizMaxPool {
		t.Fatalf("score=%d want=%d", got.Score, 100-QuizMaxPool)
	}
}

func TestSummarizeChecksQuizTerminalFamilyDoesNotDeduct(t *testing.T) {
	checks := append(quizBaseChecks(), Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 16, Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true}})
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 100 {
		t.Fatalf("terminal quiz family must not deduct: score=%d", got.Score)
	}
	if got.Level == "high_confidence" {
		t.Fatalf("quiz failed count must block the highest confidence level: %+v", got)
	}
}

func TestSummarizeChecksQuizSkippedKeepsScoreAndLevel(t *testing.T) {
	checks := append(quizBaseChecks(), Evaluation{Kind: "custom_quiz", Status: "skipped", Evidence: map[string]any{"excludedFromScoring": true, "reason": "quiz_questions_unavailable"}})
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 100 || got.Level != "high_confidence" {
		t.Fatalf("skipped quiz item must not affect the summary: %+v", got)
	}
}

func TestSummarizeChecksQuizDeductionClampsToZero(t *testing.T) {
	checks := append(quizBaseChecks(),
		Evaluation{Kind: "juice", Status: "failed", Evidence: map[string]any{"hardAnomaly": true, "scorePenalty": 80}},
		Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 31, Evidence: map[string]any{"verdict": "fail"}},
	)
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 0 {
		t.Fatalf("deduction must clamp at zero: score=%d", got.Score)
	}
	if got.Level != "suspicious" {
		t.Fatalf("juice hard anomaly must keep the suspicious short-circuit: %+v", got)
	}
}

func TestSummarizeChecksQuizQuickLadderUsesDeductedScore(t *testing.T) {
	checks := append(quizBaseChecks(), Evaluation{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 31, Evidence: map[string]any{"verdict": "fail"}})
	got := SummarizeChecks(checks, false, "quick")
	if got.Level != "uncertain" {
		t.Fatalf("quick ladder must see the deducted score 69: %+v", got)
	}
}

func TestSummarizeChecksIgnoresNestedPrefixQuizItems(t *testing.T) {
	checks := append(quizBaseChecks(), Evaluation{Kind: "trusted_comparison.custom_quiz", Status: "failed", Score: 0, MaxScore: 31, Evidence: map[string]any{"verdict": "fail"}})
	got := SummarizeChecks(checks, false, "full")
	if got.Score != 100 || got.Level != "high_confidence" {
		t.Fatalf("nested quiz items must not touch the main score: %+v", got)
	}
}
