package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// instantColumn 是升级步骤静态列清单的一行。
type instantColumn struct {
	table  string
	column string
	target string
}

// instantRevision 承载一个 Phase 2 批次的静态列清单与共用执行逻辑。
type instantRevision struct {
	columns []instantColumn
}

func (r instantRevision) actions() []instantmigration.Action {
	actions := make([]instantmigration.Action, 0, len(r.columns))
	for _, column := range r.columns {
		actions = append(actions, instantmigration.TextToInstantAction(
			"juhe_business", column.table, column.column, column.target))
	}
	return actions
}

// satisfied 是 Satisfied/Verify 同源判据：清单列全部已是目标类型与精度。
// fresh 库表尚不存在时按满足处理（EnsurePostgres 会按目标类型建表），但表
// 存在而列缺失视为结构漂移，不能当 fresh-adopt 跳过。
func (r instantRevision) satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	checkedTables := map[string]bool{}
	absentTables := map[string]bool{}
	for _, action := range r.actions() {
		key := action.Schema + "." + action.Table
		if absentTables[key] {
			continue
		}
		if !checkedTables[key] {
			present, err := relationPresent(ctx, db, action.Schema, action.Table)
			if err != nil {
				return false, err
			}
			checkedTables[key] = true
			if !present {
				absentTables[key] = true
				continue
			}
		}
		migrated, err := instantmigration.IsMigrated(ctx, db, action)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !migrated {
			return false, nil
		}
	}
	return true, nil
}

func (r instantRevision) apply(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	actions := r.actions()
	applied, err := instantmigration.ApplyBatch(ctx, db, actions)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"applied": applied, "columns": len(actions)}, nil
}

func relationPresent(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"."+table).Scan(&present); err != nil {
		return false, err
	}
	return present, nil
}
