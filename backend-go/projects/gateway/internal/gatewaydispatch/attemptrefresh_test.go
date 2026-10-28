package gatewaydispatch

// T65-①（网关上游 401 自动刷新）引擎臂钉面：失败派发器返回
// retry_refreshed_same_account + 重新水合候选时，引擎以新凭据同账户重试一次——
// 上游恰两次调用、第二次带新 Authorization，请求最终成功；刷新重试后的再次
// 401 不再触发第二次刷新（派发器 once），也不吃 same-account retry 预算
//（401 非瞬态状态码，transient same-account retry 通道不参与），直接换下一候选。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// refreshDriver 按账户凭据构建 Authorization（镜像真实链 driver 的
// access_token → Bearer 形态），供断言两次尝试的凭据差异。
type refreshDriver struct {
	fakeDriver
}

func (f *refreshDriver) BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error) {
	header := http.Header{}
	token, _ := account.Credentials["access_token"].(string)
	header.Set("Authorization", "Bearer "+token)
	return PreparedRequestParts{
		Headers:              header,
		Body:                 []byte(`{"model":"gpt-test","input":"hi"}`),
		EffectiveServiceTier: "default",
	}, nil
}

// refreshScriptedDispatcher 第一次非 2xx 返回刷新重试动作（携带重新水合的
// 候选），其后返回 skip_account——镜像链面协调器的 once 语义。
type refreshScriptedDispatcher struct {
	refreshed     AccountCandidate
	handleFailed  atomic.Int64
	refreshes     atomic.Int64
	skips         atomic.Int64
	buildBodyOnce sync.Once
}

func (d *refreshScriptedDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	call := d.handleFailed.Add(1)
	if input.Response != nil && input.Response.Body != nil {
		_, _ = io.Copy(io.Discard, input.Response.Body)
		_ = input.Response.Body.Close()
	}
	if call == 1 {
		d.refreshes.Add(1)
		refreshed := d.refreshed
		return FailedUpstreamResponseResult{
			Action:                FailedResponseActionRetryRefreshedSameAccount,
			FailureKind:           "opaque_http",
			LastAttempt:           &UpstreamAttempt{AccountID: input.Account.ID, UpstreamURL: input.UpstreamURL, Status: 401, HasStatus: true},
			RefreshedAccount:      &refreshed,
			UpstreamStageRecorded: true,
		}, nil
	}
	d.skips.Add(1)
	return FailedResponseSkipAccountResult(input), nil
}

// FailedResponseSkipAccountResult 构造与真实派发器 skip 面同形的兜底结果。
func FailedResponseSkipAccountResult(input FailedUpstreamResponseInput) FailedUpstreamResponseResult {
	return FailedUpstreamResponseResult{
		Action:      FailedResponseActionSkipAccount,
		FailureKind: "opaque_http",
		LastAttempt: &UpstreamAttempt{AccountID: input.Account.ID, UpstreamURL: input.UpstreamURL, Status: input.Response.Status(), HasStatus: true},
	}
}

func (d *refreshScriptedDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	return UpstreamRequestErrorResult{
		Action:      FailedResponseActionSkipAccount,
		LastAttempt: &UpstreamAttempt{AccountID: input.Account.ID, UpstreamURL: input.UpstreamURL, Message: input.Error.Error()},
	}, nil
}

func (d *refreshScriptedDispatcher) IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool {
	return true
}

// newRefreshRetryEngine 复制 newTestEngine 的装配（fake 协作面），换用脚本化
// 派发器与凭据化 driver。
func newRefreshRetryEngine(driver *refreshDriver, dispatcher FailureDispatcher) *Engine {
	engine := NewEngine(driver, dispatcher)
	engine.Suppression = &fakeSuppression{}
	engine.Degradation = &fakeDegradation{}
	engine.Latency = &fakeLatency{}
	engine.ProxyHealth = &fakeProxyHealth{}
	engine.ClientIPAvoidance = &fakeAvoidance{}
	engine.ClientSourceAvoidance = &fakeClientSourceAvoidance{}
	engine.HotQuality = &fakeHotQuality{}
	engine.Affinity = &fakeAffinity{}
	engine.HighConcurrencyQueue = &fakeQueue{}
	engine.ClientIPConcurrency = &fakeClientIPConcurrency{}
	engine.Quota = &fakeQuota{}
	engine.Concurrency = &fakeConcurrencyStore{}
	engine.Cache = &fakeCache{}
	engine.Locks = &fakeLocks{}
	engine.Usage = &fakeUsage{}
	engine.KeyModelStore = gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(nil)
	return engine
}

func refreshRetryAccount(id, token string) AccountCandidate {
	account := AccountCandidate{
		ID:               id,
		Name:             "OAuth 刷新账户 " + id,
		Type:             "oauth",
		Status:           "active",
		ConcurrencyLimit: 4,
		Priority:         1,
		SupportedModels:  []string{"gpt-test"},
	}
	account.Credentials = map[string]any{"access_token": token, "refresh_token": "stored"}
	return account
}

// 401 → 刷新 → 同账户新凭据重试一次 → 200：上游恰两次调用，第二次带新
// Authorization，请求最终成功。
func TestRefreshedSameAccountRetryReplaysWithNewCredentials(t *testing.T) {
	var mu sync.Mutex
	authHeaders := []string{}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		call := calls
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		if call == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	defer server.Close()

	dispatcher := &refreshScriptedDispatcher{
		refreshed: refreshRetryAccount("a-1", "new-access-token"),
	}
	driver := &refreshDriver{}
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	engine := newRefreshRetryEngine(driver, dispatcher)

	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{refreshRetryAccount("a-1", "old-access-token")}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if !result.Response.OK() {
		t.Fatalf("retry must finally succeed, status = %d", result.Response.Status())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2（401 + 新凭据重试）", calls)
	}
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer old-access-token" || authHeaders[1] != "Bearer new-access-token" {
		t.Fatalf("auth headers = %#v, want old then new bearer token", authHeaders)
	}
	if dispatcher.refreshes.Load() != 1 || dispatcher.skips.Load() != 0 {
		t.Fatalf("dispatcher refreshes=%d skips=%d, want 1/0", dispatcher.refreshes.Load(), dispatcher.skips.Load())
	}
}

// 刷新重试后的再次 401 不再触发第二次刷新（once），不吃 401 的同账户重试
// 预算，直接换下一候选成功。
func TestRefreshedSameAccountRetryDoesNotLoopOnSecond401(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call <= 2 {
			// 首次 + 刷新重试各 401 一次。
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	defer server.Close()

	dispatcher := &refreshScriptedDispatcher{
		refreshed: refreshRetryAccount("a-1", "new-access-token"),
	}
	driver := &refreshDriver{}
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
		"a-2": {server.URL + "/v1/chat/completions"},
	}
	engine := newRefreshRetryEngine(driver, dispatcher)

	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{
		refreshRetryAccount("a-1", "old-access-token"),
		refreshRetryAccount("a-2", "backup-token"),
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if !result.Response.OK() {
		t.Fatalf("failover must finally succeed, status = %d", result.Response.Status())
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("final account = %s, want backup a-2", result.Account.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	// a-1 恰两次（401 + 刷新重试 401），重试后再 401 不吃同账户重试预算、
	// 不再刷新，直接换 a-2 一次成功。
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want exactly 3（a-1 401 + a-1 刷新重试 401 + a-2 成功）", calls)
	}
	if dispatcher.refreshes.Load() != 1 {
		t.Fatalf("refresh count = %d, want exactly 1（once）", dispatcher.refreshes.Load())
	}
	if dispatcher.skips.Load() != 1 {
		t.Fatalf("skip count = %d, want 1（重试后 401 直接 skip）", dispatcher.skips.Load())
	}
}
