package accountscore

import "strings"

// SQLTable mirrors Store.table: qualifies the shared juhe_business schema.
func SQLTable(name string) string {
	return "juhe_business." + name
}

// SQLBind mirrors Store.bind: rewrites ? placeholders into $N for PostgreSQL
// and rewrites the legacy instr(X, Y) into strpos(X, Y)（语义等价：
// 1 起始下标、缺失返回 0）——2026-09-25 生产 my-accounts keyword 搜索 500
// 修复（function instr(text, unknown) does not exist）。
func SQLBind(query string) string {
	if strings.Contains(query, "instr(") {
		query = strings.ReplaceAll(query, "instr(", "strpos(")
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// BoolTrueLiteral renders the PostgreSQL TRUE literal.
func BoolTrueLiteral() string {
	return "TRUE"
}

// BoolFalseLiteral renders the PostgreSQL FALSE literal.
func BoolFalseLiteral() string {
	return "FALSE"
}

// SQLForUpdate renders the SELECT ... FOR UPDATE suffix
// (REFACTOR-0005 阶段 B 下沉：重置子域共用).
func SQLForUpdate() string {
	return " FOR UPDATE"
}

// Itoa renders a small non-negative int (placeholder indices, limits).
func Itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}
