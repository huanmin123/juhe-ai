package gatewaybody

// w11b 波次：JSON 内容类型大小写折叠、reasoning effort 三级回退、图像
// 工具降级（tool_choice 嵌套/全删键）与工具对象扫描边界。

import (
	"math"
	"testing"
)

func TestW11BContainsFoldAndReasoningEffort(t *testing.T) {
	// ascii 大小写折叠。
	if !IsJSONContentType("Application/JSON; charset=utf-8") {
		t.Fatal("大小写不敏感命中")
	}
	if IsJSONContentType("text/plain") {
		t.Fatal("非 JSON 不命中")
	}
	if IsJSONContentType("") {
		t.Fatal("空串不命中")
	}
	// reasoning effort 三级回退。
	nested := parsedReasoningEffort(map[string]any{"reasoning": map[string]any{"effort": "medium"}})
	if nested == nil || *nested != "medium" {
		t.Fatalf("nested = %v", nested)
	}
	flat := parsedReasoningEffort(map[string]any{"reasoning_effort": "low"})
	if flat == nil || *flat != "low" {
		t.Fatalf("flat = %v", flat)
	}
	config := parsedReasoningEffort(map[string]any{"output_config": map[string]any{"effort": "high"}})
	if config == nil || *config != "high" {
		t.Fatalf("config = %v", config)
	}
	if parsedReasoningEffort(map[string]any{}) != nil {
		t.Fatal("无 effort 为 nil")
	}
	if parsedReasoningEffort(map[string]any{"reasoning": "not-object"}) != nil {
		t.Fatal("reasoning 非对象为 nil")
	}
	if parsedReasoningEffort(map[string]any{"reasoning": map[string]any{"effort": "bogus"}, "reasoning_effort": "minimal"}) == nil {
		t.Fatal("非法嵌套回退平铺字段")
	}
}

func TestW11BDowngradeAutoImageGenerationToolsBranches(t *testing.T) {
	// nil body。
	if _, body := DowngradeAutoImageGenerationToolsInBody(nil); body != nil {
		t.Fatal("nil body 返回 nil")
	}
	// 强制图像工具（tool_choice 指定）不降级。
	forced := map[string]any{
		"tools":       []any{map[string]any{"type": "image_generation"}},
		"tool_choice": map[string]any{"type": "image_generation"},
	}
	result, body := DowngradeAutoImageGenerationToolsInBody(forced)
	if result.Downgraded || body != nil {
		t.Fatalf("forced = %+v", result)
	}
	// 无图像工具。
	result, _ = DowngradeAutoImageGenerationToolsInBody(map[string]any{"tools": []any{map[string]any{"type": "function"}}})
	if result.Reason != DowngradeReasonNoAutoImageGenerationTool {
		t.Fatalf("no image = %+v", result)
	}
	// 图像工具全删：tools 键被移除。
	allImage := map[string]any{"tools": []any{map[string]any{"type": "image_generation"}}}
	result, next := DowngradeAutoImageGenerationToolsInBody(allImage)
	if !result.Downgraded || result.RemovedToolCount != 1 {
		t.Fatalf("all image = %+v", result)
	}
	if _, exists := next["tools"]; exists {
		t.Fatal("全删后 tools 键应移除")
	}
	// 混合：保留非图像工具；tool_choice.tools 嵌套同步降级。
	mixed := map[string]any{
		"tools": []any{map[string]any{"type": "image_generation"}, map[string]any{"type": "function"}},
		"tool_choice": map[string]any{
			"type":  "allowed_tools",
			"tools": []any{map[string]any{"type": "image_generation"}, map[string]any{"type": "function"}},
		},
	}
	result, next = DowngradeAutoImageGenerationToolsInBody(mixed)
	if !result.Downgraded || result.RemovedToolCount != 2 {
		t.Fatalf("mixed = %+v", result)
	}
	tools := next["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	choice := next["tool_choice"].(map[string]any)
	choiceTools := choice["tools"].([]any)
	if len(choiceTools) != 1 {
		t.Fatalf("choice tools = %+v", choiceTools)
	}
	// tools 非 JSON 时 no-op。
	if got := removeImageGenerationToolArray(map[string]any{"tools": "not-array"}, "tools"); got != 0 {
		t.Fatalf("non-array = %d", got)
	}
}

func TestW11BInspectJSONToolObjectEdges(t *testing.T) {
	// type 非字符串值 → 计数跳过。
	inspection := InspectImageGenerationTools(map[string]any{
		"tools": []any{map[string]any{"type": 42}},
	})
	if inspection.ImageToolCount != 0 {
		t.Fatalf("inspection = %+v", inspection)
	}
	// tool_choice required + 仅图像工具 → ForcedImageGeneration。
	required := InspectImageGenerationTools(map[string]any{
		"tools":       []any{"image_generation"},
		"tool_choice": "required",
	})
	if !required.ForcedImageGeneration {
		t.Fatalf("required = %+v", required)
	}
	// 深度超限不递归。
	deep := InspectImageGenerationTools(map[string]any{
		"tools": []any{[]any{[]any{[]any{[]any{"image_generation"}}}}},
	})
	if deep.ImageToolCount != 0 {
		t.Fatalf("deep = %+v", deep)
	}
}

func TestW11BCreateBodyStateExplicitInputs(t *testing.T) {
	if IsJSONContentType("application/jzon") {
		t.Fatal("前缀相似但不同字符必须不匹配")
	}
	model := "gpt-5"
	stream := true
	tier := "priority"
	effort := "high"
	maxTokens := 1000
	format := "json_object"
	image := true
	state := CreateBodyState(BodyStateInput{
		RawBody: []byte("{}"), ContentType: "application/json",
		Model: &model, Stream: &stream, ServiceTier: &tier,
		ReasoningEffort: &effort, MaxOutputTokens: &maxTokens,
		ResponseFormat: &format, ImageGeneration: &image,
	})
	if state.Model == nil || *state.Model != "gpt-5" || state.Stream == nil || !*state.Stream {
		t.Fatalf("model/stream = %+v", state)
	}
	if state.ServiceTier != "priority" || state.ReasoningEffort == nil || *state.ReasoningEffort != "high" {
		t.Fatalf("tier/effort = %+v", state)
	}
	if state.MaxOutputTokens == nil || *state.MaxOutputTokens != 1000 || state.ResponseFormat == nil || *state.ResponseFormat != "json_object" {
		t.Fatalf("tokens/format = %+v", state)
	}
	if !state.ImageGeneration {
		t.Fatalf("image = %+v", state)
	}
}

func TestW11BStringImageToolNotRemovable(t *testing.T) {
	// 嵌套数组里的 image_generation 计入检查但顶层数组无工具可删 → no-op。
	body := map[string]any{"tools": []any{[]any{"image_generation"}}}
	result, next := DowngradeAutoImageGenerationToolsInBody(body)
	if result.Downgraded || next != nil || result.Reason != DowngradeReasonNoAutoImageGenerationTool {
		t.Fatalf("result = %+v next = %v", result, next)
	}
	// 无可删项的数组路径。
	if got := removeImageGenerationToolArray(map[string]any{"tools": []any{map[string]any{"type": "function"}}}, "tools"); got != 0 {
		t.Fatalf("no removal = %d", got)
	}
}

func TestW11BClassifyNilParserError(t *testing.T) {
	response := ClassifyRawBodyParserError(nil)
	if response.StatusCode != 400 || response.Message != "网关请求体无效" {
		t.Fatalf("response = %+v", response)
	}
}

func TestW11BMultipartGuards(t *testing.T) {
	if err := forEachMultipartPart([]byte("{}"), "application/json", 1024, nil); err == nil {
		t.Fatal("非 multipart 应报错")
	}
	if err := forEachMultipartPart([]byte("{}"), "multipart/form-data", 1024, nil); err == nil {
		t.Fatal("缺 boundary 应报错")
	}
}

func TestW11BSerializeBodyInvalidFloatPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("不可编码 body 必须按契约 panic")
		}
	}()
	_ = SerializeGatewayJSONObject(map[string]any{"x": math.NaN()})
}
