package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/jackc/pgx/v5/pgconn"
)

func dirtyRevisionFixture(t *testing.T, columnType string) *sql.DB {
	t.Helper()
	td := pgtestkit.NewTestDatabase(t)
	ddl := fmt.Sprintf(`CREATE SCHEMA juhe_business;
 CREATE TABLE juhe_business.accounts (id text PRIMARY KEY, system_account_id text NOT NULL, authorization_instance_source_account_id text);
 CREATE TABLE juhe_business.account_list_availability_projections (account_id text, source_generation bigint);
 CREATE TABLE juhe_business.account_list_availability_dirty (
 account_id text PRIMARY KEY, viewer_system_account_id text NOT NULL, generation bigint NOT NULL, applied_generation bigint NOT NULL,
 reason text, available_at_ms %s NOT NULL, claim_token text, claimed_by text, claim_until_ms %s,
 attempt_count integer NOT NULL, created_at_ms %s NOT NULL, updated_at_ms %s NOT NULL);
 INSERT INTO juhe_business.accounts (id, system_account_id) VALUES ('account-1','owner-1')`, columnType, columnType, columnType, columnType)
	if _, err := td.DB.ExecContext(context.Background(), ddl); err != nil {
		t.Fatal(err)
	}
	return td.DB
}

func dirtyExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatal(err)
	}
}

func TestDirtyAvailabilityInstallerPG(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	for _, columnType := range []string{"bigint", "timestamptz(6)"} {
		t.Run(columnType, func(t *testing.T) {
			db := dirtyRevisionFixture(t, columnType)
			// 从无函数开始安装并重复执行，验证两种列类型均可写入。
			dirtyExec(t, db, schema.DirtyAvailabilityFunctionInstallerSQL())
			dirtyExec(t, db, schema.DirtyAvailabilityFunctionInstallerSQL())
			dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'first')`)
			dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'second')`)
			var generation int
			var reason string
			if err := db.QueryRow(`SELECT generation, reason FROM juhe_business.account_list_availability_dirty WHERE account_id='account-1'`).Scan(&generation, &reason); err != nil {
				t.Fatal(err)
			}
			if generation != 2 || reason != "second" {
				t.Fatalf("dirty row generation=%d reason=%q", generation, reason)
			}
		})
	}
	for _, columnType := range []string{"timestamptz(6)", "text"} {
		t.Run("reject_"+columnType, func(t *testing.T) {
			db := dirtyRevisionFixture(t, "bigint")
			dirtyExec(t, db, schema.DirtyAvailabilityFunctionLegacySQL())
			dirtyExec(t, db, fmt.Sprintf(`ALTER TABLE juhe_business.account_list_availability_dirty ALTER COLUMN claim_until_ms TYPE %s USING NULL`, columnType))
			_, err := db.Exec(schema.DirtyAvailabilityFunctionInstallerSQL())
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42804" {
				t.Fatalf("expected 42804 for mixed columns, got %v", err)
			}
			var src string
			if err := db.QueryRow(`SELECT prosrc FROM pg_proc WHERE oid=to_regprocedure('juhe_business.account_list_availability_mark_dirty_accounts(text[],text)')`).Scan(&src); err != nil {
				t.Fatal(err)
			}
			if src == "\n"+schema.DirtyAvailabilityFunctionNativeBody()+"\n" {
				t.Fatal("rejected installation replaced legacy function")
			}
		})
	}
}

func TestRevision33FunctionInstallFailureRollsBackPG(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	db := dirtyRevisionFixture(t, "bigint")
	dirtyExec(t, db, schema.DirtyAvailabilityFunctionInstallerSQL())
	dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'before')`)
	var before string
	if err := db.QueryRow(`SELECT prosrc FROM pg_proc WHERE oid=to_regprocedure('juhe_business.account_list_availability_mark_dirty_accounts(text[],text)')`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// 第一个 DDL 安装新函数，随后制造失败以验证事务整体回滚。
	if _, err := applyRevision33Tx(context.Background(), tx, schema.DirtyAvailabilityFunctionNativeSQL()+` SELECT 1/0;`); err == nil {
		t.Fatal("expected installation failure")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema='juhe_business' AND table_name='account_list_availability_dirty' AND column_name IN ('available_at_ms','claim_until_ms','created_at_ms','updated_at_ms') AND data_type='bigint'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("rollback left %d bigint columns, want 4", count)
	}
	var after string
	if err := db.QueryRow(`SELECT prosrc FROM pg_proc WHERE oid=to_regprocedure('juhe_business.account_list_availability_mark_dirty_accounts(text[],text)')`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("rollback changed function")
	}
	dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'after')`)
}

func TestRevision33MigratesDirtyFunctionPG(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	db := dirtyRevisionFixture(t, "bigint")
	dirtyExec(t, db, schema.DirtyAvailabilityFunctionInstallerSQL())
	dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'before')`)
	var oldMillis int64
	if err := db.QueryRow(`SELECT available_at_ms FROM juhe_business.account_list_availability_dirty WHERE account_id='account-1'`).Scan(&oldMillis); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	count, err := applyRevision33Tx(context.Background(), tx, schema.DirtyAvailabilityFunctionNativeSQL())
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("migrated %d columns, want 4", count)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := db.QueryRow(`SELECT available_at_ms = to_timestamp($1::double precision / 1000.0) FROM juhe_business.account_list_availability_dirty WHERE account_id='account-1'`, oldMillis).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("migration changed instant")
	}
	var src string
	if err := db.QueryRow(`SELECT prosrc FROM pg_proc WHERE oid=to_regprocedure('juhe_business.account_list_availability_mark_dirty_accounts(text[],text)')`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != "\n"+schema.DirtyAvailabilityFunctionNativeBody()+"\n" {
		t.Fatal("native function not installed")
	}
	dirtyExec(t, db, `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['account-1'], 'after')`)
}
