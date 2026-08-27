package modelcheckpolicy

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func TestSQLiteReaderUsesDefaultAndFreezesConfiguredPolicy(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY,revision INTEGER NOT NULL,profile TEXT NOT NULL,manual_enforcement_enabled INTEGER NOT NULL,penalty_threshold INTEGER NOT NULL,penalty_action TEXT NOT NULL,recovery_interval_minutes INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	defaultPolicy, err := reader.Load(context.Background(), "system-1")
	if err != nil || defaultPolicy.Revision != "0" || defaultPolicy.Profile != "quick" || defaultPolicy.PenaltyAction != "fallback" || defaultPolicy.Digest == "" {
		t.Fatalf("default=%#v err=%v", defaultPolicy, err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies(system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES(?,?,?,?,?,?,?)`, "system-1", 4, "full", 0, 82, "quality_isolate", 30); err != nil {
		t.Fatal(err)
	}
	configured, err := reader.Load(context.Background(), "system-1")
	if err != nil || configured.Revision != "4" || configured.Profile != "full" || configured.ManualEnforcementEnabled || configured.PenaltyThreshold != 82 || configured.PenaltyAction != "quality_isolate" || configured.RecoveryIntervalMinutes != 30 || configured.Digest == defaultPolicy.Digest {
		t.Fatalf("configured=%#v err=%v", configured, err)
	}
}

func TestPostgresReaderContractWithDevDatabase(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("J3B_MODEL_CHECK_DEV_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("J3B_MODEL_CHECK_DEV_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := NewPostgresReader(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("dev PostgreSQL model-check policy reader contract: %v", err)
	}
}
