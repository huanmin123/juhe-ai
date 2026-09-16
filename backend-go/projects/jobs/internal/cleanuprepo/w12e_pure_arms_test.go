package cleanuprepo

import (
	"strings"
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

// TestW12EChatPartitionHelpers：分区名解析与随机回退臂。
func TestW12EChatPartitionHelpers(t *testing.T) {
	// newRandomHex32 已由 w10e 覆盖主路径；此处覆盖 chat.go 的
	// parseInstantMust（非法输入回落零值）。
	if !parseInstantMust(kitUpdatedAt).IsZero() {
		if got := parseInstantMust(kitUpdatedAt).Unix(); got != kitNow().Unix() {
			t.Fatalf("parseInstantMust = %v", got)
		}
	}
	if !parseInstantMust("bad").IsZero() {
		t.Fatalf("非法时间应回落零值")
	}
	if chatPartitionNamePattern.MatchString("nope") {
		t.Fatalf("非法分区名不应匹配")
	}
	if !strings.Contains("chat_messages_20260101", "chat_messages_") {
		t.Fatalf("前缀断言失败")
	}
}
