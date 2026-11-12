package main

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/usagerecordsmigration"
)

func revision30Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	if ok, err := usageRecordsColumnSatisfied(ctx, db, "juhe_usage", "usage_records", "created_at", "timestamp with time zone", 3); err != nil || !ok {
		return false, err
	}
	present, err := relationPresent(ctx, db, usagerecordsmigration.TargetSchema, usagerecordsmigration.UsageRecordsTable)
	if err != nil {
		return false, err
	}
	return !present, nil
}

func applyRevision30(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var rows int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_usage.usage_records`).Scan(&rows); err != nil {
		return nil, err
	}
	if err := usagerecordsmigration.ApplySwap(ctx, db); err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"rows": rows}, nil
}
