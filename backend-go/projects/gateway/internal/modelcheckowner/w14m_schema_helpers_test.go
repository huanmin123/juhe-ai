package modelcheckowner

// w14m 覆盖率补强：store 的 checkColumns/CheckSchema/健康重试扫描臂、
// CheckBusinessPostgresSchema 变体臂（w14mfailpg 驱动注入，门控库不可达时
// 整体跳过）与 trust 游标比较的双侧非法时间戳分支。原 business_schema
// SQLite 检查辅助臂已随 CheckBusinessSQLiteSchema 退役（D17）。
// 连接串只从 w11e 门控 helper 读取，不写入断言或日志。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestW14MCompareTrustCursorInvalidTimestamps(t *testing.T) {
	// 双侧 created_at 都无法解析时回退到稳定的 id 字典序比较。
	if got := compareTrustCursor("bad", "zzz", "nope", "aaa"); got <= 0 {
		t.Fatalf("compareTrustCursor = %d, want > 0", got)
	}
	if got := compareTrustCursor("bad", "aaa", "nope", "zzz"); got >= 0 {
		t.Fatalf("compareTrustCursor = %d, want < 0", got)
	}
}

// ---- store.go：checkColumns / CheckSchema / 健康重试扫描臂 ----

func w14mPgStore(db *sql.DB, schema string) *Store {
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		panic(err)
	}
	return &Store{db: db, mode: "postgres", schema: schema, HealthStatHour: statHour}
}

func w14mPlainStore(db *sql.DB, mode string) *Store {
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		panic(err)
	}
	return &Store{db: db, mode: mode, schema: "juhe_business", HealthStatHour: statHour}
}

func TestW14MStoreCheckColumnsArms(t *testing.T) {
	ctx := context.Background()

	t.Run("emptyRequired", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.disarm()
		if err := w14mPlainStore(db, "sqlite").checkColumns(ctx, "model_check_inputs", nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("sqliteScanError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.armScan("PRAGMA table_info")
		defer fp.disarm()
		err := w14mPlainStore(db, "sqlite").checkColumns(ctx, "model_check_inputs", []string{"input_id"})
		if err == nil || !strings.Contains(err.Error(), "scan J3b SQLite schema columns") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("sqliteIterateError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.armNextErr("PRAGMA table_info")
		defer fp.disarm()
		err := w14mPlainStore(db, "sqlite").checkColumns(ctx, "model_check_inputs", []string{"input_id"})
		if err == nil || !strings.Contains(err.Error(), "iterate J3b SQLite schema columns") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("checkSchemaTableMismatch", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.armScan1("type='table' AND name=?")
		defer fp.disarm()
		err := w14mPlainStore(db, "sqlite").CheckSchema(ctx)
		if err == nil || !strings.Contains(err.Error(), "returned unexpected table") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("applyHealthFactUpsertError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.arm("ON CONFLICT(account_id,stat_hour)")
		defer fp.disarm()
		changed, err := w14mPlainStore(db, "sqlite").ApplyHealthFact(ctx, HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", Level: "unavailable", StatHour: "2026-08-27T10", RunID: "run-1", ObservedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC), Threshold: 80, Score: 10})
		if err == nil || changed || !strings.Contains(err.Error(), "upsert J3b health fact") {
			t.Fatalf("changed=%t err = %v", changed, err)
		}
	})
}

func TestW14MStoreHealthRetryScanArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, db *sql.DB, runID, accountID string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,level,score,max_score,message,account_id,quality_health_sync_status,finished_at,schedule_id) VALUES ('` + runID + `','sys','sys','openai','account','acct','gpt-5.6-sol','quick','manual','completed','{"configRevision":"3"}','{}','{"revision":"4","action":"fallback","threshold":82,"recoveryIntervalMinutes":15,"manualEnforcementEligible":true}','{"evidenceFormed":true,"trustFormed":true,"hardQualityFailure":true}','probe-v1','2026-08-31T10:00:00Z','2026-08-31T10:00:00Z','2026-08-31T10:00:00Z','unavailable',0,100,'','` + accountID + `','failed','2026-08-31T10:00:00Z','sch-x')`)
		if err != nil {
			t.Fatalf("种子行插入失败: %v", err)
		}
	}

	t.Run("emptyIdentitySkipped", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.disarm()
		seed(t, db, "run-w14m-empty", "")
		retries, err := w14mPlainStore(db, "sqlite").ListHealthSyncRetries(ctx, 10)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(retries) != 0 {
			t.Fatalf("空身份行应被跳过: %v", retries)
		}
	})

	t.Run("iterateError", func(t *testing.T) {
		db, fp := w14mFailDB(t, runtimeTestDDL())
		defer func() { _ = db.Close() }()
		fp.disarm()
		seed(t, db, "run-w14m-scan", "acct")
		fp.armNextErr("quality_health_sync_status='failed'")
		defer fp.disarm()
		if _, err := w14mPlainStore(db, "sqlite").ListHealthSyncRetries(ctx, 10); err == nil || !strings.Contains(err.Error(), "iterate J3b health sync retries") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- PostgreSQL 门控臂 ----

func TestW14MStorePostgresSchemaArms(t *testing.T) {
	failDB := w14mOpenFailCoverDB(t)
	realDB := w11eOpenCoverDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()
	schema := "w14m_bs_pg"
	w11eRecreateSchema(t, realDB, schema, w11eNewPlan())

	t.Run("openStorePostgresOK", func(t *testing.T) {
		store, err := OpenStore(Config{Enabled: true, StoreMode: "postgres", PostgresURL: w11eCoverPostgresURL(t), BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if store.mode != "postgres" || store.schema != "juhe_j3b" {
			t.Fatalf("mode=%s schema=%s", store.mode, store.schema)
		}
		_ = store.Close()
	})

	t.Run("checkSchemaTableMismatch", func(t *testing.T) {
		w14mPgFP.armScan1("table_schema=$1 AND table_name=$2")
		defer w14mPgFP.disarm()
		store := w14mPgStore(failDB, schema)
		err := store.CheckSchema(ctx)
		if err == nil || !strings.Contains(err.Error(), "returned unexpected table") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("checkColumnsArms", func(t *testing.T) {
		cases := []struct {
			name string
			arm  func(pattern string)
			need string
		}{
			{"queryError", func(pattern string) { w14mPgFP.arm(pattern) }, "read J3b schema columns"},
			{"scanError", func(pattern string) { w14mPgFP.armScan(pattern) }, "scan J3b schema columns"},
			{"iterateError", func(pattern string) { w14mPgFP.armNextErr(pattern) }, "iterate J3b schema columns"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				tc.arm("table_schema=$1 AND table_name=$2")
				defer w14mPgFP.disarm()
				store := w14mPgStore(failDB, schema)
				err := store.checkColumns(ctx, "model_check_inputs", []string{"input_id"})
				if err == nil || !strings.Contains(err.Error(), tc.need) {
					t.Fatalf("err = %v, want 包含 %q", err, tc.need)
				}
			})
		}
	})

	t.Run("checkColumnsPostgresPass", func(t *testing.T) {
		w14mPgFP.disarm()
		var tableName, colName string
		err := realDB.QueryRowContext(ctx, `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema=$1 ORDER BY table_name, column_name LIMIT 1`, schema).Scan(&tableName, &colName)
		if err != nil {
			t.Skipf("schema %s 无可用列: %v", schema, err)
		}
		store := w14mPgStore(realDB, schema)
		if err := store.checkColumns(ctx, tableName, []string{colName}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}
