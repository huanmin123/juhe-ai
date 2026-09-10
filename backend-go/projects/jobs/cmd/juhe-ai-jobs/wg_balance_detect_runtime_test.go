package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

const wgBalanceSecret = "0123456789abcdef0123456789abcdef"

// wgNewBalanceRuntime 构造可直接驱动方法面的 balanceDetectRuntime（与
// wireBalanceDetectFamily 装配产物同构；组合根不导出运行态，直构造是唯一
// 单测入口）。
func wgNewBalanceRuntime(t *testing.T, upstreamURL string, withLease bool) (*balanceDetectRuntime, *sql.DB, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	statsPath := filepath.Join(root, "stats.sqlite3")
	biz := mustOpenSQLite(t, businessPath)
	t.Cleanup(func() { _ = biz.Close() })
	statsDB := mustOpenSQLite(t, statsPath)
	t.Cleanup(func() { _ = statsDB.Close() })
	if err := balanceFixtureSchema(context.Background(), biz, statsDB); err != nil {
		t.Fatal(err)
	}
	runtime := &balanceDetectRuntime{
		business: &businessDB{db: biz},
		statsDB:  statsDB,
		secret:   wgBalanceSecret,
		nowFunc:  func() time.Time { return time.Now().UTC() },
	}
	if upstreamURL != "" {
		runtime.client = &http.Client{Timeout: 5 * time.Second}
	}
	if withLease {
		store, err := taskruns.OpenStore(taskruns.StoreConfig{
			Mode:         taskruns.ModeSQLite,
			DatabasePath: filepath.Join(root, "leases.sqlite3"),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		runtime.leasestore = store
	}
	return runtime, biz, statsDB
}

// wgSeedDueAccount 写入一个加密凭据、到期意图的候选账户（id 可指定，
// 便于分页/去重场景构造多行）。
func wgSeedDueAccount(t *testing.T, db *sql.DB, id, baseURL string, dueOffset time.Duration) string {
	t.Helper()
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-" + id, "base_url": baseURL})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(dueOffset).Format(time.RFC3339Nano)
	if _, err := db.Exec(`
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at)
VALUES (?, 'sys-1', 'api_key', 'active', 1, ?, 0, '{}', ?, ?)
`, id, envelope, due, due); err != nil {
		t.Fatal(err)
	}
	return due
}

// TestBalanceRuntimeListDueCandidatesPagination 验证候选列表的游标推进、
// limit 截断与解密失败跳过。
func TestBalanceRuntimeListDueCandidatesPagination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"balance":"1"}`))
	}))
	defer upstream.Close()
	runtime, businessDB, _ := wgNewBalanceRuntime(t, "", false)
	wgSeedDueAccount(t, businessDB, "acc-a", upstream.URL, -time.Minute)
	wgSeedDueAccount(t, businessDB, "acc-b", upstream.URL, -2*time.Minute)
	// 解密必失败的行：明文非 JSON 且非封套 → 候选被跳过。
	due := time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339Nano)
	if _, err := businessDB.Exec(`
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at)
VALUES ('acc-bad', 'sys-1', 'api_key', 'active', 1, 'not-an-envelope', 0, '{}', ?, ?)
`, due, due); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// limit=1：一次只取一个候选，游标推进允许后续轮次取到剩余账户。
	first, err := runtime.ListDueCandidates(ctx, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("首轮必须返回 1 个候选: %v %v", first, err)
	}
	second, err := runtime.ListDueCandidates(ctx, 1)
	if err != nil {
		t.Fatalf("游标推进轮失败: %v", err)
	}
	if len(second) == 1 && second[0].ID == first[0].ID {
		t.Fatal("游标推进后不得重复返回同一候选")
	}
	// 全部取完后游标回绕，重新给出候选（进程内持续语义）。
	third, err := runtime.ListDueCandidates(ctx, 100)
	if err != nil || len(third) < 1 {
		t.Fatalf("回绕轮必须重新给出候选: %d %v", len(third), err)
	}
	// limit 越界被夹紧（<1 → 1，>100 → 100）不报错。
	if _, err := runtime.ListDueCandidates(ctx, 0); err != nil {
		t.Fatalf("limit=0 必须夹紧为 1: %v", err)
	}
	if _, err := runtime.ListDueCandidates(ctx, 500); err != nil {
		t.Fatalf("limit=500 必须夹紧为 100: %v", err)
	}
}

// TestBalanceRuntimeCommitAndEnableFences 覆盖探测意图提交/开启的围栏与
// 错误分支。
func TestBalanceRuntimeCommitAndEnableFences(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, businessDB, _ := wgNewBalanceRuntime(t, "", false)
	dueText := wgSeedDueAccount(t, businessDB, "acc-fence", upstream.URL, -time.Minute)
	ctx := context.Background()

	// 缺少 due 围栏 → 显式报错。
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-fence"}); err == nil {
		t.Fatal("缺少 due 围栏必须报错")
	}
	// due 非法 → 报错。
	badDue := "nope"
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-fence", ExpectedNextRefreshAt: &badDue}); err == nil {
		t.Fatal("非法 due 必须报错")
	}
	// 围栏不匹配（revision 漂移）→ false。
	if changed, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-fence", ExpectedConfigRevision: 99, ExpectedNextRefreshAt: &dueText,
	}); err != nil || changed {
		t.Fatalf("围栏不匹配必须返回 false: %v %v", changed, err)
	}
	// 围栏命中 → 收口意图（next=nil）。
	changed, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-fence", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &dueText,
	})
	if err != nil || !changed {
		t.Fatalf("围栏命中的收口必须成功: %v %v", changed, err)
	}
	// 已收口后同一围栏不再命中。
	if changed, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-fence", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &dueText,
	}); err != nil || changed {
		t.Fatalf("重复提交必须幂等 false: %v %v", changed, err)
	}

	enableDue := wgSeedDueAccount(t, businessDB, "acc-enable", upstream.URL, -time.Minute)
	nextRefresh := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339Nano)
	// next_refresh_at 非法 → 报错。
	if _, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "acc-enable", ExpectedConfigRevision: 1, NextRefreshAt: "bad",
	}); err == nil {
		t.Fatal("非法 next_refresh_at 必须报错")
	}
	// 带 due 围栏命中 → 开启成功。
	enabled, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID:              "acc-enable",
		ExpectedConfigRevision: 1,
		ExpectedNextRefreshAt:  &enableDue,
		Config:                 opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5},
		NextRefreshAt:          nextRefresh,
	})
	if err != nil || !enabled {
		t.Fatalf("围栏命中开启必须成功: %v %v", enabled, err)
	}
	// 不带围栏（Node ?? undefined 语义）对已开启行不再命中资格谓词 → false。
	enabled, err = runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "acc-enable", ExpectedConfigRevision: 1,
		Config: opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}, NextRefreshAt: nextRefresh,
	})
	if err != nil || enabled {
		t.Fatalf("已开启行不得重复开启: %v %v", enabled, err)
	}
}

// TestBalanceRuntimeReplaceSnapshot 覆盖快照写入的围栏、缓存丰富与
// 窄投影回退三条路径。
func TestBalanceRuntimeReplaceSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, businessDB, statsDB := wgNewBalanceRuntime(t, "", false)
	wgSeedDueAccount(t, businessDB, "acc-snap", upstream.URL, -time.Minute)
	ctx := context.Background()
	config := opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 10}
	display := 3.25

	// 账户不存在（enabled 围栏不满足）→ false, nil。
	if ok, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "missing", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1, ExpectedConfig: config,
		Snapshot: opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh},
	}); err != nil || ok {
		t.Fatalf("缺失账户必须返回 false: %v %v", ok, err)
	}
	// 快照围栏要求账户已开启且配置一致（enabled 谓词）。
	if _, err := businessDB.Exec(`UPDATE accounts SET balance_query_enabled = 1, balance_query_config_json = '{"adapter":"builtin","intervalMinutes":10}' WHERE id = 'acc-snap'`); err != nil {
		t.Fatal(err)
	}
	// next_refresh_after 非法 → 报错（config 围栏通过后才会解析）。
	if _, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-snap", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1, ExpectedConfig: config,
		Snapshot:         opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh},
		NextRefreshAfter: "bad",
	}); err == nil {
		t.Fatal("非法 next_refresh_after 必须报错")
	}
	// 无缓存 → 窄投影回退（DisplayBalance/RawStatus 进快照 JSON）。
	ok, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-snap", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1, ExpectedConfig: config,
		Snapshot: opsjobs.BalanceSnapshotWrite{
			Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1,
			DisplayBalance: &display, RawStatus: "raw-3.25",
			LastAttemptAt: "2026-09-10T11:00:00Z", LastSuccessAt: "2026-09-10T11:00:00Z",
		},
		NextRefreshAfter: nextRFC3339(t, 20*time.Minute),
	})
	if err != nil || !ok {
		t.Fatalf("快照写入必须成功: %v %v", ok, err)
	}
	var snapshotText string
	if err := statsDB.QueryRow(`SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-snap'`).Scan(&snapshotText); err != nil {
		t.Fatal(err)
	}
	if !containsAllJSONKeys(t, snapshotText, "remainingUsd", "rawRemaining", "lastSuccessAt") {
		t.Fatalf("窄投影快照缺字段: %s", snapshotText)
	}
	// detector 缓存优先：完整 J2 快照覆盖显示值并携带 errorMessage。
	runtime.detected.Store("snapshot:acc-snap", &accountbalance.Snapshot{
		RemainingUSD: "9.875000", RawRemaining: "9.875", RawUnit: "USD", Basis: "upstream_api",
		ErrorMessage: " legacy-error",
	})
	ok, err = runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-snap", SystemAccountID: "sys-1",
		ExpectedConfigRevision: 1, ExpectedConfig: config,
		Snapshot: opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1},
	})
	if err != nil || !ok {
		t.Fatalf("缓存丰富快照写入必须成功: %v %v", ok, err)
	}
	if err := statsDB.QueryRow(`SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-snap'`).Scan(&snapshotText); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rawUnit", "basis", "errorMessage", "remainingUsd"} {
		if !containsAllJSONKeys(t, snapshotText, key) {
			t.Fatalf("缓存丰富快照缺 %s: %s", key, snapshotText)
		}
	}
}

func int64Ptr(v int64) *int64 { return &v }

func nextRFC3339(t *testing.T, d time.Duration) string {
	t.Helper()
	return time.Now().UTC().Add(d).Format(time.RFC3339Nano)
}

func containsAllJSONKeys(t *testing.T, text string, keys ...string) bool {
	t.Helper()
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("快照 JSON 解析失败: %v", err)
	}
	for _, key := range keys {
		if _, ok := decoded[key]; !ok {
			return false
		}
	}
	return true
}

// TestBalanceRuntimeRunWithLease 覆盖租约获取/竞争/未初始化分支。
func TestBalanceRuntimeRunWithLease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, _, _ := wgNewBalanceRuntime(t, "", true)
	candidate := opsjobs.BalanceDetectionCandidate{ID: "acc-lease", SystemAccountID: "sys-1"}
	// 未初始化租约存储 → 显式报错。
	noLease := &balanceDetectRuntime{}
	if _, err := noLease.RunWithLease(context.Background(), candidate, func(context.Context) error { return nil }); err == nil {
		t.Fatal("租约存储未初始化必须报错")
	}
	// 首次获取 → 执行。
	acquired, err := runtime.RunWithLease(context.Background(), candidate, func(context.Context) error { return nil })
	if err != nil || !acquired {
		t.Fatalf("首次租约必须获取: %v %v", acquired, err)
	}
	// 任务错误透传。
	if _, err := runtime.RunWithLease(context.Background(), candidate, func(context.Context) error {
		return errors.New("探测失败")
	}); err == nil {
		t.Fatal("任务错误必须透传")
	}
}

// TestBalanceRuntimeQueryBuiltin 覆盖 builtin 查询的输入组装错误分支与
// 命中路径（含缓存凭据复用与代理封套）。
func TestBalanceRuntimeQueryBuiltin(t *testing.T) {
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":"4.5"}`))
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	runtime, businessDB, _ := wgNewBalanceRuntime(t, upstreamURL, false)
	ctx := context.Background()
	config := opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}

	// 账户缺失 → 读取失败。
	if _, err := runtime.QueryBuiltin(ctx, opsjobs.BalanceDetectionCandidate{ID: "missing"}, config); err == nil {
		t.Fatal("缺失账户必须报错")
	}
	dueText := wgSeedDueAccount(t, businessDB, "acc-q", upstream.URL, -time.Minute)
	// 凭据缓存为空 → cachedCredentials 走解密回退。
	candidate := opsjobs.BalanceDetectionCandidate{ID: "acc-q", SystemAccountID: "sys-1", ConfigRevision: 1, InputVersion: int64Ptr(1), NextRefreshAt: &dueText}
	result, err := runtime.QueryBuiltin(ctx, candidate, config)
	if err != nil {
		t.Fatalf("builtin 查询必须成功: %v", err)
	}
	if result.Adapter == "" || result.Snapshot.Status != opsjobs.BalanceSnapshotFresh {
		t.Fatalf("查询结果形状错误: %+v", result)
	}
	// base_url 缺失 → 显式报错。
	if _, err := businessDB.Exec(`UPDATE accounts SET credentials_encrypted = '{"api_key":"sk-x"}' WHERE id = 'acc-q'`); err != nil {
		t.Fatal(err)
	}
	runtime.detected = sync.Map{}
	if _, err := runtime.QueryBuiltin(ctx, candidate, config); err == nil {
		t.Fatal("缺 base_url 必须报错")
	}
	// 代理封套分支：socks5 → socks5h、非法类型、非法端口、密码解密失败。
	proxyCases := []struct {
		name    string
		kind    string
		host    string
		port    int64
		secrets string
		wantErr bool
	}{
		{"socks5 归一 socks5h", "socks5", "127.0.0.1", 1080, "", false},
		{"http 代理", "http", "127.0.0.1", 8080, "", false},
		{"非法类型", "gopher", "127.0.0.1", 80, "", true},
		{"非法端口", "http", "127.0.0.1", 0, "", true},
		{"空 host", "http", "  ", 8080, "", true},
		{"密码解密失败", "http", "127.0.0.1", 8080, "not-an-envelope", true},
	}
	for _, item := range proxyCases {
		t.Run(item.name, func(t *testing.T) {
			_, err := runtime.proxyEnvelope("proxy-1", item.kind, item.host, item.port, "user", item.secrets)
			if (err != nil) != item.wantErr {
				t.Fatalf("wantErr=%v, err=%v", item.wantErr, err)
			}
		})
	}
}

// TestBalanceBuildQueryInputFences 覆盖 buildQueryInput 的 due 解析与
// InputVersion 边界。
func TestBalanceBuildQueryInputFences(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer upstream.Close()
	runtime, businessDB, _ := wgNewBalanceRuntime(t, upstream.URL, false)
	wgSeedDueAccount(t, businessDB, "acc-input", upstream.URL, -time.Minute)
	ctx := context.Background()
	badDue := "not-a-time"
	candidate := opsjobs.BalanceDetectionCandidate{ID: "acc-input", SystemAccountID: "sys-1", ConfigRevision: 1, InputVersion: int64Ptr(1), NextRefreshAt: &badDue}
	if _, err := runtime.buildQueryInput(ctx, candidate, opsjobs.BalanceQueryConfig{Adapter: "builtin"}); err == nil {
		t.Fatal("非法 due 必须报错")
	}
}

// TestEnsureAccountUsageSnapshotsTableBranches 覆盖快照表校验的缺失分支。
func TestEnsureAccountUsageSnapshotsTableBranches(t *testing.T) {
	_, _, statsDB := wgNewBalanceRuntime(t, "", false)
	ctx := context.Background()
	// 表存在 → 通过。
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, false); err != nil {
		t.Fatalf("快照表存在必须通过: %v", err)
	}
	if _, err := statsDB.Exec(`DROP TABLE account_usage_snapshots`); err != nil {
		t.Fatal(err)
	}
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, false); err == nil {
		t.Fatal("缺表必须报错")
	}
}
