// Package usagerecordsmigration 是 Phase 4（usage_records 分区族重建）的
// prepare 预检与影子表构建内核。预检只读；DDL 由 maintenance revision 29
// 在发布窗内调用。
package usagerecordsmigration

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

const (
	UsageRecordsSchema = "juhe_usage"
	UsageRecordsTable  = "usage_records"
	TargetSchema       = "juhe_usage_migrate"
)

var partitionNamePattern = regexp.MustCompile(`^usage_records_\d{8}$`)

// Partition 是旧分区清单中的一项。
type Partition struct {
	Name   string `json:"name"`
	Bound  string `json:"bound"`
	Rows   int64  `json:"rows"`
	Size   int64  `json:"sizeBytes"`
	Target string `json:"targetPartition"`
}

// Plan 是 prepare 预检报告；任何 Blocking 非空时 Apply 必须拒绝执行。
type Plan struct {
	ParentTable              string      `json:"parentTable"`
	ParentPresent            bool        `json:"parentPresent"`
	TargetPresent            bool        `json:"targetPresent"`
	TotalRows                int64       `json:"totalRows"`
	Partitions               []Partition `json:"partitions"`
	DuplicateIDs             int64       `json:"duplicateIds"`
	NonCanonicalCreatedAt    int64       `json:"nonCanonicalCreatedAt"`
	InvalidStatsWatermarks   int64       `json:"invalidStatsWatermarks"`
	InvalidClientIPWatermark int64       `json:"invalidClientIpWatermarks"`
	Blocking                 []string    `json:"blocking"`
}

// Ready 表示预检没有阻断项。
func (p Plan) Ready() bool { return len(p.Blocking) == 0 }

// PlanPrepare 只读盘点 usage_records 父表与全部分区，输出换表前必须人工
// 核对的事实。重复 id 只报告不阻断：当前主键是 (created_at,id)，同一 id
// 跨瞬时出现会保留为独立物理行，由业务方决定是否清理。
func PlanPrepare(ctx context.Context, db *sql.DB) (Plan, error) {
	plan := Plan{ParentTable: UsageRecordsSchema + "." + UsageRecordsTable}
	present, err := relationPresent(ctx, db, UsageRecordsSchema, UsageRecordsTable)
	if err != nil {
		return plan, err
	}
	plan.ParentPresent = present
	if !present {
		plan.Blocking = append(plan.Blocking, "juhe_usage.usage_records 不存在")
		return plan, nil
	}
	targetPresent, err := relationPresent(ctx, db, TargetSchema, UsageRecordsTable)
	if err != nil {
		return plan, err
	}
	plan.TargetPresent = targetPresent
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage.usage_records`).Scan(&plan.TotalRows); err != nil {
		return plan, fmt.Errorf("统计 usage_records 总行数失败: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, pg_get_expr(c.relpartbound, c.oid), pg_total_relation_size(c.oid)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.inhparent = 'juhe_usage.usage_records'::regclass
		  AND n.nspname = 'juhe_usage'
		ORDER BY c.relname`)
	if err != nil {
		return plan, fmt.Errorf("读取 usage_records 分区清单失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var partition Partition
		if err := rows.Scan(&partition.Name, &partition.Bound, &partition.Size); err != nil {
			return plan, err
		}
		if !partitionNamePattern.MatchString(partition.Name) {
			plan.Blocking = append(plan.Blocking, "非规范分区名: "+partition.Name)
			continue
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage.`+quoteIdentifier(partition.Name)).Scan(&partition.Rows); err != nil {
			return plan, fmt.Errorf("统计分区 %s 行数失败: %w", partition.Name, err)
		}
		partition.Target = partition.Name
		plan.Partitions = append(plan.Partitions, partition)
	}
	if err := rows.Err(); err != nil {
		return plan, err
	}
	if len(plan.Partitions) == 0 && plan.TotalRows > 0 {
		plan.Blocking = append(plan.Blocking, "usage_records 有数据但没有可迁移分区")
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM (
			SELECT id FROM juhe_usage.usage_records GROUP BY id HAVING count(*) > 1
		) duplicate_ids`).Scan(&plan.DuplicateIDs); err != nil {
		return plan, fmt.Errorf("统计重复 usage id 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM juhe_usage.usage_records
		WHERE created_at IS NULL
		   OR created_at !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
		   OR NOT pg_input_is_valid(created_at, 'timestamptz')`).Scan(&plan.NonCanonicalCreatedAt); err != nil {
		return plan, fmt.Errorf("统计非规范 created_at 失败: %w", err)
	}
	if plan.NonCanonicalCreatedAt > 0 {
		plan.Blocking = append(plan.Blocking, fmt.Sprintf("created_at 有 %d 行不是毫秒 RFC3339 UTC 文本", plan.NonCanonicalCreatedAt))
	}
	for _, item := range []struct {
		column string
		target *int64
	}{
		{"usage_stats_aggregated_at", &plan.InvalidStatsWatermarks},
		{"client_ip_stats_aggregated_at", &plan.InvalidClientIPWatermark},
	} {
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT count(*) FROM juhe_usage.usage_records
			WHERE %[1]s IS NOT NULL AND %[1]s <> ''
			  AND NOT pg_input_is_valid(%[1]s, 'timestamptz')`, item.column)).Scan(item.target); err != nil {
			return plan, fmt.Errorf("统计 %s 非法值失败: %w", item.column, err)
		}
		if *item.target > 0 {
			plan.Blocking = append(plan.Blocking, fmt.Sprintf("%s 有 %d 行无法解析为 timestamptz", item.column, *item.target))
		}
	}
	return plan, nil
}

func relationPresent(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var present bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p')
		)`, schema, table).Scan(&present); err != nil {
		return false, err
	}
	return present, nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// ApplyPrepare 构建 juhe_usage_migrate.usage_records 影子父表与旧分区对应的
// 新日分区，逐分区复制数据，复制父表索引并校验总行数。全过程幂等：表/分区/
// 索引使用 IF NOT EXISTS，复制使用 ON CONFLICT DO NOTHING。
func ApplyPrepare(ctx context.Context, db *sql.DB) (Plan, error) {
	plan, err := PlanPrepare(ctx, db)
	if err != nil {
		return plan, err
	}
	if !plan.Ready() {
		return plan, fmt.Errorf("usage_records prepare 预检未通过: %v", plan.Blocking)
	}
	columns, err := usageRecordColumns(ctx, db)
	if err != nil {
		return plan, err
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdentifier(TargetSchema)); err != nil {
		return plan, fmt.Errorf("创建影子 schema 失败: %w", err)
	}
	if err := createShadowParent(ctx, db, columns); err != nil {
		return plan, err
	}
	for _, partition := range plan.Partitions {
		start, end, ok := partitionBounds(partition.Bound)
		if !ok {
			return plan, fmt.Errorf("无法解析分区 %s 边界: %s", partition.Name, partition.Bound)
		}
		statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.%s PARTITION OF %s.%s
			FOR VALUES FROM ('%sT00:00:00Z') TO ('%sT00:00:00Z')`,
			quoteIdentifier(TargetSchema), quoteIdentifier(partition.Name),
			quoteIdentifier(TargetSchema), quoteIdentifier(UsageRecordsTable), start, end)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return plan, fmt.Errorf("创建影子分区 %s 失败: %w", partition.Name, err)
		}
		if err := copyPartition(ctx, db, partition.Name, columns); err != nil {
			return plan, err
		}
	}
	if err := copyIndexes(ctx, db); err != nil {
		return plan, err
	}
	var targetRows int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+quoteIdentifier(TargetSchema)+`.`+quoteIdentifier(UsageRecordsTable)).Scan(&targetRows); err != nil {
		return plan, fmt.Errorf("统计影子表行数失败: %w", err)
	}
	if targetRows != plan.TotalRows {
		return plan, fmt.Errorf("影子表行数不一致: source=%d target=%d", plan.TotalRows, targetRows)
	}
	plan.TargetPresent = true
	return plan, nil
}

type columnDef struct {
	Name    string
	SQLType string
	NotNull bool
	Default string
}

func usageRecordColumns(ctx context.Context, db *sql.DB) ([]columnDef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		       COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = 'juhe_usage.usage_records'::regclass
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`)
	if err != nil {
		return nil, fmt.Errorf("读取 usage_records 列定义失败: %w", err)
	}
	defer rows.Close()
	var columns []columnDef
	for rows.Next() {
		var column columnDef
		if err := rows.Scan(&column.Name, &column.SQLType, &column.NotNull, &column.Default); err != nil {
			return nil, err
		}
		switch column.Name {
		case "created_at":
			column.SQLType = "timestamptz(3)"
		case "usage_stats_aggregated_at", "client_ip_stats_aggregated_at":
			column.SQLType = "timestamptz"
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func createShadowParent(ctx context.Context, db *sql.DB, columns []columnDef) error {
	defs := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		def := quoteIdentifier(column.Name) + " " + column.SQLType
		if column.NotNull {
			def += " NOT NULL"
		}
		if column.Default != "" {
			def += " DEFAULT " + column.Default
		}
		defs = append(defs, def)
	}
	defs = append(defs, "PRIMARY KEY (created_at, id)")
	statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.%s (%s) PARTITION BY RANGE (created_at)`,
		quoteIdentifier(TargetSchema), quoteIdentifier(UsageRecordsTable), strings.Join(defs, ", "))
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("创建影子父表失败: %w", err)
	}
	return nil
}

var partitionBoundPattern = regexp.MustCompile(`FROM \('([0-9-]+)'\) TO \('([0-9-]+)'\)`)

func partitionBounds(bound string) (string, string, bool) {
	match := partitionBoundPattern.FindStringSubmatch(bound)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

func copyPartition(ctx context.Context, db *sql.DB, partition string, columns []columnDef) error {
	names := make([]string, 0, len(columns))
	selects := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted := quoteIdentifier(column.Name)
		names = append(names, quoted)
		switch column.Name {
		case "created_at":
			selects = append(selects, quoted+"::timestamptz(3)")
		case "usage_stats_aggregated_at", "client_ip_stats_aggregated_at":
			selects = append(selects, "NULLIF("+quoted+", '')::timestamptz")
		default:
			selects = append(selects, quoted)
		}
	}
	statement := fmt.Sprintf(`INSERT INTO %s.%s (%s) SELECT %s FROM juhe_usage.%s ON CONFLICT DO NOTHING`,
		quoteIdentifier(TargetSchema), quoteIdentifier(UsageRecordsTable),
		strings.Join(names, ", "), strings.Join(selects, ", "), quoteIdentifier(partition))
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("复制分区 %s 失败: %w", partition, err)
	}
	return nil
}

func copyIndexes(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT pg_get_indexdef(i.indexrelid)
		FROM pg_index i
		WHERE i.indrelid = 'juhe_usage.usage_records'::regclass
		ORDER BY i.indexrelid::regclass::text`)
	if err != nil {
		return fmt.Errorf("读取 usage_records 索引定义失败: %w", err)
	}
	defer rows.Close()
	var statements []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return err
		}
		definition = strings.Replace(definition, " ON juhe_usage.usage_records", " ON "+TargetSchema+".usage_records", 1)
		definition = strings.Replace(definition, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ", 1)
		definition = strings.Replace(definition, "CREATE UNIQUE INDEX ", "CREATE UNIQUE INDEX IF NOT EXISTS ", 1)
		statements = append(statements, definition)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("创建影子索引失败: %w", err)
		}
	}
	return nil
}

// ApplySwap 在停写窗口内把影子族换入 juhe_usage，旧族移入
// juhe_usage_retired。父表 SET SCHEMA 不级联分区，必须逐分区先出后进。
func ApplySwap(ctx context.Context, db *sql.DB) error {
	columns, err := usageRecordColumns(ctx, db)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='0'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE juhe_usage.usage_records, juhe_usage_migrate.usage_records IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("锁定 usage_records 新旧族失败: %w", err)
	}
	names := make([]string, 0, len(columns))
	selects := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted := quoteIdentifier(column.Name)
		names = append(names, quoted)
		switch column.Name {
		case "created_at":
			selects = append(selects, quoted+"::timestamptz(3)")
		case "usage_stats_aggregated_at", "client_ip_stats_aggregated_at":
			selects = append(selects, "NULLIF("+quoted+", '')::timestamptz")
		default:
			selects = append(selects, quoted)
		}
	}
	catchup := fmt.Sprintf(`INSERT INTO juhe_usage_migrate.usage_records (%s)
		SELECT %s FROM juhe_usage.usage_records source
		WHERE NOT EXISTS (
			SELECT 1 FROM juhe_usage_migrate.usage_records target
			WHERE target.created_at = source.created_at::timestamptz(3) AND target.id = source.id
		) ON CONFLICT DO NOTHING`, strings.Join(names, ", "), strings.Join(selects, ", "))
	if _, err := tx.ExecContext(ctx, catchup); err != nil {
		return fmt.Errorf("swap 反连接追平失败: %w", err)
	}
	var sourceRows, targetRows int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage.usage_records`).Scan(&sourceRows); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage_migrate.usage_records`).Scan(&targetRows); err != nil {
		return err
	}
	if sourceRows != targetRows {
		return fmt.Errorf("swap 计数校验失败: source=%d target=%d", sourceRows, targetRows)
	}
	oldPartitions, err := childPartitions(ctx, tx, "juhe_usage", UsageRecordsTable)
	if err != nil {
		return err
	}
	newPartitions, err := childPartitions(ctx, tx, TargetSchema, UsageRecordsTable)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_usage_retired`); err != nil {
		return err
	}
	for _, name := range oldPartitions {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_usage.%s SET SCHEMA juhe_usage_retired`, quoteIdentifier(name))); err != nil {
			return fmt.Errorf("旧分区 %s 移入 retired 失败: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE juhe_usage.usage_records SET SCHEMA juhe_usage_retired`); err != nil {
		return fmt.Errorf("旧父表移入 retired 失败: %w", err)
	}
	for _, name := range newPartitions {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_usage_migrate.%s SET SCHEMA juhe_usage`, quoteIdentifier(name))); err != nil {
			return fmt.Errorf("新分区 %s 移入 juhe_usage 失败: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE juhe_usage_migrate.usage_records SET SCHEMA juhe_usage`); err != nil {
		return fmt.Errorf("新父表移入 juhe_usage 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `ANALYZE juhe_usage.usage_records`)
	return nil
}

func childPartitions(ctx context.Context, tx *sql.Tx, schema, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.relname FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.inhparent = ($1 || '.' || $2)::regclass AND n.nspname = $1
		ORDER BY c.relname`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ApplyRetire 在观察期结束后 DROP 旧族 schema；幂等。
func ApplyRetire(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS juhe_usage_retired CASCADE`); err != nil {
		return fmt.Errorf("删除 juhe_usage_retired 失败: %w", err)
	}
	return nil
}
