package modelcheckprobe

// w14f_probe_arms_test.go 用「按请求体标记失败的 Stage transport」驱动 RunSuite
// 各探针族的错误/终局分支，并直驱构建器与评估函数补齐剩余臂。
//
// 不可达语句登记（在当前契约下确实无法触发的臂，保持原文不改）：
//   - probe.go 各 Build* 内 json.Marshal / json.Unmarshal 错误臂（载荷为刚构造
//     的 map，序列化不失败）；
//   - probe.go buildBasicWithTunings / applyStructuredToolTunings 的
//     `generation == nil` 防御臂（BuildBasic 的 Gemini 载荷恒含 generationConfig）；
//   - probe.go Execute 内 http.NewRequestWithContext 错误臂（buildURL 已先行
//     校验同一 URL）；
//   - probe.go parseResponseDetailed 的 `payload == nil` continue 臂
//     （parseSSEResponseEvents 只收非空 JSON 载荷）与其 json.Marshal 错误臂；
//   - probe.go normalizeOpenAIOAuthCodexRequest 内 codexUUID /
//     mustHeaderUUID / json.Marshal 的错误臂（crypto/rand 不失败）；
//   - token.go randomTokenNonce、juice.go randomJuiceNonce /
//     randomJuiceCoverage / buildJuiceRequest、identity.go rand.Read 的
//     crypto/rand 与序列化错误臂；
//   - juice.go randomJuiceCoverage 的 32 次全签名碰撞臂（概率 ~1e-29）；
//   - retry.go ExecuteWithRetry 循环自然退出臂（末次尝试必在循环内返回）；
//   - suite.go probeMode 之后的 tunedBasic / buildStructured / buildTool /
//     RunSelfCrossModel / RunTrustedComparison 内部构建错误臂（协议与模式在
//     probeMode 已通过校验，同参数构建不会再失败）；
//   - suite.go RunSelfCrossModel 的 probeMode 二次错误臂（同一输入已通过）；
//   - distribution.go EvaluateDistribution 的 `score < 0` / `score > 15` 收敛臂
//     （加权分量均非负且权重和为 1）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ---- 探针构建器错误臂 ----

func TestW14fProbeBuilderArms(t *testing.T) {
	if _, err := BuildOpenAIOAuthCodexTool("", true); err == nil {
		t.Fatalf("空模型的 Codex tool 构建应报错")
	}
	// 空 endpointMode 回落 BuildBasic（194）。
	if _, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "w14f-m", "w14f-p", ""); err != nil {
		t.Fatalf("空模式应回落基础构建: %v", err)
	}
	// 基础构建在空模型下报错（201）。
	if _, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "", "w14f-p", "responses_json"); err == nil {
		t.Fatalf("空模型构建应报错")
	}
	// 模式与协议不匹配（223）。
	if _, err := buildBasicWithTunings(modelcheckprofile.ProtocolOpenAIResponses, "w14f-m", "w14f-p", "chat_json", false, 16, 0); err == nil {
		t.Fatalf("不匹配的 endpoint mode 应报错")
	}
	if _, err := BuildStructured(modelcheckprofile.ProtocolOpenAIResponses, "", false); err == nil {
		t.Fatalf("空模型 structured 构建应报错")
	}
	if _, err := BuildStructuredForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "", ""); err == nil {
		t.Fatalf("空模型 structured-for-mode 构建应报错")
	}
	if _, err := BuildStructuredForEndpointMode(modelcheckprofile.ProtocolGeminiNative, "w14f-m", "generate_content_json"); err != nil {
		t.Fatalf("Gemini structured-for-mode 构建不应报错: %v", err)
	}
	if _, err := BuildTool(modelcheckprofile.ProtocolOpenAIResponses, "", false); err == nil {
		t.Fatalf("空模型 tool 构建应报错")
	}
	if _, err := BuildToolForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "", "responses_json"); err == nil {
		t.Fatalf("空模型 tool-for-mode 构建应报错")
	}
	if _, err := BuildToolForEndpointMode(modelcheckprofile.ProtocolGeminiNative, "w14f-m", "generate_content_json"); err != nil {
		t.Fatalf("Gemini tool-for-mode 构建不应报错: %v", err)
	}
	if message := responseErrorMessage("", nil); message != "" {
		t.Fatalf("nil 载荷的错误消息应为空: %q", message)
	}
}

func TestW14fSuiteHelperArms(t *testing.T) {
	// UpstreamProtocol 未设置 → 回落 Protocol（443）。
	suite := Suite{Protocol: modelcheckprofile.ProtocolOpenAIResponses}
	if !suite.supportsTokenIdentityProbes() {
		t.Fatalf("responses 协议应支持 token identity 探针")
	}
	// 空 Suite 的 probeMode → 无默认模式（549）。
	if mode, stream, err := (Suite{}).probeMode(); err != nil || mode != "" || stream {
		t.Fatalf("空 Suite probeMode: %q %v %v", mode, stream, err)
	}
	// Codex 适配器的 tunedBasic 直达基础形状（595）。
	codex := Suite{Adapter: AdapterOpenAIOAuthCodex, Protocol: modelcheckprofile.ProtocolOpenAIResponses}
	if request, err := codex.tunedBasic("w14f-m", "w14f-p", "responses_json", false, 16, 0); err != nil || request.Path != "/responses" {
		t.Fatalf("codex tunedBasic: %+v %v", request, err)
	}
	// SupportedModels 限定下 pairedModel 取第一个异于主模型的候选（515）。
	paired := Suite{Model: "gpt-5.6-sol", SupportedModels: []string{"gpt-5.6-sol", "gpt-5.6-terra"}}.pairedModel()
	if paired != "gpt-5.6-terra" {
		t.Fatalf("pairedModel = %q", paired)
	}
}

// ---- token integrity 错误臂 ----

type w14fFuncTokenizer struct {
	version string
	count   func(string) (int, error)
}

func (t w14fFuncTokenizer) Version() string { return t.version }
func (t w14fFuncTokenizer) Count(value string) (int, error) {
	return t.count(value)
}

// w14fSpaceXTokenizer 复刻 deterministicTokenizer 的 " x" 计数语义。
func w14fSpaceXTokenizer() Tokenizer {
	return w14fFuncTokenizer{version: "w14f-spacex-v1", count: func(value string) (int, error) {
		count := 1
		for index := 0; index+1 < len(value); index++ {
			if value[index] == ' ' && value[index+1] == 'x' {
				count++
			}
		}
		return count, nil
	}}
}

func TestW14fTokenIntegrityArms(t *testing.T) {
	ctx := context.Background()
	responses := modelcheckprofile.ProtocolOpenAIResponses
	run := func(_ context.Context, request Request) (Result, error) {
		count := 1 + strings.Count(string(request.Body), " x")
		return Result{Success: true, HTTPStatus: 200, ObservedModel: request.ExpectedModel, Output: "OK", Usage: map[string]any{"input_tokens": float64(count)}}, nil
	}
	// 空 model / nil run（36）。
	if _, err := runTokenIntegrity(ctx, responses, " ", w14fSpaceXTokenizer(), run, 1); err == nil {
		t.Fatalf("空 model 应报错")
	}
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", w14fSpaceXTokenizer(), nil, 1); err == nil {
		t.Fatalf("nil run 应报错")
	}
	// rounds < 1（39）。
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", w14fSpaceXTokenizer(), run, 0); err == nil {
		t.Fatalf("rounds=0 应报错")
	}
	// tokenizer 前缀计数失败（53）。
	alwaysErr := w14fFuncTokenizer{version: "w14f-err-v1", count: func(string) (int, error) { return 0, errors.New("w14f count failure") }}
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", alwaysErr, run, 1); err == nil {
		t.Fatalf("计数失败应报错")
	}
	// endpoint mode 与协议不匹配（61）。
	if _, err := runTokenIntegrity(ctx, modelcheckprofile.ProtocolAnthropic, "w14f-m", w14fSpaceXTokenizer(), run, 1, "responses_json"); err == nil {
		t.Fatalf("模式不匹配应报错")
	}
	// run 返回错误（65）。
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", w14fSpaceXTokenizer(), func(context.Context, Request) (Result, error) {
		return Result{}, errors.New("w14f run failure")
	}, 1); err == nil {
		t.Fatalf("run 失败应报错")
	}
	// 迭代内计数失败（132）：候选含 padding 时报错。
	paddingErr := w14fFuncTokenizer{version: "w14f-paderr-v1", count: func(value string) (int, error) {
		if strings.Contains(value, " x") {
			return 0, errors.New("w14f padding count failure")
		}
		return 1, nil
	}}
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", paddingErr, run, 1); err == nil {
		t.Fatalf("padding 计数失败应报错")
	}
	// 零进展 tokenizer：循环耗尽（143）。
	stuck := w14fFuncTokenizer{version: "w14f-stuck-v1", count: func(string) (int, error) { return 1, nil }}
	if _, err := runTokenIntegrity(ctx, responses, "w14f-m", stuck, run, 1); err == nil {
		t.Fatalf("无法构造精确 padding 应报错")
	}
	// suspected_padding 状态臂（90）：上报用量与本地计数成比例放大。
	inflated := func(_ context.Context, request Request) (Result, error) {
		count := 1 + strings.Count(string(request.Body), " x")
		return Result{Success: true, HTTPStatus: 200, ObservedModel: request.ExpectedModel, Output: "OK", Usage: map[string]any{"input_tokens": float64(count * 4)}}, nil
	}
	item, err := runTokenIntegrity(ctx, responses, "w14f-m", w14fSpaceXTokenizer(), inflated, 3)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "failed" || item.Evidence["requestCount"] == 0 {
		t.Fatalf("inflated usage 应触发 suspected_padding/failed: %+v", item)
	}
}

// ---- long context 错误臂 ----

func TestW14fLongContextArms(t *testing.T) {
	ctx := context.Background()
	responses := modelcheckprofile.ProtocolOpenAIResponses
	limits := deterministicLimits{}
	okRun := func(_ context.Context, request Request) (Result, error) {
		return Result{Success: true, HTTPStatus: 200, ObservedModel: request.ExpectedModel, Output: "NEEDLE-LOW"}, nil
	}
	// tokenizer 前缀计数失败（47/78）。
	alwaysErr := w14fFuncTokenizer{version: "w14f-err-v1", count: func(string) (int, error) { return 0, errors.New("w14f count failure") }}
	if _, err := RunLongContext(ctx, "openai", "w14f-m", responses, alwaysErr, limits, okRun); err == nil {
		t.Fatalf("计数失败应报错")
	}
	// endpoint mode 不匹配（55）。
	if _, err := RunLongContext(ctx, "openai", "w14f-m", responses, w14fSpaceXTokenizer(), limits, okRun, "chat_json"); err == nil {
		t.Fatalf("模式不匹配应报错")
	}
	// run 失败（65）。
	if _, err := RunLongContext(ctx, "openai", "w14f-m", responses, w14fSpaceXTokenizer(), limits, func(context.Context, Request) (Result, error) {
		return Result{}, errors.New("w14f long run failure")
	}); err == nil {
		t.Fatalf("run 失败应报错")
	}
	// 干扰文本计数失败（84）。
	fillerErr := w14fFuncTokenizer{version: "w14f-fillererr-v1", count: func(value string) (int, error) {
		if strings.Contains(value, "干扰") {
			return 0, errors.New("w14f filler count failure")
		}
		return 1, nil
	}}
	if _, err := buildLongContextPrompt(fillerErr, LongContextDefinition{Key: "context_low", Marker: "NEEDLE-LOW", TargetInputTokens: 40}); err == nil {
		t.Fatalf("干扰文本计数失败应报错")
	}
	// 线性 tokenizer 驱动增长循环（90）。
	linear := w14fFuncTokenizer{version: "w14f-linear-v1", count: func(value string) (int, error) {
		return 1 + len([]rune(value))/8, nil
	}}
	prompt, err := buildLongContextPrompt(linear, LongContextDefinition{Key: "context_low", Marker: "NEEDLE-LOW", TargetInputTokens: 40})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(prompt)) < 40 {
		t.Fatalf("线性计数下 prompt 应增长: %d", len([]rune(prompt)))
	}
	// 评估：成功但响应缺 model 字段 → needle 命中走中性分支（135）。
	evaluation := EvaluateLongContext([]LongContextObservation{
		{Key: "context_low", Marker: "NEEDLE-LOW-x", TargetInputTokens: 10, Result: Result{Success: true, HTTPStatus: 200, Output: "prefix NEEDLE-LOW-x suffix"}},
	}, "w14f-m")
	if evaluation.Status != "warning" && evaluation.Status != "passed" {
		t.Fatalf("缺 model 证据应为 warning/neutral: %+v", evaluation)
	}
}

// ---- distribution 评估臂 ----

func w14fPairResult(success bool, output string, usage map[string]any) Result {
	result := Result{Success: success, HTTPStatus: 200, Output: output, ObservedModel: "w14f-sol", Usage: usage}
	return result
}

func TestW14fDistributionEvaluationArms(t *testing.T) {
	good := DistributionPair{
		Definition: DistributionDefinition{Key: "sequence_transform", Prompt: "从小到大"},
		Target:     w14fPairResult(true, "THETA 4|7|9", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
		Comparison: w14fPairResult(true, "THETA 4|7|9", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
	}
	// 目标约束失败、可信侧正常 → failed 路径与 0 分量（52/85）。
	divergent := DistributionPair{
		Definition: DistributionDefinition{Key: "table_extract", Prompt: "北区"},
		Target:     w14fPairResult(true, "完全无关的输出内容", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
		Comparison: w14fPairResult(true, "IOTA 17 23", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
	}
	evaluation := EvaluateDistribution([]DistributionPair{good, divergent})
	if evaluation.Status != "failed" {
		t.Fatalf("目标发散应为 failed: %+v", evaluation)
	}
	// 中等分数 → warning（93）。
	halfSimilar := DistributionPair{
		Definition: DistributionDefinition{Key: "style_compact", Prompt: "向量数据库"},
		Target:     w14fPairResult(true, "向量数据库召回率衡量结果相关内容被找回的比例", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
		Comparison: w14fPairResult(true, "完全不同的另一句输出内容", map[string]any{"prompt_tokens": 10.0, "completion_tokens": 10.0}),
	}
	evaluation = EvaluateDistribution([]DistributionPair{good, halfSimilar})
	if evaluation.Status == "failed" && evaluation.Score >= 12 {
		t.Fatalf("中等相似度不应评 failed 高分: %+v", evaluation)
	}
	// usage 只有左值（178）。
	value, ok := distributionTotalTokens(map[string]any{"prompt_tokens": 7.0})
	if !ok || value != 7 {
		t.Fatalf("usage 单左值: %v %v", value, ok)
	}
	// 归一化后无可比对 token（229）。
	if similarity := distributionTextSimilarity("!!!", "？？？"); similarity != 0 {
		t.Fatalf("无可比 token 相似度应为 0: %v", similarity)
	}
}

// ---- retry 错误臂 ----

func TestW14fRetryArms(t *testing.T) {
	// 零值 RetryOptions + 零超时 → DefaultTimeout 臂（47）。
	result, err := ExecuteWithRetry(context.Background(), Request{Path: "/v1/responses"}, Options{Endpoint: "http://w14f-target.test"}, RetryOptions{})
	if err != nil {
		t.Fatalf("默认单尝试不应报错: %v", err)
	}
	if result.Success {
		t.Fatalf("控制器错误地址不应成功: %+v", result)
	}
	// 首次 Execute 失败 → attachRetry 空 attempts（83/120）。
	_, err = ExecuteWithRetry(context.Background(), Request{Path: "/v1/responses"}, Options{Endpoint: "w14f-controller-error"}, RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond}})
	if err == nil {
		t.Fatalf("传输失败应报错")
	}
	// 取消上下文 + 默认退避 → ctx.Err（151）。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExecuteWithRetry(ctx, Request{Path: "/v1/responses"}, Options{Endpoint: "w14f-controller-error"}, RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}); err == nil {
		t.Fatalf("取消上下文应报错")
	}
}

// ---- behavior / identity 评估臂 ----

func w14fBehaviorRun(outputs map[string]string, fail bool) func(context.Context, Request) (Result, error) {
	return func(_ context.Context, request Request) (Result, error) {
		if fail {
			return Result{}, errors.New("w14f behavior run failure")
		}
		var payload struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(request.Body, &payload)
		output := "unrelated"
		for marker, value := range outputs {
			if strings.Contains(payload.Input, marker) {
				output = value
			}
		}
		return Result{Success: true, HTTPStatus: 200, ObservedModel: request.ExpectedModel, Output: output}, nil
	}
}

func TestW14fBehaviorArms(t *testing.T) {
	ctx := context.Background()
	responses := modelcheckprofile.ProtocolOpenAIResponses
	// 非法输入（31）。
	if _, err := RunBehavior(ctx, responses, " ", nil); err == nil {
		t.Fatalf("空 model 应报错")
	}
	if _, err := RunBehavior(ctx, responses, "w14f-m", nil); err == nil {
		t.Fatalf("nil run 应报错")
	}
	// 模式不匹配（42）。
	if _, err := RunBehavior(ctx, responses, "w14f-m", w14fBehaviorRun(nil, false), "chat_json"); err == nil {
		t.Fatalf("模式不匹配应报错")
	}
	// run 失败（46）。
	if _, err := RunBehavior(ctx, responses, "w14f-m", w14fBehaviorRun(nil, true)); err == nil {
		t.Fatalf("run 失败应报错")
	}
	// 部分通过 → warning（77）。
	outputs := map[string]string{
		"QUARTZ":  "wrong",
		"19 * 4 + 7": "wrong",
		"const xs": "wrong",
		"并发":      "并发限流压测期间并发限流保持稳定并记录限流数据",
		"绕过他人":    "DELTA 不能提供",
		"OMEGA":   "ZETA",
		"小赵":      "孙",
		"三行":      "ALPHA\nBETA\nGAMMA",
	}
	item, err := RunBehavior(ctx, responses, "w14f-m", w14fBehaviorRun(outputs, false))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "warning" {
		t.Fatalf("部分通过应为 warning: %+v", item)
	}
}

func TestW14fIdentityArms(t *testing.T) {
	ctx := context.Background()
	responses := modelcheckprofile.ProtocolOpenAIResponses
	// 模式不匹配（64）。
	if _, err := RunIdentityForModels(ctx, responses, "w14f-m", nil, w14fBehaviorRun(nil, false), "chat_json"); err == nil {
		t.Fatalf("模式不匹配应报错")
	}
	// 6/7 通过 → warning（87）。
	identityRun := func(_ context.Context, request Request) (Result, error) {
		var payload struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(request.Body, &payload)
		tagStart := strings.Index(payload.Input, "CANARY-")
		tag := "CANARY-XXXXXX"
		if tagStart >= 0 {
			tag = strings.TrimSpace(payload.Input[tagStart : tagStart+13])
		}
		output := "not-json"
		switch {
		case strings.Contains(payload.Input, "23 + 19"):
			output = `{"result":42,"tag":"` + tag + `"}`
		case strings.Contains(payload.Input, "filter"):
			output = "wrong"
		case strings.Contains(payload.Input, "第二大值"):
			output = `{"largest":15,"tag":"` + tag + `"}`
		case strings.Contains(payload.Input, "43"):
			output = `{"correct":42,"tag":"` + tag + `"}`
		case strings.Contains(payload.Input, "queue timeout"):
			output = `{"zh":"队列超时","en":"queue timeout","tag":"` + tag + `"}`
		case strings.Contains(payload.Input, "inspect"):
			output = `{"action":"inspect","payload":{"ids":[2,7,9],"dryRun":true},"tag":"` + tag + `"}`
		case strings.Contains(payload.Input, "2024-10"):
			output = `{"version":"B","tag":"` + tag + `"}`
		}
		return Result{Success: true, HTTPStatus: 200, ObservedModel: request.ExpectedModel, Output: output}, nil
	}
	item, err := RunIdentityForModels(ctx, responses, "w14f-m", []string{"w14f-m"}, identityRun)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "warning" {
		t.Fatalf("6/7 通过应为 warning: %+v", item)
	}
}

// ---- summary 阶梯臂 ----

func TestW14fSummaryArms(t *testing.T) {
	// juice 大额罚分把分数压到负数 → 收敛 0（44）。
	penalized := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "juice", Status: "passed", Score: 0, MaxScore: 0, Evidence: map[string]any{"scorePenalty": 150}},
	}, false, "full")
	if penalized.Score != 0 {
		t.Fatalf("罚分后分数应收敛 0: %+v", penalized)
	}
	// stability skipped → uncertain（75）。
	uncertain := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "stability", Status: "skipped", Evidence: map[string]any{"excludedFromScoring": true}},
	}, false, "full")
	if uncertain.Level != "uncertain" {
		t.Fatalf("stability skipped 应 uncertain: %+v", uncertain)
	}
	// quick 低分 → suspicious（94）：basic 通过但总分不足。
	low := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 2, MaxScore: 10, Evidence: map[string]any{"success": true}},
	}, false, "quick")
	if low.Level != "suspicious" {
		t.Fatalf("quick 低分应 suspicious: %+v", low)
	}
	// full 50-77 → uncertain（114）。
	mid := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 8, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "long_context", Status: "passed", Score: 9, MaxScore: 15, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 20, MaxScore: 35, Evidence: map[string]any{"success": true}},
	}, false, "full")
	if mid.Level != "uncertain" || mid.Score < 50 || mid.Score >= 78 {
		t.Fatalf("中段分数应 uncertain: %+v", mid)
	}
}

// ---- RunSuite 阶段失败 transport ----

const w14fTargetModel = "gpt-5.6-sol"

// w14fStageTransport 按「请求体标记 → 动作」失败：transport 错误 / 500 /
// 200 错误信封；其余请求回显请求 model 并返回 OK 输出。
type w14fStageTransport struct {
	mu            sync.Mutex
	requests      int
	crossCount    int
	crossFailFrom int // 第 N 次 CROSS-MODEL-OK 请求起返回 500（触发重试边界错误）
	errorMarker   func(string) bool
	statusMRkr    func(string) bool
	envelopeMRk   func(string) bool
}

func (t *w14fStageTransport) crossSnapshot() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.crossCount
}

func w14fUpstreamFailure(request *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"w14f upstream failure"}}`)), Request: request}
}

func (t *w14fStageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	t.mu.Lock()
	t.requests++
	if strings.Contains(text, "CROSS-MODEL-OK") {
		t.crossCount++
	}
	t.mu.Unlock()
	if t.errorMarker != nil && t.errorMarker(text) {
		return nil, errors.New("w14f transport failure")
	}
	if t.crossFailFrom > 0 && strings.Contains(text, "CROSS-MODEL-OK") && t.crossSnapshot() >= t.crossFailFrom {
		return w14fUpstreamFailure(request), nil
	}
	if t.statusMRkr != nil && t.statusMRkr(text) {
		return w14fUpstreamFailure(request), nil
	}
	if t.envelopeMRk != nil && t.envelopeMRk(text) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"model_not_found","message":"w14f model missing"}}`)), Request: request}, nil
	}
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	model := payload.Model
	if model == "" {
		model = w14fTargetModel
	}
	output := "OK-MODEL-CHECK"
	if strings.Contains(text, "CROSS-MODEL-OK") {
		output = "CROSS-MODEL-OK"
	}
	if strings.Contains(text, "STREAM-OK") {
		output = "STREAM-OK"
	}
	if strings.Contains(text, "json_reasoning") || strings.Contains(text, "SIGMA") {
		output = `{"result":83,"tag":"SIGMA"}`
	}
	encoded, _ := json.Marshal(output)
	responseBody := `{"model":"` + model + `","output_text":` + string(encoded) + `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
}

func w14fStageSuite(t *testing.T, transport *w14fStageTransport, profile string, mutate func(*Suite)) Suite {
	t.Helper()
	suite := Suite{
		Endpoint:     "http://w14f-target.test",
		ProviderCode: "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Client:       &http.Client{Transport: transport},
		Model:        w14fTargetModel,
		Profile:      profile,
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:    deterministicTokenizer{},
		ModelLimits:  deterministicLimits{},
	}
	if mutate != nil {
		mutate(&suite)
	}
	return suite
}

func w14fRunSuite(t *testing.T, suite Suite) ([]Evaluation, error) {
	t.Helper()
	return RunSuite(context.Background(), suite, 30*time.Second)
}

// TestW14fSuiteStageFailures 用「标记请求 500 + 重试配置尾部 0 超时」在指定
// 探针族的重试边界制造 execute 错误：首个尝试 500（非终局、非末次）→ 第二次
// 尝试 timeout<=0 → ExecuteWithRetry 报错，RunSuite 在对应阶段返回错误。
func TestW14fSuiteStageFailures(t *testing.T) {
	cases := []struct {
		name     string
		profile  string
		mutate   func(*Suite)
		statusFn func(string) bool
		wantErr  string
	}{
		{"basic execute error", "quick", nil, func(string) bool { return true }, "retry timeout must be positive"},
		{"stream stage error", "quick", func(s *Suite) { s.EndpointMode = modelcheckprofile.EndpointModeResponsesSSE }, func(text string) bool { return strings.Contains(text, "STREAM-OK") }, "retry timeout must be positive"},
		{"structured error", "quick", nil, func(text string) bool { return strings.Contains(text, "json_schema") }, "retry timeout must be positive"},
		{"tool error", "quick", nil, func(text string) bool { return strings.Contains(text, "record_model_check") }, "retry timeout must be positive"},
		{"quick token error", "quick", nil, func(text string) bool { return strings.Contains(text, "token-integrity-v1") }, "retry timeout must be positive"},
		{"quick cross error", "quick", nil, func(text string) bool { return strings.Contains(text, "CROSS-MODEL-OK") }, "retry timeout must be positive"},
		{"full behavior error", "full", nil, func(text string) bool { return strings.Contains(text, "OMEGA") }, "retry timeout must be positive"},
		{"full long error", "full", nil, func(text string) bool { return strings.Contains(text, "NEEDLE") }, "retry timeout must be positive"},
		{"full stability error", "full", nil, func(text string) bool { return strings.Contains(text, "VECTOR") }, "retry timeout must be positive"},
		{"full token error", "full", nil, func(text string) bool { return strings.Contains(text, "token-integrity-v1") }, "retry timeout must be positive"},
		{"full identity error", "full", nil, func(text string) bool { return strings.Contains(text, "CANARY") }, "retry timeout must be positive"},
		{"full juice error", "full", nil, func(text string) bool { return strings.Contains(text, "Valid Channels") }, "retry timeout must be positive"},
		{"full cross error", "full", nil, func(text string) bool { return strings.Contains(text, "CROSS-MODEL-OK") }, "retry timeout must be positive"},
		{"basic url error", "quick", func(s *Suite) { s.Endpoint = "://w14f-bad" }, nil, "endpoint URL is invalid"},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			transport := &w14fStageTransport{statusMRkr: item.statusFn}
			suite := w14fStageSuite(t, transport, item.profile, func(s *Suite) {
				s.Retry = RetryOptions{AttemptTimeouts: []time.Duration{time.Second, 0}}
				if item.mutate != nil {
					item.mutate(s)
				}
			})
			_, err := w14fRunSuite(t, suite)
			if err == nil || !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("%s 应报错: %v", item.name, err)
			}
		})
	}
}

func TestW14fSuiteTerminalFamilyArms(t *testing.T) {
	// structured 终局（500）→ 提前返回 usage 汇总。
	transport := &w14fStageTransport{statusMRkr: func(text string) bool { return strings.Contains(text, "json_schema") }}
	items, err := w14fRunSuite(t, w14fStageSuite(t, transport, "quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	foundUsage := false
	for _, item := range items {
		if item.Kind == "usage_shape" {
			foundUsage = true
		}
	}
	if !foundUsage {
		t.Fatalf("终局返回应包含 usage 评估: %+v", items)
	}

	// full profile：long context 终局（500）→ 提前返回。
	longTransport := &w14fStageTransport{statusMRkr: func(text string) bool { return strings.Contains(text, "NEEDLE") }}
	items, err = w14fRunSuite(t, w14fStageSuite(t, longTransport, "full", nil))
	if err != nil {
		t.Fatal(err)
	}
	foundLong := false
	for _, item := range items {
		if item.Kind == "long_context" {
			foundLong = true
		}
	}
	if !foundLong {
		t.Fatalf("long context 终局项缺失: %+v", items)
	}

	// full profile：identity 终局（500）→ 提前返回。
	identityTransport := &w14fStageTransport{statusMRkr: func(text string) bool { return strings.Contains(text, "CANARY") }}
	items, err = w14fRunSuite(t, w14fStageSuite(t, identityTransport, "full", nil))
	if err != nil {
		t.Fatal(err)
	}
	foundIdentity := false
	for _, item := range items {
		if item.Kind == "identity_observation" {
			foundIdentity = true
		}
	}
	if !foundIdentity {
		t.Fatalf("identity 终局项缺失: %+v", items)
	}

	// basic 200 错误信封 → 非终局失败，full 流程 cross_model 走 skipped 臂（284）。
	envelopeTransport := &w14fStageTransport{envelopeMRk: func(text string) bool { return strings.Contains(text, "OK-MODEL-CHECK") }}
	items, err = w14fRunSuite(t, w14fStageSuite(t, envelopeTransport, "full", nil))
	if err != nil {
		t.Fatal(err)
	}
	crossSkipped := false
	for _, item := range items {
		if item.Kind == "cross_model" && item.Status == "skipped" {
			crossSkipped = true
		}
	}
	if !crossSkipped {
		t.Fatalf("basic 失败时 cross_model 应 skipped: %+v", items)
	}
}

func TestW14fQuickTrustedComparisonArms(t *testing.T) {
	// Comparison 缺失 → 快速可信对比直接报错（308）。
	target := w14fStageSuite(t, &w14fStageTransport{}, "quick", nil)
	if _, err := runQuickTrustedComparison(context.Background(), target, nil, time.Second); err == nil {
		t.Fatalf("缺失对比 suite 应报错")
	}

	// 对比端 endpoint 非法 → RunSuite(trusted) 在 basic 阶段报错（318 / 173）。
	failing := &w14fStageTransport{}
	comparison := w14fStageSuite(t, failing, "quick", func(s *Suite) { s.Endpoint = "://w14f-bad" })
	target.Comparison = &comparison
	if _, err := runQuickTrustedComparison(context.Background(), target, nil, time.Second); err == nil {
		t.Fatalf("对比端失败应报错")
	}
	// targetItems 为 nil 时聚合函数仍可执行（空切片）。
	aggregate := buildQuickTrustedComparison(nil, nil)
	if aggregate.Kind != "trusted_comparison.comparison" {
		t.Fatalf("聚合项: %+v", aggregate)
	}
}

func TestW14fRunTrustedComparisonArms(t *testing.T) {
	newSuite := func(transport *w14fStageTransport, model string) Suite {
		return Suite{
			Endpoint:                  "http://w14f-" + model + ".test",
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "profile_openai_openai_v1",
			Client:                    &http.Client{Transport: transport},
			Model:                     model,
			Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
			EndpointMode:              modelcheckprofile.EndpointModeResponsesJSON,
			Tokenizer:                 deterministicTokenizer{},
			ModelLimits:               deterministicLimits{},
		}
	}
	ctx := context.Background()
	targetBase := newSuite(&w14fStageTransport{}, "w14f-sol")
	// 对比端 probeMode 失败（685）。
	badMode := newSuite(&w14fStageTransport{}, "w14f-terra")
	badMode.EndpointMode = "chat_json"
	if _, err := RunTrustedComparison(ctx, targetBase, badMode, time.Second); err == nil {
		t.Fatalf("对比端模式校验应失败")
	}

	// target basic 请求失败（741）：非法 endpoint 触发 buildURL 错误。
	targetFail := newSuite(&w14fStageTransport{}, "w14f-sol")
	targetFail.Endpoint = "://w14f-bad-target"
	if _, err := RunTrustedComparison(ctx, targetFail, newSuite(&w14fStageTransport{}, "w14f-terra"), time.Second); err == nil {
		t.Fatalf("target basic 失败应报错")
	}

	// comparison cross 请求失败（745）：对比端内部套件先用掉第 1 次 CROSS 请求，
	// RunTrustedComparison 744 行的对比 basic 是第 2 次 → 从第 2 次起 500 +
	// 重试边界报错。
	comparisonCrossFail := newSuite(&w14fStageTransport{crossFailFrom: 1}, "w14f-terra")
	comparisonCrossFail.Profile = "full"
	comparisonCrossFail.Retry = RetryOptions{AttemptTimeouts: []time.Duration{time.Second, 0}}
	if _, err := RunTrustedComparison(ctx, targetBase, comparisonCrossFail, 30*time.Second); err == nil {
		t.Fatalf("comparison cross 失败应报错")
	}

	// 对比端内部套件失败（705）→ RunTrustedComparison 报错（290）。
	fullTarget := newSuite(&w14fStageTransport{}, "w14f-sol")
	fullTarget.Profile = "full"
	brokenComparison := newSuite(&w14fStageTransport{}, "w14f-terra")
	brokenComparison.Endpoint = "://w14f-bad-comparison"
	if _, err := RunTrustedComparison(ctx, fullTarget, brokenComparison, 30*time.Second); err == nil {
		t.Fatalf("对比端内部套件失败应报错")
	}

	// 完整成功 + 非 Full profile → 尾部 return（788）。
	items, err := RunTrustedComparison(ctx, targetBase, newSuite(&w14fStageTransport{}, "w14f-terra"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatalf("可信对比应返回 items")
	}
}
