package cleanuprepo

import (
	"testing"
)

// w12e_pure_arms_test.go：剩余纯函数臂。

// TestW12EPostgresMultiRowPlaceholders：占位符构造的参数防御臂。
func TestW12EPostgresMultiRowPlaceholders(t *testing.T) {
	if got := postgresMultiRowPlaceholders(0, 3); got != "" {
		t.Fatalf("rowCount=0 应返回空串: %q", got)
	}
	if got := postgresMultiRowPlaceholders(2, 0); got != "" {
		t.Fatalf("columnCount=0 应返回空串: %q", got)
	}
	if got := postgresMultiRowPlaceholders(2, 2); got != "(?, ?), (?, ?)" {
		t.Fatalf("两行两列 = %q", got)
	}
}

// TestW12EParseInstantMust：非法输入回落零值臂。（分区名解析已随 chat_* 表
// 退役移除，演练场设计 §8；parseInstantMust 现居 rowscan.go。）
func TestW12EParseInstantMust(t *testing.T) {
	if !parseInstantMust("bad").IsZero() {
		t.Fatalf("非法时间应回落零值")
	}
	parsed := parseInstantMust(kitUpdatedAt)
	if parsed.IsZero() {
		t.Fatalf("合法时间不应为零值")
	}
}
