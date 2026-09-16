package chat

// w10d 覆盖收尾：conversations.go 内容块映射/输入标记的纯函数分支。

import (
	"encoding/json"
	"testing"
)

func strp(s string) *string { return &s }

// TestW10DContentBlockFromMapBranches 覆盖 contentBlockFromMap 全部分支。
func TestW10DContentBlockFromMapBranches(t *testing.T) {
	// 未知类型。
	if _, ok := contentBlockFromMap(map[string]any{"type": "wat"}); ok {
		t.Fatal("未知类型应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{}); ok {
		t.Fatal("缺 type 应失败")
	}
	// output_text。
	if block, ok := contentBlockFromMap(map[string]any{"type": "output_text", "text": "hi"}); !ok || block.Text == nil || *block.Text != "hi" {
		t.Fatalf("output_text = %+v/%v", block, ok)
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_text"}); ok {
		t.Fatal("output_text 缺 text 应失败")
	}
	if block, ok := contentBlockFromMap(map[string]any{"type": "output_text", "text": "a", "blockId": "b1"}); !ok || block.BlockID != "b1" {
		t.Fatalf("output_text blockId = %+v", block)
	}
	// reasoning 状态。
	for _, st := range []string{"started", "completed", "failed", "canceled"} {
		if block, ok := contentBlockFromMap(map[string]any{"type": "reasoning", "text": "x", "status": st}); !ok || block.Status == nil || *block.Status != st {
			t.Fatalf("reasoning %s = %+v", st, block)
		}
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "reasoning", "text": "x", "status": "bogus"}); ok {
		t.Fatal("reasoning 非法状态应失败")
	}
	if block, ok := contentBlockFromMap(map[string]any{"type": "reasoning", "text": "x", "status": 7}); !ok || block.Status != nil {
		t.Fatalf("reasoning 非字符串状态 = %+v/%v", block, ok)
	}
	// input_text。
	if block, ok := contentBlockFromMap(map[string]any{"type": "input_text", "order": float64(3), "text": "t"}); !ok || block.Order == nil || *block.Order != 3 {
		t.Fatalf("input_text = %+v/%v", block, ok)
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "input_text", "order": float64(-1), "text": "t"}); ok {
		t.Fatal("input_text 负 order 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "input_text", "order": float64(maxInputContentBlocks), "text": "t"}); ok {
		t.Fatal("input_text 越界 order 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "input_text", "order": float64(3)}); ok {
		t.Fatal("input_text 缺 text 应失败")
	}
	// input_image。
	if block, ok := contentBlockFromMap(map[string]any{"type": "input_image", "order": float64(1), "assetId": " a "}); !ok || block.AssetID == nil || *block.AssetID != " a " {
		t.Fatalf("input_image = %+v/%v", block, ok)
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "input_image", "order": float64(1), "assetId": "   "}); ok {
		t.Fatal("input_image 空 assetId 应失败")
	}
	// output_image。
	if block, ok := contentBlockFromMap(map[string]any{"type": "output_image", "blockId": "b", "order": float64(0), "assetId": "a", "status": "started"}); !ok || block.BlockID != "b" {
		t.Fatalf("output_image = %+v/%v", block, ok)
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_image", "order": float64(0), "assetId": "a", "status": "started"}); ok {
		t.Fatal("output_image 缺 blockId 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_image", "blockId": "b", "order": float64(-1), "assetId": "a", "status": "started"}); ok {
		t.Fatal("output_image 负 order 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_image", "blockId": "b", "order": float64(0), "assetId": "", "status": "started"}); ok {
		t.Fatal("output_image 空 assetId 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_image", "blockId": "b", "order": float64(0), "assetId": "a"}); ok {
		t.Fatal("output_image 缺 status 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "output_image", "blockId": "b", "order": float64(0), "assetId": "a", "status": "nope"}); ok {
		t.Fatal("output_image 非法状态应失败")
	}
	// tool_call。
	if block, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "id": "i1", "callId": "c1", "toolType": "search", "status": "completed"}); !ok || block.ID == nil || *block.ID != "i1" || block.CallID == nil || *block.CallID != "c1" {
		t.Fatalf("tool_call = %+v/%v", block, ok)
	}
	if block, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "id": "i1", "toolType": "search", "status": "started"}); !ok || block.ID == nil || block.CallID != nil {
		t.Fatalf("tool_call 仅 id = %+v/%v", block, ok)
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "toolType": "search", "status": "started"}); ok {
		t.Fatal("tool_call 缺 id/callId 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "callId": "c", "status": "started"}); ok {
		t.Fatal("tool_call 缺 toolType 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "callId": "c", "toolType": "s"}); ok {
		t.Fatal("tool_call 缺 status 应失败")
	}
	if _, ok := contentBlockFromMap(map[string]any{"type": "tool_call", "callId": "c", "toolType": "s", "status": "zz"}); ok {
		t.Fatal("tool_call 非法状态应失败")
	}
}

// TestW10DNumericIndexBranches 覆盖 numericIndex/truncF。
func TestW10DNumericIndexBranches(t *testing.T) {
	if v, ok := numericIndex(float64(5)); !ok || v != 5 {
		t.Fatalf("float64 5 = %d/%v", v, ok)
	}
	if _, ok := numericIndex(float64(5.5)); ok {
		t.Fatal("非整数 float64 应失败")
	}
	if _, ok := numericIndex(float64(-1)); ok {
		t.Fatal("负 float64 应失败")
	}
	if v, ok := numericIndex(int64(9)); !ok || v != 9 {
		t.Fatalf("int64 = %v", v)
	}
	if v, ok := numericIndex(7); !ok || v != 7 {
		t.Fatalf("int = %v", v)
	}
	if v, ok := numericIndex(json.Number("11")); !ok || v != 11 {
		t.Fatalf("json.Number = %v", v)
	}
	if _, ok := numericIndex(json.Number("abc")); ok {
		t.Fatal("非法 json.Number 应失败")
	}
	if _, ok := numericIndex("x"); ok {
		t.Fatal("非数值类型应失败")
	}
	if truncF(3.7) != 3 || truncF(-3.7) != -3 {
		t.Fatalf("truncF = %v/%v", truncF(3.7), truncF(-3.7))
	}
}

// TestW10DParseStoredInputMarkersBranches 覆盖 parseStoredInputMarkers。
func TestW10DParseStoredInputMarkersBranches(t *testing.T) {
	if _, ok := parseStoredInputMarkers(stringsRepeatW10D("x", maxContentBlocksBytes+1)); ok {
		t.Fatal("超长应失败")
	}
	if _, ok := parseStoredInputMarkers("not json"); ok {
		t.Fatal("非法 json 应失败")
	}
	if _, ok := parseStoredInputMarkers("[]"); ok {
		t.Fatal("空数组应失败")
	}
	// 合法 input_text + input_image。
	good := `[{"type":"input_text","order":0,"text":"hi"},{"type":"input_image","order":1,"assetId":" chat_asset_00000000000000000000000000000000 "}]`
	markers, ok := parseStoredInputMarkers(good)
	if !ok || len(markers) != 2 || markers[0].Type != "input_text" || markers[1].Type != "input_image" {
		t.Fatalf("合法 = %+v/%v", markers, ok)
	}
	// 键数不对。
	if _, ok := parseStoredInputMarkers(`[{"type":"input_text","order":0}]`); ok {
		t.Fatal("缺 text 应失败")
	}
	if _, ok := parseStoredInputMarkers(`[{"type":"input_text","order":0,"text":"a","extra":1}]`); ok {
		t.Fatal("多余键应失败")
	}
	if _, ok := parseStoredInputMarkers(`[{"type":"input_text","order":1,"text":"a"}]`); ok {
		t.Fatal("order 与索引不符应失败")
	}
	if _, ok := parseStoredInputMarkers(`[{"type":"input_image","order":0,"assetId":"   "}]`); ok {
		t.Fatal("空 assetId 应失败")
	}
	if _, ok := parseStoredInputMarkers(`[{"type":"input_image","order":0,"assetId":"x","extra":1}]`); ok {
		t.Fatal("image 多余键应失败")
	}
	if _, ok := parseStoredInputMarkers(`[{"type":"bogus","order":0,"text":"a"}]`); ok {
		t.Fatal("未知类型应失败")
	}
	if _, ok := parseStoredInputMarkers(good + ` extra`); ok {
		t.Fatal("尾随内容应失败")
	}
}

// TestW10DParseContentBlocksBranches 覆盖 parseContentBlocks。
func TestW10DParseContentBlocksBranches(t *testing.T) {
	if b := parseContentBlocks(""); len(b) != 0 {
		t.Fatalf("空 = %+v", b)
	}
	if b := parseContentBlocks("not json"); len(b) != 0 {
		t.Fatalf("非法 = %+v", b)
	}
	if b := parseContentBlocks(stringsRepeatW10D("x", maxContentBlocksBytes+1)); len(b) != 0 {
		t.Fatalf("超长 = %+v", b)
	}
	blocks := parseContentBlocks(`[{"type":"output_text","text":"hi"},{"type":"wat"}]`)
	if len(blocks) != 1 || blocks[0].Text == nil {
		t.Fatalf("过滤非法 = %+v", blocks)
	}
	// serializeContentBlocks 超长臂。
	if _, err := serializeContentBlocks([]ContentBlock{{Type: "output_text", Text: strp(stringsRepeatW10D("y", maxContentBlocksBytes+5))}}); err == nil {
		t.Fatal("超长 serialize 应失败")
	}
	// serializeInputContentMarkers 错误臂。
	if _, err := serializeInputContentMarkers([]InputContentBlock{}, "user"); err != nil {
		t.Fatalf("默认块 = %v", err)
	}
	if _, err := serializeInputContentMarkers([]InputContentBlock{
		{Type: "input_image", AssetID: strp("")},
	}, "x"); err == nil {
		t.Fatal("空图片 ID 应失败")
	}
	if _, err := serializeInputContentMarkers([]InputContentBlock{
		{Type: "input_text", Text: strp("a")},
		{Type: "bogus"},
	}, "x"); err == nil {
		t.Fatal("未知类型应失败")
	}
	if _, err := serializeInputContentMarkers(make([]InputContentBlock, maxInputContentBlocks+2), "x"); err == nil {
		t.Fatal("块数超限应失败")
	}
}
