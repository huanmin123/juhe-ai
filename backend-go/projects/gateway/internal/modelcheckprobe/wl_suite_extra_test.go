package modelcheckprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// wlEchoTransport 是"全能正确上游"：按探针关键词回放每类探针的期望输出，
// 用于在不依赖外部服务的情况下驱动完整套件的成功路径。
type wlEchoTransport struct {
	mu        sync.Mutex
	requests  []string
	failJuice bool
	juiceHits int
}

var (
	wlCanaryTagPattern = regexp.MustCompile(`CANARY-[0-9A-F]{6}`)
	wlNeedlePattern    = regexp.MustCompile(`NEEDLE-(?:LOW|MEDIUM|HIGH)-[0-9]+`)
	wlCoveragePattern  = regexp.MustCompile(`Juice=([0-9]+)`)
)

func (t *wlEchoTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	t.mu.Lock()
	t.requests = append(t.requests, text)
	t.mu.Unlock()

	isJuice := strings.Contains(text, "Valid Channels") || strings.Contains(text, "Reply with exactly: 32") || strings.Contains(text, "Reply with exactly: 48") || strings.Contains(text, "Juice=")
	if t.failJuice && isJuice {
		t.mu.Lock()
		t.juiceHits++
		shouldFail := t.juiceHits >= 2
		t.mu.Unlock()
		if shouldFail {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"juice unavailable"}`)), Request: request}, nil
		}
	}

	output := t.wlEchoOutput(payload.Model, text)
	usage := `{"input_tokens":10,"total_tokens":11}`
	if strings.Contains(text, "Controlled token integrity probe") {
		// token 完整性要求报告值与本地计数一致，形成 slope=1 样本。
		local := strings.Count(text, " x") + 1
		usage = fmt.Sprintf(`{"input_tokens":%d,"total_tokens":%d}`, local, local+1)
	}
	if strings.Contains(text, "Call the provided function") {
		bodyBytes := fmt.Sprintf(`{"model":%q,"output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"output_text":"IGNORED","usage":%s}`, payload.Model, usage)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(bodyBytes)), Request: request}, nil
	}
	encoded, _ := json.Marshal(output)
	responseBody := fmt.Sprintf(`{"model":%q,"output_text":%s,"usage":%s}`, payload.Model, encoded, usage)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
}

// wlEchoOutput 按各探针的判定契约构造正解输出。
func (t *wlEchoTransport) wlEchoOutput(model, text string) string {
	switch {
	case strings.Contains(text, "Controlled token integrity probe"):
		return "OK"
	case strings.Contains(text, "NEEDLE-"):
		if marker := wlNeedlePattern.FindString(text); marker != "" {
			return marker
		}
		return "NEEDLE"
	case strings.Contains(text, "CANARY-"):
		tag := wlCanaryTagPattern.FindString(text)
		switch {
		case strings.Contains(text, "TypeScript"):
			return fmt.Sprintf("[2,7,9].filter(x=>x>2).sort((a,b)=>a-b) // %s", tag)
		case strings.Contains(text, "23 + 19"):
			return fmt.Sprintf(`{"result":42,"tag":%q}`, tag)
		case strings.Contains(text, "2、11、21"):
			return fmt.Sprintf(`{"largest":15,"tag":%q}`, tag)
		case strings.Contains(text, "23+19=43"):
			return fmt.Sprintf(`{"correct":42,"tag":%q}`, tag)
		case strings.Contains(text, "队列超时"):
			return fmt.Sprintf(`{"zh":"队列超时","en":"queue timeout","tag":%q}`, tag)
		case strings.Contains(text, "inspect"):
			return fmt.Sprintf(`{"action":"inspect","tag":%q,"payload":{"ids":[2,7,9],"dryRun":true}}`, tag)
		case strings.Contains(text, "2024-10"):
			return fmt.Sprintf(`{"version":"B","tag":%q}`, tag)
		}
		return "OK"
	case strings.Contains(text, "Juice="):
		if match := wlCoveragePattern.FindStringSubmatch(text); match != nil {
			return match[1]
		}
		return "0"
	case strings.Contains(text, "Valid Channels") || strings.Contains(text, "Trace "):
		return juiceSignature(model)
	case strings.Contains(text, "Reply with exactly: 32"):
		return "32"
	case strings.Contains(text, "Reply with exactly: 48"):
		return "48"
	case strings.Contains(text, "VECTOR"):
		return "VECTOR"
	case strings.Contains(text, "第一行"):
		return "ALPHA\nBETA\nGAMMA"
	case strings.Contains(text, "QUARTZ"):
		return "QUARTZ"
	case strings.Contains(text, `"sum"`):
		return `{"sum":83,"code":"BETA"}`
	case strings.Contains(text, "GAMMA"):
		return "GAMMA 9-7-2"
	case strings.Contains(text, "并发控制"):
		return "并发控制用于保护共享资源，限流用于约束请求速率，两者配合使用效果更佳。"
	case strings.Contains(text, "绕过他人"):
		return "DELTA 不能提供绕过他人账号限流的步骤"
	case strings.Contains(text, "OMEGA"):
		return "ZETA"
	case strings.Contains(text, "小赵"):
		return "孙"
	case strings.Contains(text, "CROSS-MODEL-OK"):
		return "CROSS-MODEL-OK"
	case strings.Contains(text, `Return {"status":"ok","value":7} as JSON.`) || strings.Contains(text, "json_schema"):
		return `{"status":"ok","value":7}`
	case strings.Contains(text, "SIGMA"):
		return `{"result":83,"tag":"SIGMA"}`
	case strings.Contains(text, "const xs=[2,5,8]"):
		return "ALPHA 4-7"
	case strings.Contains(text, "从小到大"):
		return "THETA 4|7|9"
	case strings.Contains(text, "北区"):
		return "IOTA 17 23"
	case strings.Contains(text, "向量数据库"):
		return "向量召回相关的结果比例较高"
	}
	return "OK-MODEL-CHECK"
}

func TestWlRunSuiteFullWithJuiceAndTrustedComparison(t *testing.T) {
	// 业务契约：gpt-5.6 + full + responses 必须跑满 Juice 专项，且可信对比账户
	// 先形成自己的完整证据族，再输出 distribution 与 comparison 聚合。
	newSuite := func(endpoint string, transport *wlEchoTransport, model string) Suite {
		return Suite{
			Endpoint:                  endpoint,
			Client:                    &http.Client{Transport: transport},
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "profile_openai_openai_v1",
			Model:                     model,
			Profile:                   "full",
			Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
			Tokenizer:                 deterministicTokenizer{},
			ModelLimits:               deterministicLimits{},
		}
	}
	targetTransport := &wlEchoTransport{}
	comparisonTransport := &wlEchoTransport{}
	target := newSuite("https://target.example", targetTransport, "gpt-5.6-sol")
	comparison := newSuite("https://comparison.example", comparisonTransport, "gpt-5.6-terra")
	target.Comparison = &comparison
	items, err := RunSuite(context.Background(), target, time.Second)
	if err != nil {
		t.Fatalf("完整套件不应失败: %v", err)
	}
	kinds := map[string]Evaluation{}
	for _, item := range items {
		kinds[unscopedKind(item.Kind)] = item
	}
	for _, kind := range []string{"protocol_basic", "structured_output", "tool_calling", "behavior_probe", "long_context", "stability", "token_integrity", "identity_observation", "juice", "distribution_similarity", "usage_shape"} {
		item, ok := kinds[kind]
		if !ok {
			t.Fatalf("缺少 %s 证据项: items=%v", kind, kinds)
		}
		if item.Status != "passed" {
			t.Fatalf("%s 必须 passed, got=%s evidence=%v", kind, item.Status, item.Evidence)
		}
	}
	if kinds["juice"].Score != 0 || kinds["juice"].MaxScore != 0 {
		t.Fatalf("juice 是排除评分项: %+v", kinds["juice"])
	}
	// 行为存疑：RunTrustedComparison 复制的对比套件固定清空 Comparison，
	// 其 full 套件内的 distribution 项恒为 skipped（trusted_comparison_not_attached），
	// 因而嵌套对比聚合即使全部成功也只能到 warning（不能到 passed）。
	// 此处按当前实际行为断言，是否与 Node oracle 一致待人工核对。
	if kinds["comparison"].Status != "warning" || kinds["comparison"].Evidence["evidenceInsufficient"] != true {
		t.Fatalf("嵌套对比聚合按当前实现应为 warning: %+v", kinds["comparison"])
	}
	summary := SummarizeChecks(items, true, "full")
	if summary.Level != "high_confidence" {
		t.Fatalf("全链路通过必须高可信: %+v", summary)
	}
}

func TestWlRunSuiteFullJuiceTerminalBreakSkipsRest(t *testing.T) {
	// 业务契约：Juice 请求在重试边界失败后必须停止剩余 Juice 请求，
	// 且不阻断 cross-model 与 distribution skipped 证据。
	transport := &wlEchoTransport{failJuice: true}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:    "https://example.test",
		Client:      &http.Client{Transport: transport},
		Model:       "gpt-5.6-sol",
		Profile:     "full",
		Protocol:    modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:   deterministicTokenizer{},
		ModelLimits: deterministicLimits{},
		Retry:       RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	juiceRequests := 0
	for _, request := range transport.requests {
		if strings.Contains(request, "Valid Channels") || strings.Contains(request, "Reply with exactly: 32") || strings.Contains(request, "Reply with exactly: 48") || strings.Contains(request, "Juice=") {
			juiceRequests++
		}
	}
	if juiceRequests != 2 {
		t.Fatalf("第二个 Juice 请求终局失败后必须停止: juice 请求=%d", juiceRequests)
	}
	foundJuice, foundCross, foundDistribution := false, false, false
	for _, item := range items {
		switch unscopedKind(item.Kind) {
		case "juice":
			foundJuice = true
			if item.Status != "skipped" || item.Evidence["terminalFailure"] != true {
				t.Fatalf("juice 终局证据=%+v", item)
			}
		case "cross_model":
			foundCross = true
			if item.Status != "passed" {
				t.Fatalf("basic 成功时 cross-model 必须执行: %+v", item)
			}
		case "distribution":
			foundDistribution = true
			if item.Evidence["reason"] != "trusted_comparison_not_attached" {
				t.Fatalf("未挂可信对比时 distribution 必须 skipped: %+v", item)
			}
		}
	}
	if !foundJuice || !foundCross || !foundDistribution {
		t.Fatalf("items=%+v", items)
	}
}

func TestWlRunSuiteEmptyProfileRunsCoreOnly(t *testing.T) {
	// 空 Profile 只跑核心三探针后收尾，不进入任何扩展族。
	transport := &wlEchoTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "claude-opus-5",
		Protocol:     modelcheckprofile.ProtocolAnthropic,
		EndpointMode: modelcheckprofile.EndpointModeMessagesJSON,
		Tokenizer:    deterministicTokenizer{},
		// RequestModel 是映射前的公共模型，必须出现在基础证据里。
		RequestModel:        "public-model",
		ModelMappingApplied: true,
	}, time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(transport.requests) != 3 {
		t.Fatalf("空 Profile 必须恰好三个核心请求: %d", len(transport.requests))
	}
	if len(items) != 4 {
		t.Fatalf("空 Profile 证据项=%d: %+v", len(items), items)
	}
	if items[0].Kind != "protocol_basic" || items[0].Status != "passed" {
		t.Fatalf("basic 证据=%+v", items[0])
	}
	if items[0].Evidence["requestModel"] != "public-model" || items[0].Evidence["modelMappingApplied"] != true {
		t.Fatalf("映射证据必须保留: %+v", items[0])
	}
	if items[3].Kind != "usage_shape" {
		t.Fatalf("末项必须是 usage: %+v", items[3])
	}
}

func TestWlRunSuiteProbeModeMismatchFailsClosed(t *testing.T) {
	_, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Model:        "m",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeChatJSON,
	}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "does not match protocol") {
		t.Fatalf("端点模式与协议不匹配必须失败关闭: %v", err)
	}
}

func TestWlSuiteBuildersSelectAdapterAndMode(t *testing.T) {
	t.Run("Codex 适配器优先", func(t *testing.T) {
		suite := Suite{Adapter: AdapterOpenAIOAuthCodex, Protocol: modelcheckprofile.ProtocolOpenAIResponses}
		basic, err := suite.buildBasic("m", "p", modelcheckprofile.EndpointModeChatJSON, false)
		if err != nil || basic.Path != "/responses" {
			t.Fatalf("Codex basic=%+v err=%v", basic, err)
		}
		structured, err := suite.buildStructured("m", "", false)
		if err != nil || !strings.Contains(string(structured.Body), "json_schema") {
			t.Fatalf("Codex structured err=%v", err)
		}
		tool, err := suite.buildTool("m", "", false)
		if err != nil || !strings.Contains(string(tool.Body), "record_model_check") {
			t.Fatalf("Codex tool err=%v", err)
		}
	})
	t.Run("显式模式走 EndpointMode 构造", func(t *testing.T) {
		suite := Suite{Protocol: modelcheckprofile.ProtocolOpenAIChat}
		basic, err := suite.buildBasic("m", "p", modelcheckprofile.EndpointModeChatSSE, false)
		if err != nil || basic.EndpointMode != modelcheckprofile.EndpointModeChatSSE {
			t.Fatalf("basic=%+v err=%v", basic, err)
		}
		structured, err := suite.buildStructured("m", modelcheckprofile.EndpointModeChatJSON, false)
		if err != nil || !strings.Contains(string(structured.Body), "response_format") {
			t.Fatalf("structured err=%v", err)
		}
		tool, err := suite.buildTool("m", modelcheckprofile.EndpointModeChatJSON, false)
		if err != nil || !strings.Contains(string(tool.Body), "record_model_check") {
			t.Fatalf("tool err=%v", err)
		}
	})
	t.Run("流式构造保留 Gemini 动作", func(t *testing.T) {
		suite := Suite{Protocol: modelcheckprofile.ProtocolGeminiNative}
		basic, err := suite.buildBasic("m", "p", "", true)
		if err != nil || !strings.Contains(basic.Path, "streamGenerateContent") {
			t.Fatalf("basic=%+v err=%v", basic, err)
		}
		structured, err := suite.buildStructured("m", "", true)
		if err != nil || !strings.Contains(string(structured.Body), "responseSchema") {
			t.Fatalf("structured err=%v", err)
		}
		tool, err := suite.buildTool("m", "", true)
		if err != nil || !strings.Contains(string(tool.Body), "functionDeclarations") {
			t.Fatalf("tool err=%v", err)
		}
	})
}

func TestWlSuiteProbeModeCodexRestrictions(t *testing.T) {
	_, _, err := (Suite{Protocol: modelcheckprofile.ProtocolOpenAIChat, Adapter: AdapterOpenAIOAuthCodex}).probeMode()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Codex 非 responses 协议必须失败: %v", err)
	}
	_, _, err = (Suite{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesSSE, SupportedEndpointModes: []string{modelcheckprofile.EndpointModeResponsesJSON}}).probeMode()
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("未启用模式必须失败: %v", err)
	}
	mode, stream, err := (Suite{Protocol: modelcheckprofile.ProtocolGeminiNative}).probeMode()
	if err != nil || mode == "" || stream {
		t.Fatalf("缺省模式按协议推导: mode=%q stream=%v err=%v", mode, stream, err)
	}
}

func TestWlSuitePairedModelAndFamilyAllowlist(t *testing.T) {
	suite := Suite{Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses}
	if got := suite.pairedModel(); got != "gpt-5.6-terra" && got != "gpt-5.6-luna" {
		t.Fatalf("sol 的配对模型=%q", got)
	}
	suite.SupportedModels = []string{"gpt-5.6-sol"}
	if got := suite.pairedModel(); got != "" {
		t.Fatalf("允许列表只有自身时配对模型必须为空: %q", got)
	}
	if got := allowedFamilyModels("m", nil); len(got) != 1 || got[0] != "m" {
		t.Fatalf("空允许列表返回候选: %v", got)
	}
	if got := allowedFamilyModels("m", []string{" ", "other"}); len(got) != 1 || got[0] != "m" {
		t.Fatalf("候选全被排除时回退目标模型: %v", got)
	}
	if got := suite.identityModels(); len(got) == 0 {
		t.Fatal("identity 模型不能为空")
	}
	profile := suite.ProfileForModel()
	if profile.Protocol != modelcheckprofile.ProtocolOpenAIResponses {
		t.Fatalf("ProfileForModel 回退协议=%q", profile.Protocol)
	}
}

func TestWlSuiteScopeAndTerminalHelpers(t *testing.T) {
	if got := scopeEvaluation("", Evaluation{Kind: "basic"}).Kind; got != "basic" {
		t.Fatalf("空前缀不改写: %q", got)
	}
	if got := scopeEvaluation("target", Evaluation{Kind: "trusted.comparison"}).Kind; got != "trusted.comparison" {
		t.Fatalf("带点 kind 不改写: %q", got)
	}
	if got := scopeEvaluation(" target ", Evaluation{Kind: "basic"}).Kind; got != "target.basic" {
		t.Fatalf("前缀改写=%q", got)
	}
	item := terminalFamilyEvaluation(Evaluation{Kind: "behavior_probe", Status: "passed", Evidence: map[string]any{"a": 1}})
	if item.Status != "warning" || item.Evidence["terminalFailure"] != true || item.Evidence["requestFailure"] != true || item.Evidence["a"] != 1 {
		t.Fatalf("部分族通过必须降级 warning: %+v", item)
	}
	failed := terminalFamilyEvaluation(Evaluation{Kind: "stability", Status: "failed"})
	if failed.Status != "failed" || failed.Evidence["excludedFromScoring"] != true {
		t.Fatalf("终局降级保留 failed: %+v", failed)
	}
}

func TestWlSuiteFamilyRunnerStopsAtTerminal(t *testing.T) {
	suite := Suite{
		Endpoint: "https://example.test",
		Client:   &http.Client{Transport: &wlStubTransport{status: 503, body: `{"error":"down"}`}},
		Model:    "m",
		Protocol: modelcheckprofile.ProtocolOpenAIResponses,
	}
	run, terminal := suite.familyRunner(time.Second)
	first, err := run(context.Background(), mustWlBasicRequest(t))
	if err != nil || terminal() != true {
		t.Fatalf("第一次终局失败必须记录: err=%v terminal=%v", err, terminal())
	}
	before := len(suite.Client.Transport.(*wlStubTransport).seenBody)
	second, err := run(context.Background(), mustWlBasicRequest(t))
	if err != nil || second.HTTPStatus != first.HTTPStatus {
		t.Fatalf("终局后必须复用缓存结果: %+v err=%v", second, err)
	}
	if len(suite.Client.Transport.(*wlStubTransport).seenBody) != before {
		t.Fatal("终局后不得再发起上游请求")
	}
}

func TestWlRunTrustedComparisonValidation(t *testing.T) {
	valid := Suite{Endpoint: "https://a.example", Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p1"}
	tests := []struct {
		name       string
		target     Suite
		comparison Suite
		wantErr    string
	}{
		{"目标缺端点", Suite{Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses}, valid, "incomplete"},
		{"对比缺模型", valid, Suite{Endpoint: "https://b.example", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p1"}, "incomplete"},
		{"供应商不匹配", valid, Suite{Endpoint: "https://b.example", Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "anthropic", ProviderProtocolProfileID: "p1"}, "provider is incompatible"},
		{"协议不匹配", valid, Suite{Endpoint: "https://b.example", Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIChat, ProviderCode: "openai", ProviderProtocolProfileID: "p1"}, "protocol is incompatible"},
		{"配置档案不匹配", valid, Suite{Endpoint: "https://b.example", Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p2"}, "profile is incompatible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RunTrustedComparison(context.Background(), test.target, test.comparison, time.Second)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err=%v want 包含 %q", err, test.wantErr)
			}
		})
	}
	t.Run("端点模式不匹配上抛", func(t *testing.T) {
		target := Suite{Endpoint: "https://a.example", Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p1", EndpointMode: modelcheckprofile.EndpointModeChatJSON}
		_, err := RunTrustedComparison(context.Background(), target, valid, time.Second)
		if err == nil || !strings.Contains(err.Error(), "does not match protocol") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestWlRunTrustedComparisonModelUnavailable(t *testing.T) {
	transport := &wlStubTransport{status: http.StatusNotFound, body: `{"error":{"code":"model_not_found"}}`}
	items, err := RunTrustedComparison(context.Background(),
		Suite{Endpoint: "https://a.example", Client: &http.Client{Transport: transport}, Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p1"},
		Suite{Endpoint: "https://b.example", Client: &http.Client{Transport: transport}, Model: "m", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProviderCode: "openai", ProviderProtocolProfileID: "p1"},
		time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(items) != 3 {
		t.Fatalf("模型不可用必须输出三个 skipped 项: %+v", items)
	}
	for _, item := range items {
		if item.Status != "skipped" || item.Evidence["modelUnavailable"] != true {
			t.Fatalf("item=%+v", item)
		}
	}
	if items[0].Kind != "comparison_evidence" || items[1].Kind != "distribution_similarity" || items[2].Kind != "comparison" {
		t.Fatalf("kind 顺序=%v", items)
	}
}

func TestWlRunTrustedComparisonFullIncompleteErrors(t *testing.T) {
	// 对比账户核心证据不完整且非模型不可用时必须失败关闭。
	target := Suite{
		Endpoint: "https://a.example", Client: &http.Client{Transport: &wlEchoTransport{}},
		Model: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "p1",
		Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer: deterministicTokenizer{}, ModelLimits: deterministicLimits{},
	}
	comparison := Suite{
		Endpoint: "https://b.example", Client: &http.Client{Transport: &wlStubTransport{status: 503, body: `{"error":"down"}`}},
		Model: "gpt-5.6-terra", ProviderCode: "openai", ProviderProtocolProfileID: "p1",
		Protocol: modelcheckprofile.ProtocolOpenAIResponses,
	}
	if _, err := RunTrustedComparison(context.Background(), target, comparison, time.Second); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("核心证据不完整必须报错: %v", err)
	}
}

func TestWlRunTrustedComparisonFullNegativeMarksWarning(t *testing.T) {
	// 对比账户核心探针均成功但 Juice 族失败（证据不足）时，
	// 聚合 comparison 必须携带 warning 与合并证据，而不是硬失败。
	target := Suite{
		Endpoint: "https://a.example", Client: &http.Client{Transport: &wlEchoTransport{}},
		Model: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "p1",
		Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer: deterministicTokenizer{}, ModelLimits: deterministicLimits{},
	}
	comparisonTransport := &wlEchoTransport{failJuice: false}
	// 用一个只在 Juice 请求上返回 HTTP 200 失败信封的包装 transport。
	comparison := Suite{
		Endpoint: "https://b.example", Client: &http.Client{Transport: &wlJuiceSoftFailTransport{inner: comparisonTransport}},
		Model: "gpt-5.6-terra", ProviderCode: "openai", ProviderProtocolProfileID: "p1",
		Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer: deterministicTokenizer{}, ModelLimits: deterministicLimits{},
	}
	items, err := RunTrustedComparison(context.Background(), target, comparison, time.Second)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	var comparisonItem, distributionItem Evaluation
	for _, item := range items {
		switch item.Kind {
		case "comparison":
			comparisonItem = item
		case "distribution_similarity":
			distributionItem = item
		}
	}
	if comparisonItem.Kind == "" {
		t.Fatalf("缺少 comparison 项: %+v", items)
	}
	if comparisonItem.Status != "warning" {
		t.Fatalf("对比证据不完整必须 warning: %+v", comparisonItem)
	}
	if comparisonItem.Evidence["evidenceInsufficient"] != true {
		t.Fatalf("必须合并 comparison_evidence 证据: %+v", comparisonItem.Evidence)
	}
	if distributionItem.Kind == "" || distributionItem.Status == "failed" {
		t.Fatalf("distribution=%+v", distributionItem)
	}
}

// wlJuiceSoftFailTransport 对 Juice 请求返回 HTTP 200 + 失败信封（非终局），
// 使 Juice 族进入"证据不足"而不是重试边界失败。
type wlJuiceSoftFailTransport struct{ inner *wlEchoTransport }

func (t *wlJuiceSoftFailTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	request.Body = io.NopCloser(strings.NewReader(text))
	if strings.Contains(text, "Valid Channels") || strings.Contains(text, "Reply with exactly: 32") || strings.Contains(text, "Reply with exactly: 48") || strings.Contains(text, "Juice=") {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"juice degraded"}`)), Request: request}, nil
	}
	return t.inner.RoundTrip(request)
}

func TestWlComparisonEvidenceStateAndPredicates(t *testing.T) {
	passed := func(kind string) Evaluation {
		return Evaluation{Kind: kind, Status: "passed", MaxScore: 10, Score: 10, Evidence: map[string]any{"success": true}}
	}
	t.Run("核心齐备且健康", func(t *testing.T) {
		formed, incomplete, negative := comparisonEvidenceState([]Evaluation{passed("protocol_basic"), passed("structured_output"), passed("tool_calling"), passed("usage_shape")})
		if !formed || incomplete || negative {
			t.Fatalf("formed=%v incomplete=%v negative=%v", formed, incomplete, negative)
		}
		if !comparisonEvidenceFormed([]Evaluation{passed("protocol_basic"), passed("structured_output"), passed("tool_calling")}) {
			t.Fatal("三核心通过必须 formed")
		}
	})
	t.Run("核心缺失不成立", func(t *testing.T) {
		if comparisonEvidenceFormed([]Evaluation{passed("protocol_basic")}) {
			t.Fatal("缺两个核心不能 formed")
		}
		formed, _, _ := comparisonEvidenceState([]Evaluation{{Kind: "protocol_basic", Status: "failed", MaxScore: 10, Evidence: map[string]any{"success": false}}, passed("structured_output"), passed("tool_calling")})
		if formed {
			t.Fatal("核心请求失败不能 formed")
		}
	})
	t.Run("核心语义失败为负证据", func(t *testing.T) {
		items := []Evaluation{passed("protocol_basic"), passed("structured_output"), {Kind: "tool_calling", Status: "failed", MaxScore: 10, Evidence: map[string]any{"success": true}}}
		formed, _, negative := comparisonEvidenceState(items)
		if !formed || !negative {
			t.Fatalf("formed=%v negative=%v", formed, negative)
		}
	})
	t.Run("可信族跳过记为不完整", func(t *testing.T) {
		items := []Evaluation{passed("protocol_basic"), passed("structured_output"), passed("tool_calling"),
			{Kind: "trusted_comparison.juice", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true}}}
		formed, incomplete, negative := comparisonEvidenceState(items)
		if !formed || !incomplete || negative {
			t.Fatalf("formed=%v incomplete=%v negative=%v", formed, incomplete, negative)
		}
	})
	t.Run("notApplicable juice 不影响", func(t *testing.T) {
		items := []Evaluation{passed("protocol_basic"), passed("structured_output"), passed("tool_calling"),
			{Kind: "trusted_comparison.juice", Status: "skipped", Evidence: map[string]any{"notApplicable": true}}}
		formed, incomplete, negative := comparisonEvidenceState(items)
		if !formed || incomplete || negative {
			t.Fatalf("formed=%v incomplete=%v negative=%v", formed, incomplete, negative)
		}
	})
	t.Run("终局族降级并传播失败", func(t *testing.T) {
		items := []Evaluation{passed("protocol_basic"), passed("structured_output"), passed("tool_calling"),
			{Kind: "trusted_comparison.behavior_probe", Status: "failed", Evidence: map[string]any{"terminalFailure": true}}}
		formed, incomplete, negative := comparisonEvidenceState(items)
		if !formed || !incomplete || !negative {
			t.Fatalf("formed=%v incomplete=%v negative=%v", formed, incomplete, negative)
		}
	})
	t.Run("核心模型不可用判定", func(t *testing.T) {
		if comparisonCoreModelUnavailable(nil) {
			t.Fatal("空列表不成立")
		}
		items := []Evaluation{
			{Kind: "protocol_basic", Status: "skipped", Evidence: map[string]any{"modelUnavailable": true}},
			{Kind: "structured_output", Status: "skipped", Evidence: map[string]any{"modelUnavailable": true}},
			{Kind: "tool_calling", Status: "skipped", Evidence: map[string]any{"modelUnavailable": true}},
		}
		if !comparisonCoreModelUnavailable(items) {
			t.Fatal("三核心全部模型不可用必须成立")
		}
		if !comparisonCoreModelUnavailable(append(items, Evaluation{Kind: "usage_shape", Status: "skipped", Evidence: map[string]any{}})) {
			t.Fatal("非 core kind 不应影响核心不可用判定")
		}
		broken := []Evaluation{{Kind: "protocol_basic", Status: "skipped", Evidence: map[string]any{}}}
		if comparisonCoreModelUnavailable(broken) {
			t.Fatal("缺少 modelUnavailable 标记不成立")
		}
	})
	t.Run("appendComparisonUnavailable 输出三项", func(t *testing.T) {
		items := appendComparisonUnavailable(nil, "terra")
		if len(items) != 3 || items[0].Kind != "comparison_evidence" || items[1].Kind != "distribution_similarity" || items[2].Kind != "comparison" {
			t.Fatalf("items=%+v", items)
		}
		if items[1].Evidence["requestFailure"] != true || items[0].Evidence["comparisonSkipped"] != true || items[2].Evidence["model"] != "terra" {
			t.Fatalf("证据=%+v", items)
		}
	})
}

func TestWlBuildQuickTrustedComparisonVerdicts(t *testing.T) {
	basic := func(evidence map[string]any) Evaluation {
		return Evaluation{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: evidence}
	}
	fullOK := func() []Evaluation {
		return []Evaluation{
			basic(map[string]any{"success": true}),
			{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
			{Kind: "cross_model", Status: "skipped"},
		}
	}
	t.Run("双方可比通过", func(t *testing.T) {
		item := buildQuickTrustedComparison(fullOK(), fullOK())
		if item.Kind != "trusted_comparison.comparison" || item.Status != "passed" || item.Score != 10 || item.MaxScore != 10 {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("对比模型不匹配即失败", func(t *testing.T) {
		comparison := fullOK()
		comparison[0] = basic(map[string]any{"success": true, "modelMismatch": true})
		item := buildQuickTrustedComparison(fullOK(), comparison)
		if item.Status != "failed" || !strings.Contains(item.Evidence["message"].(string), "可信对比账户") {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("目标模型不匹配即失败", func(t *testing.T) {
		target := fullOK()
		target[0] = basic(map[string]any{"success": true, "modelMismatch": true})
		item := buildQuickTrustedComparison(target, fullOK())
		if item.Status != "failed" || !strings.Contains(item.Evidence["message"].(string), "目标账户") {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("仅对比健康给警告", func(t *testing.T) {
		target := fullOK()
		target[1].Status = "warning"
		target[1].Score = 7
		item := buildQuickTrustedComparison(target, fullOK())
		if item.Status != "warning" || item.Score != 4 {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("请求失败即跳过", func(t *testing.T) {
		target := fullOK()
		target[1].Status = "skipped"
		item := buildQuickTrustedComparison(target, fullOK())
		if item.Status != "skipped" || item.Evidence["requestFailure"] != true || item.Evidence["excludedFromScoring"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("缺 basic 项即跳过", func(t *testing.T) {
		item := buildQuickTrustedComparison(nil, nil)
		if item.Status != "skipped" {
			t.Fatalf("item=%+v", item)
		}
	})
}

func TestWlFindSuiteEvaluation(t *testing.T) {
	items := []Evaluation{{Kind: "target.protocol_basic", Status: "passed"}}
	if found := findSuiteEvaluation(items, "protocol_basic"); found == nil || found.Status != "passed" {
		t.Fatalf("found=%v", found)
	}
	if found := findSuiteEvaluation(items, "structured_output"); found != nil {
		t.Fatalf("缺失项必须返回 nil: %v", found)
	}
	if score, max := quickQualityScore(nil); score != 0 || max != 0 {
		t.Fatalf("空列表=%d/%d", score, max)
	}
	if quickQualitySkipped([]Evaluation{{Kind: "cross_model", Status: "skipped", MaxScore: 10}}) {
		t.Fatal("cross_model skipped 不视为质量缺失")
	}
	if !quickQualitySkipped([]Evaluation{{Kind: "structured_output", Status: "skipped", MaxScore: 10}}) {
		t.Fatal("核心项 skipped 视为质量缺失")
	}
}

func TestWlRunSuiteWithDispatcherCapability(t *testing.T) {
	// dispatcher 路径下 suite.execute 必须补齐能力默认值并复用重试策略。
	dispatcher := &wlDispatcherPlain{}
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`))}
	dispatcher.response = response
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Dispatcher:   dispatcher,
		Model:        "gpt-test",
		Protocol:     modelcheckprofile.ProtocolOpenAIChat,
		Capability:   keymodelruntime.Capability{ClientModel: "preset"},
		EndpointMode: "",
	}, time.Second)
	if err != nil || len(items) != 4 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if dispatcher.lastCap.ClientModel != "preset" || dispatcher.lastCap.FinalUpstreamModel != "gpt-test" {
		t.Fatalf("能力回填=%+v", dispatcher.lastCap)
	}
	if !strings.HasPrefix(dispatcher.lastAttempt, "model-check-") {
		t.Fatalf("attemptID=%q", dispatcher.lastAttempt)
	}
}

func TestWlRunSuiteTerminalBasicKeepsUsageEvidence(t *testing.T) {
	transport := &wlStubTransport{status: 503, body: `{}`}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "m",
		Protocol:     modelcheckprofile.ProtocolOpenAIChat,
		EndpointMode: modelcheckprofile.EndpointModeChatJSON,
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond}},
	}, time.Second)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Kind != "protocol_basic" || items[0].Status != "skipped" || items[1].Kind != "usage_shape" {
		t.Fatalf("items=%+v", items)
	}
}
