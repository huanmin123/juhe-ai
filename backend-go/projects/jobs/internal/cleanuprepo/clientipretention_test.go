package cleanuprepo

import (
	"context"
	"database/sql"
	"testing"
)

// ClientIPRetentionStore 语句形状与真实 PG 边界测试（2026-11-09 新增）。
// 背景：client_ip_stats_daily / client_ip_policy_hits 此前只挂在已退役的
// non_business_data_cleanup 清单上，现行无定时清理；本测试锁定新 store 的
// 表/列/严格 cutoff/最旧优先/limit 语义。

func TestClientIPRetentionPGStatementShape(t *testing.T) {
	rec := newPGRecorder()
	store := &ClientIPRetentionStore{DB: openRecorderPG(rec)}
	ctx := context.Background()
	if _, err := store.CleanupDailyStatsBefore(ctx, "2026-10-05", 7); err != nil {
		t.Fatalf("pg cleanup daily: %v", err)
	}
	if _, err := store.CleanupPolicyHitsBefore(ctx, "2026-10-05", 7); err != nil {
		t.Fatalf("pg cleanup policy hits: %v", err)
	}
	statements := rec.all()
	if len(statements) != 2 {
		t.Fatalf("应恰好录制两条 DELETE，实际 %d 条", len(statements))
	}
	wantDaily := normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_stats.client_ip_stats_daily
      WHERE ctid IN (
        SELECT ctid FROM juhe_stats.client_ip_stats_daily
        WHERE "stat_date" < ?
        ORDER BY "stat_date" ASC, ctid ASC
        LIMIT ?
      )
	`))
	if got := normalizeKitSQL(statements[0].query); got != wantDaily {
		t.Fatalf("PG client_ip_stats_daily 语句不匹配：\n%s", statements[0].query)
	}
	wantHits := normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_stats.client_ip_policy_hits
      WHERE ctid IN (
        SELECT ctid FROM juhe_stats.client_ip_policy_hits
        WHERE "stat_date" < ?
        ORDER BY "stat_date" ASC, ctid ASC
        LIMIT ?
      )
	`))
	if got := normalizeKitSQL(statements[1].query); got != wantHits {
		t.Fatalf("PG client_ip_policy_hits 语句不匹配：\n%s", statements[1].query)
	}
}

func TestClientIPRetentionPGRollingWindow(t *testing.T) {
	ctx := context.Background()
	db := openKitPostgres(t, func(ctx context.Context, db *sql.DB) error {
		execKitSchema(t, db,
			`CREATE SCHEMA IF NOT EXISTS juhe_stats`,
			`CREATE TABLE juhe_stats.client_ip_stats_daily (ip_hash text NOT NULL, stat_date text NOT NULL, request_count bigint NOT NULL DEFAULT 0)`,
			`CREATE TABLE juhe_stats.client_ip_policy_hits (ip_hash text NOT NULL, stat_date text NOT NULL, policy_id text NOT NULL, hit_count bigint NOT NULL DEFAULT 0)`,
		)
		return nil
	})
	store := &ClientIPRetentionStore{DB: db}
	seedDaily := func(date, ipHash string) {
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_stats.client_ip_stats_daily (ip_hash, stat_date, request_count) VALUES ($1, $2, 1)`, ipHash, date); err != nil {
			t.Fatalf("seed daily %s: %v", date, err)
		}
	}
	seedHits := func(date, ipHash string) {
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_stats.client_ip_policy_hits (ip_hash, stat_date, policy_id, hit_count) VALUES ($1, $2, $3, 1)`, ipHash, date, "policy-1"); err != nil {
			t.Fatalf("seed hits %s: %v", date, err)
		}
	}
	for _, date := range []string{"2026-10-04", "2026-10-05", "2026-10-06"} {
		seedDaily(date, "ip-"+date)
		seedHits(date, "ip-"+date)
	}
	// 严格早于 cutoff：10-04 删除，10-05 边界保留，10-06 保留。
	deleted, err := store.CleanupDailyStatsBefore(ctx, "2026-10-05", 10)
	if err != nil {
		t.Fatalf("cleanup daily: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("daily deleted = %d, want 1", deleted)
	}
	deleted, err = store.CleanupPolicyHitsBefore(ctx, "2026-10-05", 10)
	if err != nil {
		t.Fatalf("cleanup policy hits: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("policy hits deleted = %d, want 1", deleted)
	}
	for _, table := range []string{"client_ip_stats_daily", "client_ip_policy_hits"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_stats.`+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 2 {
			t.Fatalf("%s remaining = %d, want 2 (boundary + new)", table, count)
		}
	}
}
