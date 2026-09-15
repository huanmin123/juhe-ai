package main

// w1z 补充覆盖（w1z 前缀，与其他 w1* 批次不冲突）：
//  1. compose_account_balance_refresh.go：resolveProxyURLEnvelope 全臂、
//     buildManualInput 拒绝臂、RefreshManual 的 runner 取消/过期/错误臂与
//     dispatchRevision=0 + socks5h 代理 committed 臂、TestDraft 上游三臂
//     （成功 JSON / 非 2xx / 网络错误）与输入校验臂、RefreshDraftModelCatalog
//     错误臂（缺 base_url / 非 2xx / 网络错误）。
//  2. chain_usage.go：spooledUsageRecorder 的 nil-spool 丢弃计数 + 采样告警、
//     Close 后 EnqueueUsageRecord 的 spool 兜底、chainAttemptAuditSink 三方法
//     对 capture 的输入转换、chainFinalizationUsage.RecordCompletedUpstreamAttempt
//     投影（完整身份 + 最小输入）。
//  3. compose_account_test_local.go：loadGatewayTestQueueEnv 默认/覆盖/fail-closed
//     表、gatewayAccountTestDispatch 的派发/幂等/无效 ID/停止拒收与取消投影。
//  4. chain_request_failure_health.go：EnqueueProbeRequest 在 ensureSchema once
//     缓存命中后 INSERT 失败的 dispatch_rejected 臂。
//
// 与既有 *_test.go 已覆盖路径不重复（RefreshManual committed/stale/lease_busy、
// TestDraft fresh/多 Key、OAuth 目录刷新、dispatch marks、fence 行投影、审计
// sink 装配类型断言等由 compose_account_balance_refresh_test.go /
// w1_misc_pure_test.go / chain_failure_health_wiring_test.go /
// compose_account_test_local_test.go 持有）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	platformaccountbalance "github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

// ---------------------------------------------------------------------------
// 共享 w1z helper
// ---------------------------------------------------------------------------

const w1zRefreshSecret = "w1z-refresh-secret"

// w1zOpenProxyProfileDB 打开带最小 proxy_profiles 表的独立 SQLite（列集按
// resolveProxyURLEnvelope 的 SELECT 推导）。
func w1zOpenProxyProfileDB(t *testing.T) *sql.DB {
	t.Helper()
	db := w1uOpenPlainSQLite(t, "w1z-proxy.sqlite3")
	if _, err := db.Exec(`CREATE TABLE proxy_profiles (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 0,
		username TEXT NOT NULL DEFAULT '',
		password_encrypted TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("建 proxy_profiles 表失败: %v", err)
	}
	return db
}

// w1zSeedProxyProfile 插入一行代理 profile。
func w1zSeedProxyProfile(t *testing.T, db *sql.DB, id, kind, host string, port int64, username, encryptedPassword string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted)
		VALUES (?, ?, ?, ?, ?, ?)`, id, kind, host, port, username, encryptedPassword); err != nil {
		t.Fatalf("插入 proxy_profiles 行失败: %v", err)
	}
}

// w1zNewBalanceRefresher 构造带独立 SQLite 执行 store 与 canned 上游 doer 的
// 手动余额刷新适配器；runnerNow 可注入 runner 侧时钟（与 refresher.now 解耦，
// 用于触发 input 过期臂）；db 为候选代理解析句柄（可为 nil）。
func w1zNewBalanceRefresher(t *testing.T, upstreamBody string, runnerNow func() time.Time, db *sql.DB) *gatewayManualBalanceRefresher {
	t.Helper()
	store, err := platformaccountbalance.OpenStore(platformaccountbalance.StoreConfig{
		Mode: platformaccountbalance.StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w1z-balance.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner, err := platformaccountbalance.NewRunner(platformaccountbalance.RunnerConfig{
		Store:            store,
		OwnerID:          "w1z-manual-owner",
		CredentialSecret: w1zRefreshSecret,
		HTTPClient:       &cannedJSONDoer{body: upstreamBody},
		Now:              runnerNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &gatewayManualBalanceRefresher{db: db, pg: false, secret: w1zRefreshSecret, store: store, runner: runner, now: time.Now}
}

// w1zSealedCandidate 构造凭据已按 w1zRefreshSecret 加封的有效候选行。
func w1zSealedCandidate(t *testing.T, credentials accounts.Credentials) accounts.BalanceRefreshCandidate {
	t.Helper()
	envelope, err := accounts.EncryptJSON(w1zRefreshSecret, credentials)
	if err != nil {
		t.Fatalf("加密候选凭据失败: %v", err)
	}
	return accounts.BalanceRefreshCandidate{
		ID: "acct-w1z", SystemAccountID: "sys-w1z", ProviderCode: "openai", Type: "api_key",
		Status: "active", Schedulable: true, ConfigRevision: 1, DispatchRevision: 1,
		CredentialsEnvelope: envelope, ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`,
	}
}

// w1zCancelCall 记录 fake 仓储收到的取消请求。
type w1zCancelCall struct {
	taskID  string
	message string
}

// w1zManualTestRepo 是 accounttest.ManualTestTaskRepo 的内存 fake（Cancel
// 记录调用供断言；其余方法空实现——本批次只测队列同步入口，不启动队列）。
type w1zManualTestRepo struct {
	mu      sync.Mutex
	cancels []w1zCancelCall
}

func (r *w1zManualTestRepo) Maintenance(context.Context, accounttest.ManualTestMaintenanceInput) (accounttest.ManualTestMaintenanceResult, error) {
	return accounttest.ManualTestMaintenanceResult{}, nil
}

func (r *w1zManualTestRepo) MarkRunning(context.Context, string) (*accounttest.ManualTestTaskRecord, error) {
	return nil, nil
}

func (r *w1zManualTestRepo) Complete(context.Context, string, accounttest.ManualTestTaskExecutorResult, *string) error {
	return nil
}

func (r *w1zManualTestRepo) Fail(context.Context, string, string, string, *string) error { return nil }

func (r *w1zManualTestRepo) Cancel(_ context.Context, taskID, message string, _ *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, w1zCancelCall{taskID: taskID, message: message})
	return nil
}

func (r *w1zManualTestRepo) UpdateMessage(context.Context, string, string, *string) error { return nil }

func (r *w1zManualTestRepo) cancelCalls() []w1zCancelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]w1zCancelCall(nil), r.cancels...)
}

// w1zNewManualTestQueue 构造不启动后台循环的手动测试队列（不 Start/Run，
// 无 goroutine；只驱动同步入口）。
func w1zNewManualTestQueue(t *testing.T, repo *w1zManualTestRepo) *accounttest.ManualTestQueue {
	t.Helper()
	queue, err := accounttest.NewManualTestQueue(repo,
		func(context.Context, accounttest.ManualTestTaskRecord, accounttest.ProgressReporter) (accounttest.ManualTestTaskExecutorResult, error) {
			return accounttest.ManualTestTaskExecutorResult{}, nil
		},
		accounttest.ManualTestQueueConfig{
			RefillMaxBatchSize:   10,
			QueuedSweepBatchSize: 5,
			Concurrency:          1,
			QueuedMaxWaitMS:      60_000,
			RunningStaleMS:       60_000,
			NowMS:                func() int64 { return time.Now().UnixMilli() },
		})
	if err != nil {
		t.Fatalf("构造手动测试队列失败: %v", err)
	}
	return queue
}

// ---------------------------------------------------------------------------
// compose_account_balance_refresh.go
// ---------------------------------------------------------------------------

// TestW1ZResolveProxyURLEnvelopeArms 覆盖代理解析的直连/不存在/成功（http
// 带认证 + socks5→socks5h）/类型不支持/端口非法/密码解密失败/表缺失分支。
func TestW1ZResolveProxyURLEnvelopeArms(t *testing.T) {
	db := w1zOpenProxyProfileDB(t)
	passwordEnvelope, err := platformaccountbalance.NewCredentialEnvelope(w1zRefreshSecret, "proxy_password", map[string]any{"password": "w1z-pass"})
	if err != nil {
		t.Fatal(err)
	}
	wrongSecretEnvelope, err := platformaccountbalance.NewCredentialEnvelope("w1z-other-secret", "proxy_password", map[string]any{"password": "x"})
	if err != nil {
		t.Fatal(err)
	}
	w1zSeedProxyProfile(t, db, "w1z-prof-http", "http", "127.0.0.1", 8080, "w1z-user", passwordEnvelope.Ciphertext)
	w1zSeedProxyProfile(t, db, "w1z-prof-socks", "socks5", "10.0.0.7", 1080, "", "")
	w1zSeedProxyProfile(t, db, "w1z-prof-badtype", "ftp", "127.0.0.1", 21, "", "")
	w1zSeedProxyProfile(t, db, "w1z-prof-badport", "http", "127.0.0.1", 70000, "", "")
	w1zSeedProxyProfile(t, db, "w1z-prof-badsecret", "http", "127.0.0.1", 8081, "u", wrongSecretEnvelope.Ciphertext)

	cases := []struct {
		name      string
		profileID string
		wantNil   bool
		wantURL   string
		wantErr   string
	}{
		{name: "空 profile 直连", profileID: "   ", wantNil: true},
		{name: "profile 不存在直连", profileID: "w1z-prof-missing", wantNil: true},
		{name: "http 代理带认证解封", profileID: "w1z-prof-http", wantURL: "http://w1z-user:w1z-pass@127.0.0.1:8080"},
		{name: "socks5 映射 socks5h 远程 DNS", profileID: "w1z-prof-socks", wantURL: "socks5h://10.0.0.7:1080"},
		{name: "类型不支持", profileID: "w1z-prof-badtype", wantErr: "不支持的 proxy 类型"},
		{name: "端口非法", profileID: "w1z-prof-badport", wantErr: "代理地址无效"},
		{name: "密码信封解密失败", profileID: "w1z-prof-badsecret", wantErr: "认证失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := resolveProxyURLEnvelope(context.Background(), db, false, w1zRefreshSecret, tc.profileID)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveProxyURLEnvelope(%q) err = %v，want 包含 %q", tc.profileID, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProxyURLEnvelope(%q): %v", tc.profileID, err)
			}
			if tc.wantNil {
				if envelope != nil {
					t.Fatalf("resolveProxyURLEnvelope(%q) = %#v，want nil（直连）", tc.profileID, envelope)
				}
				return
			}
			if envelope == nil {
				t.Fatalf("resolveProxyURLEnvelope(%q) = nil，want proxy_url envelope", tc.profileID)
			}
			plain, err := platformaccountbalance.DecryptV1Envelope(w1zRefreshSecret, envelope.Ciphertext)
			if err != nil {
				t.Fatalf("解封 proxy_url envelope: %v", err)
			}
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(plain, &payload); err != nil {
				t.Fatalf("proxy_url 载荷不是 JSON: %v", err)
			}
			if payload.URL != tc.wantURL {
				t.Fatalf("proxy url = %q，want %q", payload.URL, tc.wantURL)
			}
		})
	}

	// 表缺失 → 查询错误上抛（不静默降级为直连）。
	bareDB := w1uOpenPlainSQLite(t, "w1z-proxy-bare.sqlite3")
	if _, err := resolveProxyURLEnvelope(context.Background(), bareDB, false, w1zRefreshSecret, "w1z-prof-http"); err == nil {
		t.Fatal("proxy_profiles 表缺失时必须报错")
	}
}

// TestW1ZRefreshManualInputValidationArms 经 RefreshManual 驱动 buildManualInput
// 的全部拒绝臂（错误臂在触达 runner 前返回，无需执行 store）。
func TestW1ZRefreshManualInputValidationArms(t *testing.T) {
	refresher := &gatewayManualBalanceRefresher{db: nil, pg: false, secret: w1zRefreshSecret, now: time.Now}

	brokenEnvelopeCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z", "base_url": "http://127.0.0.1:9"})
	brokenEnvelopeCandidate.CredentialsEnvelope = "w1z-not-a-v1-envelope"

	multiKeyCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_keys": []any{"sk-1", "sk-2"}, "base_url": "http://127.0.0.1:9"})

	missingBaseURLCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z"})

	brokenConfigCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z", "base_url": "http://127.0.0.1:9"})
	brokenConfigCandidate.ConfigJSON = `{broken`

	unknownConfigFieldCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z", "base_url": "http://127.0.0.1:9"})
	unknownConfigFieldCandidate.ConfigJSON = `{"adapter":"builtin","unknownField":1}`

	zeroRevisionCandidate := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z", "base_url": "http://127.0.0.1:9"})
	zeroRevisionCandidate.ConfigRevision = 0

	cases := []struct {
		name      string
		candidate accounts.BalanceRefreshCandidate
		wantErr   string
	}{
		{name: "凭据信封无法解封", candidate: brokenEnvelopeCandidate, wantErr: "无法解封"},
		{name: "多 Key 池拒绝", candidate: multiKeyCandidate, wantErr: "必须包含一个有效的 API Key"},
		{name: "缺少 base_url", candidate: missingBaseURLCandidate, wantErr: "缺少 base_url"},
		{name: "配置 JSON 损坏", candidate: brokenConfigCandidate, wantErr: "查询配置无效"},
		{name: "配置未知字段", candidate: unknownConfigFieldCandidate, wantErr: "查询配置无效"},
		{name: "configRevision 非正", candidate: zeroRevisionCandidate, wantErr: "configRevision 必须是正整数"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := refresher.RefreshManual(context.Background(), tc.candidate)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RefreshManual err = %v，want 包含 %q", err, tc.wantErr)
			}
			if outcome.Persisted || outcome.Outcome != "" || outcome.Snapshot != nil {
				t.Fatalf("拒绝臂不得产出 outcome: %+v", outcome)
			}
		})
	}
}

// TestW1ZRefreshManualRunnerArms 覆盖 runner 侧错误臂与 committed 附加臂：
// 已取消上下文、runner 时钟超前（input 过期 → report.Errors 原样上抛）、
// dispatchRevision=0 回落 InputVersion=1 + socks5h 代理 → committed。
func TestW1ZRefreshManualRunnerArms(t *testing.T) {
	candidate := func(proxyProfileID sql.NullString, dispatchRevision int64) accounts.BalanceRefreshCandidate {
		item := w1zSealedCandidate(t, accounts.Credentials{"api_key": "sk-w1z", "base_url": "http://127.0.0.1:9"})
		item.ProxyProfileID = proxyProfileID
		item.DispatchRevision = dispatchRevision
		return item
	}

	// ① 已取消上下文：RunManual 直接上抛 ctx 错误。
	cancelledRefresher := w1zNewBalanceRefresher(t, `{"unit":"USD","remaining":"1"}`, time.Now, nil)
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cancelledRefresher.RefreshManual(cancelledCtx, candidate(sql.NullString{}, 1)); err == nil {
		t.Fatal("已取消上下文的 RefreshManual 必须返回错误")
	}

	// ② runner 时钟超前 1h：冻结输入在 runner 视角已过期 → Errors 臂原样上抛。
	skewedRefresher := w1zNewBalanceRefresher(t, `{"unit":"USD","remaining":"1"}`, func() time.Time { return time.Now().Add(time.Hour) }, nil)
	if _, err := skewedRefresher.RefreshManual(context.Background(), candidate(sql.NullString{}, 1)); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期输入 err = %v，want 包含 过期", err)
	}

	// ③ dispatchRevision=0 → InputVersion 回落 1；socks5 代理 → socks5h envelope
	// （canned doer 持有 transport，仅校验信封不真实拨号）→ committed。
	proxyDB := w1zOpenProxyProfileDB(t)
	w1zSeedProxyProfile(t, proxyDB, "w1z-prof-manual", "socks5", "10.0.0.9", 1080, "", "")
	proxyRefresher := w1zNewBalanceRefresher(t, `{"unit":"USD","remaining":"4.25"}`, time.Now, proxyDB)
	outcome, err := proxyRefresher.RefreshManual(context.Background(), candidate(sql.NullString{String: "w1z-prof-manual", Valid: true}, 0))
	if err != nil {
		t.Fatalf("代理候选 committed: %v", err)
	}
	if !outcome.Persisted || outcome.Outcome != "committed" {
		t.Fatalf("代理候选 outcome = %+v，want committed", outcome)
	}
	if outcome.Snapshot["status"] != "fresh" || outcome.Snapshot["remainingUsd"] != "4.250000" {
		t.Fatalf("代理候选快照 = %v", outcome.Snapshot)
	}
}

// TestW1ZTestDraftProbeArms 用 httptest 上游覆盖草稿探测三臂（成功 JSON /
// 非 2xx / 网络错误）与输入校验、代理解析失败臂。
func TestW1ZTestDraftProbeArms(t *testing.T) {
	draftConfig := map[string]any{"adapter": "builtin", "intervalMinutes": 5}
	draftInput := func(baseURL string) accounts.BalanceDraftProbeInput {
		return accounts.BalanceDraftProbeInput{
			Credentials: accounts.Credentials{"api_key": "sk-w1z", "base_url": baseURL},
			Config:      draftConfig,
		}
	}

	// ① 成功 JSON：sub2api 适配器命中 /v1/usage → fresh 快照。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/usage" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"unit":"USD","remaining":"3.5"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)
	refresher := &gatewayManualBalanceRefresher{db: nil, pg: false, secret: w1zRefreshSecret, now: time.Now}
	snapshot, err := refresher.TestDraft(context.Background(), draftInput(upstream.URL))
	if err != nil {
		t.Fatalf("成功臂: %v", err)
	}
	if snapshot["status"] != "fresh" || snapshot["remainingUsd"] != "3.500000" {
		t.Fatalf("成功臂快照 = %v", snapshot)
	}

	// ② 非 2xx：全部内置适配器路径 500 → 非临时诊断 → unsupported 快照（无 error）。
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(rejecting.Close)
	snapshot, err = refresher.TestDraft(context.Background(), draftInput(rejecting.URL))
	if err != nil {
		t.Fatalf("非 2xx 臂: %v", err)
	}
	if snapshot["status"] != "unsupported" {
		t.Fatalf("非 2xx 臂快照 = %v，want unsupported", snapshot)
	}

	// ③ 网络错误：上游已关闭 → 临时诊断 → 手动 failed 快照（无 error）。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	snapshot, err = refresher.TestDraft(context.Background(), draftInput(deadURL))
	if err != nil {
		t.Fatalf("网络错误臂: %v", err)
	}
	if snapshot["status"] != "failed" {
		t.Fatalf("网络错误臂快照 = %v，want failed", snapshot)
	}

	// ④ 缺 base_url。
	if _, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-w1z"},
		Config:      draftConfig,
	}); err == nil || !strings.Contains(err.Error(), "余额查询测试缺少 base_url") {
		t.Fatalf("缺 base_url err = %v", err)
	}

	// ⑤ 配置无效（未知字段 fail closed）。
	if _, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-w1z", "base_url": upstream.URL},
		Config:      map[string]any{"adapter": "builtin", "unknownField": true},
	}); err == nil || !strings.Contains(err.Error(), "余额查询测试配置无效") {
		t.Fatalf("配置无效 err = %v", err)
	}

	// ⑥ 代理解析失败：profile 类型不支持 → 错误上抛。
	proxyDB := w1zOpenProxyProfileDB(t)
	w1zSeedProxyProfile(t, proxyDB, "w1z-prof-draft", "ftp", "127.0.0.1", 21, "", "")
	proxyRefresher := &gatewayManualBalanceRefresher{db: proxyDB, pg: false, secret: w1zRefreshSecret, now: time.Now}
	proxyID := "w1z-prof-draft"
	if _, err := proxyRefresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials:    accounts.Credentials{"api_key": "sk-w1z", "base_url": upstream.URL},
		Config:         draftConfig,
		ProxyProfileID: &proxyID,
	}); err == nil || !strings.Contains(err.Error(), "不支持的 proxy 类型") {
		t.Fatalf("代理解析失败 err = %v", err)
	}
}

// TestW1ZRefreshDraftModelCatalogErrorArms 覆盖目录刷新的缺 base_url、上游
// 非 2xx 与网络错误臂（错误臂在本地目录投影前返回，无需 provider store）。
func TestW1ZRefreshDraftModelCatalogErrorArms(t *testing.T) {
	refresher := &gatewayModelCatalogRefresher{db: nil, pg: false, secret: w1zRefreshSecret, catalog: nil, now: time.Now}
	discovery := func(baseURL string) accounts.ModelCatalogDiscoveryInput {
		return accounts.ModelCatalogDiscoveryInput{
			ProviderCode: "gpt", ProtocolCode: "openai", AccountType: "api_key",
			Credentials: accounts.Credentials{"api_key": "sk-w1z", "base_url": baseURL},
		}
	}

	if _, err := refresher.RefreshDraftModelCatalog(context.Background(), accounts.ModelCatalogDiscoveryInput{
		ProviderCode: "gpt", ProtocolCode: "openai", AccountType: "api_key",
		Credentials: accounts.Credentials{"api_key": "sk-w1z"},
	}); err == nil || !strings.Contains(err.Error(), "账户缺少 base_url") {
		t.Fatalf("缺 base_url err = %v", err)
	}

	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(rejecting.Close)
	if _, err := refresher.RefreshDraftModelCatalog(context.Background(), discovery(rejecting.URL+"/v1")); err == nil {
		t.Fatal("上游非 2xx 必须报错（路由渲染 400 契约）")
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if _, err := refresher.RefreshDraftModelCatalog(context.Background(), discovery(deadURL+"/v1")); err == nil {
		t.Fatal("上游网络错误必须报错")
	}
}

// ---------------------------------------------------------------------------
// chain_usage.go
// ---------------------------------------------------------------------------

// TestW1ZSpooledUsageRecorderNilSpoolDropsDeterministically：spool 未装配时
// 每条记录（缓冲投递或溢出）恰好计一次丢弃，并按采样输出告警事件。
func TestW1ZSpooledUsageRecorderNilSpoolDropsDeterministically(t *testing.T) {
	var buf bytes.Buffer
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 2, Logger: w1uSlogText(&buf)}, nil)
	for index := 0; index < 3; index++ {
		if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), gatewayusage.UsageRecordInput{
			TraceID: "trace_w1z_drop", TrafficSource: "gateway",
		}); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	recorder.Close()
	if recorder.dropped != 3 || recorder.failed != 0 {
		t.Fatalf("spool 未装配 dropped = %d failed = %d，want 3/0（每条记录恰好计一次丢弃）", recorder.dropped, recorder.failed)
	}
	if !strings.Contains(buf.String(), "usage_record_spool_unavailable") {
		t.Fatalf("deliver 丢弃必须输出告警事件，日志 = %q", buf.String())
	}
}

// TestW1ZSpooledUsageRecorderClosedPersistsOverflowToSpool：Close 后的
// EnqueueUsageRecord 走 persistOverflow 同步落 spool（closed 分支）。
func TestW1ZSpooledUsageRecorderClosedPersistsOverflowToSpool(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	spool := gatewayusageNewSpool(t, spoolDir, gatewaypreauth.SystemClock{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 1}, spool)
	recorder.Close()
	if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), gatewayusage.UsageRecordInput{
		TraceID: "trace_w1z_closed", TrafficSource: "gateway",
	}); err != nil {
		t.Fatalf("Close 后溢出必须经 spool 兜底: %v", err)
	}
	if entries := spoolDirectoryEntries(t, spoolDir); len(entries) == 0 {
		t.Fatal("Close 后的溢出记录必须写入 spool 文件")
	}
}

// TestW1ZChainAttemptAuditSinkConvertsDispatchInputs：nil capture 安全 + 三方法
// 的输入转换（StartAttempt 产出尝试 ID；失败 CompleteAttempt 与失败派发尝试
// 都进入审计失败根因投影）。
func TestW1ZChainAttemptAuditSinkConvertsDispatchInputs(t *testing.T) {
	blank := chainAttemptAuditSink{}
	if got := blank.StartAttempt(gatewaydispatch.StartAttemptInput{AttemptIndex: 0}); got != "" {
		t.Fatalf("nil capture StartAttempt = %q，want 空串", got)
	}
	blank.CompleteAttempt("attempt_w1z_blank", gatewaydispatch.CompleteAttemptInput{Success: true})
	blank.RecordFailedDispatchAttempt(gatewaydispatch.FailedDispatchAttemptInput{AttemptIndex: 0})

	t.Cleanup(gatewayusage.ResetActiveAuditCaptureCountForTest)
	gatewayusage.ResetActiveAuditCaptureCountForTest()
	capture := gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID: "trace_w1z_sink", StartedAtMs: 1728000000000, TrafficSource: "gateway",
		Method: http.MethodPost, Path: "/v1/chat/completions",
		Settings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{Enabled: true}},
	})
	t.Cleanup(capture.Cancel)
	sink := chainAttemptAuditSink{capture: capture}

	model := "gpt-w1z-sink"
	request := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		State: &gatewaybody.BodyState{Model: &model},
	}}
	attemptID := sink.StartAttempt(gatewaydispatch.StartAttemptInput{
		Account:                   gatewaydispatch.AccountCandidate{ID: "acc-w1z-sink", ProviderCode: "openai"},
		AttemptIndex:              2,
		UpstreamURL:               "http://127.0.0.1:9/v1/chat/completions",
		Method:                    http.MethodPost,
		Headers:                   map[string]string{"authorization": "Bearer sk-w1z", "content-type": "application/json"},
		Body:                      []byte(`{"model":"gpt-w1z-sink"}`),
		RequestForModelAccounting: request,
	})
	if !strings.HasPrefix(attemptID, "attempt_") {
		t.Fatalf("StartAttempt ID = %q，want attempt_ 前缀", attemptID)
	}

	statusCode := 502
	sink.CompleteAttempt(attemptID, gatewaydispatch.CompleteAttemptInput{
		Success: false, ErrorPhase: "upstream", ErrorCode: "w1z_boom", ErrorMessage: "上游失败",
		StatusCode:      &statusCode,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    []byte(`{"error":"boom"}`),
	})
	root := capture.LatestFailedAttemptRoot()
	if root == nil || root.ErrorPhase != "upstream" || root.ErrorCode != "w1z_boom" || root.ErrorMessage != "上游失败" {
		t.Fatalf("CompleteAttempt 后失败根因 = %+v，want upstream/w1z_boom/上游失败", root)
	}

	sink.RecordFailedDispatchAttempt(gatewaydispatch.FailedDispatchAttemptInput{
		Account:                   gatewaydispatch.AccountCandidate{ID: "acc-w1z-sink", ProviderCode: "openai"},
		AttemptIndex:              3,
		UpstreamURL:               "http://127.0.0.1:9/v1/chat/completions",
		Method:                    http.MethodPost,
		StartedAtMs:               1728000000500,
		ErrorPhase:                "dispatch",
		ErrorCode:                 "w1z_connect",
		ErrorMessage:              "连接失败",
		RequestForModelAccounting: request,
	})
	root = capture.LatestFailedAttemptRoot()
	if root == nil || root.ErrorPhase != "dispatch" || root.ErrorCode != "w1z_connect" || root.ErrorMessage != "连接失败" {
		t.Fatalf("RecordFailedDispatchAttempt 后失败根因 = %+v，want dispatch/w1z_connect/连接失败", root)
	}
}

// TestW1ZChainFinalizationUsageRecordCompletedUpstreamAttempt：成功尝试投影
// 携带完整身份与时间维度；最小输入保持空账户与缺省指针。
func TestW1ZChainFinalizationUsageRecordCompletedUpstreamAttempt(t *testing.T) {
	// recorder 缺省：静默 no-op。
	(chainFinalizationUsage{}).RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{})

	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	firstTokenMs := int64(120)
	startedAtMs := int64(1000)
	completedAtMs := int64(1300)
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID: "trace_w1z_completed", TrafficSource: "gateway", ClientIP: "198.51.100.9",
			SystemAccountID: "sys-w1z", APIKeyID: "key-w1z", GroupID: "grp-w1z",
			Endpoint: "/v1/chat/completions", ProviderCode: "openai",
		},
		Account:       gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-w1z-completed"}},
		Success:       true,
		StatusCode:    200,
		Stream:        true,
		FirstTokenMs:  &firstTokenMs,
		StartedAtMs:   startedAtMs,
		CompletedAtMs: &completedAtMs,
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d，want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.TraceID != "trace_w1z_completed" || record.TrafficSource != "gateway" || record.ClientIP != "198.51.100.9" ||
		record.SystemAccountID != "sys-w1z" || record.APIKeyID != "key-w1z" || record.GroupID != "grp-w1z" ||
		record.Endpoint != "/v1/chat/completions" || record.ProviderCode != "openai" || record.AccountID != "acc-w1z-completed" {
		t.Fatalf("身份投影 = %+v", record)
	}
	if record.UsageSemantic != "gateway_request" || !record.Success || record.CreatedAt == "" {
		t.Fatalf("语义/成功/时间戳投影 = %+v", record)
	}
	if record.Stream == nil || !*record.Stream {
		t.Fatalf("Stream 投影 = %v，want true 指针", record.Stream)
	}
	if record.StatusCode == nil || *record.StatusCode != 200 {
		t.Fatalf("StatusCode 投影 = %v，want 200", record.StatusCode)
	}
	if record.FirstTokenMs == nil || *record.FirstTokenMs != 120 {
		t.Fatalf("FirstTokenMs 投影 = %v，want 120", record.FirstTokenMs)
	}
	if record.DurationMs == nil || *record.DurationMs != 300 {
		t.Fatalf("DurationMs 投影 = %v，want 1300-1000=300", record.DurationMs)
	}

	// 最小输入：无账户 → AccountID 空串；无完成时间 → DurationMs 缺省；Stream
	// 值语义仍落地（false 指针）。
	recorder.records = nil
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_w1z_min"},
	})
	if len(recorder.records) != 1 {
		t.Fatalf("最小输入 records = %d，want 1", len(recorder.records))
	}
	record = recorder.records[0]
	if record.AccountID != "" {
		t.Fatalf("无账户 AccountID = %q，want 空串", record.AccountID)
	}
	if record.Stream == nil || *record.Stream {
		t.Fatalf("最小输入 Stream = %v，want false 指针", record.Stream)
	}
	if record.DurationMs != nil || record.FirstTokenMs != nil {
		t.Fatalf("最小输入时间指针必须缺省: %+v", record)
	}
}

// ---------------------------------------------------------------------------
// compose_account_test_local.go
// ---------------------------------------------------------------------------

// TestW1ZLoadGatewayTestQueueEnv：默认值、全量覆盖与 fail closed 表。
func TestW1ZLoadGatewayTestQueueEnv(t *testing.T) {
	defaults := gatewayTestQueueEnv{
		ProbeConcurrency:     512,
		RefillMaxBatchSize:   1_000,
		QueuedSweepBatchSize: 500,
		QueuedMaxWaitMS:      10 * 60_000,
		RunningStaleMS:       10 * 60_000,
	}
	env, err := loadGatewayTestQueueEnv(func(string) string { return "" })
	if err != nil || env != defaults {
		t.Fatalf("默认 env = %+v err = %v，want %+v", env, err, defaults)
	}

	env, err = loadGatewayTestQueueEnv(func(name string) string {
		switch name {
		case envProbeConcurrency:
			return " 16 "
		case envRefillMaxBatchSize:
			return "20"
		case envQueuedSweepBatchSize:
			return "10"
		case envQueuedMaxWaitMS:
			return "5000"
		case envRunningStaleMS:
			return "120000"
		}
		return ""
	})
	want := gatewayTestQueueEnv{ProbeConcurrency: 16, RefillMaxBatchSize: 20, QueuedSweepBatchSize: 10, QueuedMaxWaitMS: 5000, RunningStaleMS: 120000}
	if err != nil || env != want {
		t.Fatalf("覆盖 env = %+v err = %v，want %+v（值按 envInt 语义去空白）", env, err, want)
	}

	knobs := []string{envProbeConcurrency, envRefillMaxBatchSize, envQueuedSweepBatchSize, envQueuedMaxWaitMS, envRunningStaleMS}
	for _, knob := range knobs {
		for _, bad := range []struct {
			name    string
			value   string
			wantErr string
		}{
			{name: "非整数", value: "abc", wantErr: "必须是整数"},
			{name: "低于下界", value: "0", wantErr: "必须介于"},
			{name: "高于上界", value: "999999999", wantErr: "必须介于"},
		} {
			t.Run(knob+"/"+bad.name, func(t *testing.T) {
				_, err := loadGatewayTestQueueEnv(func(name string) string {
					if name == knob {
						return bad.value
					}
					return ""
				})
				if err == nil || !strings.Contains(err.Error(), bad.wantErr) {
					t.Fatalf("loadGatewayTestQueueEnv(%s=%q) err = %v，want 包含 %q", knob, bad.value, err, bad.wantErr)
				}
			})
		}
	}
}

// TestW1ZGatewayAccountTestDispatchArms：无效 ID 跳过、新任务受理、重复派发
// 幂等、停止后拒收（503 契约）、取消的无效忽略与仓储投影。
func TestW1ZGatewayAccountTestDispatchArms(t *testing.T) {
	repo := &w1zManualTestRepo{}
	queue := w1zNewManualTestQueue(t, repo)
	dispatch := &gatewayAccountTestDispatch{queue: queue}
	ctx := context.Background()

	if !dispatch.DispatchAccountTestTasks(ctx, []string{"", "   "}) {
		t.Fatal("纯无效 ID 批次必须按受理返回 true")
	}
	if !dispatch.DispatchAccountTestTasks(ctx, []string{" w1z-task-1 "}) {
		t.Fatal("新任务必须本地入队受理")
	}
	if !dispatch.DispatchAccountTestTasks(ctx, []string{"w1z-task-1"}) {
		t.Fatal("重复派发必须按幂等成功返回 true")
	}

	if calls := repo.cancelCalls(); len(calls) != 0 {
		t.Fatalf("无效取消不得触达仓储: %#v", calls)
	}
	dispatch.DispatchAccountTestCancel(" w1z-task-1 ")
	calls := repo.cancelCalls()
	if len(calls) != 1 || calls[0].taskID != "w1z-task-1" || calls[0].message != "已停止测试" {
		t.Fatalf("取消投影 = %#v，want w1z-task-1/已停止测试", calls)
	}

	// 停止后拒收（sweep 循环未启动，预取消 ctx 让 Stop 立即返回）。
	stoppedRepo := &w1zManualTestRepo{}
	stoppedQueue := w1zNewManualTestQueue(t, stoppedRepo)
	stoppedDispatch := &gatewayAccountTestDispatch{queue: stoppedQueue}
	stoppedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stoppedQueue.Stop(stoppedCtx)
	if stoppedDispatch.DispatchAccountTestTasks(ctx, []string{"w1z-task-stop"}) {
		t.Fatal("已停止队列必须拒收派发（路由 503 契约）")
	}
	if !stoppedQueue.Stopped() {
		t.Fatal("前置断言失败：队列未进入停止态")
	}
}

// ---------------------------------------------------------------------------
// chain_request_failure_health.go
// ---------------------------------------------------------------------------

// TestW1ZEnqueueProbeRequestInsertFailureRejected：ensureSchema once 缓存命中
// 后表被移除，INSERT 失败必须降级为 dispatch_rejected（非 queued）。
func TestW1ZEnqueueProbeRequestInsertFailureRejected(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "w1z-outbox.sqlite3")
	writer := newChainProbeRequestOutboxWriter(db, false, 30_000)
	if outcome := writer.EnqueueProbeRequest(context.Background(), "acc-w1z", chainRequestFailureReason, "", nil); outcome.Outcome != gatewaycodex.HealthDispatchQueued {
		t.Fatalf("首次派发必须 queued，got %#v", outcome)
	}
	if _, err := db.Exec(`DROP TABLE account_health_probe_request_outbox`); err != nil {
		t.Fatalf("删除 outbox 表失败: %v", err)
	}
	outcome := writer.EnqueueProbeRequest(context.Background(), "acc-w1z", chainRequestFailureReason, "", nil)
	if outcome.Outcome != gatewaycodex.HealthDispatchRejected || outcome.DecisionCode != "dispatch_rejected" {
		t.Fatalf("INSERT 失败必须 dispatch_rejected，got %#v", outcome)
	}
}
