package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/jackc/pgx/v5/pgconn"
)

// 这是合并撞号的代表性夹具：先由当前管线初始化，再定向回退起点相关形态。
// 25..32 的旧表形态没有复刻，保留目标态供管线采纳，不代表完整历史生产快照。
func TestUpgradeMergeRevisionPathsPG(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	for _, revision := range []int64{24, 34, 35} {
		t.Run(fmt.Sprintf("revision_%d_to_36", revision), func(t *testing.T) {
			td := pgtestkit.NewTestDatabase(t)
			ctx := context.Background()
			const secret = "merge-revision-test-secret"
			fixed := time.Date(2026, 11, 12, 3, 4, 5, 123000000, time.UTC)
			opts := upgradepipeline.Options{PrimaryDSN: td.DSN, Secret: secret, StopWriteConfirmed: true, BackupConfirmed: true, StepTimeout: 4 * time.Minute, AppVersion: "merge-revision-test"}
			steps := func() []upgradepipeline.Step { return upgradeSteps(td.DSN, secret) }
			fresh := upgradepipeline.Run(ctx, opts, steps())
			if fresh.ExitCode != upgradepipeline.ExitOK || fresh.Report == nil || fresh.Report.Result != upgradepipeline.ResultUpgraded || !reportPostflightOK(fresh.Report) {
				t.Fatalf("fresh 初始化失败: code=%d result=%s stderr=%s postflight=%s", fresh.ExitCode, reportResult(fresh.Report), strings.ReplaceAll(fresh.Stderr, td.DSN, "[DSN]"), mergePostflightDetails(fresh.Report, td.DSN))
			}
			sealed, err := accountcrypto.EncryptJSON(secret, map[string]any{"api_key": "merge-revision-test-key", "supported_endpoint_modes": []string{"chat_json"}})
			if err != nil {
				t.Fatalf("加密夹具失败: %v", err)
			}
			mergeExec(t, td.DB, td.DSN, `INSERT INTO juhe_business.accounts (id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at) VALUES ('merge-account','sys_admin','gpt','gpt','openai','v1','merge-account','api_key',$1,'m','chat_json',$2,$2)`, sealed, fixed)
			mergeExec(t, td.DB, td.DSN, `INSERT INTO juhe_business.global_settings (key,value_json,updated_at) VALUES ('merge-revision-probe','{}',$1)`, fixed)
			mergeExec(t, td.DB, td.DSN, `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,transition_id,dispatch_revision,status,available_at_ms,created_at_ms,updated_at_ms) VALUES ('merge-event','merge-projection','merge-dedupe','dispatch_revision_changed','merge-account','merge-account','merge-transition',1,'pending',$1,$1,$1)`, fixed)

			if revision == 24 {
				tx, err := td.DB.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("开始夹具事务失败: %s", strings.ReplaceAll(err.Error(), td.DSN, "[DSN]"))
				}
				defer tx.Rollback()
				mergeTxExec(t, tx, td.DSN, `SET LOCAL timezone='UTC'; SET LOCAL lock_timeout='5s'`)
				mergeTxExec(t, tx, td.DSN, `ALTER TABLE juhe_business.global_settings ALTER COLUMN updated_at TYPE text USING to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`)
				if len(epochmsmigration.CircuitRuntimeColumns) != 13 {
					t.Fatalf("circuit 列数=%d, want 13", len(epochmsmigration.CircuitRuntimeColumns))
				}
				columns := append(append([]epochmsmigration.Column{}, epochmsmigration.CircuitRuntimeColumns...), epochmsmigration.ListAvailabilityDirtyColumns...)
				for _, c := range columns {
					q := fmt.Sprintf(`ALTER TABLE juhe_business."%s" ALTER COLUMN "%s" TYPE bigint USING trunc(extract(epoch from "%s") * 1000)::bigint`, c.Table, c.Column, c.Column)
					mergeTxExec(t, tx, td.DSN, q)
				}
				mergeTxExec(t, tx, td.DSN, `DELETE FROM juhe_business.schema_migrations WHERE revision BETWEEN 24 AND 34`)
				if err := tx.Commit(); err != nil {
					t.Fatalf("提交夹具失败: %s", strings.ReplaceAll(err.Error(), td.DSN, "[DSN]"))
				}
			} else if revision == 34 {
				mergeExec(t, td.DB, td.DSN, `ALTER TABLE juhe_business.account_circuit_outbox DROP CONSTRAINT account_circuit_outbox_status_check, DROP CONSTRAINT account_circuit_outbox_status_invariant, ADD CHECK (status IN ('pending','processing','dispatched')), ADD CHECK ((status='pending' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL) OR (status='processing' AND claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL AND acknowledged_at_ms IS NULL) OR (status='dispatched' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NOT NULL))`)
				_, err := td.DB.ExecContext(ctx, `UPDATE juhe_business.account_circuit_outbox SET status='failed', acknowledged_at_ms=$1 WHERE event_id='merge-event'`, fixed)
				mergeCheckViolation(t, err)
			}
			if revision == 24 {
				// 在旧列类型上安装旧函数，并走真实账户 UPDATE 触发器。
				mergeExec(t, td.DB, td.DSN, schema.DirtyAvailabilityFunctionInstallerSQL())
				mergeAssertDirtyUpdate(t, td.DB, td.DSN, true, "before-upgrade")
			} else {
				// 34/35 夹具刻意复现旧函数与新列类型不匹配的合并态。
				mergeInstallOldDirtyFunction(t, td.DB, td.DSN)
				_, err := td.DB.ExecContext(ctx, `UPDATE juhe_business.accounts SET name='before-upgrade' WHERE id='merge-account'`)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "42804" {
					t.Fatalf("旧 dirty 函数应触发类型错误 42804，实际错误类型=%T", err)
				}
			}
			digest, err := schema.PostgresActualShapeDigest(ctx, td.DB)
			if err != nil {
				t.Fatalf("计算夹具形态失败: %s", strings.ReplaceAll(err.Error(), td.DSN, "[DSN]"))
			}
			if err := upgradepipeline.WriteSchemaState(ctx, td.DB, revision, revision, true, digest, "merge-revision-fixture", fixed); err != nil {
				t.Fatalf("写夹具 state 失败: %s", strings.ReplaceAll(err.Error(), td.DSN, "[DSN]"))
			}
			mergeAssertState(t, td.DB, revision)
			mergeAssertColumns(t, td.DB, revision == 24)
			mergeAssertFixtureData(t, td.DB, fixed, revision == 24)
			beforeMigrationDirty := mergeReadDirty(t, td.DB, revision == 24)
			var count, maxRevision int
			if err := td.DB.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE revision BETWEEN 24 AND 34), max(revision) FROM juhe_business.schema_migrations`).Scan(&count, &maxRevision); err != nil {
				t.Fatalf("读取账本失败: %v", err)
			}
			if revision == 24 && (count != 0 || maxRevision != 23) || revision >= 34 && (count != 11 || maxRevision != 34) {
				t.Fatalf("起点账本: count=%d max=%d", count, maxRevision)
			}
			beforeLedger := mergeLedger(t, td.DB)
			dryOpts := opts
			dryOpts.DryRun = true
			dryOpts.StopWriteConfirmed = false
			dryOpts.BackupConfirmed = false
			dry := upgradepipeline.Run(ctx, dryOpts, steps())
			if dry.ExitCode != upgradepipeline.ExitNeedsHuman || dry.Report == nil || dry.Report.Result != upgradepipeline.ResultBlocked {
				t.Fatalf("dry-run 不符: code=%d result=%s stderr=%s", dry.ExitCode, reportResult(dry.Report), strings.ReplaceAll(dry.Stderr, td.DSN, "[DSN]"))
			}
			planned, pending := map[int64]bool{}, map[int64]bool{}
			for _, p := range dry.Report.Plan.Pending {
				planned[p.Revision] = true
				pending[p.Revision] = true
			}
			for _, a := range dry.Report.Plan.Adopt {
				planned[a.Revision] = true
			}
			if revision == 24 {
				if len(planned) != 11 || !pending[24] || !pending[33] || !pending[34] {
					t.Fatalf("dry-run 计划不符: planned=%v pending=%v", planned, pending)
				}
				for r := int64(24); r <= 34; r++ {
					if !planned[r] {
						t.Fatalf("dry-run 缺 revision %d", r)
					}
				}
			} else if len(planned) != 0 {
				t.Fatalf("revision %d 计划应为空: %v", revision, planned)
			}
			if mergeLedger(t, td.DB) != beforeLedger {
				t.Fatal("dry-run 改变了账本")
			}
			out := upgradepipeline.Run(ctx, opts, steps())
			if out.ExitCode != upgradepipeline.ExitOK || out.Report == nil || out.Report.Result != upgradepipeline.ResultUpgraded || !reportPostflightOK(out.Report) {
				t.Fatalf("升级失败: code=%d result=%s stderr=%s postflight=%s", out.ExitCode, reportResult(out.Report), strings.ReplaceAll(out.Stderr, td.DSN, "[DSN]"), mergePostflightDetails(out.Report, td.DSN))
			}
			if revision == 24 {
				applied := map[int64]bool{}
				for _, e := range out.Report.Executed {
					if e.Status == "applied" {
						applied[e.Revision] = true
					}
				}
				if !applied[24] || !applied[33] || !applied[34] {
					t.Fatalf("24/33/34 未实际执行: %v", applied)
				}
			} else if len(out.Report.Executed) != 0 || mergeLedger(t, td.DB) != beforeLedger {
				t.Fatalf("revision %d 升级改变步骤账本", revision)
			}
			mergeAssertState(t, td.DB, 36)
			mergeAssertColumns(t, td.DB, false)
			afterMigrationDirty := mergeReadDirty(t, td.DB, false)
			if afterMigrationDirty.available != beforeMigrationDirty.available || afterMigrationDirty.created != beforeMigrationDirty.created || afterMigrationDirty.updated != beforeMigrationDirty.updated || afterMigrationDirty.generation != beforeMigrationDirty.generation {
				t.Fatalf("dirty 时间列迁移未保留数值: before=%+v after=%+v", beforeMigrationDirty, afterMigrationDirty)
			}
			mergeAssertLedger(t, td.DB, revision == 24)
			mergeAssertData(t, td.DB, fixed)
			mergeAssertConstraints(t, td.DB, fixed)
			mergeAssertDirtyUpdate(t, td.DB, td.DSN, false, "after-upgrade")
			mergeExec(t, td.DB, td.DSN, `INSERT INTO juhe_business.group_accounts (system_account_id,group_id,account_id,created_at,updated_at) VALUES ('sys_admin','grp_default_gpt_sys_admin','merge-account',$1,$1)`, fixed)
			mergeAssertDirtyOperation(t, td.DB, td.DSN, false, `UPDATE juhe_business.groups SET description='merge revision probe' WHERE id='grp_default_gpt_sys_admin'`)
			ledger := mergeLedger(t, td.DB)
			dirtySnapshot := mergeDirtySnapshot(t, td.DB)
			rerunOpts := opts
			rerunOpts.StopWriteConfirmed = false
			rerunOpts.BackupConfirmed = false
			rerun := upgradepipeline.Run(ctx, rerunOpts, steps())
			if rerun.ExitCode != upgradepipeline.ExitOK || rerun.Report == nil || rerun.Report.Result != upgradepipeline.ResultUpToDate || !reportPostflightOK(rerun.Report) || len(rerun.Report.Plan.Pending) != 0 || len(rerun.Report.Plan.Adopt) != 0 || len(rerun.Report.Executed) != 0 {
				t.Fatalf("重跑不符: code=%d result=%s stderr=%s postflight=%s", rerun.ExitCode, reportResult(rerun.Report), strings.ReplaceAll(rerun.Stderr, td.DSN, "[DSN]"), mergePostflightDetails(rerun.Report, td.DSN))
			}
			if mergeLedger(t, td.DB) != ledger {
				t.Fatal("重跑改变账本")
			}
			if mergeDirtySnapshot(t, td.DB) != dirtySnapshot {
				t.Fatal("重跑改变 dirty 数据")
			}
			mergeAssertData(t, td.DB, fixed)
			mergeAssertConstraints(t, td.DB, fixed)
		})
	}
}

func mergePostflightDetails(report *upgradepipeline.Report, dsn string) string {
	if report == nil || report.Postflight == nil {
		return "<nil>"
	}
	data, err := json.Marshal(report.Postflight)
	if err != nil {
		return "<postflight 编码失败>"
	}
	return strings.ReplaceAll(string(data), dsn, "[DSN]")
}

func mergeExec(t *testing.T, db *sql.DB, dsn, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("执行夹具 SQL 失败: %s", strings.ReplaceAll(err.Error(), dsn, "[DSN]"))
	}
}

func mergeTxExec(t *testing.T, tx *sql.Tx, dsn, query string) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("执行夹具事务失败: %s", strings.ReplaceAll(err.Error(), dsn, "[DSN]"))
	}
}

func mergeInstallOldDirtyFunction(t *testing.T, db *sql.DB, dsn string) {
	t.Helper()
	mergeExec(t, db, dsn, schema.DirtyAvailabilityFunctionLegacySQL())
}

func mergeDirtySnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var snapshot string
	if err := db.QueryRow(`SELECT row_to_json(d)::text FROM juhe_business.account_list_availability_dirty d WHERE account_id='merge-account'`).Scan(&snapshot); err != nil {
		t.Fatalf("读取 dirty 快照失败: %v", err)
	}
	return snapshot
}

type mergeDirtyValues struct {
	generation, available, created, updated int64 // 时间统一为 epoch 微秒。
	cleared                                 bool
}

func mergeReadDirty(t *testing.T, db *sql.DB, old bool) mergeDirtyValues {
	t.Helper()
	query := `SELECT generation, trunc(extract(epoch from available_at_ms)*1000000)::bigint,
		trunc(extract(epoch from created_at_ms)*1000000)::bigint, trunc(extract(epoch from updated_at_ms)*1000000)::bigint,
		claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL
		FROM juhe_business.account_list_availability_dirty WHERE account_id='merge-account'`
	if old {
		query = `SELECT generation, available_at_ms*1000, created_at_ms*1000, updated_at_ms*1000,
			claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL
			FROM juhe_business.account_list_availability_dirty WHERE account_id='merge-account'`
	}
	var result mergeDirtyValues
	if err := db.QueryRow(query).Scan(&result.generation, &result.available, &result.created, &result.updated, &result.cleared); err != nil {
		t.Fatalf("读取 dirty 状态失败: %v", err)
	}
	return result
}

func mergeDBClock(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var now int64
	if err := db.QueryRow(`SELECT trunc(extract(epoch from clock_timestamp())*1000000)::bigint`).Scan(&now); err != nil {
		t.Fatalf("读取数据库时钟失败: %v", err)
	}
	return now
}

func mergeAssertDirtyUpdate(t *testing.T, db *sql.DB, dsn string, old bool, name string) {
	t.Helper()
	mergeAssertDirtyOperation(t, db, dsn, old, `UPDATE juhe_business.accounts SET name=$1 WHERE id='merge-account'`, name)
}

func mergeAssertDirtyOperation(t *testing.T, db *sql.DB, dsn string, old bool, query string, args ...any) {
	t.Helper()
	if old {
		mergeExec(t, db, dsn, `UPDATE juhe_business.account_list_availability_dirty SET claim_token='probe',claimed_by='probe',claim_until_ms=(extract(epoch from clock_timestamp())*1000)::bigint WHERE account_id='merge-account'`)
	} else {
		mergeExec(t, db, dsn, `UPDATE juhe_business.account_list_availability_dirty SET claim_token='probe',claimed_by='probe',claim_until_ms=clock_timestamp() WHERE account_id='merge-account'`)
	}
	before := mergeReadDirty(t, db, old)
	start := mergeDBClock(t, db)
	mergeExec(t, db, dsn, query, args...)
	end := mergeDBClock(t, db)
	after := mergeReadDirty(t, db, old)
	if old {
		start = (start / 1000) * 1000 // 旧函数以毫秒向下取整。
	}
	wantAvailable := before.available
	if after.updated < wantAvailable {
		wantAvailable = after.updated
	}
	if after.generation != before.generation+1 || !after.cleared || after.created != before.created || after.available != wantAvailable || after.updated < start || after.updated > end {
		t.Fatalf("dirty 操作结果不符: before=%+v after=%+v clock=[%d,%d] wantAvailable=%d", before, after, start, end, wantAvailable)
	}
}

func mergeCheckViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("预期 CHECK 拒绝 (23514)，实际错误类型=%T", err)
	}
}

func mergeAssertState(t *testing.T, db *sql.DB, want int64) {
	t.Helper()
	var revision, minReadable int64
	var expanded bool
	if err := db.QueryRow(`SELECT schema_revision,min_readable_binary_revision,expand_completed FROM juhe_business.schema_state WHERE id=1`).Scan(&revision, &minReadable, &expanded); err != nil {
		t.Fatalf("读取 state 失败: %v", err)
	}
	if revision != want || minReadable != want || !expanded {
		t.Fatalf("state=(%d,%d,%v), want (%d,%d,true)", revision, minReadable, expanded, want, want)
	}
}

func mergeAssertColumns(t *testing.T, db *sql.DB, old bool) {
	t.Helper()
	check := func(table, column, typ string, precision int) {
		t.Helper()
		var dataType string
		var actualPrecision sql.NullInt64
		if err := db.QueryRow(`SELECT data_type,datetime_precision FROM information_schema.columns WHERE table_schema='juhe_business' AND table_name=$1 AND column_name=$2`, table, column).Scan(&dataType, &actualPrecision); err != nil {
			t.Fatalf("读取 %s.%s 类型失败: %v", table, column, err)
		}
		if dataType != typ || precision >= 0 && (!actualPrecision.Valid || actualPrecision.Int64 != int64(precision)) {
			t.Fatalf("%s.%s 类型=%s 精度=%v, want %s/%d", table, column, dataType, actualPrecision, typ, precision)
		}
	}
	if old {
		check("global_settings", "updated_at", "text", -1)
	} else {
		check("global_settings", "updated_at", "timestamp with time zone", 3)
	}
	if len(epochmsmigration.CircuitRuntimeColumns) != 13 {
		t.Fatalf("circuit 列数=%d", len(epochmsmigration.CircuitRuntimeColumns))
	}
	for _, c := range epochmsmigration.CircuitRuntimeColumns {
		if old {
			check(c.Table, c.Column, "bigint", -1)
		} else {
			check(c.Table, c.Column, "timestamp with time zone", 6)
		}
	}
	if len(epochmsmigration.ListAvailabilityDirtyColumns) != 4 {
		t.Fatalf("dirty 列数=%d", len(epochmsmigration.ListAvailabilityDirtyColumns))
	}
	for _, c := range epochmsmigration.ListAvailabilityDirtyColumns {
		if old {
			check(c.Table, c.Column, "bigint", -1)
		} else {
			check(c.Table, c.Column, "timestamp with time zone", 6)
		}
	}
}

func mergeLedger(t *testing.T, db *sql.DB) string {
	t.Helper()
	var snapshot string
	if err := db.QueryRow(`SELECT coalesce(json_agg(row_to_json(m) ORDER BY m.revision)::text, '[]') FROM juhe_business.schema_migrations m`).Scan(&snapshot); err != nil {
		t.Fatalf("读取账本快照失败: %v", err)
	}
	return snapshot
}

func mergeAssertLedger(t *testing.T, db *sql.DB, applied bool) {
	t.Helper()
	ids := []string{"instant-timestamptz-small-tables", "instant-timestamptz-crud", "instant-timestamptz-accounts", "instant-timestamptz-dataset-jobs", "stats-calendar-timestamptz", "usage-records-timestamptz-prepare", "usage-records-timestamptz-swap", "usage-records-timestamptz-retire", "account-lock-state-timestamptz", "account-list-availability-dirty-timestamptz", "circuit-runtime-timestamptz"}
	var total, ddlRevisions int
	if err := db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE revision IN (35,36)) FROM juhe_business.schema_migrations`).Scan(&total, &ddlRevisions); err != nil {
		t.Fatalf("读取账本计数失败: %v", err)
	}
	if total != 26 || ddlRevisions != 0 {
		t.Fatalf("账本总数=%d revision35/36=%d", total, ddlRevisions)
	}
	for i, expected := range ids {
		revision := i + 24
		var id, status string
		var impl int
		var adopted bool
		if err := db.QueryRow(`SELECT step_id,status,impl_version,adopted FROM juhe_business.schema_migrations WHERE revision=$1`, revision).Scan(&id, &status, &impl, &adopted); err != nil {
			t.Fatalf("读取 revision %d 失败: %v", revision, err)
		}
		if id != expected || status != "applied" || impl != 1 || (applied && (revision == 24 || revision == 33 || revision == 34) && adopted) {
			t.Fatalf("revision %d: id=%s status=%s impl=%d adopted=%v", revision, id, status, impl, adopted)
		}
	}
}

func mergeAssertFixtureData(t *testing.T, db *sql.DB, fixed time.Time, old bool) {
	t.Helper()
	var valid bool
	if old {
		if err := db.QueryRow(`SELECT updated_at=$1::text FROM juhe_business.global_settings WHERE key='merge-revision-probe'`, fixed.Format("2006-01-02T15:04:05.000Z")).Scan(&valid); err != nil || !valid {
			t.Fatalf("旧 global_settings 探针不符: valid=%v err=%v", valid, err)
		}
		if err := db.QueryRow(`SELECT available_at_ms=$1::bigint AND created_at_ms=$1::bigint AND updated_at_ms=$1::bigint AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL FROM juhe_business.account_circuit_outbox WHERE event_id='merge-event'`, fixed.UnixMilli()).Scan(&valid); err != nil || !valid {
			t.Fatalf("旧 outbox 探针不符: valid=%v err=%v", valid, err)
		}
	} else {
		mergeAssertData(t, db, fixed)
	}
}

func mergeAssertData(t *testing.T, db *sql.DB, fixed time.Time) {
	t.Helper()
	var valid bool
	if err := db.QueryRow(`SELECT updated_at=$1::timestamptz FROM juhe_business.global_settings WHERE key='merge-revision-probe'`, fixed).Scan(&valid); err != nil || !valid {
		t.Fatalf("global_settings 探针不符: valid=%v err=%v", valid, err)
	}
	if err := db.QueryRow(`SELECT status='pending' AND available_at_ms=$1::timestamptz AND created_at_ms=$1::timestamptz AND updated_at_ms=$1::timestamptz AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL FROM juhe_business.account_circuit_outbox WHERE event_id='merge-event'`, fixed).Scan(&valid); err != nil || !valid {
		t.Fatalf("outbox 探针不符: valid=%v err=%v", valid, err)
	}
}

func mergeAssertConstraints(t *testing.T, db *sql.DB, fixed time.Time) {
	t.Helper()
	var statusDef, invariantDef string
	for name, target := range map[string]*string{"account_circuit_outbox_status_check": &statusDef, "account_circuit_outbox_status_invariant": &invariantDef} {
		if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='juhe_business.account_circuit_outbox'::regclass AND conname=$1`, name).Scan(target); err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
	}
	for _, status := range []string{"pending", "processing", "dispatched", "failed"} {
		if !strings.Contains(statusDef, status) || !strings.Contains(invariantDef, status) {
			t.Fatalf("约束缺少 %s", status)
		}
	}
	if _, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET status='failed',acknowledged_at_ms=$1 WHERE event_id='merge-event'`, fixed); err != nil {
		t.Fatalf("合法 failed 被拒绝: %v", err)
	}
	_, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET claim_token='token',claimed_by='owner',claim_until_ms=$1 WHERE event_id='merge-event'`, fixed)
	mergeCheckViolation(t, err)
	_, err = db.Exec(`UPDATE juhe_business.account_circuit_outbox SET status='unknown' WHERE event_id='merge-event'`)
	mergeCheckViolation(t, err)
	_, err = db.Exec(`UPDATE juhe_business.account_circuit_outbox SET dispatch_revision=0 WHERE event_id='merge-event'`)
	mergeCheckViolation(t, err)
	if _, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET status='pending',acknowledged_at_ms=NULL WHERE event_id='merge-event'`); err != nil {
		t.Fatalf("恢复探针失败: %v", err)
	}
}
