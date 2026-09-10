package openaicompat

// covhelpers_test.go：本批补充覆盖测试共享的小断言 helper。
// 断言只用标准库；命名统一带 cov 前缀避免与既有测试基建冲突。

import (
	"testing"
)

// covField 断言 object 携带 key 并返回其值（缺失即失败，避免空断言）。
func covField(t *testing.T, object map[string]any, key string) any {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("期望字段 %s 存在，实际对象：%v", key, object)
	}
	return value
}

// covMap 断言 value 是 JSON 对象并返回。
func covMap(t *testing.T, value any) map[string]any {
	t.Helper()
	record, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("期望 JSON 对象，实际 %T：%v", value, value)
	}
	return record
}

// covSlice 断言 value 是 JSON 数组并返回。
func covSlice(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("期望 JSON 数组，实际 %T：%v", value, value)
	}
	return items
}

// covText 断言 value 是字符串并返回。
func covText(t *testing.T, value any) string {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("期望字符串，实际 %T：%v", value, value)
	}
	return text
}

// covNumber 断言 value 是数字并返回（JSON 解码后一律 float64）。
func covNumber(t *testing.T, value any) float64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("期望数字，实际 %T：%v", value, value)
	}
	return number
}

// covInt 断言 value 等于期望的整数值（兼容 int64 / float64 两种内部表示）。
func covInt(t *testing.T, value any, want int64) {
	t.Helper()
	if got, ok := value.(int64); ok {
		if got != want {
			t.Fatalf("期望 %d，实际 %d", want, got)
		}
		return
	}
	number := covNumber(t, value)
	if int64(number) != want {
		t.Fatalf("期望 %d，实际 %v", want, value)
	}
}

// covBlockText 取 blocks[i] 的 text 字段。
func covBlockText(t *testing.T, blocks []any, index int) string {
	t.Helper()
	if index >= len(blocks) {
		t.Fatalf("期望至少 %d 个 block，实际 %d 个：%v", index+1, len(blocks), blocks)
	}
	block := covMap(t, blocks[index])
	return covText(t, block["text"])
}

// covToolUseBlocks 过滤出 type=tool_use 的 block。
func covToolUseBlocks(t *testing.T, blocks []any) []map[string]any {
	t.Helper()
	output := []map[string]any{}
	for _, block := range blocks {
		record := covMap(t, block)
		if covText(t, record["type"]) == "tool_use" {
			output = append(output, record)
		}
	}
	return output
}
