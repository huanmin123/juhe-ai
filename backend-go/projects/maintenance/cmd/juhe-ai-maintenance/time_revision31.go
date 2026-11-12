package main

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/usagerecordsmigration"
)

func revision31Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='juhe_usage_retired')`).Scan(&present); err != nil {
		return false, err
	}
	return !present, nil
}

func applyRevision31(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := usagerecordsmigration.ApplyRetire(ctx, db); err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"dropped": "juhe_usage_retired"}, nil
}
