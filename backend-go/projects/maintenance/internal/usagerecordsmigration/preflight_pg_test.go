package usagerecordsmigration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestPlanPrepareCountsPartitionsAndBlockingRows(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `
			CREATE SCHEMA juhe_usage;
			CREATE TABLE juhe_usage.usage_records (
				id text NOT NULL,
				created_at text NOT NULL,
				usage_stats_aggregated_at text,
				client_ip_stats_aggregated_at text,
				PRIMARY KEY (created_at, id)
			) PARTITION BY RANGE (created_at);
			CREATE TABLE juhe_usage.usage_records_20260909 PARTITION OF juhe_usage.usage_records
				FOR VALUES FROM ('2026-09-09') TO ('2026-09-10');
			CREATE TABLE juhe_usage.usage_records_20260910 PARTITION OF juhe_usage.usage_records
				FOR VALUES FROM ('2026-09-10') TO ('2026-09-11');
			INSERT INTO juhe_usage.usage_records (id, created_at) VALUES
				('r1', '2026-09-09T10:00:00.000Z'),
				('r1', '2026-09-10T10:00:00.000Z');`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_usage.usage_records (id, created_at)
			VALUES ('bad', '2026-09-09T10:00:00+08:00')`)
		return err
	})
	plan, err := PlanPrepare(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ParentPresent || len(plan.Partitions) != 2 || plan.TotalRows != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.DuplicateIDs != 1 || plan.NonCanonicalCreatedAt != 1 || plan.Ready() {
		t.Fatalf("blocking facts = %+v", plan)
	}
}

func TestApplyPrepareCopiesRowsAndIndexes(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `
			CREATE SCHEMA juhe_usage;
			CREATE TABLE juhe_usage.usage_records (
				id text NOT NULL,
				created_at text NOT NULL,
				usage_stats_aggregated_at text,
				client_ip_stats_aggregated_at text,
				PRIMARY KEY (created_at, id)
			) PARTITION BY RANGE (created_at);
			CREATE INDEX idx_usage_records_stats_pending ON juhe_usage.usage_records(created_at, id)
				WHERE usage_stats_aggregated_at IS NULL;
			CREATE TABLE juhe_usage.usage_records_20260909 PARTITION OF juhe_usage.usage_records
				FOR VALUES FROM ('2026-09-09') TO ('2026-09-10');
			INSERT INTO juhe_usage.usage_records (id, created_at) VALUES
				('r1', '2026-09-09T10:00:00.000Z'),
				('r2', '2026-09-09T11:00:00.000Z');`)
		return err
	})
	plan, err := ApplyPrepare(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.TargetPresent || plan.TotalRows != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	var typ string
	if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
		WHERE table_schema='juhe_usage_migrate' AND table_name='usage_records' AND column_name='created_at'`).Scan(&typ); err != nil || typ != "timestamp with time zone" {
		t.Fatalf("created_at type = %s err=%v", typ, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_usage_migrate.usage_records`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("target rows = %d err=%v", count, err)
	}
}

func TestApplySwapMovesFamilies(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `
			CREATE SCHEMA juhe_usage;
			CREATE TABLE juhe_usage.usage_records (
				id text NOT NULL,
				created_at text NOT NULL,
				usage_stats_aggregated_at text,
				client_ip_stats_aggregated_at text,
				PRIMARY KEY (created_at, id)
			) PARTITION BY RANGE (created_at);
			CREATE TABLE juhe_usage.usage_records_20260909 PARTITION OF juhe_usage.usage_records
				FOR VALUES FROM ('2026-09-09') TO ('2026-09-10');
			INSERT INTO juhe_usage.usage_records (id, created_at) VALUES ('r1', '2026-09-09T10:00:00.000Z');`)
		return err
	})
	if _, err := ApplyPrepare(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := ApplySwap(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var typ string
	if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
		WHERE table_schema='juhe_usage' AND table_name='usage_records' AND column_name='created_at'`).Scan(&typ); err != nil || typ != "timestamp with time zone" {
		t.Fatalf("created_at type = %s err=%v", typ, err)
	}
	var retired int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_usage_retired.usage_records`).Scan(&retired); err != nil || retired != 1 {
		t.Fatalf("retired rows = %d err=%v", retired, err)
	}
}
