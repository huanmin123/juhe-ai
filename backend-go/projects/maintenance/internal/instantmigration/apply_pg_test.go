package instantmigration

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func prepareTwoTables(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE SCHEMA juhe_business`,
		`CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY,
			created_at text NOT NULL,
			updated_at text NOT NULL
		)`,
		`INSERT INTO juhe_business.accounts (id, created_at, updated_at) VALUES
			('a', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func actionsForAccounts() []Action {
	return []Action{
		{
			Schema: "juhe_business", Table: "accounts", Column: "created_at",
			CurrentType: "text", TargetType: "timestamptz(3)",
			SQL: `ALTER TABLE "juhe_business"."accounts" ALTER COLUMN "created_at" TYPE timestamptz(3) USING "created_at"::timestamptz`,
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "updated_at",
			CurrentType: "text", TargetType: "timestamptz(3)",
			SQL: `ALTER TABLE "juhe_business"."accounts" ALTER COLUMN "updated_at" TYPE timestamptz(3) USING "updated_at"::timestamptz`,
		},
	}
}

func TestApplyBatchAndIdempotentRerun(t *testing.T) {
	db := pgtestkit.New(t, prepareTwoTables)
	ctx := context.Background()
	actions := actionsForAccounts()
	applied, err := ApplyBatch(ctx, db, actions)
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}
	applied, err = ApplyBatch(ctx, db, actions)
	if err != nil {
		t.Fatalf("rerun ApplyBatch: %v", err)
	}
	if applied != 0 {
		t.Fatalf("rerun applied = %d, want 0", applied)
	}
	for _, action := range actions {
		migrated, err := IsMigrated(ctx, db, action)
		if err != nil || !migrated {
			t.Fatalf("%s.%s migrated=%v err=%v", action.Table, action.Column, migrated, err)
		}
	}
}

func TestApplyBatchRejectsInvalidBeforeAlter(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if err := prepareTwoTables(ctx, db); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE juhe_business.accounts SET updated_at = 'not-a-time'`)
		return err
	})
	ctx := context.Background()
	actions := actionsForAccounts()
	if _, err := ApplyBatch(ctx, db, actions); err == nil {
		t.Fatal("非法 updated_at 应在加锁前中止批次")
	}
	var dataType string
	if err := db.QueryRowContext(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema='juhe_business' AND table_name='accounts' AND column_name='created_at'`).Scan(&dataType); err != nil {
		t.Fatal(err)
	}
	if dataType != "text" {
		t.Fatalf("created_at 应保持 text，实际 %s", dataType)
	}
}

func TestPreflightBatchGroupsByTable(t *testing.T) {
	db := pgtestkit.New(t, prepareTwoTables)
	ctx := context.Background()
	actions := actionsForAccounts()
	if err := PreflightBatch(ctx, db, actions); err != nil {
		t.Fatalf("合法值批量预检失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.accounts SET updated_at = ''`); err != nil {
		t.Fatal(err)
	}
	err := PreflightBatch(ctx, db, actions)
	if err == nil || !strings.Contains(err.Error(), "updated_at") {
		t.Fatalf("批量预检应报 updated_at，实际 %v", err)
	}
}

func TestPreflightAcceptsCanonicalAndRejectsInvalid(t *testing.T) {
	db := pgtestkit.New(t, prepareTwoTables)
	ctx := context.Background()
	action := actionsForAccounts()[0]
	if err := Preflight(ctx, db, action); err != nil {
		t.Fatalf("合法值预检失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.accounts SET created_at = ''`); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(ctx, db, action); err == nil {
		t.Fatal("空串应被预检拒绝")
	}
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.accounts SET created_at = '2026-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(ctx, db, action); err == nil {
		t.Fatal("无 offset 应被预检拒绝")
	}
}
