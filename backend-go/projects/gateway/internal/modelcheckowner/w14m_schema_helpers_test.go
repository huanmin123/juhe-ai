package modelcheckowner

// w14m 覆盖率补强：business_schema 的 SQLite/PostgreSQL schema 检查辅助函数
// 错误与变体臂、store 的 checkColumns/CheckSchema/健康重试扫描臂，以及
// trust 游标比较的双侧非法时间戳分支。SQLite 臂通过共享 failpoint 注入；
// PostgreSQL 臂通过 w14mfailpg 驱动注入，门控库不可达时整体跳过。
// 连接串只从 w11e 门控 helper 读取，不写入断言或日志。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// w14mCompliantFailSchemaDB 按 newBusinessSQLiteSchemaFixture 的契约 DDL
// 构造一个可全量通过 CheckBusinessSQLiteSchema 的 failpoint 包装库，
// 用于命中检查级错误包裹臂（列/唯一约束/索引定义/外键检查）。
func w14mCompliantFailSchemaDB(t *testing.T) (*sql.DB, *w14mFailpoint) {
	t.Helper()
	statements := make([]string, 0, len(contracts.BusinessSQLiteSchema)*3)
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns)+len(spec.ForeignKeys)+2)
		for _, column := range spec.Columns {
			defs = append(defs, quoteSQLiteIdentifier(column)+" TEXT")
		}
		for _, foreignKey := range spec.ForeignKeys {
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteSQLiteColumns(foreignKey.Columns), quoteSQLiteIdentifier(foreignKey.RefTable), quoteSQLiteColumns(foreignKey.RefColumns))
			if foreignKey.OnDelete != "" {
				clause += " ON DELETE " + foreignKey.OnDelete
			}
			if foreignKey.OnUpdate != "" {
				clause += " ON UPDATE " + foreignKey.OnUpdate
			}
			defs = append(defs, clause)
		}
		if len(spec.PrimaryKey) > 0 {
			defs = append(defs, "PRIMARY KEY ("+quoteSQLiteColumns(spec.PrimaryKey)+")")
		}
		for _, unique := range spec.UniqueConstraints {
			defs = append(defs, "UNIQUE ("+quoteSQLiteColumns(unique)+")")
		}
		statements = append(statements, `CREATE TABLE `+quoteSQLiteIdentifier(table)+` (`+strings.Join(defs, ",")+`)`)
		for _, index := range spec.Indexes {
			if hasSchemaIndexDefinition(spec.IndexDefinitions, index) {
				continue
			}
			statements = append(statements, `CREATE INDEX `+quoteSQLiteIdentifier(index)+` ON `+quoteSQLiteIdentifier(table)+` (`+quoteSQLiteIdentifier(spec.Columns[0])+`)`)
		}
		for _, index := range spec.IndexDefinitions {
			columns := make([]string, 0, len(index.Columns))
			for _, column := range index.Columns {
				columns = append(columns, quoteSQLiteIdentifier(column))
			}
			kind := "INDEX"
			if index.Unique {
				kind = "UNIQUE INDEX"
			}
			ddl := `CREATE ` + kind + ` ` + quoteSQLiteIdentifier(index.Name) + ` ON ` + quoteSQLiteIdentifier(table) + ` (` + strings.Join(columns, ",") + `)`
			if index.Predicate != "" {
				ddl += " WHERE " + index.Predicate
			}
			statements = append(statements, ddl)
		}
	}
	return w14mFailDB(t, statements)
}

func TestW14MSchemaSQLiteCheckLevelArms(t *testing.T) {
	ctx := context.Background()

	t.Run("compliantPasses", func(t *testing.T) {
		db, fp := w14mCompliantFailSchemaDB(t)
		fp.disarm()
		if err := CheckBusinessSQLiteSchema(ctx, db); err != nil {
			t.Fatalf("契约库应全量通过: %v", err)
		}
	})

	t.Run("columnsQueryError", func(t *testing.T) {
		db, fp := w14mCompliantFailSchemaDB(t)
		fp.arm("PRAGMA table_info")
		defer fp.disarm()
		err := CheckBusinessSQLiteSchema(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("uniqueInspectError", func(t *testing.T) {
		// 契约 map 迭代顺序随机：若先遍历到带 IndexDefinitions 的表，
		// 同一注入会先命中索引定义检查臂；两者都属"inspect Business SQLite"。
		db, fp := w14mCompliantFailSchemaDB(t)
		fp.arm("PRAGMA index_list")
		defer fp.disarm()
		err := CheckBusinessSQLiteSchema(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "inspect Business SQLite") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("indexDefinitionInspectError", func(t *testing.T) {
		// 唯一约束检查会先消耗若干次 PRAGMA index_info；用阈值扫描找到
		// 恰好落在 IndexDefinitions 检查内的注入点。
		for threshold := 0; threshold <= 32; threshold++ {
			db, fp := w14mCompliantFailSchemaDB(t)
			fp.armAfter("PRAGMA index_info", threshold)
			err := CheckBusinessSQLiteSchema(ctx, db)
			fp.disarm()
			_ = db.Close()
			if err == nil {
				continue
			}
			if strings.Contains(err.Error(), "inspect Business SQLite index") {
				return
			}
		}
		t.Fatal("未找到命中 IndexDefinitions 检查错误的注入阈值")
	})

	t.Run("foreignKeyInspectError", func(t *testing.T) {
		db, fp := w14mCompliantFailSchemaDB(t)
		fp.arm("PRAGMA foreign_key_list")
		defer fp.disarm()
		err := CheckBusinessSQLiteSchema(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14MSchemaSQLiteHelperArms(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		pattern string
		mode    string
		need    string // 为空表示任意非 nil 错误即可（扫描列数不匹配也证明分支命中）
		call    func(t *testing.T, db *sql.DB) error
	}{
		{"objectsScan", "FROM sqlite_master", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaObjects(ctx, db, "table")
			return err
		}},
		{"columnsScan", "PRAGMA table_info", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaColumns(ctx, db, "model_check_inputs")
			return err
		}},
		{"primaryKeyScan", "PRAGMA table_info", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaPrimaryKey(ctx, db, "model_check_inputs")
			return err
		}},
		{"primaryKeyIterate", "PRAGMA table_info", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaPrimaryKey(ctx, db, "model_check_inputs")
			return err
		}},
		{"uniqueScan", "PRAGMA index_list", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := sqliteSchemaHasUniqueConstraint(ctx, db, "model_check_inputs", []string{"id"})
			return err
		}},
		{"uniqueIterate", "PRAGMA index_list", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := sqliteSchemaHasUniqueConstraint(ctx, db, "model_check_inputs", []string{"id"})
			return err
		}},
		{"uniqueIndexQuery", "PRAGMA index_info", "query", "", func(t *testing.T, db *sql.DB) error {
			table, columns, ok := w14mFirstUniqueConstraintTable()
			if !ok {
				t.Skip("契约 spec 无唯一约束表")
			}
			_, _, err := sqliteSchemaHasUniqueConstraint(ctx, db, table, columns)
			return err
		}},
		{"indexColumnsScan", "PRAGMA index_info", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaIndexColumns(ctx, db, "sqlite_autoindex_model_check_inputs_1")
			return err
		}},
		{"indexColumnsIterate", "PRAGMA index_info", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaIndexColumns(ctx, db, "sqlite_autoindex_model_check_inputs_1")
			return err
		}},
		{"foreignKeyScan", "PRAGMA foreign_key_list", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaForeignKeys(ctx, db, "announcement_reads")
			return err
		}},
		{"foreignKeyIterate", "PRAGMA foreign_key_list", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, err := sqliteSchemaForeignKeys(ctx, db, "announcement_reads")
			return err
		}},
		{"indexMatchesListScan", "PRAGMA index_list", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := sqliteSchemaIndexMatches(ctx, db, "announcements", contracts.SQLiteIndexDefinition{Name: "idx_missing", Columns: []string{"id"}})
			return err
		}},
		{"indexMatchesListIterate", "PRAGMA index_list", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := sqliteSchemaIndexMatches(ctx, db, "announcements", contracts.SQLiteIndexDefinition{Name: "idx_missing", Columns: []string{"id"}})
			return err
		}},
		{"indexMatchesInfoQuery", "PRAGMA index_info", "query", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := w14mIndexMatchesOnRealIndex(db)
			return err
		}},
		{"indexMatchesInfoScan", "PRAGMA index_info", "scan", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := w14mIndexMatchesOnRealIndex(db)
			return err
		}},
		{"indexMatchesInfoIterate", "PRAGMA index_info", "nexterr", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := w14mIndexMatchesOnRealIndex(db)
			return err
		}},
		{"indexMatchesDefinitionQuery", "SELECT sql FROM sqlite_master", "query", "", func(t *testing.T, db *sql.DB) error {
			_, _, err := w14mIndexMatchesOnRealIndex(db)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, fp := w14mCompliantFailSchemaDB(t)
			defer func() { _ = db.Close() }()
			switch tc.mode {
			case "scan":
				fp.armScan(tc.pattern)
			case "nexterr":
				fp.armNextErr(tc.pattern)
			default:
				fp.arm(tc.pattern)
			}
			defer fp.disarm()
			err := tc.call(t, db)
			if err == nil || (tc.need != "" && !strings.Contains(err.Error(), tc.need)) {
				t.Fatalf("err = %v, want 非 nil（包含 %q）", err, tc.need)
			}
		})
	}
}

// w14mFirstUniqueConstraintTable 返回契约 spec 中第一个带唯一约束的表。
func w14mFirstUniqueConstraintTable() (string, []string, bool) {
	for table, spec := range contracts.BusinessSQLiteSchema {
		if len(spec.UniqueConstraints) > 0 {
			return table, spec.UniqueConstraints[0], true
		}
	}
	return "", nil, false
}

// w14mIndexMatchesOnRealIndex 对契约库中真实存在的索引执行
// sqliteSchemaIndexMatches，使注入能命中 index_info / sqlite_master 臂。
func w14mIndexMatchesOnRealIndex(db *sql.DB) (bool, string, error) {
	for table, spec := range contracts.BusinessSQLiteSchema {
		for _, index := range spec.Indexes {
			if hasSchemaIndexDefinition(spec.IndexDefinitions, index) {
				continue
			}
			return sqliteSchemaIndexMatches(context.Background(), db, table, contracts.SQLiteIndexDefinition{Name: index, Columns: []string{spec.Columns[0]}})
		}
	}
	return false, "", nil
}

func TestW14MSchemaSQLiteHelperValues(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mFailDB(t, []string{
		"CREATE TABLE w14m_fk_parent (id TEXT PRIMARY KEY)",
		"CREATE TABLE w14m_fk_child (id TEXT, pid TEXT REFERENCES w14m_fk_parent(id) ON DELETE CASCADE ON UPDATE SET NULL)",
		"CREATE TABLE w14m_expr_t (a TEXT, b TEXT)",
		"CREATE UNIQUE INDEX w14m_expr_u ON w14m_expr_t(LOWER(a))",
		"CREATE UNIQUE INDEX w14m_partial_u ON w14m_expr_t(a) WHERE a <> ''",
		"CREATE INDEX w14m_plain_i ON w14m_expr_t(b)",
	})
	defer func() { _ = db.Close() }()
	fp.disarm()

	t.Run("foreignKeySignature", func(t *testing.T) {
		keys, err := sqliteSchemaForeignKeys(ctx, db, "w14m_fk_child")
		if err != nil {
			t.Fatal(err)
		}
		want := businessSQLiteForeignKeySignature("w14m_fk_child", contracts.SQLiteForeignKeySpec{Columns: []string{"pid"}, RefTable: "w14m_fk_parent", RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "SET NULL"})
		if !keys[want] {
			t.Fatalf("外键签名缺失: %v want %s", keys, want)
		}
	})

	t.Run("expressionUniqueSkipped", func(t *testing.T) {
		ok, observed, err := sqliteSchemaHasUniqueConstraint(ctx, db, "w14m_expr_t", []string{"a"})
		if err != nil {
			t.Fatalf("表达式唯一索引应被跳过而不是报错: %v", err)
		}
		if ok {
			t.Fatalf("表达式唯一索引不应命中普通列约束: %v", observed)
		}
		if _, err := sqliteSchemaIndexColumns(ctx, db, "w14m_expr_u"); err == nil || !strings.Contains(err.Error(), "expression") {
			t.Fatalf("err = %v, want 表达式索引分类错误", err)
		}
		okMatch, detail, err := sqliteSchemaIndexMatches(ctx, db, "w14m_expr_t", contracts.SQLiteIndexDefinition{Name: "w14m_expr_u", Unique: true, Columns: []string{"a"}})
		if err != nil || okMatch || !strings.Contains(detail, "expression") {
			t.Fatalf("ok=%t detail=%q err=%v", okMatch, detail, err)
		}
	})

	t.Run("predicateMismatchArms", func(t *testing.T) {
		// 要求无谓词但实际是部分索引 → unexpected partial predicate。
		ok, detail, err := sqliteSchemaIndexMatches(ctx, db, "w14m_expr_t", contracts.SQLiteIndexDefinition{Name: "w14m_partial_u", Unique: true, Columns: []string{"a"}})
		if err != nil || ok || !strings.Contains(detail, "unexpected partial predicate") {
			t.Fatalf("ok=%t detail=%q err=%v", ok, detail, err)
		}
		// 要求谓词但实际索引没有谓词 → predicate=... want=...
		ok, detail, err = sqliteSchemaIndexMatches(ctx, db, "w14m_expr_t", contracts.SQLiteIndexDefinition{Name: "w14m_plain_i", Columns: []string{"b"}, Predicate: "b > 'x'"})
		if err != nil || ok || !strings.Contains(detail, "want=") {
			t.Fatalf("ok=%t detail=%q err=%v", ok, detail, err)
		}
	})

	t.Run("postgresIndexMismatchPure", func(t *testing.T) {
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{expression: true}, contracts.SQLiteIndexDefinition{}); detail != "index contains an expression" {
			t.Fatalf("detail = %q", detail)
		}
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{unique: false}, contracts.SQLiteIndexDefinition{Unique: true}); !strings.Contains(detail, "unique=false want=true") {
			t.Fatalf("detail = %q", detail)
		}
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}}, contracts.SQLiteIndexDefinition{Columns: []string{"b"}}); !strings.Contains(detail, "columns=") {
			t.Fatalf("detail = %q", detail)
		}
		// 要求无谓词但实际带谓词。
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{predicate: "a > 1"}, contracts.SQLiteIndexDefinition{}); !strings.Contains(detail, "want none") {
			t.Fatalf("detail = %q", detail)
		}
		// 双方都有谓词但不等价。
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{predicate: "a > 1"}, contracts.SQLiteIndexDefinition{Predicate: "a > 2"}); !strings.Contains(detail, "predicate=") {
			t.Fatalf("detail = %q", detail)
		}
		if detail := postgresSchemaIndexMismatch(postgresSchemaIndex{predicate: "a > 1"}, contracts.SQLiteIndexDefinition{Predicate: "a > 1"}); detail != "" {
			t.Fatalf("等价谓词应通过: %q", detail)
		}
	})
}

func TestW14MSchemaSQLiteObjectsQueryError(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mCompliantFailSchemaDB(t)
	defer func() { _ = db.Close() }()
	fp.arm("FROM sqlite_master")
	defer fp.disarm()
	if _, err := sqliteSchemaObjects(ctx, db, "table"); err == nil || !strings.Contains(err.Error(), "w14m") {
		t.Fatalf("err = %v", err)
	}
}

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
