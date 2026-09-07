package main

// BUG-0162 第五刀组合测试：两个执行端口（ManualBalanceRefresher /
// ModelCatalogRefresher）的进程内装配收口。
//  1. 组合根源码断言：compose.go 必须经
//     wireInProcessBalanceAndCatalogRefresh 装配（先于该装配行的
//     provider store 构造）；
//  2. SQLite fixture 生效断言：生产同款装配后目录刷新端口可达、余额手动
//     刷新端口保持 nil（SQLite 降级契约不变）；
//  3. 目录刷新 handler 闭环：登录会话经真实 kernel 路由
//     POST /accounts/model-catalog/refresh，prepareBalanceDraft → 进程内
//     端口 → mock 上游 /v1/models，返回 {addedModels,
//     recommendedHealthCheckModel} 并与本地 custom_provider_models 投影比对。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	platformaccountbalance "github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "modernc.org/sqlite"
)

// 组合根测试的 mock 上游监听 127.0.0.1；上游 Base URL 内网防护是进程级
// sync.Once 配置，test binary 初始化即放行（acceptance harness 的同名 env
// 先例；本包无断言该拒绝行为的测试，不受影响）。
func init() {
	_ = os.Setenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS", "true")
}

// TestComposeSystemAPIWiresBalanceAndCatalogRefresh pins the composition-root
// wiring line (装配断线零容忍：源码级断言复用既有先例).
func TestComposeSystemAPIWiresBalanceAndCatalogRefresh(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "wireInProcessBalanceAndCatalogRefresh(composed, cfg, accountStore, providerStore)"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the in-process balance and catalog refresh ports: %s", needle)
	}
	providerStorePos := strings.Index(text, "providerStore, err := providers.NewStore")
	wirePos := strings.Index(text, needle)
	if providerStorePos < 0 || wirePos < providerStorePos {
		t.Fatalf("balance and catalog refresh must be wired after the provider store construction")
	}
	wiring, err := os.ReadFile("compose_account_balance_refresh.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, handover := range []string{"SetManualBalanceRefresher(", "SetModelCatalogRefresher("} {
		if !strings.Contains(string(wiring), handover) {
			t.Fatalf("balance/catalog wiring file must hand over %s", handover)
		}
	}
}

// TestInProcessRefreshPortsSQLiteFixture pins the SQLite degradation contract:
// the catalog refresher is wired in both modes (no store dependency), the
// manual balance refresher stays nil (SQLite 没有 juhe_jobs 租约表，路由维持
// 既有 500/降级快照契约；余额手动刷新为 PG 模式能力).
func TestInProcessRefreshPortsSQLiteFixture(t *testing.T) {
	_, accountStore := composeBalanceRefreshFixture(t)
	if accountStore.ModelCatalogRefresherPort() == nil {
		t.Fatal("模型目录刷新端口必须在 SQLite 模式装配（无 store 依赖）")
	}
	if accountStore.BalanceRefresherPort() != nil {
		t.Fatal("SQLite 模式余额手动刷新端口必须保持 nil（余额手动刷新为 PG 模式能力）")
	}
}

// composeBalanceRefreshFixture is the production-same SQLite composition; the
// two ports are wired exactly like compose.go does over a fresh account store.
func composeBalanceRefreshFixture(t *testing.T) (*composition, *accounts.Store) {
	t.Helper()
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	t.Cleanup(closeAudit)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	t.Cleanup(func() { composed.Shutdown() })
	accountStore, err := accounts.NewStore(composed.DB, false, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	providerStore, err := providers.NewStore(composed.DB, false, time.Now)
	if err != nil {
		t.Fatalf("providers store: %v", err)
	}
	// 生产同款装配（compose.go 同调用）。
	if err := wireInProcessBalanceAndCatalogRefresh(composed, cfg, accountStore, providerStore); err != nil {
		t.Fatalf("wire in-process balance and catalog refresh: %v", err)
	}
	return composed, accountStore
}

// TestModelCatalogRefreshHandlerClosedLoop drives the real kernel route with
// an authenticated session down to the mock upstream models endpoint.
func TestModelCatalogRefreshHandlerClosedLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("composition closed-loop test skipped in -short mode")
	}
	composed, _ := composeBalanceRefreshFixture(t)
	seedSystemSettings(t, composed.DB)
	server := httptest.NewServer(composed.Kernel)
	t.Cleanup(server.Close)

	upstreamModels := `{"data":[{"id":"mock-model-a"},{"id":"mock-model-b"},{"id":"mock-model-a"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-catalog-e2e" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamModels))
	}))
	t.Cleanup(upstream.Close)

	db := openComposeBusinessDB(t, composed)

	// Admin session (captcha disabled contract, same as the auth surface).
	// 创建即带默认内建分组（gpt 默认分组由 ensureDefaultGroups 预置）。
	adminID := createComposeSessionAdmin(t, composed)
	groupID := seedCatalogRefreshDraftFixtures(t, db, adminID)

	// 本地目录投影：custom_provider_models 全局 active 模型（chat_completions
	// family 与 openai 协议档案匹配）。mock-model-a 上游可见且不在支持列表 →
	// addedModels；mock-model-configured 已在支持列表 → 不进 addedModels；
	// mock-model-upstream-only 仅上游可见、不在本地目录 → 不进 addedModels。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	models := []struct {
		id, model string
	}{
		{id: "cpm-a", model: "mock-model-a"},
		{id: "cpm-b", model: "mock-model-configured"},
	}
	for _, item := range models {
		if _, err := db.Exec(`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id,
			status, mode, supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			created_by, created_at, updated_at)
			VALUES (?, 'gpt', ?, 'global', NULL, 'active', NULL, '["chat_completions"]', '[]', '[]', 'catalog-admin', ?, ?)`,
			item.id, item.model, now, now); err != nil {
			t.Fatalf("seed custom model: %v", err)
		}
	}

	cookies := loginComposeSession(t, server.URL, "catalog-admin")

	refresh := func() map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"目录闭环",`+
			`"type":"api_key","groupId":%q,"credentials":{"api_key":"sk-catalog-e2e","base_url":%q},`+
			`"supportedModels":["mock-model-configured"],`+
			`"healthCheckModel":"mock-model-configured","healthCheckEndpointMode":"chat_json"}}`, groupID, upstream.URL+"/v1")
		request, err := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/accounts/model-catalog/refresh",
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload struct {
			Data    map[string]any `json:"data"`
			Message string         `json:"message"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("model-catalog refresh status=%d message=%q decodeErr=%v", response.StatusCode, payload.Message, decodeErr)
		}
		if decodeErr != nil || payload.Data == nil {
			t.Fatalf("refresh payload decode: %v data=%v", decodeErr, payload.Data)
		}
		return payload.Data
	}

	// 第一轮：上游 {mock-model-a, mock-model-b}（mock-model-a 去重）。
	result := refresh()
	added, _ := result["addedModels"].([]any)
	if len(added) != 1 || added[0] != "mock-model-a" {
		t.Fatalf("addedModels = %v, want [mock-model-a]（上游∩本地目录-已选）", added)
	}
	// 已配置 mock-model-configured 不在上游 → 推荐取第一候选 mock-model-a。
	if result["recommendedHealthCheckModel"] != "mock-model-a" {
		t.Fatalf("recommendedHealthCheckModel = %v, want mock-model-a", result["recommendedHealthCheckModel"])
	}

	// 第二轮：上游收窄为 {mock-model-configured} → addedModels 空，已配置
	// 模型在候选中 → 推荐保持配置值。
	upstreamModels = `{"data":[{"id":"mock-model-configured"}]}`
	result = refresh()
	if added, _ := result["addedModels"].([]any); len(added) != 0 {
		t.Fatalf("second addedModels = %v, want empty", added)
	}
	if result["recommendedHealthCheckModel"] != "mock-model-configured" {
		t.Fatalf("second recommendation = %v, want configured model kept", result["recommendedHealthCheckModel"])
	}
}

// openComposeBusinessDB opens the fixture business database for direct row
// seeding (same file the composed system API uses).
func openComposeBusinessDB(t *testing.T, composed *composition) *sql.DB {
	t.Helper()
	// composed.DB is the shared business handle; tests reuse it directly.
	return composed.DB
}

// createComposeSessionAdmin creates one admin account and returns its system
// account ID (auth surface captcha-disabled contract).
func createComposeSessionAdmin(t *testing.T, composed *composition) string {
	t.Helper()
	mustChangePasswordFlag := false
	created, err := composed.authDeps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: "catalog-admin", DisplayName: "catalog-admin_name", Password: "catalog-admin-password-123",
		Role:               "admin",
		MustChangePassword: &mustChangePasswordFlag,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return created.ID
}

// loginComposeSession logs the admin in and returns the session cookies.
func loginComposeSession(t *testing.T, serverURL, username string) []*http.Cookie {
	t.Helper()
	login, err := http.DefaultClient.Post(serverURL+"/__aisys__/api/auth/login", "application/json",
		strings.NewReader(fmt.Sprintf(`{"username":%q,"password":"catalog-admin-password-123"}`, username)))
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	cookies := login.Cookies()
	_ = login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("admin login status=%d", login.StatusCode)
	}
	return cookies
}

// newAdapterBalanceRefresher builds the production adapter over an isolated
// SQLite execution store and a canned upstream response (the production
// wiring is PG-only; the adapter mapping itself is mode-independent).
func newAdapterBalanceRefresher(t *testing.T, ownerID, upstreamBody string, holdOwnerLease bool) (*gatewayManualBalanceRefresher, *platformaccountbalance.Store) {
	t.Helper()
	const secret = "gateway-adapter-secret"
	store, err := platformaccountbalance.OpenStore(platformaccountbalance.StoreConfig{
		Mode: platformaccountbalance.StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "adapter-balance.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if holdOwnerLease {
		lease, acquired, err := store.AcquireOwnerLease(context.Background(), "periodic-owner", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("hold owner lease: %v %t", err, acquired)
		}
		t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	}
	runner, err := platformaccountbalance.NewRunner(platformaccountbalance.RunnerConfig{
		Store:            store,
		OwnerID:          ownerID,
		CredentialSecret: secret,
		HTTPClient:       &cannedJSONDoer{body: upstreamBody},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &gatewayManualBalanceRefresher{db: nil, pg: false, secret: secret, store: store, runner: runner, now: time.Now}, store
}

// adapterCandidate builds one manual-refresh candidate with the given input
// version (dispatch_revision) and credentials envelope.
func adapterCandidate(t *testing.T, secret, baseURL string, dispatchRevision int64) accounts.BalanceRefreshCandidate {
	t.Helper()
	envelope, err := accounts.EncryptJSON(secret, accounts.Credentials{
		"api_key": "sk-adapter", "base_url": baseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return accounts.BalanceRefreshCandidate{
		ID: "acct-adapter", SystemAccountID: "sys-adapter", ProviderCode: "openai", Type: "api_key",
		Status: "active", Schedulable: true, ConfigRevision: 1, DispatchRevision: dispatchRevision,
		CredentialsEnvelope: envelope, ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`,
	}
}

// TestGatewayManualBalanceRefresherOutcomeMapping covers the production
// adapter over the shared execution core:
// committed → Persisted snapshot; held owner lease → lease_busy（路由 409
// 余额查询正在进行）；older input vs newer snapshot → stale（路由 409 配置已
// 变化）。测试辅助的第二个测试覆盖 draft 探测与多 Key 拒绝。
func TestGatewayManualBalanceRefresherOutcomeMapping(t *testing.T) {
	if testing.Short() {
		t.Skip("adapter mapping test skipped in -short mode")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unit":"USD","remaining":"12.5"}`))
	}))
	t.Cleanup(upstream.Close)

	// committed：成功执行并回读不可变 outcome 快照。
	refresher, _ := newAdapterBalanceRefresher(t, "adapter-owner", `{"unit":"USD","remaining":"12.5"}`, false)
	//凭据密文以适配器自身 secret 加封。
	candidate := adapterCandidate(t, "gateway-adapter-secret", upstream.URL, 1)
	outcome, err := refresher.RefreshManual(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Persisted || outcome.Outcome != "committed" {
		t.Fatalf("committed outcome: %+v err=%v", outcome, err)
	}
	if outcome.Snapshot["status"] != "fresh" || outcome.Snapshot["remainingUsd"] != "12.500000" {
		t.Fatalf("committed snapshot: %v", outcome.Snapshot)
	}

	// lease_busy：周期 owner 持有 owner lease → Skipped → lease_busy。
	busyRefresher, _ := newAdapterBalanceRefresher(t, "adapter-owner", `{"unit":"USD","remaining":"1"}`, true)
	busyOutcome, err := busyRefresher.RefreshManual(context.Background(), adapterCandidate(t, "gateway-adapter-secret", upstream.URL, 1))
	if err != nil {
		t.Fatal(err)
	}
	if busyOutcome.Persisted || busyOutcome.Outcome != "lease_busy" {
		t.Fatalf("lease_busy outcome: %+v", busyOutcome)
	}

	// stale：持久化快照 input_version=2 高于手动输入 1 → CAS 拒绝 → stale。
	staleRefresher, staleStore := newAdapterBalanceRefresher(t, "adapter-owner", `{"unit":"USD","remaining":"1"}`, false)
	if err := seedNewerAdapterSnapshot(t, staleRefresher, staleStore); err != nil {
		t.Fatal(err)
	}
	staleOutcome, err := staleRefresher.RefreshManual(context.Background(), adapterCandidate(t, "gateway-adapter-secret", upstream.URL, 1))
	if err != nil {
		t.Fatal(err)
	}
	if staleOutcome.Persisted || staleOutcome.Outcome != "stale" {
		t.Fatalf("stale outcome: %+v", staleOutcome)
	}
}

// TestGatewayManualBalanceRefresherTestDraft covers the non-persisted draft
// probe against the mock upstream (status fresh) and the multi-Key rejection
// (the route renders the failed-snapshot 200 shape from the error).
func TestGatewayManualBalanceRefresherTestDraft(t *testing.T) {
	if testing.Short() {
		t.Skip("draft probe test skipped in -short mode")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unit":"USD","remaining":"7.25"}`))
	}))
	t.Cleanup(upstream.Close)

	refresher, _ := newAdapterBalanceRefresher(t, "draft-owner", "", false)
	snapshot, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-draft", "base_url": upstream.URL},
		Config:      map[string]any{"adapter": "builtin", "intervalMinutes": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["status"] != "fresh" || snapshot["remainingUsd"] != "7.250000" {
		t.Fatalf("draft snapshot: %v", snapshot)
	}

	if _, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_keys": []any{"sk-1", "sk-2"}, "base_url": upstream.URL},
		Config:      map[string]any{"adapter": "builtin", "intervalMinutes": 5},
	}); err == nil || err.Error() != platformaccountbalance.MultiKeyMessage {
		t.Fatalf("multi-key draft must reject with the shared message, got %v", err)
	}
}

// seedNewerAdapterSnapshot commits a newer snapshot (input_version=2) through
// the shared lease machinery so the manual input (1) hits the CAS.
func seedNewerAdapterSnapshot(t *testing.T, refresher *gatewayManualBalanceRefresher, store *platformaccountbalance.Store) error {
	t.Helper()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "snapshot-seeder", time.Minute)
	if err != nil || !acquired {
		return fmt.Errorf("seeder owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), owner) }()
	account, acquired, err := store.AcquireAccountLease(ctx, owner, "acct-adapter", time.Minute)
	if err != nil || !acquired {
		return fmt.Errorf("seeder account lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseAccountLease(context.Background(), owner, account) }()
	now := time.Now().UTC()
	return store.AppendOutcome(ctx, owner, account, platformaccountbalance.Outcome{
		OutcomeID: "seed-outcome", RequestID: "seed-request", AccountID: "acct-adapter",
		SystemAccountID: "sys-adapter", InputVersion: 2, ConfigRevision: 1,
		Trigger: platformaccountbalance.TriggerPeriodic, ObservedAt: now,
		Snapshot: platformaccountbalance.Snapshot{Status: platformaccountbalance.StatusFresh, RemainingUSD: "9.99"},
	})
}

// cannedJSONDoer is the injectable HTTPDoer standing in for the upstream
// balance endpoint (Mock 优先规范：结果可回放、稳定).
type cannedJSONDoer struct{ body string }

func (d *cannedJSONDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{},
	}, nil
}
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at) VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, ?, ?)", []any{now, now}},
		{"INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at) VALUES ('prof-gpt', 'gpt', 'OpenAI 官方协议', 1, 'openai', 'v1', 'https://api.openai.com/v1', 'mock-model-configured', '[\"api_key\"]', '[]', ?, ?)", []any{now, now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed catalog fixture: %v", err)
		}
	}
	var groupID string
	if err := db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1
		ORDER BY updated_at DESC, id ASC LIMIT 1`, adminID).Scan(&groupID); err != nil {
		t.Fatalf("locate default gpt group: %v", err)
	}
	return groupID
}
