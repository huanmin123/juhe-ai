package statsagg

import (
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// Dialect 承载 PG 唯一形态的 SQL 生成（原 SQLite 方言臂已随存储形态退役）：
//   - 占位符：PG `$n`；
//   - stats 库表限定：`juhe_stats.` 前缀；
//   - usage 记录源表：`juhe_usage.usage_records`；
//   - 业务库与 stats 库同池（juhe_business. 前缀）。
//
// 对齐 usage-stats.repository.ts 的 PG 路径。
type Dialect struct{}

// bind 实现收敛到 shared/platform/sqldialect（行为逐字节等价）。
func (d Dialect) bind(query string) string {
	return sqldialect.BindSQL(query)
}

// StatsTable 返回 stats 库表引用。
func (d Dialect) StatsTable(tableName string) string {
	return "juhe_stats." + tableName
}

// BusinessTable 返回业务库表引用。
func (d Dialect) BusinessTable(tableName string) string {
	return "juhe_business." + tableName
}

// UsageRecordsTable 返回 usage 记录源表引用。
func (d Dialect) UsageRecordsTable() string {
	return "juhe_usage.usage_records"
}

// qualifiedTarget 返回 DO UPDATE SET 右值里指向目标行既有值的限定引用。
func (d Dialect) qualifiedTarget(tableName string) string {
	return "juhe_stats." + tableName
}

// leastExpr 返回二元取小表达式（PG LEAST）。
func (d Dialect) leastExpr(left, right string) string {
	return "LEAST(" + left + ", " + right + ")"
}

// greatestExpr 返回二元取大表达式（PG GREATEST）。
func (d Dialect) greatestExpr(left, right string) string {
	return "GREATEST(" + left + ", " + right + ")"
}
