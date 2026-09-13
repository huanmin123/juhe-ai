package modelcheckprobe

import (
	"strings"
	"testing"
)

// ---- 文本与数值辅助 ----

func TestBoundedTextVariants(t *testing.T) {
	if got := boundedText("  x  ", 10); got != "x" {
		t.Fatalf("应裁剪空白: %q", got)
	}
	if got := boundedText("abc", 0); got != "" {
		t.Fatalf("非正上限应返回空: %q", got)
	}
	if got := boundedText("中文内容", 2); len([]rune(got)) != 2 {
		t.Fatalf("应按 rune 截断: %q", got)
	}
	if got := boundedText("short", 10); got != "short" {
		t.Fatalf("未超限应原样: %q", got)
	}
}

func TestRatioBoolMinHelpers(t *testing.T) {
	if got := ratioInt(1, 0); got != 0 {
		t.Fatalf("分母为 0 应返回 0: %v", got)
	}
	if got := ratioInt(1, 4); got != 0.25 {
		t.Fatalf("比例应正确: %v", got)
	}
	if boolFloat(true) != 1 || boolFloat(false) != 0 {
		t.Fatalf("布尔转浮点不符")
	}
	if minFloat(1, 2) != 1 || minFloat(3, 2) != 2 {
		t.Fatalf("最小值不符")
	}
}

func TestSuiteComparableAndItemStatus(t *testing.T) {
	if suiteComparable(0, 0, nil, "quick") {
		t.Fatalf("满分为 0 不可比较")
	}
	if suiteComparable(3, 5, nil, "quick") {
		t.Fatalf("未满分不可比较")
	}
	if !suiteComparable(5, 5, nil, "quick") {
		t.Fatalf("quick 满分即可比较")
	}
	passed := &EvaluationItem{Status: "passed"}
	failed := &EvaluationItem{Status: "failed"}
	if !suiteComparable(5, 5, passed, "full") {
		t.Fatalf("full 且行为通过应可比较")
	}
	if suiteComparable(5, 5, failed, "full") {
		t.Fatalf("full 且行为失败不可比较")
	}
	if suiteComparable(5, 5, nil, "full") {
		t.Fatalf("full 缺行为评估不可比较")
	}
	if got := itemStatus(nil); got != "" {
		t.Fatalf("nil 项应返回空状态: %q", got)
	}
	if got := itemStatus(passed); got != "passed" {
		t.Fatalf("应返回项状态: %s", got)
	}
}

// ---- 结构化输出评分 ----

func TestStructuredScoreAndStatus(t *testing.T) {
	mismatch := modelEvidence{ModelMismatch: true, ResponseModel: "other"}
	if got := structuredScore(false, mismatch, false); got != 0 {
		t.Fatalf("模型不一致且失败应为 0: %d", got)
	}
	if got := structuredScore(true, mismatch, true); got != 5 {
		t.Fatalf("模型不一致但成功应为 4+1: %d", got)
	}
	if structuredStatus(mismatch, 5) != "failed" {
		t.Fatalf("模型不一致状态应为 failed")
	}
	normal := modelEvidence{MatchedModel: true}
	if got := structuredScore(true, normal, true); got != 15 {
		t.Fatalf("全成功应为 8+3+4: %d", got)
	}
	if structuredStatus(normal, 15) != "passed" {
		t.Fatalf("15 分应为 passed")
	}
	if structuredStatus(normal, 12) != "warning" {
		t.Fatalf("8-12 分应为 warning")
	}
	if structuredStatus(normal, 7) != "failed" {
		t.Fatalf("低于 8 分应为 failed")
	}
}

// ---- 函数调用参数提取 ----

func TestFunctionCallArgumentsVariants(t *testing.T) {
	payload := map[string]any{
		"output": []any{map[string]any{"type": "function_call", "name": "get_weather", "arguments": map[string]any{"city": "hangzhou"}}},
		"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "get_weather", "arguments": `{"city":"beijing"}`}},
		}}}},
		"content": []any{map[string]any{"type": "tool_use", "name": "get_weather", "input": map[string]any{"city": "shanghai"}}},
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"functionCall": map[string]any{"name": "get_weather", "args": map[string]any{"city": "shenzhen"}}},
		}}}},
	}
	matches := functionCallArguments(payload, "get_weather")
	if len(matches) != 4 {
		t.Fatalf("四种协议各应命中一次: %d", len(matches))
	}
	if matches[0]["city"] != "hangzhou" || matches[2]["city"] != "shanghai" || matches[3]["city"] != "shenzhen" {
		t.Fatalf("对象参数应原样提取: %#v", matches)
	}
	// choices 文本参数应解析出内嵌 JSON。
	if matches[1]["city"] != "beijing" {
		t.Fatalf("文本参数应解析 JSON: %#v", matches[1])
	}
	if got := functionCallArguments(payload, "other_tool"); len(got) != 0 {
		t.Fatalf("不匹配名称应返回空: %#v", got)
	}
}

func TestArgumentRecordVariants(t *testing.T) {
	if got := argumentRecord(map[string]any{"a": 1}); got["a"] != 1 {
		t.Fatalf("对象应原样返回: %#v", got)
	}
	if got := argumentRecord(`{"b":2}`); got["b"] != float64(2) {
		t.Fatalf("JSON 文本应解析: %#v", got)
	}
	if got := argumentRecord("prefix {\"c\":3} suffix"); got["c"] != float64(3) {
		t.Fatalf("应提取首段 JSON: %#v", got)
	}
	if got := argumentRecord("not-json"); got != nil {
		t.Fatalf("无法解析应返回 nil: %#v", got)
	}
	if got := argumentRecord(""); got != nil {
		t.Fatalf("空文本应返回 nil: %#v", got)
	}
}

func TestIsDatePrefixVariants(t *testing.T) {
	for _, ok := range []string{"2026-01-02", "2026-01-02.1", "2026-01-02_3", "2026-01-02-4"} {
		if !isDatePrefix(ok) {
			t.Fatalf("%q 应为日期前缀", ok)
		}
	}
	for _, bad := range []string{"2026-1-02", "2026/01/02", "202a-01-02", "20260102", "2026-01-0a"} {
		if isDatePrefix(bad) {
			t.Fatalf("%q 不应为日期前缀", bad)
		}
	}
}

func TestModelMatchesDateSuffixedVariants(t *testing.T) {
	if !modelMatches("gpt-5.6-sol-2026-01-02", "gpt-5.6-sol") {
		t.Fatalf("日期后缀模型应匹配")
	}
	if !modelMatches("  same  ", "same") {
		t.Fatalf("空白应裁剪后比较")
	}
	if modelMatches("", "x") || modelMatches("x", "") {
		t.Fatalf("任一侧为空不应匹配")
	}
	if modelMatches("other", "expected") {
		t.Fatalf("不同模型不应匹配")
	}
	if modelMatches("gpt-5.6-solxx", "gpt-5.6-sol") {
		t.Fatalf("非日期后缀不应匹配")
	}
}

func TestBoundedTextTruncationOfLongCJK(t *testing.T) {
	value := strings.Repeat("中", 100)
	if got := boundedText(value, 3); got != "中中中" {
		t.Fatalf("CJK 截断不符: %q", got)
	}
}
