package accountscore

import "strings"

// SQLTable mirrors Store.table: PostgreSQL qualifies the shared juhe_business
// schema, SQLite reads the plain table names.
func SQLTable(postgres bool, name string) string {
	if postgres {
		return "juhe_business." + name
	}
	return name
}

// SQLBind mirrors Store.bind: rewrites ? placeholders into $N for PostgreSQL.
// PG 分支同时把 SQLite 专有的 instr(X, Y) 改写为 strpos(X, Y)（语义等价：
// 1 起始下标、缺失返回 0）——2026-09-25 生产 my-accounts keyword 搜索 500
// 修复（function instr(text, unknown) does not exist）。
func SQLBind(postgres bool, query string) string {
	if !postgres {
		return query
	}
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

// BoolTrueLiteral mirrors Store.boolTrueLiteral: PostgreSQL TRUE, SQLite 1.
func BoolTrueLiteral(postgres bool) string {
	if postgres {
		return "TRUE"
	}
	return "1"
}

// BoolFalseLiteral mirrors Store.boolFalseLiteral: PostgreSQL FALSE, SQLite 0.
func BoolFalseLiteral(postgres bool) string {
	if postgres {
		return "FALSE"
	}
	return "0"
}

// SQLForUpdate mirrors Store.forUpdate: the SELECT ... FOR UPDATE suffix for
// PostgreSQL, empty on SQLite (REFACTOR-0005 阶段 B 下沉：重置子域共用).
func SQLForUpdate(postgres bool) string {
	if postgres {
		return " FOR UPDATE"
	}
	return ""
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
