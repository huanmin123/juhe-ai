package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// 余额快照投影（account-balance-stats-projection）测试：对齐归档 Node
// account-balance-jobs-projector 的 Go 等价任务。覆盖：
//   - J2 快照 JSON → stats relay_balance JSON 的映射（键集合与值断言，含
//     多 Key 字段透传）；
//   - 全量 UPSERT 幂等（同一行两轮执行后逐列一致）；
//   - config_revision 从 juhe_jobs 行的列取值（gateway 匹配围栏关键）；
//   - 账户缺失/已删除行跳过不阻塞；
//   - PG 分支 SQL 契约（录制驱动，对齐 wfix_balance_due_fence_test 的
//     pgRecorder 模式）。

// fixedProjectionClock 是投影测试的固定时钟（两轮执行逐列一致的前提）。
var fixedProjectionClock = time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)

// j2SnapshotJSON 用 shared Snapshot 结构序列化 J2 行的 snapshot_json（保证
// tag 与生产写入同源，测试不断言手写 JSON 形状）。
func j2SnapshotJSON(t *testing.T, snapshot accountbalance.Snapshot) string {
	t.Helper()
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(serialized)
}

// TestBalanceProjectionMappingSingleKey：单 Key J2 快照映射——键集合精确
// 等于 balanceSnapshotPersist 契约集（configRevision 由列注入），remainingUsd
// 等字段直通（gateway 读端按字符串消费，无货币换算），单 Key 无多 Key 字段。
// 该用例同时是「输入不带瞬时失败三字段 → JSON 形状与既有完全一致」的回归
// 断言：三字段为零值 omitempty，fresh 输入不会长出 consecutiveTransientFailures
// 等键（len(decoded) 精确相等检查覆盖）。
func TestBalanceProjectionMappingSingleKey(t *testing.T) {
	source := j2SnapshotJSON(t, accountbalance.Snapshot{
		Status:        accountbalance.StatusFresh,
		RemainingUSD:  "12.500000",
		RawRemaining:  "12.5",
		RawUnit:       accountbalance.RawUnitUSD,
		Basis:         accountbalance.BasisWallet,
		LastAttemptAt: "2026-09-27T07:59:00Z",
		LastSuccessAt: "2026-09-27T07:59:00Z",
	})
	view, serialized, err := projectBalanceSnapshot(source, 7)
	if err != nil {
		t.Fatalf("单 Key 映射必须成功: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"status", "configRevision", "remainingUsd", "rawRemaining", "rawUnit", "basis", "lastAttemptAt", "lastSuccessAt"}
	if len(decoded) != len(wantKeys) {
		t.Fatalf("单 Key 快照键集合必须恰为契约字段，得到 %v", decoded)
	}
	for _, key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("单 Key 快照缺 %s: %s", key, serialized)
		}
	}
	if decoded["status"] != "fresh" || decoded["configRevision"] != float64(7) {
		t.Fatalf("status/configRevision 映射错误: %v", decoded)
	}
	if decoded["remainingUsd"] != "12.500000" || decoded["rawRemaining"] != "12.5" {
		t.Fatalf("金额字段必须直通（无换算）: %v", decoded)
	}
	if decoded["rawUnit"] != "usd" || decoded["basis"] != "wallet" {
		t.Fatalf("unit/basis 必须直通: %v", decoded)
	}
	if view.ConfigRevision != 7 {
		t.Fatalf("configRevision 必须取自 juhe_jobs 行的 config_revision 列: %d", view.ConfigRevision)
	}
	// 单 Key 快照不得长出多 Key 字段（shared Snapshot omitempty 契约）。
	for _, key := range []string{"keyCount", "queriedKeyCount", "scope", "aggregation", "keyBalances"} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("单 Key 快照不得携带多 Key 字段 %s: %s", key, serialized)
		}
	}
}

// TestBalanceProjectionMappingMultiKey：多 Key J2 快照映射——合并结果字段
// 透传，keyBalances 元素键名与 shared KeyBalance 的 camelCase tag 逐键一致
// （gateway 明细端按 keyFingerprint join 的契约）。
func TestBalanceProjectionMappingMultiKey(t *testing.T) {
	source := j2SnapshotJSON(t, accountbalance.Snapshot{
		Status:          accountbalance.StatusFresh,
		RemainingUSD:    "5.500000",
		RawUnit:         accountbalance.RawUnitUSD,
		Basis:           accountbalance.BasisAPIKeyQuota,
		LastAttemptAt:   "2026-09-27T07:59:00Z",
		LastSuccessAt:   "2026-09-27T07:59:00Z",
		KeyCount:        2,
		QueriedKeyCount: 2,
		Scope:           accountbalance.ScopeKey,
		Aggregation:     accountbalance.AggregationSum,
		KeyBalances: []accountbalance.KeyBalance{
			{
				KeyFingerprint: "fp-a", MaskedKey: "sk-…:key-a", Status: accountbalance.StatusFresh,
				RemainingUSD: "2.500000", RawUnit: accountbalance.RawUnitUSD,
				Scope: accountbalance.ScopeKey, Basis: accountbalance.BasisAPIKeyQuota,
				LastAttemptAt: "2026-09-27T07:59:00Z", LastSuccessAt: "2026-09-27T07:59:00Z",
			},
			{
				KeyFingerprint: "fp-b", MaskedKey: "sk-…:key-b", Status: accountbalance.StatusFailed,
				ErrorMessage: "上游余额查询超时", LastAttemptAt: "2026-09-27T07:59:00Z",
			},
		},
	})
	_, serialized, err := projectBalanceSnapshot(source, 3)
	if err != nil {
		t.Fatalf("多 Key 映射必须成功: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"keyCount", "queriedKeyCount", "scope", "aggregation", "keyBalances"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("多 Key 快照缺 %s: %s", key, serialized)
		}
	}
	if decoded["keyCount"] != float64(2) || decoded["queriedKeyCount"] != float64(2) {
		t.Fatalf("多 Key 计数必须透传: %v", decoded)
	}
	if decoded["scope"] != "key" || decoded["aggregation"] != "sum" {
		t.Fatalf("多 Key 口径字段必须透传: %v", decoded)
	}
	entries, ok := decoded["keyBalances"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("keyBalances 必须透传 2 个元素: %v", decoded["keyBalances"])
	}
	first, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("keyBalances 元素必须是对象: %v", entries[0])
	}
	for _, key := range []string{"keyFingerprint", "maskedKey", "status", "remainingUsd", "rawUnit", "scope", "basis", "lastAttemptAt", "lastSuccessAt"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("keyBalances[0] 缺 %s（gateway 明细端 join 键）: %v", key, first)
		}
	}
	second, ok := entries[1].(map[string]any)
	if !ok || second["errorMessage"] != "上游余额查询超时" {
		t.Fatalf("失败 Key 的诊断必须透传: %v", entries[1])
	}
}

// TestBalanceProjectionMappingTransientFields：J2 pending/failed 瞬态快照的
// 瞬时失败三字段（consecutiveTransientFailures/lastTransientErrorMessage/
// lastTransientFailureAt，shared Snapshot 原名）必须直通进 stats snapshot_json
// ——gateway 读端（accountsbalance/list_snapshot.go）与前端
// 「刷新暂时失败（N/3）」提示的数据源。修复前必红：balanceSnapshotFromJ2
// 未拷贝三字段，投影 JSON 缺键。
func TestBalanceProjectionMappingTransientFields(t *testing.T) {
	source := j2SnapshotJSON(t, accountbalance.Snapshot{
		Status:                    accountbalance.StatusPending,
		LastAttemptAt:             "2026-09-27T07:59:00Z",
		ConsecutiveTransientFails: 2,
		LastTransientErrorMessage: "上游余额查询超时",
		LastTransientFailureAt:    "2026-09-27T07:58:30Z",
	})
	view, serialized, err := projectBalanceSnapshot(source, 5)
	if err != nil {
		t.Fatalf("瞬态快照映射必须成功: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != "pending" {
		t.Fatalf("瞬态快照状态必须直通: %v", decoded["status"])
	}
	if decoded["consecutiveTransientFailures"] != float64(2) {
		t.Fatalf("缺 consecutiveTransientFailures 或值错误: %s", serialized)
	}
	if decoded["lastTransientErrorMessage"] != "上游余额查询超时" {
		t.Fatalf("缺 lastTransientErrorMessage 或值错误: %s", serialized)
	}
	if decoded["lastTransientFailureAt"] != "2026-09-27T07:58:30Z" {
		t.Fatalf("缺 lastTransientFailureAt 或值错误: %s", serialized)
	}
	if view.ConsecutiveTransientFails != 2 || view.LastTransientErrorMessage != "上游余额查询超时" || view.LastTransientFailureAt != "2026-09-27T07:58:30Z" {
		t.Fatalf("映射视图瞬时失败三字段错误: %+v", view)
	}
}

// TestBalanceProjectionMappingRejectsInvalid：非 JSON 与缺 status 的 J2 行
// 必须显式失败（fail closed，不静默投影空行）。
func TestBalanceProjectionMappingRejectsInvalid(t *testing.T) {
	if _, _, err := projectBalanceSnapshot("not-json", 1); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	empty := j2SnapshotJSON(t, accountbalance.Snapshot{})
	if _, _, err := projectBalanceSnapshot(empty, 1); err == nil {
		t.Fatal("缺 status 的快照必须报错")
	}
}

// newProjectionSQLiteRuntime 构造单文件 SQLite 投影运行时：业务 accounts +
// J2 account_balance_snapshots + stats account_usage_snapshots 同库直名
// （生产 PG 三者按 schema 限定，方言分叉由 postgres 标志承担）。
func newProjectionSQLiteRuntime(t *testing.T) (*balanceProjectionRuntime, *sql.DB) {
	t.Helper()
	db := mustOpenSQLite(t, t.TempDir()+"/projection.sqlite3")
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := balanceFixtureSchema(ctx, db, db); err != nil {
		t.Fatal(err)
	}
	j2Schema := `
CREATE TABLE IF NOT EXISTS account_balance_snapshots (
  account_id TEXT PRIMARY KEY,
  input_version INTEGER NOT NULL,
  config_revision INTEGER NOT NULL,
  trigger TEXT NOT NULL,
  snapshot_json TEXT NOT NULL,
  next_refresh_at TEXT,
  updated_at TEXT NOT NULL
);
`
	if _, err := db.ExecContext(ctx, j2Schema); err != nil {
		t.Fatal(err)
	}
	runtime := &balanceProjectionRuntime{db: db, postgres: false, nowFunc: func() time.Time { return fixedProjectionClock }}
	return runtime, db
}

// seedProjectionSource 写入一行 J2 快照与对应业务账户（deleted 可选）。
func seedProjectionSource(t *testing.T, db *sql.DB, accountID, systemAccountID string, configRevision int64, snapshotJSON string, deleted bool, nextRefreshAt string) {
	t.Helper()
	var deletedAt any
	if deleted {
		deletedAt = "2026-09-01T00:00:00Z"
	}
	if _, err := db.Exec(`
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at, deleted_at)
VALUES (?, ?, 'api_key', 'active', 1, '{"api_key":"sk-x"}', 1, '{"adapter":"builtin","intervalMinutes":5}', ?, ?, ?)
`, accountID, systemAccountID, nextRefreshAt, nextRefreshAt, deletedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO account_balance_snapshots (account_id, input_version, config_revision, trigger, snapshot_json, next_refresh_at, updated_at)
VALUES (?, 1, ?, 'periodic', ?, ?, ?)
`, accountID, configRevision, snapshotJSON, nextRefreshAt, nextRefreshAt); err != nil {
		t.Fatal(err)
	}
}

// TestBalanceProjectionUpsertIdempotent：全量 UPSERT 端到端（SQLite 实测）——
// 单 Key + 多 Key 两行投影落库、账户已删除的行跳过；固定时钟下同一行两轮
// 执行后逐列一致（幂等：重复执行结果一致；生产时钟下仅 updated_at/created_at
// 随写入时间前进，与 ReplaceSnapshotIfCurrent 语义一致，载荷不变）。
func TestBalanceProjectionUpsertIdempotent(t *testing.T) {
	runtime, db := newProjectionSQLiteRuntime(t)
	ctx := context.Background()
	singleJSON := j2SnapshotJSON(t, accountbalance.Snapshot{
		Status:        accountbalance.StatusFresh,
		RemainingUSD:  "12.500000",
		RawUnit:       accountbalance.RawUnitUSD,
		Basis:         accountbalance.BasisWallet,
		LastAttemptAt: "2026-09-27T07:59:00Z",
		LastSuccessAt: "2026-09-27T07:59:00Z",
	})
	multiJSON := j2SnapshotJSON(t, accountbalance.Snapshot{
		Status:          accountbalance.StatusFresh,
		RemainingUSD:    "5.500000",
		RawUnit:         accountbalance.RawUnitUSD,
		Basis:           accountbalance.BasisAPIKeyQuota,
		LastAttemptAt:   "2026-09-27T07:59:00Z",
		LastSuccessAt:   "2026-09-27T07:59:00Z",
		KeyCount:        2,
		QueriedKeyCount: 2,
		Scope:           accountbalance.ScopeKey,
		Aggregation:     accountbalance.AggregationSum,
		KeyBalances: []accountbalance.KeyBalance{
			{KeyFingerprint: "fp-a", MaskedKey: "sk-…:a", Status: accountbalance.StatusFresh, RemainingUSD: "2.500000", Scope: accountbalance.ScopeKey, LastAttemptAt: "2026-09-27T07:59:00Z", LastSuccessAt: "2026-09-27T07:59:00Z"},
			{KeyFingerprint: "fp-b", MaskedKey: "sk-…:b", Status: accountbalance.StatusFresh, RemainingUSD: "3.000000", Scope: accountbalance.ScopeKey, LastAttemptAt: "2026-09-27T07:59:00Z", LastSuccessAt: "2026-09-27T07:59:00Z"},
		},
	})
	nextRefresh := "2026-09-27T08:05:00Z"
	seedProjectionSource(t, db, "acc-single", "sys-1", 7, singleJSON, false, nextRefresh)
	seedProjectionSource(t, db, "acc-multi", "sys-1", 3, multiJSON, false, nextRefresh)
	// 账户已删除：J2 行存在但 join 不到 → 跳过（不阻塞其余行）。
	seedProjectionSource(t, db, "acc-deleted", "sys-2", 1, singleJSON, true, nextRefresh)

	result, err := runtime.ProjectAll(ctx)
	if err != nil {
		t.Fatalf("首轮投影必须成功: %v", err)
	}
	if result.ScannedCount != 3 || result.ProjectedCount != 2 || result.SkippedCount != 1 {
		t.Fatalf("首轮计数错误: %+v", result)
	}
	type snapshotRow struct {
		systemAccountID  string
		refreshStatus    string
		snapshotJSON     string
		lastAttemptAt    string
		lastSuccessAt    sql.NullString
		nextRefreshAfter sql.NullString
		lastErrorMessage sql.NullString
		updatedAt        string
		createdAt        string
	}
	loadRows := func() map[string]snapshotRow {
		t.Helper()
		rows, err := db.Query(`SELECT system_account_id, account_id, refresh_status, snapshot_json, last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
      FROM account_usage_snapshots WHERE kind = 'relay_balance' ORDER BY account_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]snapshotRow{}
		for rows.Next() {
			var row snapshotRow
			var accountID string
			if err := rows.Scan(&row.systemAccountID, &accountID, &row.refreshStatus, &row.snapshotJSON, &row.lastAttemptAt, &row.lastSuccessAt, &row.nextRefreshAfter, &row.lastErrorMessage, &row.updatedAt, &row.createdAt); err != nil {
				t.Fatal(err)
			}
			result[accountID] = row
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := loadRows()
	if len(first) != 2 {
		t.Fatalf("投影后必须恰有 2 行 relay_balance: %d", len(first))
	}
	// 行 1（单 Key）：configRevision 必须取 juhe_jobs 行 config_revision 列，
	// next_refresh_after 直通 J2 next_refresh_at。
	single := first["acc-single"]
	if single.systemAccountID != "sys-1" || single.refreshStatus != "fresh" {
		t.Fatalf("单 Key 行主键/状态错误: %+v", single)
	}
	singleDecoded := map[string]any{}
	if err := json.Unmarshal([]byte(single.snapshotJSON), &singleDecoded); err != nil {
		t.Fatal(err)
	}
	if singleDecoded["configRevision"] != float64(7) {
		t.Fatalf("configRevision 必须来自 J2 行的 config_revision 列: %v", singleDecoded["configRevision"])
	}
	if !single.nextRefreshAfter.Valid || single.nextRefreshAfter.String != nextRefresh {
		t.Fatalf("next_refresh_after 必须直通 J2 next_refresh_at: %+v", single.nextRefreshAfter)
	}
	if single.lastAttemptAt != "2026-09-27T07:59:00Z" || !single.lastSuccessAt.Valid {
		t.Fatalf("last_attempt/last_success 列语义错误: %+v", single)
	}
	// 行 2（多 Key）：逐 Key 明细保留在 stats 行（gateway 明细端契约）。
	multiDecoded := map[string]any{}
	if err := json.Unmarshal([]byte(first["acc-multi"].snapshotJSON), &multiDecoded); err != nil {
		t.Fatal(err)
	}
	if multiDecoded["configRevision"] != float64(3) || multiDecoded["keyCount"] != float64(2) {
		t.Fatalf("多 Key 行 configRevision/keyCount 错误: %v", multiDecoded)
	}
	if _, ok := multiDecoded["keyBalances"].([]any); !ok {
		t.Fatalf("多 Key 行必须携带 keyBalances: %v", multiDecoded)
	}

	// 第二轮执行：同行覆盖，逐列一致（幂等）。
	if _, err := runtime.ProjectAll(ctx); err != nil {
		t.Fatalf("第二轮投影必须成功: %v", err)
	}
	second := loadRows()
	if len(second) != 2 {
		t.Fatalf("重复执行不得产生新行: %d", len(second))
	}
	for _, accountID := range []string{"acc-single", "acc-multi"} {
		if first[accountID] != second[accountID] {
			t.Fatalf("重复执行后 %s 行必须逐列一致:\nfirst=%+v\nsecond=%+v", accountID, first[accountID], second[accountID])
		}
	}
}

// TestBalanceProjectionMissingAccountSkipped：账户行整体缺失（非删除）时
// 同样跳过并计数，不阻塞其余行投影。
func TestBalanceProjectionMissingAccountSkipped(t *testing.T) {
	runtime, db := newProjectionSQLiteRuntime(t)
	ctx := context.Background()
	snapshotJSON := j2SnapshotJSON(t, accountbalance.Snapshot{Status: accountbalance.StatusFresh})
	if _, err := db.Exec(`
INSERT INTO account_balance_snapshots (account_id, input_version, config_revision, trigger, snapshot_json, next_refresh_at, updated_at)
VALUES ('acc-orphan', 1, 1, 'periodic', ?, NULL, '2026-09-27T07:59:00Z')
`, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ProjectAll(ctx)
	if err != nil {
		t.Fatalf("孤儿行投影必须成功跳过: %v", err)
	}
	if result.ScannedCount != 1 || result.ProjectedCount != 0 || result.SkippedCount != 1 {
		t.Fatalf("孤儿行必须跳过: %+v", result)
	}
}

// ---- PG 分支录制驱动（对齐 wfix_balance_due_fence_test 的 pgRecorder 模式，
// 脚本化返回 J2 行与 owner 行，捕获 UPSERT 语句）。----

type projectionStatement struct {
	query string
	args  []driver.Value
}

type projectionPGRecorder struct {
	mu         sync.Mutex
	statements []projectionStatement
}

func (r *projectionPGRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, projectionStatement{query: query, args: values})
}

func (r *projectionPGRecorder) all() []projectionStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]projectionStatement{}, r.statements...)
}

// projectionPGConnector 按 calls 脚本依次返回结果集；ExecContext 只捕获。
type projectionPGConnector struct {
	rec   *projectionPGRecorder
	calls []driver.Rows
}

func (c *projectionPGConnector) Connect(context.Context) (driver.Conn, error) {
	return &projectionPGConn{rec: c.rec, calls: c.calls}, nil
}
func (c *projectionPGConnector) Driver() driver.Driver { return c }
func (c *projectionPGConnector) Open(string) (driver.Conn, error) {
	return &projectionPGConn{rec: c.rec, calls: c.calls}, nil
}

type projectionPGConn struct {
	rec   *projectionPGRecorder
	calls []driver.Rows
	index int
}

func (c *projectionPGConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}
func (c *projectionPGConn) Close() error { return nil }
func (c *projectionPGConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用")
}
func (c *projectionPGConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	return driver.RowsAffected(1), nil
}
func (c *projectionPGConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if c.index >= len(c.calls) {
		return nil, errors.New("recorder: 结果集脚本耗尽")
	}
	rows := c.calls[c.index]
	c.index++
	return rows, nil
}
func (c *projectionPGConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, int64, float64, bool, []byte, string, time.Time:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	}
	return errors.New("recorder: 不支持的参数类型")
}

type projectionScriptedRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *projectionScriptedRows) Columns() []string { return r.columns }
func (r *projectionScriptedRows) Close() error      { return nil }
func (r *projectionScriptedRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

// TestBalanceProjectionPGSQLContract：PG 分支 SQL 契约——源表限定
// juhe_jobs.account_balance_snapshots，owner 查询限定 juhe_business.accounts，
// UPSERT 落 juhe_stats.account_usage_snapshots kind='relay_balance'，冲突目标
// (system_account_id, account_id, kind)，next_refresh_after/updated_at 参数为
// time.Time（pgx 原生 timestamptz）。
func TestBalanceProjectionPGSQLContract(t *testing.T) {
	nextRefresh := time.Date(2026, 9, 27, 8, 5, 0, 0, time.UTC)
	recorder := &projectionPGRecorder{}
	db := sql.OpenDB(&projectionPGConnector{
		rec: recorder,
		calls: []driver.Rows{
			// 查询 1：juhe_jobs 源快照行。
			&projectionScriptedRows{
				columns: []string{"account_id", "config_revision", "snapshot_json", "next_refresh_at"},
				values: [][]driver.Value{{
					"acc-1", int64(7),
					`{"status":"fresh","remainingUsd":"1.500000","lastAttemptAt":"2026-09-27T07:59:00Z","lastSuccessAt":"2026-09-27T07:59:00Z"}`,
					nextRefresh,
				}},
			},
			// 查询 2：业务账户 owner 命名空间。
			&projectionScriptedRows{
				columns: []string{"id", "system_account_id"},
				values:  [][]driver.Value{{"acc-1", "sys-1"}},
			},
		},
	})
	t.Cleanup(func() { _ = db.Close() })
	runtime := &balanceProjectionRuntime{db: db, postgres: true, nowFunc: func() time.Time { return fixedProjectionClock }}
	result, err := runtime.ProjectAll(context.Background())
	if err != nil {
		t.Fatalf("PG 录制投影必须成功: %v", err)
	}
	if result.ScannedCount != 1 || result.ProjectedCount != 1 {
		t.Fatalf("PG 录制投影计数错误: %+v", result)
	}
	statements := recorder.all()
	if len(statements) != 3 {
		t.Fatalf("必须捕获 3 条语句（源读/owner 读/UPSERT）: %d", len(statements))
	}
	requireContains := func(haystack, needle, label string) {
		t.Helper()
		if !strings.Contains(haystack, needle) {
			t.Fatalf("%s 缺少 %q:\n%s", label, needle, haystack)
		}
	}
	requireContains(statements[0].query, "FROM juhe_jobs.account_balance_snapshots", "源读表限定")
	requireContains(statements[1].query, "FROM juhe_business.accounts", "owner 读表限定")
	requireContains(statements[1].query, "WHERE deleted_at IS NULL AND id IN", "owner 读删除过滤与 IN 分块")
	upsert := statements[2]
	requireContains(upsert.query, "INSERT INTO juhe_stats.account_usage_snapshots", "UPSERT 表限定")
	requireContains(upsert.query, "VALUES (?, ?, 'relay_balance', 'upstream_api', ?, ?, ?, ?, ?, ?, ?, ?)", "kind/source 字面量")
	requireContains(upsert.query, "ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET", "冲突目标契约")
	requireContains(upsert.query, "updated_at = excluded.updated_at", "created_at 不更新（UPSERT 列集契约）")
	// 参数形态（0 基）：$3 快照 JSON、$4 refresh_status 文本、$7
	// next_refresh_after 为 time.Time（pgx 原生 timestamptz）、$9/$10
	// updated/created 为 time.Time。
	if text, ok := upsert.args[2].(string); !ok || !strings.Contains(text, `"configRevision":7`) {
		t.Fatalf("snapshot_json 必须携带列注入的 configRevision: %v", upsert.args[2])
	}
	if next, ok := upsert.args[6].(time.Time); !ok || !next.Equal(nextRefresh) {
		t.Fatalf("next_refresh_after 参数必须是 time.Time 源值 %v，得到 %v", nextRefresh, upsert.args[6])
	}
	if status, ok := upsert.args[3].(string); !ok || status != "fresh" {
		t.Fatalf("refresh_status 必须取快照 status: %v", upsert.args[3])
	}
	if _, ok := upsert.args[8].(time.Time); !ok {
		t.Fatalf("updated_at 参数必须是 time.Time，得到 %T", upsert.args[8])
	}
}

// TestEnsureBalanceProjectionContractBranches：契约校验的缺失分支（PG 缺
// juhe_jobs 源表 → 报错；SQLite 直名路径只校验 stats 表）。
func TestEnsureBalanceProjectionContractBranches(t *testing.T) {
	ctx := context.Background()
	_, db := newProjectionSQLiteRuntime(t)
	if err := ensureBalanceProjectionContract(ctx, db, false); err != nil {
		t.Fatalf("SQLite 分支契约校验必须通过: %v", err)
	}
	// PG 分支：录制驱动对 information_schema 计数返回 0 → 缺源表报错。
	recorder := &projectionPGRecorder{}
	pgDB := sql.OpenDB(&projectionPGConnector{
		rec: recorder,
		calls: []driver.Rows{
			&projectionScriptedRows{columns: []string{"count"}, values: [][]driver.Value{{int64(0)}}},
		},
	})
	t.Cleanup(func() { _ = pgDB.Close() })
	if err := ensureBalanceProjectionContract(ctx, pgDB, true); err == nil {
		t.Fatal("缺 juhe_jobs 源表必须报错")
	}
}
