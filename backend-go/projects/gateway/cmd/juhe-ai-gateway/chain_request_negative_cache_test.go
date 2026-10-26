package main

// L2 请求负面缓存回归测试（请求级提前耗尽与负面缓存设计 §5/§9）：
//
//   - 运行态：TTL 过期读删（10 分钟固定不续期）、容量上限 10000 拒绝新写入；
//   - 派发器集成：L1 触发后懒写入；后续重试请求第一次候选拒绝即命中并中止
//     （本次引擎调用最多撞 1 只上游账户）；来源键缺失 / invalid、body 超 1MB
//     不写不查；不同来源会话互不影响；审计标记 request_level_fast_exhausted
//     在命中路径同样落库。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func fakeNegativeCacheClock() (func() int64, *int64) {
	now := int64(1_000_000)
	return func() int64 { return now }, &now
}

// TestChainRequestNegativeCacheTTLExpiryAndNoRenewal：TTL 内命中；过期后读删
// （读即失效且条目删除）；命中不续期——剩余寿命随时间自然耗尽。
func TestChainRequestNegativeCacheTTLExpiryAndNoRenewal(t *testing.T) {
	clock, now := fakeNegativeCacheClock()
	cache := newChainRequestNegativeCache(clock)
	cache.set("k")
	if !cache.get("k") {
		t.Fatal("entry must hit within TTL")
	}
	*now += requestNegativeCacheTTLms - 1
	if !cache.get("k") {
		t.Fatal("entry must still hit just before expiry")
	}
	if cache.lenForTest() != 1 {
		t.Fatalf("hits must not delete a live entry: %d", cache.lenForTest())
	}
	*now += 1
	if cache.get("k") {
		t.Fatal("entry must expire after the fixed TTL")
	}
	if cache.lenForTest() != 0 {
		t.Fatalf("expired entry must be deleted on read: %d", cache.lenForTest())
	}
}

// TestChainRequestNegativeCacheCapacityRejectsWrites：容量上限 10000——达到
// 上限后新键写入被拒（已存在键续写仍允许，不放大占用）。
func TestChainRequestNegativeCacheCapacityRejectsWrites(t *testing.T) {
	clock, _ := fakeNegativeCacheClock()
	cache := newChainRequestNegativeCache(clock)
	for i := 0; i < requestNegativeCacheMaxEntries; i++ {
		if !cache.set("k" + string(rune(i%128)) + string(rune(i/128))) {
			t.Fatalf("write %d below capacity must succeed", i+1)
		}
	}
	if cache.lenForTest() != requestNegativeCacheMaxEntries {
		t.Fatalf("len=%d want %d", cache.lenForTest(), requestNegativeCacheMaxEntries)
	}
	if cache.set("overflow-key") {
		t.Fatal("write above capacity must be rejected")
	}
	if cache.get("overflow-key") {
		t.Fatal("rejected write must not be readable")
	}
	if !cache.set("k" + string(rune(0)) + string(rune(0))) {
		t.Fatal("existing key renewal must stay allowed at capacity")
	}
}

// negativeCacheRequest 构造带模型（gemini 路径形态）与原始 body 的网关请求
// （rawBodySnapshotOf 读 req.Body.RawBody——生产由 body 管道附加，单测直接
// 挂载），保证来源解析与指纹计算拿到真实请求事实。
func negativeCacheRequest(t *testing.T, body string, clientIP string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-test:generateContent", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(raw)
	req.Body = &gatewaybody.Request{RawBody: []byte(body)}
	req.ClientIP = clientIP
	return req
}

// newNegativeCacheDispatcher 装配带来源解析与 L2 运行态的失败派发器。
func newNegativeCacheDispatcher(t *testing.T) (*chainFailureDispatcher, *chainRequestNegativeCache, func() int64, *int64) {
	t.Helper()
	clock, now := fakeNegativeCacheClock()
	cache := newChainRequestNegativeCache(clock)
	dispatcher := &chainFailureDispatcher{
		clientStrategy: &gatewaycodex.ClientStrategyDeps{
			Source: &gatewaycodex.SourceIdentityResolver{Secret: "unit-secret"},
		},
		negativeCache: cache,
	}
	return dispatcher, cache, clock, now
}

// negativeCacheGatewayInput 构造 gateway 流量的候选拒绝输入（带客户端身份
// 与请求体，供来源解析与指纹计算）。
func negativeCacheGatewayInput(t *testing.T, dispatcher *chainFailureDispatcher, req *gatewaypreauth.GatewayRequest, sink *failureDispatchAuditSink, accountID string) gatewaydispatch.FailedUpstreamResponseInput {
	t.Helper()
	response := failureDispatchUpstreamResponse(t, http.StatusBadRequest, "application/json", `{"error":"rejected"}`)
	input := requestFastExhaustInput(response, sink, accountID)
	input.Req = req
	input.UsageContext.SystemAccountID = "sys_owner"
	input.UsageContext.APIKeyID = "key_1"
	input.UsageContext.GroupID = "group_main"
	input.UsageContext.Endpoint = "POST /v1/chat/completions"
	input.UsageContext.ClientIP = "10.0.0.1"
	return input
}

// requestFastExhaustContext 为每次调用返回独立请求 ctx（每次调用是新的
// 单次客户端调用，计数器随之新建）。
func requestFastExhaustContext() context.Context {
	return withRequestFastExhaustState(context.Background())
}

// TestChainFailureDispatcherNegativeCacheWriteThenHit：第一次调用 L1 触发后
// 写缓存；后续重试调用第一次候选拒绝即命中——返回既有耗尽出口形状错误并落
// request_level_fast_exhausted（negativeCacheHit）标记。
func TestChainFailureDispatcherNegativeCacheWriteThenHit(t *testing.T) {
	dispatcher, cache, _, _ := newNegativeCacheDispatcher(t)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`

	// 第一次调用：3 账户 5 次触发 L1 → 懒写入。
	firstCtx := requestFastExhaustContext()
	for i := 0; i < requestFastExhaustMinTotalAttempts; i++ {
		req := negativeCacheRequest(t, body, "10.0.0.1")
		account := "acc_1"
		if i > 0 {
			account = "acc_2"
		}
		if i > 3 {
			account = "acc_3"
		}
		_, err := dispatcher.HandleFailedUpstreamResponse(firstCtx,
			negativeCacheGatewayInput(t, dispatcher, req, &failureDispatchAuditSink{}, account))
		if i < requestFastExhaustMinTotalAttempts-1 && err != nil {
			t.Fatalf("first-call attempt %d must not abort: %v", i+1, err)
		}
		if i == requestFastExhaustMinTotalAttempts-1 && err == nil {
			t.Fatal("first call must reach the L1 fast-exhaust trigger")
		}
	}
	if cache.lenForTest() != 1 {
		t.Fatalf("L1 trigger must write exactly one negative-cache entry: %d", cache.lenForTest())
	}

	// 第二次调用（同一来源、同一请求内容）：第一次候选拒绝即命中。
	secondCtx := requestFastExhaustContext()
	secondSink := &failureDispatchAuditSink{}
	_, err := dispatcher.HandleFailedUpstreamResponse(secondCtx,
		negativeCacheGatewayInput(t, dispatcher, negativeCacheRequest(t, body, "10.0.0.1"), secondSink, "acc_other"))
	var attemptErr *gatewaydispatch.UpstreamAttemptError
	if !errors.As(err, &attemptErr) {
		t.Fatalf("second call first rejection must abort via L2 hit: %v", err)
	}
	found := false
	for _, entry := range secondSink.metadata {
		if entry.label == "request_level_fast_exhausted" && entry.metadata["negativeCacheHit"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("L2 hit must carry the request_level_fast_exhausted marker: %+v", secondSink.metadata)
	}
	if len(attemptErr.FailedAccountIDs) != 1 || attemptErr.FailedAccountIDs[0] != "acc_other" {
		t.Fatalf("L2 hit failedAccountIds = %v want only the single attempted account", attemptErr.FailedAccountIDs)
	}

	// 不同 body（同来源会话不同内容）不命中：完整 L1 流程继续。
	thirdCtx := requestFastExhaustContext()
	otherBody := `{"model":"gpt-test","messages":[{"role":"user","content":"别的内容"}]}`
	_, err = dispatcher.HandleFailedUpstreamResponse(thirdCtx,
		negativeCacheGatewayInput(t, dispatcher, negativeCacheRequest(t, otherBody, "10.0.0.1"), &failureDispatchAuditSink{}, "acc_1"))
	if err != nil {
		t.Fatalf("different body must not hit the negative cache: %v", err)
	}

	// 不同 API Key（同 body 同出口——来源键 HMAC 含 apiKeyID，全部来源档位
	// 都随身份隔离）同 body 不命中。
	fourthCtx := requestFastExhaustContext()
	fourthInput := negativeCacheGatewayInput(t, dispatcher, negativeCacheRequest(t, body, "10.0.0.1"), &failureDispatchAuditSink{}, "acc_1")
	fourthInput.UsageContext.APIKeyID = "key_other"
	_, err = dispatcher.HandleFailedUpstreamResponse(fourthCtx, fourthInput)
	if err != nil {
		t.Fatalf("different source session must not hit the negative cache: %v", err)
	}
}

// TestChainFailureDispatcherNegativeCacheIneligibleSourceSkipped：来源键缺失
// （UsageContext 无 APIKeyID → missing）时不写不查——两次调用都走完整 L1。
func TestChainFailureDispatcherNegativeCacheIneligibleSourceSkipped(t *testing.T) {
	dispatcher, cache, _, _ := newNegativeCacheDispatcher(t)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`

	trigger := func() {
		ctx := requestFastExhaustContext()
		for i := 0; i < requestFastExhaustMinTotalAttempts; i++ {
			req := negativeCacheRequest(t, body, "10.0.0.1")
			account := "acc_1"
			if i > 0 {
				account = "acc_2"
			}
			if i > 3 {
				account = "acc_3"
			}
			input := negativeCacheGatewayInput(t, dispatcher, req, &failureDispatchAuditSink{}, account)
			input.UsageContext.APIKeyID = "" // 来源身份缺失
			if _, err := dispatcher.HandleFailedUpstreamResponse(ctx, input); err != nil && i < requestFastExhaustMinTotalAttempts-1 {
				t.Fatalf("attempt %d must not abort: %v", i+1, err)
			}
		}
	}
	trigger()
	if cache.lenForTest() != 0 {
		t.Fatalf("ineligible source must not write: %d", cache.lenForTest())
	}
	trigger()
	// 不写必然不命中：第二次调用的第 5 次才触发（这里断言前 4 次不中止由
	// trigger 内部保证）。
}

// TestChainFailureDispatcherNegativeCacheOversizedBodySkipped：body 原始字节
// 超过审计内联上限（1MB）不参与 L2——不写不查，L1 不受影响。
func TestChainFailureDispatcherNegativeCacheOversizedBodySkipped(t *testing.T) {
	dispatcher, cache, _, _ := newNegativeCacheDispatcher(t)
	oversized := `{"model":"gpt-test","pad":"` + strings.Repeat("x", gatewayusage.AuditInlineSha256MaxBytes) + `"}`

	ctx := requestFastExhaustContext()
	for i := 0; i < requestFastExhaustMinTotalAttempts; i++ {
		req := negativeCacheRequest(t, oversized, "10.0.0.1")
		account := "acc_1"
		if i > 0 {
			account = "acc_2"
		}
		if i > 3 {
			account = "acc_3"
		}
		_, err := dispatcher.HandleFailedUpstreamResponse(ctx,
			negativeCacheGatewayInput(t, dispatcher, req, &failureDispatchAuditSink{}, account))
		if i < requestFastExhaustMinTotalAttempts-1 && err != nil {
			t.Fatalf("oversized-body attempt %d must not abort: %v", i+1, err)
		}
	}
	if cache.lenForTest() != 0 {
		t.Fatalf("oversized body must not write: %d", cache.lenForTest())
	}
}

// TestChainRequestNegativeCacheFingerprintShape：缓存键由 clientSourceKey 与
// 审计同源指纹聚合（请求内记忆化：同请求两次解析返回同键且只计算一次）。
func TestChainRequestNegativeCacheFingerprintShape(t *testing.T) {
	dispatcher, _, _, _ := newNegativeCacheDispatcher(t)
	state := requestFastExhaustStateFromContext(requestFastExhaustContext())
	req := negativeCacheRequest(t, `{"model":"gpt-test"}`, "10.0.0.1")
	input := negativeCacheGatewayInput(t, dispatcher, req, &failureDispatchAuditSink{}, "acc_1")

	key1, eligible1, first1 := state.negativeCacheKey(func() (string, bool) {
		return dispatcher.requestNegativeCacheKey(input)
	})
	key2, _, first2 := state.negativeCacheKey(func() (string, bool) {
		t.Fatal("resolver must run at most once per request")
		return "", false
	})
	if !eligible1 || !first1 || first2 {
		t.Fatalf("first resolution wrong: eligible=%v first1=%v first2=%v", eligible1, first1, first2)
	}
	if key1 == "" || key1 != key2 {
		t.Fatalf("memoized key mismatch: %q vs %q", key1, key2)
	}
	if !strings.Contains(key1, "src_v1_") {
		t.Fatalf("cache key must embed the HMAC clientSourceKey: %q", key1)
	}
	// 指纹分量与审计同源构造逐字节一致：同输入的 auditlog.RequestFingerprint
	// 重算值即键的指纹段。
	fingerprint := auditlog.RequestFingerprint(auditlog.RequestFingerprintInput{
		Method:     "POST",
		Path:       req.Path(),
		Model:      "gpt-test",
		Stream:     false,
		BodySha256: gatewayusage.BodySha256Hex(req.Body.RawBody),
	})
	if !strings.HasSuffix(key1, "\x1f"+fingerprint) {
		t.Fatalf("cache key must end with the audit-consistent request fingerprint:\nkey=%q\nfp=%q", key1, fingerprint)
	}
}
