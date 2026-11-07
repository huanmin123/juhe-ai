package proxyresolve

// PGLeaseWriter 真实 PG 门控测试：请求时租约修复的锁序/CAS/成员可分配性。
// 默认跳过；设置 JUHE_AI_TEST_EXTERNAL=1 后在隔离测试库执行。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const leaseWriterSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.accounts (
  id text PRIMARY KEY, proxy_pool_id text, deleted_at timestamptz);
CREATE TABLE juhe_business.proxy_profiles (
  id text PRIMARY KEY, enabled boolean NOT NULL,
  pool_health_status text, pool_health_failure_streak integer NOT NULL DEFAULT 0);
CREATE TABLE juhe_business.proxy_pools (id text PRIMARY KEY, enabled boolean NOT NULL);
CREATE TABLE juhe_business.proxy_pool_members (
  pool_id text NOT NULL, proxy_profile_id text NOT NULL, priority integer NOT NULL,
  enabled boolean NOT NULL, PRIMARY KEY (pool_id, proxy_profile_id));
CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq;
CREATE TABLE juhe_business.account_proxy_leases (
  account_id text PRIMARY KEY, pool_id text NOT NULL, proxy_profile_id text NOT NULL,
  generation bigint NOT NULL DEFAULT nextval('juhe_business.account_proxy_lease_generation_seq'),
  assignment_reason text NOT NULL, assigned_at timestamptz NOT NULL, updated_at timestamptz NOT NULL);
`

func leaseWriterDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, leaseWriterSchema)
		return err
	})
	for _, statement := range []string{
		`INSERT INTO juhe_business.accounts (id, proxy_pool_id) VALUES ('acc-1','pool-1')`,
		`INSERT INTO juhe_business.proxy_pools (id, enabled) VALUES ('pool-1', TRUE), ('pool-off', FALSE)`,
		`INSERT INTO juhe_business.proxy_profiles (id, enabled) VALUES ('p-1', TRUE), ('p-2', TRUE), ('p-off', FALSE)`,
		`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, enabled) VALUES
		 ('pool-1','p-1',0,TRUE), ('pool-1','p-2',1,TRUE), ('pool-1','p-off',2,FALSE)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPGLeaseWriterRepairsAndCAS(t *testing.T) {
	db := leaseWriterDB(t)
	writer, err := NewPGLeaseWriter(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first, err := writer.AssignLease(ctx, "acc-1", "pool-1", "p-1", AssignmentInitial, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProxyProfileID != "p-1" || first.Generation == 0 {
		t.Fatalf("initial lease = %+v", first)
	}

	// 成员不可分配后按 expected CAS 修复到 p-2，generation 推进。
	second, err := writer.AssignLease(ctx, "acc-1", "pool-1", "p-2", AssignmentMemberUnavailable, &first)
	if err != nil {
		t.Fatal(err)
	}
	if second.ProxyProfileID != "p-2" || second.Generation <= first.Generation {
		t.Fatalf("repaired lease = %+v (first %+v)", second, first)
	}

	// 并发臂用旧 expected 竞争：返回已修复的租约，不回写、不推代。
	raced, err := writer.AssignLease(ctx, "acc-1", "pool-1", "p-1", AssignmentMemberUnavailable, &first)
	if err != nil {
		t.Fatal(err)
	}
	if raced.ProxyProfileID != "p-2" || raced.Generation != second.Generation {
		t.Fatalf("raced lease = %+v, want current p-2 gen %d", raced, second.Generation)
	}

	// 停用成员、停用池、账户改绑其他池都必须 fail closed。
	if _, err := writer.AssignLease(ctx, "acc-1", "pool-1", "p-off", AssignmentInitial, nil); err == nil ||
		!strings.Contains(err.Error(), "member unavailable") {
		t.Fatalf("disabled member err = %v", err)
	}
	if _, err := writer.AssignLease(ctx, "acc-1", "pool-off", "p-1", AssignmentInitial, nil); err == nil {
		t.Fatalf("disabled pool must fail")
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-other' WHERE id = 'acc-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AssignLease(ctx, "acc-1", "pool-1", "p-1", AssignmentPoolReassigned, &second); err == nil {
		t.Fatalf("pool rebind race must fail closed")
	}
}
