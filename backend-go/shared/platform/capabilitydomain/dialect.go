// 方言选择：阶段记录与能力域迁移的业务库 SQL 在 SQLite 与 PostgreSQL 间
// 分支（PG 走 "juhe_business" schema 限定与 $N 占位符，SQLite 走裸表名与
// ? 占位符）。原为 maintenance internal/capabilitydomain migration.go 的
// Dialect 定义（2026-10 M2 随阶段记录读取一起下沉）。

package capabilitydomain

// Dialect 选择 schema 限定与方言 SQL。
type Dialect int

const (
	DialectPostgres Dialect = iota
	DialectSQLite
)

// String 返回报告 driver 标签。
func (d Dialect) String() string {
	if d == DialectSQLite {
		return "sqlite"
	}
	return "postgres"
}
