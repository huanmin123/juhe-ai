package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/usagerecordsmigration"
)

// revision29Satisfied：完成 prepare 后影子父表存在且类型/行数一致；完成 swap
// 后主表 created_at 已是 timestamptz(3)。两种形态都视为已满足，便于 swap
// 步骤 adopt 已完成的 prepare。
func revision29Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	if ok, err := usageRecordsColumnSatisfied(ctx, db, "juhe_usage", "usage_records", "created_at", "timestamp with time zone", 3); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	present, err := relationPresent(ctx, db, usagerecordsmigration.TargetSchema, usagerecordsmigration.UsageRecordsTable)
	if err != nil || !present {
		return false, err
	}
	if ok, err := usageRecordsColumnSatisfied(ctx, db, usagerecordsmigration.TargetSchema, usagerecordsmigration.UsageRecordsTable, "created_at", "timestamp with time zone", 3); err != nil || !ok {
		return false, err
	}
	var sourceRows, targetRows int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage.usage_records`).Scan(&sourceRows); err != nil {
		return false, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage_migrate.usage_records`).Scan(&targetRows); err != nil {
		return false, err
	}
	return sourceRows == targetRows, nil
}

func usageRecordsColumnSatisfied(ctx context.Context, db *sql.DB, schema, table, column, wantType string, wantPrecision int) (bool, error) {
	var dataType string
	var precision sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT data_type, datetime_precision FROM information_schema.columns
		WHERE table_schema=$1 AND table_name=$2 AND column_name=$3`, schema, table, column).Scan(&dataType, &precision)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return dataType == wantType && precision.Valid && int(precision.Int64) == wantPrecision, nil
}

func applyRevision29(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	plan, err := usagerecordsmigration.ApplyPrepare(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("usage_records prepare 失败: %w", err)
	}
	return upgradepipeline.Detail{"partitions": len(plan.Partitions), "rows": plan.TotalRows}, nil
}
