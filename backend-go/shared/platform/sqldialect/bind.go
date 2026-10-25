// Package sqldialect 提供 PostgreSQL 方言设施：收敛各包自写的 `?`→`$n`
// 占位符改写（taskruns bind / circuitstore bindSQL / statsagg dialect 三份
// 逐字节等价实现，评估文档 R1 取证确认）；pgx 驱动不做 `?`→`$n` 改写，方言
// 共享 SQL 在 PG 上必须先经本改写。2026-09-30 起 driver 层兜底改写（driver.go
// 的 Driver/WrapDriver/OpenDB，清理批次 C5 自 gateway/jobs pgpool 双副本收敛）
// 也归本包，供 pgpool 装配与 accountbalance 裸开 pgx 路径统一接线。
// 2026-10-25 sqlite 存储形态退役：BindSQL 的 postgres=false 透传臂随 sqlite
// 方言删除（全仓调用方核实恒传 true），参数保留以稳定跨模块签名。
package sqldialect

import (
	"strconv"
	"strings"
)

// BindSQL 把 SQL 中的顺序 `?` 占位符按出现次序改写为 PostgreSQL 的 $n 序号。
// postgres 参数已随 sqlite 方言退役失效（恒按 PG 改写），保留以稳定签名。
// 调用方必须保证 SQL 文本不含含 `?` 的字符串字面量（与既有三份实现的正确性
// 前提一致）。
func BindSQL(postgres bool, query string) string {
	_ = postgres
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	index := 0
	for _, ch := range query {
		if ch == '?' {
			index++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(index))
			continue
		}
		b.WriteRune(ch)
	}
	return b.String()
}
