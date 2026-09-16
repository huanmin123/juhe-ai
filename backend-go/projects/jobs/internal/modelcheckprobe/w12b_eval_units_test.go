package modelcheckprobe

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// w12b_eval_units_test.go 以构造 ProbeResult/ EvaluationItem 夹具直接覆盖
// 评估阶梯、决策阶梯与比较臂的分支：EvaluateBasicProtocol/EvaluateProtocolStream
// 的评分与状态矩阵、EvaluateStructuredOutput/EvaluateToolCalling/UsageShape、
// mismatchMessage 文案、SummarizeChecks 全阶梯、EvaluateTrustedComparison 全臂、
// request.go 构建校验。无网络依赖，结果稳定可回放。

func w12bSuccessResult(output, model string) ProbeResult {
	return ProbeResult{HTTPStatusCode: 200, Success: true, RequestModel: "m1", ExpectedModel: "m1",
		Response: ParsedResponse{Model: model, OutputText: output}}
}

func TestW12bEvaluateBasicProtocolMatrix(t *testing.T) {
	cases := []struct {
		name   string
		result ProbeResult
		status string
		score  int
	}{
		{"请求失败无错误消息", ProbeResult{HTTPStatusCode: 502}, "skipped", 0},
		{"全部匹配", w12bSuccessResult("OK-MODEL-CHECK", "m1"), "passed", 10},
		{"输出匹配模型缺失", w12bSuccessResult("OK-MODEL-CHECK", ""), "warning", 7},
		{"模型匹配输出不符", w12bSuccessResult("其他输出", "m1"), "warning", 3},
		{"模型不匹配输出匹配", w12bSuccessResult("OK-MODEL-CHECK", "m2"), "failed", 1},
		{"模型不匹配输出不符", w12bSuccessResult("其他", "m2"), "failed", 0},
	}
	for _, tc := range cases {
		item := EvaluateBasicProtocol(tc.result, "m1", ProtocolEvaluationOptions{ItemKey: "t.basic", ItemType: "basic", FailurePrefix: "失败"})
		if item.Status != tc.status || item.Score != tc.score {
			t.Fatalf("%s: status=%s score=%d want=%s/%d", tc.name, item.Status, item.Score, tc.status, tc.score)
		}
	}
	// 流式包装与失败消息优先级。
	stream := EvaluateStream(ProbeResult{HTTPStatusCode: 503, Response: ParsedResponse{ErrorMessage: "上游故障"}}, "m1", "t")
	if stream.Status != "skipped" || stream.ErrorMessage != "上游故障" {
		t.Fatalf("流式失败项不符: %+v", stream)
	}
	// 带首 token 时间的流式失败。
	first := int64(12)
	stream = EvaluateProtocolStream(ProbeResult{HTTPStatusCode: 500, FirstTokenMS: &first}, "m1", ProtocolEvaluationOptions{ItemKey: "t.s", ItemType: "s", FailurePrefix: "失败"})
	if stream.Evidence["firstTokenMs"] != first {
		t.Fatalf("firstTokenMs 证据缺失: %#v", stream.Evidence)
	}
}

func TestW12bEvaluateStreamScoreMatrix(t *testing.T) {
	cases := []struct {
		name   string
		result ProbeResult
		status string
		score  int
	}{
		{"全部匹配", w12bSuccessResult("STREAM-OK", "m1"), "passed", 15},
		{"模型匹配输出不符", w12bSuccessResult("其他", "m1"), "warning", 11},
		{"模型缺失输出匹配", w12bSuccessResult("STREAM-OK", ""), "warning", 12},
		{"模型缺失输出不符", w12bSuccessResult("其他", ""), "warning", 8},
		{"模型不匹配输出匹配", w12bSuccessResult("STREAM-OK", "m2"), "failed", 5},
		{"模型不匹配输出不符", w12bSuccessResult("其他", "m2"), "failed", 4},
	}
	for _, tc := range cases {
		item := EvaluateProtocolStream(tc.result, "m1", ProtocolEvaluationOptions{ItemKey: "t.s", ItemType: "s", SuccessMessage: "ok", FailurePrefix: "失败"})
		if item.Status != tc.status || item.Score != tc.score {
			t.Fatalf("%s: status=%s score=%d want=%s/%d", tc.name, item.Status, item.Score, tc.status, tc.score)
		}
	}
}

func TestW12bStructuredToolUsageArms(t *testing.T) {
	// 结构化：失败 / 非法 JSON / 合法。
	if item := EvaluateStructuredOutput(ProbeResult{HTTPStatusCode: 500}, "m1", "t"); item.Status != "skipped" {
		t.Fatalf("结构化失败应 skipped: %+v", item)
	}
	if item := EvaluateStructuredOutput(w12bSuccessResult("不是 json", "m1"), "m1", "t"); item.Status != "warning" {
		t.Fatalf("非法结构化应 warning（请求成功模型匹配）: %+v", item)
	}
	// 模型不匹配的非法结构化 → failed（score=0）。
	badModel := w12bSuccessResult("不是 json", "m2")
	if item := EvaluateStructuredOutput(badModel, "m1", "t"); item.Status != "failed" {
		t.Fatalf("不匹配且非法应 failed: %+v", item)
	}
	if item := EvaluateStructuredOutput(w12bSuccessResult(`{"status":"ok","value":7}`, "m1"), "m1", "t"); item.Status != "passed" {
		t.Fatalf("合法结构化应 passed: %+v", item)
	}
	// 工具：失败 / 未调用 / 响应与 chat 两种调用结构。
	if item := EvaluateToolCalling(ProbeResult{HTTPStatusCode: 500}, "m1", "t"); item.Status != "skipped" {
		t.Fatalf("工具失败应 skipped: %+v", item)
	}
	if item := EvaluateToolCalling(w12bSuccessResult("无工具", "m1"), "m1", "t"); item.Status != "warning" {
		t.Fatalf("未调用工具应 warning: %+v", item)
	}
	responsesCall := w12bSuccessResult("ok", "m1")
	responsesCall.Response.JSON = map[string]any{"output": []any{map[string]any{
		"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}
	if item := EvaluateToolCalling(responsesCall, "m1", "t"); item.Status != "passed" {
		t.Fatalf("responses 工具调用应 passed: %+v", item)
	}
	chatCall := w12bSuccessResult("ok", "m1")
	chatCall.Response.JSON = map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"tool_calls": []any{map[string]any{"function": map[string]any{
			"name": "record_model_check", "arguments": map[string]any{"code": "ok", "count": float64(1)}}}}}}}}
	if item := EvaluateToolCalling(chatCall, "m1", "t"); item.Status != "passed" {
		t.Fatalf("chat 工具调用应 passed: %+v", item)
	}
	anthropicCall := w12bSuccessResult("ok", "m1")
	anthropicCall.Response.JSON = map[string]any{"content": []any{map[string]any{
		"type": "tool_use", "name": "record_model_check", "input": map[string]any{"code": "ok", "count": float64(1)}}}}
	if item := EvaluateToolCalling(anthropicCall, "m1", "t"); item.Status != "passed" {
		t.Fatalf("anthropic 工具调用应 passed: %+v", item)
	}
	geminiCall := w12bSuccessResult("ok", "m1")
	geminiCall.Response.JSON = map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{
		"functionCall": map[string]any{"name": "record_model_check", "args": map[string]any{"code": "ok", "count": float64(1)}}}}}}}}
	if item := EvaluateToolCalling(geminiCall, "m1", "t"); item.Status != "passed" {
		t.Fatalf("gemini 工具调用应 passed: %+v", item)
	}

	// usage shape：无成功 / 成功但无 usage / 有 usage。
	if item := EvaluateUsageShape([]ProbeResult{{HTTPStatusCode: 500}}, "t"); item.Status != "skipped" || item.Evidence["requestFailureCount"] != 1 {
		t.Fatalf("usage 失败臂: %+v", item)
	}
	if item := EvaluateUsageShape([]ProbeResult{w12bSuccessResult("ok", "m1")}, "t"); item.Status != "skipped" || item.Evidence["evidenceInsufficient"] != true {
		t.Fatalf("usage 缺失臂: %+v", item)
	}
	withUsage := w12bSuccessResult("ok", "m1")
	withUsage.Response.Usage = map[string]any{"input_tokens": float64(3)}
	if item := EvaluateUsageShape([]ProbeResult{{HTTPStatusCode: 500}, withUsage}, "t"); item.Status != "passed" {
		t.Fatalf("usage 有效臂: %+v", item)
	}
}

func TestW12bMismatchMessageArms(t *testing.T) {
	mapping := true
	if got := buildModelEvidence(ProbeResult{RequestModel: "req", ModelMappingApplied: &mapping, Response: ParsedResponse{Model: "other"}}, "m1").mismatchMessage(); got == "" || !contains(got, "映射上游模型") {
		t.Fatalf("映射不一致文案不符: %q", got)
	}
	if got := buildModelEvidence(ProbeResult{Response: ParsedResponse{Model: "other"}}, "m1").mismatchMessage(); !contains(got, "与请求模型") {
		t.Fatalf("普通不一致文案不符: %q", got)
	}
	if got := buildModelEvidence(ProbeResult{RequestModel: "req", ModelMappingApplied: &mapping}, "m1").mismatchMessage(); got != "" {
		t.Fatalf("响应模型为空应无文案: %q", got)
	}
}

func contains(value, needle string) bool {
	return strings.Contains(value, needle)
}

func w12bCheck(key, itemType, status string, score, maxScore int, evidence map[string]any) EvaluationItem {
	return EvaluationItem{ItemKey: key, ItemType: itemType, Status: status, Score: score, MaxScore: maxScore, Evidence: evidence}
}

func TestW12bSummarizeChecksLadder(t *testing.T) {
	// 模型不一致最优先。
	mismatch := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "passed", 10, 10,
		map[string]any{"success": true, "modelMismatch": true})}
	if got := SummarizeChecks(mismatch, false, "quick"); got.Level != "suspicious" {
		t.Fatalf("模型不一致应 suspicious: %+v", got)
	}
	// juice 专项硬异常。
	juice := []EvaluationItem{
		w12bCheck("target.responses_basic", "responses_basic", "passed", 10, 10, map[string]any{"success": true}),
		w12bCheck("target.gpt56_juice", "gpt56_juice", "failed", 0, 10, map[string]any{"hardAnomaly": true}),
	}
	if got := SummarizeChecks(juice, false, "quick"); got.Level != "suspicious" {
		t.Fatalf("juice 硬异常应 suspicious: %+v", got)
	}
	// 基础探针不可用。
	unavailable := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "skipped", 0, 0, map[string]any{"success": false})}
	if got := SummarizeChecks(unavailable, false, "quick"); got.Level != "unavailable" {
		t.Fatalf("基础不可用应 unavailable: %+v", got)
	}
	// 长上下文各状态。
	longBase := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "passed", 10, 10, map[string]any{"success": true})}
	longFailed := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.long_context", "long_context", "failed", 0, 15, nil))
	if got := SummarizeChecks(longFailed, false, "full"); got.Level != "suspicious" {
		t.Fatalf("长上下文失败应 suspicious: %+v", got)
	}
	longWarnFailure := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.long_context", "long_context", "warning", 8, 15, map[string]any{"requestFailureCount": 1}))
	if got := SummarizeChecks(longWarnFailure, false, "full"); got.Level != "uncertain" || !contains(got.Message, "部分窗口请求失败") {
		t.Fatalf("长上下文部分失败应 uncertain: %+v", got)
	}
	longWarn := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.long_context", "long_context", "warning", 8, 15, nil))
	if got := SummarizeChecks(longWarn, false, "full"); !contains(got.Message, "重点排查") {
		t.Fatalf("长上下文警告应 uncertain: %+v", got)
	}
	longSkipped := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.long_context", "long_context", "skipped", 0, 0, nil))
	if got := SummarizeChecks(longSkipped, false, "full"); got.Level != "uncertain" {
		t.Fatalf("长上下文跳过应 uncertain: %+v", got)
	}
	// 行为/稳定性跳过。
	behaviorSkipped := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.behavior_probe", "behavior_probe", "skipped", 0, 0, nil))
	if got := SummarizeChecks(behaviorSkipped, false, "full"); !contains(got.Message, "部分关键探针") {
		t.Fatalf("行为跳过应 uncertain: %+v", got)
	}
	stabilitySkipped := append(append([]EvaluationItem{}, longBase...), w12bCheck("target.stability", "stability", "skipped", 0, 0, nil))
	if got := SummarizeChecks(stabilitySkipped, false, "full"); !contains(got.Message, "部分关键探针") {
		t.Fatalf("稳定性跳过应 uncertain: %+v", got)
	}
	// 行为警告带请求失败。
	behaviorWarn := append(append([]EvaluationItem{}, longBase...),
		w12bCheck("target.behavior_probe", "behavior_probe", "warning", 20, 35, map[string]any{"requestFailureCount": 1}))
	if got := SummarizeChecks(behaviorWarn, false, "full"); !contains(got.Message, "存在请求失败") {
		t.Fatalf("行为警告应 uncertain: %+v", got)
	}
	stabilityWarn := append(append([]EvaluationItem{}, longBase...),
		w12bCheck("target.stability", "stability", "warning", 9, 15, map[string]any{"requestFailureCount": 1}))
	if got := SummarizeChecks(stabilityWarn, false, "full"); !contains(got.Message, "存在请求失败") {
		t.Fatalf("稳定性警告应 uncertain: %+v", got)
	}
	// 可信对比跳过。
	trustedSkipped := append(append([]EvaluationItem{}, longBase...), w12bCheck("trusted_comparison.comparison", "trusted_comparison", "skipped", 0, 0, nil))
	if got := SummarizeChecks(trustedSkipped, true, "full"); !contains(got.Message, "可信对比探针请求失败") {
		t.Fatalf("可信对比跳过应 uncertain: %+v", got)
	}

	fullPass := []EvaluationItem{
		w12bCheck("target.responses_basic", "responses_basic", "passed", 10, 10, map[string]any{"success": true}),
		w12bCheck("target.behavior_probe", "behavior_probe", "passed", 35, 35, nil),
		w12bCheck("target.long_context", "long_context", "passed", 15, 15, nil),
		w12bCheck("target.stability", "stability", "passed", 15, 15, nil),
		w12bCheck("target.cross_model", "cross_model", "passed", 10, 10, nil),
	}
	if got := SummarizeChecks(fullPass, false, "full"); got.Level != "high_confidence" || !contains(got.Message, "辅助模型对照") {
		t.Fatalf("全通过应 high_confidence: %+v", got)
	}
	// 要求可信对比但缺失（无 cross_model 兜底）→ 只能 likely。
	noCross := fullPass[:4]
	if got := SummarizeChecks(noCross, true, "full"); got.Level != "likely" {
		t.Fatalf("要求可信对比但缺失应 not high_confidence: %+v", got)
	}
	trustedPassed := append(append([]EvaluationItem{}, fullPass...), w12bCheck("trusted_comparison.comparison", "trusted_comparison", "passed", 10, 10, nil))
	if got := SummarizeChecks(trustedPassed, true, "full"); got.Level != "high_confidence" || !contains(got.Message, "可信对比均通过") {
		t.Fatalf("含可信对比全通过应 high_confidence: %+v", got)
	}

	likely := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "warning", 8, 10, map[string]any{"success": true})}
	if got := SummarizeChecks(likely, false, "full"); got.Level != "likely" {
		t.Fatalf("较可信应 likely: %+v", got)
	}
	if got := SummarizeChecks(likely, false, "quick"); got.Level != "likely" {
		t.Fatalf("quick 较可信应 likely: %+v", got)
	}
	uncertain := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "warning", 6, 10, map[string]any{"success": true})}
	if got := SummarizeChecks(uncertain, false, "full"); got.Level != "uncertain" {
		t.Fatalf("不确定应 uncertain: %+v", got)
	}
	if got := SummarizeChecks(uncertain, false, "quick"); got.Level != "uncertain" {
		t.Fatalf("quick 不确定应 uncertain: %+v", got)
	}
	suspicious := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "warning", 3, 10, map[string]any{"success": true})}
	if got := SummarizeChecks(suspicious, false, "full"); got.Level != "suspicious" {
		t.Fatalf("疑似应 suspicious: %+v", got)
	}
	if got := SummarizeChecks(suspicious, false, "quick"); got.Level != "suspicious" {
		t.Fatalf("quick 疑似应 suspicious: %+v", got)
	}
	failed := []EvaluationItem{w12bCheck("target.responses_basic", "responses_basic", "warning", 2, 10, map[string]any{"success": true})}
	if got := SummarizeChecks(failed, false, "full"); !contains(got.Message, "HTTP 200 质量校验未通过") {
		t.Fatalf("多校验未通过应 suspicious: %+v", got)
	}
	// 非 target 前缀项不参与计分：quick 下无有效分应走快速失败臂。
	if got := SummarizeChecks([]EvaluationItem{w12bCheck("other.item", "x", "failed", 0, 10, nil)}, false, "quick"); got.Message != "快速检测多个 HTTP 200 质量校验未通过，目标链路疑似不符" {
		t.Fatalf("无有效计分项应走 quick 快速失败臂: %+v", got)
	}
}

func TestW12bTrustedComparisonArms(t *testing.T) {
	pass := func(prefix string) []EvaluationItem {
		return []EvaluationItem{
			w12bCheck(prefix+".responses_basic", "responses_basic", "passed", 10, 10, map[string]any{"success": true}),
			w12bCheck(prefix+".behavior_probe", "behavior_probe", "passed", 35, 35, nil),
		}
	}
	// 双方满分且行为通过 → passed 10。
	item := EvaluateTrustedComparison(pass("target"), pass("trusted_comparison"), "quick")
	if item.Status != "passed" || item.Score != 10 {
		t.Fatalf("可比应 passed: %+v", item)
	}
	// 目标缺基础项 → skipped。
	if item := EvaluateTrustedComparison(nil, pass("trusted_comparison"), "quick"); item.Status != "skipped" || item.Evidence["targetBasicSuccess"] != false {
		t.Fatalf("目标缺基础应 skipped: %+v", item)
	}
	// 对比侧行为缺失（full profile）→ skipped。
	if item := EvaluateTrustedComparison(pass("target"), []EvaluationItem{w12bCheck("trusted_comparison.responses_basic", "responses_basic", "passed", 10, 10, map[string]any{"success": true})}, "full"); item.Status != "skipped" {
		t.Fatalf("full 缺对比行为应 skipped: %+v", item)
	}
	// 含 skipped 计分项 → 失败。
	target := append(pass("target"), w12bCheck("target.structured_output", "structured_output", "skipped", 0, 15, nil))
	if item := EvaluateTrustedComparison(target, pass("trusted_comparison"), "quick"); item.Status != "skipped" {
		t.Fatalf("目标 skipped 计分项应 skipped: %+v", item)
	}
	// 对比侧模型不匹配 → failed + 专属文案。
	mismatched := []EvaluationItem{
		w12bCheck("trusted_comparison.responses_basic", "responses_basic", "failed", 1, 10, map[string]any{"success": true, "modelMismatch": true}),
		w12bCheck("trusted_comparison.behavior_probe", "behavior_probe", "passed", 35, 35, nil),
	}
	if item := EvaluateTrustedComparison(pass("target"), mismatched, "quick"); !contains(item.Evidence["message"].(string), "不能作为可信对比基准") {
		t.Fatalf("对比侧不匹配文案不符: %+v", item)
	}
	// 目标侧模型不匹配 → failed + 专属文案。
	targetMismatch := []EvaluationItem{
		w12bCheck("target.responses_basic", "responses_basic", "failed", 1, 10, map[string]any{"success": true, "modelMismatch": true}),
		w12bCheck("target.behavior_probe", "behavior_probe", "passed", 35, 35, nil),
	}
	if item := EvaluateTrustedComparison(targetMismatch, pass("trusted_comparison"), "quick"); !contains(item.Evidence["message"].(string), "目标账户基础探针返回模型不匹配") {
		t.Fatalf("目标侧不匹配文案不符: %+v", item)
	}
	// 双方匹配但目标未满分、对比满分 → warning 4。
	partialTarget := []EvaluationItem{
		w12bCheck("target.responses_basic", "responses_basic", "warning", 8, 10, map[string]any{"success": true}),
		w12bCheck("target.behavior_probe", "behavior_probe", "passed", 35, 35, nil),
	}
	if item := EvaluateTrustedComparison(partialTarget, pass("trusted_comparison"), "quick"); item.Status != "warning" || item.Score != 4 {
		t.Fatalf("目标未满分应 warning 4: %+v", item)
	}
	// 无任何基础项：findBasicItem 空表返回 nil、hasSkippedScoredItem false 臂。
	if findBasicItem(nil) != nil || hasSkippedScoredItem(nil) {
		t.Fatal("空表臂不符")
	}
	if score, maxScore := suiteScore([]EvaluationItem{w12bCheck("x.cross_model", "cross_model", "passed", 10, 10, nil), w12bCheck("x.basic", "basic", "passed", 3, 5, nil)}); score != 3 || maxScore != 5 {
		t.Fatalf("suiteScore 应排除 cross_model: %d/%d", score, maxScore)
	}
	if hasSkippedScoredItem([]EvaluationItem{w12bCheck("x", "cross_model", "skipped", 0, 15, nil)}) {
		t.Fatal("cross_model skipped 不应算失败")
	}
}

func TestW12bRequestBuildersArms(t *testing.T) {
	// 非法输入与未知协议。
	if _, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, " ", "p", BasicOptions{MaxOutputTokens: 1}); err == nil {
		t.Fatal("空模型应报错")
	}
	if _, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "m", " ", BasicOptions{MaxOutputTokens: 1}); err == nil {
		t.Fatal("空提示应报错")
	}
	if _, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "m", "p", BasicOptions{}); err == nil {
		t.Fatal("非正 token 应报错")
	}
	if _, err := BuildBasic(modelcheckprofile.Protocol("w12b-unknown"), "m", "p", BasicOptions{MaxOutputTokens: 1}); err == nil {
		t.Fatal("未知协议应报错")
	}
	// 全协议 structured / tool 构建（含 anthropic 无 schema 增补臂与 gemini 流式路径）。
	protocols := []modelcheckprofile.Protocol{
		modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat,
		modelcheckprofile.ProtocolAnthropic, modelcheckprofile.ProtocolGeminiNative,
	}
	for _, protocol := range protocols {
		for _, stream := range []bool{false, true} {
			if _, err := BuildStructured(protocol, "m", stream); err != nil {
				t.Fatalf("structured %s stream=%v: %v", protocol, stream, err)
			}
			if _, err := BuildTool(protocol, "m", stream); err != nil {
				t.Fatalf("tool %s stream=%v: %v", protocol, stream, err)
			}
		}
	}
	if _, err := BuildStructured(modelcheckprofile.ProtocolAnthropic, "m", false); err != nil {
		t.Fatal(err)
	}
}
