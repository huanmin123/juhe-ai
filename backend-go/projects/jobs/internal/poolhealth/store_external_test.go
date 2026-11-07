package poolhealth

// Store 真实 PG 门控测试：健康状态机、checked_at 更新与在途配置快照丢弃。
// 默认跳过；设置 JUHE_AI_TEST_EXTERNAL=1 后在隔离测试库执行。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const poolHealthSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.proxy_profiles (
  id text PRIMARY KEY, name text NOT NULL, type text NOT NULL, host text NOT NULL,
  port integer NOT NULL, username text, password_encrypted text,
  enabled boolean NOT NULL, pool_health_status text,
  pool_health_failure_streak integer NOT NULL DEFAULT 0, pool_health_checked_at timestamptz);
CREATE TABLE juhe_business.proxy_pools (id text PRIMARY KEY, enabled boolean NOT NULL);
CREATE TABLE juhe_business.proxy_pool_members (
  pool_id text NOT NULL, proxy_profile_id text NOT NULL, enabled boolean NOT NULL,
  PRIMARY KEY (pool_id, proxy_profile_id));
`

func poolHealthDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, poolHealthSchema)
		return err
	})
	for _, statement := range []string{
		`INSERT INTO juhe_business.proxy_pools (id, enabled) VALUES ('pool-1', TRUE)`,
		`INSERT INTO juhe_business.proxy_profiles
		 (id,name,type,host,port,username,password_encrypted,enabled,pool_health_status,pool_health_failure_streak)
		 VALUES ('p-1','代理','http','127.0.0.1',8080,'u','sealed',TRUE,NULL,0)`,
		`INSERT INTO juhe_business.proxy_pool_members (pool_id,proxy_profile_id,enabled) VALUES ('pool-1','p-1',TRUE)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestStoreProjectionAndStaleSnapshot(t *testing.T) {
	db := poolHealthDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

	due, err := store.ListDue(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0].ProfileID != "p-1" {
		t.Fatalf("ListDue = %+v, %v", due, err)
	}
	target := due[0]

	fail := func(at time.Time) RecordResult {
		t.Helper()
		result, err := store.Record(ctx, target, false, at)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := fail(now); result.Status != HealthDegraded || result.Streak != 1 {
		t.Fatalf("first failure = %+v", result)
	}
	if result := fail(now.Add(time.Minute)); result.Status != HealthDegraded || result.Streak != 2 {
		t.Fatalf("second failure = %+v", result)
	}
	if result := fail(now.Add(2 * time.Minute)); result.Status != HealthDead || result.Streak != 3 {
		t.Fatalf("third failure = %+v", result)
	}
	success, err := store.Record(ctx, target, true, now.Add(3*time.Minute))
	if err != nil || success.Status != HealthHealthy || success.Streak != 0 {
		t.Fatalf("success = %+v, %v", success, err)
	}

	// 在途期间连接配置变化：旧快照结果必须丢弃，不覆盖 checked_at/状态。
	changed := target
	changed.Host = "127.0.0.2"
	skipped, err := store.Record(ctx, changed, false, now.Add(4*time.Minute))
	if err != nil || !skipped.Skipped {
		t.Fatalf("stale snapshot = %+v, %v", skipped, err)
	}
	var status string
	var streak int
	var checkedAt time.Time
	if err := db.QueryRow(`SELECT pool_health_status, pool_health_failure_streak, pool_health_checked_at
		FROM juhe_business.proxy_profiles WHERE id = 'p-1'`).Scan(&status, &streak, &checkedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(HealthHealthy) || streak != 0 || !checkedAt.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("stale result overwrote projection: %s %d %s", status, streak, checkedAt)
	}
}
