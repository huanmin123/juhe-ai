package accountbalance

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresJobsStoreSmoke 验证生产 OpenStore(StorePostgres) + EnsureSchema
// + 租约/outcome 最短链。迁 pgtestkit（PLAN-20261024T051925945Z 切片，§4.2
// 裁量：smoke 迁离 JUHE_AI_J2_PG_SMOKE_ADMIN_URL 门禁，runtimelog/operationlog
// 先例已接受）；一次性临时库由 kit 创建/删除，断言逐字节保持。
func TestPostgresJobsStoreSmoke(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "j2-smoke", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v %t", err, acquired)
	}
	account, acquired, err := store.AcquireAccountLease(ctx, owner, "j2-smoke-account", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("account lease: %v %t", err, acquired)
	}
	now := time.Now().UTC()
	inserted, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "j2-smoke-outcome", RequestID: "j2-smoke-request", AccountID: "j2-smoke-account", SystemAccountID: "j2-smoke-system", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.25"}})
	if err != nil || !inserted {
		t.Fatalf("outcome: %v %t", err, inserted)
	}
}

// TestPostgresDirectInputContractSmoke 保持 JUHE_AI_J2_PG_SMOKE_BUSINESS_URL
// env 门禁现状（§4.2）：被测语义是真实共享业务库上的 reader 契约，隔离空库
// 无法复现，不属本次 SQLite 退役迁移面。
func TestPostgresDirectInputContractSmoke(t *testing.T) {
	businessURL := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_BUSINESS_URL"))
	secret := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_SECRET"))
	if businessURL == "" || secret == "" {
		t.Skip("J2 PG direct-input smoke 配置未设置")
	}
	db, err := sql.Open("pgx", businessURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := NewPostgresDirectInputReader(db, secret, 15*time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled sql.NullBool
	if err := db.QueryRowContext(ctx, `SELECT TRUE`).Scan(&enabled); err != nil || !enabled.Valid || !enabled.Bool {
		t.Fatalf("PG boolean proxy field must scan as NullBool: %#v %v", enabled, err)
	}
	if _, err := reader.LoadDue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadRecovery(ctx, 1); err != nil {
		t.Fatal(err)
	}
}
