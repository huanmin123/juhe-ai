// Package instantmigration 把 timeinventory 清单转成 Phase 2 的非分区
// instant 列迁移计划。
//
// 本包只生成计划与 SQL，不执行 DDL；真正的 ALTER TYPE 由后续 schema
// revision 步骤在停写窗口内调用 Apply。这样本地可以做 dry-run、评审和
// 真实 PG 回滚测试，但不会绕过升级账本。
package instantmigration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/timeinventory"
)

// Action 是单列迁移动作。
type Action struct {
	Schema       string                     `json:"schema"`
	Table        string                     `json:"table"`
	Column       string                     `json:"column"`
	Batch        string                     `json:"batch"`
	CurrentType  string                     `json:"currentType"`
	TargetType   string                     `json:"targetType"`
	SQL          string                     `json:"sql"`
	Dependencies timeinventory.Dependencies `json:"dependencies"`
	Warnings     []string                   `json:"warnings,omitempty"`
}

const (
	BatchSmallTables = "small_tables"
	BatchCRUD        = "crud"
	BatchAccounts    = "accounts"
	BatchDatasetJobs = "dataset_jobs"
)

// BatchOf 按计划的分批锚点给出批次名；这是执行顺序建议，不代表 revision。
func BatchOf(schema, table string) string {
	switch schema {
	case "juhe_dataset", "juhe_j3b":
		return BatchDatasetJobs
	case "juhe_stats":
		return BatchDatasetJobs
	}
	switch {
	case table == "accounts", strings.HasPrefix(table, "account_"), strings.HasPrefix(table, "proxy_"),
		table == "group_accounts":
		return BatchAccounts
	case table == "api_keys", strings.HasPrefix(table, "api_key_"),
		table == "groups", strings.HasPrefix(table, "group_"),
		table == "route_strategies", strings.HasPrefix(table, "route_strategy"),
		table == "security_policies", table == "scheduling_preferences",
		strings.HasPrefix(table, "announcement"),
		strings.HasPrefix(table, "system_team"), strings.HasPrefix(table, "resource_authorization"):
		return BatchCRUD
	default:
		return BatchSmallTables
	}
}

// Skipped 是被排除的列及原因。
type Skipped struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
	Reason string `json:"reason"`
}

// Plan 是 Phase 2 的 dry-run 输出。
type Plan struct {
	Actions []Action  `json:"actions"`
	Skipped []Skipped `json:"skipped"`
}

// Scope 决定列是否属于当前迁移阶段。
type Scope func(schema, table string) bool

// AllScope 包含所有表，仅用于诊断/全量清单。
func AllScope(_, _ string) bool { return true }

// Phase2Scope 是非分区业务表阶段：juhe_stats 只放行 Phase 2 批次内的
// background_* 运行表；juhe_usage 整体留给 Phase 4 的分区族重建。
func Phase2Scope(schema, table string) bool {
	switch schema {
	case "juhe_stats":
		return table == "background_task_runs" || table == "background_job_leases"
	case "juhe_usage":
		return false
	default:
		return true
	}
}

// BuildPlan 从 inventory 报告选择可迁移的 text instant 列。
// 分区键列留给 Phase 4，生成列、review 未确认列默认排除。
func BuildPlan(report timeinventory.Report, includeReview bool, scope Scope) Plan {
	if scope == nil {
		scope = AllScope
	}
	plan := Plan{}
	for _, column := range report.Columns {
		if column.DataType != "text" || column.Target.Kind != "instant" {
			continue
		}
		if !scope(column.Schema, column.Table) {
			plan.Skipped = append(plan.Skipped, skipped(column, "不在当前阶段范围（Phase 3/4 处理）"))
			continue
		}
		switch {
		case column.Dependencies.PartitionKey:
			plan.Skipped = append(plan.Skipped, skipped(column, "分区键列；Phase 4 整族重建"))
			continue
		case column.IsGenerated:
			plan.Skipped = append(plan.Skipped, skipped(column, "生成列；需先改生成表达式"))
			continue
		case column.Default != "":
			plan.Skipped = append(plan.Skipped, skipped(column, "列有默认值；需先改默认表达式，避免 ALTER TYPE 转换默认值失败"))
			continue
		case len(column.Dependencies.ReferencedByFKs) > 0:
			plan.Skipped = append(plan.Skipped, skipped(column, "被外键引用；需先处理引用列/约束（"+strings.Join(column.Dependencies.ReferencedByFKs, ", ")+"）"))
			continue
		case len(column.Dependencies.Views) > 0:
			plan.Skipped = append(plan.Skipped, skipped(column, "被视图/物化视图引用；需先改视图定义（"+strings.Join(column.Dependencies.Views, ", ")+"）"))
			continue
		case column.Target.Review && !includeReview:
			plan.Skipped = append(plan.Skipped, skipped(column, "review=true；需先确认精度/语义"))
			continue
		case column.Target.Type == "" || column.Target.Type == "text":
			plan.Skipped = append(plan.Skipped, skipped(column, "无 timestamptz 目标类型"))
			continue
		}
		plan.Actions = append(plan.Actions, Action{
			Schema:       column.Schema,
			Table:        column.Table,
			Column:       column.Column,
			Batch:        BatchOf(column.Schema, column.Table),
			CurrentType:  column.DataType,
			TargetType:   column.Target.Type,
			SQL:          AlterSQL(column),
			Dependencies: column.Dependencies,
			Warnings:     warnings(column),
		})
	}
	batchOrder := map[string]int{
		BatchSmallTables: 0, BatchCRUD: 1, BatchAccounts: 2, BatchDatasetJobs: 3,
	}
	sort.Slice(plan.Actions, func(left, right int) bool {
		leftOrder := batchOrder[plan.Actions[left].Batch]
		rightOrder := batchOrder[plan.Actions[right].Batch]
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if plan.Actions[left].Schema != plan.Actions[right].Schema {
			return plan.Actions[left].Schema < plan.Actions[right].Schema
		}
		if plan.Actions[left].Table != plan.Actions[right].Table {
			return plan.Actions[left].Table < plan.Actions[right].Table
		}
		return plan.Actions[left].Column < plan.Actions[right].Column
	})
	sort.Slice(plan.Skipped, func(left, right int) bool {
		if plan.Skipped[left].Schema != plan.Skipped[right].Schema {
			return plan.Skipped[left].Schema < plan.Skipped[right].Schema
		}
		if plan.Skipped[left].Table != plan.Skipped[right].Table {
			return plan.Skipped[left].Table < plan.Skipped[right].Table
		}
		return plan.Skipped[left].Column < plan.Skipped[right].Column
	})
	return plan
}

// TextToInstantAction 构造一个 text -> timestamptz 的单列迁移动作。
// 供 revision 注册表静态声明列清单，避免手写 ALTER SQL 与计划器漂移。
func TextToInstantAction(schema, table, column, targetType string) Action {
	return Action{
		Schema:      schema,
		Table:       table,
		Column:      column,
		Batch:       BatchOf(schema, table),
		CurrentType: "text",
		TargetType:  targetType,
		SQL: fmt.Sprintf(`ALTER TABLE %s.%s ALTER COLUMN %s TYPE %s USING %s::timestamptz`,
			QuoteIdentifier(schema), QuoteIdentifier(table), QuoteIdentifier(column),
			targetType, QuoteIdentifier(column)),
	}
}

// AlterSQL 生成单列 ALTER TYPE SQL。调用方必须在事务内先设置
// `SET LOCAL timezone='UTC'` 与 `SET LOCAL lock_timeout`。
func AlterSQL(column timeinventory.Column) string {
	return fmt.Sprintf(`ALTER TABLE %s.%s ALTER COLUMN %s TYPE %s USING %s::timestamptz`,
		QuoteIdentifier(column.Schema), QuoteIdentifier(column.Table),
		QuoteIdentifier(column.Column), column.Target.Type, QuoteIdentifier(column.Column))
}

// Apply 在调用方事务内执行单列 ALTER TYPE；失败即返回，由调用方回滚。
func Apply(ctx context.Context, tx *sql.Tx, action Action) error {
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return fmt.Errorf("设置 lock_timeout 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, action.SQL); err != nil {
		return fmt.Errorf("迁移 %s.%s.%s 失败: %w", action.Schema, action.Table, action.Column, err)
	}
	return nil
}

// Verify 复核列已是 timestamp with time zone，且精度符合目标。
func Verify(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, action Action) error {
	var dataType string
	var precision sql.NullInt64
	if err := queryer.QueryRowContext(ctx, `
		SELECT data_type, datetime_precision
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		action.Schema, action.Table, action.Column).Scan(&dataType, &precision); err != nil {
		return fmt.Errorf("读取 %s.%s.%s 类型失败: %w", action.Schema, action.Table, action.Column, err)
	}
	if dataType != "timestamp with time zone" {
		return fmt.Errorf("%s.%s.%s 类型 = %s, want timestamp with time zone", action.Schema, action.Table, action.Column, dataType)
	}
	if expected, ok := precisionFromType(action.TargetType); ok {
		if !precision.Valid || int(precision.Int64) != expected {
			return fmt.Errorf("%s.%s.%s 精度 = %v, want %d", action.Schema, action.Table, action.Column, precision.Int64, expected)
		}
	}
	return nil
}

func skipped(column timeinventory.Column, reason string) Skipped {
	return Skipped{Schema: column.Schema, Table: column.Table, Column: column.Column, Reason: reason}
}

func warnings(column timeinventory.Column) []string {
	var result []string
	if column.Dependencies.PrimaryKey {
		result = append(result, "主键包含该列；ALTER TYPE 会重建主键索引")
	}
	if len(column.Dependencies.Indexes) > 0 {
		result = append(result, fmt.Sprintf("%d 个索引引用该列；会随表重写重建", len(column.Dependencies.Indexes)))
	}
	if len(column.Dependencies.Constraints) > 0 {
		result = append(result, fmt.Sprintf("%d 个约束引用该列；会重新校验", len(column.Dependencies.Constraints)))
	}
	if len(column.Dependencies.Triggers) > 0 {
		result = append(result, fmt.Sprintf("%d 个触发器引用该列；需核对触发器函数", len(column.Dependencies.Triggers)))
	}
	if column.Default != "" {
		result = append(result, "列有默认值；ALTER TYPE 可能要求先 DROP DEFAULT")
	}
	return result
}

func precisionFromType(target string) (int, bool) {
	open := strings.Index(target, "(")
	close := strings.Index(target, ")")
	if open < 0 || close <= open {
		return 0, false
	}
	value := 0
	for _, ch := range target[open+1 : close] {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		value = value*10 + int(ch-'0')
	}
	return value, true
}

// QuoteIdentifier 引用 SQL 标识符；列名来自 catalog，仍按最小权限转义。
func QuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
