package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// 组合根注入端口与 codex 会话解析的收尾单测。

// ---------------------------------------------------------------------------
// oauthnormalizer_overrides 注入端口
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// codex 会话解析
// ---------------------------------------------------------------------------

func TestResolveOpenAIOAuthCodexSession(t *testing.T) {
	headers := http.Header{
		"Session-Id":         []string{"sess-1"},
		"Thread-Id":          []string{"thread-1"},
		"X-Prompt-Cache-Key": []string{"cache-hdr"},
	}
	body := map[string]any{}
	identity := OpenAIOAuthCodexIdentity{SystemAccountID: "sys", APIKeyID: "key"}
	session := resolveOpenAIOAuthCodexSession(headers, body, OpenAIOAuthCodexAccount{ID: "a-1"}, identity)
	if session.SessionID == "" || session.ConversationID == "" {
		t.Fatalf("session = %#v", session)
	}
	if session.PromptCacheKey == "" {
		t.Fatal("prompt cache key 回退到主标识")
	}
	// body 中的 prompt_cache_key 优先于头。
	body = map[string]any{"prompt_cache_key": "cache-body"}
	session = resolveOpenAIOAuthCodexSession(headers, body, OpenAIOAuthCodexAccount{ID: "a-1"}, identity)
	if session.PromptCacheKey == "" {
		t.Fatal("body 缓存键应生效")
	}
	// 无头无 body → 空会话。
	empty := resolveOpenAIOAuthCodexSession(nil, map[string]any{}, OpenAIOAuthCodexAccount{ID: "a-1"}, identity)
	if empty.SessionID != "" || empty.ConversationID != "" || empty.PromptCacheKey != "" {
		t.Fatalf("empty session = %#v", empty)
	}
	// 同输入隔离摘要稳定且不同输入不同。
	isolated := IsolateOpenAIOAuthCodexSessionID("sess-1", OpenAIOAuthCodexAccount{ID: "a-1"}, identity)
	if isolated == "" || len(isolated) != 32 {
		t.Fatalf("isolated = %q", isolated)
	}
	if isolated != IsolateOpenAIOAuthCodexSessionID("sess-1", OpenAIOAuthCodexAccount{ID: "a-1"}, identity) {
		t.Fatal("隔离摘要必须稳定")
	}
	if isolated == IsolateOpenAIOAuthCodexSessionID("sess-2", OpenAIOAuthCodexAccount{ID: "a-1"}, identity) {
		t.Fatal("不同输入必须产生不同摘要")
	}
	if IsolateOpenAIOAuthCodexSessionID("  ", OpenAIOAuthCodexAccount{ID: "a-1"}, identity) != "" {
		t.Fatal("空白输入返回空")
	}
}

func TestApplyOpenAIOAuthCodexSessionToBody(t *testing.T) {
	session := OpenAIOAuthCodexSessionResolution{PromptCacheKey: "cache-1"}
	body := map[string]any{"session_id": "s", "conversation_id": "c", "prompt_cache_key": "old"}
	applyOpenAIOAuthCodexSessionToBody(body, session, false)
	if _, ok := body["session_id"]; ok {
		t.Fatal("session_id 应删除")
	}
	if _, ok := body["conversation_id"]; ok {
		t.Fatal("conversation_id 应删除")
	}
	if body["prompt_cache_key"] != "cache-1" {
		t.Fatalf("prompt cache = %#v", body["prompt_cache_key"])
	}
	compact := map[string]any{"prompt_cache_key": "old"}
	applyOpenAIOAuthCodexSessionToBody(compact, session, true)
	if _, ok := compact["prompt_cache_key"]; ok {
		t.Fatal("compact 模式应删除 prompt_cache_key")
	}
}

// ---------------------------------------------------------------------------
// accountpreparation 请求侧清洗 + 代理不可用状态分支
// ---------------------------------------------------------------------------

func TestSanitizeCodexResponsesHistoryForAccountOnRequest(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	req.Body = newTestRequestBody(t, `{"model":"gpt-test","input":[{"role":"user"}]}`)
	// 非 codex_responses 不处理。
	engine.sanitizeCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], "")
	parsed := mustJSONObject(t, `{"model":"gpt-test","input":[{"role":"user"}]}`)
	_ = parsed
	// 无 sanitizer 不处理。
	engine.sanitizeCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], "codex_responses")
	// 注入 sanitizer 后请求 body 被替换。
	previous := gatewayoauthcodex.SanitizeCodexHistory
	gatewayoauthcodex.SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		return CodexHistorySanitizeResult{Items: []any{"req-sanitized"}, Changed: true}
	}
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = previous })
	engine.sanitizeCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], "codex_responses")
	if req.Body.Body.(map[string]any)["input"].([]any)[0] != "req-sanitized" {
		t.Fatalf("req body = %#v", req.Body.Body)
	}
	// input 非数组不处理。
	req.Body.Body = map[string]any{"input": "text"}
	engine.sanitizeCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], "codex_responses")
	if req.Body.Body.(map[string]any)["input"] != "text" {
		t.Fatal("input 非数组保持原样")
	}
}

// 原 recordingAccountState fake 与 TestHandleUnavailableProxyProfileRecordsPrecheck
// 断言的软阻断行为已随 engine.AccountState 端口删除（生产组合根按设计不
// 装配，分支受 nil 守卫从未执行）；本用例改断言当前语义。
func TestHandleUnavailableProxyProfileRecordsFailure(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	unavailable := true
	message := "代理维护中"
	account := testAccounts("a-1")[0]
	account.ProxyProfileUnavailable = &unavailable
	account.ProxyProfileErrorMessage = &message
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	usage := testUsageContext()
	usage.TrafficSource = "gateway"
	capture := AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}}
	sink := capture.Sink.(*fakeAuditSink)
	attempt, err := engine.HandleUnavailableProxyProfile(
		context.Background(), req, usage, account, fastDispatchSettings(),
		map[string]string{}, capture, 2,
	)
	if err != nil {
		t.Fatalf("HandleUnavailableProxyProfile: %v", err)
	}
	if attempt == nil || attempt.UpstreamURL != "proxy:configured" || attempt.Message != "代理维护中" {
		t.Fatalf("attempt = %#v", attempt)
	}
	if sink.failed != 1 {
		t.Fatalf("审计失败记录 = %d", sink.failed)
	}
	// usage 记录失败 → 错误透传。
	engine.Usage = errorUsageRecorder{}
	if _, err := engine.HandleUnavailableProxyProfile(
		context.Background(), req, usage, account, fastDispatchSettings(),
		map[string]string{}, capture, 4,
	); err == nil || !strings.Contains(err.Error(), "记录失败") {
		t.Fatalf("usage 错误应透传, got %v", err)
	}
}

// errorUsageRecorder 的失败记录恒报错。
type errorUsageRecorder struct{}

func (errorUsageRecorder) RecordFailedUpstreamAttempt(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account AccountCandidate, record FailedAttemptRecord) error {
	return errors.New("记录失败")
}

// ---------------------------------------------------------------------------
// 首字截止装配下的失败响应（协调器抑制分支）
// ---------------------------------------------------------------------------

func TestDispatchFailedResponseSupersedesCoordinator(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	// speed-first 首字截止只作用于流式请求（非流式已豁免）。
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.NormalRouteFirstByteConfig = &gatewayrouting.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  30_000,
	}
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); err == nil {
		t.Fatal("失败响应必须以错误收尾")
	}
}
