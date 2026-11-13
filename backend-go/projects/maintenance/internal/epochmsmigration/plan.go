// Package epochmsmigration 是 Phase 5（锁/熔断运行态 epoch 毫秒瞬时列迁
// timestamptz(6)）的只读预检。DDL 由 Revision 32 在停写窗口执行。
package epochmsmigration

import (
	"context"
	"database/sql"
	"fmt"
)

type Column struct {
	Table  string
	Column string
}

var Columns = []Column{
	{"account_lock_states", "next_retry_at_ms"},
	{"account_lock_states", "lease_until_ms"},
	{"account_circuit_incidents", "open_until_ms"},
	{"account_circuit_incidents", "next_transition_at_ms"},
	{"account_circuit_incidents", "lease_until_ms"},
	{"account_circuit_incidents", "attempt_started_at_ms"},
	{"account_circuit_incidents", "attempt_hard_deadline_ms"},
	{"account_circuit_incidents", "retained_until_ms"},
	{"account_circuit_incidents", "created_at_ms"},
	{"account_circuit_incidents", "updated_at_ms"},
	{"account_circuit_outbox", "available_at_ms"},
	{"account_circuit_outbox", "claim_until_ms"},
	{"account_circuit_outbox", "acknowledged_at_ms"},
	{"account_circuit_outbox", "created_at_ms"},
	{"account_circuit_outbox", "updated_at_ms"},
	{"account_list_availability_dirty", "available_at_ms"},
	{"account_list_availability_dirty", "claim_until_ms"},
	{"account_list_availability_dirty", "created_at_ms"},
	{"account_list_availability_dirty", "updated_at_ms"},
}

const MaxEpochMillis = int64(4102444800000) // 2100-01-01T00:00:00Z

type ColumnReport struct {
	Table      string   `json:"table"`
	Column     string   `json:"column"`
	Present    bool     `json:"present"`
	Type       string   `json:"type,omitempty"`
	Nullable   bool     `json:"nullable"`
	Rows       int64    `json:"rows"`
	Nulls      int64    `json:"nulls"`
	Min        *int64   `json:"min,omitempty"`
	Max        *int64   `json:"max,omitempty"`
	OutOfRange int64    `json:"outOfRange"`
	Indexes    []string `json:"indexes,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

type Plan struct {
	Columns  []ColumnReport `json:"columns"`
	Blocking []string       `json:"blocking"`
}

func (p Plan) Ready() bool { return len(p.Blocking) == 0 }

// Build 只读盘点所有列的存量范围。范围外（负值或 >2100 年）阻断迁移。
func Build(ctx context.Context, db *sql.DB) (Plan, error) {
	plan := Plan{}
	for _, item := range Columns {
		report := ColumnReport{Table: item.Table, Column: item.Column}
		var dataType string
		var nullable string
		err := db.QueryRowContext(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_schema='juhe_business' AND table_name=$1 AND column_name=$2`, item.Table, item.Column).Scan(&dataType, &nullable)
		if err == sql.ErrNoRows {
			report.Warnings = append(report.Warnings, "列不存在")
			plan.Columns = append(plan.Columns, report)
			continue
		}
		if err != nil {
			return plan, err
		}
		report.Present = true
		report.Type = dataType
		report.Nullable = nullable == "YES"
		table := `"juhe_business"."` + item.Table + `"`
		column := `"` + item.Column + `"`
		query := fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE %[1]s IS NULL),
			min(%[1]s), max(%[1]s),
			count(*) FILTER (WHERE %[1]s IS NOT NULL AND (%[1]s < 0 OR %[1]s > %[2]d))
			FROM %[3]s`, column, MaxEpochMillis, table)
		var min, max sql.NullInt64
		if err := db.QueryRowContext(ctx, query).Scan(&report.Rows, &report.Nulls, &min, &max, &report.OutOfRange); err != nil {
			return plan, fmt.Errorf("盘点 %s.%s 失败: %w", item.Table, item.Column, err)
		}
		if min.Valid {
			report.Min = &min.Int64
		}
		if max.Valid {
			report.Max = &max.Int64
		}
		if rows, err := db.QueryContext(ctx, `
			SELECT indexname FROM pg_indexes
			WHERE schemaname='juhe_business' AND tablename=$1 AND indexdef ILIKE '%' || $2 || '%'
			ORDER BY indexname`, item.Table, item.Column); err == nil {
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					report.Indexes = append(report.Indexes, name)
				}
			}
			rows.Close()
		}
		if report.OutOfRange > 0 {
			plan.Blocking = append(plan.Blocking, fmt.Sprintf("%s.%s 有 %d 行超出 epoch 毫秒范围", item.Table, item.Column, report.OutOfRange))
		}
		plan.Columns = append(plan.Columns, report)
	}
	return plan, nil
}

// LockColumns 是 Revision 32 的首批：account_lock_states 两列。其余运行态列
// 按后续 revision 分批迁移，保证每批库/代码同步闭环。
var LockColumns = []Column{
	{"account_lock_states", "next_retry_at_ms"},
	{"account_lock_states", "lease_until_ms"},
}

// CircuitRuntimeColumns 是 Revision 34 批次：incidents 8 列 + outbox 5 列。
var CircuitRuntimeColumns = []Column{
	{"account_circuit_incidents", "open_until_ms"},
	{"account_circuit_incidents", "next_transition_at_ms"},
	{"account_circuit_incidents", "lease_until_ms"},
	{"account_circuit_incidents", "attempt_started_at_ms"},
	{"account_circuit_incidents", "attempt_hard_deadline_ms"},
	{"account_circuit_incidents", "retained_until_ms"},
	{"account_circuit_incidents", "created_at_ms"},
	{"account_circuit_incidents", "updated_at_ms"},
	{"account_circuit_outbox", "available_at_ms"},
	{"account_circuit_outbox", "claim_until_ms"},
	{"account_circuit_outbox", "acknowledged_at_ms"},
	{"account_circuit_outbox", "created_at_ms"},
	{"account_circuit_outbox", "updated_at_ms"},
}

// ListAvailabilityDirtyColumns 是 Revision 33 批次。
var ListAvailabilityDirtyColumns = []Column{
	{"account_list_availability_dirty", "available_at_ms"},
	{"account_list_availability_dirty", "claim_until_ms"},
	{"account_list_availability_dirty", "created_at_ms"},
	{"account_list_availability_dirty", "updated_at_ms"},
}

// ApplyColumns 在停写窗口内把指定 epoch 毫秒瞬时列迁到 timestamptz(6)。
// 先跑只读预检；所有列在同一事务内 ALTER，失败整体回滚。
func ApplyColumns(ctx context.Context, db *sql.DB, columns []Column) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	applied, err := ApplyColumnsTx(ctx, tx, columns)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return applied, nil
}

// ApplyColumnsTx 在调用方事务内执行预检和转换，使依赖 DDL 与列变更同生共死。
func ApplyColumnsTx(ctx context.Context, tx *sql.Tx, columns []Column) (int, error) {
	for _, item := range columns {
		var outOfRange int64
		query := fmt.Sprintf(`SELECT count(*) FROM "juhe_business"."%s" WHERE "%s" IS NOT NULL AND ("%s" < 0 OR "%s" > %d)`,
			item.Table, item.Column, item.Column, item.Column, MaxEpochMillis)
		if err := tx.QueryRowContext(ctx, query).Scan(&outOfRange); err != nil {
			return 0, fmt.Errorf("预检 %s.%s 失败: %w", item.Table, item.Column, err)
		}
		if outOfRange > 0 {
			return 0, fmt.Errorf("%s.%s 有 %d 行超出 epoch 毫秒范围", item.Table, item.Column, outOfRange)
		}
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'; SET LOCAL timezone='UTC'`); err != nil {
		return 0, err
	}
	applied := 0
	for _, item := range columns {
		var present bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='juhe_business' AND table_name=$1 AND column_name=$2
		)`, item.Table, item.Column).Scan(&present); err != nil {
			return 0, err
		}
		if !present {
			continue
		}
		table := `"juhe_business"."` + item.Table + `"`
		column := `"` + item.Column + `"`
		statement := fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE timestamptz(6)
			USING to_timestamp(%s::double precision / 1000.0)`, table, column, column)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return 0, fmt.Errorf("迁移 %s.%s 失败: %w", item.Table, item.Column, err)
		}
		applied++
	}
	return applied, nil
}

// Apply 迁移全部 19 列（供一次性 dry-run/未来批次复用）。
func Apply(ctx context.Context, db *sql.DB) (int, error) {
	return ApplyColumns(ctx, db, Columns)
}
