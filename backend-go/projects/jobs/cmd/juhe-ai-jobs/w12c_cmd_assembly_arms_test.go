// 波次 w12c：补齐电路恢复 resolver、余额探测运行态与配置/投影装配分支。
// 全部 SQLite fixture + httptest 上游，进程内确定性。
//
// 不可达/防御守卫登记（无自然触发路径，不做无语义强注入）：
//   - worker_balance_detect.go balanceConfigJSONEqual 的 normalize 错误分支：
//     归一化输入来自已解码 JSON 的字符串/数字，json.Marshal 恒成功。
//   - worker_balance_detect.go EnableDetectedQuery 的配置归一化错误分支：
//     同上，normalizeBalanceConfigJSON 恒成功。
//
// 依赖壁垒登记（本波未达 95% 的主因，非不可达但缺注入点）：
//   - main.go main()（47.9%）：J1 PostgreSQL 直连输入、model-recovery、J2
//     account-balance、J3a 三库装配的成功分支要求共享覆盖库存在生产形状的
//     juhe_business/juhe_jobs schema 与网关 Redis 键空间；w1cover 覆盖库不
//     承载该 schema（并发代理共享），子进程 e2e 在 fail-closed 下无法进入
//     这些分支。fail() 的 os.Exit 路径无进程内注入点（wg TestMain 协议场景
//     固定，本波不改既有测试文件），仅能以子进程退出码观察。
//   - main.go jobsHTTPHandler（41.3%）：/account-balance/manual 桥的
//     RunManual 成功/租约冲突/stale 分支需要可运行的 J2 服务存储面。
//   - worker_probe_jobs.go speedFirstProbeFunc（31.8%）：完整分级诊断需要
//     accountprobe 上游协议栈的真实响应（协议行为由 platform 包测试锁定）。
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// ---------------------------------------------------------------------------
// 电路恢复 resolver
// ---------------------------------------------------------------------------

func TestW12CCircuitRecoveryResolverArms(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	resolver := circuitRecoveryTargetResolver{store: store, probe: w9hProbeService(t, store)}
	ctx := context.Background()

	// ctx 已取消 → 错误上抛。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := resolver.Resolve(cancelled, opsjobs.CircuitState{}); err == nil {
		t.Fatal("取消的 ctx 必须上抛")
	}

	// 非法 runtime key → 未找到。
	if _, found, err := resolver.Resolve(ctx, w12cStateWithRuntimeKey("::bad::")); err != nil || found {
		t.Fatalf("非法 runtime key 必须 found=false: %v %v", found, err)
	}
	// 账户缺失 → 未找到。
	if _, found, err := resolver.Resolve(ctx, w12cStateWithRuntimeKey("acc-w12c-absent")); err != nil || found {
		t.Fatalf("缺失账户必须 found=false: %v %v", found, err)
	}
	// owner 命中：target 携带 dispatch revision 与探针闭包。
	target, found, err := resolver.Resolve(ctx, w12cStateWithRuntimeKey(runtimeKey))
	if err != nil || !found || target.DispatchRevision == "" || target.Probe == nil {
		t.Fatalf("owner 命中: found=%v target=%v err=%v", found, target, err)
	}

	// authorized 身份：group/system 来自 identity（fixture 绑定 g-rt 命中）。
	authorizedKey := "acc-rt:authorized:sys-rt:g-rt:auth-1"
	authorizedTarget, found, err := resolver.Resolve(ctx, w12cStateWithRuntimeKey(authorizedKey))
	if err != nil || !found || authorizedTarget.DispatchRevision != "2" {
		t.Fatalf("authorized 命中: found=%v target=%v err=%v", found, authorizedTarget, err)
	}

	// 探针闭包：ctx 取消 → canceled 分类。
	outcome, err := target.Probe(cancelled)
	if err != nil || outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureCanceled {
		t.Fatalf("取消探针=%+v err=%v", outcome, err)
	}
	// 探针闭包：诊断对不可达上游发起请求 → 传输不完整（connection）分类。
	outcome, err = target.Probe(ctx)
	if err != nil || outcome.Kind != opsjobs.ProbeOutcomeTransportIncomplete || outcome.FailureKind != opsjobs.ProbeFailureConnection {
		t.Fatalf("不可达上游探针=%+v err=%v", outcome, err)
	}
}

func w12cStateWithRuntimeKey(runtimeKey string) opsjobs.CircuitState {
	return opsjobs.CircuitState{
		Scope: opsjobs.CircuitScope{Kind: opsjobs.CircuitScopeAccount, AccountRuntimeKey: runtimeKey},
	}
}

// ---------------------------------------------------------------------------
// speedFirstCandidateSource 的时间与可用性透传
// ---------------------------------------------------------------------------

func TestW12CSpeedFirstCandidateSourceExpiryArms(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	source := speedFirstCandidateSource{store: store}
	ctx := context.Background()

	// 合法 RFC3339 到期时间 → ExpiresAtMS 解析。
	if _, err := handle.db.Exec(`UPDATE accounts SET account_expires_at = '2030-01-01T00:00:00Z' WHERE id = 'acc-rt'`); err != nil {
		t.Fatal(err)
	}
	summary, err := source.FindAccountForTest(ctx, "acc-rt", runtimeKey)
	if err != nil || summary == nil || summary.ExpiresAtMS == nil || *summary.ExpiresAtMS <= 0 {
		t.Fatalf("到期解析: %+v %v", summary, err)
	}
	// 非法时间戳经 proberepo panic 防线之外的错误通道不可达（w9h 已锁定
	// panic 行为）；这里补充 effective availability 透传。
	if _, err := handle.db.Exec(`UPDATE accounts SET account_expires_at = '2030-01-01T00:00:00Z', status = 'active', schedulable = 1 WHERE id = 'acc-rt'`); err != nil {
		t.Fatal(err)
	}
	summary, err = source.FindAccountForTest(ctx, "acc-rt", runtimeKey)
	if err != nil || summary == nil || summary.Status != "active" {
		t.Fatalf("透传: %+v %v", summary, err)
	}
}

// ---------------------------------------------------------------------------
// 余额探测运行态补充
// ---------------------------------------------------------------------------

func TestW12CBalanceConfigHelpers(t *testing.T) {
	// balanceConfigJSONEqual：损坏 JSON / 非 object / 归一化等价。
	if balanceConfigJSONEqual("not-json", "{}") {
		t.Fatal("损坏 JSON 必须不等")
	}
	if balanceConfigJSONEqual(`[1,2]`, "{}") {
		t.Fatal("非 object 必须不等")
	}
	normalized, err := normalizeBalanceConfigJSON("builtin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !balanceConfigJSONEqual(`{"adapter":"builtin"}`, normalized) {
		t.Fatal("缺省 intervalMinutes 应归一为 5")
	}
	// parseBalanceInstant。
	if _, err := parseBalanceInstant("not-a-time"); err == nil {
		t.Fatal("非法时间必须拒绝")
	}
	// decryptCredentials：明文 JSON 数组键 / 空键 / 信封解密。
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	if credentials, ok := runtime.decryptCredentials(`{"api_keys":["sk-a",""]}`); !ok || len(credentials) != 1 {
		t.Fatalf("api_keys 数组应可用: %v %v", credentials, ok)
	}
	if _, ok := runtime.decryptCredentials(`{"api_key": "   "}`); ok {
		t.Fatal("空白 api_key 必须拒绝")
	}
	if _, ok := runtime.decryptCredentials(`{}`); ok {
		t.Fatal("无键凭据必须拒绝")
	}
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-w12c"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted, ok := runtime.decryptCredentials(envelope); !ok || decrypted["api_key"] != "sk-w12c" {
		t.Fatalf("v1 信封解密: %v %v", decrypted, ok)
	}
	if _, ok := runtime.decryptCredentials("v1:not-a-valid-envelope"); ok {
		t.Fatal("非法信封必须拒绝")
	}
	// cachedCredentials：缓存命中与回源。
	runtime.detected.Store("credentials:cached", map[string]any{"api_key": "sk-cached"})
	if got := runtime.cachedCredentials("cached", `{"api_key":"sk-x"}`); got["api_key"] != "sk-cached" {
		t.Fatalf("缓存应优先: %v", got)
	}
	if got := runtime.cachedCredentials("missing", envelope); got == nil || got["api_key"] != "sk-w12c" {
		t.Fatalf("回源解密: %v", got)
	}
	if got := runtime.cachedCredentials("bad", `{}`); got != nil {
		t.Fatalf("不可解密应返回 nil: %v", got)
	}
	// proxyEnvelope。
	if _, err := runtime.proxyEnvelope("p1", "ftp", "127.0.0.1", 1080, "", ""); err == nil {
		t.Fatal("不支持的 proxy 类型必须拒绝")
	}
	if _, err := runtime.proxyEnvelope("p1", "http", "", 8080, "", ""); err == nil {
		t.Fatal("空 host 必须拒绝")
	}
	if _, err := runtime.proxyEnvelope("p1", "http", "127.0.0.1", 0, "", ""); err == nil {
		t.Fatal("非法端口必须拒绝")
	}
	if _, err := runtime.proxyEnvelope("p1", "http", "127.0.0.1", 8080, "user", "v1:broken"); err == nil {
		t.Fatal("密码解密失败必须暴露")
	}
	withUser, err := runtime.proxyEnvelope("p1", "socks5", "127.0.0.1", 1080, "user", "")
	if err != nil || withUser == nil {
		t.Fatalf("socks5h 代理信封: %v %v", withUser, err)
	}
	anonymous, err := runtime.proxyEnvelope("p1", "https", "127.0.0.1", 8443, "", "")
	if err != nil || anonymous == nil {
		t.Fatalf("匿名 https 代理信封: %v %v", anonymous, err)
	}
}

func TestW12CBalanceRuntimeCandidateArms(t *testing.T) {
	runtime, biz, statsDB := wgNewBalanceRuntime(t, "", false)
	ctx := context.Background()

	// ListDueCandidates：limit 钳制与空库回绕。
	candidates, err := runtime.ListDueCandidates(ctx, 500)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("空库扫描: %d %v", len(candidates), err)
	}
	wgSeedDueAccount(t, biz, "acc-due-1", "http://127.0.0.1:1", -time.Minute)
	candidates, err = runtime.ListDueCandidates(ctx, 0)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("到期候选: %d %v", len(candidates), err)
	}
	// 不可解密凭据的候选被跳过但仍推进游标。
	if _, err := biz.Exec(`UPDATE accounts SET credentials_encrypted = '{}' WHERE id = 'acc-due-1'`); err != nil {
		t.Fatal(err)
	}
	runtime.cursor = nil
	candidates, err = runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("凭据不可用候选应跳过: %d %v", len(candidates), err)
	}

	// CommitDetectionDue：缺围栏 / 非法时间 / 不匹配。
	due := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-due-1"}); err == nil {
		t.Fatal("缺 due 围栏必须拒绝")
	}
	badTime := "not-a-time"
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-due-1", ExpectedNextRefreshAt: &badTime}); err == nil {
		t.Fatal("非法到期时间必须拒绝")
	}
	matched, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-due-1", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &due,
	})
	if err != nil || matched {
		t.Fatalf("配置不一致应 false: %v %v", matched, err)
	}

	// ReplaceSnapshotIfCurrent：账户缺失 false；非法 nextRefreshAfter 错误。
	written, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{AccountID: "acc-absent", SystemAccountID: "sys-1"})
	if err != nil || written {
		t.Fatalf("缺失账户应 false: %v %v", written, err)
	}
	// 启用候选（balance_query_enabled=1）后配置不匹配 → false。
	enabledConfig, err := normalizeBalanceConfigJSON("builtin", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := biz.Exec(`UPDATE accounts SET balance_query_enabled = 1, balance_query_config_json = ?, balance_query_next_refresh_at = NULL WHERE id = 'acc-due-1'`, enabledConfig); err != nil {
		t.Fatal(err)
	}
	matchedConfig := opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}
	written, err = runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-due-1", SystemAccountID: "sys-1", ExpectedConfigRevision: 1,
		ExpectedConfig: matchedConfig, NextRefreshAfter: time.Now().UTC().Format(time.RFC3339Nano),
		Snapshot: opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1, LastAttemptAt: time.Now().UTC().Format(time.RFC3339Nano)},
	})
	if err != nil || !written {
		t.Fatalf("配置一致应写入: %v %v", written, err)
	}
	badAfter := "not-a-time"
	if _, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-due-1", SystemAccountID: "sys-1", ExpectedConfigRevision: 1, ExpectedConfig: matchedConfig,
		NextRefreshAfter: badAfter,
		Snapshot:         opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1},
	}); err == nil {
		t.Fatal("非法 nextRefreshAfter 必须拒绝")
	}
	// 配置不一致 → false。
	written, err = runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{
		AccountID: "acc-due-1", SystemAccountID: "sys-1", ExpectedConfigRevision: 1,
		ExpectedConfig: opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 30},
		Snapshot:       opsjobs.BalanceSnapshotWrite{Status: opsjobs.BalanceSnapshotFresh, ConfigRevision: 1},
	})
	if err != nil || written {
		t.Fatalf("配置不一致应 false: %v %v", written, err)
	}

	// RunWithLease：未初始化租约存储必须报错。
	if _, err := runtime.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-due-1"}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("缺租约存储必须报错")
	}
	leaseStore, err := taskruns.OpenStore(taskruns.StoreConfig{
		Mode:         taskruns.ModeSQLite,
		DatabasePath: filepath.Join(t.TempDir(), "w12c-leases.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leaseStore.Close() })
	if err := leaseStore.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.leasestore = leaseStore
	ran := false
	acquired, err := runtime.RunWithLease(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-due-1", SystemAccountID: "sys-1"}, func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !acquired || !ran {
		t.Fatalf("租约执行: %v %v %v", acquired, ran, err)
	}
	_ = statsDB
}

func TestW12CBalanceBuildQueryInputArms(t *testing.T) {
	runtime, biz, _ := wgNewBalanceRuntime(t, "", false)
	ctx := context.Background()
	// 账户缺失。
	if _, err := runtime.buildQueryInput(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-absent"}, opsjobs.BalanceQueryConfig{}); err == nil {
		t.Fatal("缺失账户必须拒绝")
	}
	// 凭据不可解密。
	wgSeedDueAccount(t, biz, "acc-q-1", "", -time.Minute)
	if _, err := biz.Exec(`UPDATE accounts SET credentials_encrypted = '{}' WHERE id = 'acc-q-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.buildQueryInput(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-q-1"}, opsjobs.BalanceQueryConfig{}); err == nil {
		t.Fatal("凭据不可用必须拒绝")
	}
	// 明文凭据 + 缺 base_url。
	if _, err := biz.Exec(`UPDATE accounts SET credentials_encrypted = '{"api_key":"sk-plain"}' WHERE id = 'acc-q-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.buildQueryInput(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-q-1"}, opsjobs.BalanceQueryConfig{}); err == nil {
		t.Fatal("缺 base_url 必须拒绝")
	}
	// 带 base_url 与代理绑定。
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-q", "base_url": "http://127.0.0.1:1/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := biz.Exec(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
		VALUES ('pp-1', 'socks5', '127.0.0.1', 1080, '', '', 1)`); err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := biz.Exec(`UPDATE accounts SET credentials_encrypted = ?, proxy_profile_id = 'pp-1', balance_query_next_refresh_at = ? WHERE id = 'acc-q-1'`, string(credentials), next); err != nil {
		t.Fatal(err)
	}
	input, err := runtime.buildQueryInput(ctx, opsjobs.BalanceDetectionCandidate{
		ID: "acc-q-1", SystemAccountID: "sys-1", ConfigRevision: 1,
		NextRefreshAt: &next, ProxyProfileID: "pp-1",
	}, opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if input.BaseURL != "http://127.0.0.1:1" || input.Proxy == nil {
		t.Fatalf("输入构造: %+v", input)
	}
	// 非法 NextRefreshAt。
	badNext := "not-a-time"
	if _, err := runtime.buildQueryInput(ctx, opsjobs.BalanceDetectionCandidate{ID: "acc-q-1", NextRefreshAt: &badNext}, opsjobs.BalanceQueryConfig{}); err == nil {
		t.Fatal("非法 nextRefreshAt 必须拒绝")
	}
}

func TestW12CEnsureAccountUsageSnapshotsTableArms(t *testing.T) {
	_, _, statsDB := wgNewBalanceRuntime(t, "", false)
	ctx := context.Background()
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, false); err != nil {
		t.Fatalf("SQLite 建表: %v", err)
	}
	// 已存在时幂等。
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, false); err != nil {
		t.Fatalf("幂等建表: %v", err)
	}
}

// ---------------------------------------------------------------------------
// loadWorkerConfig 错误矩阵
// ---------------------------------------------------------------------------

func TestW12CLoadWorkerConfigErrorMatrix(t *testing.T) {
	base := func() map[string]string {
		return workerSmokeTestEnv(t)
	}
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"非法 driver", map[string]string{"JUHE_AI_DATABASE_DRIVER": "mysql"}},
		{"负 replica", map[string]string{"JUHE_AI_WORKER_REPLICA_INDEX": "-1"}},
		{"超大 replica", map[string]string{"JUHE_AI_WORKER_REPLICA_INDEX": "64"}},
		{"非法 open conns", map[string]string{"JUHE_AI_POSTGRES_MAX_OPEN_CONNS": "abc"}},
		{"非法 idle conns", map[string]string{"JUHE_AI_POSTGRES_MAX_IDLE_CONNS": "xyz"}},
		{"非法 projection max batches", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN": "401"}},
		{"非法 chat retention", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}},
		{"非法 batch size", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE": "0"}},
		{"非法 flush batches", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES": "0"}},
		{"非法 stats toggle", map[string]string{"JUHE_AI_JOBS_STATS_ENABLED": "maybe"}},
		{"非法 probe concurrency", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "0"}},
		{"非法 projection interval", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "1"}},
		{"非法 projection batch", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "101"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := base()
			for key, value := range tc.env {
				env[key] = value
			}
			if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
				t.Fatalf("%s 必须拒绝", tc.name)
			}
		})
	}
	// 合法 projection 边界值应通过。
	env := base()
	env["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED"] = "true"
	env["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS"] = "1000"
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil || !config.ListProjectionEnabled {
		t.Fatalf("合法 projection 配置应通过: %+v %v", config, err)
	}
}

// ---------------------------------------------------------------------------
// J1 outcome 投影面装配分支
// ---------------------------------------------------------------------------

func TestW12CWireHealthOutcomeProjectorArms(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite", BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3")}, slog.Default())
	defer assembly.closeStores()
	ctx := context.Background()

	// store 为 nil（J1 未启用）→ 合法缺席。
	if projector, err := assembly.wireHealthOutcomeProjector(func(string) string { return "" }, nil); err != nil || projector != nil {
		t.Fatalf("nil store 应缺席: %v %v", projector, err)
	}
	// env 显式关闭。
	disabled, disabledErr := assembly.wireHealthOutcomeProjector(func(name string) string {
		if name == "JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_DISABLED" {
			return "true"
		}
		return "JUHE_AI_ACCOUNT_HEALTH_ENABLED=true"
	}, &accounthealth.Store{})
	if disabledErr != nil || disabled != nil {
		t.Fatalf("env 关闭后投影面必须缺席: %v %v", disabled, disabledErr)
	}
	// env 未启用 J1 → 投影面合法缺席（LoadConfig 默认关闭）。
	if absent, err := assembly.wireHealthOutcomeProjector(func(string) string { return "" }, &accounthealth.Store{}); err != nil || absent != nil {
		t.Fatalf("J1 未启用应缺席: %v %v", absent, err)
	}
	// 轮询/批量 env 非法 → 装配失败（业务库可开）。
	invalidEnv := func(name string) string {
		if name == healthProjectionPollEnvVar {
			return "1"
		}
		return "JUHE_AI_ACCOUNT_HEALTH_ENABLED=true\nJUHE_AI_ACCOUNT_HEALTH_STORE=sqlite"
	}
	getenv := func(name string) string {
		pairs := map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":                  "true",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":               "go",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":                    "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":            filepath.Join(t.TempDir(), "j1.sqlite3"),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":          t.TempDir(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":        "0123456789abcdef0123456789abcdef",
			healthProjectionDisabledEnvVar:                    "",
			healthProjectionPollEnvVar:                        "1",
			healthProjectionBatchEnvVar:                       "",
		}
		return pairs[name]
	}
	_ = invalidEnv
	if _, err := assembly.wireHealthOutcomeProjector(getenv, &accounthealth.Store{}); err == nil {
		t.Fatal("非法轮询间隔必须暴露")
	}
	getenvBatch := func(name string) string {
		pairs := map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":                  "true",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":               "go",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":                    "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":            filepath.Join(t.TempDir(), "j1.sqlite3"),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":          t.TempDir(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":        "0123456789abcdef0123456789abcdef",
			healthProjectionDisabledEnvVar:                    "",
			healthProjectionPollEnvVar:                        "",
			healthProjectionBatchEnvVar:                       "1001",
		}
		return pairs[name]
	}
	if _, err := assembly.wireHealthOutcomeProjector(getenvBatch, &accounthealth.Store{}); err == nil {
		t.Fatal("非法批量必须暴露")
	}
	// 合法配置 → 投影器装配成功（stats 家族缺席 warn）。
	getenvOK := func(name string) string {
		pairs := map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":           "true",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w12c-j1",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     filepath.Join(t.TempDir(), "j1.sqlite3"),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
		}
		return pairs[name]
	}
	projector, err := assembly.wireHealthOutcomeProjector(getenvOK, &accounthealth.Store{})
	if err != nil || projector == nil {
		t.Fatalf("合法配置应装配投影器: %v %v", projector, err)
	}
	_ = ctx
}

// ---------------------------------------------------------------------------
// openBusinessDB 与 probeSettingsSource 回落
// ---------------------------------------------------------------------------

func TestW12COpenBusinessDBArms(t *testing.T) {
	// SQLite 空路径：openSQLite 会落到当前目录的库文件；断言可开且可关闭。
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite", BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3")}, slog.Default())
	defer assembly.closeStores()
	business, err := openBusinessDB(assembly, "w12c-ok")
	if err != nil || business == nil || business.postgres {
		t.Fatalf("SQLite 业务库应可开: %v %v", business, err)
	}
	// PG 驱动缺 URL → 错误。
	pgAssembly := newWorkerAssembly(workerConfig{Driver: "postgres"}, slog.Default())
	defer pgAssembly.closeStores()
	if _, err := openBusinessDB(pgAssembly, "w12c-pg-missing"); err == nil {
		t.Fatal("PG 缺 URL 必须报错")
	}
}

func TestW12CProbeSettingsSourceFallback(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlOpenSQLiteFile(filepath.Join(dir, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		time.Sleep(50 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	defer assembly.closeStores()
	settings := assembly.probeSettingsSource(&businessDB{db: db})
	// 未知键 → DefaultNumber 未收录 → 回落 min。
	if got := settings("w12c-unknown-key", 7, 100); got != 7 {
		t.Fatalf("未知键应回落 min: %d", got)
	}
	// 非法值 → Number 错误 → DefaultNumber 命中 → 默认值。
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'accountQualityWindowMinutes', '"abc"')`); err != nil {
		t.Fatal(err)
	}
	fallback := settings("accountQualityWindowMinutes", 1, 600)
	if fallback <= 0 {
		t.Fatalf("非法值应回落默认: %d", fallback)
	}
}


// ---------------------------------------------------------------------------
// wireFamilies 错误传播与代理信封补充分支
// ---------------------------------------------------------------------------

func TestW12CWireFamiliesErrorPropagation(t *testing.T) {
	// PG 驱动 + 非法连接参数：家族装配在 schedule-settings 池获取处失败，
	// buildWorkerAssembly 必须回收已打开句柄并上抛错误（wireFamilies 61.1%
	// 的错误传播臂）。
	config := workerConfig{
		Enabled:               true,
		Driver:                "postgres",
		PostgresURL:           "postgres://invalid:invalid@127.0.0.1:1/none?sslmode=disable",
		PostgresMaxOpenConns:  10,
		PostgresMaxIdleConns:  5,
		Secret:                wgBalanceSecret,
		InstanceID:            "w12c-propagation",
		StatsEnabled:          true,
		OAuthEnabled:          true,
		TaskRunsEnabled:       true,
		UsageWriterEnabled:    true,
		BalanceDetectEnabled:  true,
		RetentionEnabled:      true,
		ProbeEnabled:          true,
		ProbeConcurrency:      1,
		DrainTimeout:          time.Second,
		BusinessSQLitePath:    filepath.Join(t.TempDir(), "business.sqlite3"),
		StatsSQLitePath:       filepath.Join(t.TempDir(), "stats.sqlite3"),
		TaskRunsSQLitePath:    filepath.Join(t.TempDir(), "task-runs.sqlite3"),
		ChatSQLitePath:        filepath.Join(t.TempDir(), "chat.sqlite3"),
		DatasetSQLitePath:     filepath.Join(t.TempDir(), "dataset.sqlite3"),
		UsageCatalogSQLitePath: filepath.Join(t.TempDir(), "usage.sqlite3"),
		UsageShardRoot:        filepath.Join(t.TempDir(), "usage-shards"),
		CodexContextStateShardRoot: filepath.Join(t.TempDir(), "codex-state"),
		CodexContextStateShardCount: 1,
		ChatAssetsRoot:        filepath.Join(t.TempDir(), "chat-assets"),
		CodexContextRoot:      filepath.Join(t.TempDir(), "codex-context"),
	}
	assembly, err := buildWorkerAssembly(config, slog.Default())
	if err == nil {
		if assembly != nil {
			assembly.closeStores()
		}
		t.Fatal("不可达 PG 必须让装配失败")
	}
	// 装配失败后组合根必须返回 nil assembly（内部已回收句柄）。
	if assembly != nil {
		t.Fatal("失败装配必须返回 nil assembly")
	}
}

func TestW12CProxyEnvelopeEncryptedPassword(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	envelope, err := accountbalance.NewCredentialEnvelope(wgBalanceSecret, "proxy_password", map[string]any{"password": "w12c-pass"})
	if err != nil {
		t.Fatal(err)
	}
	// 加密密码字段是与 NewCredentialEnvelope 输出同构的信封文本。
	proxy, err := runtime.proxyEnvelope("p1", "http", "127.0.0.1", 8080, "user", envelope.Ciphertext)
	if err != nil || proxy == nil {
		t.Fatalf("加密代理解析: %v %v", proxy, err)
	}
}

func TestW12CCommitDetectionDueExecError(t *testing.T) {
	runtime, biz, _ := wgNewBalanceRuntime(t, "", false)
	wgSeedDueAccount(t, biz, "acc-due-1", "http://127.0.0.1:1", -time.Minute)
	if err := biz.Close(); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.CommitDetectionDue(context.Background(), opsjobs.BalanceCommitDueInput{
		AccountID: "acc-due-1", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &due,
	}); err == nil {
		t.Fatal("业务库关闭后的执行错误必须暴露")
	}
}
