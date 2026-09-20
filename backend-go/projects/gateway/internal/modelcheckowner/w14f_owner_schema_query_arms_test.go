package modelcheckowner

// w14f_owner_schema_query_arms_test.go：business_schema 检查辅助函数的失败
// 注入臂、SQLite 部分契约库的失败关闭臂、PostgreSQL 方言纯函数臂，以及
// query/Runtime 的读取与校验臂。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// ---- business_schema：SQLite 检查失败关闭臂 ----

func TestW14fOwnerSQLiteSchemaFailClosedArms(t *testing.T) {
	ctx := context.Background()
	// nil 库。
	if err := CheckBusinessSQLiteSchema(ctx, nil); err == nil {
		t.Fatalf("nil 库应报错")
	}
	// 空库 → 缺表。
	empty := wbOpenMemoryDB(t, nil)
	if err := CheckBusinessSQLiteSchema(ctx, empty); err == nil ||
		!strings.Contains(err.Error(), "missing table") {
		t.Fatalf("空库应报缺表: %v", err)
	}
	// 每个必需表建一个占位列 → 缺列。
	statements := []string{}
	for table := range contracts.BusinessSQLiteSchema {
		statements = append(statements, "CREATE TABLE "+table+" (placeholder TEXT)")
	}
	partial := wbOpenMemoryDB(t, statements)
	if err := CheckBusinessSQLiteSchema(ctx, partial); err == nil {
		t.Fatalf("占位列库应报错")
	}
}

func TestW14fOwnerSQLiteSchemaHelperErrorArms(t *testing.T) {
	ctx := context.Background()
	store, fp := w13g2McFailStore(t)
	db := store.db

	// sqliteSchemaObjects / Columns / PrimaryKey / Unique / IndexColumns /
	// ForeignKeys / IndexMatches 的查询错误臂。
	arms := []struct {
		name    string
		pattern string
		call    func() error
	}{
		{"objects", "FROM sqlite_master", func() error {
			_, err := sqliteSchemaObjects(ctx, db, "table")
			return err
		}},
		{"columns", "PRAGMA table_info", func() error {
			_, err := sqliteSchemaColumns(ctx, db, "model_check_runs")
			return err
		}},
		{"primaryKey", "PRAGMA table_info", func() error {
			_, err := sqliteSchemaPrimaryKey(ctx, db, "model_check_runs")
			return err
		}},
		{"uniqueConstraint", "PRAGMA index_list", func() error {
			_, _, err := sqliteSchemaHasUniqueConstraint(ctx, db, "model_check_runs", []string{"id"})
			return err
		}},
		{"indexColumns", "PRAGMA index_info", func() error {
			_, err := sqliteSchemaIndexColumns(ctx, db, "idx")
			return err
		}},
		{"foreignKeys", "PRAGMA foreign_key_list", func() error {
			_, err := sqliteSchemaForeignKeys(ctx, db, "model_check_runs")
			return err
		}},
		{"indexMatches", "PRAGMA index_list", func() error {
			_, _, err := sqliteSchemaIndexMatches(ctx, db, "model_check_runs", contracts.SQLiteIndexDefinition{Name: "idx"})
			return err
		}},
	}
	for _, item := range arms {
		item := item
		t.Run(item.name, func(t *testing.T) {
			fp.arm(item.pattern)
			defer fp.disarm()
			if err := item.call(); err == nil {
				t.Fatalf("%s 失败注入应报错", item.name)
			}
		})
	}
}

// ---- business_schema：PostgreSQL 方言纯函数臂 ----

func TestW14fOwnerPostgresSchemaHelperArms(t *testing.T) {
	// 参照动作映射（SQLite 缩写 → PG 动作）。
	if postgresReferentialAction("C") != "CASCADE" || postgresReferentialAction("A") != "NO ACTION" ||
		postgresReferentialAction("R") != "RESTRICT" || postgresReferentialAction("N") != "SET NULL" ||
		postgresReferentialAction("D") != "SET DEFAULT" || postgresReferentialAction("x") != "" {
		t.Fatalf("参照动作映射不符")
	}
	// 约束匹配：命中 / 类别不符 / 列序不符。
	constraints := map[int64]*postgresSchemaConstraint{
		1: {kind: "p", columns: []string{"id"}},
		2: {kind: "u", columns: []string{"a", "b"}},
	}
	if !hasPostgresConstraint(constraints, "p", []string{"id"}) {
		t.Fatalf("主键约束应命中")
	}
	if hasPostgresConstraint(constraints, "u", []string{"a"}) {
		t.Fatalf("列不全不应命中")
	}
	foreignKeys := map[int64]*postgresSchemaForeignKey{
		1: {refSchema: "juhe_business", refTable: "accounts", columns: []string{"account_id"}, refColumns: []string{"id"}, onUpdate: "CASCADE", onDelete: "NO ACTION"},
	}
	spec := contracts.SQLiteForeignKeySpec{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnUpdate: "CASCADE", OnDelete: "NO ACTION"}
	if !hasPostgresForeignKey(foreignKeys, "juhe_business", spec) {
		t.Fatalf("外键应命中")
	}
	wrong := spec
	wrong.OnDelete = "SET NULL"
	if hasPostgresForeignKey(foreignKeys, "juhe_business", wrong) {
		t.Fatalf("删除行为不同不应命中")
	}
	// 索引不匹配描述分支。
	mismatch := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}}, contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a", "b"}})
	if mismatch == "" {
		t.Fatalf("列序差异应有描述")
	}
	if code := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}, predicate: "WHERE x"}, contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a"}, Predicate: "WHERE y"}); code == "" {
		t.Fatalf("谓词差异应有描述")
	}
	if code := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}, expression: true}, contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a"}}); code == "" {
		t.Fatalf("表达式索引应有描述")
	}
	if code := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}}, contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a"}, Unique: true}); code == "" {
		t.Fatalf("unique 差异应有描述")
	}
	if code := postgresSchemaIndexMismatch(postgresSchemaIndex{columns: []string{"a"}}, contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a"}, Predicate: "x"}); code == "" {
		t.Fatalf("缺失谓词应有描述")
	}
	// sameSchemaIndexColumns。
	if !sameSchemaIndexColumns([]string{"a", "b"}, []string{"a", "b"}) || sameSchemaIndexColumns([]string{"a"}, nil) {
		t.Fatalf("索引列比较不符")
	}
	// 谓词等价比较。
	if !schemaIndexPredicatesEquivalent("(A AND B)", "a  and  b") {
		t.Fatalf("谓词等价比较失败")
	}
}

// ---- query / Runtime 读取与校验臂 ----

func w14fOwnerBaseRun(t *testing.T, store *Store) RunRecord {
	t.Helper()
	return RunRecord{ID: "w14f-run-query", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai",
		TargetType: "account", TargetID: "acct", AccountID: "acct", Model: "gpt-5.6-sol", Profile: "full",
		TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
		RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
}

func TestW14fOwnerGetRunValidationArms(t *testing.T) {
	ctx := context.Background()
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	runtime := &Runtime{Store: store}
	run := w14fOwnerBaseRun(t, store)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	item := ItemRecord{ID: "w14f-run-query-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability",
		Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	if err := store.AppendItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	// 汇总列损坏 → requiredJSONObject 各臂。
	for _, column := range []string{"request_summary_json", "result_summary_json", "policy_snapshot_json", "quality_decision_json"} {
		if _, err := store.db.Exec("UPDATE model_check_runs SET "+column+"='not-json' WHERE id=?", run.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runtime.GetRun(ctx, run.ID); err == nil {
			t.Fatalf("%s 损坏应报错", column)
		}
		if _, err := store.db.Exec("UPDATE model_check_runs SET "+column+"='{}' WHERE id=?", run.ID); err != nil {
			t.Fatal(err)
		}
	}
	// 读取成功后 checks 校验：证据列损坏。
	if _, err := store.db.Exec(`UPDATE model_check_items SET evidence_summary_json='not-json' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.GetRun(ctx, run.ID); err == nil {
		t.Fatalf("证据损坏应报错")
	}
	// 直驱辅助函数。
	if _, err := requiredJSONObject("w14f", "not-json"); err == nil {
		t.Fatalf("requiredJSONObject 非法输入应报错")
	}
	trust := map[string]json.RawMessage{"reasonCodes": json.RawMessage(`"not-an-array"`)}
	if hasTrustReason(trust, "x") {
		t.Fatalf("非法 reasonCodes 不应命中")
	}
	if hasTrustReason(map[string]json.RawMessage{"reasonCodes": json.RawMessage(`["a","b"]`)}, "c") {
		t.Fatalf("未含目标 reason 不应命中")
	}
	if parseTrustReasonCodes("not-json") != nil {
		t.Fatalf("非法 reason codes 应返回 nil")
	}
	if encoded := string(mustMarshalJSONRaw(make(chan int))); encoded != `{}` {
		t.Fatalf("marshal 失败应回落空对象: %s", encoded)
	}
	// mergeLatestTrustReport 分支。
	merge := func(summary string, level string, accountID string) *RunDetail {
		detail := &RunDetail{ResultSummary: json.RawMessage(summary)}
		detail.Level = level
		if accountID != "" {
			id := accountID
			detail.AccountID = &id
		}
		runtime.mergeLatestTrustReport(ctx, detail)
		return detail
	}
	merge(`not-json`, "failed", "acct")
	merge(`{"modelCheckUnverified":true}`, "failed", "acct")
	merge(`{"trustReport":"not-json"}`, "failed", "acct")
	merge(`{"trustReport":{"reasonCodes":["model_response_evidence_unavailable"]}}`, "failed", "acct")
	merge(`{}`, "unavailable", "acct")
	if detail := merge(`{}`, "failed", "acct"); detail == nil {
		t.Fatalf("正常路径应返回")
	}
}

// ---- run.go 剩余错误臂 ----

func TestW14fOwnerRunStoreFailpointArms(t *testing.T) {
	ctx := context.Background()
	s, fp := w13g2McFailStore(t)
	run := w14fOwnerBaseRun(t, s)

	t.Run("createRun", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_runs (")
		defer fp.disarm()
		if err := s.CreateRun(ctx, run); err == nil {
			t.Fatalf("createRun 注入应失败")
		}
	})
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	item := ItemRecord{ID: "w14f-run-query-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability",
		Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	t.Run("appendItem", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_items")
		defer fp.disarm()
		if err := s.AppendItem(ctx, item); err == nil {
			t.Fatalf("appendItem 注入应失败")
		}
	})
	observation := ObservationRecord{ID: "w14f-obs-1", RunID: run.ID, SystemAccountID: "sys", AccountID: "acct",
		ProviderCode: "openai", RequestedModel: "gpt-5.6-sol", MappedUpstreamModel: "gpt-5.6-sol",
		ProbeFamily: "identity", ObservationStatus: "ok", IdentityStatus: "matched", MappingStatus: "identity",
		ProtocolStatus: "ok", EvidenceCoverage: 100, CreatedAt: run.StartedAt}
	t.Run("appendObservation", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_observations")
		defer fp.disarm()
		if err := s.AppendObservation(ctx, observation); err == nil {
			t.Fatalf("appendObservation 注入应失败")
		}
	})
	if err := s.AppendItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	projection := OutcomeProjection{RunID: run.ID, Status: RunCompleted, Level: "likely", Score: 90, MaxScore: 100,
		FinishedAt: run.StartedAt.Add(time.Minute), Items: []ItemRecord{item},
		ResultSummary: json.RawMessage(`{}`), QualityDecision: json.RawMessage(`{}`)}
	t.Run("projectOutcomeRunRead", func(t *testing.T) {
		fp.arm("SELECT status,level,score")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, projection); err == nil {
			t.Fatalf("projectOutcome 注入应失败")
		}
	})
	t.Run("projectOutcomeItemInsert", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_items")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, projection); err == nil {
			t.Fatalf("projectOutcome 条目写入注入应失败")
		}
	})
	t.Run("projectOutcomeUpdate", func(t *testing.T) {
		fp.arm("SET level=?,score=?")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, projection); err == nil {
			t.Fatalf("projectOutcome 更新注入应失败")
		}
	})
	t.Run("terminalItemsQuery", func(t *testing.T) {
		fp.arm("FROM model_check_items WHERE run_id=?")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, projection); err == nil {
			t.Fatalf("terminalItems 注入应失败")
		}
	})
	t.Run("terminalItemsScan", func(t *testing.T) {
		fp.armScan("FROM model_check_items WHERE run_id=?")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, projection); err == nil {
			t.Fatalf("terminalItems 扫描注入应失败")
		}
	})
	// 正常投影（含 error_code/error_message 的条目 → Valid 分支）。
	withErrors := item
	withErrors.ErrorCode = "http_429"
	withErrors.ErrorMessage = "上游 429"
	withErrors.ID = "w14f-run-query-item-0002"
	projection.Items = []ItemRecord{withErrors}
	if err := s.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("正常投影不应失败: %v", err)
	}
}
