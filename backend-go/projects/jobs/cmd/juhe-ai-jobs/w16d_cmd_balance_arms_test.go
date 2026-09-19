// 波次 w16d：批次三。worker_balance_detect.go 的运行时方法臂（凭据解密、
// 候选扫描、意图提交、快照替换、租约、内建查询）与组合根装配失败臂
// （buildWorkerAssembly / wireFamilies 错误传播、openSQLite/家族打开失败）。
// 全部进程内 SQLite fixture，不依赖 PG。数据一律 w16d- 前缀。
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "modernc.org/sqlite"
)

// w16dBalanceFixture 是余额探测运行时的完整 SQLite fixture：业务库具备
// balance 契约列与 proxy_profiles，统计库具备 account_usage_snapshots
// （withSnapshots=false 时省略以触发 fail closed 臂）。
type w16dBalanceFixture struct {
	business *sql.DB
	stats    *sql.DB
	runtime  *balanceDetectRuntime
}

func w16dNewBalanceFixture(t *testing.T, withSnapshots bool) *w16dBalanceFixture {
	t.Helper()
	root := t.TempDir()
	businessPath := root + "/business.sqlite3"
	seedProbeCoreTables(t, businessPath)
	business := w16dOpenTestSQLite(t, businessPath)
	for _, statement := range []string{
		`ALTER TABLE accounts ADD COLUMN balance_query_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE accounts ADD COLUMN balance_query_config_json TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE accounts ADD COLUMN balance_query_next_refresh_at TEXT`,
		`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`,
		`CREATE TABLE IF NOT EXISTS proxy_profiles (
			id TEXT PRIMARY KEY, type TEXT, host TEXT, port INTEGER,
			username TEXT, password_encrypted TEXT)`,
	} {
		if _, err := business.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	statsPath := root + "/stats.sqlite3"
	stats := w16dOpenTestSQLite(t, statsPath)
	if withSnapshots {
		if _, err := stats.Exec(`CREATE TABLE account_usage_snapshots (
			system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, kind TEXT NOT NULL,
			source TEXT, snapshot_json TEXT, refresh_status TEXT,
			last_attempt_at TEXT, last_success_at TEXT, next_refresh_after TEXT,
			last_error_message TEXT, updated_at TEXT NOT NULL, created_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, account_id, kind))`); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &balanceDetectRuntime{
		business: &businessDB{db: business},
		statsDB:  stats,
		secret:   "0123456789abcdef0123456789abcdef",
		nowFunc: func() time.Time {
			return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
		},
	}
	t.Cleanup(func() {
		_ = business.Close()
		_ = stats.Close()
	})
	return &w16dBalanceFixture{business: business, stats: stats, runtime: runtime}
}

// w16dInsertDueAccount 写入一条符合 first-detect 资格谓词的候选账户。
func (f *w16dBalanceFixture) insertDueAccount(t *testing.T, id string, dispatch any, due any, credentials string) {
	t.Helper()
	if _, err := f.business.Exec(`INSERT INTO accounts
		(id, system_account_id, name, type, status, schedulable, credentials_encrypted,
		 config_revision, dispatch_revision, deleted_at, balance_query_enabled,
		 balance_query_config_json, balance_query_next_refresh_at)
		VALUES (?, 'sys-w16d', ?, 'api_key', 'active', 1, ?, 1, ?, NULL, 0, '{}', ?)`,
		id, id, credentials, dispatch, due); err != nil {
		t.Fatal(err)
	}
}

func TestW16DBalanceDecryptCredentialsArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, true)
	// 明文 JSON + api_keys → 通过。
	plaintext := `{"api_keys":["sk-w16d"],"base_url":"http://127.0.0.1:1"}`
	if credentials, ok := fixture.runtime.decryptCredentials(plaintext); !ok || credentials["base_url"] != "http://127.0.0.1:1" {
		t.Fatalf("明文 JSON 必须通过: %v %v", credentials, ok)
	}
	// 非明文且解密失败 → false（既有分支）。
	if _, ok := fixture.runtime.decryptCredentials("w16d-not-an-envelope"); ok {
		t.Fatal("垃圾封套必须拒绝")
	}
	// 封套合法但明文非 JSON → 197-199。
	sealed, err := accountbalance.EncryptV1Envelope(fixture.runtime.secret, []byte("w16d-not-json-plain"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fixture.runtime.decryptCredentials(sealed); ok {
		t.Fatal("解密后非 JSON 必须拒绝")
	}
	// 封套合法、明文 JSON 但无 API Key → 200-202。
	sealedNoKey, err := accountbalance.EncryptV1Envelope(fixture.runtime.secret, []byte(`{"base_url":"http://x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fixture.runtime.decryptCredentials(sealedNoKey); ok {
		t.Fatal("无 API Key 凭据必须拒绝")
	}
	// 明文 JSON 无 API Key → false（既有分支）。
	if _, ok := fixture.runtime.decryptCredentials(`{"base_url":"http://x"}`); ok {
		t.Fatal("明文无 Key 必须拒绝")
	}
}

func TestW16DBalanceListDueCandidatesArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, true)
	now := fixture.runtime.nowFunc()
	due := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	// scan 错误臂：dispatch_revision 存文本（INTEGER 亲和列存 TEXT 合法）。
	fixture.insertDueAccount(t, "w16d-scan-bad", "not-int", due, `{"api_key":"sk-x"}`)
	if _, err := fixture.runtime.ListDueCandidates(context.Background(), 5); err == nil {
		t.Fatal("dispatch_revision 文本必须触发 scan 错误")
	}
	if _, err := fixture.business.Exec(`DELETE FROM accounts WHERE id = 'w16d-scan-bad'`); err != nil {
		t.Fatal(err)
	}
	// scanNullTime 错误臂：due 列存整数。
	fixture.insertDueAccount(t, "w16d-due-int", int64(2), 12345, `{"api_key":"sk-x"}`)
	if _, err := fixture.runtime.ListDueCandidates(context.Background(), 5); err == nil {
		t.Fatal("整数 due 必须触发 scanNullTime 错误")
	}
	if _, err := fixture.business.Exec(`DELETE FROM accounts WHERE id = 'w16d-due-int'`); err != nil {
		t.Fatal(err)
	}
	// 正常路径：解密通过 → 候选入选。
	fixture.insertDueAccount(t, "w16d-ok", int64(2), due, `{"api_key":"sk-ok","base_url":"http://127.0.0.1:1"}`)
	fixture.insertDueAccount(t, "w16d-bad-cred", int64(3), due, `{"base_url":"http://127.0.0.1:1"}`)
	selected, err := fixture.runtime.ListDueCandidates(context.Background(), 5)
	if err != nil {
		t.Fatalf("正常扫描: %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "w16d-ok" {
		t.Fatalf("候选结果错误: %+v", selected)
	}
	// 游标耗尽回绕后重复扫描：行未被 Commit，仍是 due → 再次入选。
	again, err := fixture.runtime.ListDueCandidates(context.Background(), 5)
	if err != nil {
		t.Fatalf("回绕扫描: %v", err)
	}
	if len(again) != 1 || again[0].ID != "w16d-ok" {
		t.Fatalf("未提交的候选必须仍在扫描窗口内: %+v", again)
	}
	// 关闭句柄错误臂。
	closedPath := filepath.Join(t.TempDir(), "closed.sqlite3")
	closed, err := sql.Open("sqlite", closedPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	broken := &balanceDetectRuntime{business: &businessDB{db: closed}, nowFunc: fixture.runtime.nowFunc}
	if _, err := broken.ListDueCandidates(context.Background(), 5); err == nil {
		t.Fatal("关闭句柄扫描必须报错")
	}
}

func TestW16DBalanceCommitAndEnableArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, true)
	ctx := context.Background()
	// 缺 due 围栏。
	if _, err := fixture.runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{}); err == nil {
		t.Fatal("缺 due 围栏必须报错")
	}
	// NextRefreshAt 合法（361 分支）+ 围栏不匹配 → false。
	badFence := "2026-01-01T00:00:00Z"
	committed, err := fixture.runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID:              "w16d-missing",
		ExpectedNextRefreshAt:  &badFence,
		NextRefreshAt:          &badFence,
		ExpectedConfigRevision: 1,
	})
	if err != nil || committed {
		t.Fatalf("不匹配围栏必须 false: %v %v", committed, err)
	}
	// 非法 NextRefreshAt → 解析错误。
	bogus := "not-a-time"
	if _, err := fixture.runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "w16d-missing", ExpectedNextRefreshAt: &badFence, NextRefreshAt: &bogus,
		ExpectedConfigRevision: 1,
	}); err == nil {
		t.Fatal("非法 next 必须报错")
	}
	// 关闭句柄 → exec 错误。
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	broken := &balanceDetectRuntime{business: &businessDB{db: closed}, nowFunc: fixture.runtime.nowFunc}
	if _, err := broken.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "w16d-x", ExpectedNextRefreshAt: &badFence, ExpectedConfigRevision: 1,
	}); err == nil {
		t.Fatal("关闭句柄提交必须报错")
	}

	// EnableDetectedQuery：非法围栏 / 关闭句柄。
	if _, err := broken.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "w16d-x", NextRefreshAt: badFence, Config: opsjobs.BalanceQueryConfig{Adapter: "builtin"},
		ExpectedConfigRevision: 1,
	}); err == nil {
		t.Fatal("关闭句柄启用必须报错")
	}
	if _, err := fixture.runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "w16d-x", NextRefreshAt: badFence, Config: opsjobs.BalanceQueryConfig{Adapter: "builtin"},
		ExpectedNextRefreshAt: &bogus, ExpectedConfigRevision: 1,
	}); err == nil {
		t.Fatal("非法围栏必须报错")
	}
}

func TestW16DBalanceReplaceSnapshotArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, false) // stats 无快照表 → exec 错误臂
	ctx := context.Background()
	now := fixture.runtime.nowFunc()
	due := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	fixture.insertDueAccount(t, "w16d-snap", int64(2), due, `{"api_key":"sk-ok","base_url":"http://127.0.0.1:1"}`)
	// 候选未启用查询（balance_query_enabled=0）→ 查无行 → false。
	committed, err := fixture.runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "w16d-snap", SystemAccountID: "sys-w16d",
		ExpectedConfig: opsjobs.BalanceQueryConfig{Adapter: "builtin"}, ExpectedConfigRevision: 1,
		Snapshot: opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotStatus("fresh")},
	})
	if err != nil || committed {
		t.Fatalf("未启用账户必须 false: %v %v", committed, err)
	}
	// 启用查询并写入等价配置 → 配置一致 → stats 缺表 → 写入失败错误臂。
	next := now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := fixture.business.Exec(`UPDATE accounts SET balance_query_enabled = 1,
		balance_query_config_json = '{"adapter":"builtin","intervalMinutes":5}',
		balance_query_next_refresh_at = ? WHERE id = 'w16d-snap'`, next); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "w16d-snap", SystemAccountID: "sys-w16d",
		ExpectedConfig:         opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5},
		ExpectedConfigRevision: 1, NextRefreshAfter: next,
		Snapshot: opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotStatus("fresh"), ConfigRevision: 1},
	}); err == nil {
		t.Fatal("stats 缺快照表必须报错")
	}
}

func TestW16DBalanceRunWithLeaseArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, true)
	ctx := context.Background()
	// leasestore 缺失。
	if _, err := fixture.runtime.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "w16d-x"}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("缺租约存储必须报错")
	}
	// 关闭的租约存储 → AcquireLease 错误。
	closedLease, err := taskruns.OpenStore(taskruns.StoreConfig{
		Mode:         taskruns.ModeSQLite,
		DatabasePath: filepath.Join(t.TempDir(), "closed-lease.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := closedLease.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := closedLease.Close(); err != nil {
		t.Fatal(err)
	}
	leaseStore := w16dOpenTaskRunsStore(t)
	runtimeWithLease := &balanceDetectRuntime{business: fixture.runtime.business, statsDB: fixture.runtime.statsDB,
		secret: fixture.runtime.secret, nowFunc: fixture.runtime.nowFunc, leasestore: leaseStore}
	closedLeaseRuntime := &balanceDetectRuntime{business: fixture.runtime.business, statsDB: fixture.runtime.statsDB,
		secret: fixture.runtime.secret, nowFunc: fixture.runtime.nowFunc, leasestore: closedLease}
	if _, err := closedLeaseRuntime.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "w16d-x"}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("关闭租约存储必须报错")
	}
	// 他人持有未过期租约 → !acquired → (false, nil)。
	now := fixture.runtime.nowFunc().UTC()
	acquired, err := leaseStore.AcquireLease(ctx, taskruns.LeaseAcquireInput{
		LeaseKey: "account-balance:w16d-held", JobName: "account-balance-refresh",
		ShardKey: "sys-w16d", OwnerID: "w16d-other-owner", RunID: "w16d-run",
		LeaseUntil: now.Add(time.Minute), Now: &now,
	})
	if err != nil || !acquired {
		t.Fatalf("预占租约: %v %v", acquired, err)
	}
	held, err := runtimeWithLease.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "w16d-held", SystemAccountID: "sys-w16d"},
		func(context.Context) error { return nil })
	if err != nil || held {
		t.Fatalf("他人持锁必须 false,nil: %v %v", held, err)
	}
	// 无竞争 → 获取并执行。
	ran := false
	ok, err := runtimeWithLease.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "w16d-free", SystemAccountID: "sys-w16d"},
		func(context.Context) error { ran = true; return nil })
	if err != nil || !ok || !ran {
		t.Fatalf("无竞争租约必须执行: %v %v ran=%v", ok, err, ran)
	}
	// 任务失败透传。
	if _, err := runtimeWithLease.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "w16d-err", SystemAccountID: "sys-w16d"},
		func(context.Context) error { return context.DeadlineExceeded }); err == nil {
		t.Fatal("任务错误必须透传")
	}
}

// w16dOpenTaskRunsStore 打开独立 taskruns 租约存储。
func w16dOpenTaskRunsStore(t *testing.T) *taskruns.Store {
	t.Helper()
	store, err := taskruns.OpenStore(taskruns.StoreConfig{
		Mode:         taskruns.ModeSQLite,
		DatabasePath: filepath.Join(t.TempDir(), "task-runs.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestW16DBalanceQueryBuiltinArms(t *testing.T) {
	fixture := w16dNewBalanceFixture(t, true)
	ctx := context.Background()
	due := fixture.runtime.nowFunc().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	// 候选凭据无 base_url → buildQueryInput 错误。
	fixture.insertDueAccount(t, "w16d-nobase", int64(2), due, `{"api_key":"sk-ok"}`)
	selected, err := fixture.runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(selected) != 1 {
		t.Fatalf("候选准备: %v %v", selected, err)
	}
	if _, err := fixture.runtime.QueryBuiltin(ctx, selected[0], opsjobs.BalanceQueryConfig{Adapter: "builtin"}); err == nil {
		t.Fatal("缺 base_url 必须报错")
	}
	// 代理 envelope 错误：候选携带 proxy_profile_id，代理解析失败（713-715）。
	if _, err := fixture.business.Exec(`DELETE FROM accounts WHERE id = 'w16d-nobase'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.business.Exec(`INSERT INTO proxy_profiles (id, type, host, port) VALUES ('w16d-proxy-bad', 'ftp', '127.0.0.1', 1080)`); err != nil {
		t.Fatal(err)
	}
	fixture.insertDueAccount(t, "w16d-proxy-bad", int64(3), due, `{"api_key":"sk-ok","base_url":"http://127.0.0.1:1"}`)
	if _, err := fixture.business.Exec(`UPDATE accounts SET proxy_profile_id = 'w16d-proxy-bad' WHERE id = 'w16d-proxy-bad'`); err != nil {
		t.Fatal(err)
	}
	selected, err = fixture.runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(selected) != 1 {
		t.Fatalf("代理候选准备: %v %v", selected, err)
	}
	if _, err := fixture.runtime.QueryBuiltin(ctx, selected[0], opsjobs.BalanceQueryConfig{Adapter: "builtin"}); err == nil {
		t.Fatal("非法代理类型必须报错")
	}
	// 上游不可达 → ExecuteBalanceQuery 错误（630-632）。先清掉上一候选避免混入。
	if _, err := fixture.business.Exec(`DELETE FROM accounts WHERE id = 'w16d-proxy-bad'`); err != nil {
		t.Fatal(err)
	}
	fixture.insertDueAccount(t, "w16d-unreachable", int64(4), due, `{"api_key":"sk-ok","base_url":"http://127.0.0.1:1"}`)
	selected, err = fixture.runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(selected) != 1 {
		t.Fatalf("不可达候选准备: %v %v", selected, err)
	}
	if _, err := fixture.runtime.QueryBuiltin(ctx, selected[0], opsjobs.BalanceQueryConfig{Adapter: "builtin"}); err == nil {
		t.Fatal("上游不可达必须报错")
	}
	// proxyEnvelope 直测：非法类型 / 非法地址 / 密码解密失败。
	if _, err := fixture.runtime.proxyEnvelope("p", "ftp", "127.0.0.1", 1080, "", ""); err == nil {
		t.Fatal("非法代理类型必须报错")
	}
	if _, err := fixture.runtime.proxyEnvelope("p", "http", "", 1080, "", ""); err == nil {
		t.Fatal("空 host 必须报错")
	}
	if _, err := fixture.runtime.proxyEnvelope("p", "socks5", "127.0.0.1", 1080, "user", "w16d-garbage"); err == nil {
		t.Fatal("垃圾密码封套必须报错")
	}
	// socks5 映射 socks5h + 用户名密码（成功封套）。
	envelope, err := fixture.runtime.proxyEnvelope("p", "socks5", "127.0.0.1", 1080, "user", "")
	if err != nil || envelope == nil {
		t.Fatalf("socks5 封套: %v", err)
	}
}

func TestW16DBalanceEnsureSnapshotsTableArms(t *testing.T) {
	ctx := context.Background()
	fixture := w16dNewBalanceFixture(t, false)
	if err := ensureAccountUsageSnapshotsTable(ctx, fixture.stats, false); err == nil {
		t.Fatal("缺快照表必须报错")
	}
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := ensureAccountUsageSnapshotsTable(ctx, closed, false); err == nil {
		t.Fatal("关闭句柄必须报错")
	}
}

// ---- 组合根装配失败臂 ----

// w16dSQLiteAssemblyConfig 构造一个路径齐备的 SQLite worker 配置基座。
func w16dSQLiteAssemblyConfig(t *testing.T, mutate func(*workerConfig)) workerConfig {
	root := t.TempDir()
	config := workerConfig{
		Driver: "sqlite", InstanceID: "w16d-assembly",
		WorkerRole:                  "worker",
		Secret:                      "0123456789abcdef0123456789abcdef",
		BusinessSQLitePath:          filepath.Join(root, "business.sqlite3"),
		StatsSQLitePath:             filepath.Join(root, "stats.sqlite3"),
		TaskRunsSQLitePath:          filepath.Join(root, "task-runs.sqlite3"),
		UsageCatalogSQLitePath:      filepath.Join(root, "usage-catalog.sqlite3"),
		UsageShardRoot:              filepath.Join(root, "usage-shards"),
		DatasetSQLitePath:           filepath.Join(root, "dataset.sqlite3"),
		ChatSQLitePath:              filepath.Join(root, "chat.sqlite3"),
		CodexContextStateShardRoot:  filepath.Join(root, "codex-state"),
		CodexContextStateShardCount: 4,
		ChatAssetsRoot:              filepath.Join(root, "chat-assets"),
		StatsEnabled:                true,
		OAuthEnabled:                true,
		TaskRunsEnabled:             true,
		UsageWriterEnabled:          true,
		BalanceDetectEnabled:        false,
		RetentionEnabled:            true,
		ProbeEnabled:                true,
	}
	if mutate != nil {
		mutate(&config)
	}
	return config
}

func TestW16DBuildWorkerAssemblyFailArms(t *testing.T) {
	writeGarbage := func(name string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("w16d not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// usage-catalog 垃圾文件 → openSQLite WAL 失败（101-103）。
	garbageConfig := w16dSQLiteAssemblyConfig(t, func(config *workerConfig) {
		config.UsageCatalogSQLitePath = writeGarbage("garbage-catalog.sqlite3")
	})
	if assembly, err := buildWorkerAssembly(garbageConfig, slog.Default()); err == nil {
		if assembly != nil {
			assembly.closeStores()
		}
		t.Fatal("垃圾 usage-catalog 必须使装配失败")
	}
	// task-runs 垃圾文件 → OpenStore 失败（263-265）。
	taskRunsConfig := w16dSQLiteAssemblyConfig(t, func(config *workerConfig) {
		config.TaskRunsSQLitePath = writeGarbage("garbage-taskruns.sqlite3")
	})
	if assembly, err := buildWorkerAssembly(taskRunsConfig, slog.Default()); err == nil {
		if assembly != nil {
			assembly.closeStores()
		}
		t.Fatal("垃圾 task-runs 必须使装配失败")
	}
	// OAuth 开启且 secret 为空 → OpenStore 失败（575-577）并经 wireFamilies 传播（197-199）。
	oauthConfig := w16dSQLiteAssemblyConfig(t, func(config *workerConfig) {
		config.Secret = ""
		config.ProbeEnabled = false
		config.BalanceDetectEnabled = false
	})
	if assembly, err := buildWorkerAssembly(oauthConfig, slog.Default()); err == nil {
		if assembly != nil {
			assembly.closeStores()
		}
		t.Fatal("空 secret 必须使 OAuth 族装配失败")
	}
	// 探针族 secret 为空 → NewStore 失败（42-45）并传播（209-211）。
	probeConfig := w16dSQLiteAssemblyConfig(t, func(config *workerConfig) {
		config.Secret = ""
		config.OAuthEnabled = false
	})
	if assembly, err := buildWorkerAssembly(probeConfig, slog.Default()); err == nil {
		if assembly != nil {
			assembly.closeStores()
		}
		t.Fatal("空 secret 必须使探针族装配失败")
	}
}

func TestW16DWireBalanceDetectFamilyArms(t *testing.T) {
	ctx := context.Background()
	// openBusinessDB 失败臂（postgres 坏 URL；791-793）。需先过 secret 与
	// 租约存储门禁才能触达业务库打开。
	pgAssembly := newWorkerAssembly(workerConfig{
		Driver: "postgres", PostgresURL: "pgx://w16d-invalid",
		BalanceDetectEnabled: true, Secret: "0123456789abcdef0123456789abcdef",
	}, slog.Default())
	pgAssembly.taskRunsStore = w16dOpenTaskRunsStore(t)
	if err := pgAssembly.wireBalanceDetectFamily(ctx); err == nil {
		t.Fatal("postgres 坏 URL 必须使 balance 族失败")
	}
	pgAssembly.closeStores()

	// 快照表缺失臂：业务库具备契约列但 stats 无 account_usage_snapshots（807-811）。
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	businessDBFile := w16dOpenTestSQLite(t, businessPath)
	for _, statement := range []string{
		`ALTER TABLE accounts ADD COLUMN balance_query_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE accounts ADD COLUMN balance_query_config_json TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE accounts ADD COLUMN balance_query_next_refresh_at TEXT`,
		`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`,
		`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, type TEXT, host TEXT, port INTEGER, username TEXT, password_encrypted TEXT)`,
	} {
		if _, err := businessDBFile.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	assembly := newWorkerAssembly(workerConfig{
		Driver: "sqlite", InstanceID: "w16d-balance",
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    filepath.Join(root, "stats-no-snapshots.sqlite3"),
		TaskRunsSQLitePath: filepath.Join(root, "task-runs.sqlite3"),
		Secret:             "0123456789abcdef0123456789abcdef",
		TaskRunsEnabled:    true,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireTaskRunsFamily(ctx); err != nil {
		t.Fatalf("task-runs 装配: %v", err)
	}
	if err := assembly.wireBalanceDetectFamily(ctx); err != nil {
		t.Fatalf("balance 族装配: %v", err)
	}
	found := false
	for _, disabled := range assembly.disabledJobs {
		if disabled.JobName == "account-balance-auto-detect-recovery" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺快照表必须登记 disabled: %v", assembly.disabledJobs)
	}
}
