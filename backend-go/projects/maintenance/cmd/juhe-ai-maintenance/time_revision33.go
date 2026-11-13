package main

import (
	"context"
	"database/sql"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
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

func revision33FunctionSatisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var matched bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_proc WHERE oid = to_regprocedure('juhe_business.account_list_availability_mark_dirty_accounts(text[],text)')
   AND prosrc = $1
 )`, "\n"+schema.DirtyAvailabilityFunctionNativeBody()+"\n").Scan(&matched)
	return matched, err
}

func applyRevision33Tx(ctx context.Context, tx *sql.Tx, functionSQL string) (int, error) {
	applied, err := epochmsmigration.ApplyColumnsTx(ctx, tx, epochmsmigration.ListAvailabilityDirtyColumns)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, functionSQL); err != nil {
		return 0, err
	}
	return applied, nil
}

func applyRevision33(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	applied, err := applyRevision33Tx(ctx, tx, schema.DirtyAvailabilityFunctionNativeSQL())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"columns": applied}, nil
}
