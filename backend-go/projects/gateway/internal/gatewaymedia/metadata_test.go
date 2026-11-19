package gatewaymedia

// 唯一扩展通道 metadata 的解析/合并/回显测试（设计《对外协议面对齐 new-api
// 设计》§5.1）：对象与 JSON 字符串双形态、规范字段冲突 400、深合并覆盖、
// 键名摘要稳定排序。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractMetadataForms(t *testing.T) {
	object, err := ExtractMetadata(map[string]any{"camera_fixed": true}, VideoMetadataReserved)
	if err != nil || object["camera_fixed"] != true {
		t.Fatalf("对象形态 = %v, %v", object, err)
	}
	fromString, err := ExtractMetadata(`{"camera_fixed":true,"nested":{"k":1}}`, VideoMetadataReserved)
	if err != nil || fromString["camera_fixed"] != true {
		t.Fatalf("JSON 字符串形态 = %v, %v", fromString, err)
	}
	if _, ok := fromString["nested"].(map[string]any); !ok {
		t.Fatalf("字符串形态应解析出嵌套对象: %#v", fromString)
	}
	for _, raw := range []any{nil, "", "null"} {
		if got, err := ExtractMetadata(raw, VideoMetadataReserved); err != nil || len(got) != 0 {
			t.Fatalf("空输入 %v = %v, %v", raw, got, err)
		}
	}
	for name, raw := range map[string]any{
		"非法 JSON 字符串": "not-json",
		"数组":          []any{"x"},
		"数字":          float64(1),
	} {
		if _, err := ExtractMetadata(raw, VideoMetadataReserved); err == nil {
			t.Fatalf("%s 形态应报错", name)
		}
	}
}

func TestExtractMetadataRejectsReservedFields(t *testing.T) {
	for _, key := range []string{"prompt", "model", "size", "duration", "metadata"} {
		if _, err := ExtractMetadata(map[string]any{key: "x"}, VideoMetadataReserved); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("规范字段 %s 应被拒绝: %v", key, err)
		}
	}
	// 语音/长音频的规范字段集合不同：prompt 在语音面不是规范字段，允许扩展。
	if _, err := ExtractMetadata(map[string]any{"prompt": "x"}, SpeechMetadataReserved); err != nil {
		t.Fatalf("语音面 prompt 不是规范字段，应允许: %v", err)
	}
	if _, err := ExtractMetadata(map[string]any{"input": "x"}, SpeechMetadataReserved); err == nil {
		t.Fatal("语音面 input 是规范字段，应拒绝")
	}
	if _, err := ExtractMetadata(map[string]any{"input_url": "x"}, AudioJobMetadataReserved); err == nil {
		t.Fatal("长音频面 input_url 是规范字段，应拒绝")
	}
}

func TestMergeMetadata(t *testing.T) {
	base := map[string]any{
		"model":  "cogvideox-3",
		"nested": map[string]any{"keep": true, "overwrite": "old"},
		"list":   []any{float64(1)},
		"scalar": "base",
	}
	metadata := map[string]any{
		"nested":  map[string]any{"overwrite": "new", "added": true},
		"scalar":  "ext",
		"unknown": float64(7),
	}
	merged := MergeMetadata(base, metadata)
	nested := merged["nested"].(map[string]any)
	if nested["keep"] != true || nested["overwrite"] != "new" || nested["added"] != true {
		t.Fatalf("嵌套合并 = %#v", nested)
	}
	if merged["scalar"] != "ext" || merged["unknown"] != float64(7) || merged["model"] != "cogvideox-3" {
		t.Fatalf("顶层合并 = %#v", merged)
	}
	// 入参不被修改（base 是调用方的报文，metadata 是请求体快照）。
	if base["scalar"] != "base" || base["nested"].(map[string]any)["overwrite"] != "old" {
		t.Fatalf("base 被修改: %#v", base)
	}
	if metadata["nested"].(map[string]any)["overwrite"] != "new" {
		t.Fatalf("metadata 被修改: %#v", metadata)
	}
	if got := MergeMetadata(nil, nil); len(got) != 0 {
		t.Fatalf("空合并 = %#v", got)
	}
}

func TestMergeMetadataJSON(t *testing.T) {
	body := []byte(`{"model":"m","prompt":"p","nested":{"k":"v"}}`)
	same, err := MergeMetadataJSON(body, nil)
	if err != nil || string(same) != string(body) {
		t.Fatalf("空 metadata 应字节级透传: %s, %v", same, err)
	}
	merged, err := MergeMetadataJSON(body, map[string]any{"camera_fixed": true, "nested": map[string]any{"k2": "v2"}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatalf("merge 结果不是 JSON: %v", err)
	}
	if decoded["camera_fixed"] != true {
		t.Fatalf("扩展键未并入: %#v", decoded)
	}
	nested := decoded["nested"].(map[string]any)
	if nested["k"] != "v" || nested["k2"] != "v2" {
		t.Fatalf("嵌套未合并: %#v", nested)
	}
	if _, err := MergeMetadataJSON([]byte(`[1]`), map[string]any{"x": 1}); err == nil {
		t.Fatal("非对象报文应报错")
	}
}

func TestAppliedMetadataKeys(t *testing.T) {
	if keys := AppliedMetadataKeys(nil); keys != nil {
		t.Fatalf("空输入 = %v", keys)
	}
	keys := AppliedMetadataKeys(map[string]any{"quality": "q", "fps": float64(30)})
	if len(keys) != 2 || keys[0] != "fps" || keys[1] != "quality" {
		t.Fatalf("键名摘要 = %v, want 字典序", keys)
	}
}
