package instantmigration

import (
	"context"
	"database/sql"
	"fmt"
)

// IsMigrated 判断列是否已经是目标 timestamptz 类型与精度。
func IsMigrated(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, action Action) (bool, error) {
	var dataType string
	var precision sql.NullInt64
	if err := queryer.QueryRowContext(ctx, `
		SELECT data_type, datetime_precision
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		action.Schema, action.Table, action.Column).Scan(&dataType, &precision); err != nil {
		return false, fmt.Errorf("读取 %s.%s.%s 类型失败: %w", action.Schema, action.Table, action.Column, err)
	}
	if dataType != "timestamp with time zone" {
		return false, nil
	}
	expected, ok := precisionFromType(action.TargetType)
	if !ok {
		return true, nil
	}
	return precision.Valid && int(precision.Int64) == expected, nil
}

// ApplyBatch 在单个事务内按顺序迁移一批列；已迁移列跳过，失败整体回滚。
// 调用方负责停写门禁与备份确认；本函数不检查它们。
//
// 单事务保证批次内不会留下半迁移状态；批次大小由调用方按 revision 步骤
// 控制。每列 ALTER 前由 Apply 设置 lock_timeout。
func ApplyBatch(ctx context.Context, db *sql.DB, actions []Action) (int, error) {
	return ApplyBatchWithHooks(ctx, db, actions, nil, nil)
}

// ApplyBatchWithHooks 与 ApplyBatch 相同，但允许调用方在同一事务内执行
// before/after 钩子。用于 ALTER COLUMN TYPE 前临时移除依赖该列的
// `UPDATE OF <column>` 触发器、迁移后按原定义重建（PostgreSQL 禁止直接
// 修改被触发器定义引用的列类型，SQLSTATE 0A000）。
func ApplyBatchWithHooks(ctx context.Context, db *sql.DB, actions []Action, before, after func(*sql.Tx) error) (int, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	// 预检在任何表锁之前完成：非法存量值直接中止批次，不留下半迁移状态。
	// 同一张表的多个列合并为一次扫描。
	var pending []Action
	for _, action := range actions {
		present, err := tablePresent(ctx, db, action)
		if err != nil {
			return 0, err
		}
		if !present {
			// 表由运行进程按需创建（如 health probe request outbox）：升级时
			// 尚不存在表示没有存量文本列可迁移，交由运行时以目标类型建表。
			continue
		}
		migrated, err := IsMigrated(ctx, db, action)
		if err != nil {
			return 0, err
		}
		if !migrated {
			pending = append(pending, action)
		}
	}
	if err := PreflightBatch(ctx, db, pending); err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL timezone = 'UTC'`); err != nil {
		return 0, fmt.Errorf("设置事务时区失败: %w", err)
	}
	if before != nil {
		if err := before(tx); err != nil {
			return 0, err
		}
	}
	applied := 0
	for _, action := range actions {
		present, err := tablePresent(ctx, tx, action)
		if err != nil {
			return applied, err
		}
		if !present {
			continue
		}
		migrated, err := IsMigrated(ctx, tx, action)
		if err != nil {
			return applied, err
		}
		if migrated {
			continue
		}
		if err := Apply(ctx, tx, action); err != nil {
			return applied, err
		}
		if err := Verify(ctx, tx, action); err != nil {
			return applied, err
		}
		applied++
	}
	if after != nil {
		if err := after(tx); err != nil {
			return applied, err
		}
	}
	if err := tx.Commit(); err != nil {
		return applied, err
	}
	return applied, nil
}

// tablePresent 判断 action 目标表是否存在。tdb 接口与 IsMigrated 一致，
// *sql.DB 与 *sql.Tx 都满足。
func tablePresent(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, action Action) (bool, error) {
	var present bool
	if err := queryer.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, action.Schema+"."+action.Table).Scan(&present); err != nil {
		return false, fmt.Errorf("检查 %s.%s 是否存在失败: %w", action.Schema, action.Table, err)
	}
	return present, nil
}
