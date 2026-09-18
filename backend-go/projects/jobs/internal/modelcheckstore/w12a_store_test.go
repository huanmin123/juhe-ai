package modelcheckstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// w12a_store_test.go 覆盖 Store 构造校验、写入与投影错误分支、ListRuns/GetRun
// 查询过滤器与分页，以及 CheckSchema 的 PostgreSQL 臂（w1cover 门禁）。

func w12aStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func w12aRunInput(id string, now time.Time) RunInput {
	return RunInput{
		ID: id, SystemAccountID: "w12a-sys", ActorSystemAccountID: "w12a-actor",
		ProviderCode: "gpt", TargetType: "account", TargetID: "w12a-acct",
		Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual,
		ProbeSetVersion: "w12a-v1", StartedAt: now,
	}
}

func w12aItemInput(runID string) ItemInput {
	return ItemInput{ID: runID + "-item", RunID: runID, ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10}
}

// ---- 构造与 Schema ----

func TestW12aStoreConstructorValidation(t *testing.T) {
	if _, err := OpenSQLite("   "); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("空路径应报错: %v", err)
	}
	if _, err := OpenPostgres("   ", 10, 5); err == nil || !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("空 URL 应报错: %v", err)
	}
	if _, err := OpenPostgres("postgres://w12a.invalid/db", 5, 20); err == nil || !strings.Contains(err.Error(), "连接池配置无效") {
		t.Fatalf("maxIdle>maxOpen 应报错: %v", err)
	}
	if _, err := NewStoreWithMode(nil, StoreSQLite); err == nil {
		t.Fatalf("nil db 应报错")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStoreWithMode(db, StoreMode("w12a-invalid")); err == nil {
		t.Fatalf("非法 mode 应报错")
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close 应幂等")
	}
	ctx := context.Background()
	if err := nilStore.EnsureSchema(ctx); err == nil {
		t.Fatalf("nil store EnsureSchema 应报错")
	}
	if err := nilStore.CheckSchema(ctx); err == nil {
		t.Fatalf("nil store CheckSchema 应报错")
	}
	// SQLite 文件模式 + EnsureSchema 幂等重建。
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "w12a-dataset.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("文件模式 EnsureSchema 不应报错: %v", err)
	}
}

func TestW12aCheckSchemaFailsOnMissingTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "verify model check schema") {
		t.Fatalf("缺表应报 schema 校验错误: %v", err)
	}
}

func TestW12aValidateInputsTableDriven(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Now().UTC()
	// RunInput 校验：全空输入必命中必填分支；逐项补充触发后续分支。
	if err := store.CreateRun(ctx, RunInput{}); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("空 run 输入应报必填: %v", err)
	}
	badTrigger := w12aRunInput("w12a-run", now)
	badTrigger.Trigger = "other"
	if err := store.CreateRun(ctx, badTrigger); err == nil || !strings.Contains(err.Error(), "trigger is invalid") {
		t.Fatalf("非法 trigger 应报错: %v", err)
	}
	badProfile := w12aRunInput("w12a-run", now)
	badProfile.Profile = "deep"
	if err := store.CreateRun(ctx, badProfile); err == nil || !strings.Contains(err.Error(), "profile is invalid") {
		t.Fatalf("非法 profile 应报错: %v", err)
	}
	badStart := w12aRunInput("w12a-run", now)
	badStart.StartedAt = time.Time{}
	if err := store.CreateRun(ctx, badStart); err == nil || !strings.Contains(err.Error(), "startedAt is required") {
		t.Fatalf("零 startedAt 应报错: %v", err)
	}
	// ItemInput 校验。
	if err := store.AppendItem(ctx, ItemInput{}); err == nil || !strings.Contains(err.Error(), "item input is invalid") {
		t.Fatalf("空 item 输入应报错: %v", err)
	}
	badScore := w12aItemInput("w12a-run")
	badScore.Score = 11
	badScore.MaxScore = 10
	if err := store.AppendItem(ctx, badScore); err == nil || !strings.Contains(err.Error(), "item input is invalid") {
		t.Fatalf("score>maxScore 应报错: %v", err)
	}
	badStatus := w12aItemInput("w12a-run")
	badStatus.Status = "other"
	if err := store.AppendItem(ctx, badStatus); err == nil || !strings.Contains(err.Error(), "status is invalid") {
		t.Fatalf("非法 item status 应报错: %v", err)
	}
	// ObservationInput 校验。
	if err := store.AppendObservation(ctx, ObservationInput{}); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("空 observation 输入应报必填: %v", err)
	}
	badCoverage := ObservationInput{ID: "obs", RunID: "run", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "gpt", ProviderProtocolProfileID: "p", EndpointFamily: "responses", RequestedModel: "m", MappedUpstreamModel: "m", UpstreamBucketHMAC: "u", CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k", ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now, EvidenceCoverage: 101}
	if err := store.AppendObservation(ctx, badCoverage); err == nil || !strings.Contains(err.Error(), "observation input is invalid") {
		t.Fatalf("coverage>100 应报错: %v", err)
	}
	// OutcomeProjection 校验。
	if err := store.ProjectOutcome(ctx, OutcomeProjection{}); err == nil || !strings.Contains(err.Error(), "projection input is invalid") {
		t.Fatalf("空投影应报错: %v", err)
	}
}

// ---- Append / Finish / Project 错误分支 ----

func TestW12aAppendBranchesOnNonexistentAndTerminalRuns(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Now().UTC()
	if _, err := store.beginRunningRunTx(ctx, "w12a-ghost"); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("缺失 run 应报错: %v", err)
	}
	if err := store.AppendItem(ctx, w12aItemInput("w12a-ghost")); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("缺失 run 应拒绝 item: %v", err)
	}
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "w12a-run", RunCompleted, "likely", 10, 10, "ok", now.Add(time.Second), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendObservation(ctx, ObservationInput{ID: "obs-late", RunID: "w12a-run", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "gpt", ProviderProtocolProfileID: "p", EndpointFamily: "responses", RequestedModel: "m", MappedUpstreamModel: "m", UpstreamBucketHMAC: "u", CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k", ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("终态 run 应拒绝 observation: %v", err)
	}
	// FinishRun 校验分支。
	if err := store.FinishRun(ctx, "", RunCompleted, "likely", 0, 10, "m", now, nil, nil); err == nil {
		t.Fatalf("空 runID 应报错")
	}
	if err := store.FinishRun(ctx, "w12a-run", RunCompleted, "likely", 11, 10, "m", now, nil, nil); err == nil {
		t.Fatalf("score>maxScore 应报错")
	}
	// FinishRun：run 不存在。
	if err := store.FinishRun(ctx, "w12a-ghost", RunCompleted, "likely", 0, 10, "m", now, nil, nil); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("缺失 run 应报错: %v", err)
	}
	// ProjectOutcome：run 不存在。
	projection := OutcomeProjection{
		RunID: "w12a-ghost", Status: RunCompleted, Level: "likely", Score: 10, MaxScore: 10,
		Message: "ok", FinishedAt: now.Add(time.Second),
		Items: []ItemInput{w12aItemInput("w12a-ghost")},
	}
	if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("缺失 run 的投影应报错: %v", err)
	}
}

func TestW12aVerifyTerminalProjectionConflicts(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	finishedAt := now.Add(time.Second)
	run := w12aRunInput("w12a-run", now)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	items := []ItemInput{w12aItemInput("w12a-run")}
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 10, MaxScore: 10,
		Message: "ok", FinishedAt: finishedAt, Items: items,
		ResultSummary: json.RawMessage(`{"score":10}`), QualityDecision: json.RawMessage(`{}`),
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatal(err)
	}
	// 完全一致的重放成功。
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("一致重放应成功: %v", err)
	}
	// 级别漂移 → conflict。
	drifted := projection
	drifted.Level = "uncertain"
	if err := store.ProjectOutcome(ctx, drifted); err == nil || !strings.Contains(err.Error(), "conflicts with terminal run") {
		t.Fatalf("级别漂移应 conflict: %v", err)
	}
	// item 集缺失 → conflict（期望集合非空而库内一致，但缺失 ID 无法匹配）。
	missing := projection
	missing.Items = []ItemInput{{ID: "w12a-run-item-x", RunID: "w12a-run", ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10}}
	if err := store.ProjectOutcome(ctx, missing); err == nil || !strings.Contains(err.Error(), "conflicts with terminal run") {
		t.Fatalf("item 集漂移应 conflict: %v", err)
	}
	// 投影校验：重复 item ID。
	dup := projection
	dup.Items = []ItemInput{w12aItemInput("w12a-run"), w12aItemInput("w12a-run")}
	if err := store.ProjectOutcome(ctx, dup); err == nil || !strings.Contains(err.Error(), "duplicate item ID") {
		t.Fatalf("重复 item ID 应报错: %v", err)
	}
}

func TestW12aUpdateQualityDecisionValidation(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "w12a-run", RunCompleted, "likely", 10, 10, "ok", now.Add(time.Second), json.RawMessage(`{"r":1}`), nil); err != nil {
		t.Fatal(err)
	}
	// 非法输入组合。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: " ", Status: RunCompleted, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil {
		t.Fatalf("空 runID 应报错")
	}
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunRunning, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil {
		t.Fatalf("running 状态应报错")
	}
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`broken`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	// run 不存在。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-ghost", Status: RunCompleted, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("缺失 run 应报错: %v", err)
	}
	// status 与 result summary 漂移 → conflict。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunFailed, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("状态漂移应 conflict: %v", err)
	}
	// 空 decision 可被写入一次。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{"r":1}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{"decision":true}`)}); err != nil {
		t.Fatalf("首写 decision 不应报错: %v", err)
	}
	// 相同重放成功（幂等）。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{"r":1}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{"decision":true}`)}); err != nil {
		t.Fatalf("相同重放应成功: %v", err)
	}
	// 已有不同 decision → conflict。
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{"r":1}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{"decision":false}`)}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("不同 decision 应 conflict: %v", err)
	}
}

func TestW12aFinishRunDurationClamp(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	// finishedAt 早于 startedAt：duration 夹为 0。
	if err := store.FinishRun(ctx, "w12a-run", RunFailed, "suspicious", 5, 10, "early", now.Add(-time.Minute), json.RawMessage(`{}`), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	var duration int64
	if err := store.db.QueryRow(`SELECT duration_ms FROM model_check_runs WHERE id='w12a-run'`).Scan(&duration); err != nil || duration != 0 {
		t.Fatalf("duration 应夹为 0: %d %v", duration, err)
	}
}

// ---- ListRuns / GetRun ----

func w12aSeedRuns(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"w12a-run-a", "w12a-run-b", "w12a-run-c"} {
		input := w12aRunInput(id, now.Add(time.Duration(index)*time.Second))
		input.SystemAccountID = "w12a-sys"
		if index == 2 {
			input.TargetType = "group"
			input.TargetID = "w12a-group"
			input.Trigger = TriggerScheduled
			input.Model = "gpt-5.6-min"
		}
		if err := store.CreateRun(ctx, input); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendItem(ctx, ItemInput{ID: id + "-item", RunID: id, ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10, TraceID: "w12a-trace", EvidenceSummary: json.RawMessage(`{"k":"v"}`)}); err != nil {
			t.Fatal(err)
		}
		if index < 2 {
			if err := store.FinishRun(ctx, id, RunCompleted, "likely", 10, 10, "ok", now.Add(time.Duration(index)*time.Second+500*time.Millisecond), json.RawMessage(`{"r":1}`), json.RawMessage(`{"d":1}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestW12aListRunsFiltersAndPagination(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	w12aSeedRuns(t, store)

	var nilStore *Store
	if _, err := nilStore.ListRuns(ctx, RunListOptions{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := store.ListRuns(ctx, RunListOptions{PageSize: 101}); err == nil || !strings.Contains(err.Error(), "page size is invalid") {
		t.Fatalf("pageSize>100 应报错: %v", err)
	}
	// 全过滤器组合。
	result, err := store.ListRuns(ctx, RunListOptions{
		SystemAccountID: "w12a-sys", TargetType: "account", TargetID: "w12a-acct",
		Model: "gpt-5.6-sol", Level: "likely", Status: "completed", TriggerKind: "manual",
		StartAt: "2026-09-17T00:00:00Z", EndAt: "2026-09-18T00:00:00Z", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Total != 2 || result.HasMore {
		t.Fatalf("过滤器结果不符: total=%d items=%d hasMore=%v", result.Total, len(result.Items), result.HasMore)
	}
	if result.Items[0].ID != "w12a-run-b" || result.Items[0].DurationMS == nil || result.Items[0].DurationMS != nil && *result.Items[0].DurationMS != 500 {
		t.Fatalf("排序与 duration 不符: %#v", result.Items[0])
	}
	// 非法枚举值不会进入 WHERE（过滤被忽略），但仍返回数据。
	ignored, err := store.ListRuns(ctx, RunListOptions{Model: "bad model", Level: "nope", Status: "nope", TriggerKind: "nope", Page: 1, PageSize: 20})
	if err != nil || len(ignored.Items) != 3 {
		t.Fatalf("非法枚举应被忽略: %v %d", err, len(ignored.Items))
	}
	// hasMore：pageSize=2 时取 3 行中的 2 行并给出上界。
	paged, err := store.ListRuns(ctx, RunListOptions{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !paged.HasMore || len(paged.Items) != 2 || paged.Total != 3 {
		t.Fatalf("hasMore 分页不符: %#v", paged)
	}
	// 第二页。
	page2, err := store.ListRuns(ctx, RunListOptions{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page2.HasMore || len(page2.Items) != 1 || page2.Total != 3 {
		t.Fatalf("第二页不符: %#v", page2)
	}
}

func TestW12aGetRunDetailAndErrors(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	w12aSeedRuns(t, store)

	var nilStore *Store
	if _, _, err := nilStore.GetRun(ctx, "w12a-run-a", ""); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, _, err := store.GetRun(ctx, "  ", ""); err == nil {
		t.Fatalf("空 runID 应报错")
	}
	if _, found, err := store.GetRun(ctx, "w12a-ghost", ""); err != nil || found {
		t.Fatalf("缺失 run 应 found=false: %v", err)
	}
	// systemAccountID 过滤不匹配 → not found。
	if _, found, err := store.GetRun(ctx, "w12a-run-a", "w12a-other"); err != nil || found {
		t.Fatalf("作用域不匹配应 not found: %v", err)
	}
	detail, found, err := store.GetRun(ctx, "w12a-run-a", "w12a-sys")
	if err != nil || !found {
		t.Fatalf("应查到 run: %v %v", found, err)
	}
	if detail.ID != "w12a-run-a" || detail.Status != string(RunCompleted) || detail.DurationMS == nil {
		t.Fatalf("detail 不符: %#v", detail.RunListItem)
	}
	if detail.QualityDecision == nil || detail.QualityDecision["d"] == nil {
		t.Fatalf("quality decision 应解码: %#v", detail.QualityDecision)
	}
	if len(detail.Checks) != 1 || detail.Checks[0].TraceID != "w12a-trace" || detail.Checks[0].DurationMS != nil {
		t.Fatalf("checks 不符: %#v", detail.Checks)
	}
	// JSON 损坏 → 解码错误。
	if _, err := store.db.Exec(`UPDATE model_check_runs SET request_summary_json='broken' WHERE id='w12a-run-a'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(ctx, "w12a-run-a", ""); err == nil || !strings.Contains(err.Error(), "decode model check JSON") {
		t.Fatalf("JSON 损坏应报错: %v", err)
	}
	// item evidence 损坏 → 解码错误。
	if _, err := store.db.Exec(`UPDATE model_check_runs SET request_summary_json='{}' WHERE id='w12a-run-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_items SET evidence_summary_json='[1]' WHERE run_id='w12a-run-a'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(ctx, "w12a-run-a", ""); err == nil || !strings.Contains(err.Error(), "decode model check JSON") {
		t.Fatalf("item JSON 损坏应报错: %v", err)
	}
}

func TestW12aReadTimestampRejectsUnexpectedType(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.readTimestamp(12345); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("意外时间戳类型应报错: %v", err)
	}
	if _, err := store.readTimestamp([]byte("2026-09-17T00:00:00Z")); err != nil {
		t.Fatalf("字节切片应支持: %v", err)
	}
}

// ---- CheckSchema PostgreSQL 臂（w1cover 门禁）----

func w12aW1CoverDSN(t *testing.T) string {
	t.Helper()
	raw, err := osReadFileSharedEnv()
	if err != nil {
		t.Skip("w12a PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w12a PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func osReadFileSharedEnv() (string, error) {
	raw, err := osReadWholeFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	return string(raw), err
}

func osReadWholeFile(path string) ([]byte, error) {
	return osRead(path)
}

func osRead(path string) ([]byte, error) {
	return osReadFileImpl(path)
}

func osReadFileImpl(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// w12aDatasetSelfHealDDL 按权威列集在 juhe_dataset 幂等补建 dataset 三表与
// 索引（与 modelcheckapp/w12a_host_test.go 的 fixture 同源）；共享覆盖库被
// 外部重建后测试可自愈，不依赖他人留下的表。
var w12aDatasetSelfHealDDL = []string{
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'quick', trigger_kind TEXT NOT NULL DEFAULT 'manual', schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, level TEXT NOT NULL DEFAULT 'unavailable', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 100, status TEXT NOT NULL DEFAULT 'running', message TEXT NOT NULL DEFAULT '', trace_id TEXT, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', started_at TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT NOT NULL DEFAULT '{}', result_summary_json TEXT NOT NULL DEFAULT '{}', policy_snapshot_json TEXT NOT NULL DEFAULT '{}', quality_decision_json TEXT NOT NULL DEFAULT '{}', quality_health_sync_status TEXT, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL DEFAULT '{}', error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT NOT NULL DEFAULT 'unknown', endpoint_family TEXT NOT NULL DEFAULT 'unknown', requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, observed_model TEXT, mapping_applied INTEGER NOT NULL DEFAULT 0, upstream_bucket_hmac TEXT NOT NULL DEFAULT '', cohort_key_hmac TEXT NOT NULL DEFAULT '', population_key_hmac TEXT NOT NULL DEFAULT '', probe_key_hmac TEXT NOT NULL DEFAULT '', system_fingerprint_hmac TEXT, probe_family TEXT NOT NULL, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', tokenizer_version TEXT NOT NULL DEFAULT 'unavailable', feature_version TEXT NOT NULL DEFAULT 'none', round_index INTEGER NOT NULL DEFAULT 0, padding_tokens INTEGER NOT NULL DEFAULT 0, local_input_tokens INTEGER NOT NULL DEFAULT 0, reported_input_tokens INTEGER, cached_input_tokens INTEGER, constraint_passed INTEGER, feature_1 DOUBLE PRECISION, feature_2 DOUBLE PRECISION, feature_3 DOUBLE PRECISION, feature_4 DOUBLE PRECISION, feature_5 DOUBLE PRECISION, feature_6 DOUBLE PRECISION, feature_7 DOUBLE PRECISION, feature_8 DOUBLE PRECISION, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL DEFAULT 0, trace_id TEXT, created_at TEXT NOT NULL, aggregation_completed_at TEXT)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON juhe_dataset.model_check_runs(created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_created ON juhe_dataset.model_check_runs(system_account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_actor_created ON juhe_dataset.model_check_runs(actor_system_account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_model_created ON juhe_dataset.model_check_runs(model,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_level_created ON juhe_dataset.model_check_runs(level,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_status_created ON juhe_dataset.model_check_runs(status,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_target_created ON juhe_dataset.model_check_runs(target_type,target_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_account_created ON juhe_dataset.model_check_runs(account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_trigger_created ON juhe_dataset.model_check_runs(trigger_kind,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry ON juhe_dataset.model_check_runs(quality_health_sync_status,updated_at,id) WHERE quality_health_sync_status='failed'`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_model_created ON juhe_dataset.model_check_runs(system_account_id,model,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_level_created ON juhe_dataset.model_check_runs(system_account_id,level,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_status_created ON juhe_dataset.model_check_runs(system_account_id,status,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_target_created ON juhe_dataset.model_check_runs(system_account_id,target_type,target_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON juhe_dataset.model_check_items(run_id,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON juhe_dataset.model_check_items(run_id,item_key,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_status ON juhe_dataset.model_check_items(run_id,status,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_cursor ON juhe_dataset.model_check_observations(created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation ON juhe_dataset.model_check_observations(created_at,id) WHERE aggregation_completed_at IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_account_model ON juhe_dataset.model_check_observations(system_account_id,account_id,requested_model,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_cohort ON juhe_dataset.model_check_observations(cohort_key_hmac,mapped_upstream_model,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_population ON juhe_dataset.model_check_observations(population_key_hmac,requested_model,probe_family,created_at,id)`,
}

func TestW12aCheckSchemaPostgresArmOnW1Cover(t *testing.T) {
	// 自愈：共享覆盖库被外部重建后，这里幂等补齐 juhe_dataset 三表与索引，
	// 再验证 PostgreSQL 方言臂的表/索引校验全通过。
	store, err := OpenPostgres(w12aW1CoverDSN(t), 4, 2)
	if err != nil {
		t.Skipf("w12a PG gated: 打开失败: %v", err)
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.db.PingContext(pingCtx); err != nil {
		t.Skip("w12a PG gated: PG 不可达")
	}
	for _, ddl := range w12aDatasetSelfHealDDL {
		if _, err := store.db.Exec(ddl); err != nil {
			t.Fatalf("自愈建表失败: %v", err)
		}
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("覆盖库 schema 应满足契约: %v", err)
	}
	// EnsureSchema 在 Postgres 模式下等价于 CheckSchema。
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema 应透传 CheckSchema: %v", err)
	}
}
