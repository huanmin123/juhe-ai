package modelcheckstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// w12a_store_more_test.go 用取消上下文与宽松自建表驱动 schema 漂移分支，
// 覆盖 ListRuns/GetRun/写入链在缺列、NULL 与 canceled ctx 下的 fail-closed 行为。

func w12aCtxCanceled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}

func TestW12aCanceledContextErrors(t *testing.T) {
	ctx := w12aCtxCanceled(t)
	store := w12aStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "initialize model check schema") {
		t.Fatalf("取消上下文应使 EnsureSchema 失败: %v", err)
	}
	if _, err := store.ListRuns(ctx, RunListOptions{Page: 1, PageSize: 10}); err == nil || !strings.Contains(err.Error(), "list model check runs") {
		t.Fatalf("取消上下文应使 ListRuns 失败: %v", err)
	}
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err == nil || !strings.Contains(err.Error(), "create model check run") {
		t.Fatalf("取消上下文应使 CreateRun 失败: %v", err)
	}
	if err := store.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, w12aItemInput("w12a-run")); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("取消上下文应使 AppendItem 失败: %v", err)
	}
	if err := store.AppendObservation(ctx, ObservationInput{ID: "obs", RunID: "w12a-run", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "gpt", ProviderProtocolProfileID: "p", EndpointFamily: "responses", RequestedModel: "m", MappedUpstreamModel: "m", UpstreamBucketHMAC: "u", CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k", ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now}); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("取消上下文应使 AppendObservation 失败: %v", err)
	}
	if _, err := store.beginRunningRunTx(ctx, "w12a-run"); err == nil || !strings.Contains(err.Error(), "begin model check run append") {
		t.Fatalf("取消上下文应使 append 事务失败: %v", err)
	}
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 1, MaxScore: 10,
		Message: "ok", FinishedAt: now.Add(time.Second),
		Items: []ItemInput{w12aItemInput("w12a-run")},
	}
	if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "begin model check outcome projection") {
		t.Fatalf("取消上下文应使投影失败: %v", err)
	}
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "begin quality decision projection") {
		t.Fatalf("取消上下文应使质量决策失败: %v", err)
	}
	if err := store.FinishRun(ctx, "w12a-run", RunCompleted, "likely", 1, 10, "m", now, nil, nil); err == nil || !strings.Contains(err.Error(), "begin finish model check run") {
		t.Fatalf("取消上下文应使 FinishRun 失败: %v", err)
	}
	// sqlite 方言的 checkIndexes PRAGMA 查询失败分支。
	if err := store.CheckSchema(ctx); err == nil {
		t.Fatalf("取消上下文应使索引校验失败")
	}
}

func TestW12aCanceledContextPostgresIndexArm(t *testing.T) {
	// PG 方言臂的 pg_indexes 查询失败分支：门禁库可用，但查询上下文已取消。
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
	if err := store.checkIndexes(w12aCtxCanceled(t)); err == nil || !strings.Contains(err.Error(), "verify model check indexes") {
		t.Fatalf("取消上下文应使 PG 索引校验失败: %v", err)
	}
}

// w12aLooseFixture 建最小宽松表（全部可空、无 CHECK），驱动读取链的
// 缺列与 NULL 分支。
func w12aLooseFixture(t *testing.T, withStatusColumn bool, itemColumns string) (*Store, *sql.DB) {
	t.Helper()
	runsColumns := "id TEXT PRIMARY KEY, system_account_id TEXT, actor_system_account_id TEXT, provider_code TEXT, target_type TEXT, target_id TEXT, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT, profile TEXT, trigger_kind TEXT, schedule_id TEXT, trusted_comparison_enabled INTEGER, trusted_comparison_available INTEGER, level TEXT, score INTEGER, max_score INTEGER, status TEXT, message TEXT, trace_id TEXT, probe_set_version TEXT, started_at TEXT, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT, result_summary_json TEXT, policy_snapshot_json TEXT, quality_decision_json TEXT, error_message TEXT, created_at TEXT, updated_at TEXT"
	if !withStatusColumn {
		runsColumns = strings.Replace(runsColumns, "status TEXT, ", "", 1)
	}
	itemsDDL := "CREATE TABLE model_check_items (" + itemColumns + ")"
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		"CREATE TABLE model_check_runs (" + runsColumns + ")",
		itemsDDL,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("loose fixture 建表失败: %v", err)
		}
	}
	store, err := NewStoreWithMode(db, StoreSQLite)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func TestW12aGetRunReadErrorOnMissingColumn(t *testing.T) {
	store, _ := w12aLooseFixture(t, false, "id TEXT")
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "get model check run") {
		t.Fatalf("缺列应报读取错误: %v", err)
	}
	// beginRunningRunTx 的状态读取错误分支。
	if _, err := store.beginRunningRunTx(context.Background(), "w12a-run"); err == nil || !strings.Contains(err.Error(), "read model check run status") {
		t.Fatalf("缺列应使状态读取失败: %v", err)
	}
}

func TestW12aGetRunNullTimestampAndItemScanErrors(t *testing.T) {
	itemColumns := "id TEXT, run_id TEXT, item_key TEXT, item_type TEXT, status TEXT, score INTEGER, max_score INTEGER, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT, error_code TEXT, error_message TEXT, created_at TEXT, updated_at TEXT"
	store, db := w12aLooseFixture(t, true, itemColumns)
	// started_at 为 NULL → readTimestamp 错误；其余 NULL 文本列给出空串。
	if _, err := db.Exec(`INSERT INTO model_check_runs (id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, model, profile, trigger_kind, trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message, probe_set_version, started_at, created_at, updated_at, request_summary_json, result_summary_json, policy_snapshot_json, quality_decision_json) VALUES ('w12a-run', 'sys', 'actor', 'gpt', 'account', 'acct', 'm', 'quick', 'manual', 0, 1, 'likely', 0, 100, 'running', '', 'v1', NULL, NULL, NULL, '{}', '{}', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL started_at 应报时间戳错误: %v", err)
	}
	// 逐列补齐：created_at 仍 NULL → created 分支。
	if _, err := db.Exec(`UPDATE model_check_runs SET started_at='2026-09-17T00:00:00Z' WHERE id='w12a-run'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL created_at 应报时间戳错误: %v", err)
	}
	// 补齐 runs 时间戳，继续 items 扫描错误（status NULL）。
	if _, err := db.Exec(`UPDATE model_check_runs SET created_at='2026-09-17T00:00:00Z', updated_at='2026-09-17T00:00:00Z' WHERE id='w12a-run'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_items (id, run_id, item_key, item_type, status, score, max_score, evidence_summary_json, created_at, updated_at) VALUES ('it', 'w12a-run', 'k', 't', NULL, 0, 0, '{}', NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil {
		t.Fatalf("NULL item status 应触发扫描错误")
	}
	// item 全部合法但 created_at 为 NULL → 时间戳错误。
	if _, err := db.Exec(`UPDATE model_check_items SET status='passed', created_at='2026-09-17T00:00:00Z', updated_at='2026-09-17T00:00:00Z' WHERE id='it'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE model_check_items SET created_at=NULL WHERE id='it'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL item created_at 应报时间戳错误: %v", err)
	}
	// NULL item updated_at → updated 分支。
	if _, err := db.Exec(`UPDATE model_check_items SET created_at='2026-09-17T00:00:00Z', updated_at=NULL WHERE id='it'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL item updated_at 应报时间戳错误: %v", err)
	}
}

func TestW12aListRunsReadTimestampError(t *testing.T) {
	itemColumns := "id TEXT"
	store, _ := w12aLooseFixture(t, true, itemColumns)
	if _, err := store.db.Exec(`INSERT INTO model_check_runs (id, system_account_id, provider_code, target_type, target_id, model, profile, trigger_kind, trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message, probe_set_version, created_at) VALUES ('w12a-run', 'sys', 'gpt', 'account', 'acct', 'm', 'quick', 'manual', 0, 1, 'likely', 0, 100, 'running', '', 'v1', NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRuns(context.Background(), RunListOptions{Page: 1, PageSize: 10}); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL created_at 应报时间戳错误: %v", err)
	}
}

func TestW12aProjectOutcomeNegativeDurationClamp(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	// startedAt 晚于 finishedAt：duration 夹为 0，投影仍成功。
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 1, MaxScore: 10,
		Message: "ok", FinishedAt: now.Add(-time.Second), QualityDecision: json.RawMessage(`{}`),
		ResultSummary: json.RawMessage(`{}`),
		Items:         []ItemInput{w12aItemInput("w12a-run")},
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatal(err)
	}
	var duration int64
	if err := store.db.QueryRow(`SELECT duration_ms FROM model_check_runs WHERE id='w12a-run'`).Scan(&duration); err != nil || duration != 0 {
		t.Fatalf("投影 duration 应夹为 0: %d %v", duration, err)
	}
}

func TestW12aProjectOutcomeInvalidItem(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	bad := w12aItemInput("w12a-run")
	bad.Score = 99
	bad.MaxScore = 10
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 1, MaxScore: 10,
		Message: "ok", FinishedAt: now.Add(time.Second),
		Items: []ItemInput{bad},
	}
	if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "projection item is invalid") {
		t.Fatalf("非法投影 item 应报错: %v", err)
	}
}

func TestW12aAppendObservationFeaturePointers(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	constraint := true
	first := 1.5
	input := ObservationInput{
		ID: "obs", RunID: "w12a-run", SystemAccountID: "sys", AccountID: "acct",
		ProviderCode: "gpt", ProviderProtocolProfileID: "p", EndpointFamily: "responses",
		RequestedModel: "m", MappedUpstreamModel: "m", UpstreamBucketHMAC: "u",
		CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k",
		ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t",
		ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact",
		ProtocolStatus: "passed", CreatedAt: now, ConstraintPassed: &constraint,
		Features: [8]*float64{0: &first},
	}
	if err := store.AppendObservation(ctx, input); err != nil {
		t.Fatal(err)
	}
	var feature1 sql.NullFloat64
	var constraintPassed sql.NullInt64
	if err := store.db.QueryRow(`SELECT feature_1, constraint_passed FROM model_check_observations WHERE id='obs'`).Scan(&feature1, &constraintPassed); err != nil {
		t.Fatal(err)
	}
	if !feature1.Valid || feature1.Float64 != 1.5 || !constraintPassed.Valid || constraintPassed.Int64 != 1 {
		t.Fatalf("特征与约束写回不符: %v %v", feature1, constraintPassed)
	}
}

func TestW12aUpdateQualityDecisionReadError(t *testing.T) {
	// runs 缺 policy_snapshot_json：质量决策读取失败（非 ErrNoRows）。
	store, _ := w12aLooseFixture(t, false, "id TEXT")
	if err := store.UpdateQualityDecision(context.Background(), QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "read quality decision run") {
		t.Fatalf("缺列应使读取失败: %v", err)
	}
}

func TestW12aVerifyTerminalItemsReadError(t *testing.T) {
	// items 缺 trace_id 列：终态重放的 items 读取失败。
	store, _ := w12aLooseFixture(t, true, "id TEXT PRIMARY KEY, run_id TEXT, item_key TEXT, item_type TEXT, status TEXT, score INTEGER, max_score INTEGER, duration_ms INTEGER, evidence_summary_json TEXT, error_code TEXT, error_message TEXT, created_at TEXT, updated_at TEXT")
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(ctx, w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "w12a-run", RunCompleted, "likely", 1, 10, "m", now.Add(time.Second), json.RawMessage(`{}`), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 1, MaxScore: 10,
		Message: "m", FinishedAt: now.Add(time.Second), QualityDecision: json.RawMessage(`{}`),
		ResultSummary: json.RawMessage(`{}`),
		Items:         []ItemInput{w12aItemInput("w12a-run")},
	}
	if err := store.ProjectOutcome(ctx, projection); err == nil || !strings.Contains(err.Error(), "read projected model check items") {
		t.Fatalf("终态重放缺列应报读取错误: %v", err)
	}
}

// ---- 宽松表变体：逐列缺失驱动 UPDATE/SELECT 失败分支 ----

func TestW12aGetRunRemainingDecodeErrors(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	w12aSeedRuns(t, store)
	for _, column := range []string{"result_summary_json", "policy_snapshot_json", "quality_decision_json"} {
		if _, err := store.db.Exec(`UPDATE model_check_runs SET ` + column + `='broken' WHERE id='w12a-run-a'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetRun(ctx, "w12a-run-a", ""); err == nil || !strings.Contains(err.Error(), "decode model check JSON") {
			t.Fatalf("损坏的 %s 应报错: %v", column, err)
		}
		if _, err := store.db.Exec(`UPDATE model_check_runs SET ` + column + `='{}' WHERE id='w12a-run-a'`); err != nil {
			t.Fatal(err)
		}
	}
	// items 查询失败分支：items 表缺列（GetRun 主查询不受影响）。
	brokenItems, _ := w12aLooseFixture(t, true, "id TEXT")
	if _, err := brokenItems.db.Exec(`INSERT INTO model_check_runs (id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, model, profile, trigger_kind, trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message, probe_set_version, started_at, created_at, updated_at, request_summary_json, result_summary_json, policy_snapshot_json, quality_decision_json) VALUES ('w12a-run', 'sys', 'actor', 'gpt', 'account', 'acct', 'm', 'quick', 'manual', 0, 1, 'likely', 0, 100, 'running', '', 'v1', '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z', '{}', '{}', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := brokenItems.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "list model check items") {
		t.Fatalf("items 缺列应使查询失败: %v", err)
	}
}

func TestW12aGetRunUpdatedAtNullTimestamp(t *testing.T) {
	itemColumns := "id TEXT, run_id TEXT, item_key TEXT, item_type TEXT, status TEXT, score INTEGER, max_score INTEGER, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT, error_code TEXT, error_message TEXT, created_at TEXT, updated_at TEXT"
	store, db := w12aLooseFixture(t, true, itemColumns)
	if _, err := db.Exec(`INSERT INTO model_check_runs (id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, model, profile, trigger_kind, trusted_comparison_enabled, trusted_comparison_available, level, score, max_score, status, message, probe_set_version, started_at, created_at, updated_at, request_summary_json, result_summary_json, policy_snapshot_json, quality_decision_json) VALUES ('w12a-run', 'sys', 'actor', 'gpt', 'account', 'acct', 'm', 'quick', 'manual', 0, 1, 'likely', 0, 100, 'running', '', 'v1', '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z', NULL, '{}', '{}', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRun(context.Background(), "w12a-run", ""); err == nil || !strings.Contains(err.Error(), "invalid model check timestamp") {
		t.Fatalf("NULL updated_at 应报时间戳错误: %v", err)
	}
}

func TestW12aListRunsDefaultPage(t *testing.T) {
	ctx := context.Background()
	store := w12aStore(t)
	w12aSeedRuns(t, store)
	result, err := store.ListRuns(ctx, RunListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Page != 1 || result.PageSize != 20 {
		t.Fatalf("默认分页应为 1/20: %#v", result)
	}
	if err := store.checkIndexes(w12aCtxCanceled(t)); err == nil || !strings.Contains(err.Error(), "verify model check indexes") {
		t.Fatalf("取消上下文应使 sqlite 索引校验失败: %v", err)
	}
}

func TestW12aAppendObservationInsertError(t *testing.T) {
	// observations 缺 feature_1 列：事务开启成功但 INSERT 失败。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStoreWithMode(db, StoreSQLite)
	if err != nil {
		t.Fatal(err)
	}
	runsColumns := "id TEXT PRIMARY KEY, system_account_id TEXT, actor_system_account_id TEXT, provider_code TEXT, target_type TEXT, target_id TEXT, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT, profile TEXT, trigger_kind TEXT, schedule_id TEXT, trusted_comparison_enabled INTEGER, trusted_comparison_available INTEGER, level TEXT, score INTEGER, max_score INTEGER, status TEXT, message TEXT, trace_id TEXT, probe_set_version TEXT, started_at TEXT, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT, result_summary_json TEXT, policy_snapshot_json TEXT, quality_decision_json TEXT, error_message TEXT, created_at TEXT, updated_at TEXT"
	observationColumns := "id TEXT PRIMARY KEY, run_id TEXT, system_account_id TEXT, account_id TEXT, provider_code TEXT, provider_protocol_profile_id TEXT, endpoint_family TEXT, requested_model TEXT, mapped_upstream_model TEXT, observed_model TEXT, mapping_applied INTEGER, upstream_bucket_hmac TEXT, cohort_key_hmac TEXT, population_key_hmac TEXT, probe_key_hmac TEXT, system_fingerprint_hmac TEXT, probe_family TEXT, probe_set_version TEXT, tokenizer_version TEXT, feature_version TEXT, round_index INTEGER, padding_tokens INTEGER, local_input_tokens INTEGER, reported_input_tokens INTEGER, cached_input_tokens INTEGER, constraint_passed INTEGER, feature_2 REAL, feature_3 REAL, feature_4 REAL, feature_5 REAL, feature_6 REAL, feature_7 REAL, feature_8 REAL, observation_status TEXT, identity_status TEXT, mapping_status TEXT, protocol_status TEXT, evidence_coverage INTEGER, trace_id TEXT, created_at TEXT"
	for _, ddl := range []string{
		"CREATE TABLE model_check_runs (" + runsColumns + ")",
		"CREATE TABLE model_check_items (id TEXT)",
		"CREATE TABLE model_check_observations (" + observationColumns + ")",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	constraint := true
	input := ObservationInput{
		ID: "obs", RunID: "w12a-run", SystemAccountID: "sys", AccountID: "acct",
		ProviderCode: "gpt", ProviderProtocolProfileID: "p", EndpointFamily: "responses",
		RequestedModel: "m", MappedUpstreamModel: "m", UpstreamBucketHMAC: "u",
		CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k",
		ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t",
		ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact",
		ProtocolStatus: "passed", CreatedAt: now, ConstraintPassed: &constraint,
	}
	if err := store.AppendObservation(context.Background(), input); err == nil || !strings.Contains(err.Error(), "append model check observation") {
		t.Fatalf("observations 缺列应报错: %v", err)
	}
}

// w12aRunsFixture 建只含 runs 表（可裁剪列）的库，驱动 UPDATE 失败分支。
func w12aRunsFixture(t *testing.T, dropColumn string) *Store {
	t.Helper()
	columns := []string{
		"id TEXT PRIMARY KEY", "system_account_id TEXT", "actor_system_account_id TEXT", "provider_code TEXT",
		"target_type TEXT", "target_id TEXT", "target_name TEXT", "target_owner_system_account_id TEXT",
		"account_id TEXT", "group_id TEXT", "api_key_id TEXT", "model TEXT", "profile TEXT",
		"trigger_kind TEXT", "schedule_id TEXT", "trusted_comparison_enabled INTEGER",
		"trusted_comparison_available INTEGER", "level TEXT", "score INTEGER", "max_score INTEGER",
		"status TEXT", "message TEXT", "trace_id TEXT", "probe_set_version TEXT", "started_at TEXT",
		"finished_at TEXT", "duration_ms INTEGER", "request_summary_json TEXT", "result_summary_json TEXT",
		"policy_snapshot_json TEXT", "quality_decision_json TEXT", "error_message TEXT",
		"created_at TEXT", "updated_at TEXT",
	}
	kept := make([]string, 0, len(columns))
	for _, column := range columns {
		if strings.Split(column, " ")[0] != dropColumn {
			kept = append(kept, column)
		}
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE model_check_runs (" + strings.Join(kept, ",") + ")"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE model_check_items (id TEXT PRIMARY KEY, run_id TEXT, item_key TEXT, item_type TEXT, status TEXT, score INTEGER, max_score INTEGER, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT, error_code TEXT, error_message TEXT, created_at TEXT, updated_at TEXT)"); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreWithMode(db, StoreSQLite)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func w12aFinishedStore(t *testing.T, dropColumn string) *Store {
	t.Helper()
	store := w12aRunsFixture(t, dropColumn)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	input := w12aRunInput("w12a-run", now)
	if err := store.CreateRun(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(context.Background(), "w12a-run", RunCompleted, "likely", 1, 10, "m", now.Add(time.Second), json.RawMessage(`{}`), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW12aFinishRunUpdateError(t *testing.T) {
	store := w12aRunsFixture(t, "duration_ms")
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(context.Background(), "w12a-run", RunCompleted, "likely", 1, 10, "m", now.Add(time.Second), nil, nil); err == nil || !strings.Contains(err.Error(), "finish model check run") {
		t.Fatalf("缺 duration_ms 列应报错: %v", err)
	}
}

func TestW12aQualityDecisionWriteError(t *testing.T) {
	store := w12aStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(context.Background(), "w12a-run", RunCompleted, "likely", 1, 10, "m", now.Add(time.Second), json.RawMessage(`{}`), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE model_check_runs DROP COLUMN updated_at`); err != nil {
		t.Skipf("当前 sqlite 不支持 DROP COLUMN，跳过: %v", err)
	}
	if err := store.UpdateQualityDecision(context.Background(), QualityDecisionUpdate{RunID: "w12a-run", Status: RunCompleted, ResultSummary: json.RawMessage(`{}`), PolicySnapshot: json.RawMessage(`{}`), Decision: json.RawMessage(`{"d":1}`)}); err == nil || !strings.Contains(err.Error(), "write quality decision") {
		t.Fatalf("缺 updated_at 列应报错: %v", err)
	}
}

func TestW12aProjectOutcomeReadAndFinishErrors(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	projection := OutcomeProjection{
		RunID: "w12a-run", Status: RunCompleted, Level: "likely", Score: 1, MaxScore: 10,
		Message: "ok", FinishedAt: now.Add(time.Second), QualityDecision: json.RawMessage(`{}`),
		Items: []ItemInput{w12aItemInput("w12a-run")},
	}
	// runs 缺 duration_ms：投影 UPDATE 失败。
	noDuration := w12aRunsFixture(t, "duration_ms")
	if err := noDuration.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := noDuration.ProjectOutcome(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "finish projected model check run") {
		t.Fatalf("缺 duration_ms 列应报投影完成错误: %v", err)
	}
	// items 缺 evidence_summary_json：投影 item INSERT 失败。
	noEvidence, _ := w12aLooseFixture(t, true, "id TEXT PRIMARY KEY, run_id TEXT, item_key TEXT, item_type TEXT, status TEXT, score INTEGER, max_score INTEGER, duration_ms INTEGER, trace_id TEXT, error_code TEXT, error_message TEXT, created_at TEXT, updated_at TEXT")
	if err := noEvidence.CreateRun(context.Background(), w12aRunInput("w12a-run", now)); err != nil {
		t.Fatal(err)
	}
	if err := noEvidence.ProjectOutcome(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "append model check projected item") {
		t.Fatalf("items 缺列应报投影 item 错误: %v", err)
	}
}
