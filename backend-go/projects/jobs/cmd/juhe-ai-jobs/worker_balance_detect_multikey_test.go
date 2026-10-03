package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// 余额自动探测多 Key 执行接入测试：QueryBuiltin 改走 shared
// ExecuteAccountBalanceQuery 后——
//   - api_keys 数组封套的候选经逐 Key 查询得到合并快照（sum 场景：sub2api
//     quota_limited → scope=key → 安全合计）；
//   - 合并快照的 keyCount/queriedKeyCount/scope/aggregation/keyBalances 透传
//     进 relay_balance 持久化 JSON（gateway 明细端契约）；
//   - 单 Key 候选回归不变：行为零变化，且持久化 JSON 不长出多 Key 字段。

// newSub2APIMultiKeyUpstream 构造按 Authorization Key 返回不同余额的 sub2api
// 假上游（/v1/usage，mode=quota_limited → BasisAPIKeyQuota → scope=key）。
func newSub2APIMultiKeyUpstream(t *testing.T, balances map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		balance, ok := balances[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"unit":"USD","remaining":%q,"mode":"quota_limited"}`, balance)))
	}))
}

// seedMultiKeyDueAccount 写入一个 api_keys 数组封套、到期探测意图的候选
// （返回到期围栏文本，供 candidate 构造）。
func seedMultiKeyDueAccount(t *testing.T, db *sql.DB, id string, keys []string, baseURL string) string {
	t.Helper()
	credentials, err := json.Marshal(map[string]any{"api_keys": keys, "base_url": baseURL})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(`
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at)
VALUES (?, 'sys-1', 'api_key', 'active', 1, ?, 0, '{}', ?, ?)
`, id, envelope, due, due); err != nil {
		t.Fatal(err)
	}
	return due
}

// TestBalanceRuntimeQueryBuiltinMultiKeySum：api_keys 数组封套经
// ExecuteAccountBalanceQuery 逐 Key 查询并合并——sum 场景断言合并快照的
// 口径字段与逐 Key 明细。
func TestBalanceRuntimeQueryBuiltinMultiKeySum(t *testing.T) {
	upstream := newSub2APIMultiKeyUpstream(t, map[string]string{"sk-a": "2.5", "sk-b": "3"})
	defer upstream.Close()
	runtime, businessDB, _ := wgNewBalanceRuntime(t, upstream.URL, false)
	dueText := seedMultiKeyDueAccount(t, businessDB, "acc-multi", []string{"sk-a", "sk-b"}, upstream.URL)
	ctx := context.Background()
	config := opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}
	candidate := opsjobs.BalanceDetectionCandidate{ID: "acc-multi", SystemAccountID: "sys-1", ConfigRevision: 1, InputVersion: int64Ptr(1), NextRefreshAt: &dueText}

	result, err := runtime.QueryBuiltin(ctx, candidate, config)
	if err != nil {
		t.Fatalf("多 Key builtin 查询必须成功: %v", err)
	}
	// 窄投影的合并状态（matched 语义依赖 status=fresh）。
	if result.Snapshot.Status != opsjobs.BalanceSnapshotFresh {
		t.Fatalf("合并快照状态必须为 fresh: %+v", result.Snapshot)
	}
	// detector 缓存的完整 J2 快照必须携带多 Key 合并结果。
	cachedRaw, ok := runtime.detected.Load("snapshot:acc-multi")
	if !ok {
		t.Fatal("QueryBuiltin 必须缓存完整 J2 快照")
	}
	cached, ok := cachedRaw.(*accountbalance.Snapshot)
	if !ok {
		t.Fatalf("缓存快照类型错误: %T", cachedRaw)
	}
	if cached.Status != accountbalance.StatusFresh || cached.KeyCount != 2 || cached.QueriedKeyCount != 2 {
		t.Fatalf("合并快照计数错误: %+v", cached)
	}
	if cached.Scope != accountbalance.ScopeKey || cached.Aggregation != accountbalance.AggregationSum {
		t.Fatalf("sum 场景口径必须是 key/sum: %+v", cached)
	}
	var total float64
	if _, err := fmt.Sscanf(cached.RemainingUSD, "%g", &total); err != nil || total != 5.5 {
		t.Fatalf("合并余额必须是精确合计 2.5+3=5.5，得到 %q (%v %v)", cached.RemainingUSD, total, err)
	}
	if len(cached.KeyBalances) != 2 {
		t.Fatalf("必须携带 2 个逐 Key 明细: %+v", cached.KeyBalances)
	}
	for index, entry := range cached.KeyBalances {
		if entry.KeyFingerprint == "" || entry.MaskedKey == "" {
			t.Fatalf("逐 Key 明细必须携带 fingerprint/maskedKey: %+v", entry)
		}
		if entry.Status != accountbalance.StatusFresh || entry.Scope != accountbalance.ScopeKey {
			t.Fatalf("逐 Key 明细状态/口径错误: %+v", entry)
		}
		if entry.LastAttemptAt == "" || entry.LastSuccessAt == "" {
			t.Fatalf("逐 Key 明细必须携带时间戳: %+v", entry)
		}
		_ = index
	}
}

// TestBalanceDetectMultiKeyPersistsKeyBalances：装配全链路（SQLite 实测）——
// 多 Key 候选探测命中后，relay_balance 持久化 JSON 必须携带多 Key 字段与
// 逐 Key 明细（gateway 明细端按 keyFingerprint join 的数据源）。
func TestBalanceDetectMultiKeyPersistsKeyBalances(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	root := t.TempDir()
	businessPath := root + "/business.sqlite3"
	statsPath := root + "/stats.sqlite3"
	businessDB := mustOpenSQLite(t, businessPath)
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, statsPath)
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	upstream := newSub2APIMultiKeyUpstream(t, map[string]string{"sk-a": "2.5", "sk-b": "3"})
	defer upstream.Close()
	seedMultiKeyDueAccount(t, businessDB, "acc-detect-multi", []string{"sk-a", "sk-b"}, upstream.URL)

	env := balanceDetectAssemblyEnv(t, businessPath, statsPath)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if _, err := assembly.runWiredJobOnce(ctx, "account-balance-auto-detect-recovery"); err != nil {
		t.Fatalf("runWiredJobOnce: %v", err)
	}

	var enabled int
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled FROM accounts WHERE id = 'acc-detect-multi'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("多 Key 合并命中后账户必须已开启余额查询")
	}
	var snapshotJSON string
	if err := statsDB.QueryRowContext(ctx, `SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-detect-multi' AND kind = 'relay_balance'`).Scan(&snapshotJSON); err != nil {
		t.Fatalf("relay_balance 快照行必须写入: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(snapshotJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != "fresh" || decoded["configRevision"] != float64(1) {
		t.Fatalf("快照 status/configRevision 错误: %v", decoded)
	}
	for _, key := range []string{"keyCount", "queriedKeyCount", "scope", "aggregation", "keyBalances"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("持久化 JSON 缺多 Key 字段 %s: %s", key, snapshotJSON)
		}
	}
	if decoded["keyCount"] != float64(2) || decoded["aggregation"] != "sum" || decoded["scope"] != "key" {
		t.Fatalf("多 Key 口径字段错误: %v", decoded)
	}
	var total float64
	if text, ok := decoded["remainingUsd"].(string); !ok {
		t.Fatalf("快照必须携带合并余额: %v", decoded["remainingUsd"])
	} else if _, err := fmt.Sscanf(text, "%g", &total); err != nil || total != 5.5 {
		t.Fatalf("合并余额必须为 5.5，得到 %s", text)
	}
	entries, ok := decoded["keyBalances"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("持久化 JSON 必须携带 2 个逐 Key 明细: %v", decoded["keyBalances"])
	}
	first, _ := entries[0].(map[string]any)
	if first == nil || first["keyFingerprint"] == "" || first["maskedKey"] == "" {
		t.Fatalf("逐 Key 明细必须携带 fingerprint/maskedKey: %v", entries[0])
	}
}

// TestBalancePersistSingleKeyStaysNarrow：单 Key 快照回归——缓存的单 Key
// J2 快照持久化后不得长出多 Key 字段（shared Snapshot 的 omitempty 契约，
// 既有 JSON 形状零变化）。瞬时失败三字段同为零值省略：输入不带瞬态字段时
// 不得长出 consecutiveTransientFailures 等键（「输入不带 → JSON 形状与既有
// 完全一致」的回归断言，修复前后恒绿）。
func TestBalancePersistSingleKeyStaysNarrow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, businessDB, statsDB := wgNewBalanceRuntime(t, "", false)
	wgSeedDueAccount(t, businessDB, "acc-narrow", upstream.URL, -time.Minute)
	if _, err := businessDB.Exec(`UPDATE accounts SET balance_query_enabled = 1, balance_query_config_json = '{"adapter":"builtin","intervalMinutes":5}' WHERE id = 'acc-narrow'`); err != nil {
		t.Fatal(err)
	}
	runtime.detected.Store("snapshot:acc-narrow", &accountbalance.Snapshot{
		Status:       accountbalance.StatusFresh,
		RemainingUSD: "1.250000",
		RawUnit:      accountbalance.RawUnitUSD,
		Basis:        accountbalance.BasisWallet,
	})
	ctx := context.Background()
	ok, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-narrow", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1,
		ExpectedConfig:         opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5},
		Snapshot:               opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1},
	})
	if err != nil || !ok {
		t.Fatalf("单 Key 快照写入必须成功: %v %v", ok, err)
	}
	var snapshotJSON string
	if err := statsDB.QueryRow(`SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-narrow'`).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(snapshotJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"keyCount", "queriedKeyCount", "scope", "aggregation", "keyBalances",
		"consecutiveTransientFailures", "lastTransientErrorMessage", "lastTransientFailureAt",
	} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("单 Key 持久化 JSON 不得携带 %s: %s", key, snapshotJSON)
		}
	}
	if decoded["remainingUsd"] != "1.250000" {
		t.Fatalf("单 Key 余额必须原样保留: %s", snapshotJSON)
	}
}

// TestBalancePersistTransientFieldsPersisted：pending 瞬态快照的瞬时失败
// 三字段（consecutiveTransientFailures/lastTransientErrorMessage/
// lastTransientFailureAt，shared Snapshot 原名）必须从 detector 缓存的完整
// J2 快照透传进 relay_balance 持久化 JSON——gateway 读端
// （accountsbalance/list_snapshot.go）与前端「刷新暂时失败（N/3）」提示的
// 数据源。修复前必红：buildSnapshotJSON 未拷贝三字段，JSON 缺键。
func TestBalancePersistTransientFieldsPersisted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, businessDB, statsDB := wgNewBalanceRuntime(t, "", false)
	wgSeedDueAccount(t, businessDB, "acc-transient", upstream.URL, -time.Minute)
	if _, err := businessDB.Exec(`UPDATE accounts SET balance_query_enabled = 1, balance_query_config_json = '{"adapter":"builtin","intervalMinutes":5}' WHERE id = 'acc-transient'`); err != nil {
		t.Fatal(err)
	}
	runtime.detected.Store("snapshot:acc-transient", &accountbalance.Snapshot{
		Status:                    accountbalance.StatusPending,
		LastAttemptAt:             "2026-09-27T07:59:00Z",
		ConsecutiveTransientFails: 2,
		LastTransientErrorMessage: "上游余额查询超时",
		LastTransientFailureAt:    "2026-09-27T07:58:30Z",
	})
	ctx := context.Background()
	ok, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-transient", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1,
		ExpectedConfig:         opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5},
		Snapshot:               opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotStatus("pending"), ConfigRevision: 1, LastAttemptAt: "2026-09-27T07:59:00Z"},
	})
	if err != nil || !ok {
		t.Fatalf("瞬态快照写入必须成功: %v %v", ok, err)
	}
	var snapshotJSON string
	if err := statsDB.QueryRow(`SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-transient'`).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(snapshotJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != "pending" {
		t.Fatalf("瞬态快照状态必须直通: %v", decoded["status"])
	}
	if decoded["consecutiveTransientFailures"] != float64(2) {
		t.Fatalf("缺 consecutiveTransientFailures 或值错误: %s", snapshotJSON)
	}
	if decoded["lastTransientErrorMessage"] != "上游余额查询超时" {
		t.Fatalf("缺 lastTransientErrorMessage 或值错误: %s", snapshotJSON)
	}
	if decoded["lastTransientFailureAt"] != "2026-09-27T07:58:30Z" {
		t.Fatalf("缺 lastTransientFailureAt 或值错误: %s", snapshotJSON)
	}
}
