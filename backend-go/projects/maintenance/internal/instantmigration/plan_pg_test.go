package instantmigration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestApplyAndVerify(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		statements := []string{
			`CREATE SCHEMA juhe_business`,
			`CREATE TABLE juhe_business.accounts (
				id text PRIMARY KEY,
				created_at text NOT NULL
			)`,
			`INSERT INTO juhe_business.accounts (id, created_at) VALUES
				('a', '2026-01-01T00:00:00.000Z')`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL timezone = 'UTC'`); err != nil {
		t.Fatalf("timezone: %v", err)
	}
	action := Action{
		Schema: "juhe_business", Table: "accounts", Column: "created_at",
		CurrentType: "text", TargetType: "timestamptz(3)",
		SQL: `ALTER TABLE "juhe_business"."accounts" ALTER COLUMN "created_at" TYPE timestamptz(3) USING "created_at"::timestamptz`,
	}
	if err := Apply(ctx, tx, action); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := Verify(ctx, tx, action); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var got string
	if err := tx.QueryRowContext(ctx, `SELECT created_at::text FROM juhe_business.accounts WHERE id='a'`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "2026-01-01 00:00:00+00" {
		t.Fatalf("created_at = %q", got)
	}
}

func TestApplyRejectsInvalidText(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		statements := []string{
			`CREATE SCHEMA juhe_business`,
			`CREATE TABLE juhe_business.accounts (
				id text PRIMARY KEY,
				created_at text NOT NULL
			)`,
			`INSERT INTO juhe_business.accounts (id, created_at) VALUES ('bad', 'not-a-time')`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	action := Action{
		Schema: "juhe_business", Table: "accounts", Column: "created_at",
		SQL: `ALTER TABLE "juhe_business"."accounts" ALTER COLUMN "created_at" TYPE timestamptz(3) USING "created_at"::timestamptz`,
	}
	if err := Apply(ctx, tx, action); err == nil {
		t.Fatal("非法文本应让 ALTER TYPE 失败")
	}
}
