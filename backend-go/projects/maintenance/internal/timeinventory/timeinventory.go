// Package timeinventory 是时间类型迁移的只读清单工具。
//
// 它扫描 information_schema / pg_catalog，输出每个时间相关列的目标类型、
// 精度建议和依赖（主键/分区键/索引/约束/触发器）。工具只读，不执行任何
// DDL；真正的 ALTER TYPE 由后续 schema revision 步骤承载。
package timeinventory

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Queryer 是 *sql.DB / *sql.Tx 共有的只读查询面。
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Target 是列的目标类型建议。
type Target struct {
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Precision int    `json:"precision,omitempty"`
	Review    bool   `json:"review"`
	Reason    string `json:"reason,omitempty"`
}

// Dependencies 是列被哪些对象引用。
type Dependencies struct {
	PrimaryKey      bool     `json:"primaryKey"`
	PartitionKey    bool     `json:"partitionKey"`
	Indexes         []string `json:"indexes,omitempty"`
	Constraints     []string `json:"constraints,omitempty"`
	Triggers        []string `json:"triggers,omitempty"`
	ReferencedByFKs []string `json:"referencedByForeignKeys,omitempty"`
	Views           []string `json:"views,omitempty"`
}

// Column 是单列清单项。
type Column struct {
	Schema               string       `json:"schema"`
	Table                string       `json:"table"`
	Column               string       `json:"column"`
	DataType             string       `json:"dataType"`
	UDT                  string       `json:"udt"`
	Nullable             bool         `json:"nullable"`
	Default              string       `json:"default,omitempty"`
	IsGenerated          bool         `json:"isGenerated"`
	GenerationExpression string       `json:"generationExpression,omitempty"`
	Target               Target       `json:"target"`
	Dependencies         Dependencies `json:"dependencies"`
}

// Summary 是清单聚合。
type Summary struct {
	Total              int `json:"total"`
	AlreadyTimestamptz int `json:"alreadyTimestamptz"`
	InstantText        int `json:"instantText"`
	CalendarDate       int `json:"calendarDate"`
	CalendarHour       int `json:"calendarHour"`
	KeepText           int `json:"keepText"`
	EpochMS            int `json:"epochMs"`
	Review             int `json:"review"`
}

// Report 是命令输出。
type Report struct {
	GeneratedAt string   `json:"generatedAt"`
	Database    string   `json:"database"`
	Schemas     []string `json:"schemas"`
	Summary     Summary  `json:"summary"`
	Columns     []Column `json:"columns"`
}

// DefaultSchemas 是迁移范围涉及的 schema。
var DefaultSchemas = []string{
	"juhe_business", "juhe_usage", "juhe_stats", "juhe_dataset", "juhe_jobs", "juhe_j3b",
}

const columnsQuery = `
SELECT c.table_schema, c.table_name, c.column_name, c.data_type, c.udt_name,
       (c.is_nullable = 'YES') AS is_nullable,
       COALESCE(c.column_default, '') AS column_default,
       c.is_generated,
       COALESCE(c.generation_expression, '') AS generation_expression,
       c.datetime_precision
FROM information_schema.columns c
WHERE c.table_schema = ANY($1::text[])
  AND (
    c.data_type IN ('text', 'timestamp with time zone', 'date', 'timestamp without time zone')
    OR (c.data_type = 'bigint' AND c.column_name ~ '_ms$')
  )
  AND (
    c.column_name ~ '(_at|_until|expires|deadline|_date|_month|_week|_hour|_minute|_time|_ms)$'
    OR c.column_name IN ('converged_at','started_at','applied_at','observed_at','stored_at',
                          'window_key','bucket_key','release_date','shutdown_date','cursor_id')
    OR c.column_name ~ '_sort_key$'
  )
ORDER BY c.table_schema, c.table_name, c.column_name`

const constraintsQuery = `
SELECT n.nspname, rel.relname, con.conname, con.contype, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class rel ON rel.oid = con.conrelid
JOIN pg_namespace n ON n.oid = rel.relnamespace
WHERE n.nspname = ANY($1::text[])
ORDER BY n.nspname, rel.relname, con.conname`

const indexesQuery = `
SELECT schemaname, tablename, indexname, indexdef
FROM pg_indexes
WHERE schemaname = ANY($1::text[])
ORDER BY schemaname, tablename, indexname`

const triggersQuery = `
SELECT n.nspname, rel.relname, t.tgname, pg_get_triggerdef(t.oid)
FROM pg_trigger t
JOIN pg_class rel ON rel.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = rel.relnamespace
WHERE NOT t.tgisinternal
  AND n.nspname = ANY($1::text[])
ORDER BY n.nspname, rel.relname, t.tgname`

const partitionQuery = `
SELECT n.nspname, rel.relname, pg_get_partkeydef(rel.oid)
FROM pg_partitioned_table pt
JOIN pg_class rel ON rel.oid = pt.partrelid
JOIN pg_namespace n ON n.oid = rel.relnamespace
WHERE n.nspname = ANY($1::text[])
ORDER BY n.nspname, rel.relname`

// foreignKeyReferencesQuery 找出“其他表的外键指向本表列”的依赖。
// 这些列改类型时 PostgreSQL 会拒绝（外键两端类型必须可比较），必须在
// 计划阶段拦下，而不是在停写窗口持锁失败。
const foreignKeyReferencesQuery = `
SELECT DISTINCT parent_ns.nspname, parent.relname, att.attname,
       child_ns.nspname || '.' || child.relname || '.' || con.conname
FROM pg_constraint con
JOIN pg_class child ON child.oid = con.conrelid
JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
JOIN pg_class parent ON parent.oid = con.confrelid
JOIN pg_namespace parent_ns ON parent_ns.oid = parent.relnamespace
JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS key(attnum, ord) ON true
JOIN pg_attribute att ON att.attrelid = parent.oid AND att.attnum = key.attnum
WHERE con.contype = 'f'
  AND parent_ns.nspname = ANY($1::text[])
ORDER BY 1, 2, 3, 4`

// viewReferencesQuery 找出视图/物化视图对列的直接依赖；改列类型会被
// PostgreSQL 拒绝（视图定义仍按旧类型解析）。
const viewReferencesQuery = `
SELECT DISTINCT table_ns.nspname, table_rel.relname, att.attname,
       view_ns.nspname || '.' || view.relname
FROM pg_depend dep
JOIN pg_rewrite rewrite ON rewrite.oid = dep.objid
JOIN pg_class view ON view.oid = rewrite.ev_class
JOIN pg_namespace view_ns ON view_ns.oid = view.relnamespace
JOIN pg_class table_rel ON table_rel.oid = dep.refobjid
JOIN pg_namespace table_ns ON table_ns.oid = table_rel.relnamespace
JOIN pg_attribute att ON att.attrelid = table_rel.oid AND att.attnum = dep.refobjsubid
WHERE dep.classid = 'pg_rewrite'::regclass
  AND dep.refclassid = 'pg_class'::regclass
  AND dep.refobjsubid > 0
  AND view.relkind IN ('v', 'm')
  AND table_ns.nspname = ANY($1::text[])
ORDER BY 1, 2, 3, 4`

type relationDependency struct {
	name string
	kind string
	def  string
}

// Collect 只读收集清单。调用方应传入只读事务句柄。
func Collect(ctx context.Context, queryer Queryer, schemas []string) (Report, error) {
	if len(schemas) == 0 {
		schemas = DefaultSchemas
	}
	report := Report{
		GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Schemas:     append([]string{}, schemas...),
	}
	if err := queryer.QueryRowContext(ctx, `SELECT current_database()`).Scan(&report.Database); err != nil {
		return Report{}, fmt.Errorf("读取当前数据库失败: %w", err)
	}
	columns, err := collectColumns(ctx, queryer, schemas)
	if err != nil {
		return Report{}, err
	}
	dependencies, err := collectDependencies(ctx, queryer, schemas)
	if err != nil {
		return Report{}, err
	}
	for index := range columns {
		applyDependencies(&columns[index], dependencies)
		report.Summary.add(columns[index])
	}
	report.Summary.Total = len(columns)
	report.Columns = columns
	return report, nil
}

func collectColumns(ctx context.Context, queryer Queryer, schemas []string) ([]Column, error) {
	rows, err := queryer.QueryContext(ctx, columnsQuery, schemas)
	if err != nil {
		return nil, fmt.Errorf("读取 information_schema.columns 失败: %w", err)
	}
	defer rows.Close()
	var result []Column
	for rows.Next() {
		var (
			column      Column
			isNullable  bool
			isGenerated string
			precision   sql.NullInt64
		)
		if err := rows.Scan(
			&column.Schema, &column.Table, &column.Column, &column.DataType, &column.UDT,
			&isNullable, &column.Default, &isGenerated, &column.GenerationExpression, &precision,
		); err != nil {
			return nil, fmt.Errorf("解码时间列清单失败: %w", err)
		}
		column.Nullable = isNullable
		column.IsGenerated = strings.EqualFold(isGenerated, "ALWAYS")
		column.Target = Classify(column.Schema, column.Table, column.Column, column.DataType, precision)
		result = append(result, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Schema != result[right].Schema {
			return result[left].Schema < result[right].Schema
		}
		if result[left].Table != result[right].Table {
			return result[left].Table < result[right].Table
		}
		return result[left].Column < result[right].Column
	})
	return result, nil
}

func collectDependencies(ctx context.Context, queryer Queryer, schemas []string) (map[string][]relationDependency, error) {
	dependencies := map[string][]relationDependency{}
	appendDependency := func(schema, table, name, kind, def string) {
		key := schema + "." + table
		dependencies[key] = append(dependencies[key], relationDependency{name: name, kind: kind, def: def})
	}
	if err := forEachRow(ctx, queryer, constraintsQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, name, kind, def string
		if err := rows.Scan(&schema, &table, &name, &kind, &def); err != nil {
			return err
		}
		appendDependency(schema, table, name, "constraint:"+kind, def)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取约束依赖失败: %w", err)
	}
	if err := forEachRow(ctx, queryer, indexesQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, name, def string
		if err := rows.Scan(&schema, &table, &name, &def); err != nil {
			return err
		}
		appendDependency(schema, table, name, "index", def)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取索引依赖失败: %w", err)
	}
	if err := forEachRow(ctx, queryer, triggersQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, name, def string
		if err := rows.Scan(&schema, &table, &name, &def); err != nil {
			return err
		}
		appendDependency(schema, table, name, "trigger", def)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取触发器依赖失败: %w", err)
	}
	if err := forEachRow(ctx, queryer, partitionQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, def string
		if err := rows.Scan(&schema, &table, &def); err != nil {
			return err
		}
		appendDependency(schema, table, "partition_key", "partition", def)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取分区键依赖失败: %w", err)
	}
	if err := forEachRow(ctx, queryer, foreignKeyReferencesQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, column, child string
		if err := rows.Scan(&schema, &table, &column, &child); err != nil {
			return err
		}
		appendDependency(schema, table, child, "referenced_by_fk", column)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取外键引用依赖失败: %w", err)
	}
	if err := forEachRow(ctx, queryer, viewReferencesQuery, schemas, func(rows *sql.Rows) error {
		var schema, table, column, view string
		if err := rows.Scan(&schema, &table, &column, &view); err != nil {
			return err
		}
		appendDependency(schema, table, view, "view", column)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("读取视图依赖失败: %w", err)
	}
	return dependencies, nil
}

func forEachRow(ctx context.Context, queryer Queryer, query string, args []string, fn func(*sql.Rows) error) error {
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func applyDependencies(column *Column, dependencies map[string][]relationDependency) {
	for _, dependency := range dependencies[column.Schema+"."+column.Table] {
		if !referencesIdentifier(dependency.def, column.Column) {
			continue
		}
		switch {
		case dependency.kind == "partition":
			column.Dependencies.PartitionKey = true
		case dependency.kind == "constraint:p":
			column.Dependencies.PrimaryKey = true
			column.Dependencies.Constraints = append(column.Dependencies.Constraints, dependency.name)
		case strings.HasPrefix(dependency.kind, "constraint:"):
			column.Dependencies.Constraints = append(column.Dependencies.Constraints, dependency.name)
		case dependency.kind == "index":
			column.Dependencies.Indexes = append(column.Dependencies.Indexes, dependency.name)
		case dependency.kind == "trigger":
			column.Dependencies.Triggers = append(column.Dependencies.Triggers, dependency.name)
		case dependency.kind == "referenced_by_fk":
			column.Dependencies.ReferencedByFKs = append(column.Dependencies.ReferencedByFKs, dependency.name)
		case dependency.kind == "view":
			column.Dependencies.Views = append(column.Dependencies.Views, dependency.name)
		}
	}
	sort.Strings(column.Dependencies.Indexes)
	sort.Strings(column.Dependencies.Constraints)
	sort.Strings(column.Dependencies.Triggers)
	sort.Strings(column.Dependencies.ReferencedByFKs)
	sort.Strings(column.Dependencies.Views)
}

func referencesIdentifier(definition, column string) bool {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(column) + `\b`).MatchString(definition)
}

// Classify 给出列的目标类型建议。规则集中在纯函数里，便于单测与评审。
func Classify(schema, table, column, dataType string, precision sql.NullInt64) Target {
	switch dataType {
	case "timestamp with time zone":
		return Target{Type: formatPrecision("timestamptz", precision), Kind: "instant"}
	case "date":
		return Target{Type: "date", Kind: "calendar_date"}
	case "timestamp without time zone":
		if precision.Valid {
			return Target{Type: fmt.Sprintf("timestamp(%d) without time zone", precision.Int64), Kind: "calendar_hour", Precision: int(precision.Int64)}
		}
		return Target{Type: "timestamp without time zone", Kind: "calendar_hour"}
	}

	if dataType == "bigint" && strings.HasSuffix(column, "_ms") {
		return Target{Type: "timestamptz(6)", Kind: "epoch_ms", Precision: 6, Review: true,
			Reason: "epoch 毫秒运行态；迁移前需确认 SQL 算术/CAS/Redis 协议语义"}
	}
	if retiredTables[table] {
		return Target{Type: "text", Kind: "exception", Reason: "revision 23 已退役 usage shard 目录表"}
	}

	if isLedgerException(schema, table, column) {
		return Target{Type: "text", Kind: "exception", Reason: "升级账本引导期例外"}
	}
	if strings.HasSuffix(column, "_sort_key") {
		return Target{Type: "text", Kind: "exception", Reason: "时间派生文本排序键；随源列迁移同步改写触发器/投影"}
	}
	if keepTextColumns[column] {
		return Target{Type: "text", Kind: "exception", Reason: "非事实时间或不透明窗口/目录键"}
	}
	if calendarDateColumns[column] {
		return Target{Type: "date", Kind: "calendar_date"}
	}
	if calendarHourColumns[column] {
		return Target{Type: "timestamp(0) without time zone", Kind: "calendar_hour", Precision: 0}
	}
	if watermarkColumns[column] {
		return Target{Type: "timestamptz(6)", Kind: "instant", Precision: 6,
			Reason: "统计消费水位；只做 NULL/先后比较"}
	}
	if microsTables[table] {
		return Target{Type: "timestamptz(6)", Kind: "instant", Precision: 6,
			Reason: "现有微秒/revision/租约契约"}
	}
	if microsInstantColumns[column] {
		return Target{Type: "timestamptz(6)", Kind: "instant", Precision: 6,
			Reason: "运行态租约/CAS 列；保留微秒避免围栏精度漂移"}
	}
	if millisecondColumns[column] {
		return Target{Type: "timestamptz(3)", Kind: "instant", Precision: 3}
	}
	if strings.HasSuffix(column, "_at") || strings.HasSuffix(column, "_until") ||
		strings.HasSuffix(column, "_time") || strings.Contains(column, "expires") ||
		strings.Contains(column, "deadline") {
		return Target{Type: "timestamptz(3)", Kind: "instant", Precision: 3, Review: true,
			Reason: "默认毫秒；需确认是否有微秒/revision/外部契约"}
	}
	if strings.HasSuffix(column, "_date") || strings.HasSuffix(column, "_on") {
		return Target{Type: "date", Kind: "calendar_date", Review: true,
			Reason: "日期语义待确认（date-only 还是外部目录文本）"}
	}
	return Target{Type: "text", Kind: "unknown", Review: true, Reason: "未识别的时间语义"}
}

func formatPrecision(base string, precision sql.NullInt64) string {
	if !precision.Valid {
		return base
	}
	return fmt.Sprintf("%s(%d)", base, precision.Int64)
}

func (s *Summary) add(column Column) {
	switch column.Target.Kind {
	case "instant":
		if column.DataType == "timestamp with time zone" {
			s.AlreadyTimestamptz++
		} else {
			s.InstantText++
		}
	case "epoch_ms":
		s.EpochMS++
	case "calendar_date":
		s.CalendarDate++
	case "calendar_hour":
		s.CalendarHour++
	case "exception":
		s.KeepText++
	}
	if column.Target.Review {
		s.Review++
	}
}

var calendarDateColumns = map[string]bool{
	"stat_date": true, "stat_week": true, "stat_month": true,
	"min_stat_date": true, "max_stat_date": true, "min_changed_date": true,
}

var calendarHourColumns = map[string]bool{
	"stat_hour": true, "stat_minute": true,
}

var keepTextColumns = map[string]bool{
	"start_date": true, "end_date": true,
	"window_key": true, "bucket_key": true,
	"release_date": true, "shutdown_date": true,
	"cursor_id": true, "expires_after_anchor": true,
}

var watermarkColumns = map[string]bool{
	"usage_stats_aggregated_at":     true,
	"client_ip_stats_aggregated_at": true,
}

var microsTables = map[string]bool{
	"proxy_profiles":        true,
	"audit_logs":            true,
	"operation_logs":        true,
	"background_task_runs":  true,
	"background_job_leases": true,
}

var millisecondColumns = map[string]bool{
	"created_at": true, "updated_at": true, "expires_at": true,
	"last_used_at": true, "last_error_at": true, "last_success_at": true,
	"last_attempt_at": true, "last_failure_at": true, "last_health_check_at": true,
	"next_health_check_at": true, "next_refresh_after": true, "cooldown_until": true,
	"first_dirty_at": true, "cursor_created_at": true, "snapshot_at": true,
	"observed_at": true, "stored_at": true, "started_at": true, "ended_at": true,
	"finished_at": true, "completed_at": true, "deadline_at": true,
	"deleted_at": true, "revoked_at": true, "last_seen_at": true,
	"last_observed_at": true, "applied_at": true, "executed_at": true,
	"availability_schedule_next_check_at": true, "activated_at": true,
	"last_sample_at": true, "first_seen_at": true, "first_created_at": true,
	"account_expires_at": true, "balance_query_next_refresh_at": true,
	"next_transition_at": true, "cooldown_retest_last_at": true,
	"cooldown_retest_observation_started_at": true, "health_check_failure_started_at": true,
	"last_health_success_at": true, "oauth_access_token_expires_at": true,
	"stream_failure_window_started_at": true, "read_at": true, "published_at": true,
	"reviewed_at": true, "last_run_at": true, "next_run_at": true,
	"last_source_changed_at": true, "last_login_at": true, "joined_at": true,
	"removed_at": true, "aggregation_completed_at": true, "first_observed_at": true,
	"observation_created_at": true, "processed_at": true, "window_started_at": true,
	"window_ended_at": true, "disabled_at": true, "last_hit_at": true,
	"last_processed_at": true, "last_requested_at": true, "shard_deleted_at": true,
	"stats_subtracted_at": true, "indexed_at": true, "last_write_at": true,
}

// microsInstantColumns 是运行态租约/CAS 列：默认保留微秒，避免围栏在
// timestamptz(3) 下丢失亚毫秒精度。
var microsInstantColumns = map[string]bool{
	"available_at": true, "claimed_until": true, "reserved_at": true,
	"projected_at": true, "oldest_projected_at": true, "next_reconcile_at": true,
	"incident_started_at": true, "cleared_at": true, "recovery_due_at": true,
	"recovery_lease_until": true, "probe_claimed_until": true,
	"recovery_started_at": true, "next_probe_at": true, "last_probe_at": true,
	"lease_until": true,
}

var retiredTables = map[string]bool{
	"usage_record_shard_entries":  true,
	"usage_record_account_shards": true,
	"usage_record_api_key_shards": true,
	"usage_record_shards":         true,
}

func isLedgerException(schema, table, column string) bool {
	if schema != "juhe_business" {
		return false
	}
	if table != "schema_state" && table != "schema_migrations" {
		return false
	}
	return column == "converged_at" || column == "started_at" || column == "applied_at"
}
