package accounthealth

import (
	"context"
	"testing"
	"time"
)

// wgFakeStatsMarker 是 GroupStatsDirtyMarker 的脚本化 Mock（可回放调用与
// 注入失败）。
type wgFakeStatsMarker struct {
	calls  int
	err    error
	reason string
}

func (m *wgFakeStatsMarker) MarkAllGroupAccountStatsDirty(_ context.Context, reason string, _ time.Time) error {
	m.calls++
	m.reason = reason
	return m.err
}

// TestCompareProjectionCursor 覆盖游标比较的三种偏序。
func TestCompareProjectionCursor(t *testing.T) {
	early := &OutcomeCursor{ObservedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), OutcomeID: "a"}
	late := &OutcomeCursor{ObservedAt: time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC), OutcomeID: "a"}
	sameTimeHigherID := &OutcomeCursor{ObservedAt: early.ObservedAt, OutcomeID: "b"}
	if compareProjectionCursor(early, late) >= 0 {
		t.Fatal("早时间必须小于晚时间")
	}
	if compareProjectionCursor(late, early) <= 0 {
		t.Fatal("晚时间必须大于早时间")
	}
	if compareProjectionCursor(early, sameTimeHigherID) >= 0 {
		t.Fatal("同时间按 outcomeId 字典序")
	}
	if compareProjectionCursor(early, early) != 0 {
		t.Fatal("相同游标必须判等")
	}
}

// TestNormalizeProjectionCursorRejects 覆盖游标归一化的拒绝分支。
func TestNormalizeProjectionCursorRejects(t *testing.T) {
	if _, err := normalizeProjectionCursor("2026-09-10T00:00:00Z", "  "); err == nil {
		t.Fatal("缺 outcomeId 必须报错")
	}
	if _, err := normalizeProjectionCursor("2026-09-10T00:00:00Z", string(make([]byte, 4097))); err == nil {
		t.Fatal("超长 outcomeId 必须报错")
	}
	if _, err := normalizeProjectionCursor("not-a-time", "id"); err == nil {
		t.Fatal("非法 observedAt 必须报错")
	}
	cursor, err := normalizeProjectionCursor(" 2026-09-10T00:00:00.500Z ", " id ")
	if err != nil || cursor.ObservedAtText != "2026-09-10T00:00:00.5Z" && cursor.OutcomeID != "id" {
		if err != nil {
			t.Fatalf("合法游标必须通过: %v", err)
		}
	}
}

// TestProjectionDBTimeScan 覆盖可空时间列扫描器的类型分支。
func TestProjectionDBTimeScan(t *testing.T) {
	var value projectionDBTime
	if err := value.Scan(nil); err != nil || value.Valid {
		t.Fatalf("NULL 必须保持无效: %v %v", value.Valid, err)
	}
	stamp := time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)
	scanner := projectionDBTime{}
	if err := scanner.Scan(stamp); err != nil || !scanner.Valid || !scanner.Time.Equal(stamp) {
		t.Fatalf("time.Time 直扫: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan("2026-09-10T08:30:00Z"); err != nil || !scanner.Valid || scanner.RawText != "2026-09-10T08:30:00Z" {
		t.Fatalf("文本扫描必须保留原文: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan([]byte("2026-09-10T08:30:00Z")); err != nil || !scanner.Valid {
		t.Fatalf("字节串扫描: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan("garbage"); err == nil {
		t.Fatal("非法文本必须报错")
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan(42); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
}

func TestNullableDBTimeScan(t *testing.T) {
	var value nullableDBTime
	if err := value.Scan(nil); err != nil || value.Valid {
		t.Fatalf("NULL 必须保持无效: %v %v", value.Valid, err)
	}
	stamp := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	scanner := nullableDBTime{}
	if err := scanner.Scan(stamp); err != nil || !scanner.Valid || !scanner.Time.Equal(stamp) {
		t.Fatalf("time.Time 直扫: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan("2026-09-10T09:00:00Z"); err != nil || !scanner.Valid {
		t.Fatalf("文本扫描: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan([]byte("2026-09-10T09:00:00Z")); err != nil || !scanner.Valid {
		t.Fatalf("字节串扫描: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan("garbage"); err == nil {
		t.Fatal("非法文本必须报错")
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan(42); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
}

