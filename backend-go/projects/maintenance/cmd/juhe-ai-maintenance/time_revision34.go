package main

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

func revision34Satisfied(ctx context.Context, dsn string) (bool, error) {
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	columns := []struct{ table, column string }{
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
	for _, item := range columns {
		ok, err := usageRecordsColumnSatisfied(ctx, db, "juhe_business", item.table, item.column, "timestamp with time zone", 6)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func applyRevision34(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	applied, err := epochmsmigration.ApplyColumns(ctx, db, epochmsmigration.CircuitRuntimeColumns)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"columns": applied}, nil
}
