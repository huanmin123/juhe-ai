package main

// Gemini interactions 亲和写面链级集成测试（审计 P1：G04 迁移时链面零调用）：
// create 2xx → 提交前 Remember（GET 资源命中创建账户而非 404）；DELETE 2xx →
// 提交前 Delete（映射移除，GET 随后 404）；亲和存储写失败 → 503
// interaction_affinity_unavailable（下游不提交；Node routes.ts:1621 契约）。
import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

const interactionFixtureJSON = `{"id":"ix_e2e","name":"interactions/ix_e2e","status":"completed","steps":[]}`

// interactionUpstreamRecorder 记录链转发到上游的请求（method+path）。
type interactionUpstreamRecorder struct {
	mu       sync.Mutex
	requests []string
}

func (r *interactionUpstreamRecorder) record(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, method+" "+path)
}

func (r *interactionUpstreamRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// newInteractionsUpstream 手写 interactions mock：create/GET 回 200 JSON，
// DELETE 回 204 空体（《Gemini账号接入.md》:189 契约）。
func newInteractionsUpstream(t *testing.T, recorder *interactionUpstreamRecorder) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Method, r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(interactionFixtureJSON))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta/interactions/ix_e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(interactionFixtureJSON))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1beta/interactions/ix_e2e":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// seedInteractionGeminiAccount 种 gemini interactions 分组/账户/路由/Key
// （seedMediaGeminiVideoAccount 同构；端点模式 interactions_json/sse，
// 模型 gemini-3-pro），返回网关 API Key 明文。
func seedInteractionGeminiAccount(t *testing.T, fixture *chainFixture, id, baseURL string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed gemini interactions row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-gemini-interactions"
	credentials := map[string]any{
		"api_key":                  "sk-upstream-gemini-int",
		"base_url":                 baseURL,
		"supported_endpoint_modes": []string{"interactions_json", "interactions_sse"},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_gem_int', ?, 'gemini', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, vendor_code, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'gemini', 'gemini', 'profile_gemini_native_v1beta', 'gemini', 'v1beta', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_gem_int', ?, ?, 1, '2026-10-14T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gem_int', 'gemini', 'gemini-3-pro', 'gemini-3-pro', 'global', 'active', '["interactions_json","interactions_sse"]', '2026-10-14T00:00:00.000Z', '2026-10-14T00:00:00.000Z')`)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, model_id, created_at)
		VALUES (?, 'gemini', 'gemini-3-pro', 'mdl_gem_int', '2026-10-14T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 改连接行（credentials.base_url 不再消费）。
	chainSeedAccountConnection(t, fixture.db, id, baseURL)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_gem_int', ?, 'gemini交互', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_gem_int', 'rs_gem_int', ?, 'group_gem_int', 0, 1, 'active', '2026-10-14T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_gem_int', ?, 'rs_gem_int', 'gemini交互Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-14T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

func doInteractionRequest(t *testing.T, client *http.Client, method, url, keySecret, body string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+keySecret)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	payload, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return response, string(payload)
}

// create 2xx → Remember（GET 命中创建账户）；DELETE 2xx → Delete（GET 随后
// 404 interaction_affinity_not_found）。
func TestChainInteractionAffinityLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder := &interactionUpstreamRecorder{}
	upstream := newInteractionsUpstream(t, recorder)
	keySecret := seedInteractionGeminiAccount(t, fixture, "acc_gem_int", upstream.URL)

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}

	// 1) create 2xx → 提交前 Remember（修复前链面零调用，后续资源访问必 404）。
	createResponse, createBody := doInteractionRequest(t, client, http.MethodPost, server.URL+"/v1beta/interactions", keySecret, `{"model":"gemini-3-pro","stream":false}`)
	if createResponse.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createResponse.StatusCode, createBody)
	}

	// 2) GET 资源 → 亲和命中创建账户（非 404）。
	getResponse, getBody := doInteractionRequest(t, client, http.MethodGet, server.URL+"/v1beta/interactions/ix_e2e", keySecret, "")
	if getResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET 资源应命中亲和: status=%d body=%s", getResponse.StatusCode, getBody)
	}

	// 3) DELETE 2xx（204 空体）→ 提交前 Delete。
	deleteResponse, deleteBody := doInteractionRequest(t, client, http.MethodDelete, server.URL+"/v1beta/interactions/ix_e2e", keySecret, "")
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", deleteResponse.StatusCode, deleteBody)
	}

	// 4) 再 GET → 映射已移除：404 interaction_affinity_not_found。
	goneResponse, goneBody := doInteractionRequest(t, client, http.MethodGet, server.URL+"/v1beta/interactions/ix_e2e", keySecret, "")
	if goneResponse.StatusCode != http.StatusNotFound || !strings.Contains(goneBody, "interaction_affinity_not_found") {
		t.Fatalf("DELETE 后 GET 应 404 亲和缺失: status=%d body=%s", goneResponse.StatusCode, goneBody)
	}

	// 上游轨迹：POST/GET/DELETE 各一次（第 4 步 404 不发上游）。
	requests := recorder.snapshot()
	want := []string{"POST /v1beta/interactions", "GET /v1beta/interactions/ix_e2e", "DELETE /v1beta/interactions/ix_e2e"}
	if len(requests) != len(want) {
		t.Fatalf("上游请求=%v，期望 %v", requests, want)
	}
	for index, item := range want {
		if requests[index] != item {
			t.Fatalf("上游请求[%d]=%q，期望 %q（全量 %v）", index, requests[index], item, requests)
		}
	}
}

type interactionAffinityFailingStore struct{}

func (interactionAffinityFailingStore) GetJSON(_ context.Context, _ string, _ any) (bool, error) {
	return false, nil
}

func (interactionAffinityFailingStore) SetJSON(_ context.Context, _ string, _ any, _ time.Duration) error {
	return errors.New("affinity store down")
}

func (interactionAffinityFailingStore) Delete(_ context.Context, _ string) error {
	return errors.New("affinity store down")
}

// 亲和存储写失败 → 503 interaction_affinity_unavailable（Node routes.ts:1621
// rethrow 契约）；上游创建已发生，但下游响应不提交。
func TestChainInteractionAffinityWriteFailure503(t *testing.T) {
	fixture := newChainFixture(t)
	recorder := &interactionUpstreamRecorder{}
	upstream := newInteractionsUpstream(t, recorder)
	keySecret := seedInteractionGeminiAccount(t, fixture, "acc_gem_int", upstream.URL)

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	deps.Affinity = gatewaygemini.NewInteractionAffinity(interactionAffinityFailingStore{})
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}

	createResponse, createBody := doInteractionRequest(t, client, http.MethodPost, server.URL+"/v1beta/interactions", keySecret, `{"model":"gemini-3-pro","stream":false}`)
	if createResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("写失败应 503: status=%d body=%s", createResponse.StatusCode, createBody)
	}
	if !strings.Contains(createBody, "interaction_affinity_unavailable") {
		t.Fatalf("503 应带 interaction_affinity_unavailable: %s", createBody)
	}
	if requests := recorder.snapshot(); len(requests) != 1 {
		t.Fatalf("上游应已收到创建请求（写失败发生在成功后）: %v", requests)
	}
}
