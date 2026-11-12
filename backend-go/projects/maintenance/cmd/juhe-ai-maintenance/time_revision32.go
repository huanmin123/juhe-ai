package main

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

func revision32Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	for _, column := range []string{"next_retry_at_ms", "lease_until_ms"} {
		ok, err := usageRecordsColumnSatisfied(ctx, db, "juhe_business", "account_lock_states", column, "timestamp with time zone", 6)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func applyRevision32(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	applied, err := epochmsmigration.ApplyColumns(ctx, db, epochmsmigration.LockColumns)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"columns": applied}, nil
}
