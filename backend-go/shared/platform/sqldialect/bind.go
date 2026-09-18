// Package sqldialect 提供 SQLite/PostgreSQL 双模式的 SQL 方言设施。
// 收敛各包自写的 `?`→`$n` 占位符改写（taskruns bind / circuitstore bindSQL /
// statsagg dialect 三份逐字节等价实现，评估文档 R1 取证确认）；pgx 驱动不做
// `?`→`$n` 改写，方言共享 SQL 在 PG 模式必须先经本改写。
package sqldialect

import (
	"strconv"
	"strings"
)

// BindSQL 把 SQL 中的顺序 `?` 占位符按出现次序改写为 PostgreSQL 的 $n 序号。
// postgres=false（SQLite 模式）原样返回。调用方必须保证 SQL 文本不含含 `?`
// 的字符串字面量（与既有三份实现的正确性前提一致）。
func BindSQL(postgres bool, query string) string {
	if !postgres || !strings.Contains(query, "?") {
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
