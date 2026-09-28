package chat

// w16c（批次1-C）回归：REST 读取投影补齐（order/item/blockId/mimeType/
// width/height/revisedPrompt）与生成参数 step 透出。写入侧 assistantBlock
// 早已持久化这些字段，此处锁定"读回不丢"的契约。

import (
	"testing"
)

// TestW16CContentBlockProjectionRoundTrip 写入侧持久化的全量字段经
// parseContentBlocks 读取后逐字段恢复（前端按 order 过滤 output_text、
// tool_call 关键词/参数/失败原因、output_image 元信息刷新后不再消失）。
func TestW16CContentBlockProjectionRoundTrip(t *testing.T) {
	stored := `[
		{"type":"output_text","blockId":"blk_txt","order":2,"text":"hi"},
		{"type":"reasoning","blockId":"blk_rsn","order":1,"text":"think","status":"completed"},
		{"type":"tool_call","blockId":"blk_tool","order":3,"callId":"call_1","toolType":"web_search","status":"completed","item":{"query":"kw","arguments":{"q":"x"},"failure":"none"}},
		{"type":"output_image","blockId":"blk_img","order":4,"assetId":"asset_1","status":"completed","mimeType":"image/png","width":1024,"height":768,"revisedPrompt":"a cat"}
	]`
	blocks := parseContentBlocks(stored)
	if len(blocks) != 4 {
		t.Fatalf("块数 = %d (%+v)", len(blocks), blocks)
	}
	// output_text：order 恢复（前端 IndexedDB 按 order 过滤）。
	txt := blocks[0]
	if txt.BlockID != "blk_txt" || txt.Order == nil || *txt.Order != 2 || txt.Text == nil || *txt.Text != "hi" {
		t.Fatalf("output_text 投影 = %+v", txt)
	}
	// reasoning：order 恢复。
	rsn := blocks[1]
	if rsn.Order == nil || *rsn.Order != 1 || rsn.Status == nil || *rsn.Status != "completed" {
		t.Fatalf("reasoning 投影 = %+v", rsn)
	}
	// tool_call：item/blockId/order 恢复。
	tool := blocks[2]
	if tool.BlockID != "blk_tool" || tool.Order == nil || *tool.Order != 3 || tool.CallID == nil || *tool.CallID != "call_1" || tool.ToolType == nil || *tool.ToolType != "web_search" {
		t.Fatalf("tool_call 基础投影 = %+v", tool)
	}
	if tool.Item == nil || tool.Item["query"] != "kw" || tool.Item["failure"] != "none" {
		t.Fatalf("tool_call item 投影 = %+v", tool.Item)
	}
	args, _ := tool.Item["arguments"].(map[string]any)
	if args == nil || args["q"] != "x" {
		t.Fatalf("tool_call item 嵌套投影 = %+v", tool.Item)
	}
	// output_image：mimeType/width/height/revisedPrompt 恢复。
	img := blocks[3]
	if img.MimeType == nil || *img.MimeType != "image/png" {
		t.Fatalf("output_image mimeType = %+v", img)
	}
	if img.Width == nil || *img.Width != 1024 || img.Height == nil || *img.Height != 768 {
		t.Fatalf("output_image 尺寸 = %+v", img)
	}
	if img.RevisedPrompt == nil || *img.RevisedPrompt != "a cat" {
		t.Fatalf("output_image revisedPrompt = %+v", img)
	}
	// 恢复后的 DTO 再序列化/读取一轮，新字段仍存活（往返闭合）。
	again := parseContentBlocks(mustJSON(blocks))
	if len(again) != 4 {
		t.Fatalf("再往返块数 = %d (%+v)", len(again), again)
	}
	if again[3].RevisedPrompt == nil || *again[3].RevisedPrompt != "a cat" || again[3].Width == nil || *again[3].Width != 1024 {
		t.Fatalf("再往返 output_image = %+v", again[3])
	}
	if again[2].Item == nil || again[2].Item["query"] != "kw" || again[2].BlockID != "blk_tool" {
		t.Fatalf("再往返 tool_call = %+v", again[2])
	}
}

// TestW16CContentBlockProjectionLegacyCompat 旧格式（无新键）照常读取，
// 未恢复的字段保持缺省，不炸、不丢块。
func TestW16CContentBlockProjectionLegacyCompat(t *testing.T) {
	legacy := `[
		{"type":"output_text","text":"old"},
		{"type":"reasoning","text":"old","status":"started"},
		{"type":"tool_call","callId":"c","toolType":"search","status":"completed"},
		{"type":"output_image","blockId":"b","order":0,"assetId":"a","status":"completed"}
	]`
	blocks := parseContentBlocks(legacy)
	if len(blocks) != 4 {
		t.Fatalf("旧格式块数 = %d (%+v)", len(blocks), blocks)
	}
	// output_text/reasoning/tool_call 的 order 是本批次新增的读取字段：
	// 旧数据无键时不得恢复。output_image 的 order 是既有必填字段（=0），
	// 不在此列。
	for _, index := range []int{0, 1, 2} {
		if blocks[index].Order != nil {
			t.Fatalf("旧格式块 %d 不应恢复 order = %+v", index, blocks[index])
		}
	}
	if blocks[0].BlockID != "" {
		t.Fatalf("旧格式 output_text = %+v", blocks[0])
	}
	if blocks[2].Item != nil || blocks[2].BlockID != "" {
		t.Fatalf("旧格式 tool_call = %+v", blocks[2])
	}
	if blocks[3].MimeType != nil || blocks[3].Width != nil || blocks[3].Height != nil || blocks[3].RevisedPrompt != nil {
		t.Fatalf("旧格式 output_image = %+v", blocks[3])
	}
}

// TestW16CContentBlockProjectionMalformedMetadata 新键存在但类型/取值非法时
// 只省略该字段，不得丢弃整块（与 parseContentBlocks 的降级风格一致）。
func TestW16CContentBlockProjectionMalformedMetadata(t *testing.T) {
	stored := `[
		{"type":"output_text","text":"t","order":"x"},
		{"type":"tool_call","callId":"c","toolType":"s","status":"completed","item":"not-object","order":1.5},
		{"type":"output_image","blockId":"b","order":0,"assetId":"a","status":"completed","width":12.5,"height":"x","mimeType":7,"revisedPrompt":9}
	]`
	blocks := parseContentBlocks(stored)
	if len(blocks) != 3 {
		t.Fatalf("非法元数据不得丢块 = %+v", blocks)
	}
	if blocks[0].Order != nil {
		t.Fatalf("非法 order 应省略 = %+v", blocks[0])
	}
	if blocks[1].Item != nil || blocks[1].Order != nil {
		t.Fatalf("非法 item/order 应省略 = %+v", blocks[1])
	}
	if blocks[2].Width != nil || blocks[2].Height != nil || blocks[2].MimeType != nil || blocks[2].RevisedPrompt != nil {
		t.Fatalf("非法尺寸/mime/revisedPrompt 应省略 = %+v", blocks[2])
	}
}

// TestW16CGenerationParameterStepPayload 能力定义表与载荷都携带 step，数值
// 与 Node 定义一致（前端 capability.step 是必填滑块步长）。
func TestW16CGenerationParameterStepPayload(t *testing.T) {
	capabilities := generationParameterCapabilitiesForModel("gpt", "gpt-4o", nil)[generationProtocolChatCompletions]
	expected := map[string]float64{
		"temperature": 0.1, "topP": 0.05, "frequencyPenalty": 0.1,
		"presencePenalty": 0.1, "maxOutputTokens": 1, "seed": 1,
	}
	if len(capabilities) != len(expected) {
		t.Fatalf("gpt-4o chat_completions 参数数 = %d (%+v)", len(capabilities), capabilities)
	}
	byParameter := map[string]ChatGenerationParameterCapability{}
	for _, capability := range capabilities {
		byParameter[capability.Parameter] = capability
	}
	for parameter, step := range expected {
		if got := byParameter[parameter].Step; got != step {
			t.Fatalf("%s step = %v，期望 %v", parameter, got, step)
		}
	}
	// 载荷结构透出 step（parameter/min/max/step/defaultValue）。
	payload := generationParametersPayload(capabilities)
	if len(payload) != len(expected) {
		t.Fatalf("载荷条目数 = %d (%+v)", len(payload), payload)
	}
	seenStep := map[string]float64{}
	for _, entry := range payload {
		rendered, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("载荷条目 = %+v", entry)
		}
		parameter, _ := rendered["parameter"].(string)
		step, _ := rendered["step"].(float64)
		seenStep[parameter] = step
	}
	for parameter, step := range expected {
		if seenStep[parameter] != step {
			t.Fatalf("载荷 %s step = %v，期望 %v", parameter, seenStep[parameter], step)
		}
	}
	// glm 分支手改 Min/Max 不丢定义表的 step。
	glm := generationParameterCapabilitiesForModel("glm", "glm-4.7", nil)[generationProtocolChatCompletions]
	if len(glm) != 3 || glm[0].Step != 0.1 || glm[1].Step != 0.05 || glm[2].Step != 1 {
		t.Fatalf("glm step = %+v", glm)
	}
}
