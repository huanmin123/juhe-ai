package instantmigration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// rfc3339TextPattern 与 timefmt 的绝对时间契约一致：必须带 Z 或数值 offset，
// 小数 0-9 位；NULL 允许（可空列）。分隔符允许 T 或空格：历史行可能由
// PostgreSQL now() 直接写入 text 列，渲染为空格分隔但仍带 offset，::timestamptz
// 可无损解析；offset 允许 +08 或 +08:00（PostgreSQL 整点时区输出不带分钟）。
// 新写入仍统一 T + 完整 offset。字符类先挡住月/日/时/分/秒的明显越界，
// 月日联动（如 02-30）与 offset 越界再由 pg_input_is_valid 兜底。
const rfc3339TextPattern = `^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])[T ]([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?(Z|[+-][0-9]{2}(:[0-9]{2})?)$`

// invalidValuePredicate 返回“值非法”的 SQL 条件。除形状外使用
// pg_input_is_valid 做 PostgreSQL 语义校验（PG16+；生产 PG18、dev PG16），
// 避免 2026-02-30 这类形状合法但 cast 必然失败的值混过预检、在持锁重写时
// 才报错。函数不存在时预检直接失败，仍在加锁前，保持 fail-closed。
func invalidValuePredicate(column string) string {
	quoted := QuoteIdentifier(column)
	return fmt.Sprintf(`%s IS NOT NULL AND (btrim(%s) = '' OR %s !~ $1 OR NOT pg_input_is_valid(%s, 'timestamptz'))`,
		quoted, quoted, quoted, quoted)
}

// PreflightBatch 按表合并预检：同一张表的多个列只扫描一次，避免 accounts
// 这类宽表在批次内被反复全扫。任何一列存在非法值即中止。
func PreflightBatch(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, actions []Action) error {
	groups := map[string][]Action{}
	for _, action := range actions {
		key := action.Schema + "." + action.Table
		groups[key] = append(groups[key], action)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		selects := make([]string, len(group))
		for index, action := range group {
			selects[index] = fmt.Sprintf("count(*) FILTER (WHERE %s) AS c%d", invalidValuePredicate(action.Column), index)
		}
		table := QuoteIdentifier(group[0].Schema) + "." + QuoteIdentifier(group[0].Table)
		query := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selects, ", "), table)
		counts := make([]int64, len(group))
		dest := make([]any, len(group))
		for index := range counts {
			dest[index] = &counts[index]
		}
		if err := queryer.QueryRowContext(ctx, query, rfc3339TextPattern).Scan(dest...); err != nil {
			return fmt.Errorf("预检 %s 失败: %w", key, err)
		}
		var failures []string
		for index, count := range counts {
			if count > 0 {
				failures = append(failures, fmt.Sprintf("%s(%d; 样例: %s)",
					group[index].Column, count, invalidSamples(ctx, queryer, table, group[index].Column)))
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s 存在非法时间值（空串、无 offset、非 RFC3339 或日历越界）: %s", key, strings.Join(failures, ", "))
		}
	}
	return nil
}

// invalidSamples 读取少量非法值用于诊断；失败不影响主预检结论。
func invalidSamples(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, table, column string) string {
	var samples sql.NullString
	query := fmt.Sprintf(`SELECT string_agg(value, ', ') FROM (
		SELECT %s::text AS value FROM %s WHERE %s LIMIT 3
	) sample`, QuoteIdentifier(column), table, invalidValuePredicate(column))
	if err := queryer.QueryRowContext(ctx, query, rfc3339TextPattern).Scan(&samples); err != nil {
		return "<读取失败>"
	}
	if !samples.Valid {
		return "<无>"
	}
	return samples.String
}

// Preflight 在加锁前扫描 text 列，拒绝空串和不符合 RFC3339 的存量值。
// 这是 fail-closed：宁可中止迁移并列出样例，也不让 ALTER TYPE 在持有
// 表锁时失败。
func Preflight(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, action Action) error {
	table := QuoteIdentifier(action.Schema) + "." + QuoteIdentifier(action.Table)
	column := QuoteIdentifier(action.Column)
	invalid := invalidValuePredicate(action.Column)
	var count int64
	if err := queryer.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`, table, invalid), rfc3339TextPattern).Scan(&count); err != nil {
		return fmt.Errorf("预检 %s.%s.%s 失败: %w", action.Schema, action.Table, action.Column, err)
	}
	if count == 0 {
		return nil
	}
	var samples string
	if err := queryer.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT string_agg(value, ', ') FROM (
			SELECT %s::text AS value FROM %s WHERE %s LIMIT 5
		) sample`, column, table, invalid), rfc3339TextPattern).Scan(&samples); err != nil {
		return fmt.Errorf("%s.%s.%s 有 %d 行非法时间值（样例读取失败: %v）",
			action.Schema, action.Table, action.Column, count, err)
	}
	return fmt.Errorf("%s.%s.%s 有 %d 行非法时间值（空串、无 offset、非 RFC3339 或日历越界）；样例: %s",
		action.Schema, action.Table, action.Column, count, samples)
}
