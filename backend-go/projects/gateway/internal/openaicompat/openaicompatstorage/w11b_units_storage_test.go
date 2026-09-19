package openaicompatstorage

// w11b 波次 storage 域半边（自 w11b_units_test.go 拆出）：bridgeexecutors 实现半边的
// 阈值/媒体类型 helper、图像生成解析 helper 与文件对象构造边界。

import "testing"

func TestW11BBridgeExecutorsHelpers(t *testing.T) {
	threshold := 0.5
	options := map[string]any{"score_threshold": threshold}
	if value := scoreThresholdFromRankingOptions(options); value == nil || *value != 0.5 {
		t.Fatalf("threshold = %v", value)
	}
	if scoreThresholdFromRankingOptions(nil) != nil {
		t.Fatal("nil 选项 nil")
	}
	if scoreThresholdFromRankingOptions(map[string]any{}) != nil {
		t.Fatal("缺阈值 nil")
	}
	if !isTextBridgeMediaType("text/plain") || !isTextBridgeMediaType("text/plain; charset=utf-8") {
		t.Fatal("文本媒体类型")
	}
	if isTextBridgeMediaType("") || isTextBridgeMediaType("application/json") {
		t.Fatal("非文本媒体类型")
	}
}

func TestW11BImageGenerationParseHelpers(t *testing.T) {
	// safeParseJSON：空串/坏 JSON 折叠空对象。
	if object := safeParseJSON("bad").(map[string]any); len(object) != 0 {
		t.Fatal("坏 JSON 空对象")
	}
	if object := safeParseJSON("").(map[string]any); len(object) != 0 {
		t.Fatal("空串空对象")
	}
	if object := safeParseJSON(`{"a":1}`).(map[string]any); object["a"] != float64(1) {
		t.Fatal("对象解析")
	}
	// imageGenerationOutputItemFrom。
	if imageGenerationOutputItemFrom(nil) != nil {
		t.Fatal("nil 输出 nil")
	}
	if imageGenerationOutputItemFrom(map[string]any{}) != nil {
		t.Fatal("缺 output nil")
	}
	if imageGenerationOutputItemFrom(map[string]any{"output": []any{map[string]any{"type": "other"}}}) != nil {
		t.Fatal("无匹配项 nil")
	}
	item := imageGenerationOutputItemFrom(map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "id": "ig"}}})
	if item == nil || item["id"] != "ig" {
		t.Fatalf("item = %v", item)
	}
	// imageGenerationResultFromJSON：nil 缺 b64 → 错误。
	if _, err := imageGenerationResultFromJSON(nil, "png"); err == nil {
		t.Fatal("nil JSON 缺 b64 应报错")
	}
	result, err := imageGenerationResultFromJSON(map[string]any{"data": []any{map[string]any{"b64_json": "Zm9v"}}}, "png")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ImageBase64 == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BFileObjectHelpers(t *testing.T) {
	// 自 TestW11BComputerAdapterAndMiscHelpers 拆出的 storage 半边：文件对象构造与媒体类型。
	if mediaTypePointer(&uploadedFile{}) != nil {
		t.Fatal("无媒体类型 nil")
	}
	if value := mediaTypePointer(&uploadedFile{HasMedia: true, MediaType: "text/plain"}); value == nil || *value != "text/plain" {
		t.Fatal("媒体类型透传")
	}
	// newFileObject 非法时间戳。
	badTime := "not-a-time"
	if _, err := newFileObject(FileRecord{ID: "f", CreatedAt: badTime}); err == nil {
		t.Fatal("非法时间戳报错")
	}
	expires := "not-a-time"
	if _, err := newFileObject(FileRecord{ID: "f", CreatedAt: "2026-01-02T03:04:05Z", ExpiresAt: &expires}); err == nil {
		t.Fatal("非法过期时间报错")
	}
	if _, err := newContainerFileObject(FileRecord{CreatedAt: badTime}); err == nil {
		t.Fatal("容器文件非法时间戳报错")
	}
	object, err := newFileObject(FileRecord{ID: "f", CreatedAt: "2026-01-02T03:04:05Z", Bytes: 3, Filename: "a.txt", Purpose: "assistants", Status: "processed"})
	if err != nil || object.ID != "f" || object.Object != "file" {
		t.Fatalf("object = %+v err = %v", object, err)
	}
}
