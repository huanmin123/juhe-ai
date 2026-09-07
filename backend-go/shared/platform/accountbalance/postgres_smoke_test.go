package accountbalance

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This smoke is opt-in and creates/drops a uniquely named dev database. It
// never touches the shared business database or Redis.
func TestPostgresJobsStoreSmoke(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_ADMIN_URL"))
	if adminURL == "" {
		t.Skip("JUHE_AI_J2_PG_SMOKE_ADMIN_URL 未设置")
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("juhe_ai_sub2api_dev_j2_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) }()
	targetURL, err := replaceDatabase(adminURL, name)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := sql.Open("pgx", targetURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(context.Background(), "CREATE SCHEMA juhe_jobs"); err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	_ = bootstrap.Close()
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: targetURL})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
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

func replaceDatabase(raw, database string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}
