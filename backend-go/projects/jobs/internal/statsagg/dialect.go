package statsagg

import (
	"fmt"
	"strings"
)

// Dialect 承载 SQLite 测试 / PG 生产的 SQL 双模差异：
//   - 占位符：SQLite `?`，PG `$n`；
//   - stats 库表限定：SQLite 裸表名，PG `juhe_stats.` 前缀；
//   - usage 记录源表：SQLite `usage_records`，PG `juhe_usage.usage_records`。
//
// 双模下同一语句由 bind 生成，保证聚合语义逐字段一致（对齐
// usage-stats.repository.ts 的 PG 路径与 usage-stats-writers.ts 的 SQLite 路径）。
type Dialect struct {
	Postgres bool
}

func (d Dialect) bind(query string) string {
	if !d.Postgres {
		return query
	}
	var out strings.Builder
	index := 0
	for _, ch := range query {
		if ch == '?' {
			index++
			out.WriteString(fmt.Sprintf("$%d", index))
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}

// StatsTable 返回 stats 库表引用。
func (d Dialect) StatsTable(tableName string) string {
	if d.Postgres {
		return "juhe_stats." + tableName
	}
	return tableName
}

// UsageRecordsTable 返回 usage 记录源表引用。
func (d Dialect) UsageRecordsTable() string {
	if d.Postgres {
		return "juhe_usage.usage_records"
	}
	return "usage_records"
}

// qualifiedTarget 返回 DO UPDATE SET 右值里指向目标行既有值的限定引用。
// SQLite/PG 均支持 `table.column` 形式。
func (d Dialect) qualifiedTarget(tableName string) string {
	if d.Postgres {
		return "juhe_stats." + tableName
	}
	return tableName
}

// leastExpr 返回二元取小表达式：PG 用 LEAST，SQLite 用标量 MIN
// （对齐 Node PG 路径的 LEAST 与 SQLite 路径的 MIN 语义）。
func (d Dialect) leastExpr(left, right string) string {
	if d.Postgres {
		return "LEAST(" + left + ", " + right + ")"
	}
	return "MIN(" + left + ", " + right + ")"
}

// greatestExpr 返回二元取大表达式。
func (d Dialect) greatestExpr(left, right string) string {
	if d.Postgres {
		return "GREATEST(" + left + ", " + right + ")"
	}
	return "MAX(" + left + ", " + right + ")"
}
