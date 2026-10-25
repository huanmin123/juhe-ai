package accounthealth

import (
	"encoding/json"
	"testing"
)

// 注：原借 SQLite store 载体的 outbox drain 语义用例（BUG-0218 毒丸行回归、
// 有界并发、backlog 告警、fence 结算）随 sqlite 臂退役，PG 载体回归由存储
// 迁移批次重建；本文件仅保留纯解析契约。

func TestParseProbeOutboxSourceFence(t *testing.T) {
	if fence, err := ParseProbeOutboxSourceFence(""); err != nil || fence != nil {
		t.Fatalf("empty fence = %#v, %v", fence, err)
	}
	raw, err := json.Marshal(SourceFence{StateKey: "sk", AccountID: "a", SourceGeneration: 1, SourceFenceID: "f", RuntimeKey: "rt", ProbeGeneration: 2, ConfigRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := ParseProbeOutboxSourceFence(string(raw))
	if err != nil || fence == nil || fence.ConfigRevision != 3 || fence.ProbeGeneration != 2 {
		t.Fatalf("fence = %#v, %v", fence, err)
	}
	if _, err := ParseProbeOutboxSourceFence("{not-json"); err == nil {
		t.Fatal("corrupt fence must error")
	}
}
