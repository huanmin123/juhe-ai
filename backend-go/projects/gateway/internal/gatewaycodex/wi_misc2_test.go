package gatewaycodex

import (
	"testing"
)

// 2026-11-07 桥退役批：本文件原 compaction snapshot / contextstore helper /
// compact 失败面 / dispatch baseline / account kind 矩阵 / body 重写基线用例
// 随 chat-bridge state + compact preflight 机制整批删除，仅保留加密上下文
// 清理与契约注册表的纯函数用例。

func TestWIEncryptedContentStripHelpers(t *testing.T) {
	encrypted := map[string]any{"type": "encrypted_content", "encrypted_content": "abc"}
	plain := map[string]any{"type": "message"}
	items := []any{encrypted, plain}
	result := stripEncryptedContentItems(items)
	if !result.changed || result.removedCount != 1 {
		t.Fatalf("strip=%+v", result)
	}
	if len(result.output.([]any)) != 1 {
		t.Fatalf("output=%v", result.output)
	}
	// 单个加密对象 → 移除为空数组。
	single := stripEncryptedContentItems(encrypted)
	if !single.changed || single.removedCount != 1 {
		t.Fatalf("single=%+v", single)
	}
	// 非 加密项原样保留。
	untouched := stripEncryptedContentItems("scalar")
	if untouched.changed || untouched.output != "scalar" {
		t.Fatalf("scalar=%+v", untouched)
	}
	// 空 reasoning 项判定：只有 type/id/status 与空字段。
	emptyReasoning := map[string]any{"type": "reasoning", "id": "r1", "status": "", "summary": []any{}}
	if !isEmptyReasoningItem(emptyReasoning) {
		t.Fatal("空 reasoning 必须判定为空")
	}
	nonEmpty := map[string]any{"type": "reasoning", "summary": []any{"内容"}}
	if isEmptyReasoningItem(nonEmpty) {
		t.Fatal("有内容 reasoning 不得判空")
	}
}

func TestWIContractRegistryItem(t *testing.T) {
	// 注册表 item 查找：已知类型命中，未知类型 miss，nil 注册表安全。
	if item, ok := defaultCodexResponsesContractRegistry.Item("message"); !ok || item.Type != "message" || item.Prefix == "" {
		t.Fatalf("message item=%+v ok=%v", item, ok)
	}
	if _, ok := defaultCodexResponsesContractRegistry.Item("unknown-type"); ok {
		t.Fatal("未知类型不得命中")
	}
	var nilRegistry *CodexResponsesContractRegistry
	if _, ok := nilRegistry.Item("message"); ok {
		t.Fatal("nil 注册表必须 miss")
	}
}
