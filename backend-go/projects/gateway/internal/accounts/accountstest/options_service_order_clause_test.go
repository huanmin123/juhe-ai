package accountstest

import (
	"strings"
	"testing"
)

// 回归（2026-10-23 生产 500）：models 单表（B4 ⑧）物理列名是 public_model，
// PG 的 ORDER BY 别名不能出现在表达式内，`CASE WHEN model IN (...)` 会按
// FROM 表解析并报 SQLSTATE 42703（column "model" does not exist），SQLite
// 宽松解析掩盖了差异。置顶子句必须引用物理列。
func TestTestSelectedOrderClauseUsesPhysicalModelColumn(t *testing.T) {
	if got := testSelectedOrderClause(ManualTestOptionsQuery{}); got != "" {
		t.Fatalf("无已选模型时不得输出置顶子句：%q", got)
	}
	got := testSelectedOrderClause(ManualTestOptionsQuery{SelectedIDs: []string{"gpt-5.2", "gpt-6.1"}})
	if !strings.Contains(got, "CASE WHEN public_model IN (") {
		t.Fatalf("置顶子句必须引用物理列 public_model：%q", got)
	}
	if strings.Contains(got, "CASE WHEN model") {
		t.Fatalf("置顶子句不得引用 SELECT 别名 model（PG 表达式内解析为物理列名）：%q", got)
	}
}
