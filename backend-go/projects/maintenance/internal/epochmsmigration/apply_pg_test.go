package epochmsmigration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestApplyMigratesEpochMillisWithIndexAndCheck(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `
			CREATE SCHEMA juhe_business;
			CREATE TABLE juhe_business.account_lock_states (
				account_id text PRIMARY KEY,
				next_retry_at_ms bigint,
				lease_until_ms bigint,
				CHECK ((next_retry_at_ms IS NULL) OR (lease_until_ms IS NULL))
			);
			CREATE INDEX idx_lock_next_retry ON juhe_business.account_lock_states(next_retry_at_ms);
			INSERT INTO juhe_business.account_lock_states VALUES ('a', 1760000000123, NULL);`)
		return err
	})
	applied, err := Apply(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}
	var typ string
	if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
		WHERE table_schema='juhe_business' AND table_name='account_lock_states' AND column_name='next_retry_at_ms'`).Scan(&typ); err != nil || typ != "timestamp with time zone" {
		t.Fatalf("type = %s err=%v", typ, err)
	}
	var millis int64
	if err := db.QueryRow(`SELECT (EXTRACT(EPOCH FROM next_retry_at_ms)*1000)::bigint FROM juhe_business.account_lock_states`).Scan(&millis); err != nil || millis != 1760000000123 {
		t.Fatalf("roundtrip = %d err=%v", millis, err)
	}
}
