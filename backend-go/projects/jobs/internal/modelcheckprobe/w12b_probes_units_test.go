package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func jsonUnmarshal(raw []byte, target any) error { return json.Unmarshal(raw, target) }

func timeAfter() <-chan time.Time { return time.After(300 * time.Millisecond) }

// w12b_probes_units_test.go 以注入 RunProbe 的方式覆盖四类探针编排
// （identity/stability/longcontext/behavior）与 token 诚信差分、response
// 解析、transport URL/客户端分支。ProbeResult 全部构造，无外部依赖。

func w12bProbeResult(request Request, output, model string) ProbeResult {
	return ProbeResult{HTTPStatusCode: 200, Success: true, RequestModel: request.ExpectedModel,
		ExpectedModel: request.ExpectedModel, TraceID: "w12b-trace",
		Response: ParsedResponse{Model: model, OutputText: output, Usage: map[string]any{"input_tokens": float64(3)}}}
}

// w12bIdentityRunProbe 按提示词特征返回满足约束的输出；failKeys 中的
// canary key 返回不满足约束的输出。
func w12bIdentityRunProbe(failKeys map[string]bool) func(context.Context, Request) (ProbeResult, error) {
	return func(_ context.Context, request Request) (ProbeResult, error) {
		var payload map[string]any
		_ = jsonUnmarshal(request.Body, &payload)
		prompt := w12bResponsesPrompt(payload)
		key := w12bCanaryKey(prompt)
		output := w12bCanaryOutput(key, prompt)
		if failKeys[key] {
			output = "不符合约束的输出"
		}
		return ProbeResult{HTTPStatusCode: 200, Success: true, RequestModel: request.ExpectedModel,
			ExpectedModel: request.ExpectedModel, TraceID: "w12b-identity",
			Response: ParsedResponse{Model: request.ExpectedModel, OutputText: output, Usage: map[string]any{"output_tokens": float64(30)}}}, nil
	}
}

func w12bCanaryKey(prompt string) string {
	switch {
	case strings.Contains(prompt, "23 + 19"):
		return "constraint_json"
	case strings.Contains(prompt, "过滤为大于 2 的值"):
		return "code_patch"
	case strings.Contains(prompt, "第二大值加 4"):
		return "reasoning_order"
	case strings.Contains(prompt, "23+19=43"):
		return "error_recovery"
	case strings.Contains(prompt, "队列超时"):
		return "multilingual_consistency"
	case strings.Contains(prompt, "dryRun=true"):
		return "tool_schema"
	case strings.Contains(prompt, "知识截止 2024-10"):
		return "knowledge_window"
	default:
		return "unknown"
	}
}

func w12bCanaryOutput(key, prompt string) string {
	tag := ""
	if start := strings.Index(prompt, "CANARY-"); start >= 0 {
		tag = prompt[start:]
		if end := strings.IndexAny(tag, "。\"，"); end > 0 {
			tag = tag[:end]
		}
		if end := strings.Index(tag, "」"); end > 0 {
			tag = tag[:end]
		}
	}
	switch key {
	case "constraint_json":
		return `{"result":42,"tag":"` + tag + `"}`
	case "code_patch":
		return `[3,7,9].filter(n => n > 2).sort((a,b)=>a-b) // ` + tag
	case "reasoning_order":
		return `{"largest":15,"tag":"` + tag + `"}`
	case "error_recovery":
		return `{"correct":42,"tag":"` + tag + `"}`
	case "multilingual_consistency":
		return `{"zh":"队列超时","en":"queue timeout","tag":"` + tag + `"}`
	case "tool_schema":
		return `{"action":"inspect","tag":"` + tag + `","payload":{"ids":[2,7,9],"dryRun":true}}`
	case "knowledge_window":
		return `{"version":"B","tag":"` + tag + `"}`
	default:
		return "OK-MODEL-CHECK"
	}
}

func TestW12bPairedIdentityModels(t *testing.T) {
	if got := PairedIdentityModels("gpt-5.6-sol"); len(got) != 3 {
		t.Fatalf("gpt-5.6 家族应配 3 模型: %v", got)
	}
	if got := PairedIdentityModels("gpt-5.5"); len(got) != 2 {
		t.Fatalf("gpt-5.5 应配 2 模型: %v", got)
	}
	if got := PairedIdentityModels("w12b-other"); len(got) != 1 {
		t.Fatalf("未知模型应单例: %v", got)
	}
}

func TestW12bRunIdentityObservationArms(t *testing.T) {
	ctx := context.Background()
	// 非法输入。
	if _, _, err := RunIdentityObservation(ctx, IdentityProbeInput{}); err == nil {
		t.Fatal("缺 RunProbe 应报错")
	}
	// RunProbe 错误。
	if _, _, err := RunIdentityObservation(ctx, IdentityProbeInput{Model: "m1", RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, errors.New("w12b 注入失败")
	}}); err == nil {
		t.Fatal("RunProbe 错误应返回")
	}
	// 全部通过（含多模型配对）→ passed 10，target 计数分离。
	item, observations, err := RunIdentityObservation(ctx, IdentityProbeInput{
		Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prefix: "target", RunProbe: w12bIdentityRunProbe(nil),
	})
	if err != nil || item.Status != "passed" || item.Score != 10 {
		t.Fatalf("全通过身份项不符: %+v %v", item, err)
	}
	if len(observations) != 21 || item.Evidence["targetSuccessCount"] != 7 || item.Evidence["modelCount"] != 3 {
		t.Fatalf("配对观察数不符: %d %#v", len(observations), item.Evidence)
	}
	// 部分失败 → warning。
	item, _, err = RunIdentityObservation(ctx, IdentityProbeInput{
		Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: w12bIdentityRunProbe(map[string]bool{"code_patch": true, "knowledge_window": true}),
	})
	if err != nil || item.Status != "warning" {
		t.Fatalf("部分失败应 warning: %+v %v", item, err)
	}
	// 全部失败 → skipped/requestFailure。
	item, _, err = RunIdentityObservation(ctx, IdentityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return ProbeResult{HTTPStatusCode: 502, ExpectedModel: request.ExpectedModel, Response: ParsedResponse{ErrorMessage: "上游故障"}}, nil
	}})
	if err != nil || item.Status != "skipped" || item.Evidence["requestFailure"] != true {
		t.Fatalf("全部失败应 skipped: %+v %v", item, err)
	}
	// 首个观察即终态：提前收口。
	item, observations, err = RunIdentityObservation(ctx, IdentityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return ProbeResult{HTTPStatusCode: 502, ExpectedModel: request.ExpectedModel, RetryMaxAttempts: 1, Response: ParsedResponse{ErrorMessage: "终态"}}, nil
	}})
	if err != nil || !isTerminalProbeItem(item) || len(observations) != 1 {
		t.Fatalf("终态应提前收口: %+v %d %v", item, len(observations), err)
	}
	// identityFeatureVector 回退臂（无 usage → 按输出长度）。
	vector := identityFeatureVector("constraint", "输出内容", nil, true)
	if vector[0] != 1 || vector[7] == 0 {
		t.Fatalf("特征向量回退不符: %v", vector)
	}
}

func TestW12bStabilityArms(t *testing.T) {
	ctx := context.Background()
	if _, err := RunStabilityProbeSet(ctx, StabilityProbeInput{}); err == nil {
		t.Fatal("缺 RunProbe 应报错")
	}
	if _, err := RunStabilityProbeSet(ctx, StabilityProbeInput{Model: "m1", RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, errors.New("w12b 注入失败")
	}}); err == nil {
		t.Fatal("RunProbe 错误应返回")
	}
	// 三轮全过 → passed；RunProbe 统计调用次数应为 3。
	calls := 0
	item, err := RunStabilityProbeSet(ctx, StabilityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prefix: "target", RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		calls++
		return w12bProbeResult(request, "VECTOR", "m1"), nil
	}})
	_ = item
	if err != nil || calls != 3 {
		t.Fatalf("三轮稳定性调用数不符: %d %v", calls, err)
	}
	// 第二轮终态 → 只调用 2 次且部分证据 → warning。
	calls = 0
	item, err = RunStabilityProbeSet(ctx, StabilityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		calls++
		if calls == 2 {
			return ProbeResult{HTTPStatusCode: 502, ExpectedModel: request.ExpectedModel, RetryMaxAttempts: 1, Response: ParsedResponse{ErrorMessage: "终态"}}, nil
		}
		return w12bProbeResult(request, "VECTOR", "m1"), nil
	}})
	if err != nil || item.Status != "warning" || calls != 2 {
		t.Fatalf("部分终态应 warning: %+v calls=%d err=%v", item, calls, err)
	}
	// 全部失败 → skipped。
	item, err = RunStabilityProbeSet(ctx, StabilityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return ProbeResult{HTTPStatusCode: 500, ExpectedModel: request.ExpectedModel}, nil
	}})
	if err != nil || item.Status != "skipped" {
		t.Fatalf("全失败应 skipped: %+v %v", item, err)
	}
	// 模型不匹配 → failed + 不一致文案。
	item, err = RunStabilityProbeSet(ctx, StabilityProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return w12bProbeResult(request, "VECTOR", "其他模型"), nil
	}})
	if err != nil || item.Status != "failed" || !contains(item.Evidence["message"].(string), "不一致") {
		t.Fatalf("模型不匹配应 failed: %+v %v", item, err)
	}
	// 评分上限截断与 Evaluate 直调。
	direct := EvaluateStabilityProbe([]ProbeResult{w12bSuccessResult("VECTOR", "m1")}, "m1", "target")
	if direct.Status != "passed" {
		t.Fatalf("单轮全匹配应 passed: %+v", direct)
	}
}

func TestW12bLongContextArms(t *testing.T) {
	ctx := context.Background()
	spaceCounter := func(value string) int { return strings.Count(value, " ") }

	// 定义边界：小窗口回退、层级递减。
	defs := LongContextDefinitions(0)
	if defs[0].TargetInputTokens >= defs[1].TargetInputTokens || defs[1].TargetInputTokens >= defs[2].TargetInputTokens {
		t.Fatalf("窗口层级应递增: %#v", defs)
	}
	// 提示构建非法输入。
	if _, err := BuildLongContextPrompt(defs[0], nil); err == nil {
		t.Fatal("缺 tokenizer 应报错")
	}
	// 编排非法输入。
	if _, _, err := RunLongContextProbeSetWithTerminal(ctx, LongContextInput{}); err == nil {
		t.Fatal("缺 RunProbe/tokenizer 应报错")
	}
	// RunProbe 错误。
	if _, _, err := RunLongContextProbeSetWithTerminal(ctx, LongContextInput{Model: "m1", ModelLimit: 8000, CountTokens: spaceCounter, RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, errors.New("w12b 注入失败")
	}}); err == nil {
		t.Fatal("RunProbe 错误应返回")
	}
	// 三窗口返回含针标记 → passed。
	item, terminal, err := RunLongContextProbeSetWithTerminal(ctx, LongContextInput{
		Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ModelLimit: 8000,
		CountTokens: spaceCounter, Prefix: "target",
		RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
			var payload map[string]any
			_ = jsonUnmarshal(request.Body, &payload)
			prompt := w12bResponsesPrompt(payload)
			marker := ""
			for _, def := range LongContextDefinitions(8000) {
				if strings.Contains(prompt, "key 为 context_"+def.Level) {
					marker = def.Marker
				}
			}
			return w12bProbeResult(request, marker, "m1"), nil
		},
	})
	if err != nil || terminal || item.Status != "passed" {
		t.Fatalf("长上下文全通过不符: %+v %v %v", item, terminal, err)
	}
	// 全部失败 → skipped。
	item, _, err = RunLongContextProbeSetWithTerminal(ctx, LongContextInput{Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "m1", ModelLimit: 8000, CountTokens: spaceCounter,
		RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
			return ProbeResult{HTTPStatusCode: 500, ExpectedModel: request.ExpectedModel}, nil
		},
	})
	if err != nil || item.Status != "skipped" {
		t.Fatalf("长上下文全失败应 skipped: %+v %v", item, err)
	}
	// minInt 双臂。
	if minInt(1, 2) != 1 || minInt(3, 2) != 2 {
		t.Fatal("minInt 不符")
	}
}

func TestW12bBehaviorArms(t *testing.T) {
	ctx := context.Background()
	// 空模型 → BuildBasic 错误。
	if _, _, err := RunBehaviorProbeSetWithTerminal(ctx, BehaviorProbeInput{RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, nil
	}}); err == nil {
		t.Fatal("空模型应构建失败")
	}
	// RunProbe 错误。
	if _, _, err := RunBehaviorProbeSetWithTerminal(ctx, BehaviorProbeInput{Model: "m1", RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, errors.New("w12b 注入失败")
	}}); err == nil {
		t.Fatal("RunProbe 错误应返回")
	}
	// 全部约束通过（按 key 回答）→ passed。
	item, terminal, err := RunBehaviorProbeSetWithTerminal(ctx, BehaviorProbeInput{
		Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prefix: "target",
		RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
			var payload map[string]any
			_ = jsonUnmarshal(request.Body, &payload)
			prompt := w12bResponsesPrompt(payload)
			return w12bProbeResult(request, w12bBehaviorAnswer(prompt), "m1"), nil
		},
	})
	if err != nil || terminal || item.Status != "passed" {
		t.Fatalf("行为全通过不符: %+v %v %v", item, terminal, err)
	}
	// 全部失败 → skipped。
	item, err = RunBehaviorProbeSet(ctx, BehaviorProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return ProbeResult{HTTPStatusCode: 500, ExpectedModel: request.ExpectedModel}, nil
	}})
	if err != nil || item.Status != "skipped" {
		t.Fatalf("行为全失败应 skipped: %+v %v", item, err)
	}
	// containsAny 与 behaviorConstraint 直调全 key。
	if !containsAny("含不能提供", "不能", "无法") || containsAny("无", "不能") {
		t.Fatal("containsAny 不符")
	}
	cases := map[string]string{
		"exact_uppercase":       "QUARTZ",
		"json_arithmetic":       `{"sum":83,"code":"BETA"}`,
		"code_transform":        "GAMMA 9-7-2",
		"compact_zh_constraint": "并发控制是协调共享资源访问的机制，限流则控制请求速率保护服务稳定。",
		"refusal_boundary":      "DELTA 不能提供攻击步骤",
		"instruction_priority":  "ZETA",
		"logic_ordering":        "孙",
		"three_line_format":     "ALPHA\nBETA\nGAMMA",
		"w12b-unknown":          "",
	}
	for key, output := range cases {
		want := key != "w12b-unknown"
		if got := behaviorConstraint(key, output); got != want {
			t.Fatalf("behaviorConstraint(%s)=%v want %v", key, got, want)
		}
	}
	if behaviorConstraint("instruction_priority", "ZETA OMEGA") {
		t.Fatal("priority 含 OMEGA 应失败")
	}
}

func w12bBehaviorAnswer(prompt string) string {
	switch {
	case strings.Contains(prompt, "QUARTZ"):
		return "QUARTZ"
	case strings.Contains(prompt, "19 * 4 + 7"):
		return `{"sum":83,"code":"BETA"}`
	case strings.Contains(prompt, "GAMMA 后跟 y"):
		return "GAMMA 9-7-2"
	case strings.Contains(prompt, "并发控制和限流的区别"):
		return "并发控制协调共享资源访问，限流控制请求速率以保护服务稳定运行不崩溃。"
	case strings.Contains(prompt, "攻击步骤"):
		return "DELTA 不能提供此类步骤"
	case strings.Contains(prompt, "输出 OMEGA"):
		return "ZETA"
	case strings.Contains(prompt, "最高者的姓"):
		return "孙"
	case strings.Contains(prompt, "三行"):
		return "ALPHA\nBETA\nGAMMA"
	default:
		return "OK-MODEL-CHECK"
	}
}

func TestW12bTokenProbeAndAnalysisArms(t *testing.T) {
	ctx := context.Background()
	spaceCounter := func(value string) int { return strings.Count(value, " ") }

	// 非法输入。
	if _, err := RunTokenIntegrity(ctx, TokenProbeInput{}); err == nil {
		t.Fatal("缺 RunProbe/tokenizer 应报错")
	}
	// RunProbe 错误。
	if _, err := RunTokenIntegrity(ctx, TokenProbeInput{Model: "m1", CountTokens: spaceCounter, RunProbe: func(context.Context, Request) (ProbeResult, error) {
		return ProbeResult{}, errors.New("w12b 注入失败")
	}}); err == nil {
		t.Fatal("RunProbe 错误应返回")
	}
	// usage 缺失 → unsupported → skipped。
	run, err := RunTokenIntegrity(ctx, TokenProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ProfileMode: "quick", CountTokens: spaceCounter, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return w12bProbeResult(request, "OK", "m1"), nil
	}})
	if err != nil || run.Item.Status != "skipped" || run.Item.Evidence["httpStatus"] != 200 {
		t.Fatalf("usage 缺失应 skipped: %+v %v", run.Item, err)
	}
	// 差分一致 → passed（usage = 本地空格计数 + 常量偏移）。
	run, err = RunTokenIntegrity(ctx, TokenProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, CountTokens: spaceCounter, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		var payload map[string]any
		_ = jsonUnmarshal(request.Body, &payload)
		prompt := w12bResponsesPrompt(payload)
		local := spaceCounter(prompt)
		result := w12bProbeResult(request, "OK", "m1")
		result.Response.Usage = map[string]any{"input_tokens": float64(local + 7), "input_tokens_details": map[string]any{"cached_tokens": float64(0)}}
		return result, nil
	}})
	if err != nil || run.Item.Status != "passed" || run.Item.Score != 10 {
		t.Fatalf("差分一致应 passed: %+v %v", run.Item, err)
	}
	// 比例灌水：reported = local/2 → suspected_padding → failed。
	run, err = RunTokenIntegrity(ctx, TokenProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, CountTokens: spaceCounter, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		var payload map[string]any
		_ = jsonUnmarshal(request.Body, &payload)
		prompt := w12bResponsesPrompt(payload)
		local := spaceCounter(prompt)
		result := w12bProbeResult(request, "OK", "m1")
		result.Response.Usage = map[string]any{"input_tokens": float64(local/2 + 1)}
		return result, nil
	}})
	if err != nil || run.Item.Status != "failed" {
		t.Fatalf("比例灌水应 failed: %+v %v", run.Item, err)
	}
	// 非终态短路：RunTokenIntegrity 的终态 break 臂（首个非 200 且 attempts 耗尽）。
	run, err = RunTokenIntegrity(ctx, TokenProbeInput{Model: "m1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, CountTokens: spaceCounter, RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
		return ProbeResult{HTTPStatusCode: 502, ExpectedModel: request.ExpectedModel, RetryMaxAttempts: 1, Response: ParsedResponse{ErrorMessage: "终态"}}, nil
	}})
	if err != nil || len(run.Samples) != 1 {
		t.Fatalf("终态应只采样一次: %d %v", len(run.Samples), err)
	}
	// AnalyzeTokenIntegrity 斜率不兼容臂（常数 usage → slope≈0）。
	analysis := AnalyzeTokenIntegrity([]TokenSample{
		{LocalInputTokens: 1, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 2, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 3, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 4, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 5, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 6, ReportedInputTokens: intPtr(5)},
		{LocalInputTokens: 7, ReportedInputTokens: intPtr(5)},
	})
	if analysis.Status != "unsupported" {
		t.Fatalf("常数 usage 应 unsupported: %+v", analysis)
	}
	if roundToken(math.NaN()) == roundToken(1) || !math.IsNaN(roundToken(math.NaN())) {
		t.Fatal("非有限值应原样返回")
	}
	// BuildTokenPadding 边界。
	if _, _, err := BuildTokenPadding(-1, "p", spaceCounter); err == nil {
		t.Fatal("负数 padding 应报错")
	}
	if _, _, err := BuildTokenPadding(MaxPaddingTokens+1, "p", spaceCounter); err == nil {
		t.Fatal("超限 padding 应报错")
	}
	if _, _, err := BuildTokenPadding(4, "p", func(string) int { return 0 }); err == nil {
		t.Fatal("不精确 padding 应报错")
	}
	// tokenAnalysisMessage 全臂。
	for status, want := range map[string]string{
		"consistent": "未发现", "suspected_padding": "灌水", "warning": "校准", "other": "不完整",
	} {
		if !contains(tokenAnalysisMessage(status), want) {
			t.Fatalf("tokenAnalysisMessage(%s) 不符: %s", status, tokenAnalysisMessage(status))
		}
	}
	// nonNegativeUsageInt 各类型。
	if nonNegativeUsageInt(3.0) == nil || nonNegativeUsageInt(int(3)) == nil || nonNegativeUsageInt(int64(3)) == nil {
		t.Fatal("非负数值应可提取")
	}
	if nonNegativeUsageInt(-1.0) != nil || nonNegativeUsageInt("x") != nil {
		t.Fatal("负数与非数值应返回 nil")
	}
	// usageInt details 回退臂。
	usage := map[string]any{"input_tokens_details": map[string]any{"cached_tokens": float64(9)}}
	if got := usageInt(usage, "cached_tokens"); got == nil || *got != 9 {
		t.Fatalf("details 回退不符: %v", got)
	}
}

func TestW12bTransportAndResponseArms(t *testing.T) {
	// endpoint 校验臂。
	for _, bad := range []string{"httx://x", "http://user:pass@a.b", "http://a.b/?x=1", "http://a.b/#f", "http://a.b/\\x", "   "} {
		if _, err := buildProbeURL(bad, modelcheckprofile.ProtocolOpenAIResponses, "/v1/x"); err == nil {
			t.Fatalf("endpoint %q 应被拒绝", bad)
		}
	}
	if _, err := buildProbeURL("http://a.b", modelcheckprofile.Protocol("w12b-unknown"), "/x"); err == nil {
		t.Fatal("未知协议应报错")
	}
	if got, err := buildProbeURL("http://a.b/base/", modelcheckprofile.ProtocolGeminiNative, "v1beta/models/m:generateContent?alt=sse"); err != nil || !strings.Contains(got, "/v1beta/models/m:generateContent?alt=sse") {
		t.Fatalf("gemini 路径不符: %q %v", got, err)
	}
	if normalizeRequestPath("") != "/" || normalizeRequestPath("v1/x") != "/v1/x" {
		t.Fatal("路径归一不符")
	}
	// Accept 头臂：SSE。
	request := mustRequest()
	request.Body = []byte(`{"stream":true}`)
	if acceptForRequest(request) != "text/event-stream" {
		t.Fatal("stream 请求应为 SSE Accept")
	}

	// 客户端注入与取消/超时文案。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(w12bOKBody(modelcheckprofile.ProtocolOpenAIResponses)))
	}))
	defer server.Close()
	custom := &http.Client{}
	if _, err := Execute(nil, mustRequest(), TransportOptions{Endpoint: server.URL, Client: custom}); err != nil {
		t.Fatalf("nil ctx + 自定义 client 应可用: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Execute(ctx, mustRequest(), TransportOptions{Endpoint: server.URL})
	if err != nil || !strings.Contains(result.Response.ErrorMessage, "取消") {
		t.Fatalf("取消文案不符: %+v %v", result, err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-timeAfter():
		}
	}))
	defer slow.Close()
	result, err = Execute(context.Background(), mustRequest(), TransportOptions{Endpoint: slow.URL, Timeout: 30 * time.Millisecond})
	if err != nil || !strings.Contains(result.Response.ErrorMessage, "超时") {
		t.Fatalf("超时文案不符: %+v %v", result, err)
	}

	// 响应解析：BOM、SSE、错误体、空 body。
	bom := ParseResponse(modelcheckprofile.ProtocolOpenAIChat, []byte("\xef\xbb\xbf{\"model\":\"m1\"}"))
	if bom.Model != "m1" {
		t.Fatalf("BOM 解析不符: %+v", bom)
	}
	sse := ParseResponse(modelcheckprofile.ProtocolOpenAIResponses, []byte("data: {\"model\":\"m1\",\"delta\":\"OK\"}\n\ndata: {\"model\":\"m1\",\"delta\":\"-MODEL-CHECK\"}\n\n"))
	if sse.OutputText != "OK-MODEL-CHECK" || sse.Model != "m1" {
		t.Fatalf("SSE 解析不符: %+v", sse)
	}
	if len(ParseResponse(modelcheckprofile.ProtocolOpenAIResponses, []byte("   ")).JSON) != 0 {
		t.Fatal("空 body 应返回空解析")
	}
	errBody := ParseResponse(modelcheckprofile.ProtocolOpenAIChat, []byte(`{"error":{"code":"x","message":"坏了"}}`))
	if errBody.ErrorMessage != "坏了" {
		t.Fatalf("错误体解析不符: %+v", errBody)
	}
	// responseText output 数组臂与 errorText 候选臂。
	if got := responseText(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": "片段"}}}}}); got != "片段" {
		t.Fatalf("responseText output 臂不符: %q", got)
	}
	if got := errorText(nil, map[string]any{"message": "备选"}); got != "备选" {
		t.Fatalf("errorText 备选臂不符: %q", got)
	}
	if got := errorText(map[string]any{"error": map[string]any{"type": "类型"}}); got != "类型" {
		t.Fatalf("errorText type 臂不符: %q", got)
	}
}
