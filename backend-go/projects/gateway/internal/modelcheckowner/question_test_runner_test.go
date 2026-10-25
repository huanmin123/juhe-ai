package modelcheckowner

// question_test_runner_test.go 覆盖题库提交门禁测试执行器：fake resolver
// 断言 RunRequest 契约（SystemAccountID/TargetType/TargetID/Model，空
// revision fence）、单题最小套件的组装关键字段（模型映射、quick 档重试、
// QuizQuestions=1、Capability 账户准入键回退链）与诊断结果/账户名映射的
// 端到端链路（fake transport，不发真实 HTTP）；派发器接通时另断言每跳
// 到达派发边界的 Capability 均携带非空账户准入键（熔断门 Prepare 拦截面）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

func questionTestTarget() Target {
	return Target{
		Endpoint:                  "https://qbtest.example",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "openai-chat",
		TargetName:                "测试账户甲",
		UpstreamModel:             "upstream-mapped-model",
		UpstreamAdapter:           "",
		Protocol:                  "openai_chat",
		UpstreamProtocol:          "openai_chat",
		EndpointMode:              "chat_json",
		UpstreamEndpointMode:      "chat_json",
	}
}

type fakeQuestionTestResolver struct {
	requests []RunRequest
	target   Target
	err      error
}

func (r *fakeQuestionTestResolver) Resolve(_ context.Context, request RunRequest) (Target, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return Target{}, r.err
	}
	return r.target, nil
}

// qbTestRoundTripper 拦截两跳请求：含 "Reference answer:" 的是诊断裁判跳，
// 其余是作答跳；不发真实网络请求。
type qbTestRoundTripper struct {
	bodies []string
}

func (t *qbTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	t.bodies = append(t.bodies, text)
	output := "qbtest-answer"
	if strings.Contains(text, "Reference answer:") {
		output = `{"verdict":"pass","questionAlignment":"aligned","keyPointResults":[{"point":"要点","met":true}],"reason":"一致"}`
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"upstream-mapped-model","choices":[{"message":{"content":` + string(encoded) + `}}],"usage":{"total_tokens":2}}`)), Request: request}, nil
}

// TestQuestionTestRunnerResolveContract 断言 Resolve 入参契约：门禁测试
// 以 actor 身份解析目标账户，不携带任何 revision fence（空 revision 由
// Resolve 的分支守卫跳过比对）。
func TestQuestionTestRunnerResolveContract(t *testing.T) {
	resolver := &fakeQuestionTestResolver{target: questionTestTarget()}
	runner := NewQuestionTestRunner(resolver.Resolve, nil)
	input := modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys-actor", AccountID: "acct-9", Model: "public-model",
		QuestionText: "题面。", ReferenceAnswer: "答案。",
	}
	if _, err := runner.TestQuestion(context.Background(), input); err != nil {
		t.Fatalf("test question: %v", err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolve 恰好一次: %d", len(resolver.requests))
	}
	request := resolver.requests[0]
	if request.SystemAccountID != "sys-actor" || request.TargetType != "account" || request.TargetID != "acct-9" || request.Model != "public-model" {
		t.Fatalf("request = %+v", request)
	}
	if request.ConfigRevision != "" || request.DispatchRevision != 0 || request.SourceConfigRevision != "" || request.SourceDispatchRevision != 0 {
		t.Fatalf("门禁测试不得携带 revision fence: %+v", request)
	}

	// Resolve error 原样返回（"无法执行"语义，由题库 HTTP 层映射 400）。
	failing := &fakeQuestionTestResolver{err: context.DeadlineExceeded}
	_, err := NewQuestionTestRunner(failing.Resolve, nil).TestQuestion(context.Background(), input)
	if err == nil {
		t.Fatal("resolve error 必须原样上抛")
	}
}

// TestQuestionTestRunnerSuiteAssembly 断言单题最小套件的组装关键字段：
// Model 用上游映射模型、RequestModel 保留公开模型、二者不等即
// ModelMappingApplied、Retry 取 quick 档、QuizQuestions 恰一题且 QuizRequested。
func TestQuestionTestRunnerSuiteAssembly(t *testing.T) {
	runner := &questionTestRunner{}
	input := modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys-actor", AccountID: "acct-9", Model: "public-model",
		QuestionText: "题面。", ReferenceAnswer: "答案。", KeyPoints: []string{"要点"},
	}
	suite := runner.suiteFor(questionTestTarget(), input)
	if suite.Model != "upstream-mapped-model" || suite.RequestModel != "public-model" || !suite.ModelMappingApplied {
		t.Fatalf("model mapping = %q/%q/%v", suite.Model, suite.RequestModel, suite.ModelMappingApplied)
	}
	if len(suite.QuizQuestions) != 1 || !suite.QuizRequested {
		t.Fatalf("quiz questions = %v requested=%v", suite.QuizQuestions, suite.QuizRequested)
	}
	question := suite.QuizQuestions[0]
	if question.Question != "题面。" || question.ReferenceAnswer != "答案。" || len(question.KeyPoints) != 1 || question.KeyPoints[0] != "要点" {
		t.Fatalf("quiz question = %+v", question)
	}
	if len(suite.Retry.AttemptTimeouts) != len(modelcheckprobe.RetryOptionsForProfile("quick").AttemptTimeouts) {
		t.Fatalf("retry 必须取 quick 档: %+v", suite.Retry)
	}
	if suite.Dispatcher != nil {
		t.Fatal("nil dispatcher 必须原样传递（probe 回退直连）")
	}
	// Capability 准入键：目标未携带 CredentialSourceAccountID 时回退
	// input.AccountID（与 J3b 运行链 runtime.go 回退链同构），熔断门
	// Prepare 对空账户 id 直接拦截。
	capability := suite.Capability
	if capability.CredentialSourceAccountID != "acct-9" || capability.KeyFingerprint != "acct-9" {
		t.Fatalf("capability 账户准入键 = %+v", capability)
	}
	if capability.ClientModel != "public-model" || capability.FinalUpstreamModel != "upstream-mapped-model" {
		t.Fatalf("capability 模型 = %+v", capability)
	}
	if capability.ClientEndpointFamily != "openai_chat" || capability.UpstreamEndpointMode != "chat_json" {
		t.Fatalf("capability 端点族/模式 = %+v", capability)
	}
	// 目标显式携带 CredentialSourceAccountID / SourceEndpointFamily 时优先。
	explicit := questionTestTarget()
	explicit.CredentialSourceAccountID = "acct-source"
	explicit.SourceEndpointFamily = "openai_responses"
	explicit.UpstreamEndpointMode = ""
	explicit.UpstreamProtocol = "openai_responses"
	explicitCapability := runner.suiteFor(explicit, input).Capability
	if explicitCapability.CredentialSourceAccountID != "acct-source" || explicitCapability.KeyFingerprint != "acct-source" {
		t.Fatalf("显式 credential source 必须优先: %+v", explicitCapability)
	}
	if explicitCapability.ClientEndpointFamily != "openai_responses" {
		t.Fatalf("端点族必须取 SourceEndpointFamily: %+v", explicitCapability)
	}
	if explicitCapability.UpstreamEndpointMode == "" {
		t.Fatalf("UpstreamEndpointMode 空缺必须按协议回退: %+v", explicitCapability)
	}
	// 相同模型（无映射）时 ModelMappingApplied 必须为 false。
	same := questionTestTarget()
	same.UpstreamModel = "public-model"
	unmapped := runner.suiteFor(same, input)
	if unmapped.ModelMappingApplied {
		t.Fatal("无映射时 ModelMappingApplied 必须为 false")
	}
}

// TestQuestionTestRunnerEndToEnd 走通 resolve → 两跳诊断 → 契约映射：
// pass 结论、账户名取自 TargetName、上游请求体使用映射后的模型。
func TestQuestionTestRunnerEndToEnd(t *testing.T) {
	transport := &qbTestRoundTripper{}
	resolver := &fakeQuestionTestResolver{target: questionTestTarget()}
	resolver.target.Client = &http.Client{Transport: transport}
	runner := NewQuestionTestRunner(resolver.Resolve, nil)
	result, err := runner.TestQuestion(context.Background(), modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys-actor", AccountID: "acct-9", Model: "public-model",
		QuestionText: "题面。", ReferenceAnswer: "答案。", KeyPoints: []string{"要点"},
	})
	if err != nil {
		t.Fatalf("test question: %v", err)
	}
	if result.Verdict != modelcheckquestionbank.TestVerdictPass || result.Alignment != modelcheckquestionbank.TestAlignmentAligned {
		t.Fatalf("verdict/alignment = %q/%q", result.Verdict, result.Alignment)
	}
	if result.AccountName != "测试账户甲" {
		t.Fatalf("accountName = %q", result.AccountName)
	}
	// 裁判 reason 必须由 runner 映射进 VerdictReason（前端诊断引导依赖），
	// 与 GraderNote 同源。
	if result.VerdictReason != "一致" || result.GraderNote != "一致" {
		t.Fatalf("verdictReason/graderNote = %q/%q", result.VerdictReason, result.GraderNote)
	}
	if len(transport.bodies) != 2 {
		t.Fatalf("两跳各请求一次: %d", len(transport.bodies))
	}
	for _, body := range transport.bodies {
		if !strings.Contains(body, `"model":"upstream-mapped-model"`) {
			t.Fatalf("上游请求必须使用映射后模型: %s", body)
		}
	}
	if !strings.Contains(transport.bodies[0], "题面。") || !strings.Contains(transport.bodies[1], "Reference answer:") {
		t.Fatalf("两跳次序错误: %v", transport.bodies)
	}

	// TargetName 为空时回退账户 id。
	anonymous := questionTestTarget()
	anonymous.TargetName = ""
	anonymous.Client = &http.Client{Transport: transport}
	fallback := NewQuestionTestRunner((&fakeQuestionTestResolver{target: anonymous}).Resolve, nil)
	result, err = fallback.TestQuestion(context.Background(), modelcheckquestionbank.TestQuestionInput{SystemAccountID: "sys", AccountID: "acct-fallback", Model: "public-model", QuestionText: "回退题面。", ReferenceAnswer: "回退答案。"})
	if err != nil {
		t.Fatalf("fallback test: %v", err)
	}
	if result.AccountName != "acct-fallback" {
		t.Fatalf("accountName fallback = %q", result.AccountName)
	}
}

// capabilityCapturingDispatcher 捕获派发边界收到的 Capability 后把请求交还
// 套件客户端执行（不发真实网络）。回归覆盖：派发器接通时套件必须携带
// Capability——账户熔断门以其 CredentialSourceAccountID 为准入键，缺失会被
// Prepare 以 "account id is required" 拦截（2026-10 题库测试全量失败根因）。
type capabilityCapturingDispatcher struct {
	transport    *qbTestRoundTripper
	capabilities []keymodelruntime.Capability
}

func (d *capabilityCapturingDispatcher) Dispatch(ctx context.Context, request *http.Request, capability keymodelruntime.Capability, attemptID string) (*http.Response, func(bool), error) {
	d.capabilities = append(d.capabilities, capability)
	response, err := (&http.Client{Transport: d.transport}).Do(request)
	return response, func(bool) {}, err
}

func (d *capabilityCapturingDispatcher) DispatchWithClient(ctx context.Context, request *http.Request, capability keymodelruntime.Capability, attemptID string, client *http.Client) (*http.Response, func(bool), error) {
	d.capabilities = append(d.capabilities, capability)
	if client == nil {
		client = &http.Client{Transport: d.transport}
	}
	response, err := client.Do(request)
	return response, func(bool) {}, err
}

// TestQuestionTestRunnerDispatchCapabilityContract 走通 resolve → 派发器
// 两跳诊断，断言每跳到达派发边界的 Capability 均携带非空账户准入键。
func TestQuestionTestRunnerDispatchCapabilityContract(t *testing.T) {
	transport := &qbTestRoundTripper{}
	target := questionTestTarget()
	target.CredentialSourceAccountID = "acct-9"
	target.Client = &http.Client{Transport: transport}
	dispatcher := &capabilityCapturingDispatcher{transport: transport}
	runner := NewQuestionTestRunner((&fakeQuestionTestResolver{target: target}).Resolve, dispatcher)
	result, err := runner.TestQuestion(context.Background(), modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys-actor", AccountID: "acct-9", Model: "public-model",
		QuestionText: "题面。", ReferenceAnswer: "答案。",
	})
	if err != nil {
		t.Fatalf("test question: %v", err)
	}
	if result.Verdict != modelcheckquestionbank.TestVerdictPass {
		t.Fatalf("verdict = %q", result.Verdict)
	}
	if len(dispatcher.capabilities) != 2 {
		t.Fatalf("两跳各派发一次: %d", len(dispatcher.capabilities))
	}
	for i, capability := range dispatcher.capabilities {
		if capability.CredentialSourceAccountID != "acct-9" || capability.KeyFingerprint != "acct-9" {
			t.Fatalf("第 %d 跳 capability 账户准入键缺失: %+v", i+1, capability)
		}
	}
}
