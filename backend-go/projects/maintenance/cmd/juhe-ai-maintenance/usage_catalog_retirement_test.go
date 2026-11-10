package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestUsageShardCatalogRetirementDropsLegacyTables 钉住 revision 23 的
// Destructive 步骤：四张历史 shard 目录表按 FK 依赖顺序删除，重复执行幂等。
func TestUsageShardCatalogRetirementDropsLegacyTables(t *testing.T) {
	td := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		for _, statement := range []string{
			`CREATE SCHEMA IF NOT EXISTS juhe_usage`,
			`CREATE TABLE juhe_usage.usage_record_shards (shard_key text PRIMARY KEY)`,
			`CREATE TABLE juhe_usage.usage_record_shard_entries (
				usage_id text PRIMARY KEY,
				shard_key text NOT NULL REFERENCES juhe_usage.usage_record_shards(shard_key)
			)`,
			`CREATE TABLE juhe_usage.usage_record_account_shards (
				account_id text NOT NULL,
				shard_key text NOT NULL REFERENCES juhe_usage.usage_record_shards(shard_key),
				PRIMARY KEY (account_id, shard_key)
			)`,
			`CREATE TABLE juhe_usage.usage_record_api_key_shards (
				api_key_id text NOT NULL,
				system_account_id text NOT NULL,
				shard_key text NOT NULL REFERENCES juhe_usage.usage_record_shards(shard_key),
				PRIMARY KEY (api_key_id, system_account_id, shard_key)
			)`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})

	satisfied, err := usageShardCatalogSatisfied(context.Background(), td.DSN)
	if err != nil {
		t.Fatalf("satisfied before apply: %v", err)
	}
	if satisfied {
		t.Fatal("four legacy catalog tables should exist before apply")
	}
	detail, err := applyUsageShardCatalogRetirement(context.Background(), td.DSN)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if detail["dropped"] != 4 {
		t.Fatalf("dropped = %#v, want 4", detail["dropped"])
	}
	satisfied, err = usageShardCatalogSatisfied(context.Background(), td.DSN)
	if err != nil {
		t.Fatalf("satisfied after apply: %v", err)
	}
	if !satisfied {
		t.Fatal("four legacy catalog tables should be absent after apply")
	}
	again, err := applyUsageShardCatalogRetirement(context.Background(), td.DSN)
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if again["dropped"] != 0 || again["alreadyRetired"] != true {
		t.Fatalf("idempotent detail = %#v", again)
	}
}
