package main

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

func revision33Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	for _, column := range []string{"available_at_ms", "claim_until_ms", "created_at_ms", "updated_at_ms"} {
		ok, err := usageRecordsColumnSatisfied(ctx, db, "juhe_business", "account_list_availability_dirty", column, "timestamp with time zone", 6)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func applyRevision33(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	applied, err := epochmsmigration.ApplyColumns(ctx, db, epochmsmigration.ListAvailabilityDirtyColumns)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"columns": applied}, nil
}
