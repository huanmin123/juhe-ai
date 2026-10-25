package circuitstore

// 收尾三：零散可达分支（校验出口、hourly 配额维度、owner lastUsedAt、
// 锁视图的账户缺失分支）。

import (
	"database/sql"
	"testing"
)

func TestComposeEntryOwnerLastUsedAt(t *testing.T) {
	loader := &ProjectionItemLoader{}
	now := effNow()
	row := newSourcesRow("acc-used")
	row.status = "active"
	row.schedulable = 1
	row.lastUsedAt = sql.NullString{String: "2026-07-01T00:00:00.000Z", Valid: true}
	entry, err := loader.composeEntry(composeInput{row: row, payload: map[string]any{}, now: now})
	if err != nil {
		t.Fatal(err)
	}
	if entry.sortLastUsedAt == nil || *entry.sortLastUsedAt != "2026-07-01T00:00:00.000Z" {
		t.Fatalf("owner 行排序键应取 last_used_at: %v", entry.sortLastUsedAt)
	}
	if entry.payload["lastUsedAt"] != "2026-07-01T00:00:00.000Z" {
		t.Fatalf("owner 行 payload 应写 lastUsedAt: %v", entry.payload["lastUsedAt"])
	}
}

// TestProbeStateKeyShapeWithRevision 覆盖 runtime key 的 revision 回落。
func TestProbeStateKeyShapeWithRevision(t *testing.T) {
	if got := AvailabilityProbeRuntimeKey("acc", "kind", 0); got != "availability:acc:kind:r1" {
		t.Fatalf("revision < 1 应回落 1: %s", got)
	}
	if got := AvailabilityProbeRuntimeKey("  acc  ", "kind", 7); got != "availability:acc:kind:r7" {
		t.Fatalf("scope 应修剪: %s", got)
	}
	if !containsFence(&[]string{"a", "b"}, "b") || containsFence(&[]string{"a"}, "b") || containsFence(nil, "b") {
		t.Fatal("containsFence 语义不符")
	}
}
